#!/bin/sh
# install.sh — Standalone installer for wipnote binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/install.sh | sh
#
# With a specific version:
#   curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/install.sh | sh -s -- --version 0.35.0
#
# With a custom install directory:
#   curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/install.sh | sh -s -- --install-dir /usr/local/bin
#
# Options:
#   --version <ver>      Install a specific version (e.g. 0.35.0)
#   --install-dir <dir>  Install to a custom directory (default: ~/.local/bin)
#
# Design:
#   - POSIX sh (no bash-isms)
#   - curl first, wget fallback
#   - Checksum verification when sha256sum/shasum available
#   - Idempotent: skip download if already on the requested version
#   - Optional cosign signature verification when cosign is installed

set -e

REPO="shakestzd/wipnote"
DEFAULT_INSTALL_DIR="${HOME}/.local/bin"
BINARY_NAME="wipnote"
# Plugin tree from the release tarball is laid out under this directory.
# Phase B will flip `wipnote claude` to load from here via --plugin-dir.
DEFAULT_DATA_DIR="${HOME}/.local/share/wipnote"

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------
log_info() {
    printf '[wipnote-install] %s\n' "$*"
}

log_err() {
    printf '[wipnote-install] ERROR: %s\n' "$*" >&2
}

die() {
    log_err "$*"
    exit 1
}

# ---------------------------------------------------------------------------
# Help message
# ---------------------------------------------------------------------------
show_help() {
    cat <<EOF
wipnote installer — Download and install the wipnote binary.

Usage:
  install.sh [OPTIONS]

  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/install.sh | sh

Options:
  --version <ver>      Install a specific version (e.g. 0.38.0)
  --install-dir <dir>  Install to a custom directory (default: ~/.local/bin)
  --help               Show this help message

Examples:
  # Install latest version
  sh install.sh

  # Install specific version
  sh install.sh --version 0.38.0

  # Custom install directory
  sh install.sh --install-dir /usr/local/bin

EOF
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
VERSION=""
INSTALL_DIR="${DEFAULT_INSTALL_DIR}"

while [ $# -gt 0 ]; do
    case "$1" in
        --help)
            show_help
            exit 0
            ;;
        --version)
            [ $# -ge 2 ] || die "--version requires an argument"
            VERSION="$2"
            shift 2
            ;;
        --version=*)
            VERSION="${1#--version=}"
            shift
            ;;
        --install-dir)
            [ $# -ge 2 ] || die "--install-dir requires an argument"
            INSTALL_DIR="$2"
            shift 2
            ;;
        --install-dir=*)
            INSTALL_DIR="${1#--install-dir=}"
            shift
            ;;
        *)
            die "Unknown argument: $1"
            ;;
    esac
done

# ---------------------------------------------------------------------------
# HTTP helper: try curl, fall back to wget
# ---------------------------------------------------------------------------
http_get() {
    _url="$1"
    _dest="$2"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${_dest}" "${_url}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "${_dest}" "${_url}"
    else
        die "Neither curl nor wget found. Please install one and try again."
    fi
}

http_get_stdout() {
    _url="$1"

    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "${_url}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O - "${_url}"
    else
        die "Neither curl nor wget found. Please install one and try again."
    fi
}

# ---------------------------------------------------------------------------
# Detect OS and architecture
# ---------------------------------------------------------------------------
detect_platform() {
    _raw_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    _raw_arch="$(uname -m)"

    case "${_raw_os}" in
        darwin) PLATFORM_OS="darwin" ;;
        linux)  PLATFORM_OS="linux"  ;;
        *)      die "Unsupported OS: ${_raw_os}" ;;
    esac

    case "${_raw_arch}" in
        x86_64|amd64)  PLATFORM_ARCH="amd64" ;;
        arm64|aarch64) PLATFORM_ARCH="arm64" ;;
        *)             die "Unsupported architecture: ${_raw_arch}" ;;
    esac
}

