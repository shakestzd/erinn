# wipnote

**Causal lineage and observability for AI-assisted development.**

Answer "why does this code exist?" in one command. wipnote traces causal chains across work items, commits, sessions, and agent spawns — then stores everything as HTML files in your repo. No external infrastructure required.

## What this is NOT

- **Not a hosted platform.** Local-first. Your data stays on your machine — no cloud sync, no telemetry.
- **Not a general-purpose dev tool yet.** Today it's power-user and Claude-Code-centric. Other CLI integrations exist but are less mature.
- **Not a behavioral agent coordinator.** It observes and attributes what agents do — it does not dispatch, steer, or enforce behavior on agents at runtime.

## Architecture

| Layer | Role |
|-------|------|
| `.wipnote/*.html` | Canonical CRUD store — single source of truth |
| SQLite (`.wipnote/wipnote.db`) | Rebuildable read cache for fast queries and the dashboard |
| Go binary (`wipnote`) | CLI + hook handler |

HTML is the source of truth; SQLite is derived. If they drift, `wipnote reindex` drops the database and rebuilds it from the HTML files. No external infrastructure — no Postgres, no Redis, no cloud sync.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | bash
```

Supported platforms: `darwin_amd64`, `darwin_arm64`, `linux_amd64`, `linux_arm64`.

The installer places the `wipnote` binary in `~/.local/bin/` and the bundled plugin trees (`plugin/`, `codex-marketplace/`, `gemini-extension/`) in `~/.local/share/wipnote/`. Both locations can be customized via environment variables.

**Pinned to a specific version:**

```bash
curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_VERSION=0.60.1 bash
```

**Custom install directory:**

```bash
curl -fsSL https://raw.githubusercontent.com/shakestzd/wipnote/main/scripts/install.sh | WIPNOTE_BIN_DIR=$HOME/bin WIPNOTE_SHARE_DIR=$HOME/share/wipnote bash
```

### Upgrading

```bash
wipnote upgrade
```

### Verify

```bash
wipnote version    # should print 0.60.1 (or later)
```

<details>
<summary>What does this script do? / Manual install</summary>

The `install.sh` script:

1. Detects your OS (`uname -s`: `darwin` or `linux`) and architecture (`uname -m`: `x86_64`→`amd64`, `arm64`/`aarch64`→`arm64`). Errors clearly on unsupported combinations.
2. Resolves the version — from `WIPNOTE_VERSION` env var, or fetches the latest tag from `https://api.github.com/repos/shakestzd/wipnote/releases/latest`.
3. Downloads `wipnote_${VERSION}_${OS}_${ARCH}.tar.gz` and `wipnote_${VERSION}_checksums.txt` to a temp dir (cleaned up via `trap … EXIT`).
4. Verifies the sha256 checksum via `sha256sum` (Linux) or `shasum -a 256` (macOS). Hard-fails on mismatch; prints a clear warning if neither tool is available and skips (does NOT silently bypass).
5. Extracts the tarball, `mkdir -p`s the binary dir (`WIPNOTE_BIN_DIR`, default `$HOME/.local/bin`), moves the binary, `chmod +x`.
6. Installs the bundled plugin trees (`plugin/`, `codex-marketplace/`, `gemini-extension/`) to the share dir (`WIPNOTE_SHARE_DIR`, default `$HOME/.local/share/wipnote/`).
7. On macOS: removes the quarantine attribute (`xattr -d com.apple.quarantine`) to avoid Gatekeeper blocking.
8. Checks whether the binary dir is on your `PATH`. If not, prints instructions — it does NOT mutate your shell rc files.
9. Prints `==> Installed wipnote vX.Y.Z` and runs `wipnote version`.

**Manual install equivalent:**

```bash
VERSION=0.60.1
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
TMPD=$(mktemp -d)
curl -fsSL "https://github.com/shakestzd/wipnote/releases/download/v${VERSION}/wipnote_${VERSION}_${OS}_${ARCH}.tar.gz" \
  -o "$TMPD/wipnote.tar.gz"
curl -fsSL "https://github.com/shakestzd/wipnote/releases/download/v${VERSION}/wipnote_${VERSION}_checksums.txt" \
  -o "$TMPD/checksums.txt"
# Verify checksum (Linux: sha256sum, macOS: shasum -a 256)
sha256sum --check --ignore-missing "$TMPD/checksums.txt"  # Linux
# shasum -a 256 --check "$TMPD/checksums.txt"             # macOS
tar -xzf "$TMPD/wipnote.tar.gz" -C "$TMPD"
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/wipnote"
mv "$TMPD/wipnote" "$HOME/.local/bin/wipnote"
chmod +x "$HOME/.local/bin/wipnote"
# Install bundled plugin trees
for tree in plugin codex-marketplace gemini-extension; do
  [[ -d "$TMPD/$tree" ]] && mv "$TMPD/$tree" "$HOME/.local/share/wipnote/$tree"
done
xattr -d com.apple.quarantine "$HOME/.local/bin/wipnote" 2>/dev/null || true  # macOS only
rm -rf "$TMPD"
wipnote version
```

For other platforms (e.g. Windows), build from source:

