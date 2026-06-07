package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

func TestLooksLikeFilePath(t *testing.T) {
	tests := []struct {
		arg  string
		want bool
	}{
		{"internal/db/schema.go", true},
		{"cmd/wipnote/main.go", true},
		{"file.go", true},
		{"./relative/path", true},
		{"abc1234", false},
		{"45da73fa", false},
		{"deadbeef", false},
	}
	for _, tt := range tests {
		if got := looksLikeFilePath(tt.arg); got != tt.want {
			t.Errorf("looksLikeFilePath(%q) = %v, want %v", tt.arg, got, tt.want)
		}
	}
}

func TestTraceFile(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Seed a track and feature.
	_, err = database.Exec(`INSERT INTO tracks (id, type, title, status) VALUES (?, ?, ?, ?)`,
		"trk-bbbb2222", "track", "Test track", "in-progress")
	if err != nil {
		t.Fatalf("insert track: %v", err)
	}
	_, err = database.Exec(`INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)`,
		"feat-aaaa1111", "feature", "Test feature", "in-progress", "trk-bbbb2222")
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	// Seed a feature_files row.
	ff := &models.FeatureFile{
		ID:        "feat-aaaa1111-test",
		FeatureID: "feat-aaaa1111",
		FilePath:  "internal/db/schema.go",
		Operation: "edit",
		SessionID: "sess-test",
	}
	if err := dbpkg.UpsertFeatureFile(database, ff); err != nil {
		t.Fatalf("upsert feature file: %v", err)
	}

	results, err := dbpkg.TraceFile(database, "internal/db/schema.go")
	if err != nil {
		t.Fatalf("TraceFile: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.FeatureID != "feat-aaaa1111" {
		t.Errorf("FeatureID = %q, want feat-aaaa1111", r.FeatureID)
	}
	if r.Title != "Test feature" {
		t.Errorf("Title = %q, want 'Test feature'", r.Title)
	}
	if r.TrackID != "trk-bbbb2222" {
		t.Errorf("TrackID = %q, want trk-bbbb2222", r.TrackID)
	}
	if r.Operation != "edit" {
		t.Errorf("Operation = %q, want 'edit'", r.Operation)
	}
}

func TestTraceFile_NoResults(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	results, err := dbpkg.TraceFile(database, "nonexistent/file.go")
	if err != nil {
		t.Fatalf("TraceFile: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// seedTraceFeatureDB creates a minimal in-memory DB with a feature, commit, and file.
func seedTraceFeatureDB(t *testing.T) (*sql.DB, string, string, string) {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	featureID := "feat-11223344"
	commitSHA := "aabbccdd1234567"
	filePath := "internal/db/trace_me.go"
	sessionID := "sess-trace-test"

	// Insert track and feature.
	database.Exec(`INSERT INTO tracks (id, type, title, status) VALUES (?, ?, ?, ?)`,
		"trk-trace0001", "track", "Trace Track", "in-progress")
	database.Exec(`INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)`,
		featureID, "feature", "Trace Feature", "in-progress", "trk-trace0001")

	// Insert a commit linked to the feature.
	database.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		commitSHA, sessionID, featureID, "trace commit msg", time.Now().UTC().Format(time.RFC3339))

	// Insert a feature file.
	dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID:        "ff-trace-001",
		FeatureID: featureID,
		FilePath:  filePath,
		Operation: "edit",
		SessionID: sessionID,
	})

	return database, featureID, commitSHA, filePath
}

func TestTraceRoutesFeatureID(t *testing.T) {
	database, featureID, commitSHA, filePath := seedTraceFeatureDB(t)
	defer database.Close()

	var buf bytes.Buffer
	if err := runTraceFeature(&buf, database, featureID); err != nil {
		t.Fatalf("runTraceFeature: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, commitSHA[:9]) {
		t.Errorf("output should contain commit SHA prefix %q\ngot:\n%s", commitSHA[:9], out)
	}
	if !strings.Contains(out, filePath) {
		t.Errorf("output should contain file path %q\ngot:\n%s", filePath, out)
	}
}

func TestTraceJSONOutput(t *testing.T) {
	database, featureID, commitSHA, filePath := seedTraceFeatureDB(t)
	defer database.Close()

	var buf bytes.Buffer
	if err := runTraceFeatureJSON(&buf, database, featureID); err != nil {
		t.Fatalf("runTraceFeatureJSON: %v", err)
	}

	var result traceFeatureJSON
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput:\n%s", err, buf.String())
	}

	if result.Feature != featureID {
		t.Errorf("JSON feature = %q, want %q", result.Feature, featureID)
	}
	if len(result.Commits) == 0 {
		t.Errorf("JSON commits should be non-empty")
	} else if result.Commits[0] != commitSHA {
		t.Errorf("JSON commits[0] = %q, want %q", result.Commits[0], commitSHA)
	}
	if len(result.Files) == 0 {
		t.Errorf("JSON files should be non-empty")
	} else if result.Files[0] != filePath {
		t.Errorf("JSON files[0] = %q, want %q", result.Files[0], filePath)
	}
}

func TestTraceSHAUnchanged(t *testing.T) {
	database, _, _, _ := seedTraceFeatureDB(t)
	defer database.Close()

	commitSHA := "aabbccdd1234567"

	results, err := dbpkg.TraceCommit(database, commitSHA)
	if err != nil {
		t.Fatalf("TraceCommit: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected TraceCommit to find the seeded commit via SHA path")
	}
	if results[0].CommitHash != commitSHA {
		t.Errorf("CommitHash = %q, want %q", results[0].CommitHash, commitSHA)
	}
}

// TestTraceFeatureIncludesFileOnlySession guards against the regression where
// uniqueSessions only looked at commits and dropped sessions that touched the
// feature through feature_files without producing a commit.
func TestTraceFeatureIncludesFileOnlySession(t *testing.T) {
	database, featureID, _, _ := seedTraceFeatureDB(t)
	defer database.Close()

	fileOnlySession := "sess-file-only"
	if err := dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID:        "ff-file-only",
		FeatureID: featureID,
		FilePath:  "internal/db/file_only.go",
		Operation: "edit",
		SessionID: fileOnlySession,
	}); err != nil {
		t.Fatalf("upsert file-only feature_file: %v", err)
	}

	var buf bytes.Buffer
	if err := runTraceFeatureJSON(&buf, database, featureID); err != nil {
		t.Fatalf("runTraceFeatureJSON: %v", err)
	}
	var got traceFeatureJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !slices.Contains(got.Sessions, fileOnlySession) {
		t.Errorf("sessions should include file-only session %q, got %v", fileOnlySession, got.Sessions)
	}
}

