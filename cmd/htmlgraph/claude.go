package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/slug"
	"github.com/spf13/cobra"
)

// devModeBackup records the state we swapped out during dev mode so we can
// restore it after the session (or after a crash on next startup).
type devModeBackup struct {
	InstallPath    string `json:"installPath"`
	BackupPath     string `json:"backupPath"`
	WasEnabled     bool   `json:"wasEnabled"`
	PluginKey      string `json:"pluginKey"`
	HadInstallPath bool   `json:"hadInstallPath"`
}

// LaunchOpts controls how Claude Code is launched.
type LaunchOpts struct {
	// Mode is written to the launch marker (e.g. "go", "init", "continue", "default").
	Mode string
	// PluginDir, if non-empty, passes --plugin-dir to claude.
	PluginDir string
	// Resume adds --resume to claude args (for --continue mode).
	Resume bool
	// ResumeID, if non-empty, passes --resume <id> to claude to resume a specific session.
	// Takes precedence over Resume.
	ResumeID string
	// InjectSystemPrompt, when true, appends the embedded system prompt via
	// --append-system-prompt. Ignored when SystemPromptFile is set.
	InjectSystemPrompt bool
	// SystemPromptFile, if set, reads this file and appends it as system prompt.
	// Takes precedence over InjectSystemPrompt.
	SystemPromptFile string
	// PermissionMode, if set, passes --permission-mode to claude (e.g. "bypassPermissions").
	PermissionMode string
	// EnableAutoMode, when true, passes --enable-auto-mode to claude.
	EnableAutoMode bool
	// Name, if set, passes --name to claude for session naming.
	Name string
	// ExtraArgs are forwarded to the claude process.
	ExtraArgs []string
	// ProjectRoot is the absolute path to the project root (directory containing .htmlgraph/).
	// When set, Claude Code is started with this as the working directory, and path-sensitive
	// helpers (writeLaunchMarker, etc.) anchor their paths here instead of CWD.
	ProjectRoot string
	// HtmlgraphRoot, if set, is the main repo root containing the canonical .htmlgraph/.
	// Used when ProjectRoot is a worktree — all work item tracking resolves to this path
	// instead of the worktree copy. Injected as HTMLGRAPH_PROJECT_DIR env var.
	HtmlgraphRoot string
}

func claudeCmd() *cobra.Command {
	var dev, init_, continue_, auto, tmux bool
	var resumeID, name string

	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Launch Claude Code with HtmlGraph",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Tmux wrap must happen before any side-effecting work.
			// When --tmux is set and we are not already inside tmux, this
			// replaces the current process with: tmux new-session -A -s htmlgraph-dev -- <argv without --tmux>
			// and never returns. If tmux is missing, an error is returned.
			// If we are already inside tmux (TMUX env set), this is a no-op.
			_ = tmux // flag is consumed via os.Args inspection in maybeTmuxWrap
			if err := maybeTmuxWrap("htmlgraph-dev"); err != nil {
				return err
			}
			switch {
			case dev:
				return launchClaudeDev(args, auto, resumeID, name)
			case auto:
				return launchClaudeAuto(args, resumeID, name)
			case init_:
				return launchClaudeInit(args, resumeID, name)
			case continue_:
				return launchClaudeContinue(args, resumeID)
			default:
				return launchClaudeDefault(args, resumeID, name)
			}
		},
	}
	cmd.Flags().BoolVar(&dev, "dev", false, "Launch with local Go plugin for development")
	cmd.Flags().BoolVar(&auto, "auto", false, "Launch with auto mode enabled (autonomous operation)")
	cmd.Flags().BoolVar(&init_, "init", false, "Launch with marketplace plugin installation")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume last session with marketplace plugin")
	cmd.Flags().BoolVar(&tmux, "tmux", false, "Wrap in a tmux session named 'htmlgraph-dev' (survives disconnects; reattaches on re-run)")
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a specific Claude Code session by ID")
	cmd.Flags().StringVar(&name, "name", "", "Session label shown in Claude TUI (default: <project>-<timestamp>)")
	cmd.AddCommand(yoloCmd())
	return cmd
}

