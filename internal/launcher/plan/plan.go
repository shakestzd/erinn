// Package plan computes an isolation plan for wipnote launchers (claude, codex,
// gemini, yolo) before any harness process is started.
//
// PlanLaunch is a pure-planning function: it reads git state and the caller's
// flags, then returns a LaunchPlan describing which isolation strategy to use.
// It never creates worktrees, starts processes, or writes files.
//
// Rollout rules (HIGH critique constraints):
//   - RuntimeHost → warn-only; isolation is NEVER forced (RefuseLaunch=false).
//   - RuntimeDevcontainer → managed-worktree when a WorkItemID is provided.
//   - --in-place (InPlace=true) → IsolationExplicitInPlace regardless of runtime.
//   - No WorkItemID → cannot name a branch; falls back to warn-only in-place.
package plan

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/launcher/mode"
)

// IsolationMode describes the isolation decision for a launch.
type IsolationMode string

const (
	// IsolationManagedWorktree plans to launch inside a managed git worktree.
	IsolationManagedWorktree IsolationMode = "managed-worktree"
	// IsolationWarnOnly plans to launch in-place but emits a recommendation warning.
	IsolationWarnOnly IsolationMode = "warn-only"
	// IsolationExplicitInPlace records that --in-place was passed; no warning.
	IsolationExplicitInPlace IsolationMode = "explicit-in-place"
)

// Input bundles the caller-supplied parameters for PlanLaunch.
type Input struct {
	// RepoRoot is the absolute path to the git repository root.
	RepoRoot string
	// WorkItemID is the harness-neutral work-item identifier (feat-*, bug-*, trk-*).
	// Empty means "no work item"; managed-worktree will not be selected.
	WorkItemID string
	// RuntimeMode is the slice-1 runtime classification (host/devcontainer/ci).
	RuntimeMode mode.RuntimeMode
	// InPlace, when true, is the --in-place flag: caller explicitly opts out of
	// worktree isolation. Records IsolationExplicitInPlace.
	InPlace bool
	// BaseBranch, if set, is the branch the worktree should fork from.
	// Empty defaults to the current HEAD branch.
	BaseBranch string
	// EnforceIsolation, when true, upgrades the host warn-only guard to a hard
	// refusal. Exposed via the .wipnote/config.json "launch_isolation" key
	// ("enforce"/"auto") and the WIPNOTE_ENFORCE_ISOLATION=true env var.
	EnforceIsolation bool
	// AdhocBranchName, when set, supplies a deterministic worktree/branch slug to
	// use when there is no WorkItemID but isolation is still desired (auto mode).
	// PlanLaunch stays pure: the CALLER computes this name (e.g. an
	// "adhoc-<UTC timestamp>" slug) and passes it in, so PlanLaunch never reads
	// the clock. When set and AutoWorktree is true, an IsolationManagedWorktree is
	// planned even with an empty WorkItemID.
	AdhocBranchName string
	// AutoWorktree, when true, requests isolation even with NO WorkItemID: a bare
	// `wipnote claude` will still plan a managed (ad-hoc) worktree. This maps to
	// the "auto" launch_isolation config value. Implies EnforceIsolation.
	AutoWorktree bool
}

// LaunchPlan is the computed isolation decision returned by PlanLaunch.
type LaunchPlan struct {
	// IsolationMode is the selected strategy.
	IsolationMode IsolationMode
	// PlannedWorktreePath is the path where the worktree will be created.
	// Only meaningful when IsolationMode == IsolationManagedWorktree.
	PlannedWorktreePath string
	// CanonicalRoot is always the original repo root (not the worktree).
	// Callers should inject this as WIPNOTE_PROJECT_DIR.
	CanonicalRoot string
	// DirtyMainWarning is non-empty when the repo has uncommitted changes on
	// a protected branch (main/master). Always a warning; never a blocker on
	// host unless EnforceIsolation is set.
	DirtyMainWarning string
	// RefuseLaunch is true only when EnforceIsolation is set AND the guard
	// triggers. Default host behaviour: always false (warn-only).
	RefuseLaunch bool
}

// PlanLaunch computes a LaunchPlan from the given Input.
// It reads git state but never mutates anything.
func PlanLaunch(in Input) (LaunchPlan, error) {
	p := LaunchPlan{
		CanonicalRoot: in.RepoRoot,
	}

	// --in-place explicitly overrides everything, including the dirty-main
	// guard. applyLaunchPlan documents that inPlace=true => no warning, so the
	// InPlace short-circuit must run BEFORE any DirtyMainWarning is computed.
	if in.InPlace {
		p.IsolationMode = IsolationExplicitInPlace
		return p, nil
	}

	// Determine the current branch for dirty-main guard.
	branch := currentBranch(in.RepoRoot)
	dirty := isProtectedAndDirty(in.RepoRoot, branch)

	// Dirty-main guard: warn (or refuse when enforcement is on).
	if dirty {
		p.DirtyMainWarning = fmt.Sprintf(
			"Warning: launching on dirty protected branch %q.\n"+
				"  Uncommitted changes detected in %s.\n"+
				"  Recommendation: use a managed worktree (--work-item <id>) to isolate mutations.",
			branch, in.RepoRoot,
		)
		if in.EnforceIsolation {
			p.RefuseLaunch = true
		}
	}

	// Without a work-item ID we normally cannot create a deterministically-named
	// branch. Auto mode lifts this: the caller supplies an ad-hoc branch slug so a
	// bare session (no work item) still isolates into a managed worktree.
	if in.WorkItemID == "" {
		if in.AutoWorktree && in.AdhocBranchName != "" {
			p.IsolationMode = IsolationManagedWorktree
			p.PlannedWorktreePath = plannedWorktreePath(in.RepoRoot, in.AdhocBranchName)
			return p, nil
		}
		p.IsolationMode = IsolationWarnOnly
		return p, nil
	}

	// Host runtime: warn-only unless EnforceIsolation is set (locked off by default).
	if in.RuntimeMode == mode.RuntimeHost && !in.EnforceIsolation {
		p.IsolationMode = IsolationWarnOnly
		return p, nil
	}

	// Devcontainer / CI / enforced host: plan a managed worktree.
	p.IsolationMode = IsolationManagedWorktree
	p.PlannedWorktreePath = plannedWorktreePath(in.RepoRoot, in.WorkItemID)
	return p, nil
}

// plannedWorktreePath returns the path where a managed worktree for the given
// work-item ID will be created. Mirrors the yolo/worktree branch-naming scheme:
// .claude/worktrees/<workItemID>.
func plannedWorktreePath(repoRoot, workItemID string) string {
	return filepath.Join(repoRoot, ".claude", "worktrees", workItemID)
}

// currentBranch returns the current git branch name for the given repo root.
// Returns empty string if the branch cannot be determined.
func currentBranch(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isProtectedAndDirty returns true when branch is "main" or "master" and the
// working tree has uncommitted changes (tracked or untracked files).
func isProtectedAndDirty(repoRoot, branch string) bool {
	if branch != "main" && branch != "master" {
		return false
	}
	out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}
