---
name: htmlgraph:plan
description: Create a parallelized development plan with research synthesis and worktree-based execution. Activate when asked to create a plan, parallelize work, or organize development tasks.
---

# HtmlGraph Parallel Plan

Use this skill when asked to plan development work, create a parallel execution plan, or organize tasks for multi-agent execution.

**Trigger keywords:** create plan, development plan, parallel plan, plan tasks, parallelize work, organize work, task breakdown

---

## Core Principle: Plan Before Executing

A good parallel plan has three properties:
1. **Independent tasks** run in the same wave (no file conflicts)
2. **Dependent tasks** run in later waves (after blockers complete)
3. **File conflicts** are detected and resolved before dispatch

---

## Step 1: Analyze the Conversation for Tasks

Extract all tasks from the conversation. For each task, identify:
- What files it touches
- What other tasks it depends on
- How complex it is (haiku for well-defined, sonnet for complex)
- Its priority (blocker > high > medium > low)

---

## Step 2: Spawn 5 Parallel Research Agents

Before building the plan, gather evidence. Spawn all five simultaneously (single message):

```python
# Agent 1: Similar solutions in codebase
Task(
    subagent_type="gemini",
    description="Find similar existing implementations",
    prompt="""
    Search the codebase for patterns similar to [task description].
    Look for: existing patterns, naming conventions, module structure.
    Report file paths and relevant code snippets.
    """
)

# Agent 2: Library and dependency analysis
Task(
    subagent_type="gemini",
    description="Analyze available libraries and dependencies",
    prompt="""
    Check pyproject.toml and existing imports for relevant libraries.
    What tools are already available for [task domain]?
    Report: available packages, their usage patterns in codebase.
    """
)

# Agent 3: Codebase patterns and conventions
Task(
    subagent_type="gemini",
    description="Identify codebase patterns and conventions",
    prompt="""
    Search for coding conventions relevant to [task domain].
    Check: type annotation style, error handling patterns, test structure.
    Report patterns with file examples.
    """
)

# Agent 4: Spec validation
Task(
    subagent_type="gemini",
    description="Validate task specifications for completeness",
    prompt="""
    Review the task requirements:
    [task list]

    For each task, identify:
    - Missing requirements or ambiguities
    - Edge cases not addressed
    - Potential conflicts between tasks
    Report findings concisely.
    """
)

# Agent 5: Dependency and file conflict analysis
Task(
    subagent_type="gemini",
    description="Analyze file dependencies and conflict risks",
    prompt="""
    For these tasks: [task list]
    Analyze which files each task would likely touch.
    Identify: shared files that multiple tasks edit, circular dependencies.
    Report conflict risks.
    """
)
```

---

## Step 3: Synthesize Research Findings

After all five agents complete, read their findings and synthesize:

- Which tasks share files (potential merge conflicts)
- Which patterns to follow (consistency)
- Which tasks have unresolved ambiguities (resolve before planning)
- Library choices already available (avoid adding deps)

---

## Step 4: Build the Plan with PlanBuilder

```python
from htmlgraph.planning import PlanBuilder
from htmlgraph import SDK

sdk = SDK(agent="claude-code")
builder = PlanBuilder(sdk, name="[Descriptive Plan Name]")

# Add tasks with dependencies and metadata
builder.add_task(
    id="task-001",
    title="[Task title]",
    description="[Detailed spec including: files to create/edit, expected output, acceptance criteria]",
    priority="blocker",          # blocker | high | medium | low
    agent_type="haiku",          # haiku for well-defined, sonnet for complex
    files=["src/module/file.py"] # Files this task touches (for conflict detection)
)

builder.add_task(
    id="task-002",
    title="[Task title]",
    description="[Detailed spec]",
    priority="high",
    agent_type="sonnet",
    files=["src/module/other.py"],
    depends_on=["task-001"]      # Only add when there's a real dependency
)

# Build and display
plan = builder.build()
print(plan.summary())
```

---

## Step 5: Task Classification Rules

**Independent tasks (same wave):**
- Touch different files
- No logical dependency on each other
- Could be completed in any order

**Dependent tasks (later waves):**
- Task B imports from Task A
- Task B tests Task A's output
- Task A creates a schema Task B uses

**Agent type selection:**
- `haiku`: Single-file change, clear spec, < 50 lines, no design decisions
- `sonnet`: Multi-file, requires reading existing code to understand context, moderate complexity
- `opus`: Architecture decisions, complex algorithm design (use sparingly)

**Priority levels:**
- `blocker`: Other tasks cannot start without this
- `high`: Core functionality, needed soon
- `medium`: Important but not urgent
- `low`: Nice to have, can defer

---

## Step 6: File Conflict Detection

Before finalizing the plan, check for conflicts:

```python
# Check for tasks touching the same files
conflicts = plan.detect_conflicts()

if conflicts:
    for conflict in conflicts:
        print(f"WARNING: Tasks {conflict.task_a} and {conflict.task_b} both edit {conflict.file}")
        print(f"  Resolution: Move {conflict.task_b} to wave after {conflict.task_a}")
```

Resolve conflicts by:
1. Making one task depend on the other (sequential)
2. Splitting the file into separate concerns (parallel)
3. Assigning one task as owner of the file (parallel with communication)

---

## Step 7: Present the Plan

Show the user:

