package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/launcher"
	"github.com/shakestzd/wipnote/observe/otel/collector"
)

type geminiLaunchMode string

const (
	geminiLaunchModeDefault  geminiLaunchMode = "default"
	geminiLaunchModeDev      geminiLaunchMode = "dev"
	geminiLaunchModeContinue geminiLaunchMode = "continue"
	geminiLaunchModeInit     geminiLaunchMode = "init"
)

// geminiLaunchOpts controls how the Gemini CLI is launched.
type geminiLaunchOpts struct {
	// ResumeLast, when true, passes --resume latest to gemini.
	ResumeLast bool
	// ResumeIndex, if non-empty, passes --resume <N> to gemini.
	// Takes precedence over ResumeLast.
	ResumeIndex string
	// Extension, if non-empty, passes -e <extension> to gemini (isolate mode).
	Extension string
	// ListSessions, when true, passes --list-sessions to gemini and exits.
	ListSessions bool
	// ExtraArgs are forwarded to the gemini process.
	ExtraArgs []string
	// ProjectRoot is the absolute path to the project root (or worktree path).
	// When set, gemini is started in this directory and WIPNOTE_PROJECT_DIR is injected.
	ProjectRoot string
	// WorktreeRoot, when non-empty, overrides the working directory for the
	// Gemini process. WIPNOTE_PROJECT_DIR is set to WipnoteRoot instead.
	WorktreeRoot string
	// WipnoteRoot is the canonical project root containing .wipnote/.
	// Used to set WIPNOTE_PROJECT_DIR when running in a worktree.
	WipnoteRoot string
	// Mode is the launch mode (default, dev, continue).
	Mode geminiLaunchMode
	// DryRun, when true, prints the command that would be executed without running it.
	DryRun bool
	// ExtraEnv is layered onto the child process after the launcher sets its
	// standard wipnote and telemetry environment.
	ExtraEnv []string
}

// spawnGeminiOtelCollector spawns a per-session OTel collector and returns the
// port, session ID, and a cleanup function. On failure it writes a warning to
// stderr and returns zero port / nil cleanup so the caller can proceed without
// telemetry. Exits non-zero when WIPNOTE_OTEL_STRICT=1 and spawn fails.
func spawnGeminiOtelCollector(projectDir string) (port int, sessionID string, cleanup func()) {
	binPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wipnote: warning: gemini per-session collector skipped: %v\n", err)
		return 0, "", nil
	}

	sessionID = generateOtelSessionID()
	lc := collector.NewProcessCollector(collector.ProcessCollectorOpts{
		Stderr:     os.Stderr,
		StrictMode: os.Getenv("WIPNOTE_OTEL_STRICT") == "1",
	})

	spawnedPort, spawnCleanup, spawnErr := lc.Spawn(binPath, sessionID, projectDir)
	if spawnErr != nil {
		fmt.Fprintf(os.Stderr, "wipnote: FATAL: gemini collector spawn failed: %v\n", spawnErr)
		if os.Getenv("WIPNOTE_OTEL_STRICT") == "1" {
			os.Exit(1)
		}
		return 0, "", nil
	}

	return spawnedPort, sessionID, spawnCleanup
}

// buildGeminiOtelEnv returns a copy of base with Gemini telemetry variables set
// for the Gemini CLI child process. port and sessionID come from
// spawnGeminiOtelCollector; when port is 0 the base env is returned unchanged.
// Env var assembly is delegated to the harness registry to avoid hardcoding.
func buildGeminiOtelEnv(base []string, port int, sessionID string) []string {
	return launcher.BuildHarnessOtelEnv(base, "gemini_cli", port, sessionID)
}

func buildGeminiAgentEnv(base []string) []string {
	return launcher.BuildHarnessAgentEnv(base, "gemini_cli")
}