```bash
git clone https://github.com/shakestzd/wipnote && cd wipnote && go build -o ~/.local/bin/wipnote ./cmd/wipnote
```

</details>

The release tarball bundles the plugin trees for Claude Code, Codex CLI, and
Gemini CLI alongside the `wipnote` binary. There is no separate
`claude plugin install` step — `wipnote claude` loads the bundled plugin via
`--plugin-dir` automatically.

### Using wipnote with each harness

```bash
wipnote claude   # Claude Code with bundled plugin
wipnote codex    # Codex CLI with bundled marketplace
wipnote gemini   # Gemini CLI with bundled extension
```

Each launcher resolves the bundled tree (from `~/.local/share/wipnote/` for the
curl/dev install) and points the harness at it. Override the resolved path
per-tree with `WIPNOTE_PLUGIN_DIR`, `WIPNOTE_CODEX_DIR`, or `WIPNOTE_GEMINI_DIR`.

For users who want the bare `claude` / `codex` / `gemini` commands to route
through wipnote, opt in via shell aliases:

```bash
wipnote shell-alias >> ~/.zshrc    # or ~/.bashrc
```

This is intentionally opt-in — per-project `wipnote claude` invocation is the
recommended flow because it avoids surprising shell behavior in non-wipnote
projects.

## Quick Start

```bash
wipnote init                          # creates .wipnote/ in your repo
wipnote track create "Auth Overhaul"
wipnote feature create "Add OAuth" --track <trk-id> --description "Implement OAuth2 flow"
wipnote feature start <feat-id>
# ... do work ...
wipnote feature complete <feat-id>
wipnote serve                         # dashboard at localhost:4000
```

## What It Does

**Causal lineage** — Trace the full causal chain for any work item, commit, session, or file. Three commands cover the common queries:

```bash
# Unified causal chain: forward edges (what this caused) + backward edges (what caused this)
wipnote lineage feat-abc1234

# Reverse direction: given a feature ID, list every commit and session it produced
wipnote trace feat-abc1234

# Temporal lineage: git log for a work item's HTML file — every edit, in order
wipnote history feat-abc1234
```

**Work item tracking** — Features, bugs, spikes, and tracks as HTML files in `.wipnote/`. Every change is a git diff. Every item has a lifecycle: create, start, complete.

**Session observability** — Hooks capture every tool call, every prompt, and attribute them to the active work item. See exactly what happened in any session via the dashboard.

**Custom agents** — Define specialized agents with specific models, tools, and system prompts. A researcher agent for investigation, a coder for implementation, a test runner for quality — each scoped to its job.

**Hooks & automation** — Event-driven hooks on SessionStart, PreToolUse, PostToolUse, and Stop. Enforce safety rules, capture telemetry, block dangerous operations, or trigger custom workflows automatically.

**Skills & slash commands** — Reusable workflows as slash commands: `/deploy`, `/diagnose`, `/plan`, `/code-quality`. Package complex multi-step procedures into single invocations.

**Quality gates** — Enforce software engineering discipline: build, lint, and test before every commit. Spec compliance scoring, code health metrics, and structured diff reviews.

**Real-time dashboard** — Activity feed, kanban board, session viewer, and work item detail — served locally by `wipnote serve`.

**Multi-agent attribution and observation** — Claude Code, Gemini CLI, Codex, and GitHub Copilot all read from and write to the same work items via the CLI. Every tool call, file edit, and session is attributed to a work item so you can see what each agent actually did. (Session transcript ingestion currently supports Claude Code JSONL format.)

**Plans & specifications** — CRISPI plans break initiatives into trackable steps. Feature specs define acceptance criteria. Agents execute against the plan and report progress.

## Work Item Types

| Type | Prefix | Purpose |
|------|--------|---------|
| Feature | `feat-` | Units of deliverable work |
| Bug | `bug-` | Defects to fix |
| Spike | `spk-` | Time-boxed investigations |
| Track | `trk-` | Initiatives grouping related work |
| Plan | `plan-` | CRISPI implementation plans |

## Roadmap

The lineage command family covers work items, commits, sessions, and files within a single repo. Two natural follow-ups are explicitly out of scope for now:

- **Spec-as-node** — treating feature specs as first-class lineage nodes so acceptance criteria appear in the causal chain alongside the code that satisfies them.
- **Cross-project lineage** — tracing chains across multiple repos registered in `~/.local/share/wipnote/projects.json`. Today each project's lineage is self-contained.

## CLI Reference

```
wipnote help --compact
```

See full CLI documentation at [shakestzd.github.io/wipnote/reference/cli](https://shakestzd.github.io/wipnote/reference/cli/).

## Contributing

wipnote is developed using wipnote itself (dogfooding). `.wipnote/` contains real work items — not demos.

```bash
git clone https://github.com/shakestzd/wipnote
cd wipnote
go build -o wipnote ./cmd/wipnote/
./wipnote init
```

Quality gates: `go build ./... && go vet ./... && go test ./...`

## License

MIT

## Links

- [Documentation](https://shakestzd.github.io/wipnote/)
- [GitHub](https://github.com/shakestzd/wipnote)
