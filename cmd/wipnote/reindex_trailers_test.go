package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func TestParseTrailers_RefsFeature(t *testing.T) {
	msg := "fix: resolve null pointer\n\nRefs: feat-abc123"
	ids := parseTrailers(msg)
	if len(ids) != 1 || ids[0] != "feat-abc123" {
		t.Errorf("expected [feat-abc123], got %v", ids)
	}
}

func TestParseTrailers_FixesBug(t *testing.T) {
	msg := "fix: handle edge case\n\nFixes: bug-def456"
	ids := parseTrailers(msg)
	if len(ids) != 1 || ids[0] != "bug-def456" {
		t.Errorf("expected [bug-def456], got %v", ids)
	}
}

func TestParseTrailers_MultipleTrailers(t *testing.T) {
	msg := "feat: big change\n\nRefs: feat-abc\nFixes: bug-xyz"
	ids := parseTrailers(msg)
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %v", ids)
	}
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["feat-abc"] || !found["bug-xyz"] {
		t.Errorf("expected feat-abc and bug-xyz, got %v", ids)
	}
}

func TestParseTrailers_CommaSeparated(t *testing.T) {
	msg := "Refs: feat-a, feat-b, feat-c"
	ids := parseTrailers(msg)
	if len(ids) != 3 {
		t.Errorf("expected 3 IDs, got %v", ids)
	}
}

func TestParseTrailers_NoTrailers(t *testing.T) {
	msg := "fix: simple fix without trailers"
	ids := parseTrailers(msg)
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs, got %v", ids)
	}
}

func TestParseTrailers_InvalidIDSkipped(t *testing.T) {
	msg := "Refs: not-a-valid-id"
	ids := parseTrailers(msg)
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs for invalid prefix, got %v", ids)
	}
}

func TestParseTrailers_Deduplication(t *testing.T) {
	msg := "Refs: feat-abc\nRefs: feat-abc"
	ids := parseTrailers(msg)
	if len(ids) != 1 {
		t.Errorf("expected 1 deduplicated ID, got %v", ids)
	}
}

func TestParseTrailers_PlanAndSpecPrefixes(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []string
	}{
		{"pln prefix", "Refs: pln-abc123", []string{"pln-abc123"}},
		{"spc prefix", "Refs: spc-def456", []string{"spc-def456"}},
		{"plan prefix", "Refs: plan-abc123", []string{"plan-abc123"}},
		{"spec prefix", "Refs: spec-def456", []string{"spec-def456"}},
		{"mixed with plan", "Refs: feat-a\nFixes: pln-b", []string{"feat-a", "pln-b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids := parseTrailers(tc.msg)
			if len(ids) != len(tc.want) {
				t.Fatalf("got %v, want %v", ids, tc.want)
			}
			for i, id := range ids {
				if id != tc.want[i] {
					t.Errorf("ids[%d] = %q, want %q", i, id, tc.want[i])
				}
			}
		})
	}
}

func TestIsWorkItemID_AllPrefixes(t *testing.T) {
	valid := []string{"feat-a", "bug-b", "spk-c", "trk-d", "pln-e", "spc-f", "plan-g", "spec-h"}
	for _, id := range valid {
		if !isWorkItemID(id) {
			t.Errorf("isWorkItemID(%q) = false, want true", id)
		}
	}
	invalid := []string{"xyz-a", "feature-a", "task-b", ""}
	for _, id := range invalid {
		if isWorkItemID(id) {
			t.Errorf("isWorkItemID(%q) = true, want false", id)
		}
	}
}

func TestParseTrailers_ParenthesizedRefs(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want []string
	}{
		{"subject paren", "fix: resolve null pointer (feat-abc12345)", []string{"feat-abc12345"}},
		{"subject paren bug", "fix: edge case (bug-def45678)", []string{"bug-def45678"}},
		{"subject paren spike", "investigate crash (spk-aaa11111)", []string{"spk-aaa11111"}},
		{"paren + trailer", "fix: thing (feat-aaa11111)\n\nRefs: bug-bbb22222", []string{"feat-aaa11111", "bug-bbb22222"}},
		{"paren dedup with trailer", "fix: thing (feat-abc12345)\n\nRefs: feat-abc12345", []string{"feat-abc12345"}},
		{"no paren", "fix: simple fix without parens", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids := parseTrailers(tc.msg)
			if len(ids) != len(tc.want) {
				t.Fatalf("got %v (len %d), want %v (len %d)", ids, len(ids), tc.want, len(tc.want))
			}
			for i, id := range ids {
				if id != tc.want[i] {
					t.Errorf("ids[%d] = %q, want %q", i, id, tc.want[i])
				}
			}
		})
	}
}