// defaultSessionName builds a default session label: <project-slug>-<timestamp>.
// projectRoot may be empty, in which case the label is just the timestamp.
func defaultSessionName(projectRoot string) string {
	ts := time.Now().UTC().Format("20060102-150405")
	if projectRoot == "" {
		return ts
	}
	projectSlug := slug.Make(filepath.Base(projectRoot), 30)
	if projectSlug == "" {
		return ts
	}
	return projectSlug + "-" + ts
}

// removeMarketplaceHtmlgraph fully removes the htmlgraph marketplace plugin so it
// cannot shadow --plugin-dir agents/skills during dev mode. Belt-and-braces:
// uninstall removes the install record, disable flips the enabled flag, and
// RemoveAll wipes any cloned/cached files that linger even after uninstall.
func removeMarketplaceHtmlgraph() {
	fmt.Println("Removing marketplace htmlgraph plugin for dev mode...")
	for _, scope := range []string{"htmlgraph@htmlgraph", "htmlgraph@local-marketplace"} {
		if out, err := exec.Command("claude", "plugin", "uninstall", scope).CombinedOutput(); err != nil {
			msg := strings.ToLower(strings.TrimSpace(string(out)))
			if !strings.Contains(msg, "not found") && !strings.Contains(msg, "not installed") && !strings.Contains(msg, "already uninstalled") {
				fmt.Fprintf(os.Stdout, "warning: plugin uninstall %s: %v (%s)\n", scope, err, strings.TrimSpace(string(out)))
			}
		}
		if out, err := exec.Command("claude", "plugin", "disable", scope).CombinedOutput(); err != nil {
			msg := strings.ToLower(strings.TrimSpace(string(out)))
			if !strings.Contains(msg, "not found") && !strings.Contains(msg, "not installed") && !strings.Contains(msg, "already disabled") {
				fmt.Fprintf(os.Stdout, "warning: plugin disable %s: %v (%s)\n", scope, err, strings.TrimSpace(string(out)))
			}
		}
	}
	home, _ := os.UserHomeDir()
	marketplaceDirs := []string{
		filepath.Join(home, ".claude", "plugins", "marketplaces", "htmlgraph"),
		filepath.Join(home, ".claude", "plugins", "cache", "htmlgraph"),
		filepath.Join(home, ".claude", "plugins", "cache", "local-marketplace", "htmlgraph"),
	}
	for _, dir := range marketplaceDirs {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stdout, "warning: could not remove %s: %v\n", dir, err)
		}
	}
	fmt.Println("Marketplace htmlgraph removed (uninstalled, disabled, cache wiped).")
}

func launchClaudeDev(extraArgs []string, auto bool, resumeID, name string) error {
	// Dev mode resolves the plugin from local source, NOT the marketplace.
	// resolveProjectPluginDir walks up from CWD to find plugin/.claude-plugin/plugin.json.
	pluginDir := resolveProjectPluginDir()
	if pluginDir == "" {
		return fmt.Errorf("could not find plugin/ directory relative to project root. Run from the project directory containing .htmlgraph/ and plugin/")
	}
	// Verify expected plugin structure.
	if _, err := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); os.IsNotExist(err) {
		return fmt.Errorf("plugin.json not found at %s",
			filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	}
	if _, err := exec.LookPath("htmlgraph"); err != nil {
		return fmt.Errorf("htmlgraph binary not found on PATH\nBuild with: htmlgraph build (or plugin/build.sh)")
	}

	// Resolve project root so paths are anchored correctly regardless of CWD.
	projectRoot := ""
	if htmlgraphDir, err := findHtmlgraphDir(); err == nil {
		projectRoot = filepath.Dir(htmlgraphDir)
	}

	// Clean up any leftover symlink state from a previous dev mode crash.
	cleanupStaleDev(projectRoot)

	// Nuke marketplace plugin so it can't shadow the --plugin-dir agents/skills.
	removeMarketplaceHtmlgraph()

	sessionName := name
	if sessionName == "" {
		sessionName = defaultSessionName(projectRoot)
	}

	if auto {
		fmt.Printf("Launching Claude Code with local plugin (--plugin-dir mode) + auto mode\n")
	} else {
		fmt.Printf("Launching Claude Code with local plugin (--plugin-dir mode)\n")
	}
	fmt.Printf("  Plugin source: %s\n", pluginDir)
	fmt.Printf("  Session: %s\n", sessionName)

	return launchClaude(LaunchOpts{
		Mode:               "go",
		PluginDir:          pluginDir,
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		EnableAutoMode:     auto,
		PermissionMode:     autoPermissionMode(auto),
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
	})
}

