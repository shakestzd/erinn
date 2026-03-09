# HtmlGraph

## For AI Agents

**Documentation:** [AGENTS.md](./AGENTS.md) | **Gemini:** [GEMINI.md](./GEMINI.md)

---

## Project Vision

Local-first observability and coordination platform for AI-assisted development.

- **Work items** (features, bugs, spikes, tracks) stored as HTML files -- durable, human-readable, git-diffable
- **SQLite** is canonical for operational querying, dashboard views, analytics, and search
- **JSONL event log** provides append-only history of all agent activity
- **FastAPI + HTMX + SSE/WebSocket** serves the live dashboard
- Single-process, local-first stack -- no external database server required

---

## Orchestrator Mode

**Delegate ALL operations except:** `Task()`, `AskUserQuestion()`, `TodoWrite()`, SDK operations.

**For complete patterns:** Use `/orchestrator-directives` skill

---

## Code Quality

```bash
uv run ruff check --fix && uv run ruff format && uv run mypy src/ && uv run pytest
# Commit only when ALL pass
```

**For complete workflow:** Use `/code-quality` skill

---

## Deployment

```bash
uv run pytest                              # Run tests
./scripts/deploy-all.sh 0.9.4 --no-confirm  # Deploy
```

**For complete workflow:** Use `/deployment-automation` skill

---

## Quick Commands

| Task | Command |
|------|---------|
| View work | `uv run htmlgraph snapshot --summary` |
| Run tests | `uv run pytest` |
| Lint | `uv run ruff check --fix` |
| Type check | `uv run mypy src/` |
| Deploy | `./scripts/deploy-all.sh VERSION --no-confirm` |
| Serve dashboard | `uv run htmlgraph serve` |
| Status | `uv run htmlgraph status` |

---

## Development Mode

**CRITICAL: Hooks load htmlgraph from PyPI, not local source, even in dev mode.**

### What Dev Mode Does

Dev mode enables local plugin development by loading the plugin directly from the source directory instead of from the Claude Code marketplace. This allows you to:
- Test changes to commands, agents, skills, and hooks immediately
- Work with the latest code without deploying to PyPI
- Debug plugin functionality in a live Claude Code session

### Starting Dev Mode

```bash
uv run htmlgraph claude --dev
```

This launches Claude Code with:
- Plugin loaded from local source: `packages/claude-plugin/`
- Orchestrator system prompt injected
- Multi-AI delegation rules enabled
- All slash commands available with the plugin namespace prefix

### Plugin Directory Structure

When dev mode runs, it needs to find all plugin components. The structure must be:

```
packages/claude-plugin/              ← PLUGIN ROOT (passed to --plugin-dir)
├── .claude-plugin/
│   └── plugin.json                  ← Only this file in .claude-plugin
├── commands/                        ← At plugin root (NOT in .claude-plugin)
│   ├── deploy.md
│   ├── init.md
│   ├── plan.md
│   └── ...
├── agents/                          ← At plugin root
│   └── agent-definition.md
├── skills/                          ← At plugin root
│   ├── gemini/
│   │   └── SKILL.md                 ← Must be uppercase SKILL.md
│   ├── codex/
│   │   └── SKILL.md
│   └── copilot/
│       └── SKILL.md
└── hooks/                           ← At plugin root
    ├── hooks.json
    └── scripts/
        ├── session-start.py
        └── ...
```

**CRITICAL MISTAKE TO AVOID:** Don't put `commands/`, `agents/`, `skills/`, or `hooks/` inside `.claude-plugin/`. According to Claude Code documentation, only `plugin.json` belongs in `.claude-plugin/`. All other directories must be at the plugin root level.

### How Dev Mode Plugin Loading Works

1. **`get_plugin_dir()` returns the plugin root:** `packages/claude-plugin/`
2. **This directory is passed to Claude Code:** `claude --plugin-dir ./packages/claude-plugin`
3. **Claude Code scans the root directory for:**
   - `.claude-plugin/plugin.json` - Plugin metadata
   - `commands/` - Slash commands (discovered automatically)
   - `agents/` - Agent definitions (discovered automatically)
   - `skills/` - Agent skills with `SKILL.md` files (discovered automatically)
   - `hooks/` - Hook definitions in `hooks.json` (loaded automatically)
4. **Commands appear namespaced:** `/htmlgraph:deploy`, `/htmlgraph:init`, etc.

### Verifying Dev Mode Components

After running `uv run htmlgraph claude --dev`, you should see:

