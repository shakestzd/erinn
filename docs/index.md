---
hide:
  - navigation
  - toc
title: wipnote
---

<div class="hg-hero" markdown>

<div class="hg-hero__bolt">
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
    <path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/>
  </svg>
</div>

<h1 class="hg-hero__headline">wipnote</h1>

<p class="hg-hero__sub">
Local-first causal lineage and observability for AI-assisted development.
</p>

<p class="hg-hero__solution">
Work items, session tracking, custom agents, hooks, slash commands, quality gates,
and a real-time dashboard &mdash; managed by a single Go binary, stored as HTML files in your repo.
No external infrastructure required.
</p>

<div class="hg-hero__buttons">
  <a class="hg-btn hg-btn--primary" href="#install">Install</a>
  <a class="hg-btn hg-btn--secondary" href="reference/cli/">CLI Reference</a>
  <a class="hg-btn hg-btn--secondary" href="blog/">Blog</a>
</div>

</div>

<!-- ======================================== -->

<section class="hg-section" markdown>

<h2 class="hg-section__title">What it does</h2>

<div class="hg-cards hg-cards--3col" markdown>

<div class="hg-card" markdown>
<span class="hg-card__title">Work item tracking</span>

Features, bugs, spikes, and tracks as HTML files in `.wipnote/`. Every change is a git diff. Every item has a lifecycle: create, start, complete.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Session observability</span>

Hooks capture every tool call, every prompt, and attribute them to the active work item. See exactly what happened in any session via the dashboard.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Custom agents</span>

Define specialized agents with specific models, tools, and system prompts. A researcher agent for investigation, a coder for implementation, a test runner for quality &mdash; each scoped to its job.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Hooks &amp; automation</span>

Event-driven hooks on SessionStart, PreToolUse, PostToolUse, and Stop. Enforce safety rules, capture telemetry, block dangerous operations, or trigger custom workflows automatically.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Skills &amp; slash commands</span>

Reusable workflows as slash commands: `/deploy`, `/diagnose`, `/plan`, `/code-quality`. Package complex multi-step procedures into single invocations that agents and humans can both use.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Quality gates</span>

Enforce software engineering discipline: build, lint, and test before every commit. Spec compliance scoring, code health metrics, and structured diff reviews built into the CLI.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Real-time dashboard</span>

Activity feed, kanban board, session viewer, and work item detail &mdash; served locally by `wipnote serve`. See what every agent is doing right now.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Multi-agent coordination</span>

Claude Code, Gemini CLI, Codex, and GitHub Copilot all read from and write to the same work items. Orchestration patterns control which agent handles which task.
</div>

<div class="hg-card" markdown>
<span class="hg-card__title">Plans &amp; specifications</span>

CRISPI plans break initiatives into trackable steps. Feature specs define acceptance criteria. Agents execute against the plan and report progress.
</div>

</div>

</section>

<!-- ======================================== -->

<section class="hg-section" markdown>

<h2 class="hg-section__title">Everything is a file in your repo</h2>

<div class="hg-cards hg-cards--3col hg-cards--arch" markdown>

<div class="hg-card hg-card--arch" markdown>
<code class="hg-card__label">.wipnote/*.html</code>

**HTML files** &mdash; Work items are the source of truth. Human-readable. Git-diffable. No proprietary format.
</div>

<div class="hg-card hg-card--arch" markdown>
<code class="hg-card__label">.wipnote/wipnote.db</code>

**SQLite index** &mdash; A derived read index for fast queries and dashboard rendering. Gitignored. Rebuilt from HTML anytime.
</div>

<div class="hg-card hg-card--arch" markdown>
<code class="hg-card__label">wipnote</code>

**Go binary** &mdash; One CLI that does everything: create work items, manage sessions, serve the dashboard, run hooks.
</div>

</div>

</section>

<!-- ======================================== -->

<section class="hg-section" markdown>

<h2 class="hg-section__title">Quick start</h2>

```bash
# Initialize in your repo
wipnote init

# Create a track and feature
wipnote track create "Auth Overhaul"
wipnote feature create "Add OAuth support" --track trk-abc123 --description "Implement OAuth2 flow"
wipnote feature start feat-def456

# Work with any AI agent — context is shared
# ... Claude Code, Gemini, Codex all see the active work item ...

wipnote feature complete feat-def456
wipnote serve    # see everything at localhost:4000
```

</section>

<!-- ======================================== -->

<section class="hg-section" id="install" markdown>

<h2 class="hg-section__title">Install</h2>

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

</section>

<!-- ======================================== -->

<section class="hg-section" markdown>

<h2 class="hg-section__title">Work item types</h2>

| Type | Prefix | Purpose |
|------|--------|---------|
| Feature | `feat-` | Units of deliverable work |
| Bug | `bug-` | Defects to fix |
| Spike | `spk-` | Time-boxed investigations |
| Track | `trk-` | Initiatives grouping related work |
| Plan | `plan-` | CRISPI implementation plans |

</section>

<!-- ======================================== -->

<div class="hg-footer-links" markdown>

[CLI Reference](reference/cli.md) &nbsp;&middot;&nbsp; [Blog](blog/index.md) &nbsp;&middot;&nbsp; [GitHub](https://github.com/shakestzd/wipnote) &nbsp;&middot;&nbsp; [Claude Code Plugin](https://github.com/shakestzd/wipnote)

</div>
