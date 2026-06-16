package plan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher/mode"
	"github.com/shakestzd/wipnote/internal/launcher/plan"
)

// setupGitRepo creates a temp git repo with an initial commit on "main".
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s", args, out)
		}
	}
	f, _ := os.Create(filepath.Join(dir, "README.md"))
	f.WriteString("# Test")
	f.Close()
	exec.Command("git", "-C", dir, "add", ".").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "initial").Run()
	return dir
}

func makeDirty(t *testing.T, dir string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "dirty.txt"))
	if err != nil {
		t.Fatalf("makeDirty: %v", err)
	}
	f.WriteString("dirty")
	f.Close()
}

func TestLauncherPlan_DefaultsToWorktreeOnMain(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeDevcontainer,
		InPlace:     false,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationManagedWorktree {
		t.Errorf("devcontainer+main+work-item: want IsolationManagedWorktree, got %v", p.IsolationMode)
	}
}

func TestLauncherPlan_InPlaceEscapeHatch(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeDevcontainer,
		InPlace:     true,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationExplicitInPlace {
		t.Errorf("--in-place: want IsolationExplicitInPlace, got %v", p.IsolationMode)
	}
}

func TestHostProfile_StaysWarnOnly(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeHost,
		InPlace:     false,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode == plan.IsolationManagedWorktree {
		t.Error("host profile: must NOT default to managed-worktree (HIGH critique)")
	}
	if p.RefuseLaunch {
		t.Error("host profile: must be warn-only, not refuse (HIGH critique)")
	}
}

// TestLauncherPlan_InPlaceSuppressesDirtyWarning verifies the slice-2 contract
// (roborev job 3071): --in-place must NOT emit the dirty-main warning, even on
// a dirty protected branch. The InPlace short-circuit runs before the
// dirty-main guard, so DirtyMainWarning stays empty.
func TestLauncherPlan_InPlaceSuppressesDirtyWarning(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeHost,
		InPlace:     true,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationExplicitInPlace {
		t.Errorf("--in-place: want IsolationExplicitInPlace, got %v", p.IsolationMode)
	}
	if p.DirtyMainWarning != "" {
		t.Errorf("--in-place on dirty main: want NO DirtyMainWarning, got %q", p.DirtyMainWarning)
	}
	if p.RefuseLaunch {
		t.Error("--in-place: must never RefuseLaunch (explicit opt-out)")
	}
}

// TestLauncherPlan_InPlaceNoRefuseEvenWhenEnforced verifies --in-place wins over
// EnforceIsolation: an explicit opt-out is honored and never refused/warned.
func TestLauncherPlan_InPlaceNoRefuseEvenWhenEnforced(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:         dir,
		WorkItemID:       "feat-abc12345",
		RuntimeMode:      mode.RuntimeHost,
		InPlace:          true,
		EnforceIsolation: true,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.RefuseLaunch {
		t.Error("--in-place + EnforceIsolation: explicit opt-out must not be refused")
	}
	if p.DirtyMainWarning != "" {
		t.Errorf("--in-place: want NO DirtyMainWarning, got %q", p.DirtyMainWarning)
	}
}

func TestLauncherPlan_DirtyMainWarns(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeHost,
		InPlace:     false,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.DirtyMainWarning == "" {
		t.Error("dirty main: expected non-empty DirtyMainWarning")
	}
}

func TestWorktreeLaunch_PreservesCanonicalRoot(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "feat-abc12345",
		RuntimeMode: mode.RuntimeDevcontainer,
		InPlace:     false,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationManagedWorktree {
		t.Fatalf("expected managed worktree, got %v", p.IsolationMode)
	}
	rel, err := filepath.Rel(dir, p.PlannedWorktreePath)
	if err != nil || len(rel) == 0 || strings.HasPrefix(rel, "..") {
		t.Errorf("worktree path %q is not under repoRoot %q (rel=%q, err=%v)",
			p.PlannedWorktreePath, dir, rel, err)
	}
	if p.CanonicalRoot != dir {
		t.Errorf("CanonicalRoot: want %q, got %q", dir, p.CanonicalRoot)
	}
}

