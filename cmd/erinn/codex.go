package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// codexMarketplaceRepo is the GitHub repo that hosts the codex marketplace.
const codexMarketplaceRepo = "shakestzd/erinn"

// codexMarketplaceSparse is the sparse path within the monorepo.
const codexMarketplaceSparse = "packages/codex-marketplace"

// codexConfigPath returns the path to ~/.codex/config.toml.
func codexConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

// isCodexMarketplaceInstalledAt is the testable core that reads the given path.
func isCodexMarketplaceInstalledAt(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "[marketplaces.erinn]") ||
		strings.Contains(content, `[plugins."erinn@erinn"]`) ||
		strings.Contains(content, `[plugins."htmlgraph@htmlgraph"]`)
}

// isCodexHooksEnabledAt is the testable core.
func isCodexHooksEnabledAt(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "codex_hooks") && strings.Contains(trimmed, "=") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[1]) == "true" {
				return true
			}
		}
	}
	return false
}

// getCodexMarketplacePathAt parses config.toml and returns the registered erinn
// marketplace path, or empty string if not found.
func getCodexMarketplacePathAt(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}

	tree := make(map[string]any)
	if err := toml.Unmarshal(data, &tree); err != nil {
		return ""
	}

	// Check [marketplaces.erinn]
	if mkts, ok := tree["marketplaces"].(map[string]any); ok {
		if hg, ok := mkts["erinn"].(map[string]any); ok {
			if source, ok := hg["source"].(string); ok {
				return source
			}
			if path, ok := hg["path"].(string); ok {
				return path
			}
		}
	}

	// Check plugin entries for both current and legacy names.
	if plugins, ok := tree["plugins"].(map[string]any); ok {
		for _, key := range []string{"erinn@erinn", "htmlgraph@htmlgraph"} {
			if hg, ok := plugins[key].(map[string]any); ok {
				if source, ok := hg["source"].(string); ok {
					return source
				}
				if path, ok := hg["path"].(string); ok {
					return path
				}
			}
		}
	}

	return ""
}

// removeCodexHtmlgraphRegistrations removes any Erinn/HtmlGraph marketplace or plugin
// registrations from the given config.toml file. It is idempotent — if the file
// does not exist or contains no matching entries, it is a no-op.
// Returns (removed bool, error). removed=true indicates at least one entry was deleted.
func removeCodexHtmlgraphRegistrations(configPath string) (bool, error) {
	// Read existing config, if any
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // file doesn't exist; no-op
		}
		return false, fmt.Errorf("reading %s: %w", configPath, err)
	}

	// Parse the TOML tree
	tree := make(map[string]any)
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return false, fmt.Errorf("parsing %s: %w", configPath, err)
		}
	}

	removed := false

	// Remove from [plugins] — current and legacy exact entries only.
	if plugins, ok := tree["plugins"].(map[string]any); ok {
		for _, key := range []string{"erinn@erinn", "htmlgraph@htmlgraph"} {
			if _, exists := plugins[key]; exists {
				delete(plugins, key)
				removed = true
			}
		}
		// If [plugins] is now empty, remove the whole section
		if len(plugins) == 0 {
			delete(tree, "plugins")
		}
	}

	// Remove from [marketplaces] — current and legacy entries.
	if mkts, ok := tree["marketplaces"].(map[string]any); ok {
		for _, key := range []string{"erinn", "htmlgraph"} {
			if _, exists := mkts[key]; exists {
				delete(mkts, key)
				removed = true
			}
		}
		// If [marketplaces] is now empty, remove the whole section
		if len(mkts) == 0 {
			delete(tree, "marketplaces")
		}
	}

	// If nothing was removed, no need to rewrite the file
	if !removed {
		return false, nil
	}

	// Marshal back to TOML and write
	newData, err := toml.Marshal(tree)
	if err != nil {
		return false, fmt.Errorf("marshaling TOML: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		return false, fmt.Errorf("writing %s: %w", configPath, err)
	}

	return true, nil
}

