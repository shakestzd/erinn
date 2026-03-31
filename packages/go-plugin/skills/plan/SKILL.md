---
name: htmlgraph:plan
description: Create a dependency-first parallel plan using native TaskCreate with addBlockedBy. Maximizes parallelism by dispatching ALL independent tasks simultaneously. Activate when asked to create a plan, parallelize work, or organize development tasks.
---

# HtmlGraph Parallel Plan

Use this skill when asked to plan development work, create a parallel execution plan, or organize tasks for multi-agent execution.

**Trigger keywords:** create plan, development plan, parallel plan, plan tasks, parallelize work, organize work, task breakdown

---

## Work Item Attribution (MANDATORY)

Before creating any plan, ensure work items exist:

1. **Verify or create a track:** `htmlgraph track create "Track Title"` for the body of work
2. **Create features under the track:** `htmlgraph feature create "Feature Title" --track <track-id>`
3. **Start the track:** `htmlgraph track start <track-id>`
4. **Each task in the plan MUST reference a feature ID** in its metadata
5. **Need help?** Run `htmlgraph help` for available commands

Plans without attributed features produce untracked work.

---

## Core Principle: Maximum Parallelism via Dependency Graph

Do NOT manually assign tasks to waves. Instead:

1. **Identify real dependencies** between tasks (Task B imports Task A's output)
2. **Detect file conflicts** (two tasks editing the same file)
3. **Create native tasks** with `TaskCreate` + `addBlockedBy` for real dependencies
4. **Dispatch ALL unblocked tasks** in a single message — the dependency graph determines ordering, not manual wave assignment

```
Traditional (wrong):          Dependency-first (correct):

Wave 1: [A] [B] [C]          All independent: [A] [B] [C] [D] [E] [F]
Wave 2: [D] [E]              Blocked on A:    [G] (addBlockedBy: [A])
Wave 3: [F]                  Blocked on G:    [H] (addBlockedBy: [G])

Artificial sequencing         Only real dependencies block
```

---

## Step 1: Gather All Tasks

Extract every task from the conversation, track, or feature list. For each task, determine:

| Field | How to Determine |
|-------|-----------------|
| **Files it creates/edits** | Analyze the spec — what new files, what existing files modified |
| **Real dependencies** | Does this task need another task's OUTPUT? (import, schema, API) |
| **File conflicts** | Does another task also edit the same file? (not a dependency — a merge concern) |
| **Complexity** | haiku: single-file, clear spec. sonnet: multi-file, design needed. opus: architecture |

### Dependency vs File Conflict

**Dependency** (use `addBlockedBy`): Task B literally cannot be written until Task A exists.
- Task B imports a module Task A creates
- Task B tests an API Task A implements
- Task B extends a schema Task A defines

**File conflict** (handle at merge time, NOT with `addBlockedBy`): Both tasks edit the same file but don't depend on each other's logic.
- Both tasks add a line to `main.go` registering their command
- Both tasks add an entry to a config file
- Both tasks import from the same module

File conflicts are resolved at merge time, not by serializing tasks.

---

## Step 2: Research (Parallel Agents)

Spawn research agents in a single message to gather evidence:

```
Agent(description="Find existing patterns", subagent_type="Explore", prompt="...")
Agent(description="Check dependencies", subagent_type="Explore", prompt="...")
Agent(description="Detect file conflicts", subagent_type="Explore", prompt="...")
```

Research should answer:
- What existing patterns should tasks follow?
- Which files will each task touch? (for conflict detection)
- Are there shared registration points? (e.g., `main.go`, `hooks.json`)

---

## Step 3: Build the Dependency Graph

After research, classify every task pair:

```
For each pair (A, B):
  if B needs A's output → B.addBlockedBy(A)
  if both edit same file → note as FILE_CONFLICT (resolve at merge)
  else → independent (both dispatch immediately)
```

### Shared Registration Files

Many projects have "registration files" that multiple tasks edit (e.g., `main.go` adding commands, `hooks.json` adding handlers). These are NOT dependencies — they're predictable merge conflicts.

**Strategy:** Identify shared registration files upfront. Tell each agent: "Add your registration to [file] — expect a merge conflict that the orchestrator will resolve."

---

## Step 4: Create Native Tasks

Use `TaskCreate` for each task. Use `TaskUpdate` with `addBlockedBy` for real dependencies only.

```
# Independent tasks — no blockers, dispatch immediately
TaskCreate(subject="feat-001: Add check command", description="...",
           metadata={"files": ["check.go", "main.go"], "agent": "sonnet-coder", "feature_id": "feat-41114e5d"})

TaskCreate(subject="feat-002: Add budget command", description="...",
           metadata={"files": ["budget.go", "main.go"], "agent": "sonnet-coder", "feature_id": "feat-9ef589b4"})

TaskCreate(subject="feat-003: Add health command", description="...",
           metadata={"files": ["health.go", "main.go"], "agent": "sonnet-coder", "feature_id": "feat-e745f68f"})

# Dependent task — needs feat-001's quality gate infrastructure
TaskCreate(subject="feat-004: Spec compliance scoring", description="...",
           metadata={"files": ["compliance.go"], "agent": "sonnet-coder", "feature_id": "feat-abb438f5"})

# Link the dependency
TaskUpdate(taskId="4", addBlockedBy=["1"])  # feat-004 needs feat-001
```

### Task Description Template

Each task description must be self-contained (agents have no shared context).
**TDD is mandatory** — every task includes test specifications that the agent writes BEFORE implementation.

```
## Goal
[One sentence: what this task produces]

## Files to Create/Edit
- NEW: path/to/new_file.go
- EDIT: path/to/existing_file.go (add registration line)

## Shared Files (expect merge conflict)
- main.go: Add `rootCmd.AddCommand(yourCmd())` — orchestrator resolves conflicts

## Acceptance Criteria
1. [Specific, measurable pass/fail condition]
2. [Specific, measurable pass/fail condition]

## Test Plan (TDD — mandatory)

### Test Strategy
[What kind of testing: "unit test the parser", "integration test the CLI command",
"table-driven tests for edge cases". Match the strategy to the task's risk profile.]

### Expected Behavior
[Concrete input/output examples that tests must verify:]
- Given [input/state], expect [output/behavior]
- Given [edge case], expect [error/fallback]
- Given [invalid input], expect [specific error message]

### Tests to Write FIRST
Write these tests BEFORE any implementation. They must compile and FAIL.
- Test: [TestFunctionName] — verifies [acceptance criterion 1]
- Test: [TestFunctionName] — verifies [acceptance criterion 2]
- Test: [TestEdgeCase] — verifies [boundary/error condition]

## Quality Gate
(cd packages/go && go build ./... && go vet ./... && go test ./...)

## Commit
git commit -m "feat(scope): description (feat-XXXXXXXX)"
```

---

## Step 5: Analyze Parallelism

Before presenting, compute the parallelism profile:

```
Total tasks:        N
Independent:        X (dispatch immediately)
Blocked:            Y (wait for dependencies)
Max parallel:       X (first round)
File conflicts:     Z (handled at merge)
Merge rounds:       ceil(Y / batch_size) + 1
```

If most tasks are independent, nearly everything runs in the first dispatch. Only genuinely blocked tasks wait.

---

## Step 6: Present the Plan

Show the dependency graph, NOT waves:

```
Plan: [Name]
Total tasks: 13 | Independent: 10 | Blocked: 3 | File conflicts: 5

DISPATCH IMMEDIATELY (10 tasks, all parallel):
  #1  feat-001 [sonnet] Add check command          files: check.go, main.go
  #2  feat-002 [sonnet] Add budget command          files: budget.go, main.go
  #3  feat-003 [sonnet] Add health command          files: health.go, main.go
  #4  feat-004 [sonnet] Research gate               files: research.go, main.go
  #5  feat-005 [sonnet] Feature spec generator      files: specgen.go, main.go
  #6  feat-006 [sonnet] TDD test case generator     files: tdd.go, main.go
  #7  feat-007 [sonnet] Worktree isolation          files: yolo.go
  #8  feat-008 [sonnet] Commit attribution          files: hook.go, hooks.json
  #9  feat-009 [sonnet] Diff review gate            files: review.go
  #10 feat-010 [sonnet] Agent lineage tracking      files: lineage.go

BLOCKED (3 tasks, dispatch after dependencies complete):
  #11 feat-011 [sonnet] Spec compliance scoring     blocked-by: #5 (needs spec generator)
  #12 feat-012 [sonnet] Session timeline dashboard  blocked-by: #8, #10 (needs tracking data)
  #13 feat-013 [sonnet] UI validation               blocked-by: #12 (needs dashboard)

FILE CONFLICTS (resolved at merge, not by serialization):
  main.go:   tasks #1-#6 all add registrations — merge sequentially
  hooks.json: task #8 — single owner, no conflict

Merge strategy:
  Round 1: Merge all 10 independent branches, resolve main.go conflicts
  Round 2: Dispatch #11 after #5 merges. Dispatch #12 after #8+#10 merge.
  Round 3: Dispatch #13 after #12 merges.

To execute: /htmlgraph:execute
```

---

## Step 7: Agent Type Selection

| Agent | When to Use | Cost |
|-------|------------|------|
| `htmlgraph:haiku-coder` | Single-file, clear spec, <50 lines, no design decisions | Lowest |
| `htmlgraph:sonnet-coder` | Multi-file, needs codebase context, moderate complexity | Medium |
| `htmlgraph:opus-coder` | Architecture decisions, complex algorithms, novel design | Highest |

**Default to sonnet** unless the task is trivially simple (haiku) or requires deep reasoning (opus).

---

## Decision Rules

### When to add `addBlockedBy`

Add ONLY when:
- Task B imports/uses a module that Task A creates from scratch
- Task B extends an interface that Task A defines
- Task B's test fixtures depend on Task A's schema

Do NOT add when:
- Both tasks edit the same file (file conflict, not dependency)
- Tasks are in the same domain but logically independent
- "It would be nice" to have A before B but B could technically be written first

### When to split a task

Split when:
- Task touches >10 files (YOLO budget advisory)
- Task has >300 new lines (YOLO budget advisory)
- Task mixes unrelated concerns (e.g., CLI command + hook + dashboard)

---

## Related Skills

- **[/htmlgraph:execute](/htmlgraph:execute)** - Execute the plan with dependency-driven dispatch
- **[/htmlgraph:parallel-status](/htmlgraph:parallel-status)** - Monitor execution progress
- **[/htmlgraph:cleanup](/htmlgraph:cleanup)** - Clean up worktrees after completion
