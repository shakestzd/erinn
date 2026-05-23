package main

import (
	"fmt"
	"io"
	"os"

	"github.com/shakestzd/wipnote/internal/launcher/mode"
	"github.com/shakestzd/wipnote/internal/launcher/plan"
	"github.com/shakestzd/wipnote/internal/paths"
)

// LauncherModeResult is the computed mode object exposed to preflight paths.
// Callers can log or inspect it without changing launcher behavior.
type LauncherModeResult = mode.LauncherMode

// computeLauncherMode returns a LauncherMode for the given launcher invocation.
// worktreePath should be non-empty when running in an isolated git worktree,
// devPlugin when launched with --dev (in-tree plugin source), and
// generatedPort when a harness-generated tree is active.
//
// This is the non-behavior-changing wiring point for all launchers.
// Future slices will act on the returned value; this slice only computes and
// optionally logs it.
func computeLauncherMode(worktreePath string, devPlugin, generatedPort bool) LauncherModeResult {
	m := mode.Compute(worktreePath, false, devPlugin, generatedPort)
	if os.Getenv("WIPNOTE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr,
			"wipnote [debug]: mode runtime=%s execution=%s plugin=%s dashboard=%s:%d\n",
			m.Runtime, m.Execution, m.Plugin, m.DashboardHost, m.DashboardPort,
		)
	}
	return m
}

// applyLaunchPlan computes the isolation plan for a mutating launcher invocation
// and prints any dirty-main warning to w. It does NOT create the worktree —
// that will happen when the managed-worktree path is executed (slice-3+).
//
// Rollout rules (HIGH critique honored):
//   - Host runtime → warn-only; RefuseLaunch is always false by default.
//   - Devcontainer → managed-worktree when a workItemID is provided.
//   - inPlace=true → IsolationExplicitInPlace; no warning.
func applyLaunchPlan(repoRoot, workItemID string, inPlace bool, w io.Writer) plan.LaunchPlan {
	return applyLaunchPlanOpts(repoRoot, workItemID, inPlace, false, w)
}

// applyLaunchPlanOpts is applyLaunchPlan with control over whether the
// plan's generic DirtyMainWarning is printed. When suppressDirtyWarning is true
// (yolo will create a managed worktree this launch), the generic
// "use a managed worktree (--work-item <id>)" advisory is NOT printed — the
// caller emits an accurate message reflecting the worktree+carryover behavior
// instead (bug-7d4b6c63). The returned plan still carries DirtyMainWarning so
// enforceLaunchPlan can use it.
func applyLaunchPlanOpts(repoRoot, workItemID string, inPlace, suppressDirtyWarning bool, w io.Writer) plan.LaunchPlan {
	m := mode.Compute("", false, false, false)
	p, err := plan.PlanLaunch(plan.Input{
		RepoRoot:    repoRoot,
		WorkItemID:  workItemID,
		RuntimeMode: m.Runtime,
		InPlace:     inPlace,
		EnforceIsolation: os.Getenv("WIPNOTE_ENFORCE_ISOLATION") == "true",
	})
	if err != nil {
		return p
	}
	if p.DirtyMainWarning != "" && !suppressDirtyWarning {
		fmt.Fprintln(w, p.DirtyMainWarning)
	}
	if os.Getenv("WIPNOTE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr,
			"wipnote [debug]: launch-plan isolation=%s worktree=%s refuse=%v\n",
			p.IsolationMode, p.PlannedWorktreePath, p.RefuseLaunch,
		)
	}
	return p
}

// enforceLaunchPlan honors the LaunchPlan returned by applyLaunchPlan. When the
// plan's RefuseLaunch flag is set (EnforceIsolation gate triggered on a dirty
// protected branch), it returns a non-nil error so the caller ABORTS the launch
// before any harness process is started. On the default host profile
// (EnforceIsolation off) RefuseLaunch is always false and this is a no-op.
//
// This closes the slice-9 gate: previously callers discarded the plan and the
// WIPNOTE_ENFORCE_ISOLATION=true guard was non-functional (launch proceeded
// in-place regardless).
func enforceLaunchPlan(p plan.LaunchPlan, w io.Writer) error {
	if !p.RefuseLaunch {
		return nil
	}
	if p.DirtyMainWarning != "" {
		fmt.Fprintln(w, p.DirtyMainWarning)
	}
	return fmt.Errorf(
		"launch refused: WIPNOTE_ENFORCE_ISOLATION=true and the protected branch is dirty.\n"+
			"  Commit or stash changes, or pass --in-place to opt out of isolation,\n"+
			"  or rerun with a work item so a managed worktree can be created.")
}

// resolveManagedWorktree honors the IsolationManagedWorktree decision in the
// plan. When the plan selects a managed worktree (devcontainer/CI, or an
// enforced host with a work item) AND no explicit worktree/track/feature
// worktree has already been resolved by the caller, it creates the managed
// worktree for the given work item and returns its path. Otherwise it returns
// the caller-supplied fallbackDir unchanged.
//
// trackID/featureID select the EnsureFor* helper; when only a bare workItemID
// is known (e.g. a bug- id) it is treated as a feature-style worktree so the
// enforced-host path still isolates mutations.
func resolveManagedWorktree(p plan.LaunchPlan, projectRoot, trackID, featureID, workItemID, fallbackDir string, alreadyResolved bool, w io.Writer) (string, error) {
	if p.IsolationMode != plan.IsolationManagedWorktree || alreadyResolved {
		return fallbackDir, nil
	}
	switch {
	case trackID != "":
		return EnsureForTrack(trackID, projectRoot, w)
	case featureID != "":
		return EnsureForFeature(featureID, projectRoot, w)
	case workItemID != "":
		return EnsureForFeature(workItemID, projectRoot, w)
	}
	return fallbackDir, nil
}

// canonicalProjectRoot returns the canonical main repo root when projectRoot is
// a linked git worktree, or "" when it is the main worktree (or not a git repo).
//
// Use this to populate WipnoteRoot / wipnoteRoot in launcher opts so that
// WIPNOTE_PROJECT_DIR always points at the canonical main repo root regardless
// of whether the user ran wipnote from inside a linked worktree. It wraps
// paths.ResolveViaGitCommonDir and adds no new identity abstraction.
//
// Callers must preserve projectRoot as the working directory for the child
// process; only WIPNOTE_PROJECT_DIR (controlled via WipnoteRoot) is changed.
func canonicalProjectRoot(projectRoot string) string {
	return paths.ResolveViaGitCommonDir(projectRoot)
}