// ensureCodexHooksEnabled parses the config.toml file, merges codex_hooks = true
// into the [features] table (creating the section if absent), and writes it back.
// This is idempotent: if codex_hooks = true is already set, it's a no-op after
// re-serialization.
func ensureCodexHooksEnabled(configPath string) error {
	// Read existing config, if any
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	// Parse or create the TOML tree
	tree := make(map[string]any)
	if err == nil && len(data) > 0 {
		if err := toml.Unmarshal(data, &tree); err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}
	}

	// Ensure [features] table exists and set codex_hooks = true
	features, ok := tree["features"].(map[string]any)
	if !ok {
		features = make(map[string]any)
		tree["features"] = features
	}
	features["codex_hooks"] = true

	// Marshal back to TOML and write
	newData, err := toml.Marshal(tree)
	if err != nil {
		return fmt.Errorf("marshaling TOML: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}

	return nil
}

// promptYesNo asks the user a yes/no question and returns true if they answer y/Y/yes.
// If yes is true (--yes flag), the function returns true without prompting.
func promptYesNo(question string, yes bool) bool {
	if yes {
		return true
	}
	fmt.Print(question + " [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes"
}

// codexCmd returns the cobra command for `htmlgraph codex`.
func codexCmd() *cobra.Command {
	var init_, continue_, dev, cleanup, dryRun, yes, noWorktree bool
	var resumeID, trackID, featureID, worktreePath, workItem string

	cmd := &cobra.Command{
		Use:   "codex",
		Short: "Launch Codex CLI with HtmlGraph context",
		Long: `Launch Codex CLI with HtmlGraph observability context.

Modes:
  htmlgraph codex                   Launch Codex interactively with HtmlGraph env.
  htmlgraph codex install           Install the Erinn Codex plugin (idempotent).
  htmlgraph codex --init            Alias for htmlgraph codex install --remote.
  htmlgraph codex --continue        Resume the last Codex session (codex resume --last).
  htmlgraph codex --resume <id>     Resume a specific Codex session by ID.
  htmlgraph codex --dev             Register local packages/codex-marketplace/ and launch.
  htmlgraph codex --feature <id>    Launch in the feature's git worktree.
  htmlgraph codex --track <id>      Launch in the track's git worktree.

Session IDs come from ~/.codex/session_index.jsonl.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runCodexInit(yes, dryRun)
			case dev:
				return launchCodexDev(resumeID, cleanup, dryRun, args)
			case continue_:
				return launchCodexContinue(resumeID, args)
			default:
				return launchCodexDefault(resumeID, trackID, featureID, worktreePath, workItem, noWorktree, args)
			}
		},
	}

	cmd.Flags().BoolVar(&init_, "init", false, "Install the HtmlGraph Codex marketplace plugin (idempotent)")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume the last Codex session")
	cmd.Flags().BoolVar(&dev, "dev", false, "Register local packages/codex-marketplace/ and launch Codex")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "With --dev: unregister the local marketplace on exit")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Answer yes to all prompts (non-interactive)")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation (run in project root)")
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a specific Codex session by ID")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on (e.g., trk-3719d8f3)")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&worktreePath, "worktree", "", "Explicit worktree path (overrides --track/--feature resolution)")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for attribution prefix (e.g., feat-15c458aa)")

	cmd.AddCommand(codexInstallCmd())

	return cmd
}

type codexInstallOpts struct {
	Yes    bool
	DryRun bool
	Local  bool
	Remote bool
	Source string
}

func codexInstallCmd() *cobra.Command {
	var opts codexInstallOpts
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the Erinn Codex plugin",
		Long: `Install the Erinn Codex plugin by registering a Codex plugin marketplace
and enabling Codex hook support.

By default, this installs from the local generated marketplace at
packages/codex-marketplace/ when run inside the Erinn repo. Use --remote to
register the GitHub marketplace instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCodexInstall(opts)
		},
	}
	cmd.Flags().BoolVar(&opts.Local, "local", false, "Install from local packages/codex-marketplace/ (default inside this repo)")
	cmd.Flags().BoolVar(&opts.Remote, "remote", false, "Install from the GitHub marketplace")
	cmd.Flags().StringVar(&opts.Source, "source", "", "Explicit Codex marketplace source path or repo")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "Answer yes to all prompts (non-interactive)")
	return cmd
}