// TestLauncherPlan_EnforceIsolationRefusesDirtyMain verifies the slice-9 gate:
// when EnforceIsolation is on AND the protected branch is dirty, the plan sets
// RefuseLaunch=true so callers can abort. This is the precondition the
// launcher-side enforceLaunchPlan depends on (roborev job 3091 HIGH).
func TestLauncherPlan_EnforceIsolationRefusesDirtyMain(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:         dir,
		WorkItemID:       "feat-abc12345",
		RuntimeMode:      mode.RuntimeHost,
		InPlace:          false,
		EnforceIsolation: true,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if !p.RefuseLaunch {
		t.Error("EnforceIsolation + dirty main: want RefuseLaunch=true (slice-9 gate)")
	}
	if p.DirtyMainWarning == "" {
		t.Error("EnforceIsolation + dirty main: expected DirtyMainWarning to be set")
	}
}

// TestLauncherPlan_AutoWorktreeIsolatesWithoutWorkItem verifies the "auto"
// launch_isolation mode: even with an empty WorkItemID, when AutoWorktree is set
// and the caller supplies a deterministic AdhocBranchName, PlanLaunch selects a
// managed worktree whose planned path uses the ad-hoc slug. This keeps the test
// deterministic — PlanLaunch never reads the clock.
func TestLauncherPlan_AutoWorktreeIsolatesWithoutWorkItem(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:         dir,
		WorkItemID:       "",
		RuntimeMode:      mode.RuntimeHost,
		InPlace:          false,
		EnforceIsolation: true,
		AutoWorktree:     true,
		AdhocBranchName:  "adhoc-20260616-120000",
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationManagedWorktree {
		t.Errorf("auto + no work item: want IsolationManagedWorktree, got %v", p.IsolationMode)
	}
	wantPath := filepath.Join(dir, ".claude", "worktrees", "adhoc-20260616-120000")
	if p.PlannedWorktreePath != wantPath {
		t.Errorf("auto worktree path: want %q, got %q", wantPath, p.PlannedWorktreePath)
	}
}

// TestLauncherPlan_AutoWithoutAdhocNameStaysWarnOnly verifies the caller contract:
// AutoWorktree without an AdhocBranchName cannot name a branch, so PlanLaunch
// falls back to warn-only rather than guessing a name.
func TestLauncherPlan_AutoWithoutAdhocNameStaysWarnOnly(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:     dir,
		WorkItemID:   "",
		RuntimeMode:  mode.RuntimeHost,
		AutoWorktree: true,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode == plan.IsolationManagedWorktree {
		t.Error("auto without ad-hoc name: must not select managed-worktree (cannot name a branch)")
	}
}

// TestLauncherPlan_AutoInPlaceWins verifies --in-place beats auto mode: the
// explicit opt-out short-circuits before any worktree planning.
func TestLauncherPlan_AutoInPlaceWins(t *testing.T) {
	dir := setupGitRepo(t)
	makeDirty(t, dir)

	in := plan.Input{
		RepoRoot:         dir,
		WorkItemID:       "",
		RuntimeMode:      mode.RuntimeHost,
		InPlace:          true,
		EnforceIsolation: true,
		AutoWorktree:     true,
		AdhocBranchName:  "adhoc-20260616-120000",
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode != plan.IsolationExplicitInPlace {
		t.Errorf("auto + --in-place: want IsolationExplicitInPlace, got %v", p.IsolationMode)
	}
	if p.RefuseLaunch {
		t.Error("auto + --in-place: explicit opt-out must never refuse")
	}
}

func TestLauncherPlan_NoWorkItemSkipsWorktree(t *testing.T) {
	dir := setupGitRepo(t)

	in := plan.Input{
		RepoRoot:    dir,
		WorkItemID:  "",
		RuntimeMode: mode.RuntimeDevcontainer,
		InPlace:     false,
	}
	p, err := plan.PlanLaunch(in)
	if err != nil {
		t.Fatalf("PlanLaunch: %v", err)
	}
	if p.IsolationMode == plan.IsolationManagedWorktree {
		t.Error("no work-item: must not select managed-worktree without an ID")
	}
}
