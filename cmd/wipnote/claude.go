package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/slug"
	"github.com/shakestzd/wipnote/internal/launcher"
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
	// ProjectRoot is the absolute path to the project root (directory containing .wipnote/).
	// When set, Claude Code is started with this as the working directory, and path-sensitive
	// helpers (writeLaunchMarker, etc.) anchor their paths here instead of CWD.
	ProjectRoot string
	// WipnoteRoot, if set, is the main repo root containing the canonical .wipnote/.
	// Used when ProjectRoot is a worktree — all work item tracking resolves to this path
	// instead of the worktree copy. Injected as WIPNOTE_PROJECT_DIR env var.
	WipnoteRoot string
	// Intent records whether the launcher is starting new work or continuing
	// existing work. Harness-neutral contract for downstream launchers.
	Intent launcher.LaunchIntent
	// ExtraEnv is layered onto the child environment after the launcher builds
	// its standard wipnote + telemetry overrides.
	ExtraEnv []string
}

func claudeCmd() *cobra.Command {
	var dev, init_, continue_, auto, tmux bool
	var resumeID, name string
	// Isolation flags (slice-2). --no-worktree and --in-place are mutually equivalent;
	// --in-place is the preferred semantic name going forward.
	var noWorktree, inPlace bool
	var workItem, baseBranch string

	cmd := &cobra.Command{
		Use:   "claude",
		Short: "Launch Claude Code with wipnote",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Tmux wrap must happen before any side-effecting work.
			// When --tmux is set and we are not already inside tmux, this
			// replaces the current process with: tmux new-session -A -s wipnote-dev -- <argv without --tmux>
			// and never returns. If tmux is missing, an error is returned.
			// If we are already inside tmux (TMUX env set), this is a no-op.
			_ = tmux // flag is consumed via os.Args inspection in maybeTmuxWrap
			if err := maybeTmuxWrap("wipnote-dev"); err != nil {
				return err
			}
			// --no-worktree is a legacy alias for --in-place.
			effectiveInPlace := inPlace || noWorktree
			_ = baseBranch // reserved for slice-3+; accepted but not yet acted on
			switch {
			case dev:
				return launchClaudeDev(args, auto, resumeID, name, workItem, effectiveInPlace, continue_)
			case auto:
				return launchClaudeAuto(args, resumeID, name)
			case init_:
				return launchClaudeInit(args, resumeID, name)
			case continue_:
				return launchClaudeContinue(args, resumeID)
			default:
				return launchClaudeDefault(args, resumeID, name, workItem, effectiveInPlace)
			}
		},
	}
	cmd.Flags().BoolVar(&dev, "dev", false, "Launch with local Go plugin for development")
	cmd.Flags().BoolVar(&auto, "auto", false, "Launch with auto mode enabled (autonomous operation)")
	cmd.Flags().BoolVar(&init_, "init", false, "Launch with marketplace plugin installation")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume last session with marketplace plugin")
	cmd.Flags().BoolVar(&tmux, "tmux", false, "Wrap in a tmux session named 'wipnote-dev' (survives disconnects; reattaches on re-run)")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation; run in project root (alias for --in-place)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Intentional in-place mutation; preserves existing behavior, records opt-out of isolation")
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a specific Claude Code session by ID")
	cmd.Flags().StringVar(&name, "name", "", "Session label shown in Claude TUI (default: <project>-<timestamp>)")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for isolation planning (e.g. feat-15c458aa, trk-3719d8f3)")
	cmd.Flags().StringVar(&baseBranch, "base", "", "Base branch for managed worktree (advanced; default: current HEAD)")
	cmd.AddCommand(yoloCmd())
	return cmd
}

// defaultSessionName builds a default session label: <project-slug>-<timestamp>.
// projectRoot may be empty, in which case the label is just the timestamp.
func defaultSessionName(projectRoot string) string {
	ts := time.Now().Format("20060102-15.04.05")
	if projectRoot == "" {
		return ts
	}
	projectSlug := slug.Make(filepath.Base(projectRoot), 30)
	if projectSlug == "" {
		return ts
	}
	return projectSlug + "-" + ts
}