// runCodexInit installs the Erinn Codex marketplace plugin, idempotently.
// Corresponds to legacy: htmlgraph codex --init
// Kept as a remote alias so existing automation keeps working.
func runCodexInit(yes, dryRun bool) error {
	return runCodexInstall(codexInstallOpts{Yes: yes, DryRun: dryRun, Remote: true})
}

func codexMarketplaceAddArgs(source string, sparse string) []string {
	args := []string{"plugin", "marketplace", "add", source}
	if sparse != "" {
		args = append(args, "--sparse", sparse)
	}
	return args
}

// runCodexInstall installs the Erinn Codex marketplace plugin, idempotently.
// Phase 1: Install / verify marketplace (idempotent).
// Phase 2: Check codex_hooks — prompt user if not set.
func runCodexInstall(opts codexInstallOpts) error {
	if opts.Local && opts.Remote {
		return fmt.Errorf("choose only one of --local or --remote")
	}

	configPath := codexConfigPath()
	source := opts.Source
	sparse := ""

	switch {
	case source != "":
		// Explicit source is passed straight through.
	case opts.Remote:
		source = codexMarketplaceRepo
		sparse = codexMarketplaceSparse
	default:
		localMarketplace, err := resolveLocalCodexMarketplace()
		if err != nil {
			if opts.Local {
				return err
			}
			source = codexMarketplaceRepo
			sparse = codexMarketplaceSparse
		} else {
			source = localMarketplace
		}
	}

	// Phase 1: Install or verify marketplace.
	marketplaceInstalled := isCodexMarketplaceInstalledAt(configPath)
	if !marketplaceInstalled {
		addArgs := codexMarketplaceAddArgs(source, sparse)
		fmt.Printf("Installing Erinn Codex marketplace...\n")
		fmt.Printf("  source: %s\n", source)
		if sparse != "" {
			fmt.Printf("  sparse: %s\n", sparse)
		}

		if opts.DryRun {
			fmt.Printf("[dry-run] codex %s\n", strings.Join(addArgs, " "))
		} else {
			if out, err := exec.Command("codex", addArgs...).CombinedOutput(); err != nil {
				return fmt.Errorf("codex marketplace add failed: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			fmt.Println("Erinn Codex marketplace installed.")
		}
	} else {
		fmt.Println("Erinn Codex marketplace is already installed.")
	}

	// Phase 2: Check and optionally enable codex_hooks feature flag.
	// This runs on every --init so partial setups can be repaired.
	if !isCodexHooksEnabledAt(configPath) {
		if promptYesNo("Enable the codex_hooks feature flag in ~/.codex/config.toml?", opts.Yes) {
			if opts.DryRun {
				fmt.Println("[dry-run] would enable codex_hooks = true in ~/.codex/config.toml")
			} else {
				if err := ensureCodexHooksEnabled(configPath); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not enable codex_hooks: %v\n", err)
				} else {
					fmt.Println("codex_hooks feature flag enabled.")
				}
			}
		}
	} else {
		fmt.Println("codex_hooks feature flag is already enabled.")
	}

	fmt.Println()
	fmt.Println("Setup complete. Run: erinn codex")
	return nil
}

// launchCodexDefault launches Codex interactively with HtmlGraph env injection.
// Corresponds to: htmlgraph codex
func launchCodexDefault(resumeID, trackID, featureID, worktreePath, workItem string, noWorktree bool, extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()

	// Work item attribution: emit `htmlgraph feature start <id>` before launching.
	if workItem != "" {
		if err := runFeatureStart(workItem); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start work item %s: %v\n", workItem, err)
		}
	}

	// Resolve worktree path.
	workDir := projectRoot
	htmlgraphRoot := ""
	switch {
	case worktreePath != "":
		// Explicit path — use as-is; set ERINN_PROJECT_DIR to canonical root.
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

	fmt.Println("Launching Codex CLI with HtmlGraph context...")
	return execCodex(codexLaunchOpts{
		ResumeID:      resumeID,
		ExtraArgs:     extraArgs,
		ProjectRoot:   workDir,
		WorktreeRoot:  workDir,
		HtmlgraphRoot: htmlgraphRoot,
	})
}

// runFeatureStart runs `htmlgraph feature start <id>` for work item attribution.
func runFeatureStart(id string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine executable: %w", err)
	}
	cmd := exec.Command(exe, "feature", "start", id)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// launchCodexContinue resumes the last Codex session.
// Corresponds to: htmlgraph codex --continue
func launchCodexContinue(resumeID string, extraArgs []string) error {
	projectRoot, _ := resolveProjectRoot()
	fmt.Println("Resuming last Codex session...")
	return execCodex(codexLaunchOpts{
		ResumeLast:  resumeID == "", // only pass --last when no specific ID
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})
}

// launchCodexDev registers the local packages/codex-marketplace/ and launches Codex.
// Corresponds to: htmlgraph codex --dev [--cleanup]
// If a mismatched marketplace is already registered (e.g., from a prior --init),
// it is removed and replaced with the local path.
func launchCodexDev(resumeID string, cleanup, dryRun bool, extraArgs []string) error {
	// Resolve the local marketplace path relative to the project root.
	localMarketplace, err := resolveLocalCodexMarketplace()
	if err != nil {
		return err
	}

	fmt.Printf("Launching Codex CLI in dev mode...\n")
	fmt.Printf("  Local marketplace: %s\n", localMarketplace)

	// Ensure the local marketplace is registered (replace mismatched registrations).
	configPath := codexConfigPath()
	registeredPath := getCodexMarketplacePathAt(configPath)

	// Convert to absolute paths for comparison
	localAbs, _ := filepath.Abs(localMarketplace)
	registeredAbs, _ := filepath.Abs(registeredPath)

	if registeredAbs != "" && registeredAbs != localAbs {
		// Mismatched registration: remove the old one via direct TOML editing
		oldPathDisplay := registeredPath
		if oldPathDisplay == "" {
			oldPathDisplay = "(unknown previous path)"
		}
		fmt.Printf("Replacing mismatched marketplace registration (%s)\n", oldPathDisplay)
		if dryRun {
			fmt.Printf("[dry-run] would remove HtmlGraph registrations from %s\n", configPath)
		} else {
			removed, rmErr := removeCodexHtmlgraphRegistrations(configPath)
			if rmErr != nil {
				return fmt.Errorf("removing mismatched marketplace from %s: %w", configPath, rmErr)
			}
			if removed {
				fmt.Println("Mismatched registration removed from config.toml.")
			}
		}
		registeredPath = "" // Force re-add
	}

	// Add the local marketplace if not already registered at the correct path
	if registeredAbs != localAbs {
		addArgs := codexMarketplaceAddArgs(localMarketplace, "")
		if dryRun {
			fmt.Printf("[dry-run] codex %s\n", strings.Join(addArgs, " "))
		} else {
			if out, err := exec.Command("codex", addArgs...).CombinedOutput(); err != nil {
				return fmt.Errorf("registering local marketplace failed: %w\n%s", err, strings.TrimSpace(string(out)))
			}
			fmt.Println("Local marketplace registered.")
		}
	} else {
		fmt.Println("Local marketplace already registered — proceeding.")
	}

	projectRoot, _ := resolveProjectRoot()

	if dryRun {
		fmt.Printf("[dry-run] would exec: codex (resume=%q) in %s\n", resumeID, projectRoot)
		return nil
	}

	err = execCodex(codexLaunchOpts{
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})

	// --cleanup: unregister the local marketplace after session ends.
	if cleanup && !dryRun {
		fmt.Println("Cleaning up local marketplace registration...")
		removed, rmErr := removeCodexHtmlgraphRegistrations(configPath)
		if rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove marketplace registration: %v\n", rmErr)
		} else if !removed {
			fmt.Println("No HtmlGraph registrations found to clean up.")
		}
	}

	return err
}

