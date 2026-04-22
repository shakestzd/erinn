package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// geminiExtensionInstallDir returns the expected install directory for the
// htmlgraph Gemini extension.
func geminiExtensionInstallDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "extensions", "htmlgraph")
}

// isGeminiExtensionInstalled reports whether the htmlgraph extension is already
// installed at the default location.
func isGeminiExtensionInstalled() bool {
	_, err := os.Stat(geminiExtensionInstallDir())
	return err == nil
}

// resolveGeminiExtensionRef returns the --ref value to use when installing the
// extension. When the binary version is known (non-"dev"), it returns
// "gemini-extension-v<version>". In dev mode it falls back to the latest
// matching tag on origin (using semver sort), and errors if that also fails.
func resolveGeminiExtensionRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if version != "dev" {
		return "gemini-extension-v" + version, nil
	}
	// Dev binary: ask git for the latest gemini-extension-v* tag on origin.
	// Use git's own semver sort (--sort=-version:refname) to ensure v0.10.0
	// sorts after v0.9.0 (lexicographic tail -1 would pick incorrectly).
	out, err := exec.Command("git", "ls-remote", "--sort=-version:refname", "--tags", "origin", "gemini-extension-v*").Output()
	if err != nil {
		return "", fmt.Errorf(
			"binary built in dev mode and git ls-remote failed: %w\n"+
				"Either build with a real version (htmlgraph build) or pass --ref <ref>", err)
	}
	// Each line: "<sha>\trefs/tags/<tag>"
	// git's --sort=-version:refname puts highest semver first, so we take the first.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		tag := strings.TrimPrefix(parts[1], "refs/tags/")
		// Skip ^{} dereferenced tag entries.
		if strings.HasSuffix(tag, "^{}") {
			continue
		}
		// First non-deref tag is the highest semver.
		return tag, nil
	}
	return "", fmt.Errorf(
		"binary built in dev mode: no gemini-extension-v* tags found on origin\n" +
			"Pass --ref <ref> to specify the extension version explicitly")
}

// runGeminiInit installs the htmlgraph Gemini extension, idempotently.
// Corresponds to: htmlgraph gemini --init [--ref <ref>] [--force] [--dry-run]
func runGeminiInit(ref string, force, dryRun bool) error {
	installDir := geminiExtensionInstallDir()

	// Check idempotency BEFORE resolving ref. For dev builds (version == "dev"),
	// skipping ref resolution avoids a network call when already installed.
	if isGeminiExtensionInstalled() && !force {
		fmt.Printf("HtmlGraph Gemini extension is already installed at %s\n", installDir)
		fmt.Println("To reinstall: htmlgraph gemini --init --force")
		fmt.Println("To launch:    htmlgraph gemini")
		return nil
	}

	resolvedRef, err := resolveGeminiExtensionRef(ref)
	if err != nil {
		return err
	}

	installArgs := []string{
		"extensions", "install",
		"shakestzd/htmlgraph",
		"--ref", resolvedRef,
		"--consent",
		"--skip-settings",
	}

	fmt.Printf("Installing HtmlGraph Gemini extension...\n")
	fmt.Printf("  ref: %s\n", resolvedRef)

	if dryRun {
		fmt.Printf("[dry-run] gemini %s\n", strings.Join(installArgs, " "))
		return nil
	}

	geminiPath, err := exec.LookPath("gemini")
	if err != nil {
		return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
	}

	out, runErr := exec.Command(geminiPath, installArgs...).CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("gemini extensions install failed: %w\n%s", runErr, strings.TrimSpace(string(out)))
	}

	fmt.Println("HtmlGraph Gemini extension installed.")
	fmt.Println()
	fmt.Println("Setup complete. Run: htmlgraph gemini")
	return nil
}

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
	// When set, gemini is started in this directory and HTMLGRAPH_PROJECT_DIR is injected.
	ProjectRoot string
	// WorktreeRoot, when non-empty, overrides the working directory for the
	// Gemini process. HTMLGRAPH_PROJECT_DIR is set to HtmlgraphRoot instead.
	WorktreeRoot string
	// HtmlgraphRoot is the canonical project root containing .htmlgraph/.
	// Used to set HTMLGRAPH_PROJECT_DIR when running in a worktree.
	HtmlgraphRoot string
	// DryRun, when true, prints the command that would be executed without running it.
	DryRun bool
}