func TestReindexCommitTrailers_ParenthesizedCommit(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.go")
	run("commit", "-m", "fix: resolve crash (feat-a1b2c3d4)")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	count, err := reindexCommitTrailers(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexCommitTrailers: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 ingested parenthesized ref, got %d", count)
	}

	var featureID string
	database.QueryRow("SELECT feature_id FROM git_commits WHERE session_id = ?",
		trailerSessionID).Scan(&featureID)
	if featureID != "feat-a1b2c3d4" {
		t.Errorf("expected feature_id=feat-a1b2c3d4, got %q", featureID)
	}
}

// TestReindexCommitTrailers_MultiItemCommitLinksBothIDs is the end-to-end
// regression test for bug-3bf05d49: every trailer-ingest row is written
// under the same constant session_id (trailerSessionID), so a commit naming
// two work items used to collide on the old (commit_hash, session_id)
// primary key and silently keep only the first. With feature_id widened
// into the key (see core/db/migrations.go: stepGitCommitsCompositeKey),
// both IDs must survive.
func TestReindexCommitTrailers_MultiItemCommitLinksBothIDs(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.go")
	run("commit", "-m", "fix: shared cleanup\n\nRefs: feat-aaaa1111, bug-bbbb2222")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	count, err := reindexCommitTrailers(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexCommitTrailers: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 links (one per work item), got %d — the PK collision regressed", count)
	}

	rows, err := database.Query(
		`SELECT feature_id FROM git_commits WHERE session_id = ? ORDER BY feature_id`, trailerSessionID)
	if err != nil {
		t.Fatalf("query linked feature_ids: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var fid string
		if err := rows.Scan(&fid); err != nil {
			t.Fatalf("scan feature_id: %v", err)
		}
		got = append(got, fid)
	}
	want := []string{"bug-bbbb2222", "feat-aaaa1111"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("linked feature_ids = %v, want %v", got, want)
	}
}

func TestReindexCommitTrailers_IngestsFromGit(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo with a commit that has a trailer.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.go")
	run("commit", "-m", "fix: something\n\nRefs: feat-test-001")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	count, err := reindexCommitTrailers(database, tmpDir)
	if err != nil {
		t.Fatalf("reindexCommitTrailers: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 ingested trailer, got %d", count)
	}

	// Verify the row exists.
	var featureID string
	database.QueryRow("SELECT feature_id FROM git_commits WHERE session_id = ?",
		trailerSessionID).Scan(&featureID)
	if featureID != "feat-test-001" {
		t.Errorf("expected feature_id=feat-test-001, got %q", featureID)
	}
}

func TestReindexCommitTrailers_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Run()
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package x"), 0o644)
	run("add", "file.go")
	run("commit", "-m", "fix: thing\n\nRefs: feat-idem")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	count1, _ := reindexCommitTrailers(database, tmpDir)
	count2, _ := reindexCommitTrailers(database, tmpDir)

	if count1 != 1 {
		t.Errorf("first run: expected 1, got %d", count1)
	}
	if count2 != 0 {
		t.Errorf("second run: expected 0 (idempotent), got %d", count2)
	}
}

// TestReindexCommitTrailers_IncrementalBookmark guards against a regression
// of bug-9577013c in the other direction: a high-water mark that never
// advances (or that gets stuck and re-scans forever) would either miss new
// commits or re-pay the full-history cost on every run. This verifies both:
// the bookmark advances past already-scanned commits, and new commits made
// after the bookmark are still picked up.
func TestReindexCommitTrailers_IncrementalBookmark(t *testing.T) {
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(tmpDir, "file.go"), []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "file.go")
	run("commit", "-m", "fix: first (feat-aaaa1111)")
	run("commit", "--allow-empty", "-m", "fix: second (feat-bbbb2222)")

	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	count1, err := reindexCommitTrailers(database, tmpDir)
	if err != nil {
		t.Fatalf("first reindexCommitTrailers: %v", err)
	}
	if count1 != 2 {
		t.Fatalf("first run: expected 2 links, got %d", count1)
	}

	bookmark, _ := dbpkg.GetMetadata(database, metaKeyLastTrailerScanCommit)
	head := gitHeadCommit(tmpDir)
	if bookmark != head {
		t.Fatalf("bookmark = %q, want HEAD %q", bookmark, head)
	}

	// A third commit lands after the bookmark; it must still be picked up.
	run("commit", "--allow-empty", "-m", "fix: third (feat-cccc3333)")

	count2, err := reindexCommitTrailers(database, tmpDir)
	if err != nil {
		t.Fatalf("second reindexCommitTrailers: %v", err)
	}
	if count2 != 1 {
		t.Fatalf("incremental run: expected 1 new link (third commit only), got %d", count2)
	}

	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM git_commits WHERE session_id = ?`, trailerSessionID).Scan(&total); err != nil {
		t.Fatalf("count git_commits: %v", err)
	}
	if total != 3 {
		t.Errorf("expected 3 total linked commits, got %d", total)
	}
}
