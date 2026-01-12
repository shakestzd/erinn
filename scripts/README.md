# HtmlGraph Scripts

Collection of utility scripts for common development workflows.

## Quick Reference

```bash
# Install git hooks (run after cloning)
./scripts/install-hooks.sh

# Git workflow (3 commands → 1)
./scripts/git-commit-push.sh "commit message"

# Deployment (7 steps automated)
./scripts/deploy-all.sh 0.9.1

# All support --dry-run and --help
```

---

## Install Git Hooks (`install-hooks.sh`)

**Purpose**: Install pre-commit hooks for code quality checks.

**Run after cloning**:
```bash
./scripts/install-hooks.sh
```

**What it installs**:
- `pre-commit` hook that runs before every commit:
  - `ruff check` - linting
  - `ruff format --check` - formatting
  - `mypy` - type checking

Commits will be blocked if any check fails.

---

## Git Commit and Push (`git-commit-push.sh`)

**Purpose**: Systematize the common git workflow of staging, committing, and pushing.

**Reduces**:
```bash
# From this (3 bash calls):
git add -A
git commit -m "message"
git push origin main

# To this (1 bash call):
./scripts/git-commit-push.sh "message"
```

### Usage

```bash
# Basic usage
./scripts/git-commit-push.sh "chore: update session tracking"

# Skip confirmation prompt
./scripts/git-commit-push.sh "fix: deployment issues" --no-confirm

# Preview without executing
./scripts/git-commit-push.sh "feat: new feature" --dry-run

# Show help
./scripts/git-commit-push.sh --help
```

### Features

