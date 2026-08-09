package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// session_end_reconcile_drain_cap_test.go — the uncapped-drain guarantee.
//
// HISTORY: this class used to be a SQL scan over the read index that listed the
// newest 500 terminal items per status and asked git about each. Once those 500
// were clean, an OLDER dirty terminal artifact was permanently hidden
// (roborev-478 finding 2), which the serve drain worked around by paging.
//
// The canonical scan (feat-fc3cc9e0) inverts the direction: git is asked ONCE
// which artifacts are dirty, and only those few are read for status. There is no
// newest-N window left to fall below, so "uncapped" is now structural — and the
// synchronous scan and the serve drain, which had to differ under the cap, are
// required to agree. This test pins that: it seeds far more than 500 terminal
// items, keeps one OLD dirty artifact among them, and asserts both entry points
// reconcile it.

// gitCommitAllArtifacts commits seeded work-item artifacts so they count as
// CLEAN for the reconcile dirty-scan. The test needs committed clean artifacts,
// not hundreds of individual git commits.
func gitCommitAllArtifacts(t *testing.T, root, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	run("add", "--all")
	run("commit", "-q", "-m", msg)
}

// TestReconcileDrain_Uncapped_ReconcilesOldDirtyBeyond500 seeds 510 clean "done"
// features — more than the old 500-item window — plus ONE dirty "done" feature
// committed to the repo BEFORE them, so any newest-N ordering would sort it last.
// Both the synchronous scan and the serve drain must still reconcile it.
func TestReconcileDrain_Uncapped_ReconcilesOldDirtyBeyond500(t *testing.T) {
	clearNestedEnv(t)

	root := t.TempDir()
	gitInitRepo(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The OLD item: created and committed first, so it is the oldest terminal
	// artifact in the repo. Its later modification is what makes it dirty.
	const oldDirty = "feat-zzz-old-dirty"
	writeArtifact(t, root, "feature", oldDirty)
	gitCommitAllArtifacts(t, root, "seed old artifact")

	// 510 CLEAN done features, committed in one batch so none is dirty.
	const nClean = 510
	for i := 0; i < nClean; i++ {
		writeArtifact(t, root, "feature", fmt.Sprintf("feat-clean-%06d", i))
	}
	gitCommitAllArtifacts(t, root, "seed clean artifacts")

	// Now dirty the old artifact.
	dirtyPath := filepath.Join(root, ".wipnote", "features", oldDirty+".html")
	if err := os.WriteFile(dirtyPath, []byte(
		`<html><body><article id="`+oldDirty+`" data-type="feature" data-status="done">`+
			`<header><h1>old dirty done</h1></header></article></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("precondition: %s should be dirty", oldDirty)
	}

	// (1) The synchronous scan sees it — no cap to hide behind.
	sync := reconcileDoneButUncommittedCanonical(root)
	if !containsID(sync, oldDirty) {
		t.Fatalf("synchronous scan missed old dirty item %s (got %v)", oldDirty, sync)
	}
	if reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("synchronous scan reported %s committed but the artifact is still dirty", oldDirty)
	}

	// (2) The serve drain agrees: nothing left, and re-running is a no-op.
	if committed := ReconcileDoneButUncommittedForProject(nil, root); len(committed) != 0 {
		t.Fatalf("serve drain re-committed %v after the synchronous scan already did; must be a no-op", committed)
	}

	// (3) Dirty it again and prove the drain reconciles it on its own.
	if err := os.WriteFile(dirtyPath, []byte(
		`<html><body><article id="`+oldDirty+`" data-type="feature" data-status="done">`+
			`<header><h1>old dirty done again</h1></header></article></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	drained := ReconcileDoneButUncommittedForProject(nil, root)
	if !containsID(drained, oldDirty) {
		t.Fatalf("serve drain did not reconcile old dirty item %s (committed=%v)", oldDirty, drained)
	}
	if reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("serve drain reported %s committed but the artifact is still dirty", oldDirty)
	}
	if again := ReconcileDoneButUncommittedForProject(nil, root); len(again) != 0 {
		t.Fatalf("second drain re-committed %v; must be a no-op", again)
	}
}

// TestReconcileCanonical_NonTerminalDirtyArtifactIsNotCommitted pins the status
// half of the scan: a dirty artifact that is still in-progress is normal churn,
// not something to auto-commit.
func TestReconcileCanonical_NonTerminalDirtyArtifactIsNotCommitted(t *testing.T) {
	clearNestedEnv(t)

	root := t.TempDir()
	gitInitRepo(t, root)
	writeArtifactWithStatus(t, root, "feature", "feat-inflight", "in-progress")
	writeArtifact(t, root, "bug", "bug-finished")

	committed := reconcileDoneButUncommittedCanonical(root)
	if containsID(committed, "feat-inflight") {
		t.Fatalf("in-progress artifact must not be auto-committed, got %v", committed)
	}
	if !containsID(committed, "bug-finished") {
		t.Fatalf("terminal bug artifact should be auto-committed, got %v", committed)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
