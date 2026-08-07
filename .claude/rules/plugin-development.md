---
paths:
  - "plugin/**"
  - "packages/plugin-core/**"
  - "cmd/**"
  - "internal/**"
---

# Plugin Development

**Source of truth:**

- `packages/plugin-core/manifest.json` — plugin metadata, per-target output paths, hook event matrix (Claude, Codex, Antigravity)
- `plugin/{commands,agents,skills,templates,static,config}/` — shared markdown/static assets (copied verbatim into every target)
- `cmd/` and `internal/` — Go CLI and hook handlers

**Generated — DO NOT hand-edit (regenerate via `wipnote plugin build-ports`):**

- `plugin/.claude-plugin/plugin.json`
- `plugin/hooks/hooks.json`
- everything under `port/packages/codex-marketplace/`
- everything under `port/packages/antigravity-extension/`
- everything under `.claude/` (auto-synced from `plugin/`)

See `packages/plugin-core/README.md` for new-command / new-hook / new-target recipes.

## Directory Structure (generated Claude tree)

- `plugin/.claude-plugin/plugin.json` — manifest (generated)
- `plugin/hooks/hooks.json` + `bin/wipnote` — Go binary hook handler (hooks.json generated)
- `plugin/agents/` — markdown agent definitions (source asset, edit directly)
- `plugin/skills/` — skill directories with SKILL.md (source asset, edit directly)
- `plugin/commands/` — slash commands (source asset, edit directly)
- `plugin/config/` — classification, drift, validation (source asset, edit directly)

**CRITICAL:** Don't put `commands/`, `agents/`, `skills/`, or `hooks/` inside `.claude-plugin/`. Only `plugin.json` belongs there. Caveat: `plugin/` is the Claude target's output directory — the asset subtrees listed above are hand-edited (they are the shared source for every target), while `plugin/.claude-plugin/plugin.json` and `plugin/hooks/hooks.json` are generated from `packages/plugin-core/manifest.json` and must be regenerated, not hand-edited.

## Workflow

1. Edit shared source: `packages/plugin-core/manifest.json`, `plugin/{commands,agents,skills,…}/`, `cmd/`, or `internal/`
2. Regenerate target trees: `wipnote plugin build-ports`
3. Run: `go build ./... && go vet ./... && go test ./...`
4. Build: `wipnote build`
5. Test: `wipnote claude --dev`
6. Deploy: `./scripts/deploy-all.sh X.Y.Z --no-confirm`

## Rules

- Edit `packages/plugin-core/manifest.json` (never the generated `plugin/hooks/hooks.json` or `plugin/.claude-plugin/plugin.json`)
- Edit Go source in `cmd/` or `internal/` for hook/CLI logic
- Add agents to `plugin/agents/`, skills to `plugin/skills/`, commands to `plugin/commands/` — all targets pick them up after `wipnote plugin build-ports`
- Hooks receive CloudEvent JSON on stdin — process via Go binary
- No stderr from hooks (causes "hook error" in Claude Code UI)
- Return `{}` to allow, `{"decision":"block","reason":"..."}` to block
- Prefer file/branch state over session state for hook gates (see "Hook State" below)

## Claude Code Plugin-Loaded Subagent Field Restrictions

