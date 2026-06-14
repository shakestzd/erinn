# wipnote Architecture & Plan Portfolio Review — 2026-06-12

## 1. State of the Architecture

The HTML-canonical carve-out worked: `.wipnote/*.html` + NDJSON are the verified source of truth, canonical-first readers (`find`, `snapshot`) prove HTML-only reads are viable, and a write-owner daemon (lease, Unix socket, auto-spawn, 4 op types) shipped out-of-plan via feat-075c110d (done 2026-06-06) — hooks and the two hottest CLI sites already route through it with a counted fallback. What's actually fragile today is not transport but **integrity and process overhead**: the HTML writer corrupted committed canonical state twice in two days (bug-fddf5820, bug-91e8aa4c) with roborev as the only detector; the SQLite index lives on volatile `/tmp` in this devcontainer (overlayfs `~/.cache` fails the WAL check), wiping gate records and claim/liveness state on restart; and 66–69% of all commits are wipnote bookkeeping, flooding the review pipeline. The plan portfolio itself is the other fragility: a "done" parent plan with 16/16 slices never executed, a live deletion plan contradicting four others, and the spine plan stale against its own landed MVP — with zero supersession records anywhere.

## 2. Plan Verdicts

### plan-bb91616a — daemon write-owner + reactive projections + anomalies
**Verdict: REVISE — keep as the spine, re-baseline, cut roughly half.**