✅ **Slash commands** visible in `/help`:
- `/htmlgraph:deploy`
- `/htmlgraph:init`
- `/htmlgraph:plan`
- `/htmlgraph:research`
- `/htmlgraph:status`
- etc.

✅ **Agent skills** available to Claude when working on relevant tasks (automatic based on context)

✅ **Hooks** executing based on Claude Code events (PreToolUse, PostToolUse, etc.)

If commands don't appear, verify:
1. `get_plugin_dir()` returns the correct path (root, not `.claude-plugin`)
2. Command files exist in `packages/claude-plugin/commands/`
3. Skill files are named `SKILL.md` (uppercase), not `skill.md`
4. No files are in `.claude-plugin/` except `plugin.json`

### How Hooks Load HtmlGraph

**Hook shebangs use:**
```python
#!/usr/bin/env -S uv run --with htmlgraph
```

**Key behavior:**
- `--with htmlgraph` always pulls **latest version from PyPI**
- Even when running from project root, hooks use PyPI package
- No version pinning (always gets latest)
- No need to edit hooks when releasing new versions

### Why PyPI in Dev Mode?

**Testing in production-like environment:**
- Ensures changes work the same way for users
- Catches integration issues before distribution
- No surprises when hooks run in production
- Single source of truth (PyPI package)

### Development Workflow

1. **Make changes** to `src/python/htmlgraph/`
2. **Run tests** locally: `uv run pytest`
3. **Deploy to PyPI**: `./scripts/deploy-all.sh X.Y.Z --no-confirm`
4. **Restart Claude**: Hooks automatically load new version from PyPI
5. **Verify**: Check that changes work correctly

### Session ID Fix (v0.26.3)

**Problem:** PostToolUse hooks don't receive `session_id` in hook_input from Claude Code.

**Solution:** Database fallback query finds session with most recent UserQuery event:
```python
# In src/python/htmlgraph/hooks/context.py
cursor.execute("""
    SELECT session_id FROM agent_events
    WHERE tool_name = 'UserQuery'
    ORDER BY timestamp DESC
    LIMIT 1
""")
```

**Why this works:**
- UserPromptSubmit hooks DO receive `session_id` from Claude Code
- They create UserQuery events with correct session_id
- PostToolUse hooks query database for that session
- All events (UserQuery + tool events) share same session_id

**Verification after restart:**
```bash
sqlite3 .htmlgraph/htmlgraph.db "
SELECT session_id, tool_name, COUNT(*)
FROM agent_events
WHERE session_id = (SELECT session_id FROM sessions ORDER BY created_at DESC LIMIT 1)
GROUP BY tool_name
ORDER BY COUNT(*) DESC;
"
# Should show UserQuery, Bash, Read, etc. all with SAME session_id
```

### Troubleshooting Dev Mode

**Hooks not executing?**
- Check PyPI package is latest: `pip show htmlgraph`
- Verify hooks are executable: `ls -la packages/claude-plugin/.claude-plugin/hooks/scripts/`
- Check hook shebangs: `head -1 packages/claude-plugin/.claude-plugin/hooks/scripts/*.py`

**Session IDs still mismatched?**
- Query database for UserQuery events: `sqlite3 .htmlgraph/htmlgraph.db "SELECT session_id FROM agent_events WHERE tool_name='UserQuery' ORDER BY timestamp DESC LIMIT 1;"`
- Check active sessions: `sqlite3 .htmlgraph/htmlgraph.db "SELECT session_id, status FROM sessions WHERE status='active';"`
- Verify fix is deployed: Check that v0.26.3+ is on PyPI

**Local changes not reflected?**
- Hooks load from PyPI, not local source
- Must deploy to PyPI for hooks to see changes
- Use incremental versions (0.26.2 → 0.26.3 → 0.26.4)

---

## System Prompt Persistence & Delegation Enforcement

**Automatic context injection across session boundaries with cost-optimal delegation.**

Your project's critical guidance (model selection, delegation patterns, quality gates) persists via `.claude/system-prompt.md` and auto-injects at session start, surviving compact/resume cycles.

**Quick Setup**: Create `.claude/system-prompt.md` with project guidance
**Verification**: Run `uv run pytest tests/hooks/test_system_prompt_persistence.py`
**Test Coverage**: 52 unit tests + 31 integration tests + 8 post-compact tests, 98% coverage

### Documentation Guides

