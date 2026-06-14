package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func antigravityExtensionInstallDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "antigravity-cli", "plugins", "wipnote")
}

func isAntigravityExtensionInstalled() bool {
	_, err := os.Stat(antigravityExtensionInstallDir())
	return err == nil
}

func runAntigravityInit(force, dryRun bool) error {
	installDir := antigravityExtensionInstallDir()

	bundled, err := resolveSharedTreePath("antigravity-extension")
	if err != nil {
		return fmt.Errorf("resolving bundled Antigravity extension: %w", err)
	}

	if !force && isAntigravityExtensionInstalled() {
		fmt.Printf("wipnote Antigravity extension is already installed at: %s\n", installDir)
		fmt.Println("To reinstall: wipnote antigravity --init --force")
		fmt.Println("To launch:    wipnote antigravity")
		return nil
	}

	if isAntigravityExtensionInstalled() && !dryRun {
		agyPath, gErr := exec.LookPath("agy")
		if gErr != nil {
			return fmt.Errorf("agy not found in PATH: %w", gErr)
		}
		fmt.Printf("Replacing existing wipnote Antigravity extension install at %s\n", installDir)
		if out, uErr := exec.Command(agyPath, "plugin", "uninstall", "wipnote").CombinedOutput(); uErr != nil {
			return fmt.Errorf("agy plugin uninstall failed: %w\n%s", uErr, strings.TrimSpace(string(out)))
		}
	}

	fmt.Printf("Installing wipnote Antigravity extension (bundled)...\n")
	fmt.Printf("  path: %s\n", bundled)

	if dryRun {
		fmt.Printf("[dry-run] agy plugin install %s\n", bundled)
		return nil
	}

	agyPath, err := exec.LookPath("agy")
	if err != nil {
		return fmt.Errorf("agy not found in PATH: %w", err)
	}

	out, runErr := exec.Command(agyPath, "plugin", "install", bundled).CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("agy plugin install failed: %w\n%s", runErr, strings.TrimSpace(string(out)))
	}

	fmt.Println("wipnote Antigravity extension installed (bundled).")
	fmt.Println()
	fmt.Println("Setup complete. Run: wipnote antigravity")
	return nil
}

func ensureAntigravityExtensionLinked() {
	bundled, err := resolveSharedTreePath("antigravity-extension")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve bundled Antigravity extension: %v\n", err)
		return
	}
	if isAntigravityExtensionInstalled() {
		return
	}
	agyPath, gErr := exec.LookPath("agy")
	if gErr != nil {
		fmt.Fprintf(os.Stderr, "warning: agy not in PATH; skipping extension install: %v\n", gErr)
		return
	}
	if out, lErr := exec.Command(agyPath, "plugin", "install", bundled).CombinedOutput(); lErr != nil {
		fmt.Fprintf(os.Stderr, "warning: agy plugin install failed: %v\n%s\n", lErr, strings.TrimSpace(string(out)))
		return
	}
	fmt.Printf("wipnote Antigravity extension installed (bundled): %s\n", bundled)
}

