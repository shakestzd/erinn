# Soft File-Conflict Detection — Design Doc

**Spike:** spk-b3d44b06  
**Track:** trk-0677c709 (Launch Readiness)  
**Author:** Researcher agent  
**Date:** 2026-04-15  
**Status:** Draft

---

## 1. Problem Statement

When multiple htmlgraph sessions (e.g. separate Claude Code worktrees working in
parallel on the same repo) issue Edit or Write tool calls to the same file, they
silently overwrite each other. No warning is raised by Claude Code or by the
htmlgraph hooks layer. The last write wins, and the loser may not notice until a
later merge conflict or a test failure surfaces the clobber.

### Reproduction sketch (not a real repro — illustrative only)

```
Session A (trk-abc, feat-x branch):
  PreToolUse → Edit internal/hooks/yolo_guard.go  ← starts writing

Session B (trk-def, feat-y branch):
  PreToolUse → Edit internal/hooks/yolo_guard.go  ← also starts writing, no warning

Session A commits its edits.
Session B commits its edits.
git merge: conflict, or (if both sessions worked on different line ranges) silent semantic overwrite.
```

The worst case is the silent variant: both commits apply cleanly and one session's
work disappears without any merge marker alerting the developer.

---

## 2. Current State of File-Attribution Data

### What the schema already has

**`feature_files`** (table 12 in `internal/db/schema.go`):
```sql
CREATE TABLE feature_files (
    id         TEXT PRIMARY KEY,
    feature_id TEXT NOT NULL,
    file_path  TEXT NOT NULL,
    operation  TEXT NOT NULL DEFAULT 'unknown',
    session_id TEXT,
    first_seen DATETIME,
    last_seen  DATETIME,
    UNIQUE(feature_id, file_path)
)
```

`internal/db/feature_files_repo.go` already ships:
- `UpsertFeatureFile` — refreshes `last_seen` and `session_id` on every edit
- `ListFeaturesByFile(db, filePath)` — returns all features that have touched a file
- `TraceFile(db, filePath)` — enriches with title/status/track
- `ResolveFileOwner(db, filePath)` — returns the most frequent touching feature

**`agent_presence`** (table 13):
```sql
CREATE TABLE agent_presence (
    agent_id           TEXT PRIMARY KEY,
    status             TEXT CHECK(status IN ('active','idle','offline')),
    current_feature_id TEXT,
    last_tool_name     TEXT,
    last_activity      DATETIME,
    session_id         TEXT
)
```

**`claims`** (table 5):
- Has a `write_scope JSON` column, currently unpopulated in practice.
- The claims system tracks which `(work_item_id, agent_id)` is active, with
  lease expiry. An active claim for a work item whose `feature_files` contains
  a given path is the closest existing proxy for "who owns this file right now."

**`sessions`**:
- Tracks `status` (active/completed/paused/failed) and `project_dir`.
- No direct file-level foreign key.

### Can we answer "who is touching /path/to/file right now?"

**Partially.** The following query returns features that have touched a file
recently, cross-joined with their claims to find the live session:

```sql
SELECT ff.feature_id, f.title, f.status, c.owner_session_id,
       c.status AS claim_status, ff.last_seen
FROM feature_files ff
JOIN features f ON f.id = ff.feature_id
LEFT JOIN claims c ON c.work_item_id = ff.feature_id
WHERE ff.file_path = '/path/to/file'
  AND c.status IN ('proposed','claimed','in_progress')
  AND c.lease_expires_at > datetime('now')
ORDER BY ff.last_seen DESC;
```

**Gaps that make this unreliable today:**
1. `feature_files.last_seen` is updated at upsert time but the UNIQUE constraint
   is `(feature_id, file_path)` — so session A and session B working on the same
   file under *different* features each get their own row, and the query correctly
   surfaces both. Good.
2. `claims.write_scope` is unused — it *could* list the file paths the claim
   intends to touch, but nothing writes to it today.
3. Claims can expire silently (lease TTL). A session that is actively editing but
   missed a heartbeat will appear to have no live claim. The hook would miss the
   conflict.
4. `agent_presence.last_activity` ages out quickly; there is no "currently editing
   file X" signal beyond the `feature_files.last_seen` timestamp.
5. The `last_seen` timestamp is only as fresh as the *last committed upsert*. A
   session that started an edit but hasn't flushed yet won't be visible.

