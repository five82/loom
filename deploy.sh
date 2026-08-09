#!/bin/bash
# Build the working tree and deploy it over the installed loom.
# Run ./check-ci.sh first; this script does not test what it ships.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

print_step() {
    echo -e "\n${BLUE}:: $1${NC}"
}

print_success() {
    echo -e "${GREEN}   $1${NC}"
}

print_error() {
    echo -e "${RED}   $1${NC}"
}

audit_value() {
    local json=$1 key=$2 value
    value=$(printf '%s\n' "$json" | awk -v field="\"$key\":" '
        $1 == field {
            gsub(/,/, "", $2)
            print $2
            exit
        }
    ')
    if [[ ! $value =~ ^[0-9]+$ ]]; then
        print_error "could not read $key from the catalog audit" >&2
        return 1
    fi
    printf '%s' "$value"
}

deployment_failed() {
    print_error "$1"
    echo "   Loom remains stopped. No restoration was attempted."
    if [ -n "${SNAPSHOT:-}" ]; then
        echo "   Database snapshot: $SNAPSHOT"
    fi
    if [ -n "${PREVIOUS:-}" ]; then
        echo "   Previous binary: $PREVIOUS"
    fi
    exit 1
}

cd "$(dirname "$0")"

print_step "Locating the installed loom"
if TARGET=$(command -v loom 2>/dev/null); then
    print_success "$TARGET"
else
    TARGET="$(go env GOPATH)/bin/loom"
    if [ -x "$TARGET" ]; then
        print_success "$TARGET"
    else
        print_success "$TARGET (first install)"
    fi
fi

print_step "Building"
BUILD=$(mktemp)
trap 'rm -f "$BUILD"' EXIT
CGO_ENABLED=0 go build -trimpath -o "$BUILD" ./cmd/loom
print_success "built $(git rev-parse --short HEAD 2>/dev/null || echo 'working tree')"

PREVIOUS=""
AUDIT_BEFORE=""
SCHEMA_BEFORE=""
PLAYBACK_BEFORE=""
ARTWORK_BEFORE=""

print_step "Stopping the daemon"
if [ -x "$TARGET" ]; then
    "$TARGET" stop

    print_step "Auditing the stopped catalog"
    if ! AUDIT_BEFORE=$("$TARGET" developer audit --json); then
        printf '%s\n' "$AUDIT_BEFORE"
        deployment_failed "pre-deployment audit failed"
    fi
    SCHEMA_BEFORE=$(audit_value "$AUDIT_BEFORE" schema_version)
    # The installed binary from before durable-state counts were introduced
    # cannot report them. The candidate can read that baseline schema, so use it
    # once to bridge the first deployment of the migration framework.
    if ! PLAYBACK_BEFORE=$(audit_value "$AUDIT_BEFORE" playback_state_rows 2>/dev/null) ||
        ! ARTWORK_BEFORE=$(audit_value "$AUDIT_BEFORE" manual_artwork_selections 2>/dev/null); then
        if ! COUNTS_BEFORE=$("$BUILD" developer audit --json); then
            printf '%s\n' "$COUNTS_BEFORE"
            deployment_failed "candidate could not count preserved state before deployment"
        fi
        PLAYBACK_BEFORE=$(audit_value "$COUNTS_BEFORE" playback_state_rows)
        ARTWORK_BEFORE=$(audit_value "$COUNTS_BEFORE" manual_artwork_selections)
    fi
    print_success "schema $SCHEMA_BEFORE; $PLAYBACK_BEFORE playback rows; $ARTWORK_BEFORE manual artwork selections"
else
    echo "   nothing installed yet"
fi

SNAPSHOT=""
if [ -n "$AUDIT_BEFORE" ]; then
    print_step "Snapshotting the stopped catalog"
    if ! SNAPSHOT=$("$BUILD" backup); then
        deployment_failed "catalog snapshot failed"
    fi
    print_success "$SNAPSHOT"
fi

print_step "Installing"
mkdir -p "$(dirname "$TARGET")"
if [ -x "$TARGET" ]; then
    PREVIOUS="$TARGET.previous"
    cp "$TARGET" "$PREVIOUS"
    echo "   previous binary kept at $PREVIOUS"
fi
cp "$BUILD" "$TARGET"
print_success "installed $TARGET"

print_step "Migrating the catalog"
if ! "$TARGET" migrate; then
    deployment_failed "catalog migration failed"
fi

print_step "Auditing the migrated catalog"
if ! AUDIT_AFTER=$("$TARGET" developer audit --json); then
    printf '%s\n' "$AUDIT_AFTER"
    deployment_failed "post-migration audit failed"
fi
SCHEMA_AFTER=$(audit_value "$AUDIT_AFTER" schema_version)
PLAYBACK_AFTER=$(audit_value "$AUDIT_AFTER" playback_state_rows)
ARTWORK_AFTER=$(audit_value "$AUDIT_AFTER" manual_artwork_selections)
if [ -n "$AUDIT_BEFORE" ] &&
    { [ "$PLAYBACK_BEFORE" != "$PLAYBACK_AFTER" ] || [ "$ARTWORK_BEFORE" != "$ARTWORK_AFTER" ]; }; then
    deployment_failed "preserved state changed: playback $PLAYBACK_BEFORE -> $PLAYBACK_AFTER; manual artwork $ARTWORK_BEFORE -> $ARTWORK_AFTER"
fi
print_success "schema $SCHEMA_AFTER; $PLAYBACK_AFTER playback rows; $ARTWORK_AFTER manual artwork selections"

print_step "Starting the daemon"
if ! "$TARGET" start; then
    "$TARGET" stop || true
    deployment_failed "daemon startup failed"
fi

print_step "Status"
if ! "$TARGET" status; then
    "$TARGET" stop || true
    deployment_failed "daemon status check failed"
fi

print_step "Schema"
if [ -z "$SCHEMA_BEFORE" ]; then
    print_success "created at version $SCHEMA_AFTER"
elif [ "$SCHEMA_BEFORE" = "$SCHEMA_AFTER" ]; then
    print_success "$SCHEMA_BEFORE -> $SCHEMA_AFTER (unchanged)"
else
    print_success "$SCHEMA_BEFORE -> $SCHEMA_AFTER (changed)"
fi

if [ -n "$SNAPSHOT" ]; then
    echo "Snapshot: $SNAPSHOT"
fi

echo -e "\n${GREEN}Deployed${NC}"
if [ -n "$SCHEMA_BEFORE" ] && [ "$SCHEMA_BEFORE" != "$SCHEMA_AFTER" ]; then
    echo "The migration passed the catalog audit and preserved-state checks."
    echo "If existing files need to be re-probed, finish the deployment with: loom scan"
fi
