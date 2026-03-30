#!/usr/bin/env bash
# build.sh - Build the htmlgraph Go binary for the go-plugin.
#
# Usage:
#   ./build.sh          # Dev mode: binary at hooks/bin/htmlgraph
#   ./build.sh --dist   # Dist mode: binary at hooks/bin/htmlgraph-bin,
#                        #            bootstrap script at hooks/bin/htmlgraph

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_DIR="${SCRIPT_DIR}/../go"
BIN_DIR="${SCRIPT_DIR}/hooks/bin"
DIST_MODE=false

for arg in "$@"; do
    case "${arg}" in
        --dist) DIST_MODE=true ;;
        *)      echo "Unknown flag: ${arg}" >&2; exit 1 ;;
    esac
done

cd "${GO_DIR}"
VERSION_RAW=$(git describe --tags --always 2>/dev/null || echo "dev")
# Strip leading 'v' for consistent version strings (goreleaser, plugin.json)
VERSION="${VERSION_RAW#v}"

if [ "${DIST_MODE}" = true ]; then
    echo "Building htmlgraph (dist mode, version: ${VERSION})..."
    go build -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${BIN_DIR}/htmlgraph-bin" ./cmd/htmlgraph/
    chmod +x "${BIN_DIR}/htmlgraph-bin"

    # Copy bootstrap script as the entry point
    cp "${BIN_DIR}/bootstrap.sh" "${BIN_DIR}/htmlgraph"
    chmod +x "${BIN_DIR}/htmlgraph"

    # Install to ~/.local/bin so bootstrap skips download
    INSTALL_DIR="${HOME}/.local/bin"
    META_DIR="${HOME}/.local/share/htmlgraph"
    mkdir -p "${INSTALL_DIR}" "${META_DIR}"
    cp "${BIN_DIR}/htmlgraph-bin" "${INSTALL_DIR}/htmlgraph"
    chmod +x "${INSTALL_DIR}/htmlgraph"
    echo "${VERSION}" > "${META_DIR}/.binary-version"

    echo "Dist build complete:"
    echo "  Entry point: packages/go-plugin/hooks/bin/htmlgraph (bootstrap)"
    echo "  Binary:      packages/go-plugin/hooks/bin/htmlgraph-bin"
    echo "  Installed:   ${INSTALL_DIR}/htmlgraph (v${VERSION})"
else
    echo "Building htmlgraph (dev mode, version: ${VERSION})..."
    go build -ldflags "-s -w -X main.version=${VERSION}" \
        -o "${BIN_DIR}/htmlgraph-dev" ./cmd/htmlgraph/
    chmod +x "${BIN_DIR}/htmlgraph-dev"
    echo "Built: packages/go-plugin/hooks/bin/htmlgraph-dev"

    # Install to ~/.local/bin via symlink so resolvePluginDir() can walk back
    INSTALL_DIR="${HOME}/.local/bin"
    META_DIR="${HOME}/.local/share/htmlgraph"
    mkdir -p "${INSTALL_DIR}" "${META_DIR}"
    rm -f "${INSTALL_DIR}/htmlgraph"
    ln -s "${BIN_DIR}/htmlgraph-dev" "${INSTALL_DIR}/htmlgraph"
    echo "${VERSION}" > "${META_DIR}/.binary-version"
    echo "Installed: ${INSTALL_DIR}/htmlgraph -> ${BIN_DIR}/htmlgraph-dev (v${VERSION})"
fi