// autoPermissionMode returns "auto" when enabled is true, otherwise empty string.
// This avoids passing --permission-mode when auto mode is not requested.
func autoPermissionMode(enabled bool) string {
	if enabled {
		return "auto"
	}
	return ""
}

// launchClaudeAuto launches Claude Code with auto mode enabled for autonomous operation.
// It uses the marketplace plugin (like normal mode) but adds --enable-auto-mode and
// --permission-mode auto so Claude starts in autonomous operation immediately.
func launchClaudeAuto(extraArgs []string, resumeID, name string) error {
	projectRoot, _ := resolveProjectRoot()
	cleanupStaleDev(projectRoot)
	ensurePluginOnLaunch()
	sessionName := name
	if sessionName == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Println("Launching Claude Code in auto mode (autonomous operation)...")
	fmt.Println("  Actions will be approved by the background classifier, not prompted.")
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               "auto",
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		EnableAutoMode:     true,
		PermissionMode:     "auto",
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
	})
}

// installedPluginsJSON is the path to the Claude Code installed plugins registry.
func installedPluginsJSONPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
}

// claudeSettingsJSONPath is the path to the Claude Code user settings file.
func claudeSettingsJSONPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// devModeBackupPath returns the path to the dev-mode backup state file.
func devModeBackupPath(projectRoot string) string {
	base := ".htmlgraph"
	if projectRoot != "" {
		base = filepath.Join(projectRoot, ".htmlgraph")
	}
	return filepath.Join(base, ".dev-mode-backup")
}

// restoreFromSymlink removes the dev-mode symlink and restores the backup.
// Kept for cleanupStaleDev to recover from old symlink-based dev mode sessions.
func restoreFromSymlink(installPath, backupPath, pluginKey string, wasEnabled bool, backupStateFile string) {
	// Remove the symlink.
	if err := os.Remove(installPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: could not remove dev symlink %s: %v\n", installPath, err)
	}

	// Restore backup if it exists.
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Rename(backupPath, installPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not restore %s from %s: %v\n", installPath, backupPath, err)
		}
	}

	// Restore enabled state in settings.json.
	if err := setPluginEnabled(pluginKey, wasEnabled); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not restore plugin enabled state: %v\n", err)
	}

	// Remove the backup state file.
	os.Remove(backupStateFile) //nolint:errcheck

	fmt.Println("Dev mode cleanup complete.")
}

// setPluginEnabled sets enabledPlugins[key] = enabled in ~/.claude/settings.json.
func setPluginEnabled(key string, enabled bool) error {
	settingsPath := claudeSettingsJSONPath()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("reading settings.json: %w", err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("parsing settings.json: %w", err)
	}

	var ep map[string]bool
	if epRaw, ok := settings["enabledPlugins"]; ok {
		if err := json.Unmarshal(epRaw, &ep); err != nil {
			ep = make(map[string]bool)
		}
	} else {
		ep = make(map[string]bool)
	}

	ep[key] = enabled

	epBytes, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("marshalling enabledPlugins: %w", err)
	}
	settings["enabledPlugins"] = json.RawMessage(epBytes)

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings.json: %w", err)
	}
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		return fmt.Errorf("writing settings.json: %w", err)
	}
	return nil
}

