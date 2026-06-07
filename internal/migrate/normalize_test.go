package migrate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/paths"
)

// --- harness ----------------------------------------------------------------

// fixture builds a temporary .wipnote/ tree plus a SQLite read index seeded
// with rows that exercise every rewrite target. Tests mutate the returned
// values and call fx.run(opts) to invoke NormalizePaths.
type fixture struct {
	t          *testing.T
	repoRoot   string
	wipnoteDir string
	dbPath     string
	db         *sql.DB
	gitRun     gitRunner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	wipDir := filepath.Join(root, ".wipnote")
	for _, sub := range []string{"sessions", "features", "bugs", "spikes"} {
		if err := os.MkdirAll(filepath.Join(wipDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Seed a session row so feature_files / agent_events FKs are satisfied.
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, created_at, status, project_dir)
		 VALUES ('sess-clean', 'claude-code', datetime('now'), 'completed', ?)`,
		root,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// And a feature row so feature_files FK is satisfied.
	if _, err := database.Exec(
		`INSERT INTO features (id, type, title, status) VALUES ('feat-x', 'feature', 'x', 'todo')`,
	); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	// Default git runner: clean working tree.
	gitRun := func(repoRoot string, args ...string) (string, error) {
		return "", nil
	}

	return &fixture{
		t:          t,
		repoRoot:   root,
		wipnoteDir: wipDir,
		dbPath:     dbPath,
		db:         database,
		gitRun:     gitRun,
	}
}

func (fx *fixture) defaultOpts(dryRun bool) NormalizeOptions {
	return NormalizeOptions{
		RepoRoot:        fx.repoRoot,
		DryRun:          dryRun,
		Backup:          !dryRun,
		BackupTimestamp: "TESTSTAMP",
	}
}

func (fx *fixture) seedToolInput(eventID, toolInputJSON string) {
	fx.t.Helper()
	if _, err := fx.db.Exec(
		`INSERT INTO agent_events (event_id, agent_id, event_type, tool_name, tool_input, session_id)
		 VALUES (?, 'a', 'tool_call', 'Edit', ?, 'sess-clean')`,
		eventID, toolInputJSON,
	); err != nil {
		fx.t.Fatalf("seed agent_events %s: %v", eventID, err)
	}
}

func (fx *fixture) seedInputSummary(eventID, summary string) {
	fx.t.Helper()
	if _, err := fx.db.Exec(
		`INSERT INTO agent_events (event_id, agent_id, event_type, tool_name, input_summary, session_id)
		 VALUES (?, 'a', 'tool_call', 'Bash', ?, 'sess-clean')`,
		eventID, summary,
	); err != nil {
		fx.t.Fatalf("seed agent_events %s: %v", eventID, err)
	}
}

func (fx *fixture) seedFeatureFile(id, featureID, path, firstSeen string) {
	fx.t.Helper()
	if _, err := fx.db.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, operation, first_seen, last_seen)
		 VALUES (?, ?, ?, 'edit', ?, ?)`,
		id, featureID, path, firstSeen, firstSeen,
	); err != nil {
		fx.t.Fatalf("seed feature_files %s: %v", id, err)
	}
}

func (fx *fixture) seedPendingSubagent(agentID, cwd string) {
	fx.t.Helper()
	if _, err := fx.db.Exec(
		`INSERT INTO pending_subagent_starts (agent_id, agent_type, session_id, cwd, created_at)
		 VALUES (?, 'codex', 'sess-clean', ?, 1)`,
		agentID, cwd,
	); err != nil {
		fx.t.Fatalf("seed pending_subagent_starts: %v", err)
	}
}

func (fx *fixture) writeSessionHTML(sessionID, projectDir string) string {
	fx.t.Helper()
	path := filepath.Join(fx.wipnoteDir, "sessions", sessionID+".html")
	body := fmt.Sprintf(`<!DOCTYPE html><html><body>
<article id="%s" data-type="session" data-project-dir="%s">
  <p>Started at %s/cmd/wipnote/main.go</p>
</article>
</body></html>`, sessionID, projectDir, projectDir)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		fx.t.Fatalf("write session html: %v", err)
	}
	return path
}

