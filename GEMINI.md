# HtmlGraph for Gemini

**MANDATORY instructions for Google Gemini AI agents working with HtmlGraph projects.**

---

## 📚 REQUIRED READING - DO THIS FIRST

**→ READ [AGENTS.md](./AGENTS.md) BEFORE USING HTMLGRAPH**

The AGENTS.md file contains ALL core documentation:
- ✅ **Python SDK Quick Start** - REQUIRED installation and usage
- ✅ **Deployment Instructions** - How to use `deploy-all.sh`
- ✅ **API & CLI Alternatives** - When SDK isn't available
- ✅ **Best Practices** - MUST-FOLLOW patterns for AI agents
- ✅ **Complete Workflow Examples** - Copy these patterns
- ✅ **API Reference** - Full method documentation

**DO NOT proceed without reading AGENTS.md first.**

---

## Gemini-Specific REQUIREMENTS

### ABSOLUTE RULE: Use SDK, Never Direct File Edits

**CRITICAL: NEVER use file operations on `.htmlgraph/` HTML files.**

❌ **FORBIDDEN:**
```python
# NEVER DO THIS
Write('/path/to/.htmlgraph/features/feature-123.html', ...)
Edit('/path/to/.htmlgraph/sessions/session-456.html', ...)
```

✅ **REQUIRED - Use SDK:**
```python
from htmlgraph import SDK

# ALWAYS initialize with agent="gemini"
sdk = SDK(agent="gemini")

# Get project summary (DO THIS at session start)
print(sdk.summary(max_items=10))

# Create features (USE builder pattern)
feature = sdk.features.create("Implement Search") \
    .set_priority("high") \
    .add_steps([
        "Design search UI",
        "Add search endpoint",
        "Implement indexing"
    ]) \
    .save()

# Update features (USE context manager for auto-save)
with sdk.features.edit(feature.id) as f:
    f.status = "in-progress"
    f.agent_assigned = "gemini"
```

### Gemini Extension Integration

The HtmlGraph Gemini extension is located at `packages/gemini-extension/`.

**Installation:**
```bash
# Development
cd packages/gemini-extension
# Load as unpacked extension in Gemini

# Production
# Extension marketplace distribution (TBD)
```

**Extension Files:**
- `gemini-extension.json` - Extension manifest
- `skills/` - Gemini-specific skills
- `commands/` - Slash commands (auto-generated from YAML)

---

## Commands Available in Gemini

All HtmlGraph commands are available in Gemini through the extension:

- `/htmlgraph:start` - Start session with project context
- `/htmlgraph:status` - Check current status
- `/htmlgraph:plan` - Smart planning workflow
- `/htmlgraph:spike` - Create research spike
- `/htmlgraph:recommend` - Get strategic recommendations
- `/htmlgraph:end` - End session with summary

**→ Full command reference in [AGENTS.md](./AGENTS.md)**

---

## Platform Differences

### Gemini vs Claude Code

| Feature | Gemini | Claude Code |
|---------|--------|-------------|
| SDK Access | ✅ Full | ✅ Full |
| Slash Commands | ✅ Via Extension | ✅ Via Plugin |
| Dashboard | ✅ Browser | ✅ Browser |
| CLI Integration | ✅ Same | ✅ Same |

**Both platforms use the same:**
- Python SDK (`htmlgraph` package)
- REST API surface (`htmlgraph serve` defaults to `:8080`; `htmlgraph serve-api` defaults to `:8000`)
- CLI commands (`uv run htmlgraph`)
- HTML file format

---

## Integration Pattern