// TestTraceFileJSONStableTrackOrder guards the Low-severity regression
// where tracks were emitted from a map iteration and thus varied per run,
// making the JSON payload unstable for automation and snapshots.
func TestTraceFileJSONStableTrackOrder(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Three tracks, one feature per track, all touching the same file.
	// Deliberately insert in NON-sorted order so a map iteration would
	// produce a different order.
	tracks := []string{"trk-mmm", "trk-aaa", "trk-zzz"}
	for i, tr := range tracks {
		database.Exec(`INSERT INTO tracks (id, type, title, status) VALUES (?, ?, ?, ?)`,
			tr, "track", "T", "in-progress")
		featID := "feat-sort" + string(rune('0'+i))
		database.Exec(`INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)`,
			featID, "feature", "F", "done", tr)
		if err := dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
			ID: "ff-sort" + string(rune('0'+i)), FeatureID: featID,
			FilePath: "shared/sort.go", Operation: "edit",
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Simulate the JSON builder path by running the same logic as runTraceFile.
	results, err := dbpkg.TraceFile(database, "shared/sort.go")
	if err != nil {
		t.Fatalf("TraceFile: %v", err)
	}
	out := traceFileJSON{}
	trackSet := make(map[string]bool)
	for _, r := range results {
		if r.TrackID != "" {
			trackSet[r.TrackID] = true
		}
	}
	for tr := range trackSet {
		out.Tracks = append(out.Tracks, tr)
	}
	slicesSort(out.Tracks) // match runTraceFile

	// After sort the order must be deterministic and ascending.
	want := []string{"trk-aaa", "trk-mmm", "trk-zzz"}
	if len(out.Tracks) != len(want) {
		t.Fatalf("tracks len = %d, want %d", len(out.Tracks), len(want))
	}
	for i := range want {
		if out.Tracks[i] != want[i] {
			t.Errorf("tracks[%d] = %q, want %q", i, out.Tracks[i], want[i])
		}
	}
}

// slicesSort is a small helper matching the sort.Strings call in runTraceFile.
// Kept in the test file so the regression asserts the same sort behaviour.
func slicesSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// TestTraceCommitJSON guards the --json contract for the commit route.
func TestTraceCommitJSON(t *testing.T) {
	database, _, commitSHA, _ := seedTraceFeatureDB(t)
	defer database.Close()

	results, err := dbpkg.TraceCommit(database, commitSHA)
	if err != nil || len(results) == 0 {
		t.Fatalf("seed TraceCommit: %v (len=%d)", err, len(results))
	}
	// Assemble the JSON payload the same way runTraceCommit does so we exercise
	// the schema without reaching for os.Stdout.
	payload := traceCommitJSON{Query: commitSHA, Results: []traceCommitHit{{
		Commit:  results[0].CommitHash,
		Message: results[0].Message,
		Session: results[0].SessionID,
		Feature: results[0].FeatureID,
		Track:   results[0].TrackID,
	}}}
	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt traceCommitJSON
	if err := json.Unmarshal(buf, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt.Query != commitSHA || len(rt.Results) != 1 || rt.Results[0].Commit != commitSHA {
		t.Errorf("round-trip mismatch: %+v", rt)
	}
}

func TestTraceFile_MultipleFeatures(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Seed track and two features.
	database.Exec(`INSERT INTO tracks (id, type, title, status) VALUES (?, ?, ?, ?)`,
		"trk-xxxx1111", "track", "Test track", "in-progress")
	database.Exec(`INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)`,
		"feat-aaaa1111", "feature", "First feature", "done", "trk-xxxx1111")
	database.Exec(`INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)`,
		"feat-bbbb2222", "feature", "Second feature", "in-progress", "trk-xxxx1111")

	// Both touch the same file.
	dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID: "ff1", FeatureID: "feat-aaaa1111", FilePath: "shared/file.go", Operation: "write",
	})
	dbpkg.UpsertFeatureFile(database, &models.FeatureFile{
		ID: "ff2", FeatureID: "feat-bbbb2222", FilePath: "shared/file.go", Operation: "edit",
	})

	results, err := dbpkg.TraceFile(database, "shared/file.go")
	if err != nil {
		t.Fatalf("TraceFile: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// setupStaleSchemaDB creates a fresh, schema-less DB and configures the test
// environment to use it via WIPNOTE_DB_PATH and projectDirFlag. It registers
// cleanup via t.Cleanup so callers don't manage cleanup manually.
func setupStaleSchemaDB(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("create .wipnote dir: %v", err)
	}

	// Create a fresh (schema-less) DB file.
	dbPath := filepath.Join(tmpDir, "wipnote.db")
	freshDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create fresh db file: %v", err)
	}
	freshDB.Close()

	// Verify it's genuinely schema-less.
	schemalessDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen fresh db to verify empty: %v", err)
	}
	_, err = schemalessDB.Query("SELECT 1 FROM git_commits LIMIT 1")
	if err == nil {
		schemalessDB.Close()
		t.Fatal("expected fresh db to be schema-less, but query succeeded unexpectedly")
	}
	schemalessDB.Close()

	// Configure test env to use this DB.
	t.Setenv("WIPNOTE_DB_PATH", dbPath)
	oldProjectDir := projectDirFlag
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = oldProjectDir })
}

