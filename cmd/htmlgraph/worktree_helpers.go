package main

import (
	"io"

	"github.com/shakestzd/htmlgraph/internal/worktree"
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

// EnsureForAgent ensures a git worktree exists for the given agent task and returns its path.
// The worktree branches from the track branch and is placed at
// .claude/worktrees/<trackID>/agent-<taskName>.
// Progress is written to w; pass io.Discard to suppress output.
func EnsureForAgent(trackID, taskName, repoRoot string, w io.Writer) (string, error) {
	return worktree.EnsureForAgent(trackID, taskName, repoRoot, w)
}
