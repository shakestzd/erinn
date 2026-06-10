---
name: wipnote:execute
description: Execute a parallel plan using dependency-driven dispatch. Checks file overlap among unblocked tasks, partitions into non-conflicting waves, and dispatches simultaneously. Merges completed work, then dispatches newly unblocked tasks. No manual wave sequencing.
---

# wipnote Parallel Execute

Use this skill to execute development tasks in parallel using dependency-driven dispatch and worktree isolation.

**Trigger keywords:** execute plan, run plan, run tasks, parallelize work, work in parallel, start execution, dispatch agents

---

## Environment

When running in a worktree, `WIPNOTE_PROJECT_DIR` is set automatically. All `wipnote` CLI commands resolve to the main project's `.wipnote/` — no need to `cd` to main. Just run commands directly: `wipnote track show <id>`.

**NEVER use bare `cd` in Bash** — the hook will block it. Use subshells if you must change directories: `(cd dir && command)`.

---

## Efficiency Rules (read before dispatching)

Every tool call spends a turn. The goal is to dispatch the first subagent within **≤5 tool calls**. To hit that budget:

1. **One call, not ten.** Use `wipnote execute-preview <trk-id> --format json` to get the track, linked features/bugs/plans, and current git state in a single invocation. Do not call `wipnote track show`, `wipnote feature show`, `wipnote plan show`, and `git status` separately before the first dispatch.
2. **Batch git-state probes.** If execute-preview doesn't cover a probe you need, chain with `&&` in one Bash call — never one tool call per git subcommand.
3. **Don't feature-show more than twice in a row.** If you find yourself calling `wipnote feature show` for every linked feature, stop and re-read the preview JSON — the status you need is already there.
4. **Don't retry flag variants.** If a flag fails, check the skill for the real flag name before trying a second guess. This skill is validated by a build-time smoke test — prescribed flags are real.

---

## Work Item Attribution (MANDATORY)

Before dispatching any agents, verify attribution is set:

1. **Confirm active feature/track:** `wipnote status` — check "In progress" section
2. **If no active feature:** `wipnote feature start <id>` before proceeding
3. **Each agent prompt MUST include:**
   - `wipnote feature start {feature_id}` as the FIRST command the agent runs
   - `wipnote feature complete {feature_id}` after passing quality gates
4. **Need help?** Run `wipnote help` for available commands

Without attribution, work is invisible to the project graph.

---

## Core Principle: Dependency-Driven Dispatch Loop

Do NOT execute in manual waves. Instead, run a dispatch loop:

```
LOOP:
  1. Query: which tasks are unblocked? (pending + no blockedBy)
  1.5. Check file overlap among unblocked tasks → partition into non-conflicting waves
  2. Dispatch non-overlapping unblocked tasks in a single message (parallel agents)
  3. Wait for agents to complete
  4. Merge completed branches to main
  5. Run quality gates on merged result
  6. Check: are there newly unblocked tasks? → LOOP
  7. No more tasks? → DONE
```

This maximizes parallelism automatically. If 10 of 13 tasks are independent, all 10 run in the first dispatch — no artificial wave boundaries.

Slices promoted via `wipnote plan promote-slice` from a still-active plan are dispatched through the same dependency-readiness loop — they appear as features linked to the track via `part_of` edges and are ready to dispatch as soon as their `blocked_by` deps are complete.

### Incremental slice promotion

When a track is executing a v2 plan, slices may be promoted one at a time rather than all at once. Each call to `wipnote plan promote-slice <plan-id> <num>` creates a `feat-XXX` linked to the track and marks the slice `execution_status=promoted`. That feature immediately appears in `wipnote execute-preview <trk-id>` and enters the dispatch loop. Dep-blocked slices stay in the `blocked` bucket until their dependencies complete, exactly like manually created features. No special handling is required — promoted slices are regular features from the executor's perspective.

---

## Step 1: Query Unblocked Tasks

Use `TaskList()` to find all tasks ready for dispatch:

```
TaskList()

# Filter for: status=pending AND blockedBy is empty
# These are ready to dispatch immediately
```

If no tasks exist yet, create them from the plan (see `/wipnote:plan`).

---

## Step 1.5: Check File Overlap Among Unblocked Tasks

Before dispatching, verify that unblocked tasks do not edit the same files in parallel. File-level overlap defeats parallelism — two agents editing the same file produce merge conflicts that require manual resolution.

**Overlap detection:**

For each unblocked feature candidate, run `wipnote trace <feat-id>` to get its attributed file set. Then compute pairwise file-set intersection:

- **No overlap detected** → Proceed to dispatch all unblocked features in a single message (the existing happy path).
- **Partial overlap detected** → Partition into waves: dispatch the largest non-overlapping subset first; defer the conflicting features to the next dispatch cycle (after the first wave merges to main).
- **All candidates conflict** → Warn the orchestrator with explicit confirmation; either dispatch a single feature only this wave, or document the override choice to accept merge conflicts. This is rare but possible if all remaining features touch the same critical files (e.g., `go.mod`, `plugin.json`, main registration files).