// removeMarketplaceWipnote fully removes the wipnote marketplace plugin so it
// cannot shadow --plugin-dir agents/skills during dev mode. Belt-and-braces:
// uninstall removes the install record, disable flips the enabled flag, and
// RemoveAll wipes any cloned/cached files that linger even after uninstall.
func removeMarketplaceWipnote() {
	fmt.Println("Removing marketplace wipnote plugin for dev mode...")
	// Legacy htmlgraph scopes are still removed so old marketplace installs
	// cannot shadow local wipnote dev plugins.
	for _, scope := range []string{"wipnote@wipnote", "wipnote@local-marketplace", "htmlgraph@htmlgraph", "htmlgraph@local-marketplace"} {
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
		filepath.Join(home, ".claude", "plugins", "marketplaces", "wipnote"),
		filepath.Join(home, ".claude", "plugins", "cache", "wipnote"),
		filepath.Join(home, ".claude", "plugins", "cache", "local-marketplace", "wipnote"),
		filepath.Join(home, ".claude", "plugins", "marketplaces", "htmlgraph"),
		filepath.Join(home, ".claude", "plugins", "cache", "htmlgraph"),
		filepath.Join(home, ".claude", "plugins", "cache", "local-marketplace", "htmlgraph"),
	}
	for _, dir := range marketplaceDirs {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stdout, "warning: could not remove %s: %v\n", dir, err)
		}
	}
	fmt.Println("Marketplace wipnote removed (uninstalled, disabled, cache wiped).")
}

func launchClaudeDev(extraArgs []string, auto bool, resumeID, name, workItem string, inPlace, continue_ bool) error {
	if err := requireWipnoteOnPath(); err != nil {
		return err
	}

	// Resolve the source project root (the wipnote repo with plugin/).
	// This must happen BEFORE intent resolution so cleanupStaleDev and
	// resolveProjectPluginDirFrom can anchor to the source tree, not CWD.
	projectRoot := ""
	if wipnoteDir, err := findWipnoteDir(); err == nil {
		projectRoot = filepath.Dir(wipnoteDir)
	}

	// Clean up any leftover symlink state from a previous dev mode crash.
	cleanupStaleDev(projectRoot)

	// NOTE: removeMarketplaceWipnote() is intentionally deferred to just before
	// launchClaude — wiping the marketplace plugin before intent/isolation
	// resolution would leave the user without a working plugin if the launch
	// aborts (e.g. enforceLaunchPlan RefuseLaunch). See bug-da10ac25 finding C2.

	// Resolve the in-tree plugin/ from the source root NOW, before intent
	// resolution might redirect childDir to a worktree. The plugin source always
	// lives in the wipnote source tree, never in a linked worktree.
	pluginDir, err := devLaunchPluginDir(projectRoot)
	if err != nil {
		return err
	}
	// Verify expected plugin structure.
	if _, err := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); os.IsNotExist(err) {
		return fmt.Errorf("plugin.json not found at %s",
			filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	}

	// Resolve canonical root (non-empty only when projectRoot is a linked worktree).
	wipnoteRoot := canonicalProjectRoot(projectRoot)

	// Run the intent chooser and isolation planner — same path as launchClaudeDefault.
	// continue_ suppresses the chooser (explicit "resume most recent" intent) and is
	// honored below via Resume: true on the launch opts.
	lctx, err := resolveClaudeIntentIsolation(projectRoot, wipnoteRoot, resumeID, workItem, inPlace, continue_, extraArgs)
	if err != nil {
		return err
	}
	resumeID = lctx.intentResult.resumeID

	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	// continue_ is also excluded: --continue resumes the most-recent session and
	// must not get a synthesized --name emitted alongside the implicit resume.
	if sessionName == "" && resumeID == "" && !continue_ {
		sessionName = defaultSessionName(projectRoot)
	}

	if auto {
		fmt.Printf("Launching Claude Code with local plugin (--plugin-dir mode) + auto mode\n")
	} else {
		fmt.Printf("Launching Claude Code with local plugin (--plugin-dir mode)\n")
	}
	fmt.Printf("  Plugin source: %s\n", pluginDir)
	fmt.Printf("  Session: %s\n", sessionName)

	// Nuke marketplace plugin so it can't shadow the --plugin-dir agents/skills.
	// Deferred to here (after intent + isolation resolution succeeded) so an
	// aborted launch never leaves the user with a wiped plugin (bug-da10ac25 C2).
	removeMarketplaceWipnote()

	return launchClaude(LaunchOpts{
		// Mode is always "go" for dev sessions: it identifies the dev-plugin
		// launcher type (opts.PluginDir != "" && opts.Mode == "go") for
		// computeLauncherMode and writeLaunchMarker. The intent's continue/new
		// distinction is captured via ResumeID and Intent — not the mode string.
		Mode:      "go",
		PluginDir: pluginDir,
		// Resume the most recent session when --dev --continue was requested
		// (mirrors launchClaudeContinue). ResumeID, when set, still takes
		// precedence in launchClaude's arg construction.
		Resume:             continue_,
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		EnableAutoMode:     auto,
		PermissionMode:     autoPermissionMode(auto),
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		// childDir is the worktree (or projectRoot when in-place). Claude Code
		// runs here so the agent works on the right branch, but WIPNOTE_PROJECT_DIR
		// still points at the canonical source root via WipnoteRoot.
		ProjectRoot: lctx.childDir,
		WipnoteRoot: lctx.wipnoteRoot,
		Intent:      lctx.intentResult.intent,
		ExtraEnv:    lctx.continueEnv,
	})
}