// cleanupStaleDev checks for a leftover .dev-mode-backup file from a previous
// crash and restores the original plugin state if one is found.
func cleanupStaleDev(projectRoot string) {
	backupStateFile := devModeBackupPath(projectRoot)
	data, err := os.ReadFile(backupStateFile)
	if err != nil {
		return // No stale backup — nothing to do.
	}

	var backup devModeBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse stale dev-mode backup state: %v\n", err)
		return
	}

	fmt.Println("Found stale dev-mode state from previous crash — restoring...")
	restoreFromSymlink(backup.InstallPath, backup.BackupPath, backup.PluginKey, backup.WasEnabled, backupStateFile)
}

func launchClaudeInit(extraArgs []string, resumeID, name string) error {
	// --init always uses CWD — never walk up to a parent with .htmlgraph/.
	// The user explicitly wants to work in THIS directory, which may not
	// have .htmlgraph/ yet. Walk-up would anchor to the wrong project.
	projectRoot, _ := os.Getwd()
	cleanupStaleDev(projectRoot)
	ensurePluginOnLaunch()
	sessionName := name
	if sessionName == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Println("Launching Claude Code with marketplace plugin (init mode)...")
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               "init",
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
	})
}

func launchClaudeContinue(extraArgs []string, resumeID string) error {
	projectRoot, _ := resolveProjectRoot()
	cleanupStaleDev(projectRoot)
	ensurePluginOnLaunch()
	fmt.Println("Resuming last Claude Code session (continue mode)...")
	return launchClaude(LaunchOpts{
		Mode:        "continue",
		Resume:      true,
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
	})
}

func launchClaudeDefault(extraArgs []string, resumeID, name string) error {
	projectRoot, _ := resolveProjectRoot()
	cleanupStaleDev(projectRoot)
	ensurePluginOnLaunch()
	sessionName := name
	if sessionName == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Println("Launching Claude Code (default mode)...")
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               "default",
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
	})
}


const htmlgraphMarketplaceRepo = "shakestzd/htmlgraph"