**Verdict:** the data exists to detect *historical* file contention (e.g. "two
features touched this file in the last 10 minutes"). It is **insufficient** for
real-time "is another session editing this file *right now*?" without a lighter,
more immediate signal.

---

## 3. Proposed Mechanisms

### Option A — Hook-based: PreToolUse query against `feature_files` + `claims`

On every `PreToolUse` event for `Edit`, `Write`, or `MultiEdit`, the hook:
1. Resolves the target file path from `tool_input`.
2. Queries `feature_files` joined to active `claims` (lease not expired) for any
   feature other than the current session's active feature.
3. If a match exists and `last_seen` is within a configurable recency window
   (default: 15 minutes), emits a warning in the hook result's `reason` field.
   Does **not** block.

```go
// Pseudocode for the check
matches := db.Query(`
  SELECT ff.feature_id, f.title, c.owner_session_id
  FROM feature_files ff
  JOIN features f ON f.id = ff.feature_id
  JOIN claims c ON c.work_item_id = ff.feature_id
  WHERE ff.file_path = ?
    AND ff.feature_id != ?            -- not the current feature
    AND c.status IN ('proposed','claimed','in_progress')
    AND c.lease_expires_at > datetime('now')
    AND ff.last_seen > datetime('now', '-15 minutes')
`, filePath, currentFeatureID)

if len(matches) > 0 {
    result.Reason = fmt.Sprintf("WARN: %s also recently edited by %s (%s)",
        filePath, matches[0].Title, matches[0].FeatureID)
}
```

**Pros:**
- Builds on existing data structures; zero schema changes required.
- Warning surfaces in the Claude Code UI as a non-blocking hook annotation.
- Soft: orchestrator sees it and can choose to pause, comment, or ignore.

**Cons:**
- Detection relies on `claims.lease_expires_at` being kept fresh via heartbeats.
  Stale leases produce false negatives (missed conflicts).
- The recency window is a heuristic; it doesn't know if the other session is
  *currently* editing vs finished 14 minutes ago.
- Hook runs synchronously in PreToolUse — adds a DB round-trip to every Edit.
  Must be fast (<10ms); should skip if DB unavailable.
- Does not prevent races between PreToolUse check and the actual write.

**Invasiveness:** Low. Adds ~30 lines to an existing hook handler. No schema change.

---

### Option B — Lock files: `.htmlgraph/locks/<path-sha>.lock`

When a session begins an Edit/Write, the hook writes a lock file:
```
.htmlgraph/locks/<sha256-of-file-path>.lock
```
containing:
```json
{
  "file_path": "internal/hooks/yolo_guard.go",
  "session_id": "sess-abc",
  "feature_id": "feat-xyz",
  "locked_at": "2026-04-15T10:00:00Z",
  "ttl_seconds": 300
}
```

On PreToolUse for a file:
1. Compute the lock file path.
2. If a lock file exists and its `locked_at + ttl_seconds` is in the future:
   emit a warning with the locking session's identity.
3. Write (or overwrite) the lock file for the current session.

On PostToolUse or `WorktreeRemove`, delete the lock file.

**Pros:**
- No DB dependency — works even when SQLite is unavailable or hook skips DB init.
- File system is the ground truth; consistent with the plugin-development rule
  ("prefer file/branch state over session state for hook gates").
- Lock visibility is immediate: no lag from upsert propagation.
- Easy to inspect manually (`ls .htmlgraph/locks/`).

**Cons:**
- Lock files are not automatically cleaned on crash/kill — requires TTL-based
  stale-lock detection and a periodic or startup cleanup pass.
- `.htmlgraph/locks/` in a linked worktree resolves to the *main repo's*
  `.htmlgraph/` (since `HTMLGRAPH_PROJECT_DIR` points there), giving the correct
  cross-worktree visibility. But this must be verified — if a worktree resolves
  to a different `.htmlgraph/` path the lock files would be partitioned and
  cross-worktree detection would fail.
- The lock granularity is file-level. A single large file touched by many
  features would accumulate many lock/unlock cycles.
- Two sessions could read "no lock" and both write simultaneously (TOCTOU). Soft
  detection only — not a mutex.
- The `.htmlgraph/` write guard in `pretooluse.go` (`isHtmlGraphWrite`) currently
  blocks all Write/Edit tool calls to `.htmlgraph/`. Lock file creation must go
  through the Go binary hook handler itself (not the Write tool), or an exemption
  for `.htmlgraph/locks/` must be added to the guard.

**Invasiveness:** Low–Medium. Requires a new `lock` + `unlock` helper (~50 lines),
a startup/cleanup pass, and modifications to PreToolUse and PostToolUse handlers.
No schema change. Requires a guard exemption for `.htmlgraph/locks/`.

---

### Option C — Worktree auto-isolation: suggest or auto-create a worktree on collision

When Option A or Option B detects a file being touched by another active session,
the orchestrator is notified via the hook warning. The orchestrator can then:
1. Pause the current task.
2. Call `createFeatureWorktree` (already implemented in `cmd/htmlgraph/yolo.go`)
   to create an isolated `git worktree add` checkout.
3. Re-dispatch the agent into the new worktree with a clear merge strategy.

The existing `createFeatureWorktree` call flow:
```
git worktree add .claude/worktrees/<featureID> -b yolo-<featureID>
excludeHtmlgraphFromWorktree(worktreePath)
reindexWorktree(worktreePath)
```

`WorktreeCreate` and `WorktreeRemove` hooks are already wired in
`cmd/htmlgraph/hook.go` and `internal/hooks/missing_events.go`.

**Pros:**
- True isolation — edits in one worktree cannot clobber another.
- Leverages fully-tested infrastructure that is already in production.
- Merge is explicit and auditable (git merge / rebase after both sessions complete).

**Cons:**
- Heavy: git worktree creation is slow (~100ms+) and adds a branch that must be
  managed and eventually merged.
- The orchestrator must be aware of the collision before dispatching the second
  agent — reactive worktree creation mid-task is disruptive (the agent has
  already started edits on main).
- Merge strategy ("how does session B's work get reconciled with session A's?") is
  out of scope for this spike and requires explicit user guidance.
- Does not help when both sessions are already on separate worktrees but editing the
  same shared file (e.g. a generated config or a schema file).

**Invasiveness:** Medium–High. Requires orchestrator-level change (not just a hook
side effect), new CLI plumbing to surface "create worktree for isolation" as a
response to a collision warning, and a merge-back workflow.

---

## 4. Recommendation

**Phase 1 (minimal viable, implement first): Option B — lock files.**

The lock-file approach is the most robust real-time signal and aligns with the
project rule to prefer file/branch state over session state for hooks. It gives
immediate cross-session visibility with no DB latency or lease-TTL gaps. The
implementation is self-contained and does not require changes to `feature_files`,
`claims`, or the graph model.

Concrete MVP:
- `internal/hooks/file_lock.go` — acquire(path, sessionID, featureID, ttl),
  release(path), peekConflict(path, currentFeatureID) returning an optional
  warning string.
- PreToolUse handler for `Edit`/`Write`/`MultiEdit`: call `peekConflict`, emit
  warning in `reason` if non-empty, then call `acquire`.
- PostToolUse handler for the same tools: call `release`.
- On session start: call `releaseAllForSession(sessionID)` to clear stale locks
  from a prior run.
- Add exemption in `containsHtmlgraphDir` (or a separate predicate) to allow
  writes to `.htmlgraph/locks/` from the hook binary itself.

**Phase 2 (enhance): Option A as a complement.**

After Phase 1, add the DB query as a *supplementary* check for cases where a
session did not clean up its lock (crash before PostToolUse). The DB query is
more durable (survives restarts) and provides richer context (feature title,
track). Together, lock-file (real-time) + DB query (historical) gives both
signals.

**Option C: deferred.** Worktree auto-isolation is the right long-term answer for
parallel independent streams of work, but it requires orchestrator-level changes
and a merge-back workflow that are out of scope here. It should be a separate
feature under trk-0677c709 and built *after* the conflict signal (Phase 1 + 2) is
operational — there is no point isolating into worktrees if nothing can detect the
need.

---

## 5. Open Questions (need user input before implementation)

1. **Recency window for DB check (Option A):** What is a reasonable `last_seen`
   threshold to consider a file "actively being edited"? 5 minutes? 15 minutes?
   Should it be configurable via `.htmlgraph/hooks-config.json`?

2. **Lock TTL:** What TTL should lock files use? If an agent crashes mid-edit,
   how long should the lock persist before being treated as stale? Suggested
   default: 10 minutes. Acceptable?

3. **Warning vs. block:** Should the conflict check ever become a *hard block*
   (i.e. deny the tool call) for certain conditions (e.g. the other session's
   lock is fresh and from a different track)? Or always soft-warn only?

4. **`write_scope` on claims:** Should Phase 1 also populate `claims.write_scope`
   with the set of files being edited, so that the DB-based query becomes reliable
   enough to be the sole mechanism (removing need for lock files)? This would
   require the claim heartbeat to refresh `write_scope` continuously.

5. **Cross-worktree `.htmlgraph/` resolution:** Confirm that a session running
   inside `.claude/worktrees/trk-xxx/` resolves its `HTMLGRAPH_PROJECT_DIR` to the
   *main repo's* `.htmlgraph/`, not a local one. If not, lock files will be
   partitioned and cross-worktree detection will silently fail. (Ref: `cmd/htmlgraph/main.go`
   git worktree detection logic.)

---

## 6. Out of Scope for This Spike

- Actual implementation of any option (design only).
- Merge strategy / conflict resolution workflow after isolation into a worktree.
- UI / dashboard visualization of file contention.
- Performance benchmarking of the DB query path at scale.
- Integration with `feat-9e1143f8` (Rich work graph): the graph feature will
  add typed edges between sessions and file paths; this spike should not duplicate
  or pre-empt that model. The lock-file approach is intentionally outside the
  graph model and does not require waiting for feat-9e1143f8.
- Enforcement or access control (this is observability, not a security boundary).
