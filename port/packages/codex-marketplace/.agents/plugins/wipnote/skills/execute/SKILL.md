---
name: wipnote:execute
description: Execute a parallel plan using dependency-driven dispatch. Coordinate ready work items, partition non-conflicting slices, and keep all spawned agents attributed and closed.
---

# wipnote Parallel Execute

Use this skill to execute development work in parallel with dependency-driven dispatch and isolated agents.

**Trigger keywords:** execute plan, run plan, run tasks, parallelize work, work in parallel, start execution, dispatch agents

## Environment

When running in a worktree, `WIPNOTE_PROJECT_DIR` is set automatically. All `wipnote` CLI commands resolve to the main project's `.wipnote/`. Run commands directly unless a tool explicitly needs a different directory.

## Efficiency Rules

Every tool call spends a turn. Aim to dispatch the first subagent quickly.

1. Use `wipnote execute-preview <trk-id> --format json` as the main discovery call.
2. Reuse that payload instead of re-querying the same track or feature data repeatedly.
3. Batch related `wipnote` probes when you need extra state.
4. Let readiness and file overlap decide dispatch order; do not build a manual wave plan first.

## Work Item Attribution

Before dispatching agents:

1. Run `wipnote status` and confirm the active feature, bug, or track.
2. If the target work item is not active, start it first.
3. Every spawned agent prompt must include:
   - `wipnote feature start <feat-id>` or `wipnote bug start <bug-id>` as the first command
   - completion of the same work item after quality gates pass

Without attribution, the execution graph stays incomplete.

## Core Loop

Run a dispatch loop driven by readiness, not by manual waves:

1. Identify ready work: pending items with no unresolved dependencies.
2. Check file overlap among ready candidates.
3. Spawn all non-conflicting ready tasks in parallel.
4. Wait for each spawned agent to finish.
5. Review results, merge the completed work, and run quality gates.
6. Recompute readiness and repeat until no work remains.

If only one ready task remains, use the same loop; the parallel step just has one agent.

## Step 1: Find Ready Work

Use the preview payload as the source of truth.

Treat linked features or bugs with pending status and no blockers as ready candidates. Keep blocked items out of the current dispatch set. Reuse the track title when describing the overall execution session.

If the track does not yet have executable work items, stop and create or promote them first.

## Step 2: Check File Overlap

Before parallel dispatch, compare the file sets for ready candidates.

For each candidate:

1. Run `wipnote trace <work-item-id>` to inspect attributed files.
2. Compute pairwise overlap.
3. Partition ready work into the largest non-conflicting set you can safely run together.

Rules:

- No overlap: dispatch every ready candidate now.
- Partial overlap: dispatch the non-conflicting subset first and defer the conflict to the next loop.
- Full overlap: dispatch one candidate, note the constraint, and continue after merge.

Dependency independence is not enough if two agents will both edit the same file.

## Step 3: Spawn Agents

Use Codex lifecycle primitives directly.

For each chosen task, call `spawn_agent` with:

- the appropriate agent type
- a concise description
- the full task prompt, including attribution and test expectations

Example structure:

```text
spawn_agent(
  agent_type="wipnote-feature-coder",
  description="feat-1234: Add execute preview cache",
  message="""
Feature: feat-1234

FIRST command: wipnote feature start feat-1234

Goal: implement the slice described below.
Files to edit: internal/foo.go, internal/foo_test.go
Requirements:
- write or update focused tests
- run relevant quality gates
- complete the feature when passing
Report changed files and tests run.
"""
)
```

Use `wipnote-patch-coder` for tiny, tightly scoped edits and `wipnote-feature-coder` for ordinary feature slices unless the plan clearly calls for another role.

## Step 4: Track Running Agents

Immediately record the handles returned by `spawn_agent`.

Maintain a live table in your notes with:

- work item id
- agent handle
- intended files
- dispatch time

This keeps the execution state easy to resume.

## Step 5: Wait and Unblock

Use the lifecycle primitives in this order:

1. `wait_agent` for a spawned handle until it yields a result or needs input.
2. If the agent asks for clarification and you can answer from existing context, use `send_input`.
3. When an agent is done and no further interaction is needed, `close_agent` its handle.

Guidelines:

- Prefer concrete file or requirement context when answering.
- If an agent is stuck on a failing test or merge conflict, resolve whether to clarify, defer, or reroute before spawning more overlapping work.
- Close idle handles promptly so the execution state stays clean.

## Step 6: Merge and Verify

After an agent completes:

1. Inspect its summary and changed files.
2. Merge or apply the completed work.
3. Run the relevant quality gates for the touched surface.
4. Only then mark the work item complete.

If quality gates fail, reopen the loop with a narrower follow-up task instead of carrying broken state forward.

## Step 7: Repeat Until Done

After each successful merge:

1. Refresh readiness from `wipnote execute-preview <trk-id> --format json`.
2. Re-run the file overlap check on the newly ready set.
3. Spawn the next non-conflicting batch.

Stop when there are no pending ready items and all blocked items have either completed dependencies or been explicitly deferred.

## Failure Handling

When a spawned agent fails:

1. capture the failure summary
2. decide whether a short `send_input` can unblock it
3. if not, `close_agent` the failed handle
4. create or queue a narrower follow-up task before retrying

Do not leave abandoned agent handles hanging around after a failed wave.

## Completion Checklist

Before you declare execution finished:

- every spawned agent has been waited on
- any necessary clarifications were sent with `send_input`
- completed handles were closed with `close_agent`
- merged changes passed relevant tests and checks
- work items were completed in `wipnote`
- remaining blocked or deferred work is explicitly called out
