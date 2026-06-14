package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/shakestzd/wipnote/internal/launcher"
	"github.com/shakestzd/wipnote/observe/otel/collector"
)

// antigravityLaunchOpts controls how the Antigravity CLI is launched.
type antigravityLaunchOpts struct {
	// ExtraArgs are forwarded to the agy process.
	ExtraArgs []string
	// ProjectRoot is the absolute path to the project root (or worktree path).
	ProjectRoot string
	// WorktreeRoot, when non-empty, overrides the working directory for the
	// agy process.
	WorktreeRoot string
	// WipnoteRoot is the canonical project root containing .wipnote/.
	WipnoteRoot string
	// DryRun, when true, prints the command that would be executed without running it.
	DryRun bool
}

// spawnAntigravityOtelCollector spawns a per-session OTel collector.
func spawnAntigravityOtelCollector(projectDir string) (port int, sessionID string, cleanup func()) {
	binPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wipnote: warning: antigravity per-session collector skipped: %v\n", err)
		return 0, "", nil
	}

	sessionID = generateOtelSessionID()
	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr:     os.Stderr,
		StrictMode: os.Getenv("WIPNOTE_OTEL_STRICT") == "1",
	})

	spawnedPort, spawnCleanup, spawnErr := lc.Spawn(binPath, sessionID, projectDir)
	if spawnErr != nil {
		fmt.Fprintf(os.Stderr, "wipnote: FATAL: antigravity collector spawn failed: %v\n", spawnErr)
		if os.Getenv("WIPNOTE_OTEL_STRICT") == "1" {
			os.Exit(1)
		}
		return 0, "", nil
	}

	return spawnedPort, sessionID, spawnCleanup
}

// buildAntigravityOtelEnv returns a copy of base with Antigravity telemetry variables set.
func buildAntigravityOtelEnv(base []string, port int, sessionID string) []string {
	return launcher.BuildHarnessOtelEnv(base, "antigravity_cli", port, sessionID)
}

func buildAntigravityAgentEnv(base []string) []string {
	return launcher.BuildHarnessAgentEnv(base, "antigravity_cli")
}

// execAntigravity builds the agy argv and runs it.
func execAntigravity(opts antigravityLaunchOpts) error {
	var agyArgs []string
	agyArgs = append(agyArgs, opts.ExtraArgs...)

	if opts.DryRun {
		fmt.Printf("[dry-run] agy %s\n", strings.Join(agyArgs, " "))
		if opts.ProjectRoot != "" {
			fmt.Printf("[dry-run] in directory: %s\n", opts.ProjectRoot)
		}
		return nil
	}

	markerRoot := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		markerRoot = opts.WipnoteRoot
	}
	writeLaunchMarker("default", markerRoot)

	agyPath, err := exec.LookPath("agy")
	if err != nil {
		return fmt.Errorf("agy not found in PATH: %w\nInstall Antigravity CLI first.", err)
	}

	effectiveProjDir := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		effectiveProjDir = opts.WipnoteRoot
	}

	// Show the one-time OTel first-launch notice.
	MaybeShowOtelNotice(effectiveProjDir)

	// Auto-start a detached `wipnote serve` for the dashboard.
	ensureServeForDashboard(effectiveProjDir)

	// Launch-time guard-profile initialization.
	ensureGuardProfile(effectiveProjDir)

	var otelPort int
	var otelSessionID string
	var otelCleanup func()
	if effectiveProjDir != "" && !isExplicitlyDisabled(os.Getenv("WIPNOTE_OTEL_ENABLED")) {
		otelPort, otelSessionID, otelCleanup = spawnAntigravityOtelCollector(effectiveProjDir)
		if otelCleanup != nil {
			defer otelCleanup()
		}
	}

	c := exec.Command(agyPath, agyArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Build the child env.
	env := os.Environ()
	switch {
	case opts.WorktreeRoot != "":
		projectDir := opts.WipnoteRoot
		if projectDir == "" {
			projectDir = opts.ProjectRoot
		}
		env = setOrReplaceEnv(env, "WIPNOTE_PROJECT_DIR", projectDir)
		c.Dir = opts.WorktreeRoot
	case opts.ProjectRoot != "":
		env = setOrReplaceEnv(env, "WIPNOTE_PROJECT_DIR", opts.ProjectRoot)
		c.Dir = opts.ProjectRoot
	}
	env = buildAntigravityAgentEnv(env)
	env = append(env, "WIPNOTE_AGENT=antigravity")
	env = buildAntigravityOtelEnv(env, otelPort, otelSessionID)

	if otelSessionID != "" && effectiveProjDir != "" {
		familyID := resolveSessionFamilyID(effectiveProjDir, otelSessionID, "", false)
		env = setOrReplaceEnv(env, "WIPNOTE_SESSION_FAMILY_ID", familyID)
		persistLauncherSessionFamily(effectiveProjDir, otelSessionID, "antigravity", familyID)
	}

	c.Env = env

	return runHarnessWithCleanup(c, otelCleanup)
}
