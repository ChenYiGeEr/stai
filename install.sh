#!/usr/bin/env bash
set -euo pipefail

# Install stai — AI companion for SourceTree git workflows.
# Pinned to v0.1.0. Safe to re-run.

VERSION="v0.1.0"
REPO="github.com/chenyiGeEr/stai"

DRY_RUN=false
if [ "${1:-}" = "--dry-run" ] || [ "${1:-}" = "-n" ]; then
  DRY_RUN=true
fi

# Colors (only when stdout is a terminal)
RED=''
GREEN=''
YELLOW=''
RESET=''
if [ -t 1 ]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  RESET='\033[0m'
fi

log()  { echo -e "${GREEN}[stai]${RESET} $*"; }
warn() { echo -e "${YELLOW}[stai]${RESET} $*"; }
err()  { echo -e "${RED}[stai]${RESET} $*" >&2; }

# Check Go installation and version.
if ! command -v go >/dev/null 2>&1; then
  err "Go is not installed. stai requires Go 1.23+."
  err "Install Go from https://go.dev/dl/ or via your package manager, then re-run this installer."
  exit 1
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/^go//')
REQUIRED="1.23.0"
if [ "$(printf '%s\n' "$REQUIRED" "$GO_VERSION" | sort -V | head -n1)" != "$REQUIRED" ]; then
  err "Go $GO_VERSION is too old. stai requires Go 1.23+."
  exit 1
fi

# Find a writable directory that is already in PATH.
# Prefer user-local directories so sudo is not required.
find_install_dir() {
  local dirs=("$HOME/.local/bin" "$HOME/bin" "/usr/local/bin")
  for d in "${dirs[@]}"; do
    case ":$PATH:" in
      *":$d:"*)
        if [ -d "$d" ] && [ -w "$d" ]; then
          echo "$d"
          return 0
        fi
        if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then
          echo "$d"
          return 0
        fi
        ;;
    esac
  done
  return 1
}

INSTALL_DIR=$(find_install_dir) || {
  err "Could not find a writable directory in PATH to install stai."
  err "Tried: ~/.local/bin, ~/bin, /usr/local/bin"
  err "Add one of these to your PATH, or run this script from a directory you can write to."
  exit 1
}

if $DRY_RUN; then
  log "[dry-run] Would install stai $VERSION to $INSTALL_DIR/stai"
  log "[dry-run] Would run: GOBIN=$INSTALL_DIR go install $REPO/cmd/stai@$VERSION"
  exit 0
fi

log "Installing stai $VERSION to $INSTALL_DIR ..."
GOBIN="$INSTALL_DIR" go install "$REPO/cmd/stai@$VERSION"

if [ ! -f "$INSTALL_DIR/stai" ]; then
  err "Installation failed: $INSTALL_DIR/stai was not created."
  err "Make sure your Go install path matches the selected directory."
  exit 1
fi

log "stai $VERSION installed to $INSTALL_DIR/stai"
log "Run 'stai doctor' to verify the setup."
log "Run 'stai install' in a git repository to set up hooks and SourceTree custom actions."
