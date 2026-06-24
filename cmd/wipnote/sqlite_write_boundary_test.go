// SQLite writable-open enforcement boundary — slice 5 of plan-ae0c37b2,
// updated through slice 7 (plan-2390966a).
//
// Architectural rule: there must be exactly one writer process per project
// database. Slice 6 introduced the dedicated writer service; slice 7
// migrated the hot hook writes to daemon-first enqueue-only routing. Without
// this enforcement gate, the codebase can drift back into "direct writable
// opens", which silently recreates the SQLITE_BUSY contention the plan
// eliminated.
//
// This file is the enforcement boundary. It maintains an explicit inventory
// of every first-party Go callsite that opens a writable SQLite handle and
// fails the build when:
//
//  1. A new writable open appears in a forbidden path (hook, collector,
//     indexer, event-capture) without being added to the inventory.
//  2. An inventory entry no longer matches a real callsite (stale entry).
//  3. A forbidden-path entry is mis-classified as something other than
//     daemon-routed-writer-service or canonical-first-hook-fallback.
//     (daemon-routed-pending-slice-6 is RETIRED — no entries use it; any
//     new forbidden-path open must go through the daemon, not add to the
//     legacy pending classification.)
//
// SCOPE — IMPORTANT:
//
// This boundary scans first-party Go source under cmd/, internal/ ONLY.
// (plugin/ is markdown / static assets only — verified at scan time.)
// MCP servers, third-party plugins, and external tools that open the DB
// file directly are EXPLICITLY OUT OF SCOPE — Go-level enforcement cannot
// reach them. That is a known limitation documented in the plan's review
// critique (review-2026-05-11) and surfaced in the inventory comment for
// the receiver/writer entry.
//
// HOW TO EXTEND:
//
//   - New canonical-first command: no inventory change needed (does not open DB).
//   - New CLI command that legitimately mutates work items: add to inventory
//     with classification "intentional-cli-mutation".
//   - New reindex command: add with "reindex-only".
//   - New schema migration runner: add with "migration-only".
//   - New hook / collector / indexer write path: STOP. Route it through
//     RouteHookWrite / RouteInsertEvent (daemon-first enqueue-only seam,
//     plan-2390966a). Do not add a direct open and do not use the retired
//     daemon-routed-pending-slice-6 classification.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeSiteClassification labels how a callsite is allowed to open the DB
// in writable mode. The labels deliberately read like a status board so
// reviewers can see at a glance which entries still need migration to the
// writer service.
type writeSiteClassification string

const (
	// daemonRoutedPendingSlice6 is RETIRED — no inventory entries use it.
	// It was the migration staging label for hook/indexer write sites while
	// slices 3-7 (plan-2390966a) moved them onto daemon-first enqueue-only
	// routing (RouteHookWrite / RouteInsertEvent). The constant is kept to
	// preserve the historical classification vocabulary and to prevent the
	// compiler from rejecting the known[] map in
	// TestWriteSiteInventoryComplete; it is excluded from the
	// isForbiddenPathClassification allow-list so any new forbidden-path
	// entry using it will cause the boundary test to fail.
	daemonRoutedPendingSlice6 writeSiteClassification = "daemon-routed-pending-slice-6"

	// daemonRoutedWriterService marks the slice-6 writer service's own
	// internal `Open` — the single writable SQLite handle that the queue
	// worker uses to apply all serialized writes. There is exactly one
	// such site per project DB by architectural invariant. This is the
	// terminal classification: producers that route through the queue
	// no longer need their own entry in this inventory.
	daemonRoutedWriterService writeSiteClassification = "daemon-routed-writer-service"

	// canonicalFirstHookFallback marks the single writable open used by
	// hook subprocesses (`wipnote hook <name>` spawned by Claude Code).
	// Slice 7 (feat-33c26c74) consolidated the three formerly-direct
	// `db.Open` call sites in cmd/wipnote/hook.go into one helper
	// (internal/hooks/dbgate.go: OpenHookDB) so the failure-tolerance
	// contract — log + count fallback, return canonical-success — lives
	// at ONE auditable boundary.
	//
	// Architectural rationale: hook subprocesses can't reach the in-process
	// queue inside `wipnote serve`. They still need to read project context
	// and emit derived-index rows synchronously while data is fresh. The
	// canonical NDJSON write upstream (in the handler tree) makes any
	// failed open safely recoverable on the next reindex cycle.
	canonicalFirstHookFallback writeSiteClassification = "canonical-first-hook-fallback"

	// intentionalCLIMutation marks user-driven CLI commands that legitimately
	// mutate work items (e.g., wipnote feature start). These keep direct
	// writable opens because they are short-lived foreground processes;
	// slice 6's queue is for high-frequency hook/indexer/collector traffic.
	intentionalCLIMutation writeSiteClassification = "intentional-cli-mutation"

	// reindexOnly marks call sites for the wipnote reindex family. Reindex
	// is the rebuild path — it rebuilds the SQLite read index from canonical
	// HTML/NDJSON state, and is the ONE writer-of-record while running.
	reindexOnly writeSiteClassification = "reindex-only"

	// migrationOnly marks call sites that exist solely to run schema
	// migrations (wipnote init, wipnote migrate). Migrations are run-once
	// and must keep a direct writable handle to apply DDL.
	migrationOnly writeSiteClassification = "migration-only"
)