func launchAntigravityDefault(trackID, featureID, worktreePath, workItem string, noWorktree bool, extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	willCreateWorktree := !noWorktree && (trackID != "" || featureID != "" || workItem != "")
	launchPlan := applyLaunchPlanOpts(projectRoot, workItem, noWorktree, willCreateWorktree, os.Stderr)
	if err := enforceLaunchPlan(launchPlan, os.Stderr); err != nil {
		return err
	}
	if !dryRun {
		ensureAntigravityExtensionLinked()
	}

	if workItem != "" && !dryRun {
		if err := runFeatureStart(workItem); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start work item %s: %v\n", workItem, err)
		}
	}

	canonicalRoot := projectRoot
	if c := canonicalProjectRoot(projectRoot); c != "" {
		canonicalRoot = c
	}
	workDir := projectRoot
	wipnoteRoot := canonicalProjectRoot(projectRoot)
	resolved := false
	worktreeCreated := false
	switch {
	case worktreePath != "":
		workDir = worktreePath
		wipnoteRoot = canonicalRoot
		resolved = true
	case !noWorktree && trackID != "":
		wt, created, err := EnsureForTrackStatus(trackID, canonicalRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
		resolved = true
	case !noWorktree && featureID != "":
		wt, created, err := EnsureForFeatureStatus(featureID, canonicalRoot, os.Stdout)
		if err != nil {
			return err
		}
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
		resolved = true
	}

	if wt, created, werr := resolveManagedWorktreeStatus(launchPlan, canonicalRoot, trackID, featureID, workItem, workDir, resolved, os.Stdout); werr != nil {
		return werr
	} else if wt != "" && wt != workDir {
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
	}

	if workDir != projectRoot {
		emitWorktreeCarryoverMessage(launchPlan, canonicalRoot, workDir, worktreeCreated, os.Stdout)
	}

	fmt.Println("Launching Antigravity CLI with wipnote context...")
	return execAntigravity(antigravityLaunchOpts{
		ExtraArgs:    extraArgs,
		ProjectRoot:  workDir,
		WorktreeRoot: workDir,
		WipnoteRoot:  wipnoteRoot,
		DryRun:       dryRun,
	})
}

func launchAntigravityDev(dryRun bool, extraArgs []string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return fmt.Errorf("could not find project root (.wipnote/ directory not found)")
	}
	projectRoot := filepath.Dir(wipnoteDir)
	localExtPath := filepath.Join(projectRoot, "port", "packages", "antigravity-extension")

	fmt.Printf("Launching Antigravity CLI in dev mode...\n")
	fmt.Printf("  Local extension: %s\n", localExtPath)

	if !dryRun {
		agyPath, err := exec.LookPath("agy")
		if err != nil {
			return fmt.Errorf("agy not found in PATH")
		}
		_ = exec.Command(agyPath, "plugin", "uninstall", "wipnote").Run()
		if out, err := exec.Command(agyPath, "plugin", "install", localExtPath).CombinedOutput(); err != nil {
			return fmt.Errorf("agy plugin install failed: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	}

	return execAntigravity(antigravityLaunchOpts{
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		DryRun:      dryRun,
	})
}

func antigravityCmd() *cobra.Command {
	var init_, dev, force, dryRun, noWorktree, inPlace bool
	var trackID, featureID, worktreePath, workItem, baseBranch string

	cmd := &cobra.Command{
		Use:   "antigravity",
		Short: "Launch Antigravity CLI with wipnote context",
		Long: `Launch Antigravity CLI with wipnote observability context.

Modes:
  wipnote antigravity                  Launch Antigravity interactively with wipnote env.
  wipnote antigravity --init           Install the wipnote Antigravity extension (idempotent).
  wipnote antigravity --dev            Link port/packages/antigravity-extension/ and launch.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runAntigravityInit(force, dryRun)
			case dev:
				return launchAntigravityDev(dryRun, args)
			default:
				effectiveInPlace := inPlace || noWorktree
				_ = baseBranch
				return launchAntigravityDefault(trackID, featureID, worktreePath, workItem, effectiveInPlace, args, dryRun)
			}
		},
	}

	cmd.Flags().BoolVar(&init_, "init", false, "Install the wipnote Antigravity extension (idempotent)")
	cmd.Flags().BoolVar(&dev, "dev", false, "Link port/packages/antigravity-extension/ and launch")
	cmd.Flags().BoolVar(&force, "force", false, "With --init: reinstall even if already installed")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation; run in project root")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Intentional in-place mutation; records opt-out of isolation")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on")
	cmd.Flags().StringVar(&worktreePath, "worktree", "", "Explicit worktree path")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for attribution prefix")
	cmd.Flags().StringVar(&baseBranch, "base", "", "Base branch for managed worktree")

	return cmd
}
