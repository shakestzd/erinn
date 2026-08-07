#!/bin/sh
# bootstrap.sh - Lightweight bootstrap for wipnote Go binary.
#
# In the distributed plugin, this script IS named "wipnote".
# On first run it downloads the correct platform binary from GitHub Releases,
# verifies its SHA256 against the published checksums file, then exec's into
# it. Subsequent runs simply exec the cached binary after a fast (~1 ms)
# version check.
#
# Install location: ~/.local/bin/wipnote — shared by plugin bootstrap,
# curl install script, Homebrew, and setup-cli. Metadata (version tracking)
# lives at ~/.local/share/wipnote/.
#
# Design constraints:
#   - POSIX sh (no bash-isms)
#   - Dependencies: curl, tar, shasum or sha256sum (all standard on
#     macOS + Linux). No wget fallback.
#   - Never blocks Claude Code: on error, prints {} to stdout and exits 0
#   - Stdin passthrough via exec (CloudEvent JSON piped by hooks)

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Install the binary to ~/.local/bin so it's on PATH for both plugin and
# standalone users. Metadata (version file) lives in ~/.local/share/wipnote.
INSTALL_DIR="${HOME}/.local/bin"
BINARY="${INSTALL_DIR}/wipnote"
META_DIR="${HOME}/.local/share/wipnote"
VERSION_FILE="${META_DIR}/.binary-version"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log_err() {
    echo "[wipnote] $*" >&2
}

# bail outputs {} to stdout (so Claude Code sees valid JSON) and exits 0.
# Load-bearing: any stderr-only failure would surface as "hook error" in
# Claude Code's UI.
bail() {
    echo "{}"
    exit 0
}

# sha256_of prints the SHA256 hex digest of $1 to stdout.
# Uses shasum (macOS default) or sha256sum (Linux default).
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        log_err "Neither sha256sum nor shasum found; cannot verify checksum."
        bail
    fi
}

# ---------------------------------------------------------------------------
# Resolve expected version from plugin.json
# ---------------------------------------------------------------------------
resolve_version() {
    plugin_json=""

    # CLAUDE_PLUGIN_ROOT is set by Claude Code at hook invocation time.
    if [ -n "${CLAUDE_PLUGIN_ROOT:-}" ]; then
        plugin_json="${CLAUDE_PLUGIN_ROOT}/.claude-plugin/plugin.json"
    fi

    # Fallback: walk up from script dir (hooks/bin -> hooks -> plugin root)
    if [ -z "${plugin_json}" ] || [ ! -f "${plugin_json}" ]; then
        plugin_json="${SCRIPT_DIR}/../../.claude-plugin/plugin.json"
    fi

    if [ ! -f "${plugin_json}" ]; then
        # Third fallback: explicit env var (for CI / pinning).
        if [ -n "${WIPNOTE_VERSION:-}" ]; then
            echo "${WIPNOTE_VERSION}"
            return
        fi

        # Fourth fallback: query GitHub releases API for the latest tag.
        # Wrapped in a 5-second timeout; silently ignored if unavailable.
        _api_url="https://api.github.com/repos/shakestzd/wipnote/releases/latest"
        _tag=""
        if command -v curl >/dev/null 2>&1; then
            _tag="$(curl -fsSL --max-time 5 "${_api_url}" 2>/dev/null \
                | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' \
                | head -1 || true)"
        fi
        echo "${_tag:-}"
        return
    fi

    # Extract "version": "X.Y.Z" without jq — portable sed.
    sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "${plugin_json}" | head -1
}

