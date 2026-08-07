# plugin-core — DRY source of truth for wipnote plugin ports

All wipnote plugin ports (Claude Code, Codex CLI, Antigravity) are generated from the
files in this directory so we never edit the same logic twice. (Gemini CLI was retired
as a launch/generation target — feat-02f25a24 — but wipnote still reads historical
Gemini CLI session data via the retained ingest read path; see `AGENTS.md`.)

## Source of truth

- **`manifest.json`** — plugin metadata, per-target output paths, hook event
  matrix. `plugin/.claude-plugin/plugin.json`,
  `port/packages/codex-marketplace/.agents/plugins/wipnote/.codex-plugin/plugin.json`, and
  `port/packages/antigravity-extension/plugin.json` are all generated from it.
- **Assets** (commands, agents, skills, templates, static, config) live in
  `plugin/…/`. Codex skills and commands are copied in their native markdown
  form, while Codex agents are translated from `plugin/agents/*.md` into
  custom-agent TOML under the generated marketplace plugin's `agents/` directory.
  Antigravity requires TOML slash commands, so a sub-emitter translates the
  markdown on the way out (reusing the same Gemini-CLI-derived translation
  helpers Antigravity's tool vocabulary shares — see `port/pluginbuild/gemini_compat.go`).
- **Generated trees** — `plugin/` (Claude), `port/packages/codex-marketplace/` (Codex),
  and `port/packages/antigravity-extension/` (Antigravity) are output directories. Treat them
  as build artifacts: do not hand-edit anything under `plugin/.claude-plugin/`,
  `plugin/hooks/hooks.json`, `port/packages/codex-marketplace/`, or
  `port/packages/antigravity-extension/`. Regenerate instead.

## Build

    wipnote plugin build-ports              # regenerate all targets
    wipnote plugin build-ports --target codex
    wipnote plugin build-ports --target claude
    wipnote plugin build-ports --target antigravity

The command writes each target's tree under the `outDir` declared in
`manifest.json → targets.<name>`.

Codex custom agents have a second runtime step: `wipnote codex --init`,
`wipnote codex`, and `wipnote codex --dev` mirror the generated
`port/packages/codex-marketplace/.agents/plugins/wipnote/agents/*.toml` files into
Codex's documented custom-agent lookup directory. Normal installs use
`~/.codex/agents`; dev launches also refresh project-local `.codex/agents`.
The launcher additionally passes explicit `-c agents.<name>.config_file=...`
overrides so fresh Codex CLI sessions do not rely only on plugin-cache discovery.

Current limitation: some tool-backed Codex sessions still expose only generic
`default`, `explorer`, and `worker` spawn roles even when custom-agent TOML files
exist on disk. Treat plugin/cache file presence as generation proof, not runtime
spawn proof; verify runtime behavior with an actual `spawn_agent` smoke test.

### Agent roles and model policy

Agent names describe responsibilities, not model families. Use stable role names
such as `patch-coder`, `feature-coder`, and `architect-coder`; keep provider
model choices in frontmatter or target-specific emitters.

Current mapping:

| Role | Purpose | Claude model alias | Codex model | Antigravity model |
|------|---------|--------------------|-------------|--------------|
| `patch-coder` | Small, clear edits | `haiku` | `gpt-5.4-mini`, low effort | `flash-lite` |
| `feature-coder` | Moderate implementation | `sonnet` | `gpt-5.4`, medium effort | `flash` |
| `architect-coder` | Complex architecture/high risk | `opus` | `gpt-5.5`, high effort | `pro` |

This follows each harness's documented shape: Claude subagents use a role `name`
plus separate model configuration, Codex custom-agent TOML identifies agents by
`name` and supports separate `model` / `model_reasoning_effort`, and Antigravity
subagents use role slugs with optional model overrides (inherited from the
Gemini-CLI convention it descends from — see `mapGeminiAgentModel` in
`port/pluginbuild/gemini_compat.go`).

## Hooks — thin wrappers

Every hook resolves to `wipnote hook <handler>`. Business logic lives in the
Go CLI (`internal/hooks/`); the plugin manifests only declare which events route
to which handler and on which target. Events whose `targets` list omits a given
target are not emitted to that target's hooks file.

### Hook event matrix

Derived from `manifest.json → hooks.events`. Update this table whenever you
edit the manifest.

| Event | Handler | Claude | Codex | Antigravity | Notes |
|-------|---------|:---:|:---:|:---:|-------|
| `SessionStart` | `session-start` | x | x | | |
| `SessionStart` | `session-resume` | x | | | matcher: `resume` |
| `SessionEnd` | `session-end` | x | | | timeout 90s |
| `UserPromptSubmit` | `user-prompt` | x | x | x | |
| `AfterAgent` | `after-agent` | | | x | Antigravity-specific |
| `UserPromptSubmit` | `timestamp` | x | | | shell `command:` only — injects local timestamp |
| `PreToolUse` | `pretooluse` | x | x | x | |
| `PostToolUse` | `posttooluse` | x | x | x | |
| `PostToolUse` | `exit-plan-mode` | x | | | matcher: `ExitPlanMode` |
| `SubagentStart` | `subagent-start` | x | | | |
| `SubagentStop` | `subagent-stop` | x | | | |
| `Stop` | `stop` | x | | x | timeout 90s |
| `PostToolUseFailure` | `posttooluse-failure` | x | | | |
| `PreCompact` | `pre-compact` | x | | | |
| `TeammateIdle` | `teammate-idle` | x | | | |
| `TaskCompleted` | `task-completed` | x | | | |
| `InstructionsLoaded` | `instructions-loaded` | x | | | |
| `WorktreeCreate` | `worktree-create` | x | | | |
| `WorktreeRemove` | `worktree-remove` | x | | | |
| `PostCompact` | `post-compact` | x | | | |
| `TaskCreated` | `task-created` | x | | | |
| `PermissionRequest` | `permission-request` | x | | | |
| `ConfigChange` | `config-change` | x | | | |
| `TaskStarted` | `task-started` | | x | | Codex-specific |
| `TaskComplete` | `stop` | | x | | Codex-specific — reuses `stop` handler; timeout 90s |
| `TaskComplete` | `session-end` | | x | | Codex-specific — reuses `session-end` handler; timeout 90s |
| `TurnAborted` | `task-aborted` | | x | | Codex-specific |

## Recipes

### Add a new slash command / agent / skill

Drop the markdown file into the matching `plugin/` subtree and regenerate:

```bash
# examples
$EDITOR plugin/commands/mycmd.md
$EDITOR plugin/agents/my-agent.md
$EDITOR plugin/skills/my-skill/SKILL.md

wipnote plugin build-ports
```

Every target picks the new asset up automatically — no manifest edit needed,
because `manifest.json → assetSources` already points at `plugin/{commands,agents,skills,…}`.

### Add a new hook event

Three places, always in this order:

1. **Manifest** (`packages/plugin-core/manifest.json`) — add one entry to
   `hooks.events`, listing the event name, handler, and targets:

   ```json
   { "name": "MyNewEvent", "handler": "my-new-event", "targets": ["claude", "codex"] }
   ```

   Optional keys: `matcher` (e.g. `"ExitPlanMode"`), `timeout` (seconds),
   `command` (escape hatch for shell-only hooks; bypasses `handler`).

2. **Go handler** (`internal/hooks/my_new_event.go`) — implement the handler with
   the signature matching the wiring you'll use:

   ```go
   package hooks

   func MyNewEvent(event *CloudEvent, db *sql.DB) (*HookResult, error) {
       // business logic
       return &HookResult{}, nil
   }
   ```

3. **Route** (`cmd/wipnote/hook.go`) — register the CLI subcommand so
   `wipnote hook my-new-event` resolves to the Go handler:

   ```go
   hookSubcmd("my-new-event", "Handle MyNewEvent event", emptyResult, hooks.MyNewEvent),
   ```

   Use `hookSubcmdWithProject(...)` instead when the handler needs the project
   dir passed through (see `session-start` for the pattern).

Then run `wipnote plugin build-ports && wipnote build` and update the
**Hook event matrix** table above.

### Add a new harness — complete checklist

A new harness requires changes in both the plugin build layer (steps 1–3) and
the Go runtime layer (steps 4–6). Follow the steps in order; validate at the end.

Antigravity is the current reference for the plugin build layer —
see `port/pluginbuild/antigravity.go` for the canonical adapter pattern (it reuses
translation helpers from the retired Gemini CLI adapter, kept at
`port/pluginbuild/gemini_compat.go`, since Antigravity descends from Gemini CLI's
extension conventions).

**Plugin build layer**

1. **Manifest** — add a `targets.<name>` entry to
   `packages/plugin-core/manifest.json`. Alongside `outDir`, `manifestPath`,
   `hooksPath`, and the optional `mcpPath`, the schema also supports:

   - `contextFile` — path (relative to the repo root) of a context/instruction
     file that should be copied into the target tree. Antigravity uses this for
     its `plugin/contexts/ANTIGRAVITY.md` file.
   - `commandNamespace` — sub-directory under `commands/` that holds the
     target's slash commands. Antigravity groups its translated TOML commands
     under a namespace so they don't collide with other extensions.

   Example:

   ```json
   "mytool": {
     "outDir": "packages/mytool-extension",
     "manifestPath": "mytool-extension.json",
     "hooksPath": "hooks/hooks.json",
     "contextFile": "MYTOOL.md",
     "commandNamespace": "wipnote"
   }
   ```

   Then tag each applicable hook event in `hooks.events` with `"mytool"` in its
   `targets` list.

2. **Hook events** — in the manifest, tag relevant hook events with the new
   target name in their `targets` list so the build emits hook configs for it.

3. **Plugin adapter** — implement the `Adapter` interface in a new file under
   `port/pluginbuild/` (model it on `claude.go` / `codex.go` / `antigravity.go`):

   ```go
   package pluginbuild

   type mytoolAdapter struct{}

   func init() { Register(mytoolAdapter{}) }

   func (mytoolAdapter) Name() string { return "mytool" }

   func (mytoolAdapter) Emit(m *Manifest, repoRoot, outDir string) error {
       // 1. write the target-specific plugin.json from m (use writeJSON)
       // 2. write the target-specific hooks.json from m.Hooks.Events
       //    (filter with HookEvent.AppliesTo("mytool"))
       // 3. copy assets with copyAssetTree(...) using m.AssetSources
       return nil
   }
   ```

   The `Adapter` interface is defined in `port/pluginbuild/adapter.go`:

   - `Name() string` — must match the manifest `targets.<name>` key.
   - `Emit(m *Manifest, repoRoot, outDir string) error` — write the full tree
     rooted at `outDir`.

   `init()` must call `Register(...)` so the target is discoverable by
   `wipnote plugin build-ports --target <name>`. Duplicate registrations panic.

   **Sub-emitters for format translation.** If the target needs per-asset
   translation (e.g. Antigravity's markdown-to-TOML slash commands), do **not**
   extend `copyAssetTree` — add a sub-emitter function instead that registers
   a callback in the adapter's sub-emitter slice. The parent adapter iterates
   its sub-emitter slice, so each phase or format converter can land
   independently. See `port/pluginbuild/antigravity.go` for the canonical
   registration pattern (the `antigravitySubEmitters` slice and
   `AntigravitySubEmitter` signature).

**Go runtime layer**

4. **Harness registry** — create `internal/harness/registry_<name>.go`
   registering a `HarnessConfig` via `init()`:

   - `ID` — DB-canonical identifier written to `agent_events.harness`; must
     match the corresponding `otel.Harness*` constant (verified by test).
   - `AgentID` — value set in `WIPNOTE_AGENT_ID` by the launcher; used by
     `detectHarnessWithEnv` for disambiguation.
   - `ServiceNames` — OTel `resource.service.name` values emitted by this
     harness. Use a slice if the harness has multiple variants.
   - `SessionAttr` — OTel attribute key whose value becomes `SessionID` in
     `UnifiedSignal`. Claude and Antigravity use `"session.id"`; Codex uses
     `"conversation.id"`.
   - `HookEventNames` — native `hook_event_name` values emitted by this
     harness. Non-empty for Antigravity only (inherited from the Gemini-CLI
     hook vocabulary it descends from); Claude and Codex leave it nil.
   - `HooksHarness` — a new iota constant added to
     `core/harness/registry.go`. The ordering MUST match
     `hooks.HarnessClaude/Codex/Gemini/Antigravity` exactly (Gemini's iota slot
     is retained for index stability even though it's no longer an active
     launch target — see feat-02f25a24); verified by
     `TestRegistry_HooksHarnessMatchesHooksConst`.
   - `OtelEnv` — func returning OTel env vars to inject at launch time; must
     be non-nil for every harness except Claude.

5. **OTel adapter** — add `internal/otel/adapter/<name>.go` implementing
   `otel.Adapter`. The `Identify()` method reads
   `harness.Get(<id>).ServiceNames`; session ID resolution reads
   `harness.Get(<id>).SessionAttr`. Do NOT hardcode service names or attribute
   keys — read them from the registry.

6. **Launcher** — add `cmd/wipnote/<name>_launch.go`. Inject OTel env vars via
   `harness.Get(<id>).OtelEnv(port, sessionID)` and agent attribution vars via
   `harness.Get(<id>).BuildAgentEnv()`. Do NOT hardcode env var names.

**Validate**

7. Run the full quality gate and confirm the new harness appears:

   ```bash
   wipnote plugin build-ports
   go build ./... && go vet ./... && go test ./...
   wipnote harness list   # confirm the new harness row appears
   ```
