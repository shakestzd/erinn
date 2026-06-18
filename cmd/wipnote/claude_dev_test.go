package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/launcher"
)

func execCapableBase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("GOTMPDIR")
	if base == "" {
		base = os.Getenv("TMPDIR")
	}
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "wipnote-gotmp-")
	if err != nil {
		t.Fatalf("mkdir exec-capable tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func execCapableTempDir(t *testing.T) string {
	t.Helper()
	base := execCapableBase(t)
	t.Setenv("TMPDIR", base)
	return t.TempDir()
}

func TestRequireWipnoteOnPathAcceptsWipnoteBinary(t *testing.T) {
	binDir := execCapableTempDir(t)
	wipnotePath := filepath.Join(binDir, "wipnote")
	if err := os.WriteFile(wipnotePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake wipnote: %v", err)
	}
	t.Setenv("PATH", binDir)

	if err := requireWipnoteOnPath(); err != nil {
		t.Fatalf("requireWipnoteOnPath() error = %v, want nil", err)
	}
}

func TestRequireWipnoteOnPathRejectsOnlyLegacyBinary(t *testing.T) {
	binDir := execCapableTempDir(t)
	// Legacy binary name is intentionally insufficient after the wipnote rename.
	legacyPath := filepath.Join(binDir, "htmlgraph")
	if err := os.WriteFile(legacyPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake legacy binary: %v", err)
	}
	t.Setenv("PATH", binDir)

	err := requireWipnoteOnPath()
	if err == nil {
		t.Fatal("requireWipnoteOnPath() error = nil, want missing wipnote error")
	}
	if got := err.Error(); !strings.Contains(got, "wipnote binary not found on PATH") {
		t.Fatalf("error = %q, want wipnote PATH guidance", got)
	}
}

// makeDevProject creates a minimal fake project with .wipnote/ and plugin/
// so that resolveProjectPluginDirFrom and findWipnoteDir succeed in tests.
func makeDevProject(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	// .wipnote/ marks the project root
	if err := os.MkdirAll(filepath.Join(tmpDir, ".wipnote"), 0755); err != nil {
		t.Fatalf("makeDevProject: mkdir .wipnote: %v", err)
	}
	// plugin/.claude-plugin/plugin.json required by devLaunchPluginDir
	pluginJSON := filepath.Join(tmpDir, "plugin", ".claude-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(pluginJSON), 0755); err != nil {
		t.Fatalf("makeDevProject: mkdir plugin: %v", err)
	}
	if err := os.WriteFile(pluginJSON, []byte(`{"name":"wipnote","version":"0.1.0"}`), 0644); err != nil {
		t.Fatalf("makeDevProject: write plugin.json: %v", err)
	}
	return tmpDir
}

// makeGitDevProject creates a minimal fake project with .wipnote/, plugin/, and
// git initialization so that git worktree operations succeed in tests.
// This is required for tests that call resolveClaudeIntentIsolation with a --work-item,
// which needs a git repository with at least one commit.
func makeGitDevProject(t *testing.T) string {
	t.Helper()
	projectRoot := makeDevProject(t)

	// Initialize git repo
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makeGitDevProject: git init: %v\n%s", err, out)
	}

	// Configure git user for commit
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("makeGitDevProject: git config user.email: %v", err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("makeGitDevProject: git config user.name: %v", err)
	}

	// Create initial commit (git worktree add requires a commit/HEAD)
	readmeFile := filepath.Join(projectRoot, "README")
	if err := os.WriteFile(readmeFile, []byte("wipnote test\n"), 0644); err != nil {
		t.Fatalf("makeGitDevProject: write README: %v", err)
	}

	cmd = exec.Command("git", "add", "README")
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makeGitDevProject: git add: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "commit", "-m", "init")
	cmd.Dir = projectRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("makeGitDevProject: git commit: %v\n%s", err, out)
	}

	return projectRoot
}

// TestDevLaunchPluginDir_ReturnsPluginFromSourceRoot verifies that devLaunchPluginDir
// returns the plugin/ directory under the supplied sourceRoot.
func TestDevLaunchPluginDir_ReturnsPluginFromSourceRoot(t *testing.T) {
	root := makeDevProject(t)
	pluginDir, err := devLaunchPluginDir(root)
	if err != nil {
		t.Fatalf("devLaunchPluginDir() error = %v", err)
	}
	wantSuffix := filepath.Join(root, "plugin")
	if !strings.HasSuffix(pluginDir, "plugin") || !strings.HasPrefix(pluginDir, root) {
		t.Fatalf("devLaunchPluginDir() = %q, want path under %q ending in 'plugin'", pluginDir, root)
	}
	_ = wantSuffix
}

