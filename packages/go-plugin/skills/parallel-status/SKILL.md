---
name: htmlgraph:parallel-status
description: Monitor parallel execution progress — task dependency status, worktree state, and merge readiness. Activate when asked for parallel status, task progress, or execution monitoring.
---

# HtmlGraph Parallel Status

Use this skill to monitor parallel execution progress across tasks and worktrees.

**Trigger keywords:** parallel status, task status, worktree status, execution progress, monitor tasks, check progress

**CLI reference:** Run `htmlgraph help` for available commands.

---

## Quick Status

```
# Task dependency graph status
TaskList()
# Shows: id, subject, status, owner, blockedBy for each task

# Worktree status
git worktree list

# Recent commits across all branches
git for-each-ref --sort=-committerdate refs/heads/ \
  --format='%(refname:short) %(committerdate:relative) %(subject)' | head -20
```

---

## Status Categories

### Ready to Dispatch
Tasks that are `pending` with empty `blockedBy` — can be dispatched immediately.

### In Progress
Tasks that are `in_progress` — agents are working on them in worktrees.

### Blocked
Tasks that are `pending` with non-empty `blockedBy` — waiting for dependencies.

### Completed
Tasks marked `completed` — branches merged, quality gates passed.

---

## Interpreting the Dependency Graph

```
TaskList() output example:

#1  [completed]   Add check command
#2  [completed]   Add budget command
#3  [in_progress] Add health command         (worktree: agent-abc123)
#4  [pending]     Spec compliance scoring    blockedBy: [5]
#5  [in_progress] Feature spec generator     (worktree: agent-def456)
#6  [pending]     Session timeline dashboard blockedBy: [3, 5]

Status: 2 completed | 2 in progress | 2 blocked
Next dispatch: #4 unblocks when #5 completes. #6 unblocks when #3 and #5 complete.
```

---

## Related Skills

- **[/htmlgraph:plan](/htmlgraph:plan)** - Create the dependency graph
- **[/htmlgraph:execute](/htmlgraph:execute)** - Run the dispatch loop
- **[/htmlgraph:cleanup](/htmlgraph:cleanup)** - Clean up after completion