# ---------------------------------------------------------------------------
# Detect OS and architecture, map to goreleaser archive names
# ---------------------------------------------------------------------------
detect_platform() {
    _os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    _arch="$(uname -m)"

    case "${_os}" in
        darwin) PLATFORM_OS="darwin" ;;
        linux)  PLATFORM_OS="linux"  ;;
        *)
            log_err "Unsupported OS: ${_os}"
            bail
            ;;
    esac

    case "${_arch}" in
        x86_64|amd64)   PLATFORM_ARCH="amd64" ;;
        arm64|aarch64)  PLATFORM_ARCH="arm64" ;;
        *)
            log_err "Unsupported architecture: ${_arch}"
            bail
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Download binary from GitHub Releases (with SHA256 verification)
# ---------------------------------------------------------------------------
download_binary() {
    _version="$1"
    _archive="wipnote_${_version}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
    _checksums="wipnote_${_version}_checksums.txt"
    _base="https://github.com/shakestzd/wipnote/releases/download/v${_version}"

    log_err "Downloading binary v${_version} for ${PLATFORM_OS}/${PLATFORM_ARCH}..."

    if ! command -v curl >/dev/null 2>&1; then
        log_err "curl not found. Cannot download binary."
        bail
    fi

    mkdir -p "${INSTALL_DIR}"
    mkdir -p "${META_DIR}"
    _tmpdir="$(mktemp -d)"
    _tarball="${_tmpdir}/${_archive}"
    _sumfile="${_tmpdir}/${_checksums}"

    if ! curl -fsSL -o "${_tarball}" "${_base}/${_archive}" 2>/dev/null; then
        rm -rf "${_tmpdir}"
        log_err "Download failed: ${_base}/${_archive}"
        bail
    fi

    # Fetch the checksums manifest. A failure here is fatal — we never skip
    # verification silently.
    if ! curl -fsSL -o "${_sumfile}" "${_base}/${_checksums}" 2>/dev/null; then
        rm -rf "${_tmpdir}"
        log_err "Checksum manifest download failed: ${_base}/${_checksums}"
        bail
    fi

    # Extract expected hash for our archive. Format: "<hex>  <filename>".
    _expected="$(awk -v f="${_archive}" '$2 == f {print $1; exit}' "${_sumfile}")"
    if [ -z "${_expected}" ]; then
        rm -rf "${_tmpdir}"
        log_err "Archive ${_archive} not listed in ${_checksums}."
        bail
    fi

    _actual="$(sha256_of "${_tarball}")"
    if [ "${_actual}" != "${_expected}" ]; then
        rm -rf "${_tmpdir}"
        log_err "Checksum mismatch for ${_archive}"
        log_err "  expected: ${_expected}"
        log_err "  actual:   ${_actual}"
        bail
    fi

    # Extract — goreleaser archive contains the binary "wipnote" at root and
    # (since v0.59) the plugin tree under plugin/.
    if ! tar xzf "${_tarball}" -C "${_tmpdir}" 2>/dev/null; then
        rm -rf "${_tmpdir}"
        log_err "Failed to extract archive."
        bail
    fi

    if [ ! -f "${_tmpdir}/wipnote" ]; then
        rm -rf "${_tmpdir}"
        log_err "Binary not found in archive."
        bail
    fi

    mv "${_tmpdir}/wipnote" "${BINARY}"
    chmod +x "${BINARY}"
    echo "${_version}" > "${VERSION_FILE}"

    # Lay down the bundled plugin tree at ~/.local/share/wipnote/plugin so the
    # binary and its plugin assets stay in lockstep. Phase B will flip
    # `wipnote claude` to load from this path via --plugin-dir.
    #
    # We extracted into ${_tmpdir} (not the destination) precisely so we never
    # delete the directory we may currently be running from — bootstrap.sh in
    # the legacy marketplace flow lives at
    #   ~/.claude/plugins/marketplaces/wipnote/plugin/hooks/bin/bootstrap.sh
    # and in the new bundled flow lives at
    #   ~/.local/share/wipnote/plugin/hooks/bin/bootstrap.sh
    # Removing the destination here is fine because tarball contents are
    # already fully extracted to ${_tmpdir} before the swap.
    if [ -d "${_tmpdir}/plugin" ]; then
        rm -rf "${META_DIR}/plugin"
        mv "${_tmpdir}/plugin" "${META_DIR}/plugin"
        log_err "Installed plugin tree v${_version} to ${META_DIR}/plugin."
    fi

    # Same pattern for the Codex CLI marketplace tree. bootstrap.sh runs from
    # the Claude hooks path so Codex sessions don't normally trigger it, but
    # extracting the trees on Claude's first run is harmless (disk cost is
    # negligible) and keeps the layout identical to install.sh.
    if [ -d "${_tmpdir}/codex-marketplace" ]; then
        rm -rf "${META_DIR}/codex-marketplace"
        mv "${_tmpdir}/codex-marketplace" "${META_DIR}/codex-marketplace"
        log_err "Installed codex-marketplace tree v${_version} to ${META_DIR}/codex-marketplace."
    fi

    rm -rf "${_tmpdir}"
    log_err "Installed wipnote v${_version} to ${BINARY}."
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
EXPECTED_VERSION="$(resolve_version)"

if [ -z "${EXPECTED_VERSION}" ]; then
    log_err "Could not determine expected version from plugin.json."
    bail
fi

# ---------------------------------------------------------------------------
# Prefer PATH-installed binary if version matches (Homebrew, go install,
# curl install, dev builds via `wipnote build`).
#
# Version-string contract: this regex assumes `wipnote version` prints the
# semver triple "X.Y.Z" somewhere on its first line. That format is set by
# `versionCmd()` in cmd/wipnote/main.go and injected via -ldflags at build
# time — if that contract changes, this match must change too.
# ---------------------------------------------------------------------------
PATH_BINARY="$(command -v wipnote 2>/dev/null || true)"
if [ -n "${PATH_BINARY}" ]; then
    # Guard: don't exec ourselves (bootstrap is also named "wipnote").
    _real_path="$(cd "$(dirname "${PATH_BINARY}")" && pwd)/$(basename "${PATH_BINARY}")"
    _self_path="${SCRIPT_DIR}/$(basename "$0")"

    if [ "${_real_path}" != "${_self_path}" ]; then
        _path_ver="$("${PATH_BINARY}" version 2>/dev/null | grep -o '[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*' | head -1 || true)"
        if [ "${_path_ver}" = "${EXPECTED_VERSION}" ]; then
            exec "${PATH_BINARY}" "$@"
        fi
    fi
fi

# Fast path: cached binary exists and version matches.
if [ -x "${BINARY}" ] && [ -f "${VERSION_FILE}" ]; then
    CACHED_VERSION="$(cat "${VERSION_FILE}" 2>/dev/null || echo "")"
    if [ "${CACHED_VERSION}" = "${EXPECTED_VERSION}" ]; then
        exec "${BINARY}" "$@"
    fi
fi

# Slow path: download (and verify) the binary, then exec.
detect_platform
download_binary "${EXPECTED_VERSION}"

if [ -x "${BINARY}" ]; then
    exec "${BINARY}" "$@"
fi

log_err "Binary not executable after download."
bail