| Guide | Audience | Purpose |
|-------|----------|---------|
| [System Prompt Quick Start](./docs/SYSTEM_PROMPT_QUICK_START.md) | Users | Create and customize your system prompt (5-min setup) |
| [System Prompt Architecture](./docs/SYSTEM_PROMPT_ARCHITECTURE.md) | Developers | Deep technical dive + troubleshooting |
| [Delegation Enforcement Admin Guide](./docs/DELEGATION_ENFORCEMENT_ADMIN_GUIDE.md) | Admins/Teams | Setup and monitor delegation enforcement across your team |
| [System Prompt Developer Guide](./docs/SYSTEM_PROMPT_DEVELOPER_GUIDE.md) | Developers | Extend system with custom layers, hooks, and skills |

**Start here**: [System Prompt Quick Start](./docs/SYSTEM_PROMPT_QUICK_START.md)

---

## Debugging Workflow

**CRITICAL: Research first, implement second.**

```bash
# Built-in debug tools
claude --debug <command>    # Verbose output
/hooks                      # List active hooks
/doctor                     # System diagnostics
```

**For complete workflow:** Use `/debugging-workflow` skill

---

## Memory Sync

**Keep documentation synchronized across platforms.**

```bash
uv run htmlgraph sync-docs           # Sync all files
uv run htmlgraph sync-docs --check   # Check sync status
```

**For complete workflow:** Use `/memory-sync` skill

---

## Dogfooding

This project uses HtmlGraph to develop HtmlGraph. The `.htmlgraph/` directory contains real usage examples.

---

## Hook & Plugin Development

**CRITICAL: ALL Claude Code integrations (hooks, agents, skills) must be built in the PLUGIN SOURCE.**

**Plugin Source:** `packages/claude-plugin/.claude-plugin/`
**Do NOT edit:** `.claude/` directory (auto-synced from plugin)

### Plugin Components - What Belongs in the Plugin

Everything that extends Claude Code functionality should be in `packages/claude-plugin/.claude-plugin/`:

#### 1. **Hooks** (All CloudEvent handlers)
   - **Location:** `packages/claude-plugin/.claude-plugin/hooks/`
   - **What:** Python scripts that respond to Claude Code events
   - **Examples:**
     - `session-start.py` - Runs when Claude Code session starts
     - `user-prompt-submit.py` - Runs when user submits a prompt (creates UserQuery events)
     - `track-event.py` - Records all tool calls and completions to database
     - `pretooluse-integrator.py` - Track tool use and link to parent activities
     - `session-end.py` - Cleanup when session ends
     - `subagent-stop.py` - Handles subagent completion
   - **Why plugin:** Hooks are Claude Code infrastructure—must be packaged for distribution

#### 2. **Skills** (User-invocable commands)
   - **Location:** `packages/claude-plugin/.claude-plugin/skills/`
   - **What:** Markdown skill definitions + embedded Python for orchestration
   - **Current Examples:**
     - `/orchestrator-directives` - Delegation patterns
     - `/code-quality` - Quality gate workflow
     - `/deployment-automation` - Release process
     - `/debugging-workflow` - Debug methodology
     - `/memory-sync` - Doc synchronization
   - **Why plugin:** Skills are Claude Code UI components—must be packaged for distribution

#### 3. **Plugin Configuration**
   - **Location:** `packages/claude-plugin/.claude-plugin/plugin.json`
   - **What:** Plugin metadata, MCP server configurations
   - **Includes:**
     - Plugin name, version, description
     - MCP server configurations
   - **Why plugin:** Defines how Claude Code loads and runs the plugin

#### 4. **Configuration & Prompts**
   - **Location:** `packages/claude-plugin/.claude-plugin/config/`
   - **What:** System prompts, classification rules, drift thresholds
   - **Examples:**
     - `classification-prompt.md` - Prompt for work type classification
     - `drift-config.json` - Context drift detection settings
   - **Why plugin:** Shared across all users; updates distributed via plugin

### Directory Structure

