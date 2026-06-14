#!/usr/bin/env bash
# wipnote installer
# Usage: curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | bash
#
# Environment variables:
#   WIPNOTE_VERSION    Version to install (default: latest). Example: 0.60.1
#   WIPNOTE_BIN_DIR    Directory to install the binary (default: $HOME/.local/bin)
#   WIPNOTE_SHARE_DIR  Directory to install plugin trees (default: $HOME/.local/share/wipnote)
#
# Examples:
#   Install latest:
#     curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | bash
#
#   Pin to a specific version:
#     curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_VERSION=0.60.1 bash
#
#   Custom install directory:
#     curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_BIN_DIR=$HOME/bin bash
#
#   Custom share directory:
#     curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_SHARE_DIR=$HOME/share/wipnote bash

set -euo pipefail

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  cat <<'EOF'
wipnote installer

USAGE
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_VERSION=0.60.1 bash
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_BIN_DIR=$HOME/bin bash

ENVIRONMENT VARIABLES
  WIPNOTE_VERSION    Version to install. Defaults to "latest" (resolves from GitHub API).
                     Set to a specific semver tag without the leading "v", e.g. "0.60.1".
  WIPNOTE_BIN_DIR    Directory to install the binary. Defaults to "$HOME/.local/bin".
  WIPNOTE_SHARE_DIR  Directory to install plugin trees (plugin/, codex-marketplace/,
                     gemini-extension/). Defaults to "$HOME/.local/share/wipnote".

SUPPORTED PLATFORMS
  darwin_amd64, darwin_arm64, linux_amd64, linux_arm64

  Other platforms (e.g. Windows) must build from source:
    git clone https://github.com/shakestzd/wipnote && cd wipnote && go build -o ~/.local/bin/wipnote ./cmd/wipnote

EXAMPLES
  # Install latest
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | bash

  # Pin to 0.60.1
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_VERSION=0.60.1 bash

  # Install into ~/bin
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_BIN_DIR=$HOME/bin bash

  # Install with custom share directory
  curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_SHARE_DIR=$HOME/share/wipnote bash
EOF
  exit 0
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf '==> %s\n' "$*"; }
warn()  { printf 'WARNING: %s\n' "$*" >&2; }
error() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Platform detection
# ---------------------------------------------------------------------------
info "Detecting platform…"

raw_os=$(uname -s)
raw_arch=$(uname -m)

case "$raw_os" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux"  ;;
  *)
    error "Unsupported OS: $raw_os. Build from source: https://github.com/shakestzd/wipnote"
    ;;
esac

case "$raw_arch" in
  x86_64)          ARCH="amd64" ;;
  arm64|aarch64)   ARCH="arm64" ;;
  *)
    error "Unsupported architecture: $raw_arch. Build from source: https://github.com/shakestzd/wipnote"
    ;;
esac

PLATFORM="${OS}_${ARCH}"

# Verify we publish an asset for this combination
case "$PLATFORM" in
  darwin_amd64|darwin_arm64|linux_amd64|linux_arm64)
    ;;
  *)
    error "No pre-built asset for ${PLATFORM}. Build from source: https://github.com/shakestzd/wipnote"
    ;;
esac

info "Platform: ${PLATFORM}"

# ---------------------------------------------------------------------------
# Resolve version
# ---------------------------------------------------------------------------
WIPNOTE_VERSION="${WIPNOTE_VERSION:-latest}"

if [[ "$WIPNOTE_VERSION" == "latest" ]]; then
  info "Resolving latest release from GitHub…"
  VERSION=$(curl -fsSL "https://api.github.com/repos/shakestzd/wipnote/releases/latest" \
    | grep -m1 '"tag_name"' \
    | sed -E 's/.*"v?([^"]+)".*/\1/')
  if [[ -z "$VERSION" ]]; then
    error "Could not resolve latest version from GitHub API. Check your internet connection or set WIPNOTE_VERSION explicitly."
  fi
else
  # Strip leading 'v' if user included it
  VERSION="${WIPNOTE_VERSION#v}"
fi

info "Installing wipnote v${VERSION}…"

# ---------------------------------------------------------------------------
# Install directories
# ---------------------------------------------------------------------------
WIPNOTE_BIN_DIR="${WIPNOTE_BIN_DIR:-$HOME/.local/bin}"
WIPNOTE_SHARE_DIR="${WIPNOTE_SHARE_DIR:-$HOME/.local/share/wipnote}"

# ---------------------------------------------------------------------------
# Download
# ---------------------------------------------------------------------------
TMPDIR_INSTALL=$(mktemp -d)
trap 'rm -rf "$TMPDIR_INSTALL"' EXIT

