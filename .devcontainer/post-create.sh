#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

retry() {
  local attempts=5
  local delay=2
  local try=1
  local status=0

  while true; do
    "$@" && return 0
    status=$?

    if [ "$try" -ge "$attempts" ]; then
      echo "command failed after ${attempts} attempts: $*" >&2
      return "$status"
    fi

    echo "command failed (exit ${status}); retrying in ${delay}s: $*" >&2
    sleep "$delay"
    try=$((try + 1))
    delay=$((delay * 2))
  done
}

install_script_from_url() {
  local url="$1"
  local tmp

  tmp="$(mktemp)"
  retry curl -fsSL "$url" -o "$tmp"
  sh "$tmp"
  rm -f "$tmp"
}

# Fix ownership of named-volume mount points (Docker creates these as root:root)
sudo chown -R vscode:vscode \
    "${REPO_ROOT}/.wipnote" \
    /home/vscode/.codex \
    /home/vscode/.gemini \
    /home/vscode/.copilot \
    /home/vscode/.roborev \
    2>/dev/null || true

cd "${REPO_ROOT}"

export PATH="${HOME}/.local/bin:${PATH}"

echo "==> Verifying image tools..."
for tool in bwrap tmux rg fd jq sqlite3 shellcheck direnv zsh; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "missing required image tool: $tool" >&2
    exit 1
  }
done

echo "==> Installing AI agent CLIs..."
retry npm install -g --no-fund --no-audit \
    --fetch-timeout=60000 \
    --fetch-retries=1 \
    --fetch-retry-mintimeout=5000 \
    --fetch-retry-maxtimeout=30000 \
    @anthropic-ai/claude-code \
    @google/gemini-cli \
    @openai/codex \
    @github/copilot

echo "==> Building wipnote from source..."
# Bootstrap: raw go build because the wipnote binary does not exist yet.
mkdir -p "${HOME}/.local/bin"
go build -o "${HOME}/.local/bin/wipnote" ./cmd/wipnote/
chmod +x "${HOME}/.local/bin/wipnote"
# Full build: mirrors plugin trees, creates wn symlink, writes .binary-version.
wipnote build

echo "==> Running quality gates..."
go build ./...
go vet ./...

echo "==> Fixing Claude Code plugin data directory permissions..."
sudo chown -R vscode:vscode ~/.claude 2>/dev/null || true
mkdir -p ~/.claude/plugins/data

echo "==> Installing uv..."
if ! command -v uv >/dev/null 2>&1; then
  install_script_from_url https://astral.sh/uv/install.sh
fi

echo "==> Installing mkdocs with material theme and extensions..."
retry uv tool install mkdocs --with mkdocs-material --with pymdown-extensions

echo "==> Installing Oh My Posh..."
if ! command -v oh-my-posh >/dev/null 2>&1; then
  tmp_omp="$(mktemp)"
  retry curl -fsSL https://ohmyposh.dev/install.sh -o "$tmp_omp"
  bash "$tmp_omp" -d "$HOME/.local/bin"
  rm -f "$tmp_omp"
fi

echo "==> Installing ttyd (required by the dashboard terminal launcher)..."
if ! command -v ttyd >/dev/null 2>&1; then
  mkdir -p "$HOME/.local/bin"
  TTYD_VERSION=$(curl -sfL https://api.github.com/repos/tsl0922/ttyd/releases/latest 2>/dev/null \
    | grep -oE '"tag_name": "[^"]+"' | head -1 | cut -d'"' -f4)
  TTYD_VERSION=${TTYD_VERSION:-1.7.7}
  retry curl -fL -o "$HOME/.local/bin/ttyd" \
    "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.$(uname -m)"
  chmod +x "$HOME/.local/bin/ttyd"
fi

echo "==> Installing roborev..."
if ! command -v roborev >/dev/null 2>&1; then
  mkdir -p "$HOME/.local/bin" "$HOME/.roborev"
  tmp_roborev="$(mktemp)"
  retry curl -fsSL https://roborev.io/install.sh -o "$tmp_roborev"
  ROBOREV_INSTALL_DIR="$HOME/.local/bin" bash "$tmp_roborev"
  rm -f "$tmp_roborev"
fi

echo "==> Initializing roborev for this repository..."
mkdir -p "$HOME/.roborev"
roborev init || {
  echo "roborev init failed; continuing because AI agent authentication may not be complete yet" >&2
}

echo "==> Configuring roborev to skip wipnote bookkeeping commits..."
# Idempotent: only inject the guard if feat-1915300d marker is not present
if [ -f "$REPO_ROOT/.git/hooks/post-commit" ] && ! grep -q 'feat-1915300d' "$REPO_ROOT/.git/hooks/post-commit" 2>/dev/null; then
  # Insert the wipnote bookkeeping skip guard before the final roborev invocation
  tmpfile=$(mktemp)
  awk '
    /^"?\$?ROBOREV"? post-commit 2>\/dev\/null$/ && !done {
      print "# wipnote: skip bookkeeping autocommits — they carry no reviewable code content (feat-1915300d)"
      print "case \"$(git log -1 --format='"'"'%s'"'"')\" in"
      print "  wipnote:*) exit 0 ;;"
      print "esac"
      done = 1
    }
    { print }
  ' "$REPO_ROOT/.git/hooks/post-commit" > "$tmpfile"
  mv "$tmpfile" "$REPO_ROOT/.git/hooks/post-commit"
  chmod +x "$REPO_ROOT/.git/hooks/post-commit"