```
Plan: [Name]
Total tasks: N | Waves: W | Estimated speedup: Xx

Wave 1 (parallel):
  - task-001 [haiku] [blocker] - Title
  - task-003 [haiku] [high]    - Title

Wave 2 (parallel, after Wave 1):
  - task-002 [sonnet] [high]   - Title (depends on: task-001)
  - task-004 [haiku] [medium]  - Title (depends on: task-003)

Wave 3 (parallel, after Wave 2):
  - task-005 [sonnet] [medium] - Title (depends on: task-002, task-004)

Conflict warnings:
  [none | list any detected]

To execute: /htmlgraph:execute
```

---

## Step 8: Parallel Execution Optimization (REQUIRED for all plans)

After creating the initial plan, ALWAYS restructure it for parallel execution using git worktrees. This is mandatory — every plan must be optimized for parallelism before presenting to the user.

### 8a. Dependency Analysis

Group tasks by files touched and identify parallelism:

- List all files each task reads or writes
- Group tasks that share NO file overlap → candidates for parallel streams
- Group tasks that depend on another task's output → sequential constraint (later stream)
- Mark long-running or optional work as independent streams (can start anytime)

Example analysis:
```
Task A (hooks/pretooluse.py, hooks/event_tracker.py) — no overlap with B
Task B (api/routes.py, api/templates/) — no overlap with A
Task C (tests/test_integration.py) — depends on A and B outputs
→ A and B run in parallel, C is the sequential integration gate
```

### 8b. Restructure Phases as Streams

Rebuild the track phases to reflect parallel streams. Name each phase so it maps directly to a worktree:

```python
from htmlgraph import SDK
import subprocess, os

sdk = SDK(agent="claude-code")
repo_name = os.path.basename(os.getcwd())

track = sdk.tracks.builder() \
    .title("[Descriptive Track Title]") \
    .with_plan_phases([
        (f"Stream A: [Domain A] (worktree: {repo_name}-stream-a)", [
            "Task 1 description (Xh)",
            "Task 2 description (Xh)",
        ]),
        (f"Stream B: [Domain B] (worktree: {repo_name}-stream-b)", [
            "Task 3 description (Xh)",
        ]),
        ("Stream C: Integration Tests + Merge (after A AND B complete)", [
            "Merge Stream A and B branches (0.5h)",
            "Run full test suite (0.5h)",
            "Deploy (0.5h)",
        ]),
        ("Stream D: Long-term / Independent (anytime)", [
            "Optional or long-horizon work (Xh)",
        ]),
    ]) \
    .create()
```

Only include streams that have actual tasks. Omit Stream D if there is no independent work.

### 8c. Create Git Worktrees Automatically

After creating the track, immediately create worktrees for all parallel streams (those NOT gated behind other streams). Do NOT create worktrees for sequential gate streams like Stream C — those are created when their dependencies finish.

```python
track_short = track.id.replace("trk-", "")[:8]
created_worktrees = []

# Only parallel streams get worktrees now — sequential gates come later
parallel_streams = [
    ("stream-a", f"{repo_name}-stream-a"),
    ("stream-b", f"{repo_name}-stream-b"),
]

for stream_id, worktree_dir in parallel_streams:
    branch = f"{stream_id}-{track_short}"
    worktree_path = f"../{worktree_dir}"
    result = subprocess.run(
        ["git", "worktree", "add", worktree_path, "-b", branch],
        capture_output=True, text=True, cwd=os.getcwd()
    )
    if result.returncode == 0:
        created_worktrees.append((worktree_path, branch, stream_id))
    else:
        print(f"Note: {worktree_path}: {result.stderr.strip()}")

# Verify
verify = subprocess.run(["git", "worktree", "list"], capture_output=True, text=True)
print(verify.stdout)
```

### 8d. Output Execution-Ready Summary

End the plan command with this summary so the user can act immediately:

```
## Plan Complete — Ready to Execute

Track: {track.id} — {track.title}

Parallel streams (run simultaneously):
  Stream A: ../{repo}-stream-a  (branch: stream-a-{short_id})
  Stream B: ../{repo}-stream-b  (branch: stream-b-{short_id})

Sequential gate:
  Stream C: Integration Tests + Merge — starts after A AND B complete

Say "start it" to launch parallel subagents into these worktrees.
```

If worktree creation failed for any stream, show the manual git commands instead:
```bash
git worktree add ../{repo}-stream-a -b stream-a-{short_id}
git worktree add ../{repo}-stream-b -b stream-b-{short_id}
```

---

## Integration with HtmlGraph

```python
from htmlgraph import SDK
sdk = SDK(agent="claude-code")

# Track plan creation
spike = sdk.spikes.create("Plan: [plan name]")
spike.set_findings(f"""
Research synthesis:
- Patterns found: [summary]
- Conflicts detected: [summary]
- Ambiguities resolved: [summary]

Plan: {plan.summary()}
""").save()
```

---

## Related Skills

- **[/htmlgraph:execute](/htmlgraph:execute)** - Execute the created plan
- **[/htmlgraph:parallel-status](/htmlgraph:parallel-status)** - Monitor execution progress
- **[/htmlgraph:cleanup](/htmlgraph:cleanup)** - Clean up after completion
- **[/strategic-planning](/strategic-planning)** - Analytics for prioritization
