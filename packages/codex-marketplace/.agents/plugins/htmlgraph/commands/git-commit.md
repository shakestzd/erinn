# /htmlgraph:git-commit

Commit changes using Bash-copilot first, haiku-coder as fallback.

## Usage

```
/htmlgraph:git-commit [message] [--push] [--files <list>]
```

## Parameters

- `message` (optional): Commit message. If omitted, Claude analyzes the diff and drafts one.
- `--push`: Also push after committing.
- `--files <list>`: Specific files to stage. Default: analyze git diff and select source files, excluding `.htmlgraph/`.

## Examples

```
/htmlgraph:git-commit "feat: add user authentication"
/htmlgraph:git-commit --push
/htmlgraph:git-commit --files internal/foo.go internal/foo_test.go "fix: resolve null pointer"
```

## Instructions for Claude

**MANDATORY: You MUST follow these steps in order. Do NOT skip to direct git.**

### Step 1: Analyze what to commit

```bash
git diff --stat HEAD; git status --short
```

Select source files to stage. Exclude `.htmlgraph/` directory unless explicitly requested.

If no message was provided, draft one from the diff using conventional commit format.

### Step 2: Check copilot availability

```bash
which copilot 2>/dev/null && echo "COPILOT AVAILABLE" || echo "COPILOT NOT FOUND"
```

**This check is MANDATORY.** You must run it before proceeding.

### Step 3: Commit (follow priority order strictly)

**If copilot is available (Step 2 returned AVAILABLE) — use it:**

```bash
copilot -p "Stage files: <list>. Commit with message: '<message>'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
```

If `--push` was passed, add: "Then push to origin." to the prompt.

**Only if copilot failed, timed out, or was not found — delegate to haiku-coder:**

```text
Agent(
    subagent_type="htmlgraph:haiku-coder",
    description="Commit: <message>",
    prompt="Stage these files: <list>. Commit with message: '<message>'. Do NOT push."
)
```

**Only if BOTH copilot and haiku-coder failed — use direct git as last resort:**

```bash
git add <files> && git commit -m "<message>"
```

**NEVER use direct git without first trying copilot.** If you find yourself writing `git add && git commit` without having run `which copilot` first, STOP and go back to Step 2.

## Commit Message Format

```
<type>: <short description>

[optional body]

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`

Always append the Co-Authored-By line.

## Pre-commit Checks

This project uses `.githooks/` with pre-commit checks (go build, go vet, go test). If pre-commit fails, the commit fails — fix the reported issues and retry.

## Output Format

Report which path was used and the result:

```
Committed via: bash-copilot | haiku-coder | direct git
Commit: <hash>
Files changed: <count>
Message: <message>
```