**Implementation note:** The dependency graph alone is insufficient. Features can be dependency-independent but file-overlapping. This check is orthogonal to `blockedBy` — a feature may be unblocked but file-overlapping with another unblocked feature.

---

## Step 1.6: Precondition Check — SendMessage Availability (Diagnostic)

Before dispatching ANY agents in parallel, check whether the `SendMessage` tool is available. This diagnostic informs your recovery strategy if a sub-agent pauses on tool budget.

**Run this check BEFORE step 2:**

```
ToolSearch(query="select:SendMessage", max_results=1)
```

**If the result is empty or "No matching deferred tools found":**

SendMessage is not available (gated behind `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`). First, check the strict gating env var:

```
Bash(command="[ \"$WIPNOTE_EXECUTE_REQUIRE_SENDMESSAGE\" = \"1\" ] && echo strict || echo lax")
```

If the output is `strict`, print this abort message and STOP — do NOT proceed to dispatch:

```
/wipnote:execute: WIPNOTE_EXECUTE_REQUIRE_SENDMESSAGE=1 is set and SendMessage
is not loaded. Strict gating mode is active — aborting parallel dispatch.

To proceed, either:
  (a) unset WIPNOTE_EXECUTE_REQUIRE_SENDMESSAGE
  (b) enable agent-teams mode (CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1)
```

Otherwise (output is `lax`), print this WARNING and continue to step 1.7:

```
WARNING: SendMessage is not available in this session.

Implications:
- Parallel dispatch will proceed (first-pass execution works normally).
- If any sub-agent pauses on tool budget mid-execution, it cannot be resumed
  automatically. You must either:
    (a) Accept the paused task as failed-needs-rerun and continue with other tasks.
    (b) Fall back to sequential dispatch: `wipnote yolo --feature <id>` per feature.
    (c) Enable agent-teams mode (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`) to load
        SendMessage (note: even with SendMessage, worktree subagents cannot be
        resumed per Claude Code issue #42596 — SendMessage only helps agent-teams
        mode).

Proceeding with parallel dispatch. Review your recovery options above before starting.
```

**If the result lists SendMessage:** proceed to the dispatch loop. (Note: paused
worktree subagents cannot be resumed via SendMessage per Claude Code issue #42596;
SendMessage only benefits agent-teams mode.)

> Note: SendMessage availability affects recovery of paused sub-agents during
> parallel execution. Absence does not prevent first-pass execution; it affects
> what happens if a sub-agent exhausts tool budget mid-task. Worktree subagents
> still cannot be resumed via SendMessage per Claude Code issue #42596; this is
> a limitation of the underlying implementation, not this check.

---

## Step 1.7: Populate Tasks from Plan

When executing features that originated from a plan, you must first resolve the track title, then create tasks with readable subject fields.

**Resolve the track title in the same call that fetched everything else:**

You already called `wipnote execute-preview <trk-id> --format json` in the first discovery step. The track's human title is `.track.title` in that payload — reuse it. Do not issue a second `wipnote track show` call just for the title.

Use the title (not the raw track ID) as the outer `use the appropriate Gemini agent invocation` description when spawning the top-level coordinator:

```
use the appropriate Gemini agent invocation  # ✓ GOOD
```

Never use the raw track ID:
```
use the appropriate Gemini agent invocation  # ✗ BAD
```

**Create tasks with human-readable subjects:**

For each approved slice in the plan, create one task using this template:

```
TaskCreate(
    subject="<slice.title>",                    # e.g. "Registry package — internal/registry/"
    description="<slice.what truncated to 200 chars>",
    activeForm="Implementing <slice.title>",
    metadata={
        "feature_id": "<slice.id>",             # e.g. feat-7540a6cc
        "num": <slice.num>,                     # e.g. 1
        "agent": "feature-coder"
    }
)
```

**Why this matters:** The execute log renders `task.subject` as the task badge label. If you pass a slice number (e.g., `1`, `2`, `3`) or raw feature ID, the dispatcher log becomes unreadable at a glance. Always use the slice title — it gives agents immediate context about what they're implementing.

---

## Step 2: Dispatch All Unblocked Tasks

Spawn ALL ready tasks in a single message. Each gets an isolated worktree:

```
# In a SINGLE message, dispatch all N unblocked tasks:

Use Gemini agent invocation with:
    description="feat-001: Add check command",
    agent="@feature-coder",
    isolation="worktree",
    message="[full task spec — see template below]"

Use Gemini agent invocation with:
    description="feat-002: Add budget command",
    agent="@feature-coder",
    isolation="worktree",
    message="[full task spec]"

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
wipnote feature start {metadata.feature_id}

## Plan Context (if available)
This feature is part of plan {plan_id} on track {track_id}.

**Design decisions affecting this feature:**
{relevant_design_decisions}

**Critique notes (HIGH/DANGER only):**
{high_danger_critique_items}

**Sibling features (for awareness, do NOT implement):**
{sibling_feature_list}