- ✅ Shows files to be committed before proceeding
- ✅ Confirms action (unless \`--no-confirm\`)
- ✅ Stages all changes (\`git add -A\`)
- ✅ Commits with provided message
- ✅ Pushes to origin/main
- ✅ Supports \`--dry-run\` for preview

### Flags

- \`--dry-run\` - Show what would happen without executing
- \`--no-confirm\` - Skip confirmation prompt
- \`--help\` - Show help message

---

## Plugin Sync Tool (`sync_plugin_to_local.py`)

**Purpose**: Maintain single source of truth by syncing `packages/claude-plugin/` → `.claude/` for dogfooding.

**What it syncs**:
- Hook scripts (`hooks/scripts/*.py`)
- Hook configuration (`hooks/hooks.json`)
- Skills (`skills/*/SKILL.md`)
- Config files (`config/*.json`, `config/*.md`)

### Usage

```bash
# Check sync status
uv run python scripts/sync_plugin_to_local.py --check

# Preview changes
uv run python scripts/sync_plugin_to_local.py --dry-run

# Perform sync
uv run python scripts/sync_plugin_to_local.py
```

### When to Use

**Before committing plugin changes**:
```bash
# 1. Edit plugin files
vim packages/claude-plugin/hooks/scripts/session-start.py

# 2. Sync to .claude for testing
uv run python scripts/sync_plugin_to_local.py

# 3. Test locally, then commit both
git add packages/claude-plugin/ .claude/
git commit -m "feat: enhance session tracking"
```

**Before deployment**: The deploy script automatically checks sync status and fails if out of sync.

### Features

- ✅ Ensures `.claude/` matches distributed plugin exactly
- ✅ Enables proper dogfooding (use what we ship)
- ✅ Integrated into deployment workflow
- ✅ Preserves local-only files (session-start.sh, protect-htmlgraph.sh)

See [Plugin Sync Documentation](../docs/PLUGIN_SYNC.md) for details.

---

## Deployment Script (`deploy-all.sh`)

**Purpose**: Automate the complete deployment workflow from git push to PyPI publish to plugin updates.

**Pre-flight Checks + 9 Deployment Steps**:

**Pre-flight (Before deployment)**:
- ✅ **Code Quality Checks**
  - `ruff check` - Linting
  - `ruff format --check` - Code formatting
  - `mypy` - Type checking
  - `pytest` - Test suite (warns on failure, allows override)
- ✅ **Plugin Sync Verification** - Ensures packages/claude-plugin and .claude match

**Deployment Steps**:
0. **Update version numbers** - Auto-update and commit all version files
1. **Push to git** - With automatic tag creation
2. **Build Python package** - Create wheel and source distribution
3. **Publish to PyPI** - Upload to package index
4. **Install locally** - Install and verify latest version
5. **Update Claude plugin** - Sync packages/claude-plugin → .claude for dogfooding
6. **Update Gemini extension** - Update version metadata
7. **Update Codex skill** - If applicable
8. **Create GitHub release** - With distribution files and release notes

### Usage

```bash
# Full release
./scripts/deploy-all.sh 0.9.1

# Full release (non-interactive, no prompts)
./scripts/deploy-all.sh 0.9.1 --no-confirm

# Documentation changes only (skip build/publish)
./scripts/deploy-all.sh --docs-only

# Build package only (skip git/publish/install)
./scripts/deploy-all.sh --build-only

# Skip PyPI publishing
./scripts/deploy-all.sh 0.9.1 --skip-pypi

# Preview what would happen
./scripts/deploy-all.sh --dry-run
```

### Pre-Deployment Checklist

**CRITICAL - Do these first:**

1. ✅ **MUST be in project root directory** - Script fails from subdirectories
2. ~~✅ **Commit all changes first**~~ - **AUTOMATED!** Script auto-commits version changes
3. ~~✅ **Verify version numbers**~~ - **AUTOMATED!** Script auto-updates and commits versions
4. ✅ **Run tests** - `uv run pytest` must pass before deployment

**What's Automated Now (v0.9.4+):**
- ✅ Version number updates (Step 0)
- ✅ Auto-commit of version files
- ✅ Session tracking files excluded from git (via .gitignore)
- ✅ Non-interactive mode with `--no-confirm` flag

**New Workflow:**
```bash
# 1. Run tests
uv run pytest

# 2. Deploy (one command, fully automated!)
./scripts/deploy-all.sh 0.9.4 --no-confirm

# That's it! Script handles:
# - Version updates in all files
# - Auto-commit version changes
# - Git push with tags
# - Build, publish, install
# - Plugin updates
```

---

## Common Workflows

### Quick Commit and Push

```bash
./scripts/git-commit-push.sh "chore: update docs" --no-confirm
```

### Full Release

```bash
# 1. Pre-deployment checks
cd /Users/shakes/DevProjects/htmlgraph
uv run pytest
git status

# 2. Deploy
./scripts/deploy-all.sh 0.9.1
```

### Development Notes

**CRITICAL**: All scripts use \`uv run python\` instead of bare \`python\` to comply with project standards.

---

## Troubleshooting

### "No such file or directory"
**Solution**: Always run from project root
```bash
cd /Users/shakes/DevProjects/htmlgraph
./scripts/git-commit-push.sh "message"
```

### "Uncommitted changes detected"
**Solution**: Commit changes first
```bash
./scripts/git-commit-push.sh "chore: commit" --no-confirm
./scripts/deploy-all.sh 0.9.1
```

### Pre-Deployment Checklist

**CRITICAL - Do these first:**

1. ✅ **MUST be in project root directory** - Script fails from subdirectories
2. ✅ **Commit all changes first** - Script checks for uncommitted changes
3. ~~✅ **Verify version numbers**~~ - **AUTOMATED!** Script now updates all version numbers automatically
4. ✅ **Run tests** - `uv run pytest` must pass before deployment

---

### Version Management (AUTOMATED!)

**NEW:** The script now automatically updates version numbers in all files!

Just provide the version number and the script handles the rest:

```bash
./scripts/deploy-all.sh 0.9.3
```

**Files Updated Automatically:**
- ✅ `pyproject.toml` - Python package version
- ✅ `src/python/htmlgraph/__init__.py` - `__version__` variable
- ✅ `packages/claude-plugin/.claude-plugin/plugin.json` - Claude plugin version
- ✅ `packages/gemini-extension/gemini-extension.json` - Gemini extension version

**How it works:**
1. Script detects version from command line argument
2. Updates all 4 files before git push (Step 0)
3. Commits include correct version numbers
4. Build uses updated version numbers
5. No more manual version updates needed!

**Example workflow:**
```bash
# Old way (manual):
# 1. Edit pyproject.toml version
# 2. Edit __init__.py version
# 3. Edit plugin.json versions
# 4. Commit version changes
# 5. Run deployment

# New way (automatic):
./scripts/deploy-all.sh 0.9.3  # That's it!
```

---

### Environment Variables

Required for PyPI publishing:
```bash
# In .env file:
PyPI_API_TOKEN=pypi-YOUR_TOKEN_HERE

# Or as environment variable:
export UV_PUBLISH_TOKEN="pypi-YOUR_TOKEN_HERE"
```

---

## Python Utility Scripts

### Analysis & Investigation Tools

#### `analyze_features.py`
**Purpose**: Analyze features in .htmlgraph/ directory for status, completion, and patterns.

```bash
uv run python scripts/analyze_features.py
```

**Output**: Statistics on feature states, durations, and completion rates.

#### `analyze_orchestrator_impact.py` & `analyze_orchestrator_impact_v2.py`
**Purpose**: Measure impact of orchestrator enforcement on delegation patterns and tool usage.

```bash
uv run python scripts/analyze_orchestrator_impact.py
uv run python scripts/analyze_orchestrator_impact_v2.py
```

**Output**: Analysis of delegation rates, tool usage patterns before/after orchestrator enforcement.

#### `delegation_analysis.py`
**Purpose**: Create HtmlGraph spike documenting delegation enforcement issues and solutions.

```bash
uv run python scripts/delegation_analysis.py
```

**Output**: Creates spike HTML file in .htmlgraph/spikes/

#### `verify_htmx_dashboard.py`, `verify_new_dashboard.py`, `verify_spawner_tracking.py`
**Purpose**: Verification scripts for dashboard functionality and spawner tracking.

```bash
uv run python scripts/verify_htmx_dashboard.py
uv run python scripts/verify_new_dashboard.py
uv run python scripts/verify_spawner_tracking.py
```

---

### Data Generation & Setup Tools

#### `generate_real_events.py`
**Purpose**: Generate realistic HtmlGraph events for testing and demonstration.

```bash
uv run python scripts/generate_real_events.py
```

**Output**: Populates .htmlgraph/htmlgraph.db with test events.

#### `setup_features.py`
**Purpose**: Initialize feature tracking for HtmlGraph development phases.

```bash
uv run python scripts/setup_features.py
```

**Output**: Creates feature HTML files in .htmlgraph/features/

#### `create_delegation_test_features.py`
**Purpose**: Create test features for delegation workflow testing.

```bash
uv run python scripts/create_delegation_test_features.py
```

---

### Spike & Report Generation

#### `create_spike.py`
**Purpose**: Create investigation spike from findings and analysis.

```bash
uv run python scripts/create_spike.py
```

**Output**: Creates spike HTML in .htmlgraph/spikes/

#### `create_spike_report.py`
**Purpose**: Generate comprehensive spike report with findings summary.

```bash
uv run python scripts/create_spike_report.py
```

#### `create_integrity_spike.py`
**Purpose**: Create feature integrity analysis spike.

```bash
uv run python scripts/create_integrity_spike.py
```

---

### Maintenance & Migration Tools

#### `cleanup_wip.py`
**Purpose**: Clean up work-in-progress features, remove duplicates, archive test features.

```bash
uv run python scripts/cleanup_wip.py
```

**Warning**: Modifies .htmlgraph/ directory. Review changes carefully.

#### `migrate_html_to_sqlite.py`
**Purpose**: Migrate legacy HTML-based storage to SQLite database.

```bash
uv run python scripts/migrate_html_to_sqlite.py
```

#### `migrate_work_types.py`
**Purpose**: Migrate work item types in database schema.

```bash
uv run python scripts/migrate_work_types.py
```

#### `reindex_all.py`
**Purpose**: Rebuild database indexes for performance.

```bash
uv run python scripts/reindex_all.py
```

---

### Linking & Integration Tools

#### `link_features_to_track.py`
**Purpose**: Link features to parent track for hierarchy management.

```bash
uv run python scripts/link_features_to_track.py
```

#### `record_orchestration_verification.py`
**Purpose**: Record orchestration verification findings to HtmlGraph database.

```bash
uv run python scripts/record_orchestration_verification.py
```

#### `update_phase2_feature.py`
**Purpose**: Update Phase 2 feature status and mark steps completed.

```bash
uv run python scripts/update_phase2_feature.py
```

---

### Development Server

#### `start_api_server.py`
**Purpose**: Start FastAPI server with correct database path for development.

```bash
uv run python scripts/start_api_server.py
```

**Default**: Serves on http://localhost:8000
**Alternative**: Use `uv run htmlgraph serve` (recommended)

---

### Image & Asset Processing

#### `process_images.py`
**Purpose**: Process and optimize images for documentation.

```bash
uv run python scripts/process_images.py
```

#### `generate_branding.py`
**Purpose**: Generate branding assets (logos, icons, color schemes).

```bash
uv run python scripts/generate_branding.py
```

#### `generate_aliases.py`
**Purpose**: Generate command aliases for CLI.

```bash
uv run python scripts/generate_aliases.py
```

---

### Database Utilities

#### `setup-dashboard-db.py`
**Purpose**: Initialize dashboard database with schema and seed data.

```bash
uv run python scripts/setup-dashboard-db.py
```

#### `test-event-tracking.py`
**Purpose**: Test event tracking functionality in hooks.

```bash
uv run python scripts/test-event-tracking.py
```

---

### CIGS (Code Intelligence Graph System) Tools

#### `cigs-wave1-delegation.py`
**Purpose**: Wave 1 CIGS implementation - delegation pattern analysis.

```bash
uv run python scripts/cigs-wave1-delegation.py
```

#### `cigs-wave2-integration.py`
**Purpose**: Wave 2 CIGS implementation - integration with Claude Code.

```bash
uv run python scripts/cigs-wave2-integration.py
```

---

### Memory & Documentation Sync

#### `sync_memory_files.py`
**Purpose**: Synchronize documentation files across Claude, Gemini, and central AGENTS.md.

```bash
# Check sync status
uv run python scripts/sync_memory_files.py --check

# Synchronize all files
uv run python scripts/sync_memory_files.py
```

**See also**: `uv run htmlgraph sync-docs` (CLI command)

---

### Shell Scripts

#### `deploy-all.sh`
Complete deployment automation (see above for full documentation).

#### `git-commit-push.sh`
Simplified git workflow (see above for full documentation).

#### `install-hooks.sh`
Install pre-commit hooks for code quality.

#### `statusline.py`
Generate status line for terminal display.

```bash
uv run python scripts/statusline.py
```

---

## Script Categories

**For Development:**
- start_api_server.py
- generate_real_events.py
- setup_features.py

**For Analysis:**
- analyze_features.py
- analyze_orchestrator_impact.py
- delegation_analysis.py

**For Maintenance:**
- cleanup_wip.py
- migrate_html_to_sqlite.py
- reindex_all.py

**For Deployment:**
- deploy-all.sh
- git-commit-push.sh

**For Verification:**
- verify_htmx_dashboard.py
- verify_new_dashboard.py
- test-event-tracking.py

---

## Best Practices

1. **Always use `uv run python`** instead of bare `python` or `python3`
2. **Run from project root** (`/Users/shakes/DevProjects/htmlgraph/`)
3. **Check --help** if script supports it
4. **Review output** before committing changes from data-modifying scripts
5. **Use --dry-run** when available to preview changes

---

## Adding New Scripts

When adding new utility scripts:

1. Place in `scripts/` directory
2. Add shebang: `#!/usr/bin/env python3`
3. Include docstring explaining purpose
4. Support `--help` flag if complex
5. Use `uv run python` in examples
6. Document here in appropriate category
7. Update this README

---

## Deprecated Scripts

Scripts that should not be used (kept for reference):

- None currently deprecated

---

## See Also

- [Python Scripts Reference](../PYTHON_SCRIPTS_REFERENCE.md) - Complete inventory
- [Documentation Structure](../DOCUMENTATION_STRUCTURE.md) - Where files live
- [Deployment Rules](../.claude/rules/deployment.md) - Deployment workflow
- [Contributing Guide](../CONTRIBUTING.md) - Development workflow