// resolveLocalCodexMarketplace returns the absolute path to packages/codex-marketplace/
// by walking up from CWD to find the project root (directory containing .erinn/).
// Returns an error if no project root is found or the marketplace directory is missing.
func resolveLocalCodexMarketplace() (string, error) {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return "", fmt.Errorf("could not find project root (.erinn/ directory not found)\n" +
			"Run from the HtmlGraph project directory, or use htmlgraph codex --init for the marketplace version")
	}
	projectRoot := filepath.Dir(htmlgraphDir)
	marketplacePath := filepath.Join(projectRoot, "packages", "codex-marketplace")
	if _, statErr := os.Stat(marketplacePath); os.IsNotExist(statErr) {
		return "", fmt.Errorf("packages/codex-marketplace/ not found at %s\n"+
			"Run from the HtmlGraph repo root, or use htmlgraph codex --init for the marketplace version",
			marketplacePath)
	}
	abs, err := filepath.Abs(marketplacePath)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", marketplacePath, err)
	}
	return abs, nil
}

// codexLaunchOpts controls how Codex is launched.
type codexLaunchOpts struct {
	// ResumeLast, when true, passes "resume --last" to codex.
	ResumeLast bool
	// ResumeID, if non-empty, passes "resume <id>" to codex.
	// Takes precedence over ResumeLast.
	ResumeID string
	// ExtraArgs are forwarded to the codex process.
	ExtraArgs []string
	// ProjectRoot is the absolute path to the project root (or worktree path).
	// When set, Codex is started with this as the working directory, and
	// ERINN_PROJECT_DIR env var is injected.
	ProjectRoot string
	// WorktreeRoot, when non-empty, overrides the working directory for the
	// Codex process. The process runs in WorktreeRoot but ERINN_PROJECT_DIR
	// is set to HtmlgraphRoot (the canonical project root with .erinn/).
	WorktreeRoot string
	// HtmlgraphRoot is the canonical project root containing .erinn/.
	// Used to set ERINN_PROJECT_DIR when running in a worktree.
	HtmlgraphRoot string
}