func requireWipnoteOnPath() error {
	if _, err := exec.LookPath("wipnote"); err != nil {
		return fmt.Errorf("wipnote binary not found on PATH\nBuild with: wipnote build")
	}
	return nil
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
	// Resolve canonical main repo root when CWD is a linked worktree (slice-3).
	wipnoteRoot := canonicalProjectRoot(projectRoot)
	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}
	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	if sessionName == "" && resumeID == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Println("Launching Claude Code in auto mode (autonomous operation)...")
	fmt.Println("  Actions will be approved by the background classifier, not prompted.")
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               "auto",
		PluginDir:          pluginDir,
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		EnableAutoMode:     true,
		PermissionMode:     "auto",
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
		WipnoteRoot:        wipnoteRoot,
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
	base := ".wipnote"
	if projectRoot != "" {
		base = filepath.Join(projectRoot, ".wipnote")
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
	// --init always uses CWD — never walk up to a parent with .wipnote/.
	// The user explicitly wants to work in THIS directory, which may not
	// have .wipnote/ yet. Walk-up would anchor to the wrong project.
	projectRoot, _ := os.Getwd()
	cleanupStaleDev(projectRoot)
	// Resolve canonical main repo root when CWD is a linked worktree (slice-3).
	wipnoteRoot := canonicalProjectRoot(projectRoot)
	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}
	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	if sessionName == "" && resumeID == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Println("Launching Claude Code with bundled wipnote plugin (init mode)...")
	fmt.Printf("  Plugin: %s\n", pluginDir)
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               "init",
		PluginDir:          pluginDir,
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
		WipnoteRoot:        wipnoteRoot,
	})
}

func launchClaudeContinue(extraArgs []string, resumeID string) error {
	projectRoot, _ := resolveProjectRoot()
	cleanupStaleDev(projectRoot)
	// Resolve canonical main repo root when CWD is a linked worktree (slice-3).
	wipnoteRoot := canonicalProjectRoot(projectRoot)
	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}
	fmt.Println("Resuming last Claude Code session (continue mode)...")
	return launchClaude(LaunchOpts{
		Mode:        "continue",
		PluginDir:   pluginDir,
		Resume:      true,
		ResumeID:    resumeID,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		WipnoteRoot: wipnoteRoot,
		Intent:      launcher.ContinueWorkIntent("", "claude", resumeID, "", true),
	})
}

func launchClaudeDefault(extraArgs []string, resumeID, name, workItem string, inPlace bool) error {
	projectRoot, _ := resolveProjectRoot()
	cleanupStaleDev(projectRoot)

	// Resolve canonical main repo root when CWD is a linked worktree (slice-3).
	// canonicalProjectRoot returns "" for the main worktree (no override needed).
	wipnoteRoot := canonicalProjectRoot(projectRoot)

	lctx, err := resolveClaudeIntentIsolation(projectRoot, wipnoteRoot, resumeID, workItem, inPlace, false, extraArgs)
	if err != nil {
		return err
	}
	resumeID = lctx.intentResult.resumeID

	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}
	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	if sessionName == "" && resumeID == "" {
		sessionName = defaultSessionName(projectRoot)
	}
	fmt.Printf("Launching Claude Code (%s mode)...\n", lctx.intentResult.mode)
	fmt.Printf("  Plugin: %s\n", pluginDir)
	fmt.Printf("  Session: %s\n", sessionName)
	return launchClaude(LaunchOpts{
		Mode:               lctx.intentResult.mode,
		PluginDir:          pluginDir,
		ResumeID:           resumeID,
		InjectSystemPrompt: true,
		Name:               sessionName,
		ExtraArgs:          extraArgs,
		ProjectRoot:        lctx.childDir,
		WipnoteRoot:        lctx.wipnoteRoot,
		Intent:             lctx.intentResult.intent,
		ExtraEnv:           lctx.continueEnv,
	})
}

// resolveBundledPluginDir resolves the path to the bundled wipnote Claude
// plugin tree installed by `wipnote build` or `brew install wipnote`. It
// is the Phase-B replacement for relying on the marketplace plugin install.
// Dev mode (--dev) bypasses this and uses the in-tree plugin/ directly.
func resolveBundledPluginDir() (string, error) {
	return resolveSharedTreePath("plugin")
}

const wipnoteMarketplaceRepo = "shakestzd/wipnote"

