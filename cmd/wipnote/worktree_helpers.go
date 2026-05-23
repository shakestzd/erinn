package main

import (
	"io"

	"github.com/shakestzd/wipnote/internal/worktree"
)

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

// EnsureForTrackWithTitleStatus is EnsureForTrackWithTitle plus a "created vs
// reused" signal. created is true only when a NEW worktree was created on disk.
// Used by the yolo launch path to gate uncommitted-change carryover (bug-bcf8a311).
func EnsureForTrackWithTitleStatus(trackTitle, trackID, repoRoot string, w io.Writer) (string, bool, error) {
	return worktree.EnsureForTrackTitledStatus(trackTitle, trackID, repoRoot, w)
}

// EnsureForFeatureStatus is EnsureForFeature plus a "created vs reused" signal.
func EnsureForFeatureStatus(featureID, repoRoot string, w io.Writer) (string, bool, error) {
	return worktree.EnsureForFeatureStatus(featureID, repoRoot, w)
}

// EnsureForAgent ensures a git worktree exists for the given agent task and returns its path.
// The worktree branches from the track branch and is placed at
// .claude/worktrees/<trackID>/agent-<taskName>.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForAgent(trackID, taskName, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForAgent(trackID, taskName, repoRoot, w)
}
