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
	// Continue, when true, passes --continue to agy (resume most recent
	// conversation).
	Continue bool
	// ResumeID, if non-empty, passes --conversation <id> to agy (resume by
	// conversation ID). Takes precedence over Continue.
	ResumeID string
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
	// ExtraEnv is layered onto the child process after the launcher sets its
	// standard wipnote and telemetry environment.
	ExtraEnv []string
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

// buildAntigravityArgs builds the agy argv for resume/continue plus any
// forwarded extra args. Resume-by-id (--conversation <id>) takes precedence
// over --continue. Flag names verified live against agy v1.0.8: agy has no
// --resume flag; resume-by-id is --conversation <id>, and -c/--continue
// resumes the most recent conversation.
func buildAntigravityArgs(continue_ bool, resumeID string, extraArgs []string) []string {
	var args []string
	switch {
	case resumeID != "":
		args = append(args, "--conversation", resumeID)
	case continue_:
		args = append(args, "--continue")
	}
	args = append(args, extraArgs...)
	return args
}

// writeAntigravitySystemPrompt renders the embedded wipnote orchestrator system
// prompt for Antigravity, writes it to a temp file, and returns the absolute
// path. The PreInvocation hook reads this file (via WIPNOTE_ANTIGRAVITY_SYSTEM_MD)
// and injects it into agy through injectSteps[].systemMessage. It reuses the
// Gemini rendering, then applies agy's tool rename (run_shell_command ->
// run_command) so tool references match what agy exposes.
func writeAntigravitySystemPrompt() (string, error) {
	f, err := os.CreateTemp("", "wipnote-antigravity-system-*.md")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	rendered := renderGeminiSystemPrompt(geminiSystemPrompt, geminiLaunchModeDefault)
	rendered = strings.ReplaceAll(rendered, "run_shell_command", "run_command")
	if _, err := f.WriteString(rendered); err != nil {
		f.Close()
		return "", fmt.Errorf("writing antigravity system prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return f.Name(), nil
}

// execAntigravity builds the agy argv and runs it.
func execAntigravity(opts antigravityLaunchOpts) error {
	agyArgs := buildAntigravityArgs(opts.Continue, opts.ResumeID, opts.ExtraArgs)

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
	env = mergeLauncherEnv(env, opts.ExtraEnv...)

	// Make the orchestrator system prompt available to the PreInvocation hook,
	// which injects it via injectSteps[].systemMessage — the only channel agy
	// honors (GEMINI_SYSTEM_MD / additionalContext / plugin context are ignored).
	// Non-fatal: without it, hooks still fire and the agent runs unguided.
	if smdPath, smdErr := writeAntigravitySystemPrompt(); smdErr == nil {
		env = setOrReplaceEnv(env, "WIPNOTE_ANTIGRAVITY_SYSTEM_MD", smdPath)
	} else {
		fmt.Fprintf(os.Stderr, "wipnote: warning: could not stage Antigravity orchestrator prompt: %v\n", smdErr)
	}

	if otelSessionID != "" && effectiveProjDir != "" {
		// A resumed launch (--continue or --resume <id>) must join the existing
		// session family, not start a new one, so dashboard/observability
		// grouping matches the other resume-capable launchers. agy's --resume ID
		// is a conversation ID, not a wipnote session ID, so pass "" as the
		// resumed session ID and rely on resolveSessionFamilyID's most-recent
		// path (mirrors gemini_launch.go).
		isResume := opts.Continue || opts.ResumeID != ""
		familyID := resolveSessionFamilyID(effectiveProjDir, otelSessionID, "", isResume)
		env = setOrReplaceEnv(env, "WIPNOTE_SESSION_FAMILY_ID", familyID)
		persistLauncherSessionFamily(effectiveProjDir, otelSessionID, "antigravity", familyID)
	}

	env = withHarnessEnv(env, harnessAntigravity)
	c.Env = env

	return runHarnessWithCleanup(c, otelCleanup)
}
