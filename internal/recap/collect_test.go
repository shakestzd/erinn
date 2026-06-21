package recap

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// fixture bundles a temp git repo and an in-memory read index with helpers to
// stage realistic recap inputs.
type fixture struct {
	t       *testing.T
	dir     string
	db      *sql.DB
	commits []string // commit hashes, oldest first
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "commit.gpgsign", "false")

	database, err := dbpkg.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return &fixture{t: t, dir: dir, db: database}
}

// commit writes the given file contents and creates a commit, returning its hash.
func (f *fixture) commit(msg string, files map[string]string) string {
	f.t.Helper()
	for name, content := range files {
		path := filepath.Join(f.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			f.t.Fatalf("mkdir: %v", err)
		}
		if content == "" {
			os.Remove(path)
		} else if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			f.t.Fatalf("write %s: %v", name, err)
		}
	}
	run(f.t, f.dir, "git", "add", "-A")
	run(f.t, f.dir, "git", "commit", "-q", "-m", msg)
	hash := output(f.t, f.dir, "git", "rev-parse", "HEAD")
	f.commits = append(f.commits, hash)
	return hash
}

// recordCommit inserts a git_commits read-index row for a hash.
func (f *fixture) recordCommit(hash, session, feature, msg string, ts time.Time) {
	f.t.Helper()
	err := dbpkg.InsertGitCommit(f.db, &models.GitCommit{
		CommitHash: hash,
		SessionID:  session,
		FeatureID:  feature,
		Message:    msg,
		Timestamp:  ts,
	})
	if err != nil {
		f.t.Fatalf("InsertGitCommit: %v", err)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func output(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(trimNL(out))
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func fileChange(data *RecapData, path string) *FileChange {
	for i := range data.Files {
		if data.Files[i].Path == path {
			return &data.Files[i]
		}
	}
	return nil
}

func TestCollect_FromFeature(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()

	f.commit("seed", map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	c1 := f.commit("feat: add b", map[string]string{
		"b.go": "package b\n\nfunc B() {}\n",
		"a.go": "package a\n\nfunc A() int { return 1 }\n",
	})
	c2 := f.commit("feat: drop a", map[string]string{"a.go": ""})

	if err := dbpkg.InsertFeature(f.db, &dbpkg.Feature{
		ID: "feat-aaa", Type: "feature", Title: "Add B and prune A",
		Status: "done", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertFeature: %v", err)
	}
	f.recordCommit(c1, "sess-1", "feat-aaa", "feat: add b", now)
	f.recordCommit(c2, "sess-1", "feat-aaa", "feat: drop a", now.Add(time.Second))

	data, err := Collect(f.db, Options{Input: "feat-aaa", ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if data.Outcome != "Add B and prune A" {
		t.Errorf("Outcome = %q, want feature title", data.Outcome)
	}
	if data.Provenance.Kind != InputWorkItem || !data.Provenance.Grounded {
		t.Errorf("Provenance = %+v, want grounded work-item", data.Provenance)
	}
	if len(data.Commits) != 2 {
		t.Fatalf("Commits = %d, want 2", len(data.Commits))
	}

	// Union of changed files across the range: a.go (modified then deleted),
	// b.go (added). Deterministic, path-sorted.
	if got := len(data.Files); got != 2 {
		t.Fatalf("Files = %d, want 2: %+v", got, data.Files)
	}
	if data.Files[0].Path != "a.go" || data.Files[1].Path != "b.go" {
		t.Fatalf("files not path-sorted: %v %v", data.Files[0].Path, data.Files[1].Path)
	}
	if aChange := fileChange(data, "a.go"); aChange == nil || aChange.Change != ChangeDelete {
		t.Errorf("a.go change = %v, want delete", aChange)
	}
	if bChange := fileChange(data, "b.go"); bChange == nil || bChange.Change != ChangeAdd {
		t.Errorf("b.go change = %v, want add", bChange)
	}
	bChange := fileChange(data, "b.go")
	if len(bChange.Hunks) == 0 || len(bChange.Hunks[0].After) == 0 {
		t.Errorf("b.go hunks missing after-content: %+v", bChange.Hunks)
	}
}

func TestCollect_FromFeature_DiffsOnlySelectedCommits(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()

	selected := f.commit("selected root", map[string]string{"selected.txt": "root\n"})
	unrelated := f.commit("unrelated", map[string]string{"unrelated.txt": "do not include\n"})
	f.commit("selected followup", map[string]string{"selected.txt": "root\nfollowup\n"})

	if err := dbpkg.InsertFeature(f.db, &dbpkg.Feature{
		ID: "feat-selected", Type: "feature", Title: "Selected only",
		Status: "done", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertFeature: %v", err)
	}
	f.recordCommit(selected, "sess-1", "feat-selected", "selected root", now)
	f.recordCommit(f.commits[2], "sess-1", "feat-selected", "selected followup", now.Add(time.Second))

	data, err := Collect(f.db, Options{Input: "feat-selected", ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if fc := fileChange(data, "selected.txt"); fc == nil {
		t.Fatalf("selected.txt missing from selected commit diffs: %+v", data.Files)
	}
	if fc := fileChange(data, "unrelated.txt"); fc != nil {
		t.Fatalf("unrelated intervening commit leaked into recap diff after %s: %+v", unrelated, fc)
	}
}

func TestCollect_FromFeature_RootCommitDoesNotRequireParent(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()

	root := f.commit("root selected", map[string]string{"root.txt": "hello\n"})
	if err := dbpkg.InsertFeature(f.db, &dbpkg.Feature{
		ID: "feat-root", Type: "feature", Title: "Root",
		Status: "done", Priority: "medium", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("InsertFeature: %v", err)
	}
	f.recordCommit(root, "sess-root", "feat-root", "root selected", now)

	data, err := Collect(f.db, Options{Input: "feat-root", ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect root commit: %v", err)
	}
	if fc := fileChange(data, "root.txt"); fc == nil || fc.Change != ChangeAdd {
		t.Fatalf("root.txt change = %+v, want added file", fc)
	}
}

func TestCollect_FromFeature_Deterministic(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	f.commit("seed", map[string]string{"a.go": "package a\n"})
	c1 := f.commit("feat", map[string]string{
		"z.go": "package z\n", "m.go": "package m\n", "a.go": "package a\n// edit\n",
	})
	f.recordCommit(c1, "sess-x", "feat-det", "feat", now)

	var prev []string
	for i := 0; i < 3; i++ {
		data, err := Collect(f.db, Options{Input: "feat-det", ProjectDir: f.dir})
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		var paths []string
		for _, fc := range data.Files {
			paths = append(paths, fc.Path)
		}
		if i > 0 && !equal(prev, paths) {
			t.Fatalf("non-deterministic file set: %v vs %v", prev, paths)
		}
		prev = paths
	}
	if !equal(prev, []string{"a.go", "m.go", "z.go"}) {
		t.Errorf("file set = %v, want sorted a,m,z", prev)
	}
}

func TestCollect_FromRange(t *testing.T) {
	f := newFixture(t)
	base := f.commit("base", map[string]string{"x.go": "package x\n"})
	f.commit("change", map[string]string{
		"x.go": "package x\n\nfunc X() {}\n",
		"y.go": "package y\n",
	})

	data, err := Collect(f.db, Options{Input: base + "..HEAD", ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if data.Provenance.Kind != InputRange {
		t.Errorf("Kind = %v, want range", data.Provenance.Kind)
	}
	if data.Provenance.Grounded {
		t.Errorf("bare range must not be grounded")
	}
	if len(data.LineageChain) != 0 {
		t.Errorf("bare range must omit lineage, got %d nodes", len(data.LineageChain))
	}
	if len(data.Commits) != 1 {
		t.Errorf("Commits = %d, want 1 (the change commit)", len(data.Commits))
	}
	if len(data.Files) != 2 {
		t.Fatalf("Files = %d, want 2", len(data.Files))
	}
	if data.Files[0].Path != "x.go" || data.Files[1].Path != "y.go" {
		t.Errorf("range files not path-sorted: %+v", data.Files)
	}
}

// TestCollect_FromRange_RootCommit verifies that collectRange handles the
// EmptyTreeSHA..HEAD sentinel correctly: the root commit appears in Commits and
// its added files appear in Files. Previously, `git log EmptyTreeSHA..HEAD`
// errored because EmptyTreeSHA is a tree object (not a commit).
func TestCollect_FromRange_RootCommit(t *testing.T) {
	f := newFixture(t)
	// Single root commit — no prior commits exist.
	f.commit("root", map[string]string{"root.go": "package root\n"})

	gitRange := EmptyTreeSHA + "..HEAD"
	data, err := Collect(f.db, Options{Input: gitRange, ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect with root-commit range: %v", err)
	}

	if data.Provenance.Kind != InputRange {
		t.Errorf("Kind = %v, want range", data.Provenance.Kind)
	}
	// Must include the root commit.
	if len(data.Commits) == 0 {
		t.Fatalf("Commits is empty — root commit was dropped by git log error path")
	}
	// Must include the file added by the root commit.
	if fc := fileChange(data, "root.go"); fc == nil || fc.Change != ChangeAdd {
		t.Errorf("root.go = %+v, want added file", fc)
	}
}

func TestCollect_FromSession(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	f.commit("seed", map[string]string{"a.go": "package a\n"})
	c1 := f.commit("work", map[string]string{"s.go": "package s\n"})

	f.recordCommit(c1, "sess-work", "feat-sess", "work", now)
	// A commit from a different session must not appear.
	other := f.commit("other", map[string]string{"o.go": "package o\n"})
	f.recordCommit(other, "sess-other", "", "other", now.Add(time.Second))

	data, err := Collect(f.db, Options{Input: "sess-work", ProjectDir: f.dir})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if data.Provenance.Kind != InputSession {
		t.Errorf("Kind = %v, want session", data.Provenance.Kind)
	}
	if !data.Provenance.Grounded {
		t.Errorf("session with feature_id commit should be grounded")
	}
	if len(data.Commits) != 1 || data.Commits[0].Hash != c1 {
		t.Fatalf("Commits = %+v, want only c1", data.Commits)
	}
	if fc := fileChange(data, "s.go"); fc == nil || fc.Change != ChangeAdd {
		t.Errorf("s.go = %v, want add", fc)
	}
	if fc := fileChange(data, "o.go"); fc != nil {
		t.Errorf("o.go from another session must not appear")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