For full plan context: `wipnote plan show {plan_id}`
For track context: `wipnote track show {track_id}`

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

## TDD Protocol
Follow the TDD protocol from /wipnote:tdd-protocol — write failing tests first, run quality gates (`go build && go vet && go test`), commit per the documented format, attribute via `wipnote feature start` / `complete`.

Report: files changed, lines added, tests passing, test names.
```

### Populating Plan Context

When dispatching features that originated from a plan (features with a `planned_in` edge), the orchestrator should inject compact plan context into each agent's prompt. This ensures agents understand the design rationale and constraints without reading the full plan.

**Steps to populate the template variables:**

1. **plan_id / track_id**: Read from the feature's `planned_in` edge (target is the plan ID) and the feature's `part_of` edge (target is the track ID).
2. **relevant_design_decisions**: Read the plan YAML (`wipnote plan show {plan_id}` or `.wipnote/plans/{plan_id}.yaml`). Extract answered `questions:` entries that are relevant to this specific slice. Keep to 3-5 lines.
3. **high_danger_critique_items**: From the plan YAML `critique:` section, extract any HIGH or DANGER severity items that reference this slice or the feature's scope. Omit LOW/MEDIUM items.
4. **sibling_feature_list**: Run `wipnote track show {track_id}` to list all features on the same track. Format as a compact list of ID + title. Mark which are in-progress, done, or todo so the agent knows what is already handled.

**Guidelines:**
- Keep the injected context to 5-10 lines total. Agents can run `wipnote plan show` for details.
- If no plan exists (feature was created manually), omit the "Plan Context" section entirely.
- Design decisions prevent agents from making conflicting architectural choices.
- Critique notes prevent agents from repeating known risks the reviewer flagged.

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
go build ./... && go vet ./... && go test ./...
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

## Step 6: Review, Merge, and Clean Up

After all tasks complete, transition from autonomous development to reviewed integration.

### 6a. Code Review (MANDATORY before merge)

Run roborev on the feature branch:
```bash
# Review all branch commits
/wipnote:roborev  # or: roborev review-branch

# Address any medium+ severity findings before merging
# Fix on the feature branch, or acknowledge with justification
```

### 6b. Merge to Main

```bash
# Switch to main
git checkout main

# Merge the feature branch (merge conflict resolution on main is allowed)
git merge --no-ff <branch> -m "feat: merge <track> — <title>"

# If merge conflicts: resolve them (edits on main during merge are permitted)
# Then: git add <resolved files> && git commit

# Final quality gate on merged result
go build ./... && go vet ./... && go test ./...
```

### 6c. Clean Up

```bash
# Remove worktrees
git worktree list
git worktree remove .claude/worktrees/agent-XXXX --force

# Remove branches
git branch -D worktree-agent-XXXX

# Or use /wipnote:cleanup for automated cleanup
```

---

## Dispatch Loop Pseudocode

```
while True:
    ready = [t for t in TaskList() if t.status == "pending" and not t.blockedBy]
    if not ready:
        break  # All tasks done or blocked on failed tasks

    # Check file overlap and partition if necessary
    file_sets = {t.id: get_files(wipnote_trace(t.metadata.feature_id)) for t in ready}
    no_overlap = find_max_independent_subset(file_sets)
    
    # Dispatch non-overlapping tasks in ONE message
    for task in no_overlap:
        TaskUpdate(taskId=task.id, status="in_progress")
        Use Gemini agent invocation with:
            description=task.subject,  # Must be slice.title from TaskCreate, NOT slice.num or feature_id
            agent=task.metadata.agent,
            isolation="worktree",
            message=build_prompt(task)
    
    # Defer overlapping tasks to next wave
    for task in ready:
        if task not in no_overlap:
            # Keep status as "pending" — will be in ready set again next iteration
            pass

    # Wait for all agents to complete (foreground)

    # Merge all completed branches
    for task in no_overlap:
        merge(task.branch)
        resolve_conflicts_if_any()

    # Quality gates
    run_quality_gates()  # MUST pass before next dispatch

    # Mark complete
    for task in no_overlap:
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

---

## Monitoring During Parallel Execution

```bash
# Task dependency graph status
TaskList()
# Shows: id, subject, status, owner, blockedBy

# Worktree state
git worktree list

# Recent commits across all branches
git for-each-ref --sort=-committerdate refs/heads/ \
  --format='%(refname:short) %(committerdate:relative) %(subject)' | head -20
```

Status categories: **ready** (pending, no blockedBy) | **in_progress** | **blocked** (pending, has blockedBy) | **completed**

---

## Cleanup After Completion

```bash
# Prune merged worktrees and confirm clean state in one call
git worktree prune && git worktree list

# Remove a specific worktree and its branch (chain to avoid leaving orphans)
git worktree remove <path> && git branch -D worktree-agent-XXXX

# Final quality gates
go build ./... && go vet ./... && go test ./...
```

---

## Related Skills

- **[/wipnote:plan](/wipnote:plan)** - Create the dependency graph before executing