// renderGeminiSystemPrompt pre-processes the embedded orchestrator prompt by
// substituting tool-name placeholders with literal tool names. This is a defensive
// measure: even if Gemini's GEMINI_SYSTEM_MD substitution ever regresses, users will
// see literal tool names (read_file, replace, etc.) rather than template variables.
//
// Section placeholders (${AgentSkills}, ${SubAgents}, ${AvailableTools}) are left
// unchanged — they benefit from Gemini's runtime rendering and are more complex
// to emulate statically.
func renderGeminiSystemPrompt(content string) string {
	toolNameReplacements := map[string]string{
		"${read_file_ToolName}":            "read_file",
		"${replace_ToolName}":              "replace",
		"${write_file_ToolName}":           "write_file",
		"${grep_search_ToolName}":          "grep_search",
		"${glob_ToolName}":                 "glob",
		"${run_shell_command_ToolName}":    "run_shell_command",
		"${web_fetch_ToolName}":            "web_fetch",
		"${google_web_search_ToolName}":    "google_web_search",
	}

	result := content
	for placeholder, literal := range toolNameReplacements {
		result = strings.ReplaceAll(result, placeholder, literal)
	}
	return result
}

// writeGeminiSystemPrompt writes the embedded orchestrator prompt to a temp file
// and returns the absolute path. Gemini reads the file at startup via GEMINI_SYSTEM_MD.
// The caller does not need to clean up — the OS temp dir is cleared automatically.
func writeGeminiSystemPrompt() (string, error) {
	f, err := os.CreateTemp("", "htmlgraph-gemini-system-*.md")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	// Pre-render tool-name placeholders before writing.
	rendered := renderGeminiSystemPrompt(geminiSystemPrompt)
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

// execGemini builds the gemini argv and runs it, replacing the current process
// (or returning an error if exec fails). If opts.DryRun is true, prints the
// intended command and returns without executing.
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
	// The env var expects an absolute path; Gemini does a full override (not append).
	systemMdPath, err := writeGeminiSystemPrompt()
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

	geminiPath, err := exec.LookPath("gemini")
	if err != nil {
		return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
	}

	c := exec.Command(geminiPath, geminiArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Inject HTMLGRAPH_PROJECT_DIR, HTMLGRAPH_AGENT, and GEMINI_SYSTEM_MD so hooks,
	// skills, and the orchestrator prompt all resolve correctly regardless of CWD.
	// When WorktreeRoot is set, the process runs in the worktree but
	// HTMLGRAPH_PROJECT_DIR points to the canonical project root (HtmlgraphRoot).
	env := os.Environ()
	switch {
	case opts.WorktreeRoot != "":
		projectDir := opts.HtmlgraphRoot
		if projectDir == "" {
			projectDir = opts.ProjectRoot
		}
		env = setOrReplaceEnv(env, "HTMLGRAPH_PROJECT_DIR", projectDir)
		c.Dir = opts.WorktreeRoot
	case opts.ProjectRoot != "":
		env = setOrReplaceEnv(env, "HTMLGRAPH_PROJECT_DIR", opts.ProjectRoot)
		c.Dir = opts.ProjectRoot
	}
	env = append(env, "HTMLGRAPH_AGENT=gemini")
	env = append(env, "GEMINI_SYSTEM_MD="+systemMdPath)
	c.Env = env

	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// launchGeminiDefault launches Gemini interactively with HtmlGraph env injection.
// Corresponds to: htmlgraph gemini
func launchGeminiDefault(trackID, featureID, worktreePath, workItem string, noWorktree bool, extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()

	// Work item attribution: emit `htmlgraph feature start <id>` before launching.
	if workItem != "" && !dryRun {
		if err := runFeatureStart(workItem); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start work item %s: %v\n", workItem, err)
		}
	}

	// Resolve worktree path.
	workDir := projectRoot
	htmlgraphRoot := ""
	switch {
	case worktreePath != "":
		workDir = worktreePath
		htmlgraphRoot = projectRoot
	case !noWorktree && trackID != "":
		wt, err := EnsureForTrack(trackID, projectRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		htmlgraphRoot = projectRoot
	case !noWorktree && featureID != "":
		wt, err := EnsureForFeature(featureID, projectRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		htmlgraphRoot = projectRoot
	}

	fmt.Println("Launching Gemini CLI with HtmlGraph context...")
	return execGemini(geminiLaunchOpts{
		ExtraArgs:     extraArgs,
		ProjectRoot:   workDir,
		WorktreeRoot:  workDir,
		HtmlgraphRoot: htmlgraphRoot,
		DryRun:        dryRun,
	})
}

// launchGeminiContinue resumes the latest Gemini session.
// Corresponds to: htmlgraph gemini --continue
func launchGeminiContinue(extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Println("Resuming latest Gemini session...")
	return execGemini(geminiLaunchOpts{
		ResumeLast:  true,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		DryRun:      dryRun,
	})
}

// launchGeminiResume resumes a specific Gemini session by index.
// Corresponds to: htmlgraph gemini --resume <N>
func launchGeminiResume(index string, extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Printf("Resuming Gemini session %s...\n", index)
	return execGemini(geminiLaunchOpts{
		ResumeIndex: index,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		DryRun:      dryRun,
	})
}

// geminiExtensionMetadata represents the install metadata for the htmlgraph extension.
type geminiExtensionMetadata struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

// isExtensionAlreadyLinkedToLocalPath checks if the htmlgraph extension is already
// linked (as a live pointer) to the specified local path. Returns true only if
// the metadata exists, matches the local path, and is a link type.
func isExtensionAlreadyLinkedToLocalPath(localExtPath string) bool {
	home, _ := os.UserHomeDir()
	metaPath := filepath.Join(home, ".gemini", "extensions", "htmlgraph", ".gemini-extension-install.json")

	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}

	var meta geminiExtensionMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}

	return meta.Type == "link" && meta.Source == localExtPath
}

// buildGeminiLinkArgs returns the args for `gemini extensions link`.
// Exported for testability.
func buildGeminiLinkArgs(localExtPath string) []string {
	return []string{"extensions", "link", localExtPath, "--consent"}
}

// launchGeminiDev links the local packages/gemini-extension and launches Gemini.
// Corresponds to: htmlgraph gemini --dev [--isolate]
func launchGeminiDev(isolate, dryRun bool, extraArgs []string) error {
	// Resolve the local extension path relative to the project root.
	localExtPath, err := resolveLocalGeminiExtension()
	if err != nil {
		return err
	}

	fmt.Printf("Launching Gemini CLI in dev mode...\n")
	fmt.Printf("  Local extension: %s\n", localExtPath)

	// Check idempotency: if already linked to this local path, skip the link exec.
	// If linked elsewhere or installed from a different source, uninstall first.
	if !dryRun {
		if isExtensionAlreadyLinkedToLocalPath(localExtPath) {
			fmt.Println("Extension already linked (live pointer to local source).")
		} else {
			// Check if there's a stale install that needs cleaning up.
			home, _ := os.UserHomeDir()
			metaPath := filepath.Join(home, ".gemini", "extensions", "htmlgraph", ".gemini-extension-install.json")
			if data, err := os.ReadFile(metaPath); err == nil {
				var meta geminiExtensionMetadata
				if json.Unmarshal(data, &meta) == nil && (meta.Type != "link" || meta.Source != localExtPath) {
					// Stale or wrong-source install — uninstall first.
					fmt.Println("Replacing existing htmlgraph extension install...")
					geminiPath, geminiErr := exec.LookPath("gemini")
					if geminiErr != nil {
						return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", geminiErr)
					}
					uninstallArgs := []string{"extensions", "uninstall", "htmlgraph"}
					// Surface uninstall errors rather than swallowing them.
					out, uninstallErr := exec.Command(geminiPath, uninstallArgs...).CombinedOutput()
					if uninstallErr != nil {
						return fmt.Errorf("gemini extensions uninstall (while replacing stale install) failed: %w\n%s", uninstallErr, strings.TrimSpace(string(out)))
					}
				}
			}

			// Link the extension (idempotent — it's a live pointer).
			linkArgs := buildGeminiLinkArgs(localExtPath)
			fmt.Println("Linking extension...")
			geminiPath, err := exec.LookPath("gemini")
			if err != nil {
				return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
			}
			if out, linkErr := exec.Command(geminiPath, linkArgs...).CombinedOutput(); linkErr != nil {
				return fmt.Errorf("gemini extensions link failed: %w\n%s", linkErr, strings.TrimSpace(string(out)))
			}

			// Verify filesystem state: re-read the metadata file and validate its contents.
			// Check that type == "link" and source == localExtPath, not just that the file exists.
			// This catches cases where gemini may have used a different path or install method.
			home2, _ := os.UserHomeDir()
			metaPath2 := filepath.Join(home2, ".gemini", "extensions", "htmlgraph", ".gemini-extension-install.json")
			metaData, err := os.ReadFile(metaPath2)
			if err != nil {
				return fmt.Errorf("gemini extensions link appeared to succeed but %s was not created — the link may have been blocked by an interactive prompt in gemini. Check gemini extensions list and try: 'gemini extensions link %s --consent' manually", metaPath2, localExtPath)
			}

			var postLinkMeta geminiExtensionMetadata
			if err := json.Unmarshal(metaData, &postLinkMeta); err != nil {
				return fmt.Errorf("gemini extensions link produced invalid metadata at %s: %w", metaPath2, err)
			}

			if postLinkMeta.Type != "link" {
				return fmt.Errorf("gemini extensions link produced wrong type at %s: got %q, want \"link\"", metaPath2, postLinkMeta.Type)
			}

			if postLinkMeta.Source != localExtPath {
				return fmt.Errorf("gemini extensions link did not update source path: still points at %q, expected %q — try `gemini extensions uninstall htmlgraph` manually and retry", postLinkMeta.Source, localExtPath)
			}

			fmt.Println("Extension linked (live pointer to local source).")
		}
	}

	// Handle dry-run mode for link.
	if dryRun {
		linkArgs := buildGeminiLinkArgs(localExtPath)
		fmt.Printf("[dry-run] gemini %s\n", strings.Join(linkArgs, " "))
	}

	projectRoot, _ := resolveProjectRoot()

	ext := ""
	if isolate {
		ext = "htmlgraph"
	}

	return execGemini(geminiLaunchOpts{
		Extension:   ext,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		DryRun:      dryRun,
	})
}

// resolveLocalGeminiExtension returns the absolute path to packages/gemini-extension/
// by walking up from CWD to find the project root (directory containing .htmlgraph/).
func resolveLocalGeminiExtension() (string, error) {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return "", fmt.Errorf("could not find project root (.htmlgraph/ directory not found)\n" +
			"Run from the HtmlGraph project directory, or use htmlgraph gemini --init for the extension version")
	}
	projectRoot := filepath.Dir(htmlgraphDir)
	extPath := filepath.Join(projectRoot, "packages", "gemini-extension")
	if _, statErr := os.Stat(extPath); os.IsNotExist(statErr) {
		return "", fmt.Errorf("packages/gemini-extension/ not found at %s\n"+
			"Run from the HtmlGraph repo root, or use htmlgraph gemini --init for the published version",
			extPath)
	}
	abs, err := filepath.Abs(extPath)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", extPath, err)
	}
	return abs, nil
}