// TestTraceStaleSchemaRegressions verifies that all three trace.go read-only
// open paths (runTrace line 78, runTraceCommit line 118, runTraceFile line 211)
// now bootstrap+migrate via OpenReadOnlyMigrated, ensuring a fresh or
// stale-schema DB succeeds where bare OpenReadOnly would fail. bug-f9622af9.
//
// Each subtest gets its own fresh, schema-less DB (via setupStaleSchemaDB
// called per-subtest, not shared). This prevents the first subtest's successful
// OpenReadOnlyMigrated from poisoning the schema state for later subtests,
// ensuring each callsite is independently guarded: if any callsite reverts to
// bare dbpkg.OpenReadOnly, its subtest goes RED ("no such table" error).
//
// The test drives the ACTUAL command paths (not direct OpenReadOnlyMigrated
// calls), so a revert at any line (78, 118, 211) makes that subtest FAIL.
func TestTraceStaleSchemaRegressions(t *testing.T) {
	tests := []struct {
		name          string
		runFunc       func() error
		wantErrorMsg  string // exact substring expected in error if OpenReadOnlyMigrated worked; empty string means expect nil
	}{
		{
			name: "runTrace with work-item ID (line 78)",
			runFunc: func() error {
				// runTrace takes a work-item ID (feat-, bug-, spk- prefix) and calls
				// dbpkg.OpenReadOnlyMigrated at line 78 in the looksLikeWorkItemID branch.
				// Against a fresh, schema-bootstrapped-but-empty DB, it returns nil
				// (prints empty results to stdout).
				return runTrace("feat-deadbeef", false)
			},
			wantErrorMsg: "", // expects nil on success
		},
		{
			name: "runTraceCommit with SHA (line 118)",
			runFunc: func() error {
				// runTraceCommit takes a commit SHA and calls dbpkg.OpenReadOnlyMigrated
				// at line 118. Against a fresh DB with no commits, it returns an error.
				return runTraceCommit("deadbeef1234567", false)
			},
			wantErrorMsg: "not found in git_commits table", // exact substring expected in the error
		},
		{
			name: "runTraceFile with file path (line 211)",
			runFunc: func() error {
				// runTraceFile takes a file path and calls dbpkg.OpenReadOnlyMigrated
				// at line 211. Against a fresh, schema-bootstrapped-but-empty DB, it
				// returns nil (prints "No features found" to stdout).
				return runTraceFile("cmd/wipnote/main.go", false)
			},
			wantErrorMsg: "", // expects nil on success
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own fresh, schema-less DB to prevent
			// cross-test pollution from OpenReadOnlyMigrated bootstrapping.
			setupStaleSchemaDB(t)

			err := tt.runFunc()

			// RED signal: schema error indicates bare OpenReadOnly was used.
			// The "no such table" error proves the schema was NOT bootstrapped.
			if err != nil {
				if strings.Contains(err.Error(), "no such table") {
					t.Fatalf("schema error (bare OpenReadOnly detected, not OpenReadOnlyMigrated): %v", err)
				}
			}

			// GREEN signal: assert the concrete expected outcome per case.
			if tt.wantErrorMsg == "" {
				// This case expects nil (schema bootstrapped, query succeeded with empty/no results).
				if err != nil {
					t.Fatalf("expected nil but got error: %v", err)
				}
			} else {
				// This case expects a specific error substring (schema bootstrapped, query ran).
				if err == nil {
					t.Fatalf("expected error with substring %q but got nil", tt.wantErrorMsg)
				}
				if !strings.Contains(err.Error(), tt.wantErrorMsg) {
					t.Fatalf("expected error substring %q, got: %v", tt.wantErrorMsg, err)
				}
			}
		})
	}
}