// TestDevLaunchPluginDir_ErrorWhenNoPlugin verifies devLaunchPluginDir fails
// gracefully when no plugin/ structure can be found.
func TestDevLaunchPluginDir_ErrorWhenNoPlugin(t *testing.T) {
	emptyDir := t.TempDir()
	// Create .wipnote but no plugin/
	if err := os.MkdirAll(filepath.Join(emptyDir, ".wipnote"), 0755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	_, err := devLaunchPluginDir(emptyDir)
	if err == nil {
		t.Fatal("devLaunchPluginDir() error = nil, want error when plugin/ absent")
	}
	if !strings.Contains(err.Error(), "could not find plugin/ directory") {
		t.Fatalf("error = %q, want 'could not find plugin/ directory'", err.Error())
	}
}

// TestResolveProjectPluginDirFrom_FindsPluginFromRoot verifies that the
// from-path variant finds the correct plugin dir without CWD dependency.
func TestResolveProjectPluginDirFrom_FindsPluginFromRoot(t *testing.T) {
	root := makeDevProject(t)
	got := resolveProjectPluginDirFrom(root)
	want := filepath.Join(root, "plugin")
	if got != want {
		t.Fatalf("resolveProjectPluginDirFrom(%q) = %q, want %q", root, got, want)
	}
}

// TestResolveProjectPluginDirFrom_ReturnsEmptyWhenAbsent verifies graceful
// fallback when no plugin structure exists under startDir.
func TestResolveProjectPluginDirFrom_ReturnsEmptyWhenAbsent(t *testing.T) {
	got := resolveProjectPluginDirFrom(t.TempDir())
	if got != "" {
		t.Fatalf("resolveProjectPluginDirFrom() = %q, want empty string", got)
	}
}

// TestDevLaunchChooserSuppression_NonTTY verifies that resolveClaudeIntentIsolation
// suppresses the chooser on non-TTY (CI / piped stdin) and returns NewWorkIntent.
// This mirrors the invariant for launchClaudeDefault — dev mode must honour the
// same TTY/CI gate.
func TestDevLaunchChooserSuppression_NonTTY(t *testing.T) {
	// Swap chooseLaunchIntentFn so we can detect if it was called.
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	called := false
	chooseLaunchIntentFn = func(projectRoot, canonicalRoot, harness string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
		called = true
		return launcher.ContinueWorkIntent("feat-picked", harness, "sess-picked", "", true), nil
	}

	root := makeDevProject(t)
	// CI=true forces non-interactive path (TTY detection also false in tests)
	t.Setenv("CI", "true")

	lctx, err := resolveClaudeIntentIsolation(root, "", "", "", false, false, nil)
	if err != nil {
		t.Fatalf("resolveClaudeIntentIsolation() error = %v", err)
	}
	if called {
		t.Fatal("chooser was called on non-TTY/CI launch — should be suppressed")
	}
	if lctx.intentResult.intent.Kind != launcher.LaunchIntentNew {
		t.Fatalf("intent.Kind = %q, want %q (NewWork for suppressed chooser)", lctx.intentResult.intent.Kind, launcher.LaunchIntentNew)
	}
}

// TestDevLaunchChooserSuppression_WithWorkItem verifies that providing --work-item
// suppresses the chooser (explicit intent, no interactive prompt needed).
// It uses makeGitDevProject to set up a real git repository so that git worktree
// operations succeed.
func TestDevLaunchChooserSuppression_WithWorkItem(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	called := false
	chooseLaunchIntentFn = func(_, _, _ string, _ io.Reader, _ io.Writer) (launcher.LaunchIntent, error) {
		called = true
		return launcher.NewWorkIntent(), nil
	}

	root := makeGitDevProject(t)
	t.Setenv("CI", "false")

	lctx, err := resolveClaudeIntentIsolation(root, "", "", "feat-abc123", false, false, nil)
	if err != nil {
		t.Fatalf("resolveClaudeIntentIsolation() error = %v", err)
	}
	if called {
		t.Fatal("chooser was called with explicit --work-item — should be suppressed")
	}
	// workItem is reflected in the result via intentResult.workItem
	_ = lctx
}

// TestDevLaunchChooserSuppression_WithResumeID verifies that --resume suppresses
// the chooser (explicit session, no interactive prompt needed).
func TestDevLaunchChooserSuppression_WithResumeID(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	called := false
	chooseLaunchIntentFn = func(_, _, _ string, _ io.Reader, _ io.Writer) (launcher.LaunchIntent, error) {
		called = true
		return launcher.NewWorkIntent(), nil
	}

	root := makeDevProject(t)
	t.Setenv("CI", "false")

	_, err := resolveClaudeIntentIsolation(root, "", "sess-abc123", "", false, false, nil)
	if err != nil {
		t.Fatalf("resolveClaudeIntentIsolation() error = %v", err)
	}
	if called {
		t.Fatal("chooser was called with explicit --resume — should be suppressed")
	}
}

// TestDevLaunchChooserSuppression_InPlace verifies that --in-place suppresses the
// chooser (user has declared explicit in-place intent, no worktree selection needed).
func TestDevLaunchChooserSuppression_InPlace(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	called := false
	chooseLaunchIntentFn = func(_, _, _ string, _ io.Reader, _ io.Writer) (launcher.LaunchIntent, error) {
		called = true
		return launcher.NewWorkIntent(), nil
	}

	root := makeDevProject(t)
	t.Setenv("CI", "false")

	_, err := resolveClaudeIntentIsolation(root, "", "", "", true /*inPlace*/, false, nil)
	if err != nil {
		t.Fatalf("resolveClaudeIntentIsolation() error = %v", err)
	}
	if called {
		t.Fatal("chooser was called with --in-place — should be suppressed")
	}
}

// TestDevLaunchChooserSuppression_ExplicitContinue verifies that --dev --continue
// suppresses the interactive chooser (the explicitContinue arg flows into
// chooserEligibility.ExplicitContinue). Regression guard for bug-da10ac25 C1:
// previously `case dev:` short-circuited before `case continue_:`, so --continue
// was silently dropped in dev mode.
func TestDevLaunchChooserSuppression_ExplicitContinue(t *testing.T) {
	original := chooseLaunchIntentFn
	defer func() { chooseLaunchIntentFn = original }()

	called := false
	chooseLaunchIntentFn = func(_, _, _ string, _ io.Reader, _ io.Writer) (launcher.LaunchIntent, error) {
		called = true
		return launcher.NewWorkIntent(), nil
	}

	root := makeDevProject(t)
	t.Setenv("CI", "false")

	_, err := resolveClaudeIntentIsolation(root, "", "", "", false, true /*explicitContinue*/, nil)
	if err != nil {
		t.Fatalf("resolveClaudeIntentIsolation() error = %v", err)
	}
	if called {
		t.Fatal("chooser was called with --dev --continue — should be suppressed by explicitContinue")
	}
}

// TestShouldOfferLaunchIntentChooser_ExplicitContinueSuppressed verifies the
// eligibility gate treats ExplicitContinue as a chooser suppressor, matching the
// other harness launchers (codex/gemini/antigravity).
func TestShouldOfferLaunchIntentChooser_ExplicitContinueSuppressed(t *testing.T) {
	if shouldOfferLaunchIntentChooser(chooserEligibility{TTY: true, ExplicitContinue: true}) {
		t.Fatal("shouldOfferLaunchIntentChooser(ExplicitContinue) = true, want false")
	}
}

// TestShouldOfferLaunchIntentChooser_DevSuppressionsMatchDefault verifies that
// the suppression gates checked by shouldOfferLaunchIntentChooser apply uniformly
// to both the default and dev launchers (since they both call resolveClaudeIntentIsolation
// which feeds the same chooserEligibility).
func TestShouldOfferLaunchIntentChooser_DevSuppressionsMatchDefault(t *testing.T) {
	tests := []struct {
		name     string
		opts     chooserEligibility
		wantShow bool
	}{
		{
			name:     "dev non-tty suppressed",
			opts:     chooserEligibility{TTY: false},
			wantShow: false,
		},
		{
			name:     "dev ci suppressed",
			opts:     chooserEligibility{TTY: true, CI: true},
			wantShow: false,
		},
		{
			name:     "dev resume suppressed",
			opts:     chooserEligibility{TTY: true, ResumeID: "sess-x"},
			wantShow: false,
		},
		{
			name:     "dev work-item suppressed",
			opts:     chooserEligibility{TTY: true, WorkItem: "feat-x"},
			wantShow: false,
		},
		{
			name:     "dev in-place suppressed",
			opts:     chooserEligibility{TTY: true, InPlace: true},
			wantShow: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldOfferLaunchIntentChooser(tt.opts)
			if got != tt.wantShow {
				t.Fatalf("shouldOfferLaunchIntentChooser() = %v, want %v", got, tt.wantShow)
			}
		})
	}
}