// writeSite describes one approved writable SQLite open in first-party
// Go source. The triple (file, line, openExpr) is the de-duplication key.
// note SHOULD explain why this site exists and what (if anything) will
// migrate it onto the slice-6 writer service.
type writeSite struct {
	File           string                  // path relative to module root, forward slashes
	Line           int                     // 1-indexed source line of the open call
	Function       string                  // enclosing function name
	OpenExpr       string                  // "db.Open" | "dbpkg.Open" | "sql.Open" | "db.OpenWritable" | "dbpkg.OpenWritable"
	Classification writeSiteClassification // see constants above
	Note           string                  // human-readable rationale
}

// approvedWriteSites is the canonical inventory. To add a new entry, scroll
// to the matching classification block and insert in alphabetical order
// by File. To remove an obsolete entry, delete the line.
//
// MAINTENANCE: daemon-routed-pending-slice-6 entries have been fully
// retired (slices 3-7, plan-2390966a). The forbidden-path inventory now
// contains only daemon-routed-writer-service (the slice-6 writer service's
// own handle) and canonical-first-hook-fallback (the single daemon-miss
// open in core/hooks/dbgate.go). isForbiddenPathClassification no longer
// accepts the legacy pending classification — any new forbidden-path entry
// must use one of the two currently-allowed classes.
var approvedWriteSites = []writeSite{
	// ----------------------------------------------------------------------
	// daemon-routed-writer-service / canonical-first-hook-fallback
	// (FORBIDDEN PATHS — explicitly classified)
	// ----------------------------------------------------------------------
	// Slice 7 (feat-33c26c74, plan-2390966a) consolidated the former direct
	// opens in cmd/wipnote/hook.go into a single helper at
	// core/hooks/dbgate.go:OpenHookDB. Slices 3-7 then migrated the hot
	// hooks (SessionStart, pretooluse, user-prompt, subagent-start, Stop) to
	// RouteHookWrite / RouteInsertEvent (enqueue-only daemon seam), so
	// OpenHookDB is now the DAEMON-MISS FALLBACK ONLY — never the primary
	// path. The hook tree has exactly one approved writable open, reached
	// rarely on healthy systems.
	{
		File:           "core/hooks/dbgate.go",
		Line:           149,
		Function:       "OpenHookDB",
		OpenExpr:       "db.Open",
		Classification: canonicalFirstHookFallback,
		Note:           "DAEMON-MISS FALLBACK ONLY (plan-2390966a slices 3-7). Hot hooks (SessionStart, pretooluse, user-prompt, subagent-start, Stop) route derived-index writes through RouteHookWrite / RouteInsertEvent (enqueue-only daemon seam, apply.RouteSQLAsync); this direct db.Open is reached ONLY when the daemon is unavailable/spawn-forbidden/queue-full. On a reachable-daemon system the open is rarely or never called. Logs a structured `writer_unavailable` fallback and returns nil-DB on open failure; callers MUST treat nil as canonical-success. The canonical NDJSON write upstream guarantees reindex recovers any rows neither path could write.",
	},
	{
		File:           "observe/otel/receiver/writer.go",
		Line:           96,
		Function:       "NewWriter",
		OpenExpr:       "sql.Open",
		Classification: daemonRoutedWriterService,
		Note:           "Slice 6 writer service (feat-f3bcbcef): the single writable SQLite handle owned by the writequeue worker inside `wipnote serve`. Indexer + OTLP receiver no longer open writable handles directly — they submit batches through internal/db/writequeue to this writer.",
	},
	{
		File:           "observe/otel/sink/sqlite/writer.go",
		Line:           66,
		Function:       "NewWriter",
		OpenExpr:       "sql.Open",
		Classification: daemonRoutedWriterService,
		Note:           "OTel sink SQLite writer (companion to feat-f3bcbcef slice 6): structured sink for the OTel signal write path, owns the single writable handle for the otel_signals table family.",
	},

	// ----------------------------------------------------------------------
	// intentional-cli-mutation (CLI commands that mutate work items)
	// ----------------------------------------------------------------------
	{
		File:           "cmd/wipnote/ingest_gemini.go",
		Line:           57,
		Function:       "runIngestGemini",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote ingest gemini`; short-lived foreground process.",
	},
	{
		File:           "cmd/wipnote/plan_feedback_cmd.go",
		Line:           76,
		Function:       "planFeedback",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote plan feedback`; short-lived foreground process.",
	},
	{
		File:           "cmd/wipnote/plan_finalize_yaml.go",
		Line:           69,
		Function:       "finalizeYAML",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote plan finalize-yaml`; short-lived foreground process.",
	},
	{
		File:           "cmd/wipnote/plan_typed_sections.go",
		Line:           52,
		Function:       "buildTypedPlanSections",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "Plan rendering helper used by CLI plan commands; best-effort optional open.",
	},
	{
		File:           "cmd/wipnote/plan_yaml_cmds.go",
		Line:           560,
		Function:       "openPlanDB",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "Plan CLI helper for plan create/edit/finalize commands.",
	},
	{
		File:           "cmd/wipnote/plan_yaml_extras.go",
		Line:           524,
		Function:       "applyAcceptedAmendments",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven plan amendment apply; short-lived foreground process.",
	},
	{
		File:           "internal/gate/check.go",
		Line:           453,
		Function:       "PersistRecord",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "`wipnote check --gate` persists the session-local gate record after foreground build/vet/test execution completes.",
	},
	{
		File:           "cmd/wipnote/plan_yaml_extras.go",
		Line:           664,
		Function:       "runReadFeedbackYAML",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote plan read-feedback-yaml`; short-lived.",
	},
	// bug-7dbaf552: `wipnote query` (runQuery) was switched from the writable
	// dbpkg.Open to dbpkg.OpenReadOnly — graph.ExecuteDSL is strictly
	// SELECT-only, so it no longer needs (and must not hold) the writer lock.
	// Its former intentional-cli-mutation inventory entry is therefore
	// removed; the read-only open is not a writable-boundary site.
	// feat-075c110d increment 2: runServeChild (the HTTP dashboard child) NO
	// LONGER opens a writable handle. It now opens the project DB read-only
	// (dbpkg.OpenReadOnlyMigrated — a brief bootstrap Open inside internal/db,
	// which is OUT of scan scope, then a read-only handle) and ensures the
	// headless writer DAEMON is running for every write. Its former
	// intentional-cli-mutation entry (serve_child.go:246, runServeChild) is
	// therefore removed: with serve running there is exactly ONE writable
	// SQLite handle per project — the daemon's (runWriterOnly below).
	//
	// runWriterOnly is the SOLE per-project writable opener while serve runs.
	// It opens the writable handle to run schema/migrations AND to back the
	// background maintenance loops (auto-ingest, ai-title backfill, indexer
	// prompt-ID bridge, retention) that moved here from runServeChild; the
	// daemon socket listener funnels all socket-delivered ops through the
	// writequeue worker (receiver.NewWriter). No HTTP mux.
	{
		File:           "cmd/wipnote/link_commit.go",
		Line:           62,
		Function:       "runLinkCommit",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote link commit`; short-lived foreground process that links a commit to a work item.",
	},
	{
		File:           "cmd/wipnote/serve_child.go",
		Line:           163,
		Function:       "runWriterOnly",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "Headless writer-only daemon (feat-075c110d increment 2): the SOLE writable handle per project while serve runs. Backs schema/migrations, the background maintenance loops (auto-ingest, ai-title backfill, indexer prompt-ID bridge, retention — MOVED here from runServeChild), and the daemon socket listener's writequeue worker (receiver.NewWriter). The HTTP serve_child (runServeChild) is now strictly read-only and ensures+reaps this daemon.",
	},
	{
		File:           "cmd/wipnote/plan_interview.go",
		Line:           316,
		Function:       "serveInterviewForm",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "feat-2852d0c8 cross-harness plan interview web form: short-lived foreground server that mounts the same plan API as the dashboard (planRouter) so the embedded plan-review chat works. Needs a writable handle because the chat persists feedback/amendments. Process exits on submit; one open for the form's lifetime.",
	},
	{
		File:           "cmd/wipnote/serve_child.go",
		Line:           401,
		Function:       "runServeChild",
		OpenExpr:       "dbpkg.OpenWritable",
		Classification: intentionalCLIMutation,
		Note:           "bug-528478ad: dashboard mutation endpoints (plan feedback POST, finalize, delete, chat, manual session ingest) require a writable handle. Read routes use the read-only `database` handle. MaxOpenConns=1 serialises with the writer daemon. These are low-frequency user-triggered writes that cannot yet be expressed as daemon op_types (no wire-protocol expansion in scope).",
	},
	{
		File:           "cmd/wipnote/session.go",
		Line:           229,
		Function:       "openDB",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "Package-level writable-open helper used by many mutating CLI paths (session start/end, claim, ingest, backfill, blame, cleanup, compliance, report, who, …). Read-only commands must NOT use openDB — they open read-only, either via the cmd-level openReadOnlyDB helper or a direct dbpkg.OpenReadOnlyMigrated call. This inventory entry exists to catch any future read-only command that wrongly calls the writable openDB instead. feat-075c110d MVP-4: the two highest-contention session writes (start→InsertSession, end→UpdateSessionStatus) are now routed through the per-project writer daemon FIRST (apply.RouteSessionInsert / RouteSessionStatus, bounded ~2s, auto-spawn) and only use this direct handle as the fallback on daemon miss; the open itself stays direct because it still backs the read paths and other session mutations.",
	},
	{
		File:           "cmd/wipnote/status.go",
		Line:           90,
		Function:       "runStatus",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "`wipnote status` opens writable to run pending migrations (best-effort) before read.",
	},
	{
		File:           "cmd/wipnote/sweep.go",
		Line:           44,
		Function:       "sweepOrphanedEventsCmd",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "User-driven `wipnote sweep` orphan cleanup CLI command.",
	},
	{
		File:           "cmd/wipnote/track.go",
		Line:           187,
		Function:       "openTrackDB",
		OpenExpr:       "db.Open",
		Classification: intentionalCLIMutation,
		Note:           "Helper for `wipnote track show` CLI command.",
	},
	{
		File:           "core/workitem/project.go",
		Line:           80,
		Function:       "Open",
		OpenExpr:       "dbpkg.Open",
		Classification: intentionalCLIMutation,
		Note:           "Canonical entry point for every CLI work-item operation (feature/bug/spike/track start/complete). feat-075c110d MVP-4: the highest-contention work-item write — the start/complete status transition (UpdateFeatureStatus, bug-74a7bda7) — is now routed through the per-project writer daemon FIRST (apply.RouteFeatureStatus, bounded ~2s, auto-spawn) and only falls back to this direct handle on daemon miss; the open itself stays direct because it still backs reads, claim/release, and step-counter writes.",
	},

	// ----------------------------------------------------------------------
	// reindex-only (rebuilds SQLite from canonical HTML/NDJSON)
	// ----------------------------------------------------------------------
	{
		File:           "cmd/wipnote/lazy_reindex.go",
		Line:           42,
		Function:       "ensureIndexPopulated",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "bug-4b07fd94: brief writable open for cold-clone staleness check (COUNT on features/graph_edges). Closed immediately before any reindex write path runs.",
	},
	{
		File:           "cmd/wipnote/lazy_reindex.go",
		Line:           93,
		Function:       "runFullSyncReindex",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "bug-4b07fd94: lazy full-reindex on cold clone — reuses the same reindex primitives as `wipnote reindex --full`.",
	},
	{
		File:           "cmd/wipnote/lazy_reindex.go",
		Line:           120,
		Function:       "runFullSyncReindex",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "bug-4b07fd94: second open in runFullSyncReindex for plan-edge rebuild pass after main handle is closed.",
	},
	{
		File:           "cmd/wipnote/purge_spikes.go",
		Line:           126,
		Function:       "runFullReindex",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "Full-reindex helper invoked after spike purge.",
	},
	{
		File:           "cmd/wipnote/reindex.go",
		Line:           52,
		Function:       "runReindex",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "`wipnote reindex` top-level command.",
	},
	{
		File:           "cmd/wipnote/reindex_orphans.go",
		Line:           82,
		Function:       "runReindexBackfillOrphans",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "`wipnote reindex backfill-orphans` reindex variant.",
	},
	{
		File:           "cmd/wipnote/reindex_otel_events.go",
		Line:           76,
		Function:       "reindexOtelEvents",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "Slice 9 (feat-229f3333): bridge handle for the prompt_id correlation pass inside the OTel NDJSON replay. Reads orphans + writes UPDATE on agent_events.prompt_id only; the receiver.Writer owns the otel_signals write path. Disjoint tables, single-process reindex — no contention with the main writer.",
	},
	{
		File:           "cmd/wipnote/recap_list.go",
		Line:           105,
		Function:       "openRecapsIndex",
		OpenExpr:       "dbpkg.Open",
		Classification: reindexOnly,
		Note:           "feat-7bc6410b (slice-10): writable open for `wipnote recap list|show|delete` to refresh the recaps read-index (reindexRecaps) from the canonical .wipnote/recaps/*.html before querying. Same reindex family as the other reindex-only sites; single-process CLI invocation.",
	},

	// ----------------------------------------------------------------------
	// migration-only (schema bootstrap / DDL upgrades)
	// ----------------------------------------------------------------------
	{
		File:           "cmd/wipnote/init.go",
		Line:           79,
		Function:       "initDatabase",
		OpenExpr:       "db.Open",
		Classification: migrationOnly,
		Note:           "`wipnote init` runs the first-time schema migrations.",
	},
	{
		File:           "cmd/wipnote/migrate.go",
		Line:           62,
		Function:       "runMigrateSessions",
		OpenExpr:       "dbpkg.Open",
		Classification: migrationOnly,
		Note:           "`wipnote migrate sessions` schema upgrade command.",
	},
	{
		File:           "cmd/wipnote/migrate_attribution.go",
		Line:           80,
		Function:       "runMigrateAttributionFix",
		OpenExpr:       "dbpkg.Open",
		Classification: migrationOnly,
		Note:           "`wipnote migrate attribution-fix` schema upgrade command.",
	},
	{
		File:           "cmd/wipnote/migrate_normalize.go",
		Line:           88,
		Function:       "runMigrateNormalize",
		OpenExpr:       "dbpkg.Open",
		Classification: migrationOnly,
		Note:           "`wipnote migrate normalize-paths` (feat-39b81fa6): one-shot data migration that rewrites absolute host paths in .wipnote/ artefacts to repo-relative form. Run-once foreground CLI command; same shape as the other migration entries.",
	},
}

// forbiddenPathPrefixes is the set of first-party directories where a
// writable SQLite open MUST be marked daemon-routed-writer-service or
// canonical-first-hook-fallback. daemon-routed-pending-slice-6 is retired
// and no longer accepted (architectural ratchet — see
// isForbiddenPathClassification). Hook, collector, indexer, and
// event-capture paths are the contention sources the plan targets; new
// writes in these paths must route through RouteHookWrite / RouteInsertEvent.
var forbiddenPathPrefixes = []string{
	"cmd/wipnote/hook.go",     // hook event handlers
	"core/hooks/",             // hook implementations (moved out of internal/ — feat-0e3f1b3f)
	"observe/otel/indexer/",   // NDJSON→SQLite indexer (lifted into observe/ — feat-67f3ab7f)
	"observe/otel/receiver/",  // OTLP HTTP receiver writer (lifted into observe/ — feat-67f3ab7f)
	"observe/otel/collector/", // OTLP collector spawn (defensive — not currently a writer)
}

// scannedDirs lists the first-party Go directories the boundary covers.
// plugin/ holds only markdown / static assets (verified by the file-walk).
var scannedDirs = []string{"cmd", "internal", "core", "plan", "port", "observe"}

// excludedDirs lists package directories whose internal sql.Open / Open
// calls are NOT caller sites — they are the canonical open primitives
// themselves. core/db defines Open / OpenWritable / OpenReadOnly,
// which by definition must call into the SQLite driver. The boundary
// rule applies to CALLERS of these primitives, not to the primitives.
var excludedDirs = []string{
	"core/db",
}

// foundSite captures one writable-open occurrence discovered by the AST scan.
type foundSite struct {
	File     string
	Line     int
	Function string
	OpenExpr string
}

// TestWritableDBOpenBoundary is the enforcement gate. It walks the
// first-party Go source tree, finds every writable SQLite open, and
// compares against approvedWriteSites. The test fails on:
//
//  1. A new writable open that is not in approvedWriteSites (review/migration trigger).
//  2. An approved entry that no longer matches a real callsite (stale entry).
//  3. A forbidden-path entry that is not marked daemon-routed-writer-service
//     or canonical-first-hook-fallback (architectural ratchet: hook/indexer/
//     receiver/collector writes MUST route through RouteHookWrite /
//     RouteInsertEvent; daemon-routed-pending-slice-6 is retired).
func TestWritableDBOpenBoundary(t *testing.T) {
	root := findModuleRoot(t)

	found, err := scanWritableOpens(root)
	if err != nil {
		t.Fatalf("scan writable opens: %v", err)
	}

	// Build lookup keyed by file:line:openExpr — unique per call site.
	type key struct {
		File     string
		Line     int
		OpenExpr string
	}
	mkKey := func(f, expr string, line int) key { return key{File: f, Line: line, OpenExpr: expr} }

	foundByKey := make(map[key]foundSite, len(found))
	for _, fs := range found {
		foundByKey[mkKey(fs.File, fs.OpenExpr, fs.Line)] = fs
	}

	approvedByKey := make(map[key]writeSite, len(approvedWriteSites))
	for _, ws := range approvedWriteSites {
		approvedByKey[mkKey(ws.File, ws.OpenExpr, ws.Line)] = ws
	}

	// 1. New direct opens not in the inventory.
	var newSites []foundSite
	for k, fs := range foundByKey {
		if _, ok := approvedByKey[k]; !ok {
			newSites = append(newSites, fs)
		}
	}

	// 2. Inventory entries with no matching real call site.
	var staleEntries []writeSite
	for k, ws := range approvedByKey {
		if _, ok := foundByKey[k]; !ok {
			staleEntries = append(staleEntries, ws)
		}
	}

	// 3. Forbidden-path entries must be daemon-routed-writer-service (the
	// writer service's own internal Open — terminal state for slice 6) or
	// canonical-first-hook-fallback (the single daemon-miss fallback open in
	// core/hooks/dbgate.go). Any other classification on a forbidden path
	// means someone added a direct writable open in the
	// hook/indexer/receiver/collector tree that bypasses the daemon — this
	// SHOULD be routed through RouteHookWrite / RouteInsertEvent instead.
	// Note: daemon-routed-pending-slice-6 is retired and no longer accepted.
	var misclassified []writeSite
	for _, ws := range approvedWriteSites {
		if !isForbiddenPath(ws.File) {
			continue
		}
		if !isForbiddenPathClassification(ws.Classification) {
			misclassified = append(misclassified, ws)
		}
	}

	// 4. Forbidden-path call sites discovered by the scan must also live
	// in the inventory under one of the daemon-routed classifications —
	// catches the case where someone removes the inventory entry but
	// leaves the direct open in place (this is also caught by check #1
	// above; this check is explicit so the failure message is precise).
	var unannotatedForbidden []foundSite
	for _, fs := range found {
		if !isForbiddenPath(fs.File) {
			continue
		}
		ws, ok := approvedByKey[mkKey(fs.File, fs.OpenExpr, fs.Line)]
		if !ok {
			// Will already be reported under newSites.
			continue
		}
		if !isForbiddenPathClassification(ws.Classification) {
			unannotatedForbidden = append(unannotatedForbidden, fs)
		}
	}

	if len(newSites) > 0 || len(staleEntries) > 0 || len(misclassified) > 0 || len(unannotatedForbidden) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "writable SQLite open boundary failed.\n\n")

		if len(newSites) > 0 {
			sort.Slice(newSites, func(i, j int) bool {
				if newSites[i].File != newSites[j].File {
					return newSites[i].File < newSites[j].File
				}
				return newSites[i].Line < newSites[j].Line
			})
			fmt.Fprintf(&b, "NEW direct writable opens not in inventory (%d):\n", len(newSites))
			fmt.Fprintf(&b, "  These must be classified by adding entries to approvedWriteSites.\n")
			fmt.Fprintf(&b, "  Hook / indexer / receiver / event-capture paths SHOULD instead route through the slice-6 writer service.\n")
			for _, fs := range newSites {
				fmt.Fprintf(&b, "  + %s:%d  func=%s  open=%s\n", fs.File, fs.Line, fs.Function, fs.OpenExpr)
			}
			b.WriteString("\n")
		}
		if len(staleEntries) > 0 {
			sort.Slice(staleEntries, func(i, j int) bool {
				if staleEntries[i].File != staleEntries[j].File {
					return staleEntries[i].File < staleEntries[j].File
				}
				return staleEntries[i].Line < staleEntries[j].Line
			})
			fmt.Fprintf(&b, "STALE inventory entries (no matching call site found, %d):\n", len(staleEntries))
			fmt.Fprintf(&b, "  Either the line moved (update Line field) or the call was removed (delete entry).\n")
			for _, ws := range staleEntries {
				fmt.Fprintf(&b, "  - %s:%d  func=%s  open=%s  class=%s\n", ws.File, ws.Line, ws.Function, ws.OpenExpr, ws.Classification)
			}
			b.WriteString("\n")
		}
		if len(misclassified) > 0 {
			fmt.Fprintf(&b, "MISCLASSIFIED forbidden-path entries (%d):\n", len(misclassified))
			fmt.Fprintf(&b, "  Hook / indexer / receiver / event-capture paths must use %q (writer service internal open) or %q (daemon-miss fallback open in core/hooks/dbgate.go).\n",
				daemonRoutedWriterService, canonicalFirstHookFallback)
			fmt.Fprintf(&b, "  Route new hook writes through RouteHookWrite / RouteInsertEvent instead of adding a direct open.\n")
			fmt.Fprintf(&b, "  The retired %q classification is no longer accepted.\n", daemonRoutedPendingSlice6)
			for _, ws := range misclassified {
				fmt.Fprintf(&b, "  ! %s:%d  func=%s  class=%s\n",
					ws.File, ws.Line, ws.Function, ws.Classification)
			}
			b.WriteString("\n")
		}
		if len(unannotatedForbidden) > 0 {
			fmt.Fprintf(&b, "UN-ANNOTATED forbidden-path sites (%d):\n", len(unannotatedForbidden))
			for _, fs := range unannotatedForbidden {
				fmt.Fprintf(&b, "  ? %s:%d  func=%s  open=%s\n", fs.File, fs.Line, fs.Function, fs.OpenExpr)
			}
			b.WriteString("\n")
		}

		t.Fatalf("%s", b.String())
	}
}

// TestWriteSiteInventoryComplete is a redundant safety net: it re-asserts
// that every discovered writable open lives in the inventory AND every
// inventory entry references a real file. TestWritableDBOpenBoundary
// already covers this, but having a separate, narrower test makes the
// failure mode immediately readable in CI output.
func TestWriteSiteInventoryComplete(t *testing.T) {
	root := findModuleRoot(t)

	// Verify every inventory file exists.
	for _, ws := range approvedWriteSites {
		full := filepath.Join(root, filepath.FromSlash(ws.File))
		if _, err := os.Stat(full); err != nil {
			t.Errorf("inventory file %s does not exist: %v", ws.File, err)
		}
	}

	// Verify every classification is a known constant.
	known := map[writeSiteClassification]bool{
		daemonRoutedPendingSlice6:  true,
		daemonRoutedWriterService:  true,
		canonicalFirstHookFallback: true,
		intentionalCLIMutation:     true,
		reindexOnly:                true,
		migrationOnly:              true,
	}
	for _, ws := range approvedWriteSites {
		if !known[ws.Classification] {
			t.Errorf("inventory %s:%d uses unknown classification %q", ws.File, ws.Line, ws.Classification)
		}
	}

	// Verify the plugin/ directory contains no Go source — slice 5 documents
	// that plugin/ is markdown / static assets, so the boundary scan does
	// not cover it. If a Go file ever lands there, this test catches it
	// before the boundary scan silently misses a write site.
	pluginDir := filepath.Join(root, "plugin")
	if _, err := os.Stat(pluginDir); err == nil {
		err := filepath.Walk(pluginDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("plugin/ now contains a Go file (%s) — extend scannedDirs to include plugin/", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk plugin/: %v", err)
		}
	}
}

// findModuleRoot resolves the wipnote module root by walking up from the
// test's CWD until it finds go.mod. The cmd/wipnote test package always
// runs from cmd/wipnote/, so we step up two levels to get the root.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up at most 6 levels searching for go.mod.
	dir := cwd
	for range 6 {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("cannot find module root from %s", cwd)
	return ""
}

// scanWritableOpens parses every .go file (excluding _test.go) under
// scannedDirs and returns every writable SQLite open call discovered.
//
// A "writable open" is any of:
//
//   - <db-alias>.Open(...)         — internal/db.Open (writable, runs migrations)
//   - <db-alias>.OpenWritable(...) — internal/db.OpenWritable (writable, no migrations)
//   - sql.Open("sqlite", ...)      — direct driver open; checked for ?mode=ro
//     in the DSN — if mode=ro is present, the call is READ-ONLY and skipped.
//
// The db-alias resolution honours the import statement at the top of
// each file (e.g. `import dbpkg "github.com/shakestzd/wipnote/core/db"`
// makes `dbpkg.Open(...)` a write call).
func scanWritableOpens(root string) ([]foundSite, error) {
	var sites []foundSite
	for _, dir := range scannedDirs {
		walkRoot := filepath.Join(root, dir)
		err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if isExcludedPath(root, path) {
				return nil
			}
			fileSites, err := scanFile(root, path)
			if err != nil {
				return fmt.Errorf("scan %s: %w", path, err)
			}
			sites = append(sites, fileSites...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sites, nil
}

// scanFile parses one Go file and returns every writable SQLite open it
// contains. relPath is the path relative to the module root, used to
// label results.
func scanFile(root, path string) ([]foundSite, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// Map import-name → import-path so we can identify which package
	// aliases resolve to internal/db.
	dbAliases := make(map[string]bool) // alias name → is db package
	for _, imp := range f.Imports {
		// imp.Path.Value is the quoted import path, e.g. "\"...internal/db\"".
		pathStr := strings.Trim(imp.Path.Value, "\"")
		if pathStr != "github.com/shakestzd/wipnote/core/db" {
			continue
		}
		alias := "db" // default package name
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" {
			alias = imp.Name.Name
		}
		dbAliases[alias] = true
	}
	// Always-watched aliases. The literal `sql.Open` (database/sql) is
	// caught separately because the DSN must be inspected for mode=ro.
	hasSQLImport := false
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "\"") == "database/sql" {
			hasSQLImport = true
			break
		}
	}

	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	relPath = filepath.ToSlash(relPath)

	var sites []foundSite

	// Stack of enclosing function names, so nested closures resolve to
	// their containing func.
	var funcStack []string
	currentFunc := func() string {
		if len(funcStack) == 0 {
			return ""
		}
		return funcStack[len(funcStack)-1]
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Body == nil {
				return false
			}
			funcStack = append(funcStack, node.Name.Name)
			// Pre-scan: does this function contain a literal that flags
			// the open as read-only?  If yes, sql.Open calls in this
			// function body are treated as RO and skipped.
			funcIsReadOnly := funcBodyDeclaresReadOnlyDSN(node.Body)
			ast.Inspect(node.Body, func(inner ast.Node) bool {
				return inspectCall(inner, currentFunc(), funcIsReadOnly, fset, relPath, dbAliases, hasSQLImport, &sites)
			})
			funcStack = funcStack[:len(funcStack)-1]
			return false
		}
		return true
	})

	return sites, nil
}

// funcBodyDeclaresReadOnlyDSN returns true when the function body contains
// any string literal whose value includes "mode=ro". This is the heuristic
// for "this function opens read-only" — it catches DSNs assembled by
// fmt.Sprintf, string concatenation, or any literal-bearing expression.
func funcBodyDeclaresReadOnlyDSN(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, "mode=ro") {
			found = true
			return false
		}
		return true
	})
	return found
}

// inspectCall examines one AST node; if it is a writable DB open call,
// it appends a foundSite to sites. funcIsReadOnly is the result of a
// per-function pre-scan: if true, sql.Open calls in this function are
// suppressed because the function's DSN literals indicate read-only.
func inspectCall(n ast.Node, fnName string, funcIsReadOnly bool, fset *token.FileSet, relPath string, dbAliases map[string]bool, hasSQLImport bool, sites *[]foundSite) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return true
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return true
	}
	pkgName := pkgIdent.Name
	method := sel.Sel.Name

	// internal/db writable opens.
	if dbAliases[pkgName] && (method == "Open" || method == "OpenWritable") {
		pos := fset.Position(call.Pos())
		*sites = append(*sites, foundSite{
			File:     relPath,
			Line:     pos.Line,
			Function: fnName,
			OpenExpr: pkgName + "." + method,
		})
		return true
	}

	// database/sql.Open — only count writable opens. Read-only DSNs
	// (mode=ro) are excluded.
	if hasSQLImport && pkgName == "sql" && method == "Open" {
		if funcIsReadOnly || isReadOnlySQLOpenArg(call) {
			return true
		}
		pos := fset.Position(call.Pos())
		*sites = append(*sites, foundSite{
			File:     relPath,
			Line:     pos.Line,
			Function: fnName,
			OpenExpr: "sql.Open",
		})
	}
	return true
}

// isReadOnlySQLOpenArg returns true when the DSN argument of a sql.Open
// call is a literal/concat expression containing "mode=ro". The function-
// scope scan (funcBodyDeclaresReadOnlyDSN) catches DSNs assembled via
// fmt.Sprintf; this fallback handles the simple inline-literal case.
func isReadOnlySQLOpenArg(call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return false
	}
	return containsModeRO(call.Args[1])
}

// containsModeRO walks a (possibly-concatenated) expression and returns
// true if any string-literal node contains "mode=ro".
func containsModeRO(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strings.Contains(v.Value, "mode=ro")
		}
	case *ast.BinaryExpr:
		return containsModeRO(v.X) || containsModeRO(v.Y)
	case *ast.ParenExpr:
		return containsModeRO(v.X)
	}
	return false
}

// isForbiddenPath returns true if relPath sits under a directory where
// writable DB opens must be daemon-routed.
func isForbiddenPath(relPath string) bool {
	for _, prefix := range forbiddenPathPrefixes {
		if strings.HasPrefix(relPath, prefix) {
			return true
		}
	}
	return false
}

// isForbiddenPathClassification reports whether a classification is
// permitted on a forbidden-path entry. Exactly two labels are accepted:
//
//   - daemon-routed-writer-service:  slice-6 writer service's own internal
//     writable open — the single handle per project while serve runs.
//   - canonical-first-hook-fallback: the daemon-miss fallback open in
//     core/hooks/dbgate.go:OpenHookDB — reached only when the daemon is
//     unavailable; failure is logged + counted and recovered via reindex.
//
// daemon-routed-pending-slice-6 is RETIRED (no entries use it; excluded
// here as the architectural ratchet: any new forbidden-path entry using
// that classification will cause this check to fail, forcing the author to
// route through RouteHookWrite / RouteInsertEvent instead).
func isForbiddenPathClassification(c writeSiteClassification) bool {
	return c == daemonRoutedWriterService ||
		c == canonicalFirstHookFallback
}

// isExcludedPath returns true when path lives under one of excludedDirs,
// meaning its writable opens are the canonical primitives themselves and
// not call sites the boundary should police.
func isExcludedPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	relSlash := filepath.ToSlash(rel)
	for _, dir := range excludedDirs {
		if strings.HasPrefix(relSlash, dir+"/") || relSlash == dir {
			return true
		}
	}
	return false
}
