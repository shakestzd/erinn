package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// session_end_reconcile_drain_cap_test.go — roborev-478 round-3 finding 2.
//
// The serve-side drain (ReconcileDoneButUncommittedForProject) is documented as
// uncapped, but it formerly delegated to reconcileDoneButUncommitted, which only
// scans the newest 500 terminal items per status (ListFeaturesByStatus LIMIT
// 500). Once the newest 500 are clean, an OLDER dirty terminal artifact was
// permanently hidden on the drain path. The drain now pages through EVERY
// terminal item, so the old dirty artifact is eventually reconciled.

// commitArtifact stages and commits a single work-item artifact so it counts as
// CLEAN for the reconcile dirty-scan. Used to fill the newest-500 window with
// clean items, leaving exactly one OLD dirty item below the cap.
func commitArtifact(t *testing.T, root, typeName, id string) {
	t.Helper()
	rel := filepath.Join(".wipnote", typeName+"s", id+".html")
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
	run("add", "--", rel)
	run("commit", "-q", "-m", "seed clean "+id)
}

// TestReconcileDrain_Uncapped_ReconcilesOldDirtyBeyond500 seeds >500 clean
// "done" features (so the newest-500 window is entirely clean) plus ONE older
// dirty "done" feature that the priority/created_at ordering sorts BELOW the
// 500-item cap. It proves:
//
//  1. the capped synchronous scan (reconcileDoneButUncommitted) does NOT see the
//     old dirty item — reproducing the bug, and
//  2. the serve drain (ReconcileDoneButUncommittedForProject) DOES reconcile it —
//     the uncapped/paginated scan no longer hides it.
func TestReconcileDrain_Uncapped_ReconcilesOldDirtyBeyond500(t *testing.T) {
	clearNestedEnv(t)
	td := setupTestDB(t)

	root := t.TempDir()
	gitInitRepo(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".wipnote"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 510 CLEAN done features at priority "medium" — these fill (and overflow)
	// the newest-500 capped window. Each artifact is written AND committed, so
	// none are dirty.
	const nClean = 510
	for i := 0; i < nClean; i++ {
		id := fmt.Sprintf("feat-clean-%06d", i)
		td.addFeature(id, "feature", "clean done", "done")
		writeArtifact(t, root, "feature", id)
		commitArtifact(t, root, "feature", id)
	}

	// One OLD dirty done feature at priority "low". The capped scan orders by
	// priority DESC (low sorts AFTER medium) then created_at DESC and stops at
	// 500 medium items, so it never returns this low item. Its artifact is
	// written but NOT committed → dirty.
	const oldDirty = "feat-zzz-old-dirty"
	tdAddLowPriorityFeature(t, td, oldDirty, "done")
	writeArtifact(t, root, "feature", oldDirty)

	dirtyPath := filepath.Join(root, ".wipnote", "features", oldDirty+".html")

	// Precondition: the artifact is dirty.
	if !reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("precondition: %s should be dirty", oldDirty)
	}

	// (1) The capped synchronous scan must MISS the old dirty item (the bug).
	capped := reconcileDoneButUncommitted(td.DB, root)
	for _, id := range capped {
		if id == oldDirty {
			t.Fatalf("capped reconcile unexpectedly saw %s — the >500 cap should hide it "+
				"(this test would not prove the fix)", oldDirty)
		}
	}
	if !reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("capped reconcile committed %s; it must remain dirty under the cap", oldDirty)
	}

	// (2) The uncapped serve drain MUST reconcile the old dirty item.
	committed := ReconcileDoneButUncommittedForProject(td.DB, root)
	found := false
	for _, id := range committed {
		if id == oldDirty {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("serve drain did not reconcile old dirty item %s (committed=%d); the "+
			"newest-500 cap must no longer hide it", oldDirty, len(committed))
	}
	if reconcilePathDirty(root, dirtyPath) {
		t.Fatalf("serve drain reported %s committed but the artifact is still dirty", oldDirty)
	}

	// Idempotent: a second drain re-commits nothing.
	if again := ReconcileDoneButUncommittedForProject(td.DB, root); len(again) != 0 {
		t.Fatalf("second drain re-committed %v; must be a no-op", again)
	}
}

// tdAddLowPriorityFeature inserts a "low" priority feature so it sorts to the
// very end of the capped ListFeaturesByStatus order (priority DESC), beyond the
// 500-item window filled by the medium-priority clean items.
func tdAddLowPriorityFeature(t *testing.T, td *testDB, id, status string) {
	t.Helper()
	feat := &db.Feature{
		ID:        id,
		Type:      "feature",
		Title:     "old dirty done",
		Status:    status,
		Priority:  "low",
		CreatedAt: td.now,
		UpdatedAt: td.now,
	}
	if err := db.InsertFeature(td.DB, feat); err != nil {
		t.Fatalf("InsertFeature(%s): %v", id, err)
	}
}
