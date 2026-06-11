# From MVP to Canonical: Three Releases, Three Hard Lessons in AI Dev Observability

*April 9–10, 2026 · ~36 hours from first ship to architectural stability*

---

## 1. What I Set Out to Build

Last year I got tired of not knowing what my AI coding agents had actually done.

Claude Code sessions accumulate hundreds of tool calls — file reads, edits, bash commands, test runs — and the only record is a raw JSONL transcript buried in `~/.claude/projects/`. There's no dashboard, no way to ask "which feature did this session contribute to?", no way to see the graph of which work items depend on which. When something went wrong, the debugging workflow was: find the session ID, open the JSONL, count lines. Not great.

So I built wipnote — local-first observability for AI-assisted development. The core idea is simple: hook into Claude Code's event stream, write every tool call into an HTML file as it happens, keep a SQLite index for fast queries, and expose a dashboard. HTML files as the canonical store means the data survives even if the database gets corrupted or deleted — you just reindex.

That's the theory. Here's what shipping it actually looked like.

---

## 2. The Multi-Project MVP (v0.52.0, April 9 00:17 EDT)

The first version of multi-project support shipped on April 9 at 12:17 AM. Eight commits on top of v0.51.0, tagged v0.52.0.

The implementation was exactly what you'd reach for first: a `--global` flag on the server that loaded every registered project's SQLite database, cached them in memory, and dispatched each API request to the right database by parsing a `?project=<id>` query parameter from the URL. The dashboard got a project switcher UI — a `<select>` element that reloaded the current view scoped to the selected project by appending `?project=<id>` to every API call. A `/api/mode` endpoint told the frontend whether to show the switcher.

It worked. The graph loaded, sessions appeared, features were attributable per project. I shipped it and went to sleep.

I found the flaw before noon the same day.

---

## 3. The Flaw I Found Within Hours

The `?project=<id>` dispatch pattern had a fundamental problem: every write operation in the system used the server's working directory and environment to determine which project it was writing to.

In single-project mode, that's fine — you launch the server from inside the project, it inherits the project's `CWD`, and every hook, every ingest, every work item creation lands in the right place.

In `--global` mode, the server runs from whatever directory you started it in. Reads were correctly dispatched by project ID. Writes were not — they defaulted to the server's own context. A write operation that came in for project B would actually land in whichever project the server's process owned. Silent cross-project contamination.

[VERIFY: the spec describes "cross-project contamination risk" — the plan description for plan-237fb251 confirms this as the architectural flaw: "project-scoped writes route through global server's read-only cache with wrong CWD/env/hook context". Confirm whether any actual data was contaminated or if this was caught pre-incident.]

The more I stared at the code, the worse it got. The per-project SQLite DB handles were cached in memory by the global server but opened read-only to avoid accidental writes. That was a guard against one class of contamination, but it exposed another: any write path that fell through to default behavior would silently hit the wrong project. There was no isolation boundary — just one process holding multiple projects' data simultaneously.

The right fix wasn't a patch. It was an architectural change.

---

## 4. The Rewrite: Per-Project Child Processes (v0.53.0, April 9 23:49 EDT)

By mid-afternoon on April 9, I had a plan (literally — plan-237fb251, committed at 4:47 PM). Six slices, approved, in flight.

The new architecture:

- A **parent doorway server** that holds zero SQLite handles. Its only job is routing.
- One **child process per project**, spawned on demand via a new `internal/childproc` supervisor package.
- Each child runs `htmlgraph _serve-child` with `--project-dir <dir>`, inheriting the correct working directory and environment for that project.
- The parent forwards `/p/<id>/*` traffic to each child's ephemeral port via `httputil.ReverseProxy`.

The handshake protocol is blunt and effective: the parent scans the child's stdout for an exact pattern `htmlgraph-serve-ready port=<N> pid=<P>` with a 5-second deadline. If the handshake doesn't arrive, the spawn fails. The pre-built reverse proxy points at `127.0.0.1:<N>`.

Crash recovery was a consequence of the design, not a separate feature. The `cmd.Wait` reaper removes dead children from the supervisor's map. The next request to that project calls `GetOrSpawn`, which sees no warm child and forks a fresh one. No special recovery logic required — the normal spawn path handles it.

