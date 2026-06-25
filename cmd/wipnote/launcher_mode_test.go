package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/internal/launcher/plan"
)

// TestEnforceLaunchPlan_RefusesWhenRefuseLaunch verifies the slice-9 gate is
// now functional (roborev job 3091 HIGH): when the plan has RefuseLaunch set,
// enforceLaunchPlan returns a non-nil error so the launcher ABORTS before any
// harness process is started. Previously callers discarded the plan and the
// WIPNOTE_ENFORCE_ISOLATION=true guard had no effect.
func TestEnforceLaunchPlan_RefusesWhenRefuseLaunch(t *testing.T) {
	p := plan.LaunchPlan{
		RefuseLaunch:     true,
		DirtyMainWarning: "Warning: launching on dirty protected branch \"main\".",
	}
	var buf bytes.Buffer
	err := enforceLaunchPlan(p, &buf)
	if err == nil {
		t.Fatal("RefuseLaunch=true: enforceLaunchPlan must return an error to abort the launch")
	}
	if !strings.Contains(err.Error(), "launch refused") {
		t.Errorf("error message should explain the refusal, got %q", err.Error())
	}
	if !strings.Contains(buf.String(), "protected branch is dirty") {
		t.Errorf("refuse advisory should be echoed before aborting, got %q", buf.String())
	}
}

// TestEnforceLaunchPlan_NoopOnDefaultHost verifies the host default stays
// warn-only: when RefuseLaunch is false (EnforceIsolation off), enforceLaunchPlan
// is a no-op and the launch proceeds. This guards against accidentally flipping
// host defaults to hard-refusal.
func TestEnforceLaunchPlan_NoopOnDefaultHost(t *testing.T) {
	p := plan.LaunchPlan{
		IsolationMode:    plan.IsolationWarnOnly,
		RefuseLaunch:     false,
		DirtyMainWarning: "Warning: launching on dirty protected branch \"main\".",
	}
	var buf bytes.Buffer
	if err := enforceLaunchPlan(p, &buf); err != nil {
		t.Fatalf("RefuseLaunch=false: enforceLaunchPlan must be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("no-op path must not write anything, got %q", buf.String())
	}
}

// TestResolveManagedWorktree_FallbackWhenNotManaged verifies resolveManagedWorktree
// returns the caller's fallback dir unchanged when the plan does NOT select a
// managed worktree, and when an explicit/track/feature worktree was already
// resolved by the caller (alreadyResolved=true) - so the existing
// explicit/track/feature paths are never double-created.
func TestResolveManagedWorktree_FallbackWhenNotManaged(t *testing.T) {
	fallback := "/tmp/project-root"

	p := plan.LaunchPlan{IsolationMode: plan.IsolationWarnOnly}
	got, err := resolveManagedWorktree(p, "/repo", "", "", "bug-12345678", fallback, false, io.Discard)
	if err != nil {
		t.Fatalf("resolveManagedWorktree: %v", err)
	}
	if got != fallback {
		t.Errorf("warn-only plan: want fallback %q, got %q", fallback, got)
	}

	pm := plan.LaunchPlan{IsolationMode: plan.IsolationManagedWorktree}
	got2, err := resolveManagedWorktree(pm, "/repo", "", "", "bug-12345678", fallback, true, io.Discard)
	if err != nil {
		t.Fatalf("resolveManagedWorktree (alreadyResolved): %v", err)
	}
	if got2 != fallback {
		t.Errorf("alreadyResolved: want fallback %q, got %q", fallback, got2)
	}
}

// TestResolveManagedWorktree_NoWorkItemReturnsFallback verifies that a managed
// plan with no track/feature/workItem identifiers cannot name a worktree and so
// returns the fallback dir rather than attempting creation.
func TestResolveManagedWorktree_NoWorkItemReturnsFallback(t *testing.T) {
	fallback := "/tmp/project-root"
	pm := plan.LaunchPlan{IsolationMode: plan.IsolationManagedWorktree}
	got, err := resolveManagedWorktree(pm, "/repo", "", "", "", fallback, false, io.Discard)
	if err != nil {
		t.Fatalf("resolveManagedWorktree: %v", err)
	}
	if got != fallback {
		t.Errorf("no work item: want fallback %q, got %q", fallback, got)
	}
}

// TestIsLauncherDiagnosticSubtree verifies that `wipnote launcher doctor` (and
// the launcher subtree generally) is recognized as a read-only diagnostic
// command so persistentPreRunE skips its destructive side-effects
// (roborev job 3091 MEDIUM, main.go).
func TestIsLauncherDiagnosticSubtree(t *testing.T) {
	launcher := launcherCmd()
	if !isLauncherDiagnosticSubtree(launcher) {
		t.Error("launcher command itself should be in the diagnostic subtree")
	}

	var doctor *cobra.Command
	for _, c := range launcher.Commands() {
		if c.Name() == "doctor" {
			doctor = c
			break
		}
	}
	if doctor == nil {
		t.Fatal("expected `launcher doctor` subcommand to exist")
	}
	if !isLauncherDiagnosticSubtree(doctor) {
		t.Error("`launcher doctor` must be recognized as a launcher diagnostic subtree (skip persistentPreRunE)")
	}

	other := versionCmd()
	if isLauncherDiagnosticSubtree(other) {
		t.Error("version command must not be treated as a launcher diagnostic subtree")
	}
}
