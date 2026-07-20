package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/commitqueue"
)

// workItemScopedIntent reports whether intent belongs to workItemID. When
// workItemID is empty (no active work item could be resolved for this gate
// run) no intent is considered in-scope, so the gate cannot block on
// unattributed repo-wide backlog it has no way to relate to the current run.
func workItemScopedIntent(intent commitqueue.Intent, workItemID string) bool {
	if strings.TrimSpace(workItemID) == "" {
		return false
	}
	return intent.WorkItemID == workItemID
}

// reportDeferredArtifactQueueHealth prints a non-blocking advisory summarizing
// repo-wide deferred artifact-commit queue backlog (bug-a5a846bc, #150). This
// never fails the gate — it exists so an operator running WIPNOTE_ARTIFACT_COMMIT_POLICY=defer
// still sees accumulating backlog from other work items without every later
// gate inheriting a stranger's failure state.
func reportDeferredArtifactQueueHealth(w io.Writer, pendingCount, deadLetteredCount int) {
	if pendingCount == 0 && deadLetteredCount == 0 {
		return
	}
	fmt.Fprintf(w, "advisory: %d repo-wide pending / %d dead-lettered deferred work-item artifact commit intent(s) queued (not blocking this gate — scoped to the current work item only). Run `wipnote commit-queue flush` to drain the backlog.\n", pendingCount, deadLetteredCount)
}

// wipnoteSelfModulePath is wipnote's own Go module path (see go.mod at repo
// root). It is the anchor isWipnoteSelfRepo uses to distinguish wipnote's own
// dev tree from an unrelated user project (bug-b3d49476, #154).
const wipnoteSelfModulePath = "github.com/shakestzd/wipnote"

// isWipnoteSelfRepo reports whether projectRoot is wipnote's own repository
// (dogfooding), detected by checking whether the module declared in its
// go.mod matches wipnote's own module path. Used to gate output that only
// makes sense inside wipnote's own dev/release workflow — e.g. the internal
// launch-readiness roster (bug-b3d49476, #154) — from leaking into unrelated
// user projects that happen to also be Go modules.
func isWipnoteSelfRepo(projectRoot string) bool {
	data, err := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "module "+wipnoteSelfModulePath {
			return true
		}
	}
	return false
}