Idle reap runs on a 60-second tick, killing children whose `LastRequest` timestamp is stale. This caps resource usage when you have ten projects registered but only two active.

The old `dispatchByProject` function and all its cross-project aggregation code — `globalRecentEventsHandler`, `globalSSEHandler`, the in-memory DB cache — were deleted in commit `ebc2c1800`: 555 lines removed, replaced with a 90-line doorway. The parent's entire API surface shrank to `/api/mode → {"mode":"global"}` and the `/p/<id>/*` reverse proxy.

v0.53.0 shipped at 11:49 PM on April 9 — about 23 hours after the MVP.

---

## 5. The Next Gap: Sessions Weren't HTML-Canonical

The isolation problem was solved. Something else was broken.

wipnote's stated invariant is that session HTML files are the canonical store. SQLite is a derived read index — you should be able to delete the database and reconstruct it completely by running `htmlgraph reindex`. The HTML files are committed to the repo; the SQLite database is not.

The ingest path didn't honor this. When you ran `htmlgraph ingest` on a JSONL transcript, it wrote SQLite rows but no session HTML file. That meant ingested sessions existed in the database but not as canonical files. If you deleted SQLite and reindexed, those sessions vanished.

Similarly, the live hook path had a gap: `PreToolUse` wrote a "started" marker, and `PostToolUse` was supposed to consolidate both events into a single `<li>` in the HTML. But if the process crashed between the two — tool call started, never completed — the marker stayed in the database as `status='started'` forever, leaving a permanent hole in the session history with no way to recover.

And for the corpus of sessions that existed *before* the HTML-canonical architecture was designed, there were no HTML files at all — only SQLite rows. The `reindex` command had nothing to read.

Three gaps. One plan (plan-43659c92, four slices). One more release.

---

## 6. The Rebuild: Live-Append HTML, Crash Recovery, Migration (v0.54.0, April 10 12:56 EDT)

v0.54.0 shipped the next morning. Four slices in one day:

**Slice 1 — Ingest renders HTML.** `hooks.RenderIngestedSessionHTML` was added to the ingest pipeline. It scaffolds the session HTML via `CreateSessionHTML`, appends each tool call through the same flock-protected write path that live hooks use, and finalizes the header. The skip-if-exists guard means live-hook-written files are never overwritten by ingest — the live hooks are authoritative for sessions they observed.

**Slice 2 — Orphan sweep.** `db.FindOrphanedEvents` finds `status='started'` rows older than 5 minutes. `hooks.SweepOrphanedEventsForSession` writes synthetic `<li data-status="aborted" data-reason="no-post-hook">` entries through the flock path, then marks the SQLite row swept. The dedup check is keyed on `data-event-id` so the sweep is idempotent even across crash-recovery scenarios. Lazy triggers: end of PostToolUse (session-scoped) and end of SessionStart (project-scoped), plus a standalone `htmlgraph sweep orphaned-events` CLI.

**Slice 3 — Backfill migration.** `htmlgraph migrate sessions --dry-run`. A one-shot command that finds every session in SQLite without a corresponding HTML file and renders one. It prefers re-parsing the original JSONL transcript from `~/.claude/projects/` for fidelity; falls back to the stored SQLite rows when the transcript is gone. Per-session errors don't abort the run. After migration, every pre-existing session has an HTML file and `reindex` can reconstruct from them.

**Slice 4 — Round-trip acceptance test.** The load-bearing integration test in `session_roundtrip_test.go`. Five subtests: ingest → reindex, live hook simulation → reindex, orphan sweep → reindex (verifies `aborted` status survives), migration → reindex, and the concurrent writer stress test: 20 goroutines across three writer types (live PostToolUse, ingest render, orphan sweep) all hitting the same session HTML file simultaneously. Every `data-event-id` must be present in the final file, and `reindex` must rebuild all 20 rows. Verified stable across 10 stress runs.

One bug surfaced during slice 4 development: concurrent sweep goroutines could double-post the same synthetic entry because the goquery dedup check was outside any mutex. Fix: `MarkEventAborted` now uses `UPDATE ... WHERE event_id=? AND status='started'` and returns `RowsAffected` — only the goroutine that wins the state transition writes the HTML entry. The goquery check stays as a second line of defense.

