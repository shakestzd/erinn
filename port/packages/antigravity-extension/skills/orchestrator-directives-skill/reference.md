# Orchestrator Directives - Complete Reference

This document contains the complete orchestration rules and patterns for wipnote project.

**Source:** `packages/claude-plugin/rules/orchestration.md`

---

## Core Philosophy

**CRITICAL: When operating in orchestrator mode, you MUST delegate ALL operations except a minimal set of strategic activities.**

**You don't know the outcome before running a tool.** What looks like "one bash call" often becomes 2, 3, 4+ calls when handling failures, conflicts, hooks, or errors. Delegation preserves strategic context by isolating tactical execution in subagent threads.

## Operations You MUST Delegate

**ALL operations EXCEPT:**
- `use the appropriate Gemini agent invocation` - Delegation itself
- `AskUserQuestion()` - Clarifying requirements with user
- `TodoWrite()` - Tracking work items
- SDK operations - Creating features, spikes, bugs, analytics

**Everything else MUST be delegated**, including:

### 1. Git Operations - ALWAYS DELEGATE

- ❌ NEVER run git commands directly (add, commit, push, branch, merge)
- ✅ ALWAYS delegate to subagent with error handling

**Why?** Git operations cascade unpredictably:
- Commit hooks may fail (need fix + retry)
- Conflicts may occur (need resolution + retry)
- Push may fail (need pull + merge + retry)
- Tests may fail in hooks (need fix + retry)

**Context cost comparison:**
```
Direct execution: 7+ tool calls
  git add → commit fails (hook) → fix code → commit → push fails → pull → push

Delegation: 2 tool calls
  use the appropriate Gemini agent invocation → Read result
```

**Delegation pattern (Bash-first):**
```bash
# Priority 1: Try copilot CLI directly
copilot -p "Stage files: CLAUDE.md, SKILL.md, git-commit-push.sh. Commit with message: 'docs: enforce strict git delegation in orchestrator directives'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
```

```python
# Priority 2: patch-coder fallback (if copilot unavailable)
Use Gemini agent invocation with:
    agent="@patch-coder",
    description="Commit: docs: enforce strict git delegation",
    message="""
    Stage files: CLAUDE.md, SKILL.md, git-commit-push.sh
    Commit with message: "docs: enforce strict git delegation in orchestrator directives"
    Do NOT push.
    Handle any errors (pre-commit hooks, conflicts, etc).
    """,
```

### 2. Code Changes - DELEGATE Unless Trivial

- ❌ Multi-file edits
- ❌ Implementation requiring research
- ❌ Changes with testing requirements
- ✅ Single-line typo fixes (OK to do directly)

### 3. Research & Exploration - ALWAYS DELEGATE

- ❌ Large codebase searches (multiple Grep/Glob calls)
- ❌ Understanding unfamiliar systems
- ❌ Documentation research
- ✅ Single file quick lookup (OK to do directly)

### 4. Testing & Validation - ALWAYS DELEGATE

- ❌ Running test suites
- ❌ Debugging test failures
- ❌ Quality gate validation
- ✅ Checking test command exists (OK to do directly)

### 5. Build & Deployment - ALWAYS DELEGATE

- ❌ Build processes
- ❌ Package publishing
- ❌ Environment setup
- ✅ Checking deployment script exists (OK to do directly)

### 6. File Operations - DELEGATE Complex Operations

- ❌ Batch file operations (multiple files)
- ❌ Large file reading/writing
- ❌ Complex file transformations
- ✅ Reading single config file (OK to do directly)
- ✅ Writing single small file (OK to do directly)

### 7. Analysis & Computation - DELEGATE Heavy Work

- ❌ Performance profiling
- ❌ Large-scale analysis
- ❌ Complex calculations
- ✅ Simple status checks (OK to do directly)

## Why Strict Delegation Matters

### 1. Context Preservation

- Each tool call consumes tokens
- Failed operations consume MORE tokens
- Cascading failures consume MOST tokens
- Delegation isolates failure to subagent context

### 2. Parallel Efficiency

- Multiple subagents can work simultaneously
- Orchestrator stays available for decisions
- Higher throughput on independent tasks

### 3. Error Isolation

- Subagent handles retries and recovery
- Orchestrator receives clean success/failure
- No pollution of strategic context

### 4. Cognitive Clarity

- Orchestrator maintains high-level view
- Subagents handle tactical details
- Clear separation of concerns