fi

echo "==> Ensuring \$HOME/.local/bin is on PATH in shell rc files..."
for _rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  if [ -f "$_rc" ] && ! grep -q '.local/bin' "$_rc"; then
    # shellcheck disable=SC2016
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$_rc"
  fi
done

echo "==> Installing wipnote + Oh My Posh Claude Code wrapper..."
mkdir -p "$HOME/.claude"
cat > "$HOME/.claude/omp-claude-wrapper.sh" << 'EOF'
#!/bin/bash
# wipnote + Oh My Posh wrapper for Claude Code status line

_dir="$(pwd)"
while [ "$_dir" != "/" ]; do
    [ -d "$_dir/.wipnote" ] && break
    _dir=$(dirname "$_dir")
done
if [ "$_dir" = "/" ]; then
    echo ""
    exit 0
fi

INPUT=$(cat)
SESS_ID=$(echo "$INPUT" | python3 -c "import sys, json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
ACTIVE_WORK=$(wipnote statusline --session "$SESS_ID" 2>/dev/null)
export WIPNOTE_ACTIVE="$ACTIVE_WORK"

OMP_BIN="${WIPNOTE_OMP_BIN:-$(which oh-my-posh 2>/dev/null)}"
OMP_CONFIG="${WIPNOTE_OMP_CONFIG:-$HOME/.claude.omp.json}"

if [ -n "$OMP_BIN" ] && [ -f "$OMP_CONFIG" ]; then
    echo "$INPUT" | "$OMP_BIN" claude --config "$OMP_CONFIG"
else
    echo "$ACTIVE_WORK"
fi
EOF
chmod +x "$HOME/.claude/omp-claude-wrapper.sh"

echo "==> Linking Claude Code Oh My Posh theme..."
# Source of truth lives in .devcontainer/ (tracked in git); symlink into HOME so
# it survives container rebuilds without needing to re-copy each time.
# Use an absolute path — a relative `$(dirname "$0")` symlink resolves from
# $HOME (the symlink's own dir), landing at $HOME/.devcontainer/... which is
# not the repo file.
ln -sf "${SCRIPT_DIR}/claude.omp.json" "$HOME/.claude.omp.json"

echo "==> Installing oh-my-zsh..."
if [ ! -d "$HOME/.oh-my-zsh" ]; then
  tmp_omz="$(mktemp)"
  retry curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh -o "$tmp_omz"
  RUNZSH=no CHSH=no KEEP_ZSHRC=yes sh "$tmp_omz"
  rm -f "$tmp_omz"
fi

echo "==> Installing powerlevel10k..."
if [ ! -d "$HOME/powerlevel10k" ]; then
  retry git clone --depth=1 https://github.com/romkatv/powerlevel10k.git "$HOME/powerlevel10k"
fi

echo "==> Installing zsh plugins..."
ZSH_CUSTOM="${ZSH_CUSTOM:-$HOME/.oh-my-zsh/custom}"
[ -d "$ZSH_CUSTOM/plugins/zsh-syntax-highlighting" ] || \
  retry git clone --depth=1 https://github.com/zsh-users/zsh-syntax-highlighting.git "$ZSH_CUSTOM/plugins/zsh-syntax-highlighting"
[ -d "$ZSH_CUSTOM/plugins/zsh-autosuggestions" ] || \
  retry git clone --depth=1 https://github.com/zsh-users/zsh-autosuggestions.git "$ZSH_CUSTOM/plugins/zsh-autosuggestions"

echo "==> Copying dotfiles..."
cp "${SCRIPT_DIR}/dotfiles/.zshrc" "$HOME/.zshrc"
cp "${SCRIPT_DIR}/dotfiles/.p10k.zsh" "$HOME/.p10k.zsh"

echo "==> Setting default shell to zsh..."
sudo chsh -s /usr/bin/zsh vscode 2>/dev/null || chsh -s /usr/bin/zsh || true

echo
echo "==> Installed tool versions:"
go version
node --version
npm --version
claude --version || true
codex --version || true
gemini --version || true
copilot --version || true
roborev version || true
wipnote version || true
oh-my-posh --version || true

cat <<'EOF'

Devcontainer bootstrap complete.

This is a source-development environment — every change you make to
cmd/, internal/, or plugin/ can be rebuilt with `wipnote build`.

Next steps:
- Authenticate the CLIs once (stored in persistent volumes):
    claude           # OAuth browser login (or API key)
    codex
    gemini
    copilot
- Launch Claude Code in dev mode so it loads the plugin from source:
    wipnote claude --dev
- Start the dashboard:
    wipnote serve
    # http://localhost:8088 (container serves on 8080)
- Run the full test suite on demand:
    bash scripts/devcontainer-verify.sh

Persistent volumes mounted:
  /home/vscode/.claude         — Claude Code credentials
  /home/vscode/.codex          — Codex credentials
  /home/vscode/.gemini         — Gemini credentials
  /home/vscode/.copilot        — GitHub Copilot CLI credentials
  /home/vscode/.roborev        — roborev config, review database, and daemon state
  /home/vscode/.local          — wipnote binary + version metadata
  <workspace>/.wipnote         — tracked dogfood work items; runtime artifacts
                                 are ignored by git
EOF