# ---------------------------------------------------------------------------
# Resolve latest v* release version from GitHub API
# ---------------------------------------------------------------------------
resolve_latest_version() {
    log_info "Fetching latest release version..."

    _api_url="https://api.github.com/repos/${REPO}/releases"
    _response="$(http_get_stdout "${_api_url}" 2>/dev/null)" || die "Failed to fetch releases from GitHub API."

    # Extract the first tag_name that starts with v (semantic versioning)
    # Skip pre-releases and drafts by looking at the first non-prerelease tag
    _tag="$(printf '%s' "${_response}" | grep -o '"tag_name": "v[0-9][^"]*"' | head -1 | grep -o 'v[0-9][^"]*')"

    if [ -z "${_tag}" ]; then
        die "Could not determine latest version. No v* release found."
    fi

    # Strip "v" prefix to get bare semver (e.g. 0.35.0)
    VERSION="${_tag#v}"
    log_info "Latest version: ${VERSION}"
}

# ---------------------------------------------------------------------------
# Verify checksum if sha256sum or shasum is available
# ---------------------------------------------------------------------------
verify_checksum() {
    _tarball="$1"
    _version="$2"
    _checksums_url="https://github.com/${REPO}/releases/download/v${_version}/wipnote_${_version}_checksums.txt"
    _tmpfile="${_tarball}.checksums.txt"

    if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
        log_info "Checksum tool not found; skipping verification."
        return 0
    fi

    log_info "Downloading checksums file..."
    if ! http_get "${_checksums_url}" "${_tmpfile}" 2>/dev/null; then
        log_info "Checksums file unavailable; skipping verification."
        return 0
    fi

    _archive_name="wipnote_${_version}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
    _expected="$(grep "${_archive_name}" "${_tmpfile}" | awk '{print $1}')"

    if [ -z "${_expected}" ]; then
        log_info "No checksum entry for ${_archive_name}; skipping verification."
        return 0
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        _actual="$(sha256sum "${_tarball}" | awk '{print $1}')"
    else
        # shasum -a 256 (macOS)
        _actual="$(shasum -a 256 "${_tarball}" | awk '{print $1}')"
    fi

    if [ "${_actual}" != "${_expected}" ]; then
        die "Checksum mismatch for ${_archive_name}. Expected ${_expected}, got ${_actual}."
    fi

    log_info "Checksum verified."

    # Optional cosign keyless signature verification of the checksums file.
    # Soft-fails (warns and continues) so users without cosign keep working.
    verify_signature "${_tmpfile}" "${_version}"
}

# ---------------------------------------------------------------------------
# Optional cosign keyless signature verification of the checksums file.
# Verifies that the checksums file was signed by the wipnote GitHub Actions
# release workflow via Sigstore's transparency log. Soft-fails when cosign
# is not installed — SHA256 checksum verification remains the default.
# ---------------------------------------------------------------------------
verify_signature() {
    _checksums_file="$1"
    _version="$2"

    if ! command -v cosign >/dev/null 2>&1; then
        log_info "cosign not installed; skipping signature verification."
        log_info "  (Optional: install cosign for stronger provenance — https://docs.sigstore.dev/cosign/installation/ )"
        return 0
    fi

    _sig_url="https://github.com/${REPO}/releases/download/v${_version}/wipnote_${_version}_checksums.txt.sig"
    _cert_url="https://github.com/${REPO}/releases/download/v${_version}/wipnote_${_version}_checksums.txt.pem"
    _sig_file="${_checksums_file}.sig"
    _cert_file="${_checksums_file}.pem"

    log_info "Downloading cosign signature and certificate..."
    if ! http_get "${_sig_url}" "${_sig_file}" 2>/dev/null; then
        log_info "Signature file unavailable for v${_version}; skipping cosign verification."
        return 0
    fi
    if ! http_get "${_cert_url}" "${_cert_file}" 2>/dev/null; then
        log_info "Certificate file unavailable for v${_version}; skipping cosign verification."
        return 0
    fi

    log_info "Verifying cosign signature..."
    # NOTE: identity-regexp matches any workflow ref under shakestzd/wipnote/.
    # When the release workflow path is known to be stable, tighten this to:
    #   --certificate-identity-regexp '^https://github.com/shakestzd/wipnote/\.github/workflows/release-go\.yml@'
    if ! cosign verify-blob \
        --certificate "${_cert_file}" \
        --signature "${_sig_file}" \
        --certificate-identity-regexp '^https://github.com/shakestzd/wipnote/' \
        --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
        "${_checksums_file}" >/dev/null 2>&1; then
        die "cosign signature verification failed for checksums file."
    fi

    log_info "Signature verified (cosign keyless, GitHub OIDC)."
}

# ---------------------------------------------------------------------------
# Check if installed version already matches the target
# ---------------------------------------------------------------------------
is_already_installed() {
    _target_bin="${INSTALL_DIR}/${BINARY_NAME}"

    [ -x "${_target_bin}" ] || return 1

    _installed_ver="$("${_target_bin}" version 2>/dev/null | grep -o '[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*' | head -1 || true)"

    [ "${_installed_ver}" = "${VERSION}" ]
}

# ---------------------------------------------------------------------------
# Download and install the binary
# ---------------------------------------------------------------------------
download_and_install() {
    _archive="wipnote_${VERSION}_${PLATFORM_OS}_${PLATFORM_ARCH}.tar.gz"
    _url="https://github.com/${REPO}/releases/download/v${VERSION}/${_archive}"

    log_info "Downloading ${_archive}..."

    _tmpdir="$(mktemp -d)"
    # Ensure tmpdir is cleaned up on exit (normal or error)
    trap 'rm -rf "${_tmpdir}"' EXIT

    _tarball="${_tmpdir}/${_archive}"

    if ! http_get "${_url}" "${_tarball}"; then
        die "Download failed: ${_url}"
    fi

    verify_checksum "${_tarball}" "${VERSION}"

    log_info "Extracting archive..."
    if ! tar xzf "${_tarball}" -C "${_tmpdir}"; then
        die "Failed to extract archive."
    fi

    if [ ! -f "${_tmpdir}/${BINARY_NAME}" ]; then
        die "Binary not found in archive (expected '${BINARY_NAME}')."
    fi

    mkdir -p "${INSTALL_DIR}"
    mv "${_tmpdir}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

    ln -sfn "${INSTALL_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/wn"
    log_info "Created alias: wn -> wipnote"

    # Lay down the bundled plugin tree at ~/.local/share/wipnote/plugin so
    # the binary and its plugin assets stay in lockstep. Releases >= v0.59
    # ship plugin/ at the archive root; older releases simply skip this step.
    if [ -d "${_tmpdir}/plugin" ]; then
        mkdir -p "${DEFAULT_DATA_DIR}"
        rm -rf "${DEFAULT_DATA_DIR}/plugin"
        mv "${_tmpdir}/plugin" "${DEFAULT_DATA_DIR}/plugin"
        log_info "Installed plugin tree to ${DEFAULT_DATA_DIR}/plugin"
    fi

    # Same pattern for the Codex CLI marketplace tree. Releases >= v0.59 ship
    # codex-marketplace/ at the archive root; older releases simply skip.
    if [ -d "${_tmpdir}/codex-marketplace" ]; then
        mkdir -p "${DEFAULT_DATA_DIR}"
        rm -rf "${DEFAULT_DATA_DIR}/codex-marketplace"
        mv "${_tmpdir}/codex-marketplace" "${DEFAULT_DATA_DIR}/codex-marketplace"
        log_info "Installed codex-marketplace tree to ${DEFAULT_DATA_DIR}/codex-marketplace"
    fi

    # Same pattern for the Gemini CLI extension tree. Releases >= v0.59 ship
    # gemini-extension/ at the archive root; older releases simply skip.
    if [ -d "${_tmpdir}/gemini-extension" ]; then
        mkdir -p "${DEFAULT_DATA_DIR}"
        rm -rf "${DEFAULT_DATA_DIR}/gemini-extension"
        mv "${_tmpdir}/gemini-extension" "${DEFAULT_DATA_DIR}/gemini-extension"
        log_info "Installed gemini-extension tree to ${DEFAULT_DATA_DIR}/gemini-extension"
    fi

    # Same pattern for the Antigravity extension tree. Releases with
    # antigravity-extension/ at the archive root install it for bundled init.
    if [ -d "${_tmpdir}/antigravity-extension" ]; then
        mkdir -p "${DEFAULT_DATA_DIR}"
        rm -rf "${DEFAULT_DATA_DIR}/antigravity-extension"
        mv "${_tmpdir}/antigravity-extension" "${DEFAULT_DATA_DIR}/antigravity-extension"
        log_info "Installed antigravity-extension tree to ${DEFAULT_DATA_DIR}/antigravity-extension"
    fi

    log_info "Installed ${BINARY_NAME} to ${INSTALL_DIR}/${BINARY_NAME}"
}

# ---------------------------------------------------------------------------
# PATH check
# ---------------------------------------------------------------------------
check_path() {
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*)
            # Already on PATH — nothing to do
            ;;
        *)
            printf '\n'
            log_info "NOTE: ${INSTALL_DIR} is not in your PATH."
            log_info "Add it by appending one of the following to your shell config:"
            printf '\n'
            printf '  # bash (~/.bashrc or ~/.bash_profile):\n'
            printf '  export PATH="${HOME}/.local/bin:${PATH}"\n'
            printf '\n'
            printf '  # zsh (~/.zshrc):\n'
            printf '  export PATH="${HOME}/.local/bin:${PATH}"\n'
            printf '\n'
            printf '  # fish (~/.config/fish/config.fish):\n'
            printf '  fish_add_path ~/.local/bin\n'
            printf '\n'
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
    detect_platform

    if [ -z "${VERSION}" ]; then
        resolve_latest_version
    fi

    log_info "Installing wipnote v${VERSION} (${PLATFORM_OS}/${PLATFORM_ARCH}) to ${INSTALL_DIR}..."

    if is_already_installed; then
        log_info "wipnote v${VERSION} is already installed at ${INSTALL_DIR}/${BINARY_NAME}. Nothing to do."
        exit 0
    fi

    download_and_install

    log_info "Verifying installation..."
    if ! "${INSTALL_DIR}/${BINARY_NAME}" version >/dev/null 2>&1; then
        die "Installed binary failed to run. Check ${INSTALL_DIR}/${BINARY_NAME}."
    fi

    _installed_ver="$("${INSTALL_DIR}/${BINARY_NAME}" version 2>/dev/null | tr -s ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "(unknown)")"
    log_info "Successfully installed wipnote ${_installed_ver}"

    check_path
}

main "$@"