## Decision Framework

Ask yourself:

1. **Will this likely be one tool call?**
   - If uncertain → DELEGATE
   - If certain → MAY do directly

2. **Does this require error handling?**
   - If yes → DELEGATE

3. **Could this cascade into multiple operations?**
   - If yes → DELEGATE

4. **Is this strategic (decisions) or tactical (execution)?**
   - Strategic → Do directly
   - Tactical → DELEGATE

## Orchestrator Reflection System

When orchestrator mode is enabled (strict), you'll receive reflections after direct tool execution:

```
ORCHESTRATOR REFLECTION: You executed code directly.

Ask yourself:
- Could this have been delegated to a subagent?
- Would parallel use the appropriate Gemini agent invocation calls have been faster?
- Is a work item tracking this effort?
- What if this operation fails - how many retries will consume context?
```

Use these reflections to adjust your delegation habits.

## Integration with wipnote CLI

Always use the CLI to track orchestration activities:

```bash
# Track what you delegate
wipnote feature create "Implement authentication" --track <trk-id>
wipnote feature start <feat-id>
```

```bash
# Try CLI tools directly first
gemini -p "Find all auth-related code in src/: What library is used? Where is validation?" \
  --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```
```bash
codex exec "Implement OAuth flow based on research findings" \
  --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```

**See:** `packages/go-plugin/skills/orchestrator-directives-skill/SKILL.md` for complete orchestrator patterns

## Parallel Task Coordination

**Problem:** Multiple parallel tasks need independent result tracking.

**Solution:** Dispatch all tasks in a single message — Claude Code runs them in parallel automatically.

```bash
# Dispatch 3 parallel Bash calls in a single message (CLI-first pattern)
codex exec "Add JWT auth to API endpoints..." --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```
```bash
codex exec "Write unit + integration tests for auth endpoints..." --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @patch-coder
```
```bash
gemini -p "Update API documentation for auth endpoints..." --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```
# All three run in parallel; each reports results independently

**Benefits:**
- True parallelism (all dispatched in one message)
- Each task runs in isolation
- Cheaper agents used for each task type

## Git Workflow Patterns

### Orchestrator Pattern (REQUIRED)

When operating as orchestrator, try the CLI directly first, then delegate to patch-coder as fallback:

```bash
# ✅ CORRECT - Priority 1: Try copilot CLI directly
copilot -p "Stage files: [list files]. Commit with message: 'chore: update session tracking'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
```

```python
# ✅ CORRECT - Priority 2: patch-coder fallback (if copilot unavailable)
Use Gemini agent invocation with:
    agent="@patch-coder",
    description="Commit: chore: update session tracking",
    message="""
    Commit and push changes to git:

    Files to commit: [list files or use 'all changes']
    Commit message: "chore: update session tracking"

    Steps:
    1. git add [files]
    2. git commit -m "message"
    3. Handle any errors (pre-commit hooks, conflicts, push failures)
    4. Retry with fixes if needed

    Report final status: success or failure with details.
    """,
```

**Why Bash-first?** Skips the agent overhead when the CLI works — fast, transparent, cost-efficient.
**Why fallback to coder agent?** When CLI isn't installed, the coder agent handles all retries in its own context.

**Context cost:**
- Bash-copilot (success): 1 tool call
- patch-coder fallback: 2 tool calls (Agent + result review)
- Direct git without delegation: 5-10+ tool calls (with failures and retries)

## Detailed Delegation Examples

### Example 1: Feature Implementation Workflow

```bash
# 1. Create feature (orchestrator does this directly)
wipnote feature create "Add user authentication" --track <trk-id>
wipnote feature start <feat-id>
```

```bash
# 2. Research (try gemini CLI first)
gemini -p "Research existing auth patterns: What library is used? Where is validation? What OAuth providers are supported?" \
  --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```

```bash
# 3. Implement (try codex CLI first, after research completes)
codex exec "Implement OAuth flow: Add JWT auth to API endpoints, create middleware for token validation, support Google and GitHub OAuth" \
  --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```

```bash
# 4. Commit (try copilot CLI first)
copilot -p "Commit with message: 'feat: add user authentication with OAuth support'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
# fallback → use @patch-coder
```

```bash
# 5. Mark feature complete
wipnote feature complete <feat-id>
```

### Example 2: Bug Fix Workflow

```bash
# 1. Create bug
wipnote bug create "Session timeout not working" --track <trk-id>
```