```
packages/claude-plugin/.claude-plugin/  <-- SOURCE (make changes here)
├── hooks/
│   ├── hooks.json                   ← Hook event routing
│   └── scripts/
│       ├── session-start.py         ← Database session creation
│       ├── user-prompt-submit.py    ← UserQuery event creation
│       ├── track-event.py           ← All event tracking
│       ├── pretooluse-integrator.py ← Track tool use and link to parent activities
│       ├── posttooluse-integrator.py ← Activity linking
│       ├── session-end.py           ← Session cleanup
│       └── subagent-stop.py         ← Subagent completion
├── skills/
│   ├── orchestrator-directives/     ← Delegation patterns
│   ├── code-quality/                ← Quality gates
│   ├── deployment-automation/       ← Release workflow
│   ├── debugging-workflow/          ← Debug methodology
│   └── memory-sync/                 ← Doc synchronization
├── config/
│   ├── classification-prompt.md     ← Work classification AI
│   └── drift-config.json            ← Drift thresholds
└── plugin.json                      ← Plugin metadata

.claude/  <-- AUTO-SYNCED (do not edit)
├── hooks/ (synced from plugin)
├── skills/ (synced from plugin)
└── config/ (synced from plugin)
```

### Critical Rule: Single Source of Truth

**NEVER edit `.claude/` expecting changes to persist.**

- ❌ Edit `.claude/hooks/hooks.json` → Changes lost on plugin update
- ❌ Edit `.claude/hooks/scripts/*.py` → Changes lost on plugin update
- ❌ Edit `.claude/agents/` → Changes lost on plugin update
- ❌ Add hooks to `.claude/` → Not published, not shareable

**ALWAYS edit in plugin source:**

- ✅ Edit `packages/claude-plugin/.claude-plugin/hooks/hooks.json`
- ✅ Edit `packages/claude-plugin/.claude-plugin/hooks/scripts/*.py`
- ✅ Add agents to `packages/claude-plugin/.claude-plugin/agents/`
- ✅ Add skills to `packages/claude-plugin/.claude-plugin/skills/`

### Workflow: Making Changes to Plugin

1. **Make changes in plugin source:**
   ```bash
   # Edit files in packages/claude-plugin/.claude-plugin/
   vim packages/claude-plugin/.claude-plugin/hooks/scripts/user-prompt-submit.py
   vim packages/claude-plugin/.claude-plugin/plugin.json
   ```

2. **Run quality checks:**
   ```bash
   uv run ruff check --fix && uv run ruff format && uv run mypy src/ && uv run pytest
   ```

3. **Verify plugin is synced (in dev mode, hooks run from plugin source):**
   ```bash
   # In dev mode, Claude Code runs hooks from plugin source directly
   # No need to manually sync during development
   ```

4. **Commit changes:**
   ```bash
   git add packages/claude-plugin/.claude-plugin/
   git commit -m "fix: update hook X with Y changes"
   ```

5. **Deploy (publishes plugin update):**
   ```bash
   ./scripts/deploy-all.sh 0.9.7 --no-confirm
   # This updates version in plugin.json and publishes to distribution

### Never Do This

- Edit `.claude/hooks/hooks.json` directly
- Edit `.claude/hooks/scripts/*.py` directly
- Edit `.claude/agents/` directly
- Add new hooks to `.claude/` expecting them to run
- Make changes to `.claude/` expecting them to persist

### Always Do This

- Edit `packages/claude-plugin/.claude-plugin/hooks/hooks.json`
- Edit `packages/claude-plugin/.claude-plugin/hooks/scripts/*.py`
- Add agents to `packages/claude-plugin/.claude-plugin/agents/`
- Add skills to `packages/claude-plugin/.claude-plugin/skills/`
- Commit plugin source files
- Test in dev mode (hooks run from plugin automatically)

---

## Project vs General Tooling

**This project is both:**
1. **HtmlGraph Package Development** - Building the tool itself
2. **HtmlGraph Dogfooding** - Using the tool to build itself

**CLAUDE.md contains:**
- ✅ Project-specific: Deployment, testing, debugging HtmlGraph package
- ✅ Quick reference: Links to skills for general patterns

**Plugin/Skills contain:**
- ✅ General patterns: Orchestration, coordination (for all users)
- ✅ Progressive disclosure: Load details on-demand

---

## Skills Reference

| Skill | Use For |
|-------|---------|
| `/orchestrator-directives` | Delegation patterns, decision framework |
| `/code-quality` | Lint, type check, testing workflow |
| `/deployment-automation` | Release, versioning, PyPI publishing |
| `/debugging-workflow` | Research-first debugging methodology |
| `/memory-sync` | Documentation synchronization |

---

## Rules Reference

Detailed rules in `.claude/rules/`:
- `orchestration.md` - Complete orchestrator directives
- `code-hygiene.md` - Quality standards
- `deployment.md` - Release workflow
- `debugging.md` - Debug methodology
- `dogfooding.md` - Self-hosting context
