# HtmlGraph Devcontainer

A clean-room **source development** environment for working on htmlgraph
itself, isolated from your laptop's installed htmlgraph release and from
any host-side Claude/Gemini/Codex configuration.

## Scope

This devcontainer is not a user environment — it is specifically for
editing `cmd/`, `internal/`, and `plugin/` source files, running the Go
test suite, and exercising the dashboard against a container-local
`.htmlgraph/` state.

Your laptop keeps running the installed `htmlgraph` release for day-to-day
work across other projects. The two are fully independent.

## Toolchain

| Tool | Source |
|------|--------|
| Go 1.23 | Devcontainer base image |
| Node.js 22 | `ghcr.io/devcontainers/features/node` |
| GitHub CLI | `ghcr.io/devcontainers/features/github-cli` |
| Claude Code CLI | `npm install -g @anthropic-ai/claude-code` |
| Gemini CLI | `npm install -g @google/gemini-cli` |
| Codex CLI | `npm install -g @openai/codex` |
| `htmlgraph` binary | Built from repo source via `plugin/build.sh` |
| `git`, `make`, `ripgrep`, `fd`, `jq`, `sqlite3`, `shellcheck` | Dockerfile apt install |

## Start

1. Open the repository in VS Code.
2. Run `Dev Containers: Reopen in Container`.
3. Wait for `.devcontainer/post-create.sh` to finish. It installs the
   three agent CLIs, runs `./plugin/build.sh` so `htmlgraph` lands on
   PATH, and runs `go build` + `go vet` as quality gates.

## Authentication

**No host environment variables are forwarded into the container.** This
is deliberate — the container is supposed to be isolated from your laptop
configuration. On first boot, authenticate each CLI interactively:

```bash
claude         # OAuth browser login (or API key if you prefer)
codex          # OpenAI login
gemini         # Google login
```

Credentials persist across rebuilds via named Docker volumes mounted at
`/home/vscode/.claude`, `/home/vscode/.codex`, and `/home/vscode/.gemini`.

### GitHub Codespaces (optional)

If you ever run this devcontainer on Codespaces instead of locally and
want non-interactive API-key access, add the keys as **Codespaces user
secrets** in your GitHub account settings. Codespaces injects them as
environment variables automatically. No changes to `devcontainer.json`
are required.

## State Isolation

The container uses a **named Docker volume** (`htmlgraph-dev-state`) for
`.htmlgraph/`. This means:

- The container starts with an empty `.htmlgraph/` on first boot.
- Work items you create inside the container stay inside the container
  and do not affect your host `.htmlgraph/`.
- The state persists across container rebuilds, so you can dogfood
  htmlgraph inside the container without losing work.
- If you want to reset the container-local state, delete the volume:
  `docker volume rm htmlgraph-dev-state`.

## Development Workflow

```bash
# Edit cmd/, internal/, or plugin/ files in VS Code.
# Rebuild the binary after any change:
htmlgraph build

# Run the full test suite on demand:
bash scripts/devcontainer-verify.sh

# Launch Claude Code in dev mode so it loads the plugin from repo source:
htmlgraph claude --dev

# Start the dashboard to inspect the container-local .htmlgraph/:
htmlgraph serve
# Dashboard forwarded to http://localhost:8088 on your host (container port 8080).
```

## What changed from the previous devcontainer

The previous devcontainer was built for the removed Python-based
htmlgraph. It ran `uv sync --frozen` against a `pyproject.toml` that no
longer exists, never built the Go binary, never enabled the plugin, and
forwarded six host API keys into the container. This version:

- Builds `htmlgraph` from source and installs it to `~/.local/bin/`.
- Uses the Go 1.23 devcontainer base image (Python base dropped).
- Forwards no host API keys — all authentication is interactive or via
  Codespaces secrets.
- Isolates `.htmlgraph/` in a named volume so the container never sees
  your laptop's work items.