```bash
# 2. Investigate (try gemini CLI first)
gemini -p "Debug session timeout: expected 30min, observed ~5min. Find config, check middleware, review logs, identify root cause." \
  --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```

```bash
# Fix (try codex CLI first, after investigation)
codex exec "Fix session timeout to 30 minutes. Add regression test. Verify fix works." \
  --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```

```bash
# 3. Commit (try copilot CLI first)
copilot -p "Commit with message: 'fix: correct session timeout to 30 minutes'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
# fallback → use @patch-coder
```

```bash
# 4. Mark bug resolved
wipnote bug complete <bug-id>
```

### Example 3: Parallel Task Coordination

```bash
# Create feature
wipnote feature create "Refactor API layer" --track <trk-id>
```

```bash
# Dispatch 3 parallel Bash calls in a single message
gemini -p "Update API documentation to reflect new endpoints" --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```
```bash
codex exec "Update test suite for refactored API endpoints" --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```
```bash
gemini -p "Create migration guide for API changes" --output-format json --yolo --include-directories . 2>&1
# fallback → use @patch-coder
```

```bash
# After all complete — commit everything (try copilot CLI first)
copilot -p "Commit all API refactoring changes with message: 'refactor: update API layer with improved endpoints'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
# fallback → use @patch-coder
```

```bash
wipnote feature complete <feat-id>
```

## Common Anti-Patterns to Avoid

### Anti-Pattern 1: Direct Git Execution

```python
# ❌ WRONG - Orchestrator executing git directly
Bash(command="git add .")
Bash(command="git commit -m 'feat: new feature'")
Bash(command="git push origin main")

# This will likely fail due to:
# - Pre-commit hooks
# - Merge conflicts
# - Remote changes
# Each failure consumes context and requires recovery
```

```python
# ✅ CORRECT - Delegate to subagent
Use Gemini agent invocation with:
    message="""
    Commit and push changes:
    Message: "feat: new feature"
    Handle all errors (hooks, conflicts, etc)
    """,
    workflow="general-purpose"
```

### Anti-Pattern 2: Sequential When Parallel is Possible

```python
# ❌ WRONG - Sequential delegation
use the appropriate Gemini agent invocation
# Wait for result...
use the appropriate Gemini agent invocation
# Wait for result...
use the appropriate Gemini agent invocation

# Total time: T1 + T2 + T3
```

```python
# ✅ CORRECT - Parallel delegation
use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation

# Total time: max(T1, T2, T3)
```

### Anti-Pattern 3: Not Using Task IDs

```python
# ❌ WRONG - No task IDs, can't distinguish results
use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation

# Which result is which?
```

```python
# ✅ CORRECT - Use task IDs
auth_id, auth_prompt = delegate_with_id("Research auth", "...", "general-purpose")
cache_id, cache_prompt = delegate_with_id("Research caching", "...", "general-purpose")
log_id, log_prompt = delegate_with_id("Research logging", "...", "general-purpose")

use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation
use the appropriate Gemini agent invocation

# Retrieve results independently
auth_results = get_results_by_task_id(sdk, auth_id)
cache_results = get_results_by_task_id(sdk, cache_id)
log_results = get_results_by_task_id(sdk, log_id)
```

### Anti-Pattern 4: Not Tracking Work Items

```python
# ❌ WRONG - No feature/bug tracking
use the appropriate Gemini agent invocation
# No record of what was planned or completed
```

```bash
# ✅ CORRECT - Track with wipnote CLI
wipnote feature create "Implement new feature" --track <trk-id>
wipnote feature start <feat-id>
```

```python
use the appropriate Gemini agent invocation
```

```bash
# Update status after completion
wipnote feature complete <feat-id>
```

## Summary

**Key Principles:**

1. **Delegate Everything** - Except use the appropriate Gemini agent invocation, AskUserQuestion(), TodoWrite(), and CLI operations
2. **Parallel Dispatch** - Send all independent Tasks in one message
3. **Track Work** - Use wipnote CLI for all features, bugs, spikes
4. **Parallel > Sequential** - Delegate independently when possible
5. **Git = Always Delegate** - Never run git commands directly

**Benefits:**

- Context preservation (fewer tokens consumed)
- Parallel efficiency (faster completion)
- Error isolation (cleaner orchestration)
- Cognitive clarity (strategic focus)

**When in doubt, DELEGATE.**