Per the [Claude Code subagent docs](https://code.claude.com/docs/en/sub-agents), subagents loaded from plugins silently ignore the `hooks`, `mcpServers`, and `permissionMode` frontmatter fields. These fields work in user-/project-level agents at `~/.claude/agents/` or `.claude/agents/` but are stripped for plugin-shipped agents (which is how wipnote ships every harness's agent definitions).

**Workaround:** If you need any of these fields, define them at the harness level (e.g., plugin-wide `hooks.json`, `.mcp.json`, system-prompt context) rather than per-agent.

**Honored frontmatter fields for plugin-loaded subagents:** `name`, `description`, `model`, `color`, `tools`, `maxTurns`, `memory`, and the markdown body (system prompt).

`wipnote plugin build-ports` enforces per-harness frontmatter allowlists during agent generation and logs a build-time warning for any stripped field. The source of truth is `agentFrontmatterFieldSpecs` in `port/pluginbuild/agent_frontmatter.go`; it records supported harnesses, output-name translations, and upstream doc provenance for every shared source field. Current derived allowlists:

- Claude: `name`, `description`, `model`, `color`, `tools`, `maxTurns`, `memory`
- Codex: `name`, `description`, `model`, `tools`, `initialPrompt`
- Antigravity: `name`, `description`, `model`, `tools`, `max_turns`, `timeout_mins`

Keep shared agent source frontmatter in `plugin/agents/*.md` within those per-harness allowlists. If you add a new source field intentionally, update `agentFrontmatterFieldSpecs` and the tests in `port/pluginbuild/*_test.go` in the same change; do not add a separate prose-only allowlist.

## Hook Event Names Are Gated Per Harness

No harness validates hook event names. Codex silently drops unrecognized names at
dispatch (no warning, no error, no `codex doctor` diagnostic); the Antigravity generator
skips names it cannot translate. A wrong name therefore produces a hook that never fires,
forever, with no signal — the defect behind bug-e39d408f, where wipnote registered three
phantom Codex events and Codex sessions consequently emitted no stop or session-end signal
at all.

`wipnote plugin build-ports` now **hard-fails** on any `(event name, target)` pair that the
target harness does not dispatch. The source of truth is `hookEventNameSpecs` in
`port/pluginbuild/hook_event_names.go`, which records each canonical name, the harnesses
that dispatch it, the harness-native spelling where it differs, and the provenance of the
verification. Verified vocabularies:

- **Claude**: `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
  `PostToolUseFailure`, `Stop`, `SubagentStart`, `SubagentStop`, `PreCompact`, `PostCompact`,
  `PermissionRequest`, `TeammateIdle`, `TaskCreated`, `TaskCompleted`, `InstructionsLoaded`,
  `ConfigChange`, `WorktreeCreate`, `WorktreeRemove`
- **Codex** (0.147.0 — exactly 11): `SessionStart`, `SessionEnd`, `UserPromptSubmit`,
  `PreToolUse`, `PostToolUse`, `PermissionRequest`, `PreCompact`, `PostCompact`,
  `SubagentStart`, `SubagentStop`, `Stop`
- **Antigravity** (agy v1.0.8 — 5, translated on emit): `UserPromptSubmit`→`PreInvocation`,
  `AfterAgent`→`PostInvocation`, `PreToolUse`, `PostToolUse`, `Stop`

Registering a name for only some targets is normal and never an error — that is what
`targets` is for. The gate fires only when a name is registered against a harness that
cannot dispatch it. To add a harness or an event, add a verified entry with provenance;
never add one from an unverified source, since an unverifiable name is precisely the
failure the gate exists to prevent.

## Hook State: Prefer File/Branch State Over Session State

**Rule:** Hooks should answer questions from durable state (files, branches, staged diff)
rather than session state (session_id, tool-name history, task IDs). Session state is
brittle — tool names change, sessions rotate, and substring matches fail silently.

**Why it matters.** Two bugs shared the same root cause shape:

- GH#35 asked "is this session the claimant?" — the right question was "does this branch
  correspond to a valid claim?" Checking session_id against a claims table caused false
  blocks when sessions rotated (e.g. after reconnect).
- GH#36 (bug-a10ae96a, fixed in commit 88d2f51b4) asked "did someone call a tool matching
  `%screenshot%`?" — the right question was "does the staged diff contain UI files?" The
  LIKE match never fired because the Chrome MCP tool name didn't contain "screenshot".
  Moving the gate to `git diff --cached --name-only` fixed it cleanly.

Both bugs produced unsatisfiable checks or false positives on unrelated commands. The cost
is invisible failures: the guard either never fires (permissive) or fires on the wrong
trigger (noisy).

**How to apply:**

1. Before adding a hook, write down the question it answers. If the answer lives in
   `git diff --cached`, the branch name, the working tree, or config files — use that.
   Don't approximate it from session history.
2. Session state is fine for *attribution* and *telemetry*, not for *gates*. Gates must
   be reproducible across session restarts; telemetry just needs to be eventually consistent.
3. When blocking a commit, first check the staged diff. Short-circuit to allow when no
   relevant files are staged — see `hasStagedUIFiles()` in `internal/hooks/yolo_guard.go`
   and commit `88d2f51b4` as the canonical pattern.
4. Anchor Bash command matching to the specific command shape, not substrings.
   Use `^\s*git\s+commit(\s|$|--|-[^a-z])`, not `LIKE '%commit%'`.
