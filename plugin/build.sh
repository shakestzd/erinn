#!/usr/bin/env bash
# build.sh - Build the erinn Go binary for the plugin.
#
# Usage:
#   ./build.sh          # Dev mode: binary at ~/.local/bin/erinn
#   ./build.sh --dist   # Dist mode: binary at hooks/bin/erinn-bin,
#                        #            bootstrap script at hooks/bin/erinn

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_DIR="${SCRIPT_DIR}/.."
BIN_DIR="${SCRIPT_DIR}/hooks/bin"
DIST_MODE=false

for arg in "$@"; do
    case "${arg}" in
        --dist) DIST_MODE=true ;;
        *)      echo "Unknown flag: ${arg}" >&2; exit 1 ;;
    esac
done

cd "${GO_DIR}"

# Copy remaining notebook files for embedding (plan prototypes removed in S4 cleanup)
echo "  Copying notebook files for embedding..."
mkdir -p internal/notebook/files
for f in prototypes/*.py; do
    [ -f "$f" ] && cp "$f" internal/notebook/files/
done

VERSION_RAW=$(git describe --tags --always 2>/dev/null || echo "dev")
# Strip leading 'v' for consistent version strings (goreleaser, plugin.json)
VERSION="${VERSION_RAW#v}"

if [ "${DIST_MODE}" = true ]; then
    echo "Building erinn (dist mode, version: ${VERSION})..."
    go build -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${BIN_DIR}/erinn-bin" ./cmd/erinn/
    chmod +x "${BIN_DIR}/erinn-bin"

    # Copy bootstrap script as the entry point
    cp "${BIN_DIR}/bootstrap.sh" "${BIN_DIR}/erinn"
    chmod +x "${BIN_DIR}/erinn"

    # Install to ~/.local/bin so bootstrap skips download
    INSTALL_DIR="${HOME}/.local/bin"
    META_DIR="${HOME}/.local/share/erinn"
    mkdir -p "${INSTALL_DIR}" "${META_DIR}"
    rm -f "${INSTALL_DIR}/erinn"  # Fresh inode avoids macOS signature cache
    cp "${BIN_DIR}/erinn-bin" "${INSTALL_DIR}/erinn"
    chmod +x "${INSTALL_DIR}/erinn"
    echo "${VERSION}" > "${META_DIR}/.binary-version"

    echo "Dist build complete:"
    echo "  Entry point: plugin/hooks/bin/erinn (bootstrap)"
    echo "  Binary:      plugin/hooks/bin/erinn-bin"
    echo "  Installed:   ${INSTALL_DIR}/erinn (v${VERSION})"
else
    echo "Building erinn (dev mode, version: ${VERSION})..."

    # Build directly to ~/.local/bin/erinn — no intermediate artifact.
    # The plugin-has-its-own-binary architecture was removed in commit 5ae76555c;
    # hooks.json now uses bare 'erinn' via PATH lookup.
    INSTALL_DIR="${HOME}/.local/bin"
    META_DIR="${HOME}/.local/share/erinn"
    mkdir -p "${INSTALL_DIR}" "${META_DIR}"
    rm -f "${INSTALL_DIR}/erinn"  # Fresh inode avoids macOS signature cache
    go build -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${INSTALL_DIR}/erinn" ./cmd/erinn/
    chmod +x "${INSTALL_DIR}/erinn"
    if [ ! -e "${INSTALL_DIR}/htmlgraph" ] || [ -L "${INSTALL_DIR}/htmlgraph" ]; then
        ln -sfn "${INSTALL_DIR}/erinn" "${INSTALL_DIR}/htmlgraph"
    else
        echo "Note: ${INSTALL_DIR}/htmlgraph already exists; leaving it untouched."
    fi
    echo "${VERSION}" > "${META_DIR}/.binary-version"
    echo "Installed: ${INSTALL_DIR}/erinn (v${VERSION})"
fi