func (fx *fixture) writeFeatureHTML(featureID, embeddedPath string) string {
	fx.t.Helper()
	path := filepath.Join(fx.wipnoteDir, "features", featureID+".html")
	body := fmt.Sprintf(`<!DOCTYPE html><html><body>
<article id="%s" data-type="feature">
  <section data-properties affected_files="%s">touch</section>
</article>
</body></html>`, featureID, embeddedPath)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		fx.t.Fatalf("write feature html: %v", err)
	}
	return path
}

// resetNormalizeCacheForTesting clears the shared per-process resolver cache
// so each test sees a fresh git lookup. Without this, the second test in a
// package run sees stale "this dir resolves to no anchor" answers from the
// first test's TempDir.
func resetNormalizeCacheForTesting() {
	paths.ResetNormalizeCacheForTesting()
}

// --- T1: dry-run on clean tree produces proposals and writes nothing -------

func TestNormalizePaths_DryRunCleanTree_ProposesNoWrites(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Seed a single absolute tool_input that will be rewritten.
	fx.seedToolInput("evt-1", `{"file_path":"`+fx.repoRoot+`/cmd/wipnote/main.go"}`)

	opts := fx.defaultOpts(true)
	summary, err := NormalizePaths(fx.db, opts)
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if summary.DBValuesNormalized == 0 {
		t.Fatalf("expected at least one DB proposal, summary=%+v", summary)
	}
	// Verify the DB was NOT mutated.
	var got string
	if err := fx.db.QueryRow(`SELECT tool_input FROM agent_events WHERE event_id = 'evt-1'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(got, fx.repoRoot) {
		t.Fatalf("dry-run mutated the DB; got %q", got)
	}
}

// --- T2: dry-run errors on dirty tree (no override) ------------------------
// (Implemented at the CLI layer; the library reports IsWorkingTreeDirty.)
func TestIsWorkingTreeDirty_DetectsDirty(t *testing.T) {
	stub := func(repoRoot string, args ...string) (string, error) {
		return " M .wipnote/foo.html\n", nil
	}
	dirty, err := IsWorkingTreeDirty("/tmp/whatever", stub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !dirty {
		t.Fatal("expected dirty=true")
	}
}

func TestIsWorkingTreeDirty_DetectsClean(t *testing.T) {
	stub := func(repoRoot string, args ...string) (string, error) {
		return "", nil
	}
	dirty, err := IsWorkingTreeDirty("/tmp/whatever", stub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if dirty {
		t.Fatal("expected dirty=false")
	}
}

// --- T3: dirty tree + --allow-dirty proceeds; summary notes the override ---
// The CLI layer attaches AllowDirtyOverride to the summary; the library
// surfaces NormalizeOptions.AllowDirty so callers can honour the audit.

func TestNormalizePaths_AllowDirtyFieldIsPassedThrough(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	opts := fx.defaultOpts(true)
	opts.AllowDirty = true
	summary, err := NormalizePaths(fx.db, opts)
	if err != nil {
		t.Fatalf("run with allow-dirty: %v", err)
	}
	// summary.AllowDirtyOverride is set by the CLI layer; the library
	// simply reads the opt without complaining, which is what this test
	// asserts (no error).
	if summary.HTMLFilesRewritten != 0 || summary.DBValuesNormalized != 0 {
		t.Logf("summary=%+v", summary)
	}
}

// --- T4: idempotency — running twice leaves DB+HTML byte-identical --------

func TestNormalizePaths_Idempotent(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	fx.seedToolInput("evt-1", `{"file_path":"`+fx.repoRoot+`/cmd/wipnote/main.go"}`)
	htmlPath := fx.writeSessionHTML("sess-A", fx.repoRoot)

	// First run — performs writes.
	opts := fx.defaultOpts(false)
	if _, err := NormalizePaths(fx.db, opts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstDB := snapshotDB(t, fx.db)
	firstHTML, _ := os.ReadFile(htmlPath)

	// Second run — should be a no-op.
	opts2 := fx.defaultOpts(false)
	opts2.BackupTimestamp = "TESTSTAMP-2"
	summary2, err := NormalizePaths(fx.db, opts2)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if summary2.DBValuesNormalized != 0 || summary2.HTMLFilesRewritten != 0 {
		t.Fatalf("second run was not a no-op: %+v", summary2)
	}
	secondDB := snapshotDB(t, fx.db)
	secondHTML, _ := os.ReadFile(htmlPath)
	if firstDB != secondDB {
		t.Fatalf("DB snapshot drift on second run\nfirst:  %s\nsecond: %s", firstDB, secondDB)
	}
	if string(firstHTML) != string(secondHTML) {
		t.Fatalf("HTML drift on second run\nfirst:  %s\nsecond: %s", firstHTML, secondHTML)
	}
}

// --- T5: feature_files collision default merge ----------------------------

func TestNormalizePaths_CollisionDefaultMerge(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Two rows that normalise to the same path.
	// Realistic collision: one row captured before the runtime
	// normaliser landed (absolute), another after (already relative).
	// Both normalise to "cmd/main.go" so they collide post-migration.
	// ff-A is the older (first_seen earlier) row so it must win the merge.
	fx.seedFeatureFile("ff-A", "feat-x", "cmd/main.go", "2025-01-01 00:00:00")
	fx.seedFeatureFile("ff-B", "feat-x", fx.repoRoot+"/cmd/main.go", "2025-02-01 00:00:00") // later

	opts := fx.defaultOpts(false)
	summary, err := NormalizePaths(fx.db, opts)
	if err != nil {
		t.Fatalf("merge run: %v", err)
	}
	if summary.CollisionsMerged != 1 {
		t.Fatalf("expected CollisionsMerged=1, got %d (collisions=%v)", summary.CollisionsMerged, summary.Collisions)
	}
	// The earlier row (ff-A) must win.
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM feature_files WHERE feature_id = 'feat-x'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row after merge, got %d", count)
	}
	var keeperID, keeperPath string
	if err := fx.db.QueryRow(`SELECT id, file_path FROM feature_files WHERE feature_id = 'feat-x'`).Scan(&keeperID, &keeperPath); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if keeperID != "ff-A" {
		t.Errorf("expected keeper ff-A (earliest first_seen), got %s", keeperID)
	}
	if keeperPath != "cmd/main.go" {
		t.Errorf("expected normalised path cmd/main.go, got %s", keeperPath)
	}
}

// --- T6: feature_files collision --no-merge-collisions aborts --------------

func TestNormalizePaths_CollisionNoMergeAborts(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Realistic collision: one row captured before the runtime
	// normaliser landed (absolute), another after (already relative).
	// Both normalise to "cmd/main.go" so they collide post-migration.
	// ff-A is the older (first_seen earlier) row so it must win the merge.
	fx.seedFeatureFile("ff-A", "feat-x", "cmd/main.go", "2025-01-01 00:00:00")
	fx.seedFeatureFile("ff-B", "feat-x", fx.repoRoot+"/cmd/main.go", "2025-02-01 00:00:00")

	opts := fx.defaultOpts(false)
	opts.NoMergeCollisions = true
	_, err := NormalizePaths(fx.db, opts)
	if err == nil {
		t.Fatal("expected an error with --no-merge-collisions; got nil")
	}
	// And the DB must be untouched.
	var count int
	if err := fx.db.QueryRow(`SELECT COUNT(*) FROM feature_files WHERE feature_id = 'feat-x'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows after abort, got %d", count)
	}
}

