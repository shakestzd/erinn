# Commit Outbox — Serialized wipnote Artifact Commit Queue

Status: implemented (MVP) — feat-76504033 (track trk-b1d06a84)

## Problem

Today every agent/CLI that completes a work item writes the canonical
`.wipnote` HTML/YAML and then commits it to git directly on its own hot path
(`commitWipnoteArtifact`, `commitPlanChange`). Under concurrency that puts every
agent on git's index-writing path. The repo-scoped advisory lock
(`runGitMutation`) serializes those writers, but each agent still blocks on git.

## Model: write-first, commit-later outbox

1. The canonical `.wipnote` write completes FIRST (unchanged).
2. A **commit intent** is appended to a durable outbox. This is the only extra
   work a producer does — it never touches git.
3. A single serialized committer (`wipnote commit-queue flush`) later drains the
   outbox in FIFO order, committing each artifact under the repo-scoped advisory
   git lock via `runGitMutation`, so it serializes against all other wipnote git
   writers and no agent sits on the git hot path.

## Location — per-user cache dir, NOT inside `.wipnote/`

The outbox is derived, local, and never committed — exactly like the SQLite
read-index and the `git-mutation.lock`. It lives in the per-user cache dir:

```
~/.cache/wipnote/<path-hash>/commit-outbox.ndjson
~/.cache/wipnote/<path-hash>/commit-outbox.deadletter.ndjson
```

The path is derived from `storage.CanonicalDBPath(repoRoot)` so the path-hash
keying is reused, not re-invented (`commitOutboxPath` in
`cmd/wipnote/commit_queue.go`). Putting the outbox inside `.wipnote/` would make
it itself need committing — recursion. Keeping it out of the working tree also
means it can never be accidentally staged.

## Format — append-only NDJSON

One JSON `Intent` per line:

```json
{"repo_root":"/repo","rel_paths":[".wipnote/features/feat-1.html"],
 "message":"wipnote: complete feat-1","work_item_id":"feat-1",
 "action":"complete","enqueued_at":"2026-05-26T...Z","attempts":0}
```

Append-only means a crash mid-write loses at most the last partial line; earlier
intents are intact. `readIntents` skips blank/partial/corrupt lines so one bad
trailing line never wedges the drain. Each append is an exclusive
`flock(LOCK_EX)` + `fsync` (mirrors `internal/otel/sink/ndjson`).

## Cross-operation locking

A dedicated sibling lock file (`commit-outbox.ndjson.lock`) serializes whole
operations. Both `Append` and the *entire* `Flush` snapshot → commit → rewrite
cycle run under `withLock`. The lock spans the whole flush — not just the
individual file writes — because `Flush` reads a snapshot, commits, then rewrites
the pending file with what remains; without a lock covering that whole window, an
`Append` landing between the snapshot and the rewrite would be silently dropped
by the stale-snapshot rewrite (a lost-update race). The lock file is *separate*
from the data file because `Flush` swaps the data file via `rename`, which would
shed a lock held on the data-file inode; the stable lock-file inode persists.

## Ordering, recovery, idempotency

- **FIFO**: intents drain oldest-first.
- **Drain under the lock**: production committer (`outboxCommitter`) stages and
  commits via `runGitMutation`.
- **Confirm-then-remove**: an intent is removed from the pending file only after
  its commit returns success. The pending file is rewritten once per pass via an
  atomic temp-file + rename, so a reader never sees a half-rewritten queue.
- **Restartable / idempotent**: if a flush is interrupted after some commits but
  before the rewrite, the next flush re-runs those intents. The underlying
  artifact commit is idempotent — an already-committed artifact yields "nothing
  to commit", treated as success — so re-running causes no double-commit harm
  and the queue converges to empty.

## Dead-letter / skip semantics

Each intent carries an `attempts` counter. On commit failure the counter is
incremented and the intent stays queued. Once `attempts` reaches `max-attempts`
(default 5) the intent is moved to the dead-letter NDJSON sibling and dropped
from pending. The drain continues with the next intent in the SAME pass, so one
poison commit can never freeze the ordered queue. `commit-queue flush` and
`commit-queue status` both surface the dead-letter depth.

## CLI

```
wipnote commit-queue status                 # pending / dead-letter depths + path
wipnote commit-queue flush                  # drain FIFO under the advisory lock
wipnote commit-queue flush --max-attempts N # override dead-letter threshold
```

## Scope and follow-ups (out of scope here)

This change ADDS the outbox mechanism, the `recordCommitIntent` producer API,
and the flush command. It does NOT change the existing direct-commit default —
the outbox is the durable alternative. Explicit follow-ups:

- Make the outbox the default autocommit path (route `commitWipnoteArtifact` /
  `commitPlanChange` through `recordCommitIntent`).
- A daemon/hook driver that flushes automatically (e.g. on `SessionStop`)
  instead of requiring a manual `flush` invocation.
- Surface pending/dead-letter depth in `wipnote status` alongside the writer
  queue line.
