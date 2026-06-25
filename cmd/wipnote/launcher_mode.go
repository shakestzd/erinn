package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/worktree"
	"github.com/shakestzd/wipnote/internal/launcher/mode"
	"github.com/shakestzd/wipnote/internal/launcher/plan"
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

// applyLaunchPlanOpts computes the LaunchPlan for a launch. It NO LONGER prints
// the dirty-main advisory as its own separate block — the warning now flows into
// the single boxless launch banner emitted by each launcher (bug-0f6af202).
//
// The returned plan ALWAYS carries DirtyMainWarning when the protected branch is
// dirty. Two consumers share it: emitWorktreeCarryoverMessage (when a worktree is
// created) and the caller's launch banner (when launching in-place). Use
// bannerDirtyWarning to pick the right one — it returns the advisory only when no
// worktree will be created, so exactly one surface shows it and nothing
// double-prints. The suppressDirtyWarning parameter is retained for
// source-compatibility (callers pass willCreateWorktree) but no longer gates an
// inline print.
//
// canonicalRoot must be the canonical main repository root (from canonicalProjectRoot),
// not a linked worktree path. It is used to read the launch_isolation config from
// the main repo's .wipnote/ directory.
func applyLaunchPlanOpts(canonicalRoot, repoRoot, workItemID string, inPlace, suppressDirtyWarning bool, w io.Writer) plan.LaunchPlan {
	m := mode.Compute("", false, false, false)

	// Resolve effective isolation from .wipnote/config.json ("launch_isolation")
	// ORed with the legacy WIPNOTE_ENFORCE_ISOLATION env var. Absent config keeps
	// today's warn-only behavior (backward compatible).
	// Always read from the canonical root so the project-level config is honored
	// even when called from within an isolated worktree.
	enforce, autoWorktree := resolveIsolationFlags(canonicalRoot)

	// In auto mode with no work item, supply a deterministic ad-hoc branch slug so
	// PlanLaunch can plan a managed worktree without reading the clock itself.
	var adhoc string
	if autoWorktree && workItemID == "" && !inPlace {
		adhoc = adhocBranchName(time.Now())
	}

	p, err := plan.PlanLaunch(plan.Input{
		RepoRoot:         repoRoot,
		WorkItemID:       workItemID,
		RuntimeMode:      m.Runtime,
		InPlace:          inPlace,
		EnforceIsolation: enforce,
		AutoWorktree:     autoWorktree,
		AdhocBranchName:  adhoc,
	})
	if err != nil {
		return p
	}
	// suppressDirtyWarning is retained for source-compatibility with callers that
	// pass willCreateWorktree; it no longer gates an inline print (there is none).
	// The returned plan ALWAYS carries DirtyMainWarning when the protected branch
	// is dirty so both emitWorktreeCarryoverMessage (worktree path) and the
	// caller's launch banner (in-place path) can consume it. Callers decide which
	// surface shows it via bannerDirtyWarning(plan, willCreateWorktree).
	_ = suppressDirtyWarning
	if os.Getenv("WIPNOTE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr,
			"wipnote [debug]: launch-plan isolation=%s worktree=%s refuse=%v\n",
			p.IsolationMode, p.PlannedWorktreePath, p.RefuseLaunch,
		)
	}
	return p
}

// bannerDirtyWarning returns the dirty-main advisory to fold into a launcher's
// single boxless launch banner, or "" when none should be shown. The advisory is
// shown ONLY when no managed worktree will be created this launch: when a
// worktree IS created, emitWorktreeCarryoverMessage prints an accurate
// worktree+carryover advisory instead, so the banner must stay silent to avoid
// double-messaging (bug-0f6af202 / bug-7d4b6c63).
func bannerDirtyWarning(p plan.LaunchPlan, willCreateWorktree bool) string {
	if willCreateWorktree {
		return ""
	}
	return p.DirtyMainWarning
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
	refuseMsg := "launch refused: protected branch is dirty — pass --work-item <id> to isolate, --in-place to opt out, or commit your changes"
	fmt.Fprintln(w, launchtui.RenderLaunchBanner(nil, launchtui.BannerInput{
		Warning:         refuseMsg,
		WarningSeverity: "red",
	}))
	return fmt.Errorf("%s", refuseMsg)
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
	path, _, err := resolveManagedWorktreeStatus(p, projectRoot, trackID, featureID, workItemID, fallbackDir, alreadyResolved, w)
	return path, err
}

// resolveManagedWorktreeStatus is resolveManagedWorktree plus a "created vs
// reused" signal. created is true only when a NEW worktree was created on disk
// this call. Callers that need to gate carryover on new-worktree creation (e.g.
// launchClaudeDefault) use this variant; callers that only need the path use
// resolveManagedWorktree.
func resolveManagedWorktreeStatus(p plan.LaunchPlan, projectRoot, trackID, featureID, workItemID, fallbackDir string, alreadyResolved bool, w io.Writer) (string, bool, error) {
	if p.IsolationMode != plan.IsolationManagedWorktree || alreadyResolved {
		return fallbackDir, false, nil
	}
	switch {
	case trackID != "":
		path, created, err := worktree.EnsureForTrackStatus(trackID, projectRoot, w)
		return path, created, err
	case featureID != "":
		path, created, err := worktree.EnsureForFeatureStatus(featureID, projectRoot, w)
		return path, created, err
	case workItemID != "":
		path, created, err := worktree.EnsureForFeatureStatus(workItemID, projectRoot, w)
		return path, created, err
	case p.PlannedWorktreePath != "":
		// Auto mode with no work item: the plan named an ad-hoc worktree (its
		// directory basename is the deterministic adhoc slug). Create it under
		// .claude/worktrees/<slug> consistent with plannedWorktreePath.
		slug := filepath.Base(p.PlannedWorktreePath)
		path, created, err := worktree.EnsureForAdhocStatus(slug, projectRoot, w)
		return path, created, err
	}
	return fallbackDir, false, nil
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

// effectiveWorkItemID returns the first non-empty ID from workItem, trackID,
// featureID. This is the ID passed to applyLaunchPlanOpts so the isolation
// planner knows a work item exists even when only --track/--feature was set
// (workItem is empty in that case). Without this, EnforceIsolation=true would
// fire a false RefuseLaunch before the track/feature worktree is resolved.
func effectiveWorkItemID(workItem, trackID, featureID string) string {
	if workItem != "" {
		return workItem
	}
	if trackID != "" {
		return trackID
	}
	return featureID
}
