package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
	"github.com/shakestzd/wipnote/internal/launcher/plan"
	"github.com/shakestzd/wipnote/core/worktree"
)

// emitWorktreeCarryoverMessage carries canonical main-repo uncommitted tracked
// changes into a freshly-created worktree and prints an accurate dirty-main
// advisory. It is shared by both the yolo and claude launch paths.
//
//   - Carryover runs ONLY when created is true (a NEW worktree was made this
//     launch). A reused worktree is skipped to avoid double-applying.
//   - Main's working tree is never mutated; failure is non-fatal (warning only).
//   - The advisory is emitted only when main was actually dirty (the plan carries
//     a DirtyMainWarning). It deliberately does NOT mention "--work-item" — the
//     worktree already exists.
func emitWorktreeCarryoverMessage(p plan.LaunchPlan, canonicalRoot, worktreePath string, created bool, w io.Writer) {
	if !created {
		// Reused worktree: skip carryover (guardrail) and skip the dirty-main
		// advisory (the prior session already isolated the work).
		return
	}

	res, _ := worktree.CarryUncommittedChanges(canonicalRoot, worktreePath, w)

	// Only emit the dirty-main advisory when main actually had uncommitted
	// changes. PlanLaunch records that in DirtyMainWarning.
	if p.DirtyMainWarning == "" {
		return
	}

	untracked := "none"
	if len(res.UntrackedFiles) > 0 {
		untracked = strings.Join(res.UntrackedFiles, ", ")
	}

	switch {
	case res.ApplyError != nil:
		fmt.Fprintf(w,
			"Dirty main detected — isolating in managed worktree %s; "+
				"could not auto-carry your uncommitted changes (%v) — they remain on main, apply manually. "+
				"Untracked files not carried: %s\n",
			worktreePath, res.ApplyError, untracked)
	default:
		fmt.Fprintf(w,
			"Dirty main detected — isolating in managed worktree %s; "+
				"copied your uncommitted changes into the worktree (main left unchanged). "+
				"Untracked files not carried: %s\n",
			worktreePath, untracked)
	}
}

// EnsureForFeature ensures a git worktree exists for the given feature and returns its path.
// When the feature belongs to a parent track, the track worktree is created/reused instead.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForFeature(featureID, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForFeature(featureID, repoRoot, w)
}

// EnsureForTrack ensures a git worktree exists for the given track and returns its path.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForTrack(trackID, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForTrack(trackID, repoRoot, w)
}

// EnsureForTrackWithTitle ensures a git worktree exists for the given track, using a
// human-readable directory name "<title-slug>-<trackID>" when trackTitle is non-empty.
// Existing worktrees at the legacy bare-ID path are reused unchanged.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForTrackWithTitle(trackTitle, trackID, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForTrackTitled(trackTitle, trackID, repoRoot, w)
}

// ensureWorktreeWithSpinner runs a worktree-prep function under an animated
// spinner (feat-e97607b3) so the user sees live feedback during the sometimes-
// slow `git worktree add` instead of a frozen line, resolving to a final ✓.
// Off a TTY it is a transparent passthrough: fn writes straight to w with no
// animation and no extra chrome, so log/CI/test output is unchanged.
func ensureWorktreeWithSpinner(label string, w io.Writer, fn func(io.Writer) (string, bool, error)) (string, bool, error) {
	var path string
	var created bool
	err := launchtui.RunWithSpinner(w, label, func(bw io.Writer) error {
		var e error
		path, created, e = fn(bw)
		return e
	})
	return path, created, err
}

// EnsureForTrackWithTitleStatus is EnsureForTrackWithTitle plus a "created vs
// reused" signal. created is true only when a NEW worktree was created on disk.
// Used by the yolo launch path to gate uncommitted-change carryover (bug-bcf8a311).
func EnsureForTrackWithTitleStatus(trackTitle, trackID, repoRoot string, w io.Writer) (string, bool, error) {
	return ensureWorktreeWithSpinner("Preparing worktree", w, func(bw io.Writer) (string, bool, error) {
		return worktree.EnsureForTrackTitledStatus(trackTitle, trackID, repoRoot, bw)
	})
}

// EnsureForTrackStatus is EnsureForTrack plus a "created vs reused" signal.
// created is true only when a NEW worktree was created on disk.
// Used by the codex/antigravity launch paths to gate uncommitted-change carryover (bug-938e56ae).
func EnsureForTrackStatus(trackID, repoRoot string, w io.Writer) (string, bool, error) {
	return ensureWorktreeWithSpinner("Preparing worktree", w, func(bw io.Writer) (string, bool, error) {
		return worktree.EnsureForTrackStatus(trackID, repoRoot, bw)
	})
}

// EnsureForFeatureStatus is EnsureForFeature plus a "created vs reused" signal.
func EnsureForFeatureStatus(featureID, repoRoot string, w io.Writer) (string, bool, error) {
	return ensureWorktreeWithSpinner("Preparing worktree", w, func(bw io.Writer) (string, bool, error) {
		return worktree.EnsureForFeatureStatus(featureID, repoRoot, bw)
	})
}

// EnsureForAgent ensures a git worktree exists for the given agent task and returns its path.
// The worktree branches from the track branch and is placed at
// .claude/worktrees/<trackID>/agent-<taskName>.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForAgent(trackID, taskName, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForAgent(trackID, taskName, repoRoot, w)
}
