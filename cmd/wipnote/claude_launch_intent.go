package main

import (
	"fmt"
	"os"
)

// claudeLaunchContext holds the resolved intent, isolation, and routing
// state shared between launchClaudeDefault and launchClaudeDev.
type claudeLaunchContext struct {
	// intentResult captures the chooser outcome (mode, resumeID, workItem, intent).
	intentResult claudeIntentResult
	// continueEnv holds extra env vars for continue handoff (base64 handoff,
	// continued-from session ID).
	continueEnv []string
	// childDir is the directory Claude Code should run in (worktree or projectRoot).
	childDir string
	// wipnoteRoot is the canonical main-repo root when childDir is a worktree.
	// Empty when running in-place (no worktree).
	wipnoteRoot string
}

// resolveClaudeIntentIsolation runs the full intent-chooser → isolation-planner
// pipeline shared between launchClaudeDefault and launchClaudeDev. Callers
// supply the already-resolved projectRoot and wipnoteRoot so they can each do
// their own preliminary setup (cleanupStaleDev, removeMarketplaceWipnote, etc.)
// before calling this.
//
// Parameters:
//   - projectRoot: absolute path to the project containing .wipnote/
//   - wipnoteRoot: canonical main-repo root (empty when projectRoot IS the main repo)
//   - resumeID, workItem, inPlace, explicitContinue, extraArgs: flags from the CLI
//
// explicitContinue is true when the caller passed --continue ("resume most
// recent"); it suppresses the interactive chooser. The caller is responsible
// for honoring the continue semantics (e.g. Resume: true on the launch opts).
//
// The function prints warnings and carryover messages to os.Stderr/os.Stdout.
func resolveClaudeIntentIsolation(projectRoot, wipnoteRoot, resumeID, workItem string, inPlace, explicitContinue bool, extraArgs []string) (claudeLaunchContext, error) {
	intent, err := resolveLaunchIntentForDefaultLaunch(projectRoot, wipnoteRoot, "claude", chooserEligibility{
		TTY:              isInteractiveTerminalFile(os.Stdin) && isInteractiveTerminalFile(os.Stdout),
		CI:               os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") != "",
		ResumeID:         resumeID,
		WorkItem:         workItem,
		InPlace:          inPlace,
		ExplicitContinue: explicitContinue,
		ExtraArgs:        extraArgs,
	}, os.Stdin, os.Stdout)
	if err != nil {
		return claudeLaunchContext{}, err
	}

	intentResult := applyClaudeLaunchIntent(resumeID, workItem, intent)
	resumeID = intentResult.resumeID
	workItem = intentResult.workItem

	continueCtx, err := resolveContinueLaunchContext(projectRoot, wipnoteRoot, "claude", intentResult.intent)
	if err != nil {
		return claudeLaunchContext{}, err
	}
	for _, warning := range continueCtx.Warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
	if workItem == "" && continueCtx.WorkItemID != "" {
		workItem = continueCtx.WorkItemID
		intentResult.workItem = workItem
	}
	if resumeID == "" && continueCtx.TranscriptResumeID != "" {
		resumeID = continueCtx.TranscriptResumeID
		intentResult.resumeID = resumeID
	}

	// Isolation planner: compute the launch plan, enforce it (RefuseLaunch
	// aborts here), and resolve the managed worktree when applicable.
	willCreateWorktree := !inPlace && workItem != ""
	configRoot := wipnoteRoot
	if configRoot == "" {
		configRoot = projectRoot
	}
	launchPlan := applyLaunchPlanOpts(configRoot, projectRoot, workItem, inPlace, willCreateWorktree, os.Stderr)
	if err := enforceLaunchPlan(launchPlan, os.Stderr); err != nil {
		return claudeLaunchContext{}, err
	}

	childDir := projectRoot
	resolved := false
	worktreeCreated := false
	if continueCtx.WorktreePath != "" {
		childDir = continueCtx.WorktreePath
		resolved = true
		if wipnoteRoot == "" {
			wipnoteRoot = projectRoot
		}
	}
	if wt, created, werr := resolveManagedWorktreeStatus(launchPlan, projectRoot, "", "", workItem, childDir, resolved, os.Stdout); werr != nil {
		return claudeLaunchContext{}, werr
	} else if wt != "" && wt != projectRoot {
		childDir = wt
		worktreeCreated = created
		if wipnoteRoot == "" {
			wipnoteRoot = projectRoot
		}
	}

	if childDir != projectRoot {
		effectiveRoot := projectRoot
		if wipnoteRoot != "" {
			effectiveRoot = wipnoteRoot
		}
		emitWorktreeCarryoverMessage(launchPlan, effectiveRoot, childDir, worktreeCreated, os.Stdout)
	}

	return claudeLaunchContext{
		intentResult: intentResult,
		continueEnv:  continueCtx.ExtraEnv(),
		childDir:     childDir,
		wipnoteRoot:  wipnoteRoot,
	}, nil
}

// devLaunchPluginDir resolves the in-tree plugin/ directory for dev mode.
// It always walks up from the canonical source root (projectRoot or wipnoteRoot
// when set), NOT from the worktree childDir, because the plugin source lives in
// the wipnote source tree — not in the linked worktree.
//
// This preserves the invariant that --dev always tests the in-tree plugin/
// regardless of whether a worktree was selected for isolation.
func devLaunchPluginDir(sourceRoot string) (string, error) {
	// resolveProjectPluginDir walks up from CWD. Override CWD behaviour by
	// temporarily changing the working directory concept via a direct path lookup.
	// We replicate the same logic: find plugin/.claude-plugin/plugin.json relative
	// to sourceRoot.
	pluginDir := resolveProjectPluginDirFrom(sourceRoot)
	if pluginDir == "" {
		return "", fmt.Errorf("could not find plugin/ directory relative to project root %s. Run from the project directory containing .wipnote/ and plugin/", sourceRoot)
	}
	return pluginDir, nil
}
