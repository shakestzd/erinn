# Wipnote Devcontainer

A clean-room **source development** environment for working on wipnote
itself, isolated from your laptop's installed wipnote release and from
any host-side Claude/Codex configuration.

## Scope

This devcontainer is not a user environment — it is specifically for
editing `cmd/`, `internal/`, and `plugin/` source files, running the Go
test suite, and exercising the dashboard against the repository's tracked
dogfood `.wipnote/` state.

Your laptop keeps running the installed `wipnote` release for day-to-day
work across other projects. The two are fully independent.

## Toolchain

| Tool | Source |
|------|--------|
| Go 1.24 | Devcontainer base image |
| Node.js 22 | `ghcr.io/devcontainers/features/node` |
| GitHub CLI | `ghcr.io/devcontainers/features/github-cli` |
| Claude Code CLI | `npm install -g @anthropic-ai/claude-code` |
| Codex CLI | `npm install -g @openai/codex` |
| GitHub Copilot CLI | `npm install -g @github/copilot` |
| Roborev CLI | `.devcontainer/post-create.sh` via official `roborev.io` installer |
| `wipnote` binary | Built from repo source via `plugin/build.sh` |
| `bwrap`, `tmux`, `rg`, `fd`, `jq`, `sqlite3`, `shellcheck`, `direnv`, `zsh` | Dockerfile apt install |
| `uv` | `.devcontainer/post-create.sh` via Astral installer |
| MkDocs + Material + pymdown extensions | `.devcontainer/post-create.sh` via `uv tool install` |
| Oh My Posh | `.devcontainer/post-create.sh` via upstream installer |
| `ttyd` | `.devcontainer/post-create.sh` via upstream release binary |
| Oh My Zsh, Powerlevel10k, zsh plugins | `.devcontainer/post-create.sh` |

## Start

1. Open the repository in VS Code.
2. Run `Dev Containers: Reopen in Container`.
3. Wait for `.devcontainer/post-create.sh` to finish. It installs the
   agent CLIs, docs/shell/dashboard helper tools, runs `./plugin/build.sh`
   so `wipnote` lands on PATH, and runs `go build` + `go vet` as quality
   gates.

The devcontainer pins DNS to Cloudflare and Google via `runArgs` because
Docker Desktop's internal DNS proxy can intermittently return `EAI_AGAIN`
for registry lookups even when the host network is healthy. Network
installs in `post-create.sh` also retry transient failures before failing
the bootstrap.

## Authentication

**No host environment variables are forwarded into the container.** This
is deliberate — the container is supposed to be isolated from your laptop
configuration. On first boot, authenticate each CLI interactively:

```bash
claude         # OAuth browser login (or API key if you prefer)
codex          # OpenAI login
copilot        # GitHub login
```

Credentials persist across rebuilds via named Docker volumes mounted at
`/home/vscode/.claude`, `/home/vscode/.codex`,
and `/home/vscode/.copilot`. The source-built `wipnote` binary and
locally installed helper tools persist in `/home/vscode/.local`. Roborev
state persists in `/home/vscode/.roborev`, and `post-create.sh`
re-initializes this repository's Roborev hooks. This repo intentionally leaves
Roborev agent selection open so you can use both Codex and Claude Code with
per-command flags such as `roborev review --agent codex` or
`roborev review --agent claude-code`.

The volume names are `wipnote-*`. If you previously used the old
`htmlgraph-*` devcontainer volumes, expect to authenticate once after
rebuilding or copy credentials between Docker volumes manually.

### GitHub Codespaces (optional)

If you ever run this devcontainer on Codespaces instead of locally and
want non-interactive API-key access, add the keys as **Codespaces user
secrets** in your GitHub account settings. Codespaces injects them as
environment variables automatically. No changes to `devcontainer.json`
are required.

## State

The repository's `.wipnote/**/*.html` files are canonical dogfood work
items and are intentionally visible inside the devcontainer. They are
tracked in Git so closing or resuming wipnote work has the same context
inside and outside the container.

Runtime files under `.wipnote/` remain untracked and ignored: SQLite
indexes, session telemetry, logs, locks, and temporary collector state.
CLI credentials and user-level agent config are isolated in named Docker
volumes under `$HOME`; no host auth files are mounted into the container.

## Development Workflow

```bash
# Edit cmd/, internal/, or plugin/ files in VS Code.
# Rebuild the binary after any change:
wipnote build

# Run the full test suite on demand:
bash scripts/devcontainer-verify.sh

# Launch Claude Code in dev mode so it loads the plugin from repo source:
wipnote claude --dev

# Start the dashboard to inspect the container-local .wipnote/:
wipnote serve
# Dashboard binds 0.0.0.0:8088 inside the container; VS Code forwards port 8088 to host.
```

## What changed from the previous devcontainer

The previous devcontainer was built for the removed Python-based
wipnote. It ran `uv sync --frozen` against a `pyproject.toml` that no
longer exists, never built the Go binary, never enabled the plugin, and
forwarded six host API keys into the container. This version:

- Builds `wipnote` from source and installs it to `~/.local/bin/`.
- Uses the Go 1.24 devcontainer base image (Python base dropped).
- Forwards no host API keys — all authentication is interactive or via
  Codespaces secrets.
- Keeps tracked `.wipnote` work items in the repo while ignoring runtime
  artifacts, so dogfood state is available when wipnote sessions close.
## Deployment Profiles

wipnote detects four deployment profiles at launcher startup. The devcontainer
uses the `devcontainer-dev` profile automatically — no configuration needed.

| Profile | Dashboard default | Rollout mode |
|---------|-------------------|--------------|
| `host-production` | `127.0.0.1:8080` | warn-only (no enforcement until slice-9) |
| `devcontainer-dev` | `0.0.0.0:8088` | config-gated opt-in |
| `local-plugin-dev` | `127.0.0.1:8080` | warn-only |
| `ci-test` | `127.0.0.1:8080` | config-gated opt-in |

The devcontainer dashboard binds `0.0.0.0:8088` so VS Code's port-forward
(configured via `forwardPorts: [8088]` in `devcontainer.json`) can reach it on
`http://127.0.0.1:8088` from your host browser. Do not use port 8080 for
dashboard sessions inside this devcontainer — that is the host production default.

Run `scripts/devcontainer-verify.sh` to validate that the launcher mode,
port convention, and exec-capable temp root all pass in this container.