// renderGeminiSystemPrompt pre-processes the embedded orchestrator prompt by
// substituting tool-name placeholders with literal tool names.
func renderGeminiSystemPrompt(content string, mode geminiLaunchMode) string {
	toolNameReplacements := map[string]string{
		"${read_file_ToolName}":         "read_file",
		"${replace_ToolName}":           "replace",
		"${write_file_ToolName}":        "write_file",
		"${grep_search_ToolName}":       "grep_search",
		"${glob_ToolName}":              "glob",
		"${run_shell_command_ToolName}": "run_shell_command",
		"${web_fetch_ToolName}":         "web_fetch",
		"${google_web_search_ToolName}": "google_web_search",
	}

	result := content
	for placeholder, literal := range toolNameReplacements {
		result = strings.ReplaceAll(result, placeholder, literal)
	}

	// Add mode-specific addendum.
	addendum := geminiInstructionAddendum(mode)
	if addendum != "" {
		result += "\n\n# wipnote " + geminiInstructionModeTitle(mode) + " Addendum\n\n" + addendum
	}

	// Append the shared research-routing disposition exactly once. Source of
	// truth: cmd/wipnote/prompts/research-routing.md.
	result = strings.TrimSpace(result) + "\n\n" + strings.TrimSpace(researchRoutingContent) + "\n"

	return result
}

// writeGeminiSystemPrompt writes the embedded orchestrator prompt to a temp file
// and returns the absolute path.
func writeGeminiSystemPrompt(mode geminiLaunchMode) (string, error) {
	f, err := os.CreateTemp("", "wipnote-gemini-system-*.md")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	// Pre-render tool-name placeholders before writing.
	rendered := renderGeminiSystemPrompt(geminiSystemPrompt, mode)
	if _, err := f.WriteString(rendered); err != nil {
		f.Close()
		return "", fmt.Errorf("writing gemini system prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	abs, err := filepath.Abs(f.Name())
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for temp file: %w", err)
	}
	return abs, nil
}

// execGemini builds the gemini argv and runs it, replacing the current process.
func execGemini(opts geminiLaunchOpts) error {
	var geminiArgs []string

	if opts.ListSessions {
		geminiArgs = append(geminiArgs, "--list-sessions")
	} else if opts.ResumeIndex != "" {
		geminiArgs = append(geminiArgs, "--resume", opts.ResumeIndex)
	} else if opts.ResumeLast {
		geminiArgs = append(geminiArgs, "--resume", "latest")
	}

	if opts.Extension != "" {
		geminiArgs = append(geminiArgs, "-e", opts.Extension)
	}

	geminiArgs = append(geminiArgs, opts.ExtraArgs...)

	// Write the embedded orchestrator prompt to a tmpfile and inject via GEMINI_SYSTEM_MD.
	systemMdPath, err := writeGeminiSystemPrompt(opts.Mode)
	if err != nil {
		return fmt.Errorf("failed to prepare GEMINI_SYSTEM_MD: %w", err)
	}

	if opts.DryRun {
		fmt.Printf("[dry-run] GEMINI_SYSTEM_MD=%s\n", systemMdPath)
		fmt.Printf("[dry-run] gemini %s\n", strings.Join(geminiArgs, " "))
		if opts.ProjectRoot != "" {
			fmt.Printf("[dry-run] in directory: %s\n", opts.ProjectRoot)
		}
		return nil
	}

	// Write launch marker to the main project root, not the worktree.
	markerRoot := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		markerRoot = opts.WipnoteRoot
	}
	writeLaunchMarker(string(opts.Mode), markerRoot)

	geminiPath, err := exec.LookPath("gemini")
	if err != nil {
		return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
	}

	// Resolve the effective project dir for OTel collector spawning.
	effectiveProjDir := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		effectiveProjDir = opts.WipnoteRoot
	}

	// Show the one-time OTel first-launch notice.
	MaybeShowOtelNotice(effectiveProjDir)

	// Auto-start a detached `wipnote serve` for the dashboard.
	ensureServeForDashboard(effectiveProjDir)

	// Launch-time guard-profile initialization (peer to the OTel collector
	// bootstrap). No-op when already approved or non-interactive; never blocks.
	ensureGuardProfile(effectiveProjDir)

	// Spawn a per-session OTel collector when a project dir is known and OTel
	// is not explicitly disabled. Non-fatal: falls back gracefully on failure.
	var otelPort int
	var otelSessionID string
	var otelCleanup func()
	if effectiveProjDir != "" && !isExplicitlyDisabled(os.Getenv("WIPNOTE_OTEL_ENABLED")) {
		otelPort, otelSessionID, otelCleanup = spawnGeminiOtelCollector(effectiveProjDir)
		if otelCleanup != nil {
			defer otelCleanup()
		}
	}

	c := exec.Command(geminiPath, geminiArgs...)
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
	env = buildGeminiAgentEnv(env)
	env = append(env, "WIPNOTE_AGENT=gemini")
	env = append(env, "GEMINI_SYSTEM_MD="+systemMdPath)
	env = buildGeminiOtelEnv(env, otelPort, otelSessionID)
	env = mergeLauncherEnv(env, opts.ExtraEnv...)

	// Session-family continuity (slice-4, feat-a225ce7c):
	// Resolve which family this Gemini session belongs to, then inject
	// WIPNOTE_SESSION_FAMILY_ID so the SessionStart hook can write the DB column.
	// Also persist the launcher-side state file immediately (concrete write path
	// that survives even when hooks are not configured).
	if otelSessionID != "" && effectiveProjDir != "" {
		isResume := opts.ResumeLast || opts.ResumeIndex != ""
		// Gemini resume is a numeric --resume <N> index, never a wipnote
		// session ID, so no concrete resumed session ID is available here;
		// resolveSessionFamilyID uses the ordered most-recent-session family.
		familyID := resolveSessionFamilyID(effectiveProjDir, otelSessionID, "", isResume)
		env = setOrReplaceEnv(env, "WIPNOTE_SESSION_FAMILY_ID", familyID)
		persistLauncherSessionFamily(effectiveProjDir, otelSessionID, "gemini", familyID)
	}

	env = withHarnessEnv(env, harnessGemini)
	c.Env = env

	return runHarnessWithCleanup(c, otelCleanup)
}

