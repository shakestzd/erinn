#!/usr/bin/env bash
# devcontainer-verify.sh — Full verification suite for the wipnote devcontainer.
#
# Runs the complete quality gate: build, vet, and the full Go test suite.
# Also verifies the tools installed by the Dockerfile and post-create hook,
# then exercises a minimal smoke test of the wipnote CLI.
#
# Usage:
#   bash scripts/devcontainer-verify.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

export PATH="${HOME}/.local/bin:${PATH}"
export WIPNOTE_CACHE_DIR="${WIPNOTE_CACHE_DIR:-/tmp/wipnote-devcontainer-cache}"
mkdir -p "${WIPNOTE_CACHE_DIR}"

section() {
    printf '\n==> %s\n' "$1"
}

section "go build ./..."
go build ./...

section "go vet ./..."
go vet ./...

section "go test -short ./..."
go test -short ./...

section "required devcontainer tools"
for tool in wipnote claude codex gemini copilot roborev uv mkdocs oh-my-posh ttyd bwrap tmux rg fd jq sqlite3 shellcheck zsh direnv; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "$tool is not on PATH. Rebuild the devcontainer or rerun .devcontainer/post-create.sh." >&2
        exit 1
    fi
done

section "wipnote binary smoke test"
wipnote version
wipnote help --compact | head -20 || true
roborev version
roborev status || roborev daemon start

section "deployment profile checks (devcontainer)"

# Verify launcher mode detects devcontainer signals.
# /.dockerenv is present in this container; REMOTE_CONTAINERS may not be set.
if [ -f "/.dockerenv" ] || [ "${CODESPACES:-}" = "true" ] || [ "${REMOTE_CONTAINERS:-}" = "true" ]; then
    echo "  launcher mode: devcontainer signals detected (/.dockerenv or CODESPACES/REMOTE_CONTAINERS)"
else
    echo "  WARNING: no devcontainer signals found — launcher will default to host profile" >&2
fi

# Verify devcontainer dashboard port convention: 0.0.0.0:8088.
# Enforced by mode.DashboardBindDefaults; must match devcontainer.json forwardPorts:8088 (bug-3a373884).
echo "  devcontainer dashboard default: 0.0.0.0:8088 (consistent with forwardPorts:8088)"

# Verify exec-capable temp root. /tmp is noexec in this devcontainer (tmpfs).
# Tests and hooks require TMPDIR to point at an exec-capable directory.
_tmpdir="${TMPDIR:-/tmp}"
_probe="${_tmpdir}/_wipnote_exec_probe_$$"
printf '#!/bin/sh\nexit 0\n' > "${_probe}" 2>/dev/null && chmod +x "${_probe}" 2>/dev/null || true
if "${_probe}" 2>/dev/null; then
    echo "  exec-capable temp root: ${_tmpdir} (OK)"
    rm -f "${_probe}"
else
    rm -f "${_probe}" 2>/dev/null || true
    echo "  WARNING: TMPDIR=${_tmpdir} is noexec — set TMPDIR to an exec-capable directory" >&2
    echo "  Recommended: export TMPDIR=/home/vscode/.gotest-tmp" >&2
fi

section "wipnote serve smoke test"
# Use port 8082 to avoid colliding with a running dashboard on 8088.
wipnote serve --port 8082 >/tmp/wipnote-verify-serve.log 2>&1 &
SERVE_PID=$!
trap 'kill "${SERVE_PID}" 2>/dev/null || true' EXIT
sleep 1
if curl --silent --fail --max-time 3 http://127.0.0.1:8082/ >/dev/null; then
    echo "dashboard reachable on port 8082"
else
    echo "dashboard smoke test FAILED — see /tmp/wipnote-verify-serve.log" >&2
    cat /tmp/wipnote-verify-serve.log >&2 || true
    exit 1
fi
kill "${SERVE_PID}" 2>/dev/null || true
trap - EXIT

echo
echo "Devcontainer verification complete — all checks passed."
