package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

// setupLinkCommitRepo builds a git repo with one commit and a canonical bug
// artifact, and returns (repoRoot, commitHash). The commit message deliberately
// does NOT mention the work item — a commit that already names its item needs
// no manual link, so the interesting case is the one that does not.
func setupLinkCommitRepo(t *testing.T, itemID, collection string) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repoRoot
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(repoRoot, "fix.go"), []byte("package fix\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	run("add", "fix.go")
	run("commit", "-m", "fix: repair the widget")
	hash := run("rev-parse", "HEAD")

	wipnoteDir := filepath.Join(repoRoot, ".wipnote")
	dir := filepath.Join(wipnoteDir, collection)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	nodeType := strings.TrimSuffix(collection, "s")
	html := `<!DOCTYPE html><html><body><article id="` + itemID +
		`" data-type="` + nodeType + `" data-status="in-progress" data-priority="medium">` +
		`<h1>Test ` + itemID + `</h1></article></body></html>`
	if err := os.WriteFile(filepath.Join(dir, itemID+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write item: %v", err)
	}
	return repoRoot, hash
}

// commitEdgeTargets returns the targets of the item's committed_in edges as
// read back off disk.
func commitEdgeTargets(t *testing.T, repoRoot, itemID, collection string) []string {
	t.Helper()
	path := filepath.Join(repoRoot, ".wipnote", collection, itemID+".html")
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, e := range node.Edges[string(RelCommittedIn)] {
		out = append(out, e.TargetID)
	}
	return out
}

// TestLinkCommit_WritesCanonicalEdge is the regression test for the live bug
// this command carried: it used to insert a git_commits row and nothing else,
// so it printed "Linked: …" while the link existed nowhere any read path could
// find it. The assertion is deliberately made by re-parsing the artifact from
// disk — the link has to survive the process that created it.
func TestLinkCommit_WritesCanonicalEdge(t *testing.T) {
	repoRoot, hash := setupLinkCommitRepo(t, "bug-0a812209", "bugs")

	isolateProjectDir(t, repoRoot)
	withWorkingDir(t, repoRoot, func() {
		if err := runLinkCommit("bug", "bug-0a812209", hash[:8]); err != nil {
			t.Fatalf("runLinkCommit: %v", err)
		}
	})

	targets := commitEdgeTargets(t, repoRoot, "bug-0a812209", "bugs")
	if len(targets) != 1 {
		t.Fatalf("committed_in targets = %v, want exactly one", targets)
	}
	if targets[0] != hash {
		t.Errorf("committed_in target = %q, want the full 40-char hash %q", targets[0], hash)
	}
}

// TestLinkCommit_CarriesCommitSubject verifies the edge is self-describing:
// the commit subject rides along as the edge title, so the artifact reads as
// prose without a second git lookup.
func TestLinkCommit_CarriesCommitSubject(t *testing.T) {
	repoRoot, hash := setupLinkCommitRepo(t, "feat-0a812209", "features")

	isolateProjectDir(t, repoRoot)
	withWorkingDir(t, repoRoot, func() {
		if err := runLinkCommit("feature", "feat-0a812209", hash); err != nil {
			t.Fatalf("runLinkCommit: %v", err)
		}
	})

	node, err := htmlparse.ParseFile(filepath.Join(repoRoot, ".wipnote", "features", "feat-0a812209.html"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	edges := node.Edges[string(RelCommittedIn)]
	if len(edges) != 1 {
		t.Fatalf("committed_in edges = %d, want 1", len(edges))
	}
	if edges[0].Title != "fix: repair the widget" {
		t.Errorf("edge title = %q, want the commit subject", edges[0].Title)
	}
	if edges[0].Since.IsZero() {
		t.Error("edge Since is zero — the commit timestamp should be recorded")
	}
}

// TestLinkCommit_Idempotent verifies a re-link does not stack a second edge.
// AddEdge appends unconditionally, so without the pre-read guard this would
// grow the artifact on every invocation.
func TestLinkCommit_Idempotent(t *testing.T) {
	repoRoot, hash := setupLinkCommitRepo(t, "bug-0a812210", "bugs")

	isolateProjectDir(t, repoRoot)
	withWorkingDir(t, repoRoot, func() {
		if err := runLinkCommit("bug", "bug-0a812210", hash); err != nil {
			t.Fatalf("first link: %v", err)
		}
		if err := runLinkCommit("bug", "bug-0a812210", hash[:10]); err != nil {
			t.Fatalf("second link: %v", err)
		}
	})

	targets := commitEdgeTargets(t, repoRoot, "bug-0a812210", "bugs")
	if len(targets) != 1 {
		t.Fatalf("committed_in targets after re-link = %v, want exactly one", targets)
	}
}

// TestLinkCommit_UnknownCommitFails verifies the SHA is verified against the
// repo before anything is written — a typo must not land a dangling edge.
func TestLinkCommit_UnknownCommitFails(t *testing.T) {
	repoRoot, _ := setupLinkCommitRepo(t, "bug-0a812211", "bugs")

	isolateProjectDir(t, repoRoot)
	withWorkingDir(t, repoRoot, func() {
		err := runLinkCommit("bug", "bug-0a812211", "0123456789abcdef0123456789abcdef01234567")
		if err == nil {
			t.Fatal("expected an error for a SHA that is not in the repo")
		}
	})

	if targets := commitEdgeTargets(t, repoRoot, "bug-0a812211", "bugs"); len(targets) != 0 {
		t.Errorf("no edge should have been written, got %v", targets)
	}
}
