package main

import (
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
// matching tag on origin, and errors if that also fails.
func resolveGeminiExtensionRef(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if version != "dev" {
		return "gemini-extension-v" + version, nil
	}
	// Dev binary: ask git for the latest gemini-extension-v* tag on origin.
	out, err := exec.Command("git", "ls-remote", "--tags", "origin", "gemini-extension-v*").Output()
	if err != nil {
		return "", fmt.Errorf(
			"binary built in dev mode and git ls-remote failed: %w\n"+
				"Either build with a real version (htmlgraph build) or pass --ref <ref>", err)
	}
	// Each line: "<sha>\trefs/tags/<tag>"
	var latest string
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
		latest = tag
	}
	if latest == "" {
		return "", fmt.Errorf(
			"binary built in dev mode: no gemini-extension-v* tags found on origin\n" +
				"Pass --ref <ref> to specify the extension version explicitly")
	}
	return latest, nil
}

// runGeminiInit installs the htmlgraph Gemini extension, idempotently.
// Corresponds to: htmlgraph gemini --init [--ref <ref>] [--force] [--dry-run]
func runGeminiInit(ref string, force, dryRun bool) error {
	resolvedRef, err := resolveGeminiExtensionRef(ref)
	if err != nil {
		return err
	}

	installDir := geminiExtensionInstallDir()
	if isGeminiExtensionInstalled() && !force {
		fmt.Printf("HtmlGraph Gemini extension is already installed at %s\n", installDir)
		fmt.Println("To reinstall: htmlgraph gemini --init --force")
		fmt.Println("To launch:    htmlgraph gemini")
		return nil
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
	// ProjectRoot is the absolute path to the project root.
	// When set, gemini is started in this directory and HTMLGRAPH_PROJECT_DIR is injected.
	ProjectRoot string
}

// execGemini builds the gemini argv and runs it, replacing the current process
// (or returning an error if exec fails).
func execGemini(opts geminiLaunchOpts) error {
	geminiPath, err := exec.LookPath("gemini")
	if err != nil {
		return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
	}

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

	c := exec.Command(geminiPath, geminiArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Inject HTMLGRAPH_PROJECT_DIR and HTMLGRAPH_AGENT so hooks and skills
	// resolve to the correct project root regardless of CWD.
	env := os.Environ()
	if opts.ProjectRoot != "" {
		env = append(env, "HTMLGRAPH_PROJECT_DIR="+opts.ProjectRoot)
		c.Dir = opts.ProjectRoot
	}
	env = append(env, "HTMLGRAPH_AGENT=gemini")
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
func launchGeminiDefault(extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Println("Launching Gemini CLI with HtmlGraph context...")
	return execGemini(geminiLaunchOpts{
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})
}

// launchGeminiContinue resumes the latest Gemini session.
// Corresponds to: htmlgraph gemini --continue
func launchGeminiContinue(extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Println("Resuming latest Gemini session...")
	return execGemini(geminiLaunchOpts{
		ResumeLast:  true,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})
}

// launchGeminiResume resumes a specific Gemini session by index.
// Corresponds to: htmlgraph gemini --resume <N>
func launchGeminiResume(index string, extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Printf("Resuming Gemini session %s...\n", index)
	return execGemini(geminiLaunchOpts{
		ResumeIndex: index,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})
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

	// Link the extension (idempotent — it's a live pointer).
	linkArgs := []string{"extensions", "link", localExtPath}
	if dryRun {
		fmt.Printf("[dry-run] gemini %s\n", strings.Join(linkArgs, " "))
	} else {
		geminiPath, err := exec.LookPath("gemini")
		if err != nil {
			return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
		}
		if out, linkErr := exec.Command(geminiPath, linkArgs...).CombinedOutput(); linkErr != nil {
			return fmt.Errorf("gemini extensions link failed: %w\n%s", linkErr, strings.TrimSpace(string(out)))
		}
		fmt.Println("Extension linked (live pointer to local source).")
	}

	projectRoot, _ := resolveProjectRoot()

	if dryRun {
		ext := ""
		if isolate {
			ext = "htmlgraph"
		}
		if ext != "" {
			fmt.Printf("[dry-run] would exec: gemini -e %s in %s\n", ext, projectRoot)
		} else {
			fmt.Printf("[dry-run] would exec: gemini in %s\n", projectRoot)
		}
		return nil
	}

	ext := ""
	if isolate {
		ext = "htmlgraph"
	}

	return execGemini(geminiLaunchOpts{
		Extension:   ext,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
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
	var init_, continue_, dev, force, isolate, listSessions, dryRun bool
	var resumeIndex, ref string

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

Session indices come from: gemini --list-sessions.

Installation:
  htmlgraph gemini --init               Installs gemini-extension-v<version> from GitHub.
  htmlgraph gemini --init --ref <ref>   Override the extension ref (for pre-release testing).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runGeminiInit(ref, force, dryRun)
			case listSessions:
				return execGemini(geminiLaunchOpts{ListSessions: true})
			case dev:
				return launchGeminiDev(isolate, dryRun, args)
			case continue_:
				return launchGeminiContinue(args)
			case resumeIndex != "":
				return launchGeminiResume(resumeIndex, args)
			default:
				return launchGeminiDefault(args)
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
	cmd.Flags().StringVar(&resumeIndex, "resume", "", "Resume a specific Gemini session by index (e.g. --resume 3)")
	cmd.Flags().StringVar(&ref, "ref", "", "With --init: override the extension ref (default: gemini-extension-v<version>)")

	return cmd
}
