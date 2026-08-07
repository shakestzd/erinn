@AGENTS.md

# wipnote — Claude Code

Local-first observability and coordination platform for AI-assisted development.

---

## Build

**Always use `wipnote build`, never `go build` directly.**

```bash
wipnote build      # outputs to ~/.local/bin/wipnote (on your PATH)
```

Running `go build -o wipnote ./cmd/wipnote/` puts the binary in CWD, not on your PATH.

---

## Quality Gates

```bash
go build ./... && go vet ./... && go test ./...
# Commit only when ALL pass
```

Use `/wipnote:code-quality-skill` for the complete pre-commit workflow.

---

## Deploy

```bash
./scripts/deploy-all.sh X.Y.Z --no-confirm   # full pipeline
```

Or `/wipnote:deploy X.Y.Z`. The release tarball (and the Homebrew formula) bundle the plugin tree alongside the `wipnote` binary; the deploy script rebuilds and mirrors both atomically. There is no separate `claude plugin install` step.

## Launching Claude with wipnote

```bash
wipnote claude   # uses the bundled plugin in ~/.local/share/wipnote/plugin/
```

The launcher resolves the plugin tree (env override → `~/.local/share/wipnote/plugin/` → `$(brew --prefix)/share/wipnote/plugin/` → dev source) and passes it to Claude Code via `--plugin-dir`. Hooks, agents, skills, and slash commands all load from the bundled tree.

For users who want bare `claude` to route through wipnote globally, opt-in via `wipnote shell-alias >> ~/.zshrc`. Per-project `wipnote claude` is the recommended flow.

---

## Dev Mode

```bash
wipnote claude --dev   # loads plugin from in-tree plugin/ via --plugin-dir
```

Dev mode bypasses the bundled tree resolver and instead points Claude Code at the in-tree `plugin/` directory directly — so agent, skill, hook, and command edits in the working tree are picked up immediately. It also clears any legacy marketplace install so it cannot shadow the dev source.

**Why full removal of legacy installs is required:** Disabling a legacy marketplace plugin only affects hooks. Agent definitions and skill content continue loading from `~/.claude/plugins/marketplaces/`, silently shadowing dev source changes.

## Resuming a Specific Session

`wipnote claude`, `wipnote yolo`, and `wipnote dev` all accept `--resume <session-id>` to resume a specific prior Claude Code session. On exit, Claude Code prints a line like `claude --resume d846b50d-…`; pass that ID through the wipnote launcher to get the wipnote plugin, system prompt, and (in `--dev`) local `--plugin-dir` applied to the resumed session:

```bash
wipnote claude --resume d846b50d-9ce4-45c1-8ad2-0f84da537efd
wipnote claude --dev --resume <session-id>
wipnote yolo --dev --resume <session-id>
wipnote dev --resume <session-id>
```

`--resume <id>` differs from `--continue` (which resumes the most recent session). If both are passed, `--resume <id>` wins.

## Dev Mode in Codespaces

Codespaces clients disconnect on idle, browser refresh, or network blips — killing long dev sessions. Wrap dev in tmux:

    wipnote claude --dev --tmux

First run creates a tmux session named `wipnote-dev`. On disconnect, detach instead of dying. Re-run the same command to reattach to the surviving session. Manually detach with `Ctrl-b d`; kill the session with `tmux kill-session -t wipnote-dev`.

## Yolo Mode in Codespaces

Codespaces clients disconnect on idle, browser refresh, or network blips — killing long yolo runs. Wrap yolo in tmux:

    wipnote yolo --dev --tmux

First run creates a tmux session named `wipnote-yolo`. On disconnect, detach instead of dying. Re-run the same command to reattach to the surviving session. Manually detach with `Ctrl-b d`; kill the session with `tmux kill-session -t wipnote-yolo`.

---

## Plugin Source — Single Source of Truth

wipnote ships one plugin to multiple AI-tool targets (Claude Code, Codex CLI, Antigravity) from
a single source. Each target's plugin tree is **generated** — never hand-edit it. (Gemini CLI
was retired as a launch/generation target — see `AGENTS.md` — but wipnote still reads historical
Gemini CLI session data via the retained ingest read path.)

**Layering (edit the left column only):**

| Layer | What lives there | Edit? |
|-------|------------------|-------|
| `packages/plugin-core/manifest.json` | Canonical plugin metadata + per-target output paths + hook event matrix (which events target Claude, Codex, Antigravity, or any combination) | YES |
| `plugin/commands/`, `plugin/agents/`, `plugin/skills/`, `plugin/templates/`, `plugin/static/`, `plugin/config/` | Shared markdown/static assets — copied verbatim into every target tree | YES |
| `cmd/`, `internal/` | Go CLI + hook handlers (all hooks are thin wrappers over `wipnote hook <handler>`) | YES |
| `plugin/.claude-plugin/plugin.json`, `plugin/hooks/hooks.json` | Generated Claude Code tree | NO — regenerate |
| `port/packages/codex-marketplace/` | Generated Codex CLI tree | NO — regenerate |
| `port/packages/antigravity-extension/` | Generated Antigravity tree | NO — regenerate |
| `.claude/` (anything) | Auto-synced from `plugin/` — changes are lost | NO |

