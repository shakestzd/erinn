package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/claimledger"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// Canonical reconcile scans (feat-fc3cc9e0).
//
// The two item-shaped reconcile classes used to be SQL over the per-project
// read index: `SELECT … FROM features WHERE status IN (…)` for done-but-
// uncommitted, and a features⋈sessions anti-join for started-but-orphaned. That
// index is gone, and every caller was passing a nil handle, so both classes had
// silently stopped running. They are re-derived here from committed state.
//
// The done-but-uncommitted scan is also INVERTED relative to the old one, which
// is what removes its cap. The old scan listed terminal items (LIMIT 500 per
// status, later paginated) and asked git about each one; this asks git ONCE
// which artifacts are dirty — a set that is small by construction — and reads
// the status only of those few. There is no window in which an old dirty
// artifact can hide, so the capped/paginated split the DB scan needed is gone.

// terminalWorkItemStatuses are the states that make an artifact's git-dirtiness
// a reconcilable fact rather than normal in-flight churn.
var terminalWorkItemStatuses = map[models.NodeStatus]bool{
	models.StatusDone:  true,
	models.StatusEnded: true,
}

// workItemArtifactDirs are the .wipnote subdirectories holding the work-item
// artifacts both reconcile classes care about, in a fixed order so the git
// invocation and the on-disk probe are deterministic.
var workItemArtifactDirs = []string{"bugs", "features", "spikes"}

// isWorkItemArtifactDir reports whether dir is one of workItemArtifactDirs.
func isWorkItemArtifactDir(dir string) bool {
	for _, d := range workItemArtifactDirs {
		if d == dir {
			return true
		}
	}
	return false
}

// dirtyArtifact is one work-item HTML file with uncommitted changes.
type dirtyArtifact struct {
	ID  string
	Abs string
	Rel string
}

// reconcileDoneButUncommittedCanonical auto-commits the canonical artifact of
// every work item that is in a terminal state and whose HTML is dirty in git.
// Returns the IDs committed this pass. Uncapped: the candidate set is the dirty
// set, not the terminal set.
func reconcileDoneButUncommittedCanonical(projectDir string) []string {
	repoRoot := reconcileRepoRoot(projectDir)
	if repoRoot == "" {
		return nil
	}

	var committed []string
	for _, art := range dirtyWorkItemArtifacts(repoRoot) {
		node, err := htmlparse.ParseFile(art.Abs)
		if err != nil || node == nil || !terminalWorkItemStatuses[node.Status] {
			continue
		}
		if reconcileArtifactCommitFn(repoRoot, art.Abs, art.Rel, art.ID) {
			committed = append(committed, art.ID)
		}
	}
	sort.Strings(committed)
	return committed
}

// dirtyWorkItemArtifacts returns the work-item HTML files under
// .wipnote/{features,bugs,spikes} that git reports as changed or untracked, in
// a single `git status` invocation scoped to those three directories.
func dirtyWorkItemArtifacts(repoRoot string) []dirtyArtifact {
	args := []string{
		"-C", repoRoot, "status", "--porcelain=v1", "-z",
		// =all, not =normal: git collapses a wholly-untracked directory to a
		// single "dir/" entry under =normal, which would hide every artifact in
		// a .wipnote store that has never been committed.
		"--untracked-files=all", "--",
	}
	// Only pass pathspecs that exist: git fails the whole invocation on a
	// pathspec matching nothing, and a project with no spikes yet is normal.
	pathspecs := 0
	for _, dir := range workItemArtifactDirs {
		rel := filepath.Join(".wipnote", dir)
		if fi, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil || !fi.IsDir() {
			continue
		}
		args = append(args, rel)
		pathspecs++
	}
	if pathspecs == 0 {
		return nil
	}

	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}

	var arts []dirtyArtifact
	for _, rel := range porcelainZPaths(out) {
		slash := filepath.ToSlash(rel)
		if !strings.HasSuffix(slash, ".html") {
			continue
		}
		parts := strings.Split(slash, "/")
		if len(parts) != 3 || parts[0] != ".wipnote" {
			continue
		}
		if !isWorkItemArtifactDir(parts[1]) {
			continue
		}
		arts = append(arts, dirtyArtifact{
			ID:  strings.TrimSuffix(parts[2], ".html"),
			Abs: filepath.Join(repoRoot, filepath.FromSlash(slash)),
			Rel: slash,
		})
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].ID < arts[j].ID })
	return arts
}

