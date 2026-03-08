# HtmlGraph - "HTML is All You Need"

## For AI Agents

**Documentation:** [AGENTS.md](./AGENTS.md) | **Gemini:** [GEMINI.md](./GEMINI.md)

---

## Project Vision

Lightweight graph database with document-native graph storage and database-backed observability for AI agent coordination.

- HTML files = Graph nodes
- Hyperlinks = Graph edges
- CSS selectors = Query language
- JSONL + SQLite = event history, analytics, and dashboard queries

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

**CRITICAL: Hooks run from the active environment and bootstrap local source when available.**

### What Dev Mode Does

Dev mode enables local plugin development by loading the plugin directly from the source directory instead of from the Claude Code marketplace. This allows you to:
- Test changes to commands, agents, skills, and hooks immediately
- Work with local source during development
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
│   ├── plugin.json                  ← Required plugin metadata
│   ├── marketplace.json             ← Marketplace metadata
│   └── system-prompt-default.md     ← Default prompt template
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

**CRITICAL MISTAKE TO AVOID:** Don't put `commands/`, `agents/`, `skills/`, or `hooks/` inside `.claude-plugin/`. Keep operational directories at plugin root; `.claude-plugin/` is for plugin metadata files.

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
4. `.claude-plugin/` contains metadata files only (not hooks/skills/commands/agents)

### How Hooks Load HtmlGraph

**Hook shebangs use:**
```python
#!/usr/bin/env -S uv run
```

**Key behavior:**
- Hooks run with the active `uv`/Python environment.
- Hook bootstrap adds `src/python` when running in the htmlgraph repo.
- In project repos, bootstrap resolves `.venv` site-packages when available.
- Hook execution is not hard-pinned to `uv run --with htmlgraph`.

### Why This Helps in Dev Mode?

**Testing in production-like environment:**
- Local source can be validated immediately in-repo.
- Installed package behavior can still be validated before release.
- Reduces confusion about which runtime hooks are executing under.

### Development Workflow

1. **Make changes** to `src/python/htmlgraph/`
2. **Run tests** locally: `uv run pytest`
3. **Restart Claude/dev session** so plugin/hooks reload cleanly
4. **Verify** behavior end-to-end
5. **Optional release validation**: deploy package and re-test in a clean environment

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
- Check hook runtime import path: `uv run python -c "import htmlgraph; print(htmlgraph.__file__)"`
- Verify hooks are executable: `ls -la packages/claude-plugin/hooks/scripts/`
- Check hook shebangs: `head -1 packages/claude-plugin/hooks/scripts/*.py`

**Session IDs still mismatched?**
- Query database for UserQuery events: `sqlite3 .htmlgraph/htmlgraph.db "SELECT session_id FROM agent_events WHERE tool_name='UserQuery' ORDER BY timestamp DESC LIMIT 1;"`
- Check active sessions: `sqlite3 .htmlgraph/htmlgraph.db "SELECT session_id, status FROM sessions WHERE status='active';"`
- Verify fix is deployed: Check that v0.26.3+ is on PyPI

**Local changes not reflected?**
- Restart Claude/session so hooks reload
- Confirm bootstrap can see `src/python` (repo mode) or `.venv` site-packages (project mode)
- If validating packaged behavior, deploy a new version and retest in a clean environment

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

**Plugin Source Root:** `packages/claude-plugin/`
**Do NOT edit:** `.claude/` directory (auto-synced from plugin)

### Plugin Components - What Belongs in the Plugin

Everything that extends Claude Code functionality should be in `packages/claude-plugin/`:

#### 1. **Hooks** (All CloudEvent handlers)
   - **Location:** `packages/claude-plugin/hooks/`
   - **What:** Python scripts that respond to Claude Code events
   - **Examples:** `hooks/scripts/session-start.py`, `hooks/scripts/user-prompt-submit.py`, `hooks/scripts/track-event.py`
   - **Why plugin:** Hooks are Claude Code infrastructure—must be packaged for distribution

#### 2. **Skills** (User-invocable commands)
   - **Location:** `packages/claude-plugin/skills/`
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
   - **Why plugin:** Defines how Claude Code loads and runs the plugin

#### 4. **Shared Config & Prompts**
   - **Location:** `packages/claude-plugin/config/`
   - **What:** Prompt fragments and runtime configuration used by plugin hooks/skills

### Directory Structure

```
packages/claude-plugin/  <-- SOURCE (make changes here)
├── .claude-plugin/
│   ├── plugin.json                  ← Plugin metadata
│   ├── marketplace.json             ← Marketplace metadata
│   └── system-prompt-default.md     ← Default prompt template
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
├── commands/                        ← Slash command definitions
├── agents/                          ← Agent definitions
├── skills/
│   └── ...                          ← Skill folders with SKILL.md
├── config/
│   └── ...                          ← Shared prompt/config files
└── rules/                           ← Additional plugin rule files

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

- ✅ Edit `packages/claude-plugin/hooks/hooks.json`
- ✅ Edit `packages/claude-plugin/hooks/scripts/*.py`
- ✅ Add agents to `packages/claude-plugin/agents/`
- ✅ Add skills to `packages/claude-plugin/skills/`

### Workflow: Making Changes to Plugin

1. **Make changes in plugin source:**
   ```bash
   # Edit files in packages/claude-plugin/
   vim packages/claude-plugin/hooks/scripts/user-prompt-submit.py
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
   git add packages/claude-plugin/
   git commit -m "fix: update hook X with Y changes"
   ```

5. **Deploy (publishes plugin update):**
   ```bash
   ./scripts/deploy-all.sh 0.9.7 --no-confirm
   # This updates version in plugin.json and publishes to distribution
   ```

### Never Do This

- Edit `.claude/hooks/hooks.json` directly
- Edit `.claude/hooks/scripts/*.py` directly
- Edit `.claude/agents/` directly
- Add new hooks to `.claude/` expecting them to run
- Make changes to `.claude/` expecting them to persist

### Always Do This

- Edit `packages/claude-plugin/hooks/hooks.json`
- Edit `packages/claude-plugin/hooks/scripts/*.py`
- Add agents to `packages/claude-plugin/agents/`
- Add skills to `packages/claude-plugin/skills/`
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