TARBALL="wipnote_${VERSION}_${OS}_${ARCH}.tar.gz"
CHECKSUMS="wipnote_${VERSION}_checksums.txt"
BASE_URL="https://github.com/shakestzd/wipnote/releases/download/v${VERSION}"

info "Downloading ${TARBALL}…"
curl -fsSL "${BASE_URL}/${TARBALL}" -o "${TMPDIR_INSTALL}/${TARBALL}"

info "Downloading ${CHECKSUMS}…"
curl -fsSL "${BASE_URL}/${CHECKSUMS}" -o "${TMPDIR_INSTALL}/${CHECKSUMS}"

# ---------------------------------------------------------------------------
# Checksum verification
# ---------------------------------------------------------------------------
info "Verifying checksum…"

if command -v sha256sum >/dev/null 2>&1; then
  EXPECTED=$(grep "${TARBALL}" "${TMPDIR_INSTALL}/${CHECKSUMS}" | awk '{print $1}')
  ACTUAL=$(sha256sum "${TMPDIR_INSTALL}/${TARBALL}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  EXPECTED=$(grep "${TARBALL}" "${TMPDIR_INSTALL}/${CHECKSUMS}" | awk '{print $1}')
  ACTUAL=$(shasum -a 256 "${TMPDIR_INSTALL}/${TARBALL}" | awk '{print $1}')
else
  warn "Neither sha256sum nor shasum is available. Skipping checksum verification."
  warn "To verify manually, download ${CHECKSUMS} from the release page and check the tarball."
  EXPECTED=""
  ACTUAL=""
fi

if [[ -n "$EXPECTED" && -n "$ACTUAL" ]]; then
  if [[ "$EXPECTED" != "$ACTUAL" ]]; then
    error "Checksum mismatch for ${TARBALL}!
  Expected: ${EXPECTED}
  Got:      ${ACTUAL}
Delete the downloaded file and try again, or download manually from:
  ${BASE_URL}/${TARBALL}"
  fi
  info "Checksum verified."
fi

# ---------------------------------------------------------------------------
# Extract + install
# ---------------------------------------------------------------------------
info "Extracting…"
tar -xzf "${TMPDIR_INSTALL}/${TARBALL}" -C "${TMPDIR_INSTALL}"

mkdir -p "${WIPNOTE_BIN_DIR}"
mv "${TMPDIR_INSTALL}/wipnote" "${WIPNOTE_BIN_DIR}/wipnote"
chmod +x "${WIPNOTE_BIN_DIR}/wipnote"

# ---------------------------------------------------------------------------
# Install bundled plugin trees
# ---------------------------------------------------------------------------
mkdir -p "${WIPNOTE_SHARE_DIR}"

for tree in plugin codex-marketplace gemini-extension antigravity-extension; do
  SRC="${TMPDIR_INSTALL}/${tree}"
  DST="${WIPNOTE_SHARE_DIR}/${tree}"

  if [[ -d "$SRC" ]]; then
    # Clean replace on upgrade
    rm -rf "$DST"
    mv "$SRC" "$DST"
    info "Installed plugin tree → ${DST}"
  fi
done

# ---------------------------------------------------------------------------
# macOS Gatekeeper
# ---------------------------------------------------------------------------
if [[ "$OS" == "darwin" ]]; then
  xattr -d com.apple.quarantine "${WIPNOTE_BIN_DIR}/wipnote" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# PATH check (notify only — do NOT mutate shell rc files)
# ---------------------------------------------------------------------------
case ":${PATH}:" in
  *":${WIPNOTE_BIN_DIR}:"*)
    ;;
  *)
    printf '\n'
    warn "${WIPNOTE_BIN_DIR} is not in your PATH."
    printf '    Add it by running:\n'
    # shellcheck disable=SC2016  # $PATH is intentionally literal in this user-facing message
    printf '      export PATH="%s:$PATH"\n' "${WIPNOTE_BIN_DIR}"
    printf '    Or add that line to your shell rc file (~/.zshrc, ~/.bashrc, etc.).\n'
    printf '\n'
    ;;
esac

# ---------------------------------------------------------------------------
# Done
# ---------------------------------------------------------------------------
printf '\n'
info "Installed wipnote v${VERSION}"
info "  Binary  → ${WIPNOTE_BIN_DIR}/wipnote"
info "  Plugins → ${WIPNOTE_SHARE_DIR}"
printf '\n'
"${WIPNOTE_BIN_DIR}/wipnote" version