Load-bearing reasons (all verified):
1. **Stale against its own tree.** Slices 1–2 and material parts of 4–5 already landed via feat-075c110d (`serve_child.go:88 --headless`, `core/daemon/wire.go` implements the exact slice-1 envelope, `dbgate.go` daemon-first hook routing). Assumption A2 ("headless mode is greenfield") is now false; feat-d2754049 is a byte-identical duplicate todo of landed work. Verified correction: **17** intentionalCLIMutation sites exist (not the plan's 13), ~15 still direct — the plan is more stale than its own enumeration.
2. **Phase C (slices 8, 10–13, ~36% of plan) answers no observed pain.** Zero open polling bugs; the 9 duplicate-feed bugs were ingestion bugs, not transport. Meanwhile the plan is silent (zero YAML mentions) on the #1 observed pain — autocommit noise — despite building the exact daemon that `commit_queue.go:97-99` names as the outbox's missing driver.
3. **The rebuildable-derived premise has a real hole, with a built-in escape hatch.** Gate records ("intentionally NOT canonical", `gate_records.go:11`) and claims/liveness are not reconstructible, and the DB is on `/tmp` here. Nuance from verification: slice 7's registry *can* classify them "non-rebuildable" truthfully, and a wipe fails closed (forced gate re-run, not unsafe completion) — so the cost is gate-cost friction and temporarily blind collision detection, not corruption. What's missing is the remediation slice.

Concrete revisions:
- Add a **re-baseline slice**: fold feat-075c110d into the plan record, restate A2/A4 and slice 4/5 scope against the as-built tree; close feat-d2754049 as duplicate.
- Keep: slice 5 remainder (~15 sites), slice 6 (reindex coordination — `reindex_otel_events.go` still has zero daemon/lease awareness), slice 7 (extended with a gate/claims durability follow-up), slice 9, slice 14 shrunk to duplicate-agent + failed-write anomalies (28 redundant start commits since 06-07 justify it).
- Cut: slice 3 (spool — a distributed-systems component with zero observed incidents; the counted fallback is the last resort), slices 8, 10–13.
- Add: **commit-outbox daemon driver** (see §4 #1), canonical-HTML integrity check, and sandbox-aware fail-fast spawn (the permanent-failure-retried anti-pattern is the corpus's unifying failure mode).
- Do not retire the direct-open fallback until feat-156e0a1a's BUSY counters exist to justify it.

### plan-149eeb44 — Delete SQLite (Phase 4)
**Verdict: KILL — mark superseded by bb91616a with an explicit decision record.**

Load-bearing reasons (all verified):
1. **Unexecutable as written; precondition false.** Parent plan-3bbc313f is `status: done` with **16/16 slices not_started** — the precondition "all consumer migrations verified complete" rests on bookkeeping fiction. Every deletion target path (`internal/db/`, `cmd/htmlgraph/`, `packages/codex-plugin/`) no longer exists; `database/sql` importers grew from the plan's ~110 baseline to **187**; modernc.org/sqlite is required by 3 modules directly.
2. **The tree has already decided against it.** All daemon work (earliest commit 2026-06-03) post-dates the plan and hardens SQLite as the derived tier — exactly the parent plan's own escape clause ("SQLite allowed back strictly as a disposable derived speed tier"). An active plan (6ee9b0c8) adds SQLite schema. No plan anywhere references 149eeb44; the supersession is unrecorded.
3. **The stated problem is governance, not architecture**, and the enforcement primitive already exists: always-on `TestWritableDBOpenBoundary` (`sqlite_write_boundary_test.go:462`) plus bb91616a slice 7's classification registry achieve "make the promise real" at ~5% of the risk.

Salvage: extract slice 2's substance as a standalone S/Low item — exactly **14** SQLite mentions remain in the generated port trees, ~half genuinely stale (`internal/db/`, `.wipnote/wipnote.db` paths). Note: 6 live in shared source under `plugin/`, so regeneration alone won't fix them — edit source, reword to "disposable derived index," regenerate. Also reconcile plan-3bbc313f's done/not-started anomaly.

### plan-251676c5 — Distribution de-bundle + version-sync + container launchers
**Verdict: KILL — mark superseded by shipped v0.59 lockstep distribution.**

Load-bearing reasons (all verified):
1. **8 of 11 slices already shipped in different form or were obsoleted by the opposite decision** — including Homebrew (slice 8), which verification found *already ships* via `.goreleaser.yml`'s `brews:` section, contrary to the critique's defer recommendation. The tree chose lockstep single-tarball bundling (`upgrade_cmd.go:105`, since v0.59) — the inverse of slice 3's two-artifact split, which would reintroduce the High-rated skew window it exists to detect.
2. **Slice 1 would delete live, CI-tested functionality**: `plugin/hooks/bin/bootstrap.sh` is the SHA256-verifying install shim, packaged by goreleaser and covered by `bootstrap-smoke-test.yml`.
3. **Slice 4's block-on-missing-CLI path is logically unreachable**: hooks are `wipnote hook <handler>` — no CLI means the blocking hook never runs. The missing-CLI case is owned by bootstrap.sh, which downloads instead of blocking.

Salvage: one S slice — `wipnote doctor` cross-harness installed-version matrix (the single unmet goal; `launcher doctor` is worktree-only, verify-versions.sh is deploy-side). Flagged/dropped from earlier critique: the Homebrew-deferral recommendation (obsolete) and the loose scoping of bug-65887a3a as "the /tmp wipe fix" (its recorded scope is a non-Unix TMPDIR portability bug; the durability problem is broader and needs its own item).

### Adjacent plans (for sequencing)
- **plan-ee999752**: proceed first — freshest critique, current naming, explicitly feeds bb91616a's Phase A via the gate-migration checklist (slice 4). Answer slice-2's q-check-scope with the gate-cost evidence in mind: full `go test` on every commit at current commit volume is untenable; build+vet on commit, full check at complete.
- **plan-6ee9b0c8** (active, 0/4 slices started): proceed, but reconcile its slice-3 own-DB-connection goroutine with the daemon cutover explicitly — extend the daemon op set to cover tool_calls ingest, or land before fallback retirement, and record which.
- **plan-a33c983e**: defer; redraft post-daemon (its in-process bus competes with daemon eventing; FTS5 support in modernc is unconfirmed). The transition log (slice 3) is the valuable kernel — it enables the cycle-time metric analytics currently lacks.

## 3. What the Dogfooding History Teaches

| Finding | Number | Implication |
|---|---|---|
| Bookkeeping commit noise | 176–183 of 266 commits since 06-07 (**66–69%**) | Autocommit-per-state-change is the dominant process cost |
| Duplicate-start churn | 28 redundant start commits, 17 items, **38% of starts are re-claims** | Claim coordination is strained; validates the duplicate-agent anomaly |
| Review spam | **80%** of recent roborev jobs review `wipnote:` metadata commits; ~90% of open verdicts "No issues found" | Review pipeline is mostly burning cycles on zero-content commits |
| Completion-gate cost | ~13 completes/day × 5–6 min full suite ≈ **65–80 min/day** of wall time | Gate re-runs mostly re-validate bookkeeping-only changes |
| SQLITE_BUSY cluster | 39 bugs, **8 still open**; proof slice feat-156e0a1a still todo | Contention is contained, not closed — and unproven |
| Canonical-store corruption | **2 incidents in 2 days** (edge-href mapping gaps), detected only by roborev | The layer everything depends on has no first-party integrity check |
| Env fragility | /tmp DB wipe, TMPDIR test hazard, sandbox 401s, bwrap — unified by **permanent failures retried as transient** (roborev retried 401s 3×15 jobs) | Failure-classification, not retry logic, is the gap |
| Maintenance ratio | 1.1 bugs/feature (tool self-flags as high) | — |

## 4. Ranked Opportunities

1. **Wire `internal/commitqueue` into the daemon: batch/debounce bookkeeping autocommits + suppress redundant re-claim commits.** Why: #1 pain by volume (66%+ noise, 80% of review load, 28 redundant starts); both halves exist (outbox undriven per `commit_queue.go:97-99`; daemon landed). Effort: **M**. Belongs to: bb91616a (new slice, promoted to first deliverable). Companion quick win: exclude `wipnote:`-prefixed commits from roborev triggering (**S**).
2. **Portfolio hygiene pass: supersession records for 149eeb44 and 251676c5, reconcile plan-3bbc313f's done/not-started anomaly, re-baseline bb91616a, close feat-d2754049.** Why: nothing records any resolution; a live deletion plan contradicts four building plans; a duplicate todo invites duplicate work. Effort: **S**. Quick win.
3. **`wipnote fsck` — canonical-HTML integrity check at reindex/daemon-write time** (hrefs resolve, collection-aware mapping incl. prefixless session IDs, track_id preserved). Why: two corruption incidents in two days; roborev is the only detector; every plan depends on this layer. Effort: **S–M**. Belongs to: bb91616a (new slice).
4. **Completion-gate cost reduction: tree-hash-keyed gate caching extended to the completion gate.** Why: 65–80 min/day of suite runs, mostly on bookkeeping changes; ee999752 slice 2 already builds the tree-hash cache. Effort: **S–M**. Belongs to: ee999752 (extend slice 2's mechanism).
5. **ee999752 slices 1–4 (always-on commit gate + gate registry).** Why: any non-YOLO session on any harness can currently commit a failing build; slice 4 is bb91616a's explicit input. Effort: **M**. Belongs to: ee999752.
6. **DB durability off /tmp** (devcontainer-safe persistent path, e.g. allow non-WAL `~/.cache` or a project-local derived dir; fix bug-65887a3a's portability bug alongside, but scope the durability item separately). Why: restart wipes gate records (forced re-runs → compounds #4) and claim state; AGENTS.md/runtime inconsistency. Effort: **S**. Quick win.
7. **bb91616a Phase A remainder: ~15 CLI sites + reindex-as-daemon-op, landed with feat-156e0a1a's contention observability gate.** Why: 8 open BUSY bugs; the checkpoint race is real and uncoordinated; without counters, "BUSY is gone" is unverifiable (bug-3eaeb7c2 flags false-pass risk). Effort: **L**. Belongs to: bb91616a + ae0c37b2.
8. **6ee9b0c8: sub-agent tool_calls ingestion**, reconciled with the daemon op set. Why: active plan, real attribution bug, small; must not reintroduce the independent-writer pattern. Effort: **M**.
9. **Port-tree doc fix** (14 SQLite mentions; edit the 6 shared-source files, regenerate) + **`wipnote doctor` version matrix**. Why: the only salvage from the two killed plans; doc-drift discipline already exists. Effort: **S** each. Quick wins.
10. **Permanent-vs-transient failure classification** (sandbox spawn, autocommit 401, codex bwrap): fail fast and permanently on environmental signatures. Why: the unifying anti-pattern across the failure corpus. Effort: **M**, spread across touchpoints.
11. **Slice 14 (shrunk): duplicate-agent + failed-write anomalies.** Why: 38% re-claim rate is direct evidence. Effort: **M**. Belongs to: bb91616a.
12. **Deferred: a33c983e transition log → cycle-time analytics.** Effort: **M**, post-daemon.

Explicitly cut: bb91616a slices 3, 8, 10–13; all of 149eeb44 slice 1; all of 251676c5 except the doctor matrix.

## 5. Recommended Sequencing

1. **Now (S, this week):** Portfolio hygiene pass (#2) + roborev metadata-commit exclusion + DB durability (#6) + port-tree doc fix (#9). All small, all unblock or de-noise everything else.
2. **Next:** ee999752 (slices 1–4, with q-check-scope answered as build+vet-on-commit / full-on-complete) — it feeds bb91616a and carries item #4's cache mechanism.
3. **Then:** 6ee9b0c8 (#8), with the daemon-op-set decision recorded.
4. **Then:** Rescoped bb91616a — **commit-outbox driver first** (#1), then fsck (#3), Phase A remainder + feat-156e0a1a (#7), slice 7 + durability follow-up, shrunk slice 14. Fallback retirement only after counters justify it.
5. **Deferred indefinitely:** bb91616a Phase C (keep slice 9 only); a33c983e redraft post-daemon; anything Homebrew/distribution (already shipped).

The single most important act is step 1: the portfolio's biggest defect isn't the SQLite contradiction — the tree already resolved that in bb91616a's favor — it's that **nothing records the resolutions**, so superseded drafts remain live invitations to contradictory work.