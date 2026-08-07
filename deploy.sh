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

cd "$(dirname "$0")"

print_step "Locating the installed loom"
if TARGET=$(command -v loom 2>/dev/null); then
    print_success "$TARGET"
else
    TARGET="$(go env GOPATH)/bin/loom"
    print_success "$TARGET (first install)"
fi

# Snapshot before the new binary can touch the catalog, using the binary that
# wrote it. VACUUM INTO reads through one transaction, so the daemon keeps
# serving and a running scan is not disturbed.
if [ -x "$TARGET" ]; then
    print_step "Snapshotting the catalog"
    SNAPSHOT=$("$TARGET" backup)
    print_success "$SNAPSHOT"
fi

print_step "Building"
BUILD=$(mktemp)
trap 'rm -f "$BUILD"' EXIT
CGO_ENABLED=0 go build -trimpath -o "$BUILD" ./cmd/loom
print_success "built $(git rev-parse --short HEAD 2>/dev/null || echo 'working tree')"

# A schema change migrates on startup, so the daemon has to be down before the
# new binary lands rather than restarted after it.
print_step "Stopping the daemon"
if [ -x "$TARGET" ]; then
    "$TARGET" stop
else
    echo "   nothing installed yet"
fi

print_step "Installing"
mkdir -p "$(dirname "$TARGET")"
if [ -x "$TARGET" ]; then
    cp "$TARGET" "$TARGET.previous"
    echo "   previous binary kept at $TARGET.previous"
fi
cp "$BUILD" "$TARGET"
print_success "installed $TARGET"

print_step "Starting the daemon"
"$TARGET" start

print_step "Status"
"$TARGET" status

echo -e "\n${GREEN}Deployed${NC}"
echo "After a schema change, verify the migration before trusting this:"
echo "  loom status, PRAGMA user_version, PRAGMA foreign_key_check,"
echo "  playback state, and manual artwork selections. See AGENTS.md."
echo "A re-probe of existing files needs a scan: loom scan"