// ensureHtmlgraphPlugin registers the htmlgraph marketplace (if needed) and
// installs or updates the plugin. Returns an error if both install and update fail.
func ensureHtmlgraphPlugin() error {
	// Step 1: Register marketplace if not already known.
	fmt.Println("Registering htmlgraph marketplace...")
	exec.Command("claude", "plugin", "marketplace", "add",
		htmlgraphMarketplaceRepo).Run() //nolint:errcheck

	// Step 2: Try install, fall back to update.
	fmt.Println("Installing/updating htmlgraph plugin...")
	if out, err := exec.Command("claude", "plugin", "install", "htmlgraph@htmlgraph").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("claude", "plugin", "update", "htmlgraph").CombinedOutput(); err2 != nil {
			return fmt.Errorf("plugin install failed: %s\nplugin update failed: %s",
				strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

// launchClaude is the shared launcher used by all modes.
func launchClaude(opts LaunchOpts) error {
	// Write launch marker to the main project root, not the worktree.
	markerRoot := opts.ProjectRoot
	if opts.HtmlgraphRoot != "" {
		markerRoot = opts.HtmlgraphRoot
	}
	writeLaunchMarker(opts.Mode, markerRoot)

	// SystemPromptFile takes precedence over InjectSystemPrompt.
	var systemPrompt string
	if opts.SystemPromptFile != "" {
		if data, err := os.ReadFile(opts.SystemPromptFile); err == nil {
			systemPrompt = string(data)
		}
	} else if opts.InjectSystemPrompt {
		systemPrompt = systemPromptContent
	}

	var claudeArgs []string
	if opts.ResumeID != "" {
		claudeArgs = append(claudeArgs, "--resume", opts.ResumeID)
	} else if opts.Resume {
		claudeArgs = append(claudeArgs, "--resume")
	}
	if opts.PluginDir != "" {
		claudeArgs = append(claudeArgs, "--plugin-dir", opts.PluginDir)
	}
	if opts.EnableAutoMode {
		claudeArgs = append(claudeArgs, "--enable-auto-mode")
	}
	if opts.PermissionMode != "" {
		claudeArgs = append(claudeArgs, "--permission-mode", opts.PermissionMode)
	}
	if opts.Name != "" {
		claudeArgs = append(claudeArgs, "--name", opts.Name)
	}
	if systemPrompt != "" {
		claudeArgs = append(claudeArgs, "--append-system-prompt", systemPrompt)
	}
	claudeArgs = append(claudeArgs, opts.ExtraArgs...)

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found in PATH: %w", err)
	}

	// Show the one-time OTel first-launch notice before starting the
	// server so users see the explanation before any server output.
	MaybeShowOtelNotice(opts.ProjectRoot)

	// Auto-start a detached `htmlgraph serve` for the dashboard and
	// semantic-ops (AI-title backfill, etc.). The serve process is now a
	// pure reader + dashboard server — OTLP ingest is handled by the
	// per-session collector spawned below. See claude_serve_autostart.go
	// for the probe + spawn logic.
	ensureServeForDashboard(opts.ProjectRoot)

	// Generate a per-session ID and spawn a per-session OTel collector.
	// The collector writes NDJSON to .htmlgraph/sessions/<sid>/ and
	// exposes an ephemeral OTLP HTTP port. Non-fatal: on failure, the
	// existing serve-based receiver is used as fallback.
	var envOverrides otelEnvOverrides
	if opts.ProjectRoot != "" && !isExplicitlyDisabled(os.Getenv("HTMLGRAPH_OTEL_ENABLED")) {
		envOverrides = spawnSessionCollector(opts.ProjectRoot)
		if envOverrides.Cleanup != nil {
			defer envOverrides.Cleanup()
		}
	}

	c := exec.Command(claudePath, claudeArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	// Compose the child env: start from os.Environ, layer
	// HTMLGRAPH_PROJECT_DIR when running in a worktree, and layer OTel
	// exporter vars when HTMLGRAPH_OTEL_ENABLED=1 so Claude's OTLP
	// pipeline points at the htmlgraph serve receiver. See
	// claude_env.go:buildClaudeLaunchEnv for precedence rules (user-set
	// OTEL_* always wins).
	var worktreeOverride string
	if opts.HtmlgraphRoot != "" && opts.HtmlgraphRoot != opts.ProjectRoot {
		worktreeOverride = opts.HtmlgraphRoot
	}
	c.Env = buildClaudeLaunchEnv(worktreeOverride, &envOverrides)

	// Set working directory to project root so Claude starts in the right place,
	// even if this command is run from a subdirectory like packages/go.
	if opts.ProjectRoot != "" {
		c.Dir = opts.ProjectRoot
	}

	if err := c.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// writeLaunchMarker writes .htmlgraph/.launch-mode for hooks to detect the launch mode.
// projectRoot must be non-empty; if it is empty the write is skipped to avoid
// polluting whatever directory the user happens to be in.
func writeLaunchMarker(mode, projectRoot string) {
	if projectRoot == "" {
		return // No project root — skip rather than polluting CWD
	}
	marker := map[string]any{
		"mode":      mode,
		"pid":       os.Getpid(),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return
	}
	dir := filepath.Join(projectRoot, ".htmlgraph")
	os.MkdirAll(dir, 0755)                                       //nolint:errcheck
	os.WriteFile(filepath.Join(dir, ".launch-mode"), data, 0644) //nolint:errcheck
}