```python
# Gemini Code Assist workflow
def gemini_workflow():
    """Example workflow for Gemini agents."""
    from htmlgraph import SDK

    # 1. Initialize with Gemini identifier
    sdk = SDK(agent="gemini")

    # 2. Get recommendations
    recs = sdk.recommend_next_work(agent_count=1)
    if recs:
        print(f"Recommended: {recs[0]['title']}")

    # 3. Get next task
    task = sdk.next_task(priority="high", auto_claim=True)

    if task:
        print(f"Working on: {task.title}")

        # 4. Complete work
        with sdk.features.edit(task.id) as f:
            for i, step in enumerate(f.steps):
                if not step.completed:
                    # Do the work...
                    step.completed = True
                    step.agent = "gemini"
                    break

        print("Step completed!")
```

---

## Troubleshooting

### Extension Not Loading

Check extension status in Gemini settings:
```
Gemini Settings → Extensions → HtmlGraph
```

### Commands Not Available

Regenerate commands from YAML:
```bash
cd packages/gemini-extension
uv run python ../common/generators/generate_commands.py
# Reload extension
```

### SDK Import Errors

Ensure htmlgraph is installed:
```bash
uv pip install htmlgraph
# or
pip install htmlgraph
```

---

## Deployment & Release

### Using the Deployment Script (FLEXIBLE OPTIONS)

**CRITICAL: Use `./scripts/deploy-all.sh` for all deployment operations.**

**Quick Usage:**
```bash
# Documentation changes only (commit + push)
./scripts/deploy-all.sh --docs-only

# Full release (all 7 steps)
./scripts/deploy-all.sh 0.7.1

# Build package only (test builds)
./scripts/deploy-all.sh --build-only

# Skip PyPI publishing (build + install only)
./scripts/deploy-all.sh 0.7.1 --skip-pypi

# Preview what would happen (dry-run)
./scripts/deploy-all.sh --dry-run

# Show all options
./scripts/deploy-all.sh --help
```

**Available Flags:**
- `--docs-only` - Only commit and push to git (skip build/publish)
- `--build-only` - Only build package (skip git/publish/install)
- `--skip-pypi` - Skip PyPI publishing step
- `--skip-plugins` - Skip plugin update steps
- `--dry-run` - Show what would happen without executing

**What the Script Does (7 Steps):**
1. **Git Push** - Push commits and tags to origin/main
2. **Build Package** - Create wheel and source distributions
3. **Publish to PyPI** - Upload package to PyPI
4. **Local Install** - Install latest version locally
5. **Update Claude Plugin** - Run `claude plugin update htmlgraph`
6. **Update Gemini Extension** - Update version in gemini-extension.json
7. **Update Codex Skill** - Check for Codex and update if present

**See:** `scripts/README.md` for complete documentation

---

## Memory File Synchronization

**CRITICAL: Use `uv run htmlgraph sync-docs` to maintain documentation consistency.**

HtmlGraph uses a centralized documentation pattern:
- **AGENTS.md** - Single source of truth (SDK, API, CLI, workflows)
- **CLAUDE.md** - Platform-specific notes + references AGENTS.md
- **GEMINI.md** - Platform-specific notes + references AGENTS.md

**Quick Usage:**
```bash
# Check if files are synchronized
uv run htmlgraph sync-docs --check

# Generate platform-specific file
uv run htmlgraph sync-docs --generate gemini
uv run htmlgraph sync-docs --generate claude

# Synchronize all files (default)
uv run htmlgraph sync-docs
```

**Why This Matters:**
- ✅ Single source of truth in AGENTS.md
- ✅ Platform-specific notes in separate files
- ✅ Easy maintenance (update once, not 3+ times)
- ✅ Consistency across all platforms

**See:** `scripts/README.md` for complete documentation

---

## Documentation

- **Main Guide**: [AGENTS.md](./AGENTS.md) - Complete AI agent documentation
- **Deployment**: [AGENTS.md#deployment--release](./AGENTS.md#deployment--release)
- **SDK Reference**: `docs/SDK_FOR_AI_AGENTS.md`
- **Extension Code**: `packages/gemini-extension/`

---

**→ For complete documentation, see [AGENTS.md](./AGENTS.md)**
