---
name: htmlgraph:execute
description: Execute a parallel plan using dependency-driven dispatch. Dispatches ALL unblocked tasks simultaneously, merges completed work, then dispatches newly unblocked tasks. No manual wave sequencing.
---

# HtmlGraph Parallel Execute

Use this skill to execute development tasks in parallel using dependency-driven dispatch and worktree isolation.

**Trigger keywords:** execute plan, run plan, run tasks, parallelize work, work in parallel, start execution, dispatch agents

---

## Work Item Attribution (MANDATORY)

Before dispatching any agents, verify attribution is set:

1. **Confirm active feature/track:** `htmlgraph status` — check "In progress" section
2. **If no active feature:** `htmlgraph feature start <id>` before proceeding
3. **Each agent prompt MUST include:**
   - `htmlgraph feature start {feature_id}` as the FIRST command the agent runs
   - `htmlgraph feature complete {feature_id}` after passing quality gates
4. **Need help?** Run `htmlgraph help` for available commands

Without attribution, work is invisible to the project graph.

---

## Core Principle: Dependency-Driven Dispatch Loop

Do NOT execute in manual waves. Instead, run a dispatch loop:

```
LOOP:
  1. Query: which tasks are unblocked? (pending + no blockedBy)
  2. Dispatch ALL unblocked tasks in a single message (parallel agents)
  3. Wait for agents to complete
  4. Merge completed branches to main
  5. Run quality gates on merged result
  6. Check: are there newly unblocked tasks? → LOOP
  7. No more tasks? → DONE
```

This maximizes parallelism automatically. If 10 of 13 tasks are independent, all 10 run in the first dispatch — no artificial wave boundaries.

---

## Step 1: Query Unblocked Tasks

Use `TaskList()` to find all tasks ready for dispatch:

```
TaskList()

# Filter for: status=pending AND blockedBy is empty
# These are ready to dispatch immediately
```

If no tasks exist yet, create them from the plan (see `/htmlgraph:plan`).

---

## Step 2: Dispatch All Unblocked Tasks

Spawn ALL ready tasks in a single message. Each gets an isolated worktree:

```
# In a SINGLE message, dispatch all N unblocked tasks:

Agent(
    description="feat-001: Add check command",
    subagent_type="htmlgraph:sonnet-coder",
    isolation="worktree",
    prompt="[full task spec — see template below]"
)

Agent(
    description="feat-002: Add budget command",
    subagent_type="htmlgraph:sonnet-coder",
    isolation="worktree",
    prompt="[full task spec]"
)

# ... repeat for ALL unblocked tasks
```

Mark each task as in_progress:
```
TaskUpdate(taskId="1", status="in_progress")
TaskUpdate(taskId="2", status="in_progress")
```

### Task Prompt Template

Each agent receives a self-contained prompt with TDD enforcement.
**The agent MUST write failing tests before any implementation code.**

```
## Task: {task.subject}
**Feature:** {metadata.feature_id}

## Step 0: Attribution (FIRST — before any code)
htmlgraph feature start {metadata.feature_id}

## Goal
{task.description}

## Files to Create/Edit
{metadata.files}

## Shared Registration Files
These files are edited by multiple parallel agents. Add your changes —
the orchestrator will resolve merge conflicts after all agents complete:
- {list of shared files like main.go}

## Do NOT Touch
{files owned by other concurrent tasks — for awareness only}

## Step 1: Write Failing Tests FIRST (TDD — mandatory)
Before writing ANY implementation code, create test file(s):
{task.tests — from acceptance criteria in the plan}

After writing tests, verify:
- Tests COMPILE (no syntax errors)
- Tests FAIL (implementation doesn't exist yet)
Run: go test ./... — expect failures, not compilation errors.

## Step 2: Implement Until Tests Pass
Now write the minimum implementation to make all tests pass.
Do not add code that isn't covered by a test.

## Step 3: Quality Gate (MANDATORY before commit)
Go:     (cd packages/go && go build ./... && go vet ./... && go test ./...)
Python: uv run ruff check --fix && uv run ruff format && uv run mypy src/ && uv run pytest

## Commit
git add {specific files}
git commit -m "feat({scope}): {description} ({feature_id})"

## Step 4: Complete Attribution
htmlgraph feature complete {metadata.feature_id}

Report: files changed, lines added, tests passing, test names.
```

---

## Step 3: Merge Completed Branches

After all dispatched agents complete, merge their branches to main:

```bash
git checkout main

# Merge each completed branch
git merge --no-ff worktree-agent-XXXX -m "feat: merge {task title} ({feature_id})"

# If merge conflict (expected for shared files like main.go):
# 1. Read the conflicted file
# 2. Resolve by including ALL additions (they're independent registrations)
# 3. git add + git commit
```

### Conflict Resolution Strategy

**Shared registration files** (main.go, hooks.json, etc.):
- Conflicts are additive — each agent added a line. Include all lines.
- Resolve by keeping all additions in a logical order.

**Unexpected conflicts** (agents touched same logic):
- Investigate which agent's change is correct
- May indicate a missed dependency — add `addBlockedBy` for future runs

---

## Step 4: Quality Gates After Merge

Run full quality gates on the merged result:

```bash
# Go
(cd packages/go && go build ./... && go vet ./... && go test ./...)

# Python (if applicable)
uv run ruff check --fix && uv run ruff format && uv run mypy src/ && uv run pytest
```

If gates fail:
1. Identify which merge introduced the failure (bisect if needed)
2. Fix directly on main (small fix) or revert and re-dispatch (large fix)
3. Gates must pass before dispatching blocked tasks

---

## Step 5: Mark Complete and Check for Newly Unblocked

```
# Mark merged tasks as completed
TaskUpdate(taskId="1", status="completed")
TaskUpdate(taskId="2", status="completed")
# ... for each merged task

# Query again — completing tasks may have unblocked new ones
TaskList()
# Filter: status=pending AND blockedBy is empty
# If any found → go to Step 2 (dispatch next round)
# If none → DONE
```

---

## Step 6: Clean Up

After all tasks complete:

```bash
# Remove worktrees
git worktree list
git worktree remove .claude/worktrees/agent-XXXX --force
# ... for each worktree

# Remove branches
git branch -D worktree-agent-XXXX
# ... for each branch

# Final quality gate
(cd packages/go && go build ./... && go vet ./... && go test ./...)
```

Or use `/htmlgraph:cleanup` for automated cleanup.

---

## Dispatch Loop Pseudocode

```
while True:
    ready = [t for t in TaskList() if t.status == "pending" and not t.blockedBy]
    if not ready:
        break  # All tasks done or blocked on failed tasks

    # Dispatch all ready tasks in ONE message
    for task in ready:
        TaskUpdate(taskId=task.id, status="in_progress")
        Agent(
            description=task.subject,
            subagent_type=task.metadata.agent,
            isolation="worktree",
            prompt=build_prompt(task)
        )

    # Wait for all agents to complete (foreground)

    # Merge all completed branches
    for task in ready:
        merge(task.branch)
        resolve_conflicts_if_any()

    # Quality gates
    run_quality_gates()  # MUST pass before next dispatch

    # Mark complete
    for task in ready:
        TaskUpdate(taskId=task.id, status="completed")
```

---

## Error Handling

### Agent Fails (tests not passing)
```
Do NOT merge to main.
Create follow-up task: TaskCreate(subject="Fix: {original task}", ...)
Continue merging other successful agents.
Mark original as completed (the fix task handles the remainder).
```

### Merge Conflict in Non-Registration File
```
Investigate: was this a missed dependency?
If yes: add addBlockedBy for the conflicting task, re-dispatch
If no: resolve manually, document for future conflict detection
```

### Quality Gate Fails After Merge
```
git log --oneline -N  # Find which merge broke it
git revert <breaking-merge>  # Revert the offending merge
Re-dispatch that task with a fix note in the prompt
```

### All Remaining Tasks Are Blocked
```
Some dependency failed or was never completed.
TaskList() — find tasks with non-empty blockedBy
TaskGet(id) — check what they're waiting for
Either: fix the blocker, or remove the dependency if it's no longer needed
```

---

## Monitoring During Execution

```bash
# Active worktrees
git worktree list

# Recent commits across all branches
git for-each-ref --sort=-committerdate refs/heads/ \
  --format='%(refname:short) %(committerdate:relative) %(subject)' | head -20

# Task status
TaskList()  # Shows status + blockedBy for each task
```

---

## Related Skills

- **[/htmlgraph:plan](/htmlgraph:plan)** - Create the dependency graph before executing
- **[/htmlgraph:parallel-status](/htmlgraph:parallel-status)** - Monitor progress
- **[/htmlgraph:cleanup](/htmlgraph:cleanup)** - Clean up after completion