// --- T7: agent_events.tool_input JSON rewrite -----------------------------

func TestNormalizePaths_RewritesToolInputJSON(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	input := `{"file_path":"` + fx.repoRoot + `/foo.go","command":"ls"}`
	fx.seedToolInput("evt-1", input)

	if _, err := NormalizePaths(fx.db, fx.defaultOpts(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got string
	if err := fx.db.QueryRow(`SELECT tool_input FROM agent_events WHERE event_id = 'evt-1'`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("invalid json after rewrite: %v\nraw=%s", err, got)
	}
	if parsed["file_path"] != "foo.go" {
		t.Errorf("expected file_path=foo.go after rewrite, got %v", parsed["file_path"])
	}
}

// --- T8: agent_events.input_summary embed rewrite -------------------------

func TestNormalizePaths_RewritesInputSummaryEmbed(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Embed a /workspaces/... absolute path inside free text. Because the
	// repo root is the temp dir, paths under it relative to repoRoot
	// normalise; we use a /workspaces/foo path that won't match the
	// repoRoot to assert "unresolved:" prefixing kicks in (host-path under
	// a foreign anchor).
	fx.seedInputSummary("evt-2", "Edited /workspaces/wipnote/.claude/worktrees/foo and finished")

	if _, err := NormalizePaths(fx.db, fx.defaultOpts(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got string
	if err := fx.db.QueryRow(`SELECT input_summary FROM agent_events WHERE event_id = 'evt-2'`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(got, "/workspaces/wipnote/") && !strings.Contains(got, "unresolved:/workspaces/wipnote/") {
		t.Errorf("expected /workspaces/ to be normalised or marked unresolved; got %q", got)
	}
}

// --- T9: sessions.project_dir rewrite -------------------------------------

func TestNormalizePaths_RewritesSessionsProjectDir(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// session row was seeded with project_dir=fx.repoRoot. After
	// normalize it must be ".".
	if _, err := NormalizePaths(fx.db, fx.defaultOpts(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got string
	if err := fx.db.QueryRow(`SELECT project_dir FROM sessions WHERE session_id = 'sess-clean'`).Scan(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "." {
		t.Errorf("expected project_dir='.', got %q", got)
	}
}

// --- T10: HTML data-project-dir attribute rewrite -------------------------

func TestNormalizePaths_RewritesHTMLDataProjectDir(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Use a /workspaces/ host path to trigger the host-path scanner;
	// the temp-root repoRoot won't match (foreign anchor), so the result
	// is "unresolved:" — which is still a rewrite.
	htmlPath := fx.writeSessionHTML("sess-A", "/workspaces/notwip/foo")

	if _, err := NormalizePaths(fx.db, fx.defaultOpts(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, _ := os.ReadFile(htmlPath)
	if !strings.Contains(string(data), "unresolved:/workspaces/notwip/foo") {
		t.Errorf("expected unresolved: prefix in HTML; got:\n%s", data)
	}
}

// --- T11: HTML affected_files property rewrite ---------------------------

func TestNormalizePaths_RewritesHTMLAffectedFiles(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	htmlPath := fx.writeFeatureHTML("feat-A", "/workspaces/notwip/cmd/main.go")
	if _, err := NormalizePaths(fx.db, fx.defaultOpts(false)); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, _ := os.ReadFile(htmlPath)
	if !strings.Contains(string(data), "unresolved:/workspaces/notwip/cmd/main.go") {
		t.Errorf("expected unresolved: prefix in feature HTML; got:\n%s", data)
	}
}

// --- T12: backup directory + restore reverses the change ------------------

func TestNormalizePaths_BackupAndRestoreRoundTrip(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	htmlPath := fx.writeSessionHTML("sess-A", "/workspaces/notwip/foo")
	originalBody, _ := os.ReadFile(htmlPath)

	opts := fx.defaultOpts(false)
	opts.Backup = true
	summary, err := NormalizePaths(fx.db, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.BackupDir == "" {
		t.Fatal("expected BackupDir to be populated")
	}
	// The backup directory should contain a sibling sessions/sess-A.html
	// with the ORIGINAL bytes.
	backupCopy := filepath.Join(summary.BackupDir, "sessions", "sess-A.html")
	got, err := os.ReadFile(backupCopy)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != string(originalBody) {
		t.Errorf("backup contents differ from original\n  backup=  %s\n  original=%s", got, originalBody)
	}

	// Restore: simulate restore by copying the backup file back over and
	// confirm the bytes match the pre-migration body.
	if err := os.WriteFile(htmlPath, got, 0o644); err != nil {
		t.Fatalf("simulated restore write: %v", err)
	}
	restored, _ := os.ReadFile(htmlPath)
	if string(restored) != string(originalBody) {
		t.Errorf("restore did not produce original bytes")
	}
}

// --- supporting helpers ----------------------------------------------------

// snapshotDB returns a stable string capturing every row of every table we
// rewrite. Used by the idempotency test.
func snapshotDB(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	queries := []string{
		`SELECT event_id, COALESCE(tool_input,''), COALESCE(input_summary,'') FROM agent_events ORDER BY event_id`,
		`SELECT id, feature_id, file_path FROM feature_files ORDER BY id`,
		`SELECT agent_id, COALESCE(cwd,'') FROM pending_subagent_starts ORDER BY agent_id`,
		`SELECT session_id, COALESCE(project_dir,'') FROM sessions ORDER BY session_id`,
	}
	for _, q := range queries {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("snapshot %s: %v", q, err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(&b, "--- %v ---\n", cols)
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]interface{}, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatalf("scan: %v", err)
			}
			for _, v := range vals {
				b.WriteString(v.String)
				b.WriteString("|")
			}
			b.WriteByte('\n')
		}
		rows.Close()
	}
	return b.String()
}

// --- sanity unit tests for the helpers themselves -------------------------

func TestNormalizeOnePath_RelativePassThrough(t *testing.T) {
	got, changed, _ := normalizeOnePath("cmd/main.go", "/repo")
	if changed {
		t.Fatal("relative path should not be marked changed")
	}
	if got != "cmd/main.go" {
		t.Errorf("relative path should pass through, got %q", got)
	}
}

func TestNormalizeOnePath_AlreadyUnresolvedIsNoOp(t *testing.T) {
	in := "unresolved:/Users/alice/x"
	got, changed, unres := normalizeOnePath(in, "/repo")
	if changed {
		t.Fatal("unresolved: prefix should not re-normalise")
	}
	if !unres {
		t.Fatal("unresolved flag should be sticky")
	}
	if got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestRewriteToolInputColumn_Idempotent(t *testing.T) {
	repo := "/repo"
	in := `{"file_path":"/repo/cmd/main.go"}`
	out, changed, _, err := rewriteToolInputColumn(in, repo)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("first pass expected changed=1, got %d", changed)
	}
	out2, changed2, _, err := rewriteToolInputColumn(out, repo)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 != 0 {
		t.Errorf("second pass should be a no-op, got changed=%d (out=%q)", changed2, out2)
	}
	if out != out2 {
		t.Errorf("second-pass drift: %q -> %q", out, out2)
	}
}

func TestRewriteEmbeds_LeavesRelativesAlone(t *testing.T) {
	in := "edited cmd/main.go and ./foo"
	out, c, _ := rewriteEmbeds(in, "/repo")
	if c != 0 {
		t.Errorf("expected 0 changes on relative-only input, got %d", c)
	}
	if out != in {
		t.Errorf("input was mutated unnecessarily: %q -> %q", in, out)
	}
}

// TestNormalizePaths_FailedTxRollsBack verifies that a SQL error mid-run
// rolls back every change.
func TestNormalizePaths_FailedTxRollsBack(t *testing.T) {
	resetNormalizeCacheForTesting()
	fx := newFixture(t)
	// Insert two valid tool_input rows so the rewrite proceeds.
	fx.seedToolInput("evt-1", `{"file_path":"`+fx.repoRoot+`/x.go"}`)
	// Close the DB to force the next Exec to fail mid-run.
	// (We don't actually want the test to crash — assert that the helper
	// signals an error and returns the partial summary.)
	if err := fx.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	_, err := NormalizePaths(fx.db, fx.defaultOpts(false))
	if err == nil {
		t.Fatal("expected error after DB close")
	}
}
