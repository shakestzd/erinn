# /wipnote:deploy

Deploy a new wipnote release. Handles quality gates, git push, GitHub Release, marketplace sync, plugin update, and CLI binary rebuild.

## Usage

```
/wipnote:deploy <version>
```

## Parameters

- `version` (required): Semantic version (e.g., 0.42.3, 0.43.0, 1.0.0)

## Examples

```
/wipnote:deploy 0.42.3
```
Patch release (bug fixes)

```
/wipnote:deploy 0.43.0
```
Minor release (new features)

## Instructions for Claude

### Pre-deployment

1. Ensure all source changes are committed:
   ```bash
   git status --porcelain -- cmd/ internal/ go.mod plugin/
   ```
   If there are uncommitted changes, commit them first (ask the user for a commit message if unclear).

2. Run quality gates:
   ```bash
   go build ./... && go vet ./... && go test ./...
   ```
   ALL must pass. Fix any failures before proceeding.

### Deployment

Run the deploy script from the project root:
```bash
./scripts/deploy-all.sh {version} --no-confirm
```

The script handles everything:
- Version bump in plugin.json
- Git commit, tag, push
- GitHub Actions triggers GoReleaser (cross-platform binaries)
- Marketplace clone pull
- `claude plugin update wipnote@wipnote`
- CLI binary rebuild via `plugin/build.sh`

### Post-deployment verification

```bash
# Check CI pipelines
gh run list --workflow=ci.yml --limit 1
gh run list --workflow=release-go.yml --limit 1

# Verify installed versions match
grep '"version"' plugin/.claude-plugin/plugin.json
cat ~/.local/share/wipnote/.binary-version
```

### If deployment fails

- **Quality gate failure**: Fix the issue, re-run
- **Git push failure**: Check branch is main, no upstream conflicts
- **Plugin update failure**: Manually pull marketplace clone and retry:
  ```bash
  (cd ~/.claude/plugins/marketplaces/wipnote && git pull origin main)
  claude plugin update wipnote@wipnote
  ```
- **Build failure**: Run `sh plugin/build.sh` directly

### Rules

- NEVER deploy without passing quality gates
- NEVER skip `--no-confirm` (causes interactive prompts that block automation)
- ALWAYS verify post-deployment that CLI and plugin versions match
- Patch (X.Y.Z+1) for bug fixes, Minor (X.Y+1.0) for features, Major (X+1.0.0) for breaking changes