// geminiCmd returns the cobra command for `htmlgraph gemini`.
func geminiCmd() *cobra.Command {
	var init_, continue_, dev, force, isolate, listSessions, dryRun, noWorktree bool
	var resumeIndex, ref, trackID, featureID, worktreePath, workItem string

	cmd := &cobra.Command{
		Use:   "gemini",
		Short: "Launch Gemini CLI with HtmlGraph context",
		Long: `Launch Gemini CLI with HtmlGraph observability context.

Modes:
  htmlgraph gemini                      Launch Gemini interactively with HtmlGraph env.
  htmlgraph gemini --init               Install the HtmlGraph Gemini extension (idempotent).
  htmlgraph gemini --continue           Resume the latest Gemini session (gemini --resume latest).
  htmlgraph gemini --resume <N>         Resume a specific Gemini session by index.
  htmlgraph gemini --dev                Link packages/gemini-extension/ and launch Gemini.
  htmlgraph gemini --list-sessions      Pass-through: gemini --list-sessions.
  htmlgraph gemini --feature <id>       Launch in the feature's git worktree.
  htmlgraph gemini --track <id>         Launch in the track's git worktree.

Session indices come from: gemini --list-sessions.

Installation:
  htmlgraph gemini --init               Installs gemini-extension-v<version> from GitHub.
  htmlgraph gemini --init --ref <ref>   Override the extension ref (for pre-release testing).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runGeminiInit(ref, force, dryRun)
			case listSessions:
				return execGemini(geminiLaunchOpts{ListSessions: true, DryRun: dryRun})
			case dev:
				return launchGeminiDev(isolate, dryRun, args)
			case continue_:
				return launchGeminiContinue(args, dryRun)
			case resumeIndex != "":
				return launchGeminiResume(resumeIndex, args, dryRun)
			default:
				return launchGeminiDefault(trackID, featureID, worktreePath, workItem, noWorktree, args, dryRun)
			}
		},
	}

	cmd.Flags().BoolVar(&init_, "init", false, "Install the HtmlGraph Gemini extension (idempotent)")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume the latest Gemini session")
	cmd.Flags().BoolVar(&dev, "dev", false, "Link packages/gemini-extension/ as a live pointer and launch Gemini")
	cmd.Flags().BoolVar(&force, "force", false, "With --init: reinstall even if already installed")
	cmd.Flags().BoolVar(&isolate, "isolate", false, "With --dev: pass -e htmlgraph to suppress other extensions")
	cmd.Flags().BoolVar(&listSessions, "list-sessions", false, "Pass-through to gemini --list-sessions")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation (run in project root)")
	cmd.Flags().StringVar(&resumeIndex, "resume", "", "Resume a specific Gemini session by index (e.g. --resume 3)")
	cmd.Flags().StringVar(&ref, "ref", "", "With --init: override the extension ref (default: gemini-extension-v<version>)")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on (e.g., trk-3719d8f3)")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&worktreePath, "worktree", "", "Explicit worktree path (overrides --track/--feature resolution)")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for attribution prefix (e.g., feat-15c458aa)")

	return cmd
}
