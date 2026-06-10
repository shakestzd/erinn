# wipnote for Gemini

**MANDATORY instructions for Google Gemini AI agents working with wipnote projects.**

---

## REQUIRED READING - DO THIS FIRST

**→ READ [AGENTS.md](./AGENTS.md) BEFORE USING WIPNOTE**

The AGENTS.md file contains ALL core documentation:
- ✅ **CLI Quick Start** - REQUIRED commands and usage
- ✅ **Deployment Instructions** - How to use `deploy-all.sh`
- ✅ **REST API** - When CLI isn't available
- ✅ **Best Practices** - MUST-FOLLOW patterns for AI agents
- ✅ **Complete Workflow Examples** - Copy these patterns
- ✅ **CLI Reference** - Full command documentation

**DO NOT proceed without reading AGENTS.md first.**

---

## Gemini-Specific REQUIREMENTS

### ABSOLUTE RULE: Use CLI, Never Direct File Edits

**CRITICAL: NEVER use file operations on `.wipnote/` HTML files.**

❌ **FORBIDDEN:**
```python
# NEVER DO THIS
Write('/path/to/.wipnote/features/feature-123.html', ...)
Edit('/path/to/.wipnote/sessions/session-456.html', ...)
```

✅ **REQUIRED - Use CLI:**
```bash
# Get project summary (DO THIS at session start)
wipnote snapshot --summary

# Create a feature
wipnote feature create "Implement Search"
# Returns: feat-abc12345

# Start working on it
wipnote feature start feat-abc12345
```

### Gemini Extension Integration

The wipnote Gemini extension is located at `packages/gemini-extension/`.

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

All wipnote commands are available in Gemini through the extension:

- `/wipnote:start` - Start session with project context
- `/wipnote:status` - Check current status
- `/wipnote:plan` - Smart planning workflow
- `/wipnote:spike` - Create research spike
- `/wipnote:recommend` - Get strategic recommendations
- `/wipnote:end` - End session with summary

**→ Full command reference in [AGENTS.md](./AGENTS.md)**

---

## Platform Differences

### Gemini vs Claude Code

| Feature | Gemini | Claude Code |
|---------|--------|-------------|
| CLI Access | ✅ Full | ✅ Full |
| Slash Commands | ✅ Via Extension | ✅ Via Plugin |
| Dashboard | ✅ Browser | ✅ Browser |
| REST API | ✅ Same | ✅ Same |

**Both platforms use the same:**
- Go CLI binary (`wipnote`)
- REST API (port 8080)
- CLI commands (`uvx wipnote`)
- HTML file format

---

## Integration Pattern

```bash
# Gemini Code Assist workflow

# 1. Get oriented
wipnote snapshot --summary

# 2. Get recommendations
wipnote analytics recommend

# 3. Find next high-priority task
wipnote find features --status todo

# 4. Start working on it
wipnote feature start feat-abc12345

# 5. (Do the actual implementation work...)

# 6. Complete the feature
wipnote feature complete feat-abc12345
```

---

## Troubleshooting

### Extension Not Loading

Check extension status in Gemini settings:
```
Gemini Settings → Extensions → wipnote
```

### Commands Not Available

Regenerate commands from YAML:
```bash
cd packages/gemini-extension
uv run python ../common/generators/generate_commands.py
# Reload extension
```

### CLI Not Found

Ensure wipnote is installed:
```bash
uv pip install wipnote
# or
pip install wipnote
# Verify
wipnote version
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
5. **Update Claude Plugin** - Run `claude plugin update wipnote`
6. **Update Gemini Extension** - Update version in gemini-extension.json
7. **Update Codex Skill** - Check for Codex and update if present

**See:** `scripts/README.md` for complete documentation

---

## Memory File Synchronization

**CRITICAL: Use `uvx wipnote sync-docs` to maintain documentation consistency.**

wipnote uses a centralized documentation pattern:
- **AGENTS.md** - Single source of truth (SDK, API, CLI, workflows)
- **CLAUDE.md** - Platform-specific notes + references AGENTS.md
- **GEMINI.md** - Platform-specific notes + references AGENTS.md

**Quick Usage:**
```bash
# Check if files are synchronized
uvx wipnote sync-docs --check

# Generate platform-specific file
uvx wipnote sync-docs --generate gemini
uvx wipnote sync-docs --generate claude

# Synchronize all files (default)
uvx wipnote sync-docs
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