---

## 7. The Proof It Works: Nuke the Database, Reindex

The acceptance test covers all this in-process, but the real test is operational.

After v0.54.0 landed, I deleted the SQLite cache for the wipnote repo itself.

[VERIFY: the spec describes a "destructive SQL incident followed by successful reindex recovery" as the proof. I cannot verify from git history whether this was a deliberate test, an accidental deletion, or a corruption event. If you have notes on what actually happened — rm, corruption, schema migration gone wrong — fill in the specifics here. The reindex capability is verifiable from the code (feat-229f3333 "Reindex rebuild guarantees for dashboard-critical rows" also landed around this time as a follow-on); the specific incident needs your firsthand account.]

What I can verify: the integration test in slice 4 does exactly this — nuke the DB, run `htmlgraph reindex`, assert every row comes back. The design passes. If the real operational test of deleting your own project's SQLite and running reindex succeeded, that's the story worth telling — because it means the invariant held under actual pressure, not just test conditions.

---

## 8. What I Learned

Three releases in 36 hours teaches you things faster than three months of planning.

**The architectural flaw in the MVP wasn't obvious from reading the code — it was obvious from thinking about the failure mode.** The `?project=<id>` pattern looked correct: reads went to the right DB, the project switcher filtered the UI. The contamination risk only became visible when I asked "what happens when a write lands here?" That question should have been the first question, not the one I asked after shipping.

**The plan-review-critique workflow caught the load-bearing errors in the rewrite before code.** The Multi-Project Hardening plan (plan-237fb251) went through multiple rewrites and a critic pass before any code was written. The feasibility critic flagged that a per-test go build for the integration tests would cost 5-15 seconds each and blow through CI budgets. The fix — a `TestMain` that builds once and shares the binary across all tests — was in the final plan, not a post-hoc optimization. When you're doing surgery on your server's core isolation model, having a critic tell you "this test strategy doesn't work" before you've written 400 lines of test code is worth the hour it costs.

**HTML-canonical is the right invariant, but you have to actually enforce it.** I wrote the invariant down — "SQLite is a derived read index, delete it and reindex" — long before v0.54.0. But the ingest path violated it from day one. The invariant was aspirational, not enforced. The v0.54.0 work made it structural: every write path now goes through a chokepoint that produces HTML, and the round-trip acceptance test makes regression on the invariant a CI failure. An invariant you can't test isn't an invariant; it's a comment.

---

## [OPTIONAL EPILOGUE — newer material]

The arc above ends at v0.54.0 (April 10, 2026). By May–June, several things happened that extend the story:

- **Modular carve-out.** The binary was renamed from `htmlgraph` to `wipnote`, reflecting a broader scope: the tool now ships to Codex CLI and Gemini CLI in addition to Claude Code, from a single plugin source tree generated by `wipnote plugin build-ports`. The multi-harness architecture forced formalization of what had been implicit — agent frontmatter schemas, hook event matrices, per-target output paths.

- **Architectural memory system.** The project added a durable arch-card system (`wipnote arch`) for capturing decisions, hazards, and invariants that don't belong in code comments or PR descriptions. The session-HTML canonical invariant from beat 8 above is now an arch card — something future agents can query before making a decision that would violate it.

- **The system catching its own builders' bugs.** In May 2026, roborev (the code-review automation built on top of wipnote) started flagging bugs in wipnote's own codebase — a plan-finalize flow that stranded after cache invalidation (bug-eca8141d), cross-project bleed in the statusline cache (bug-95dc78ba). The observability layer that started as "I want to see what my agents did" became the instrument catching errors in its own development. That loop closing on itself is either the intended outcome or a useful coincidence, depending on your priors.

- **Plans as first-class data.** The plan system (started as an internal tool for structured slice-based work) became an exposed API: `wipnote plan chat`, critic passes, amendment parsing from conversation. The insight from beats 3-4 above — that a critic pass before code prevents wasted effort — is now a documented workflow in the plugin.

---

*Draft status: awaiting author review. See [VERIFY] items above for specifics that need firsthand confirmation.*