func geminiInstructionModeTitle(mode geminiLaunchMode) string {
	switch mode {
	case geminiLaunchModeDev:
		return "Dev"
	case geminiLaunchModeContinue:
		return "Continue"
	case geminiLaunchModeInit:
		return "Init"
	default:
		return "Orchestrator"
	}
}

func geminiInstructionAddendum(mode geminiLaunchMode) string {
	switch mode {
	case geminiLaunchModeDev:
		return strings.TrimSpace(geminiDevInstructions)
	case geminiLaunchModeContinue:
		return strings.TrimSpace(geminiContinueInstructions)
	default:
		return ""
	}
}

const geminiDevInstructions = `## Gemini Dev Mode

This session was launched with ` + "`wipnote gemini --dev`" + `.

- You are using the local ` + "`packages/gemini-extension/`" + ` source.
- Prefer editing the source of truth: ` + "`packages/plugin-core/manifest.json`" + ` and shared plugin assets under ` + "`plugin/`" + `.
- After plugin asset or manifest changes, rebuild generated ports with ` + "`wipnote plugin build-ports`" + ` before validating Gemini behavior.
- Use ` + "`wipnote gemini --dev --isolate`" + ` to ensure other extensions don't interfere with your tools.`

const geminiContinueInstructions = `## Gemini Continue Mode

This session is resuming an existing Gemini conversation.

- Preserve the resumed session's prior intent and active work item unless the user explicitly redirects.
- Before starting new work, recover current context from the conversation, ` + "`wipnote status`" + `, and the active work item hints.
- Do not recreate setup or restart already-completed tasks just because the launcher resumed the session.`