// ensureWipnotePlugin registers the wipnote marketplace (if needed) and
// installs or updates the plugin. Returns an error if both install and update fail.
func ensureWipnotePlugin() error {
	// Step 1: Register marketplace if not already known.
	fmt.Println("Registering wipnote marketplace...")
	exec.Command("claude", "plugin", "marketplace", "add",
		wipnoteMarketplaceRepo).Run() //nolint:errcheck

	// Step 2: Try install, fall back to update.
	fmt.Println("Installing/updating wipnote plugin...")
	if out, err := exec.Command("claude", "plugin", "install", "wipnote@wipnote").CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("claude", "plugin", "update", "wipnote").CombinedOutput(); err2 != nil {
			return fmt.Errorf("plugin install failed: %s\nplugin update failed: %s",
				strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

// launchClaude is the shared launcher used by all modes.
func launchClaude(opts LaunchOpts) error {
	// Compute launcher mode for preflight logging/inspection (no behavior change).
	// devPlugin is true when a PluginDir was supplied in go/dev mode.
	_ = computeLauncherMode(opts.WipnoteRoot, opts.PluginDir != "" && opts.Mode == "go", false)

	// Write launch marker to the main project root, not the worktree.
	markerRoot := opts.ProjectRoot
	if opts.WipnoteRoot != "" {
		markerRoot = opts.WipnoteRoot
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
	// Append the shared research-routing disposition exactly once. Source of
	// truth: cmd/wipnote/prompts/research-routing.md (mirrored for subagents in
	// the agent-context skill).
	if systemPrompt != "" {
		systemPrompt = strings.TrimSpace(systemPrompt) + "\n\n" + strings.TrimSpace(researchRoutingContent) + "\n"
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

	// Launch-time guard-profile initialization (peer to the OTel collector
	// bootstrap). No-op when already approved or non-interactive; never blocks.
	ensureGuardProfile(opts.ProjectRoot)

	// Auto-start a detached `wipnote serve` for the dashboard and
	// semantic-ops (AI-title backfill, etc.). The serve process is now a
	// pure reader + dashboard server — OTLP ingest is handled by the
	// per-session collector spawned below. See claude_serve_autostart.go
	// for the probe + spawn logic.
	ensureServeForDashboard(opts.ProjectRoot)

	// Generate a per-session ID and spawn a per-session OTel collector.
	// The collector writes NDJSON to .wipnote/sessions/<sid>/ and
	// exposes an ephemeral OTLP HTTP port. Non-fatal: on failure, the
	// existing serve-based receiver is used as fallback.
	var envOverrides otelEnvOverrides
	if opts.ProjectRoot != "" && !isExplicitlyDisabled(os.Getenv("WIPNOTE_OTEL_ENABLED")) {
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
	// WIPNOTE_PROJECT_DIR when running in a worktree, and layer OTel
	// exporter vars when WIPNOTE_OTEL_ENABLED=1 so Claude's OTLP
	// pipeline points at the wipnote serve receiver. See
	// claude_env.go:buildClaudeLaunchEnv for precedence rules (user-set
	// OTEL_* always wins).
	var worktreeOverride string
	if opts.WipnoteRoot != "" && opts.WipnoteRoot != opts.ProjectRoot {
		worktreeOverride = opts.WipnoteRoot
	}
	c.Env = buildClaudeLaunchEnv(worktreeOverride, &envOverrides)
	c.Env = mergeLauncherEnv(c.Env, opts.ExtraEnv...)

	// Set working directory to project root so Claude starts in the right place,
	// even if this command is run from a subdirectory like packages/go.
	if opts.ProjectRoot != "" {
		c.Dir = opts.ProjectRoot
	}

	return runHarnessWithCleanup(c, envOverrides.Cleanup)
}

// Harness identifiers for the WIPNOTE_HARNESS env var (feat-9348de66 slice 1).
// These are the closed enum the SessionStart hook accepts; values outside it
// are rejected at the write path. Each launcher stamps its own constant:
// claude.go uses harnessClaude here; codex/gemini/antigravity launchers wire
// their own in later slices.
const (
	harnessEnvKey      = "WIPNOTE_HARNESS"
	harnessClaude      = "claude"
	harnessCodex       = "codex"
	harnessGemini      = "gemini"
	harnessAntigravity = "antigravity"
)

// withHarnessEnv returns env with WIPNOTE_HARNESS set to harness, replacing any
// inherited value (launcher-authoritative). Shared across launchers so each
// harness stamps a single source of truth. Reuses setOrReplaceEnv (claude_env.go).
func withHarnessEnv(env []string, harness string) []string {
	return setOrReplaceEnv(env, harnessEnvKey, harness)
}

// writeLaunchMarker writes .wipnote/.launch-mode for hooks to detect the launch mode.
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
	dir := filepath.Join(projectRoot, ".wipnote")
	os.MkdirAll(dir, 0755)                                       //nolint:errcheck
	os.WriteFile(filepath.Join(dir, ".launch-mode"), data, 0644) //nolint:errcheck
}