// execCodex builds the codex argv and execs it, replacing the current process.
// Returns only on exec error.
func execCodex(opts codexLaunchOpts) error {
	codexPath, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("codex not found in PATH: %w\nInstall Codex CLI first: https://github.com/openai/codex", err)
	}

	var codexArgs []string

	// Determine if we're resuming.
	if opts.ResumeID != "" {
		codexArgs = append(codexArgs, "resume", opts.ResumeID)
	} else if opts.ResumeLast {
		codexArgs = append(codexArgs, "resume", "--last")
	}

	codexArgs = append(codexArgs, opts.ExtraArgs...)

	c := exec.Command(codexPath, codexArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Resolve the effective project dir for OTel collector spawning.
	effectiveProjDir := opts.ProjectRoot
	if opts.HtmlgraphRoot != "" {
		effectiveProjDir = opts.HtmlgraphRoot
	}

	// Spawn a per-session OTel collector when a project dir is known and OTel
	// is not explicitly disabled. Non-fatal: falls back gracefully on failure.
	var otelPort int
	var otelSessionID string
	var otelCleanup func()
	if effectiveProjDir != "" && !isExplicitlyDisabled(os.Getenv("ERINN_OTEL_ENABLED")) {
		otelPort, otelSessionID, otelCleanup = spawnCodexOtelCollector(effectiveProjDir)
		if otelCleanup != nil {
			defer otelCleanup()
		}
	}

	// Build the child env: start from os.Environ, inject ERINN_PROJECT_DIR,
	// and layer OTel exporter vars when a collector was spawned.
	env := os.Environ()
	workDir := ""

	switch {
	case opts.WorktreeRoot != "":
		projectDir := opts.HtmlgraphRoot
		if projectDir == "" {
			projectDir = opts.ProjectRoot
		}
		env = setOrReplaceEnv(env, "ERINN_PROJECT_DIR", projectDir)
		workDir = opts.WorktreeRoot
	case opts.ProjectRoot != "":
		env = setOrReplaceEnv(env, "ERINN_PROJECT_DIR", opts.ProjectRoot)
		workDir = opts.ProjectRoot
	}

	env = buildCodexOtelEnv(env, otelPort, otelSessionID)
	c.Env = env
	if workDir != "" {
		c.Dir = workDir
	}

	return runHarnessWithCleanup(c, otelCleanup)
}