// porcelainZPaths extracts the changed path of each entry in `git status
// --porcelain=v1 -z` output. Each record is "XY <path>\0"; rename/copy entries
// append a second "\0<source>" which is consumed and discarded, so the path
// returned is always the CURRENT one.
func porcelainZPaths(out []byte) []string {
	var paths []string
	fields := bytes.Split(out, []byte{0})
	for i := 0; i < len(fields); i++ {
		entry := string(fields[i])
		if len(entry) < 4 {
			continue
		}
		x, y := entry[0], entry[1]
		paths = append(paths, entry[3:])
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++ // skip the rename/copy source path
		}
	}
	return paths
}

// reconcileStartedButOrphanedCanonical reports work items still held by an open
// claim episode whose owning session is no longer running. It is the canonical
// replacement for the features⋈sessions anti-join: the claim ledger records who
// holds what, and .session-pid records whether that owner is alive.
//
// Reported only, never auto-resolved — silently reopening or completing an
// in-flight item would corrupt state.
//
// Cheap enough for the Stop hot path: the claim ledger is a handful of
// per-root-session shards, liveness is a kill(pid,0), and only the few
// candidates that survive both are parsed off disk.
//
// STRICTER THAN THE OLD QUERY, DELIBERATELY. The anti-join reported any
// in-progress item with no active session row; this requires an open claim AND
// an owner that IsSessionProcessAlive can prove dead. That predicate
// safe-degrades to LIVE whenever it cannot prove death (no .session-pid anchor,
// EPERM, non-Linux without a start time), so sessions without an anchor are
// never reported. Under-reporting is the right direction for a class that is
// report-only: a missed orphan is noise the next pass can still catch, whereas a
// falsely-orphaned live item is a lie about work in flight.
func reconcileStartedButOrphanedCanonical(projectDir string) []string {
	repoRoot := reconcileRepoRoot(projectDir)
	if repoRoot == "" {
		return nil
	}
	wipnoteDir := filepath.Join(repoRoot, ".wipnote")

	episodes, err := claimledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return nil
	}
	current := EnvSessionID("")

	seen := map[string]bool{}
	var orphaned []string
	for _, e := range episodes {
		if !e.IsOpen() || e.WorkItemID == "" || seen[e.WorkItemID] {
			continue
		}
		owner := e.RootSessionID
		if owner == "" {
			owner = e.SessionID
		}
		if owner == "" || owner == current {
			// Unknown owner, or the session running this pass: both degrade to
			// "not orphaned". Reporting the reconciling session's own in-flight
			// item is exactly the false positive the old query's status='active'
			// clause avoided.
			continue
		}
		if IsSessionProcessAlive(filepath.Join(wipnoteDir, "sessions", owner)) {
			continue
		}
		if !workItemStillOpen(wipnoteDir, e.WorkItemID) {
			continue
		}
		seen[e.WorkItemID] = true
		orphaned = append(orphaned, e.WorkItemID)
	}
	sort.Strings(orphaned)
	return orphaned
}

// workItemStillOpen reports whether the item's canonical artifact exists and is
// in a non-terminal state. A claim left open on an item that was completed
// anyway is stale bookkeeping, not an orphan.
func workItemStillOpen(wipnoteDir, id string) bool {
	for _, dir := range workItemArtifactDirs {
		path := filepath.Join(wipnoteDir, dir, id+".html")
		node, err := htmlparse.ParseFile(path)
		if err != nil || node == nil {
			continue
		}
		return !terminalWorkItemStatuses[node.Status]
	}
	return false
}