**Regenerate after every manifest or asset edit:**

```bash
wipnote plugin build-ports              # all targets
wipnote plugin build-ports --target codex
wipnote plugin build-ports --target claude
wipnote plugin build-ports --target antigravity
```

See `packages/plugin-core/README.md` for the per-task recipes (new command, new hook,
new target) and `.claude/rules/plugin-development.md` for full plugin structure reference.

---

## Orchestration

Delegate ALL operations except `Task()`, `AskUserQuestion()`, `TodoWrite()`, SDK operations.

Use `/wipnote:orchestrator-directives-skill` for delegation patterns and model selection.

---

## Quick Commands

| Task | Command |
|------|---------|
| View work | `wipnote snapshot --summary` |
| Run tests | `go test ./...` |
| Build binary | `wipnote build` |
| Deploy | `./scripts/deploy-all.sh VERSION --no-confirm` |
| Dashboard | `wipnote serve` |
| Status | `wipnote status` |
| Self-update | `wipnote upgrade` |

---

## Monitoring Upstream Harnesses (Claude Code / Codex / Antigravity)

wipnote ships to three independently-evolving CLIs: Claude Code, Codex CLI, and Antigravity (Gemini CLI was retired as a launch/generation target — feat-02f25a24). Each harness is an integration surface; plugins, hooks, skills, agents, and observability all have harness-specific contracts that change on different vendor release cadences. A doc audit on 2026-05-15 found silent-failure drift in all three harnesses shipped at that time (Claude Code, Codex, Gemini) — fields set in your agent config may stop being honored with no error. Continuous monitoring is non-negotiable.

**Periodically review the upstream docs for changes to:**

- **Plugin system / agent manifest** — formats, frontmatter schema, marketplace/config lookup paths
- **Hooks** — event names, payload shapes, exit-code semantics (Claude Code only)
- **Skills** — frontmatter fields, activation triggers, invocation patterns (Claude Code only)
- **Agent configuration** — frontmatter schema (name, description, model, color, tools, timeout/maxTurns, memory, max_depth)
- **Observability** — session metadata, telemetry, cost/token reporting, transcript format
- **Tool interfaces** — available tools per harness (Claude Code ≠ Codex ≠ Antigravity), invocation signatures

**Upstream sources to monitor:**

**Claude Code (Anthropic):**
- https://code.claude.com/docs/en/sub-agents — subagent frontmatter schema
- https://code.claude.com/docs/en/plugins — plugin manifest and marketplace structure
- https://code.claude.com/docs/en/hooks — hook events, payloads, exit codes
- https://code.claude.com/docs/en/skills — skill frontmatter and activation
- Claude Code release notes / changelog

**Codex CLI (OpenAI):**
- https://developers.openai.com/codex/subagents — custom agent TOML schema and native lookup paths (~/.codex/agents/, .codex/agents/)
- https://developers.openai.com/codex/config-reference — config keys including [agents] section (max_threads, max_depth, job_max_runtime_seconds; note: NO per-agent interactive turn cap)
- https://developers.openai.com/codex/config-advanced — advanced configuration options

**Antigravity (Google):**
- No verified canonical upstream doc set yet — Antigravity is a newly-added target (feat-02f25a24) and currently inherits Gemini-CLI's extension/plugin conventions (agent frontmatter, TOML slash commands, tool-name vocabulary) via the retained compat helpers in `port/pluginbuild/gemini_compat.go` rather than an independently-verified contract. Until a dedicated doc audit establishes canonical Antigravity sources, treat `port/pluginbuild/antigravity.go` as the executable spec and re-derive from Google's official Antigravity docs when auditing.

**Re-verify on a cadence, not just on suspicion.** These schemas drift silently — a field you set may stop being honored with no error. Re-run the cross-harness doc-verification audit at least every release cycle (or via a scheduled routine), using `agentFrontmatterFieldSpecs` in `port/pluginbuild/agent_frontmatter.go` as the checklist for agent frontmatter fields, supported harnesses, output-name translations, and provenance links. When a contract changes, the fix lands in `plugin/agents/*.md`, `packages/plugin-core/manifest.json`, `port/pluginbuild/`, or `cmd/wipnote/prompts/system-prompt.md` — never in user-facing docs like AGENTS.md or CLAUDE.md (those describe, they don't configure).

---

## Worktrees and .wipnote/ Artifact Commits

Conductor-managed worktrees (and any worktree created by `wipnote yolo`) install a per-worktree
gitignore entry that excludes `.wipnote/` from the worktree's own `git status`. This is intentional
noise-reduction. The artifact is still committed: `wipnote feature/bug/spike complete` calls
`commitWipnoteArtifact` which uses `git -C <repoRoot>` with an explicit absolute path to stage and
commit the HTML in the main repository, bypassing the exclusion.

**Conductor limitation:** Conductor archives worktrees directly without invoking `wipnote * complete`.
To ensure your work item artifact is committed, you must call `wipnote feature complete <id>` (or
`bug`/`spike`) from within the workspace before Conductor tears down the worktree. wipnote cannot
intercept Conductor's archive step.

---

## Dogfooding

This project uses wipnote to develop itself. `.wipnote/` contains real work items — not demos.
