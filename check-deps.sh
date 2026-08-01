#!/bin/bash
# Dependency health check for loom.
# Reports reachable vulnerabilities immediately, declared Go module updates after a cooldown,
# and newer CI action tags without changing files.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
NC='\033[0m'

UPDATE_COOLDOWN_DAYS=${UPDATE_COOLDOWN_DAYS:-7}
if ! [[ "$UPDATE_COOLDOWN_DAYS" =~ ^[0-9]+$ ]]; then
    echo -e "${RED}   UPDATE_COOLDOWN_DAYS must be a non-negative integer.${NC}"
    exit 1
fi
UPDATE_COOLDOWN_SECONDS=$((UPDATE_COOLDOWN_DAYS * 24 * 60 * 60))
CURRENT_EPOCH=$(date -u +%s)

print_step() {
    echo -e "\n${BLUE}:: $1${NC}"
}

print_success() {
    echo -e "${GREEN}   $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}   $1${NC}"
}

print_error() {
    echo -e "${RED}   $1${NC}"
}

is_version_tag() {
    [[ "$1" =~ ^v?[0-9]+([.][0-9]+)*$ ]]
}

is_major_version_tag() {
    [[ "$1" =~ ^v?[0-9]+$ ]]
}

version_major() {
    local version=${1#v}
    echo "${version%%.*}"
}

latest_stable_tag() {
    local repo=$1
    local tag_output

    if ! tag_output=$(git ls-remote --tags --refs "$repo" 2>/dev/null); then
        return 1
    fi

    printf '%s\n' "$tag_output" |
        awk -F 'refs/tags/' 'NF > 1 {print $2}' |
        grep -E '^v?[0-9]+([.][0-9]+)*$' |
        sort -V |
        tail -n 1 || true
}

is_newer_version_available() {
    local current=$1
    local latest=$2
    local highest

    if [ -z "$latest" ] || ! is_version_tag "$current"; then
        return 1
    fi

    if is_major_version_tag "$current"; then
        [ "$(version_major "$latest")" -gt "$(version_major "$current")" ]
        return
    fi

    highest=$(printf '%s\n%s\n' "$current" "$latest" | sort -V | tail -n 1)
    [ "$highest" = "$latest" ] && [ "$current" != "$latest" ]
}

module_version_time_epoch() {
    local module=$1
    local version=$2
    local release_time

    if ! release_time=$(GOWORK=off go list -m -json "$module@$version" 2>/dev/null | awk -F '"' '/"Time":/ {print $4; exit}'); then
        return 1
    fi

    if [ -z "$release_time" ]; then
        return 1
    fi

    date -u -d "$release_time" +%s 2>/dev/null
}

is_update_past_cooldown() {
    local module=$1
    local version=$2
    local release_epoch
    local age_seconds

    if [ "$UPDATE_COOLDOWN_DAYS" -eq 0 ]; then
        return 0
    fi

    if ! release_epoch=$(module_version_time_epoch "$module" "$version"); then
        return 0
    fi

    age_seconds=$((CURRENT_EPOCH - release_epoch))
    [ "$age_seconds" -ge "$UPDATE_COOLDOWN_SECONDS" ]
}

if ! command -v go &>/dev/null; then
    print_error "Go is not installed."
    exit 1
fi

print_step "Checking for reachable Go vulnerabilities"
if ! command -v govulncheck &>/dev/null; then
    echo "   Installing govulncheck..."
    go install golang.org/x/vuln/cmd/govulncheck@latest
fi

if GOWORK=off govulncheck ./...; then
    print_success "No reachable vulnerabilities found"
else
    print_error "Reachable vulnerabilities detected"
    exit 1
fi

print_step "Checking for available declared Go module updates"
DECLARED_MODULES=$(GOWORK=off go mod edit -json | awk '
    /"Require": \[/ { in_require = 1; next }
    in_require && /^[[:space:]]*]/ { in_require = 0 }
    in_require && /"Path":/ {
        gsub(/.*"Path": "/, "")
        gsub(/".*/, "")
        print
    }
')
UPDATE_OUTPUT=$(GOWORK=off go list -m -u all)
OUTDATED_OUTPUT=""
COOLDOWN_OUTPUT=""

while IFS= read -r line; do
    if [[ "$line" != *"["* ]]; then
        continue
    fi

    module=$(awk '{print $1}' <<< "$line")
    latest_version=${line##*[}
    latest_version=${latest_version%%]*}

    if ! grep -Fxq "$module" <<< "$DECLARED_MODULES"; then
        continue
    fi

    if is_update_past_cooldown "$module" "$latest_version"; then
        OUTDATED_OUTPUT+="$line"$'\n'
    else
        COOLDOWN_OUTPUT+="$line"$'\n'
    fi
done <<< "$UPDATE_OUTPUT"

OUTDATED_OUTPUT=${OUTDATED_OUTPUT%$'\n'}
COOLDOWN_OUTPUT=${COOLDOWN_OUTPUT%$'\n'}

if [ -n "$OUTDATED_OUTPUT" ]; then
    print_warning "Updates are available:"
    printf '%s\n' "$OUTDATED_OUTPUT" | sed 's/^/   /'
    echo
    echo "   To apply a listed update, run:"
    echo "     go get <module>@latest"
    echo "     go mod tidy"
    echo "     ./check-ci.sh"
else
    if [ "$UPDATE_COOLDOWN_DAYS" -eq 0 ]; then
        print_success "Declared Go modules are up to date"
    else
        print_success "No declared Go module updates older than $UPDATE_COOLDOWN_DAYS days"
    fi
fi

if [ -n "$COOLDOWN_OUTPUT" ]; then
    cooldown_count=$(printf '%s\n' "$COOLDOWN_OUTPUT" | grep -c .)
    print_success "$cooldown_count recent Go module update(s) are in the ${UPDATE_COOLDOWN_DAYS}-day cooldown:"
    printf '%s\n' "$COOLDOWN_OUTPUT" | sed 's/^/   /'
fi

if [ -d .forgejo/workflows ]; then
    print_step "Checking Forgejo Actions dependencies"
    ACTION_REFS=$(grep -RhoE 'uses:[[:space:]]*[^[:space:]]+' .forgejo/workflows 2>/dev/null | sed 's/^uses:[[:space:]]*//' | sort -u || true)
    if [ -n "$ACTION_REFS" ]; then
        if ! command -v git &>/dev/null; then
            printf '%s\n' "$ACTION_REFS" | sed 's/^/   /'
            echo
            print_warning "Git is not installed; skipping action version checks."
        else
            OUTDATED_ACTIONS=0

            while IFS= read -r action_ref; do
                repo=${action_ref%@*}
                current_ref=${action_ref##*@}

                echo "   $action_ref"

                if [[ "$action_ref" != *@* ]]; then
                    echo "      no @ref found; skipping version check"
                    continue
                fi

                if ! latest=$(latest_stable_tag "$repo"); then
                    echo "      could not reach action tags; skipping version check"
                    continue
                fi

                if [ -z "$latest" ]; then
                    echo "      no stable version tags found; skipping version check"
                    continue
                fi

                if is_newer_version_available "$current_ref" "$latest"; then
                    echo "      latest stable tag: $latest; newer version may be available"
                    OUTDATED_ACTIONS=1
                elif is_major_version_tag "$current_ref" && [ "$(version_major "$current_ref")" = "$(version_major "$latest")" ]; then
                    echo "      latest stable tag: $latest; $current_ref tracks the latest major"
                elif is_version_tag "$current_ref"; then
                    echo "      latest stable tag: $latest; no newer stable tag found"
                else
                    echo "      latest stable tag: $latest; current ref is not a version tag"
                fi
            done <<< "$ACTION_REFS"

            if [ "$OUTDATED_ACTIONS" -eq 1 ]; then
                echo
                print_warning "Newer CI action tags may be available; review before updating."
            fi
        fi
    else
        print_success "No action dependencies found"
    fi
fi

echo -e "\n${GREEN}Dependency check complete${NC}"
