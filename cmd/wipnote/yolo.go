package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/internal/htmlparse"
	"github.com/shakestzd/wipnote/internal/slug"
	"github.com/shakestzd/wipnote/internal/workitem"
	"github.com/spf13/cobra"
)

func yoloCmd() *cobra.Command {
	var dev, initMode, continueMode, noWorktree, inPlace, tmux bool
	var permMode, trackID, featureID, resumeID, name string

	cmd := &cobra.Command{
		Use:   "yolo",
		Short: "Launch Claude Code in autonomous YOLO mode with development guardrails",
		Long: `Launches Claude Code with bypassPermissions and enforced quality guardrails.

YOLO mode removes permission prompts but enforces code quality at every step:
  - Mandatory TDD workflow (tests before implementation)
  - Quality gate checks before every commit
  - Budget limits to keep features focused
  - Worktree-per-feature isolation

Each session is auto-named with a timestamp for easy identification.

Requires --track or --feature to identify the work item for attribution.
Without either flag, launches in planning mode to help you create one first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Tmux wrap must happen before any side-effecting work.
			// When --tmux is set and we are not already inside tmux, this
			// replaces the current process with: tmux new-session -A -s wipnote-yolo -- <argv without --tmux>
			// and never returns. If tmux is missing, an error is returned.
			// If we are already inside tmux (TMUX env set), this is a no-op.
			_ = tmux // flag is consumed via os.Args inspection in maybeTmuxWrap
			if err := maybeTmuxWrap("wipnote-yolo"); err != nil {
				return err
			}
			switch {
			case dev:
				effectiveInPlaceDev := inPlace || noWorktree
				return launchYoloDev(trackID, featureID, effectiveInPlaceDev, resumeID, name, args)
			case initMode:
				return launchYoloInit(trackID, featureID, resumeID, name, args)
			case continueMode:
				return launchYoloContinue(args, resumeID)
			default:
				effectiveInPlace := inPlace || noWorktree
				return launchYoloDefault(permMode, trackID, featureID, effectiveInPlace, resumeID, name, args)
			}
		},
	}

	cmd.Flags().BoolVar(&dev, "dev", false, "Load plugin from local source (development mode)")
	cmd.Flags().BoolVar(&initMode, "init", false, "Initialize .wipnote/ then launch in YOLO mode")
	cmd.Flags().BoolVar(&continueMode, "continue", false, "Resume last YOLO session")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation; run in project root (alias for --in-place)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Intentional in-place mutation; records opt-out of isolation")
	cmd.Flags().BoolVar(&tmux, "tmux", false, "Wrap yolo in a tmux session named 'wipnote-yolo' (survives disconnects; reattaches on re-run)")
	cmd.Flags().StringVar(&permMode, "permission-mode", "bypassPermissions",
		"Permission mode (bypassPermissions, acceptEdits)")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on (e.g., trk-3719d8f3)")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&resumeID, "resume", "", "Resume a specific Claude Code session by ID")
	cmd.Flags().StringVar(&name, "name", "", "Session label shown in Claude TUI (default: <track-title>-yolo-<timestamp>)")
	return cmd
}

// yoloDefaultName builds the default session label for YOLO mode.
// When a trackID or featureID is provided, it resolves the track title and
// builds "<track-slug>-yolo-<timestamp>". Falls back to "<project-slug>-yolo-<timestamp>".
func yoloDefaultName(trackID, featureID, projectRoot string) string {
	ts := time.Now().Format("20060102-15.04.05")
	trackTitle := resolveTrackTitle(trackID, featureID, projectRoot)
	if trackTitle != "" {
		s := slug.Make(trackTitle, 30)
		if s != "" {
			return s + "-yolo-" + ts
		}
	}
	// Fallback: use project basename.
	if projectRoot != "" {
		s := slug.Make(filepath.Base(projectRoot), 30)
		if s != "" {
			return s + "-yolo-" + ts
		}
	}
	return "yolo-" + ts
}

// resolveTrackTitle returns the title of the track associated with the given
// trackID or featureID. Returns empty string if not resolvable.
func resolveTrackTitle(trackID, featureID, projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	// Resolve the track ID: either direct or via feature's parent track.
	tid := trackID
	if tid == "" && featureID != "" {
		tid = resolveTrackForFeature(featureID, projectRoot)
	}
	if tid == "" {
		return ""
	}
	trackFile := filepath.Join(projectRoot, ".wipnote", "tracks", tid+".html")
	node, err := htmlparse.ParseFile(trackFile)
	if err != nil {
		return ""
	}
	return node.Title
}

// validateWorkItem checks that a track or feature HTML file exists in .wipnote/.
// Returns the validated ID and item type, or an error.
func validateWorkItem(trackID, featureID, projectRoot string) (id, kind string, err error) {
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	switch {
	case trackID != "":
		htmlFile := filepath.Join(wipnoteDir, "tracks", trackID+".html")
		if _, statErr := os.Stat(htmlFile); os.IsNotExist(statErr) {
			return "", "", workitem.ErrNotFound("track", trackID)
		}
		return trackID, "track", nil
	case featureID != "":
		htmlFile := filepath.Join(wipnoteDir, "features", featureID+".html")
		if _, statErr := os.Stat(htmlFile); os.IsNotExist(statErr) {
			return "", "", workitem.ErrNotFound("feature", featureID)
		}
		return featureID, "feature", nil
	default:
		return "", "", nil
	}
}

// mergeAgentToTrack merges an agent branch back into its parent track branch
// and removes the agent worktree. This is the cleanup step after agent completion.
func mergeAgentToTrack(trackID, taskName, projectRoot string) error {
	agentBranch := "agent-" + trackID + "-" + taskName
	worktreePath := filepath.Join(projectRoot, ".claude", "worktrees", trackID, "agent-"+taskName)

	// Checkout track branch first
	checkoutCmd := exec.Command("git", "-C", projectRoot, "checkout", trackID)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout track branch failed: %w\n%s", err, out)
	}

	// Merge agent branch into track branch
	mergeCmd := exec.Command("git", "-C", projectRoot, "merge", "--no-ff", agentBranch,
		"-m", fmt.Sprintf("feat: merge agent-%s into %s", taskName, trackID))

	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("merge agent to track failed: %w\n%s", err, out)
	}

	// Remove the agent worktree
	removeCmd := exec.Command("git", "-C", projectRoot, "worktree", "remove", "--force", worktreePath)
	removeCmd.Run() //nolint:errcheck

	// Delete the agent branch
	deleteCmd := exec.Command("git", "-C", projectRoot, "branch", "-d", agentBranch)
	deleteCmd.Run() //nolint:errcheck

	return nil
}

// resolveTrackForFeature reads a feature HTML file and returns its data-track-id attribute.
// If the feature file doesn't exist or has no track ID, returns empty string.
func resolveTrackForFeature(featureID, projectRoot string) string {
	featureFile := filepath.Join(projectRoot, ".wipnote", "features", featureID+".html")
	node, err := htmlparse.ParseFile(featureFile)
	if err != nil {
		// File not found or parse error — gracefully return empty
		return ""
	}
	return node.TrackID
}

// buildWorkItemPromptPrefix returns the work item header to prepend to the yolo prompt.
func buildWorkItemPromptPrefix(id, _ string) string {
	return strings.Join([]string{
		"## Active Work Item",
		fmt.Sprintf("You are working on: %s", id),
		"All work in this session must be attributed to this item.",
		"",
	}, "\n")
}

// buildYoloSystemPrompt prepends the work item header to the embedded yolo prompt.
func buildYoloSystemPrompt(id, kind string) string {
	var sb strings.Builder
	if id != "" {
		sb.WriteString(buildWorkItemPromptPrefix(id, kind))
	}
	sb.WriteString(yoloPromptContent)
	return sb.String()
}

// launchYoloPlanningMode launches Claude in planning mode (no bypass permissions)
// when no --track or --feature is provided. Prints guidance before launching.
func launchYoloPlanningMode(projectRoot string, extraArgs []string) error {
	fmt.Println("No --track or --feature specified.")
	fmt.Println("Launching in planning mode to help you create a track or feature first.")
	fmt.Println("Once you have a track/feature, restart with:")
	fmt.Println("  wipnote yolo --track <track-id>")
	fmt.Println("  wipnote yolo --feature <feature-id>")
	fmt.Println()
	return launchClaude(LaunchOpts{
		Mode:               "yolo-planning",
		InjectSystemPrompt: true,
		ExtraArgs:          extraArgs,
		ProjectRoot:        projectRoot,
	})
}

func launchYoloDefault(permMode, trackID, featureID string, noWorktree bool, resumeID, name string, extraArgs []string) error {
	projectRoot := ""
	if wipnoteDir, err := findWipnoteDir(); err == nil {
		projectRoot = filepath.Dir(wipnoteDir)
	}
	// Resolve canonical main repo root when CWD is a linked worktree (slice-3).
	// Do NOT overwrite projectRoot: it stays the child working directory for
	// --no-worktree/planning launches. Only WIPNOTE_PROJECT_DIR (WipnoteRoot)
	// and worktree creation use the canonical main root.
	canonicalRoot := projectRoot
	if canonical := canonicalProjectRoot(projectRoot); canonical != "" {
		canonicalRoot = canonical
	}

	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}

	// No work item provided — fall back to planning mode. The child cwd is the
	// (un-rewritten) projectRoot per the slice-3 contract.
	if trackID == "" && featureID == "" {
		_ = computeLauncherMode("", false, false)
		return launchYoloPlanningMode(projectRoot, extraArgs)
	}

	// Validate the provided work item exists.
	id, kind, err := validateWorkItem(trackID, featureID, canonicalRoot)
	if err != nil {
		return err
	}

	// Honor the isolation plan (slice-9): a RefuseLaunch plan aborts before the
	// harness starts. The validated work item id drives the dirty-main/enforce guard.
	launchPlan := applyLaunchPlan(canonicalRoot, id, noWorktree, os.Stderr)
	if err := enforceLaunchPlan(launchPlan, os.Stderr); err != nil {
		return err
	}

	// Resolve track title once — used for both the session name and the worktree directory.
	trackTitle := resolveTrackTitle(trackID, featureID, canonicalRoot)

	// Create a worktree for isolation (skip for --no-worktree). Worktrees are
	// always created under the canonical main root, never a linked worktree copy.
	workDir := projectRoot
	if !noWorktree && canonicalRoot != "" {
		if trackID != "" {
			worktreePath, wtErr := EnsureForTrackWithTitle(trackTitle, trackID, canonicalRoot, os.Stdout)
			if wtErr != nil {
				return wtErr
			}
			workDir = worktreePath
		} else if featureID != "" {
			// Resolve the parent track so features use the titled track worktree.
			parentTrackID := resolveTrackForFeature(featureID, canonicalRoot)
			if parentTrackID != "" {
				parentTitle := resolveTrackTitle(parentTrackID, "", canonicalRoot)
				worktreePath, wtErr := EnsureForTrackWithTitle(parentTitle, parentTrackID, canonicalRoot, os.Stdout)
				if wtErr != nil {
					return wtErr
				}
				workDir = worktreePath
			} else {
				worktreePath, wtErr := EnsureForFeature(featureID, canonicalRoot, os.Stdout)
				if wtErr != nil {
					return wtErr
				}
				workDir = worktreePath
			}
		}
	}

	// Compute launcher mode for preflight logging AFTER the effective worktree
	// path is resolved (slice-1 fix): passing the resolved workDir means
	// WIPNOTE_DEBUG reports execution=worktree for isolated YOLO instead of
	// always reporting execution=in-place.
	effectiveWorktree := ""
	if workDir != projectRoot {
		effectiveWorktree = workDir
	}
	_ = computeLauncherMode(effectiveWorktree, false, false)

	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	if sessionName == "" && resumeID == "" {
		sessionName = yoloDefaultName(trackID, featureID, canonicalRoot)
	}
	yoloPrompt := buildYoloSystemPrompt(id, kind)

	fmt.Printf("Launching Claude Code in YOLO mode (%s)...\n", permMode)
	fmt.Printf("  Session: %s\n", sessionName)
	fmt.Printf("  Work item: %s\n", id)

	// Write the combined prompt to a temp file so launchClaude can pass it via
	// --append-system-prompt without needing a new field.
	tmpFile, err := os.CreateTemp("", "yolo-prompt-*.md")
	if err != nil {
		return fmt.Errorf("could not create temp prompt file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(yoloPrompt); err != nil {
		return fmt.Errorf("could not write temp prompt file: %w", err)
	}
	tmpFile.Close()

	return launchClaude(LaunchOpts{
		Mode:             "yolo",
		PluginDir:        pluginDir,
		ResumeID:         resumeID,
		SystemPromptFile: tmpFile.Name(),
		PermissionMode:   permMode,
		Name:             sessionName,
		ExtraArgs:        extraArgs,
		ProjectRoot:      workDir,
		WipnoteRoot:      canonicalRoot,
	})
}

func launchYoloDev(trackID, featureID string, noWorktree bool, resumeID, name string, extraArgs []string) error {
	// Dev mode resolves the plugin from local source, NOT the marketplace.
	pluginDir := resolveProjectPluginDir()
	if pluginDir == "" {
		return fmt.Errorf("could not find plugin/ directory relative to project root. Run from the project directory containing .wipnote/ and plugin/")
	}
	if _, err := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); os.IsNotExist(err) {
		return fmt.Errorf("plugin.json not found at %s",
			filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	}
	if err := requireWipnoteOnPath(); err != nil {
		return err
	}

	projectRoot := ""
	if wipnoteDir, err := findWipnoteDir(); err == nil {
		projectRoot = filepath.Dir(wipnoteDir)
	}

	// No work item provided — fall back to planning mode.
	if trackID == "" && featureID == "" {
		return launchYoloPlanningMode(projectRoot, extraArgs)
	}

	// Validate the provided work item exists.
	id, kind, err := validateWorkItem(trackID, featureID, projectRoot)
	if err != nil {
		return err
	}

	// Resolve track title once — used for both the session name and the worktree directory.
	trackTitle := resolveTrackTitle(trackID, featureID, projectRoot)

	// Create a worktree for isolation (skip for --no-worktree).
	workDir := projectRoot
	if !noWorktree && projectRoot != "" {
		if trackID != "" {
			worktreePath, wtErr := EnsureForTrackWithTitle(trackTitle, trackID, projectRoot, os.Stdout)
			if wtErr != nil {
				return wtErr
			}
			workDir = worktreePath
		} else if featureID != "" {
			// Resolve the parent track so features use the titled track worktree.
			parentTrackID := resolveTrackForFeature(featureID, projectRoot)
			if parentTrackID != "" {
				parentTitle := resolveTrackTitle(parentTrackID, "", projectRoot)
				worktreePath, wtErr := EnsureForTrackWithTitle(parentTitle, parentTrackID, projectRoot, os.Stdout)
				if wtErr != nil {
					return wtErr
				}
				workDir = worktreePath
			} else {
				worktreePath, wtErr := EnsureForFeature(featureID, projectRoot, os.Stdout)
				if wtErr != nil {
					return wtErr
				}
				workDir = worktreePath
			}
		}
	}

	// Nuke marketplace plugin so it can't shadow the --plugin-dir agents/skills.
	removeMarketplaceWipnote()

	sessionName := name
	// Only synthesize a default name for new sessions. When resuming an existing
	// session, skip default-name generation so we don't rename or conflict with
	// the resumed session. The user can still override with an explicit --name.
	if sessionName == "" && resumeID == "" {
		sessionName = yoloDefaultName(trackID, featureID, projectRoot)
	}
	yoloPrompt := buildYoloSystemPrompt(id, kind)

	fmt.Printf("Launching Claude Code in YOLO dev mode...\n")
	fmt.Printf("  Plugin: %s\n", pluginDir)
	fmt.Printf("  Session: %s\n", sessionName)
	fmt.Printf("  Work item: %s\n", id)

	tmpFile, err := os.CreateTemp("", "yolo-prompt-*.md")
	if err != nil {
		return fmt.Errorf("could not create temp prompt file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(yoloPrompt); err != nil {
		return fmt.Errorf("could not write temp prompt file: %w", err)
	}
	tmpFile.Close()

	return launchClaude(LaunchOpts{
		Mode:             "yolo-dev",
		PluginDir:        pluginDir,
		ResumeID:         resumeID,
		SystemPromptFile: tmpFile.Name(),
		PermissionMode:   "bypassPermissions",
		Name:             sessionName,
		ExtraArgs:        extraArgs,
		ProjectRoot:      workDir,
		WipnoteRoot:      projectRoot,
	})
}

func launchYoloInit(trackID, featureID, resumeID, name string, extraArgs []string) error {
	// Initialize .wipnote/ first.
	if err := runInit(nil, nil); err != nil {
		return fmt.Errorf("init failed: %w", err)
	}
	fmt.Println()
	return launchYoloDefault("bypassPermissions", trackID, featureID, false, resumeID, name, extraArgs)
}

func launchYoloContinue(extraArgs []string, resumeID string) error {
	projectRoot := ""
	if wipnoteDir, err := findWipnoteDir(); err == nil {
		projectRoot = filepath.Dir(wipnoteDir)
	}

	pluginDir, err := resolveBundledPluginDir()
	if err != nil {
		return err
	}
	fmt.Println("Resuming last YOLO session...")

	return launchClaude(LaunchOpts{
		Mode:           "yolo-continue",
		PluginDir:      pluginDir,
		Resume:         true,
		ResumeID:       resumeID,
		PermissionMode: "bypassPermissions",
		ExtraArgs:      extraArgs,
		ProjectRoot:    projectRoot,
	})
}
