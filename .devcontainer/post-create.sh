#!/usr/bin/env bash

set -euo pipefail

# Fix ownership of named-volume mount points (Docker creates these as root:root)
sudo chown -R vscode:vscode \
    /workspaces/htmlgraph/.htmlgraph \
    /workspaces/htmlgraph/.claude \
    /home/vscode/.codex \
    /home/vscode/.gemini \
    /home/vscode/.copilot \
    2>/dev/null || true

cd "$(dirname "$0")/.."

export PATH="${HOME}/.local/bin:${PATH}"

echo "==> Installing tmux and ripgrep..."
sudo apt-get update && sudo apt-get install -y tmux ripgrep

echo "==> Installing AI agent CLIs..."
npm install -g --no-fund --no-audit \
    @anthropic-ai/claude-code \
    @google/gemini-cli \
    @openai/codex \
    @github/copilot

echo "==> Building htmlgraph from source..."
./plugin/build.sh

echo "==> Running quality gates..."
go build ./...
go vet ./...

echo "==> Fixing Claude Code plugin data directory permissions..."
sudo chown -R vscode:vscode ~/.claude 2>/dev/null || true
mkdir -p ~/.claude/plugins/data

echo "==> Installing uv..."
if ! command -v uv >/dev/null 2>&1; then
  curl -LsSf https://astral.sh/uv/install.sh | sh
fi

echo "==> Installing Oh My Posh..."
if ! command -v oh-my-posh >/dev/null 2>&1; then
  curl -s https://ohmyposh.dev/install.sh | bash -s -- -d "$HOME/.local/bin"
fi

echo "==> Installing ttyd (required by the dashboard terminal launcher)..."
if ! command -v ttyd >/dev/null 2>&1; then
  mkdir -p "$HOME/.local/bin"
  TTYD_VERSION=$(curl -sfL https://api.github.com/repos/tsl0922/ttyd/releases/latest 2>/dev/null \
    | grep -oE '"tag_name": "[^"]+"' | head -1 | cut -d'"' -f4)
  TTYD_VERSION=${TTYD_VERSION:-1.7.7}
  curl -fL -o "$HOME/.local/bin/ttyd" \
    "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.$(uname -m)"
  chmod +x "$HOME/.local/bin/ttyd"
fi

echo "==> Ensuring \$HOME/.local/bin is on PATH in shell rc files..."
for _rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  if [ -f "$_rc" ] && ! grep -q '.local/bin' "$_rc"; then
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> "$_rc"
  fi
done

echo "==> Installing HtmlGraph + Oh My Posh Claude Code wrapper..."
mkdir -p "$HOME/.claude"
if [ ! -f "$HOME/.claude/omp-claude-wrapper.sh" ]; then
  cat > "$HOME/.claude/omp-claude-wrapper.sh" << 'EOF'
#!/bin/bash
# HtmlGraph + Oh My Posh wrapper for Claude Code status line

_dir="$(pwd)"
while [ "$_dir" != "/" ]; do
    [ -d "$_dir/.htmlgraph" ] && break
    _dir=$(dirname "$_dir")
done
if [ "$_dir" = "/" ]; then
    echo ""
    exit 0
fi

INPUT=$(cat)
SESS_ID=$(echo "$INPUT" | python3 -c "import sys, json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
ACTIVE_WORK=$(htmlgraph statusline --session "$SESS_ID" 2>/dev/null)
export HTMLGRAPH_ACTIVE="$ACTIVE_WORK"

OMP_BIN="${HTMLGRAPH_OMP_BIN:-$(which oh-my-posh 2>/dev/null)}"
OMP_CONFIG="${HTMLGRAPH_OMP_CONFIG:-$HOME/.claude.omp.json}"

if [ -n "$OMP_BIN" ] && [ -f "$OMP_CONFIG" ]; then
    echo "$INPUT" | "$OMP_BIN" claude --config "$OMP_CONFIG"
else
    echo "$ACTIVE_WORK"
fi
EOF
fi
chmod +x "$HOME/.claude/omp-claude-wrapper.sh"

echo "==> Linking Claude Code Oh My Posh theme..."
# Source of truth lives in .devcontainer/ (tracked in git); symlink into HOME so
# it survives container rebuilds without needing to re-copy each time.
# Use an absolute path — a relative `$(dirname "$0")` symlink resolves from
# $HOME (the symlink's own dir), landing at $HOME/.devcontainer/... which is
# not the repo file.
ln -sf "$(cd "$(dirname "$0")" && pwd)/claude.omp.json" "$HOME/.claude.omp.json"

echo "==> Installing oh-my-zsh..."
if [ ! -d "$HOME/.oh-my-zsh" ]; then
  RUNZSH=no CHSH=no KEEP_ZSHRC=yes sh -c "$(curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh)"
fi

echo "==> Installing powerlevel10k..."
if [ ! -d "$HOME/powerlevel10k" ]; then
  git clone --depth=1 https://github.com/romkatv/powerlevel10k.git "$HOME/powerlevel10k"
fi

echo "==> Installing zsh plugins..."
ZSH_CUSTOM="${ZSH_CUSTOM:-$HOME/.oh-my-zsh/custom}"
[ -d "$ZSH_CUSTOM/plugins/zsh-syntax-highlighting" ] || \
  git clone --depth=1 https://github.com/zsh-users/zsh-syntax-highlighting.git "$ZSH_CUSTOM/plugins/zsh-syntax-highlighting"
[ -d "$ZSH_CUSTOM/plugins/zsh-autosuggestions" ] || \
  git clone --depth=1 https://github.com/zsh-users/zsh-autosuggestions.git "$ZSH_CUSTOM/plugins/zsh-autosuggestions"

echo "==> Copying dotfiles..."
cp "$(dirname "$0")/dotfiles/.zshrc" "$HOME/.zshrc"
cp "$(dirname "$0")/dotfiles/.p10k.zsh" "$HOME/.p10k.zsh"

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
htmlgraph version || true
oh-my-posh --version || true

cat <<'EOF'

Devcontainer bootstrap complete.

This is a source-development environment — every change you make to
cmd/, internal/, or plugin/ can be rebuilt with `htmlgraph build`.

Next steps:
- Authenticate the CLIs once (stored in persistent volumes):
    claude           # OAuth browser login (or API key)
    codex
    gemini
    copilot
- Launch Claude Code in dev mode so it loads the plugin from source:
    htmlgraph claude --dev
- Start the dashboard:
    htmlgraph serve
    # http://localhost:8080
- Run the full test suite on demand:
    bash scripts/devcontainer-verify.sh

Persistent volumes mounted:
  /home/vscode/.claude         — Claude Code credentials
  /home/vscode/.codex          — Codex credentials
  /home/vscode/.gemini         — Gemini credentials
  /home/vscode/.copilot        — GitHub Copilot CLI credentials
  /home/vscode/.local          — htmlgraph binary + version metadata
  <workspace>/.htmlgraph       — devcontainer-only work item state
                                 (isolated from your host .htmlgraph/)
EOF
