package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/launcher"
	"github.com/spf13/cobra"
)

// geminiExtensionInstallDir returns the expected install directory for the
// wipnote Gemini extension.
func geminiExtensionInstallDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini", "extensions", "wipnote")
}

// isGeminiExtensionInstalled reports whether the wipnote extension is already
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
				"Either build with a real version (wipnote build) or pass --ref <ref>", err)
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

// runGeminiInit installs the wipnote Gemini extension, idempotently.
// Corresponds to: wipnote gemini --init [--ref <ref>] [--force] [--dry-run]
//
// Phase B of the marketplace-to-bundled-plugin migration: --init now links
// the bundled local extension tree resolved via
// resolveSharedTreePath("gemini-extension") instead of downloading the
// extension from GitHub. The Gemini extension tree is shipped alongside the
// wipnote binary (in the release tarball or via brew install) and mirrored
// by `wipnote build`. The --ref flag is accepted but ignored; a deprecation
// notice prints when it is supplied.
func runGeminiInit(ref string, force, dryRun bool) error {
	installDir := geminiExtensionInstallDir()

	bundled, err := resolveSharedTreePath("gemini-extension")
	if err != nil {
		return fmt.Errorf("resolving bundled Gemini extension: %w", err)
	}

	if ref != "" {
		fmt.Fprintf(os.Stderr,
			"warning: --ref %q is ignored under bundled-extension mode (extension is shipped with the wipnote binary)\n", ref)
	}

	// Check idempotency: if the extension is already linked to the bundled
	// path, skip. `--force` re-links unconditionally.
	if !force && isExtensionAlreadyLinkedToLocalPath(bundled) {
		fmt.Printf("wipnote Gemini extension is already linked to bundled path: %s\n", bundled)
		fmt.Println("To reinstall: wipnote gemini --init --force")
		fmt.Println("To launch:    wipnote gemini")
		return nil
	}

	// If there is an existing install (GitHub-installed or linked elsewhere),
	// uninstall it first so the link can take. Mirrors the launchGeminiDev
	// logic that handles stale installs.
	if isGeminiExtensionInstalled() && !dryRun {
		geminiPath, gErr := exec.LookPath("gemini")
		if gErr != nil {
			return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", gErr)
		}
		fmt.Printf("Replacing existing wipnote Gemini extension install at %s\n", installDir)
		if out, uErr := exec.Command(geminiPath, "extensions", "uninstall", "wipnote").CombinedOutput(); uErr != nil {
			return fmt.Errorf("gemini extensions uninstall failed: %w\n%s", uErr, strings.TrimSpace(string(out)))
		}
	}

	linkArgs := buildGeminiLinkArgs(bundled)
	fmt.Printf("Linking wipnote Gemini extension (bundled)...\n")
	fmt.Printf("  path: %s\n", bundled)

	if dryRun {
		fmt.Printf("[dry-run] gemini %s\n", strings.Join(linkArgs, " "))
		return nil
	}

	geminiPath, err := exec.LookPath("gemini")
	if err != nil {
		return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", err)
	}

	out, runErr := exec.Command(geminiPath, linkArgs...).CombinedOutput()
	if runErr != nil {
		return fmt.Errorf("gemini extensions link failed: %w\n%s", runErr, strings.TrimSpace(string(out)))
	}

	fmt.Println("wipnote Gemini extension linked (bundled).")

	// Install hooks into ~/.gemini/settings.json so they are picked up by
	// Gemini CLI. The extension-dir hooks.json is not read by Gemini CLI;
	// hooks must live in settings.json#hooks.
	fmt.Println()
	if err := installGeminiHooks("", dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install hooks into settings.json: %v\n", err)
		fmt.Fprintln(os.Stderr, "  Run manually: wipnote gemini --init")
	}

	fmt.Println()
	fmt.Println("Setup complete. Run: wipnote gemini")
	return nil
}

// interactiveGeminiExtensionInstall prompts the user to install the extension.
//
// Phase B: under bundled-extension mode the prompt is suppressed; the launcher
// auto-links the bundled tree. This stub is retained for tests/backcompat but
// becomes a no-op when called.
func interactiveGeminiExtensionInstall() {
	fmt.Println()
	fmt.Println("wipnote extension is not installed for Gemini CLI.")
	fmt.Println("The extension adds hooks, agents, skills, and slash commands.")
	fmt.Println()
	fmt.Print("Install wipnote Gemini extension? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(strings.ToLower(line))

	if choice == "y" || choice == "yes" {
		fmt.Println()
		if err := runGeminiInit("", false, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: extension installation failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "  Run manually: wipnote gemini --init\n")
		} else {
			fmt.Println("Extension installed successfully.")
		}
	} else {
		fmt.Println("Continuing without extension. Run 'wipnote gemini --init' later to add it.")
	}
	fmt.Println()
}

// ensureGeminiExtensionLinked auto-links the bundled Gemini extension tree
// when no extension is installed yet, or when the existing install does not
// point at the bundled path. Idempotent on the happy path; logs warnings but
// does not block the launch on failure. Phase B replacement for the
// interactive install prompt.
func ensureGeminiExtensionLinked() {
	bundled, err := resolveSharedTreePath("gemini-extension")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve bundled Gemini extension: %v\n", err)
		fmt.Fprintln(os.Stderr, "  Install via 'brew install wipnote' or 'wipnote build', or run 'wipnote gemini --init' manually.")
		return
	}
	if isExtensionAlreadyLinkedToLocalPath(bundled) {
		return
	}
	if isGeminiExtensionInstalled() {
		// Different install (GitHub-installed or linked elsewhere). Tear it down so we can re-link.
		geminiPath, gErr := exec.LookPath("gemini")
		if gErr != nil {
			fmt.Fprintf(os.Stderr, "warning: gemini not in PATH; skipping extension link: %v\n", gErr)
			return
		}
		if out, uErr := exec.Command(geminiPath, "extensions", "uninstall", "wipnote").CombinedOutput(); uErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not uninstall previous wipnote Gemini extension: %v\n%s\n", uErr, strings.TrimSpace(string(out)))
			return
		}
	}
	geminiPath, gErr := exec.LookPath("gemini")
	if gErr != nil {
		fmt.Fprintf(os.Stderr, "warning: gemini not in PATH; skipping extension link: %v\n", gErr)
		return
	}
	linkArgs := buildGeminiLinkArgs(bundled)
	if out, lErr := exec.Command(geminiPath, linkArgs...).CombinedOutput(); lErr != nil {
		fmt.Fprintf(os.Stderr, "warning: gemini extensions link failed: %v\n%s\n", lErr, strings.TrimSpace(string(out)))
		fmt.Fprintln(os.Stderr, "  Run manually: wipnote gemini --init")
		return
	}
	fmt.Printf("wipnote Gemini extension linked (bundled): %s\n", bundled)
}

// ensureGeminiExtensionOnLaunch is called by the default launcher.
var ensureGeminiExtensionOnLaunchFn = ensureGeminiExtensionOnLaunch
var isGeminiExtensionInstalledFn = isGeminiExtensionInstalled
var interactiveGeminiExtensionInstallFn = interactiveGeminiExtensionInstall

// ensureGeminiExtensionOnLaunch ensures the wipnote Gemini extension is
// available before launching. Phase B: auto-link the bundled tree instead
// of prompting the user; the interactive helper is retained for callers
// that still want to opt in (e.g. tests).
func ensureGeminiExtensionOnLaunch() {
	ensureGeminiExtensionLinked()
}

func maybeEnsureGeminiExtensionOnLaunch(dryRun bool) {
	if dryRun {
		return
	}
	ensureGeminiExtensionOnLaunchFn()
}

// launchGeminiDefault launches Gemini interactively with wipnote env injection.
// Corresponds to: wipnote gemini
func launchGeminiDefault(trackID, featureID, worktreePath, workItem string, noWorktree bool, extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	// Resolve worktree path.
	// canonicalProjectRoot detects when CWD is already a linked worktree (slice-3):
	// returns the canonical main repo root, or "" when in the main worktree.
	// canonicalRoot is the value injected as WIPNOTE_PROJECT_DIR AND used as the
	// base for worktree creation — it must always be the canonical main root,
	// never the linked worktree copy (slice-3 contract).
	canonicalRoot := projectRoot
	if c := canonicalProjectRoot(projectRoot); c != "" {
		canonicalRoot = c
	}
	intent, err := resolveLaunchIntentForDefaultLaunch(projectRoot, canonicalRoot, "gemini", chooserEligibility{
		TTY:       isInteractiveTerminalFile(os.Stdin) && isInteractiveTerminalFile(os.Stdout),
		CI:        os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") != "",
		WorkItem:  workItem,
		Targeted:  trackID != "" || featureID != "" || worktreePath != "",
		InPlace:   noWorktree,
		ExtraArgs: extraArgs,
	}, os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	intentResult := applyGeminiLaunchIntent(worktreePath, workItem, "", intent)
	worktreePath = intentResult.worktreePath
	workItem = intentResult.workItem
	continueCtx, err := resolveContinueLaunchContext(projectRoot, canonicalRoot, "gemini", intent)
	if err != nil {
		return err
	}
	for _, warning := range continueCtx.Warnings {
		fmt.Fprintln(os.Stderr, warning)
	}
	if workItem == "" && continueCtx.WorkItemID != "" {
		workItem = continueCtx.WorkItemID
	}
	// When continuing, always prefer the validated/normalized worktree path from
	// resolveContinueLaunchContext (which absolutizes relative paths and clears
	// stale/missing ones). An empty continueCtx.WorktreePath means validation
	// failed; we clear the stale chooser path rather than launching in a missing
	// directory.
	if intent.WantsContinue() {
		worktreePath = continueCtx.WorktreePath
	} else if worktreePath == "" && continueCtx.WorktreePath != "" {
		worktreePath = continueCtx.WorktreePath
	}

	// Apply isolation plan and HONOR it (slice-9): a RefuseLaunch plan aborts
	// before Gemini starts. noWorktree here is effectiveInPlace (--in-place || --no-worktree).
	// When the plan will create a managed worktree, suppress the generic dirty-main
	// advisory — we emit an accurate message after carryover instead (bug-938e56ae).
	willCreateWorktree := !noWorktree && (trackID != "" || featureID != "" || workItem != "")
	launchPlan := applyLaunchPlanOpts(canonicalRoot, projectRoot, effectiveWorkItemID(workItem, trackID, featureID), noWorktree, willCreateWorktree, os.Stderr)
	if err := enforceLaunchPlan(launchPlan, os.Stderr); err != nil {
		return err
	}
	maybeEnsureGeminiExtensionOnLaunch(dryRun)

	// Work item attribution: emit `wipnote feature start <id>` before launching.
	if workItem != "" && !dryRun {
		if err := runFeatureStart(workItem); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not start work item %s: %v\n", workItem, err)
		}
	}
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

	// Honor a managed-worktree plan (slice-9) when no explicit/track/feature
	// worktree was resolved above. WIPNOTE_PROJECT_DIR stays the canonical root.
	if wt, created, werr := resolveManagedWorktreeStatus(launchPlan, canonicalRoot, trackID, featureID, workItem, workDir, resolved, os.Stdout); werr != nil {
		return werr
	} else if wt != "" && wt != workDir {
		workDir = wt
		wipnoteRoot = canonicalRoot
		worktreeCreated = created
	}

	// Carry the canonical main repo's uncommitted tracked changes into the
	// freshly-created worktree so the session builds on the user's latest
	// working state (bug-938e56ae). Only for newly-created worktrees: a reused
	// worktree may already contain prior work and re-applying would double-apply
	// or fail. Main is never mutated. Carryover failure is non-fatal.
	if workDir != projectRoot {
		emitWorktreeCarryoverMessage(launchPlan, canonicalRoot, workDir, worktreeCreated, os.Stdout)
	}

	fmt.Println("Launching Gemini CLI with wipnote context...")
	return execGemini(geminiLaunchOpts{
		ResumeLast:   intentResult.resumeLast,
		ResumeIndex:  intentResult.resumeIndex,
		ExtraArgs:    extraArgs,
		ProjectRoot:  workDir,
		WorktreeRoot: workDir,
		WipnoteRoot:  wipnoteRoot,
		Mode:         intentResult.mode,
		DryRun:       dryRun,
		ExtraEnv:     continueCtx.ExtraEnv(),
	})
}

type geminiIntentResult struct {
	mode         geminiLaunchMode
	resumeLast   bool
	resumeIndex  string
	worktreePath string
	workItem     string
}

func applyGeminiLaunchIntent(worktreePath, workItem, resumeIndex string, intent launcher.LaunchIntent) geminiIntentResult {
	result := geminiIntentResult{
		mode:         geminiLaunchModeDefault,
		resumeIndex:  resumeIndex,
		worktreePath: worktreePath,
		workItem:     workItem,
	}
	if !intent.WantsContinue() {
		return result
	}
	result.mode = geminiLaunchModeContinue
	if result.workItem == "" && intent.WorkItemID != "" {
		result.workItem = intent.WorkItemID
	}
	if result.worktreePath == "" && intent.WorktreePath != "" {
		result.worktreePath = intent.WorktreePath
	}
	// Gemini's launcher contract resumes by index/latest, not by stored session ID.
	// Chooser-driven continue therefore preserves work-item/worktree context and
	// continue-mode instructions without attempting a lossy session-ID conversion.
	return result
}

// launchGeminiContinue resumes the latest Gemini session.
// Corresponds to: wipnote gemini --continue
func launchGeminiContinue(extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	maybeEnsureGeminiExtensionOnLaunch(dryRun)
	fmt.Println("Resuming latest Gemini session...")
	return execGemini(geminiLaunchOpts{
		ResumeLast:  true,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		Mode:        geminiLaunchModeContinue,
		DryRun:      dryRun,
	})
}

// launchGeminiResume resumes a specific Gemini session by index.
// Corresponds to: wipnote gemini --resume <N>
func launchGeminiResume(index string, extraArgs []string, dryRun bool) error {
	projectRoot, _ := resolveProjectRoot()
	maybeEnsureGeminiExtensionOnLaunch(dryRun)
	fmt.Printf("Resuming Gemini session %s...\n", index)
	return execGemini(geminiLaunchOpts{
		ResumeIndex: index,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		Mode:        geminiLaunchModeContinue,
		DryRun:      dryRun,
	})
}

// geminiExtensionMetadata represents the install metadata for the wipnote extension.
type geminiExtensionMetadata struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

// isExtensionAlreadyLinkedToLocalPath checks if the wipnote extension is already
// linked (as a live pointer) to the specified local path. Returns true only if
// the metadata exists, matches the local path, and is a link type.
func isExtensionAlreadyLinkedToLocalPath(localExtPath string) bool {
	home, _ := os.UserHomeDir()
	metaPath := filepath.Join(home, ".gemini", "extensions", "wipnote", ".gemini-extension-install.json")

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
// Corresponds to: wipnote gemini --dev [--isolate]
func launchGeminiDev(isolate, dryRun bool, extraArgs []string) error {
	// Compute launcher mode for preflight logging/inspection (no behavior change).
	_ = computeLauncherMode("", true, false)

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
			metaPath := filepath.Join(home, ".gemini", "extensions", "wipnote", ".gemini-extension-install.json")
			if data, err := os.ReadFile(metaPath); err == nil {
				var meta geminiExtensionMetadata
				if json.Unmarshal(data, &meta) == nil && (meta.Type != "link" || meta.Source != localExtPath) {
					// Stale or wrong-source install — uninstall first.
					fmt.Println("Replacing existing wipnote extension install...")
					geminiPath, geminiErr := exec.LookPath("gemini")
					if geminiErr != nil {
						return fmt.Errorf("gemini not found in PATH: %w\nInstall Gemini CLI first: https://github.com/google-gemini/gemini-cli", geminiErr)
					}
					uninstallArgs := []string{"extensions", "uninstall", "wipnote"}
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
			metaPath2 := filepath.Join(home2, ".gemini", "extensions", "wipnote", ".gemini-extension-install.json")
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
				return fmt.Errorf("gemini extensions link did not update source path: still points at %q, expected %q — try `gemini extensions uninstall wipnote` manually and retry", postLinkMeta.Source, localExtPath)
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
		ext = "wipnote"
	}

	return execGemini(geminiLaunchOpts{
		Extension:   ext,
		ExtraArgs:   extraArgs,
		ProjectRoot: projectRoot,
		Mode:        geminiLaunchModeDev,
		DryRun:      dryRun,
	})
}

// resolveLocalGeminiExtension returns the absolute path to packages/gemini-extension/
// by walking up from CWD to find the project root (directory containing .wipnote/).
func resolveLocalGeminiExtension() (string, error) {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return "", fmt.Errorf("could not find project root (.wipnote/ directory not found)\n" +
			"Run from the wipnote project directory, or use wipnote gemini --init for the extension version")
	}
	projectRoot := filepath.Dir(wipnoteDir)
	extPath := filepath.Join(projectRoot, "port", "packages", "gemini-extension")
	if _, statErr := os.Stat(extPath); os.IsNotExist(statErr) {
		return "", fmt.Errorf("port/packages/gemini-extension/ not found at %s\n"+
			"Run from the wipnote repo root, or use wipnote gemini --init for the published version",
			extPath)
	}
	abs, err := filepath.Abs(extPath)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", extPath, err)
	}
	return abs, nil
}

// geminiCmd returns the cobra command for `wipnote gemini`.
func geminiCmd() *cobra.Command {
	var init_, continue_, dev, force, isolate, listSessions, dryRun, noWorktree, inPlace bool
	var resumeIndex, ref, trackID, featureID, worktreePath, workItem, baseBranch string

	cmd := &cobra.Command{
		Use:   "gemini",
		Short: "Launch Gemini CLI with wipnote context",
		Long: `Launch Gemini CLI with wipnote observability context.

Modes:
  wipnote gemini                      Launch Gemini interactively with wipnote env.
  wipnote gemini --init               Install the wipnote Gemini extension (idempotent).
  wipnote gemini --continue           Resume the latest Gemini session (gemini --resume latest).
  wipnote gemini --resume <N>         Resume a specific Gemini session by index.
  wipnote gemini --dev                Link packages/gemini-extension/ and launch Gemini.
  wipnote gemini --list-sessions      Pass-through: gemini --list-sessions.
  wipnote gemini --feature <id>       Launch in the feature's git worktree.
  wipnote gemini --track <id>         Launch in the track's git worktree.

Session indices come from: gemini --list-sessions.

Installation:
  wipnote gemini --init               Installs gemini-extension-v<version> from GitHub.
  wipnote gemini --init --ref <ref>   Override the extension ref (for pre-release testing).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case init_:
				return runGeminiInit(ref, force, dryRun)
			case listSessions:
				return execGemini(geminiLaunchOpts{ListSessions: true, DryRun: dryRun, Mode: geminiLaunchModeDefault})
			case dev:
				return launchGeminiDev(isolate, dryRun, args)
			case continue_:
				return launchGeminiContinue(args, dryRun)
			case resumeIndex != "":
				return launchGeminiResume(resumeIndex, args, dryRun)
			default:
				effectiveInPlace := inPlace || noWorktree
				_ = baseBranch // reserved for slice-3+
				return launchGeminiDefault(trackID, featureID, worktreePath, workItem, effectiveInPlace, args, dryRun)
			}
		},
	}

	cmd.Flags().BoolVar(&init_, "init", false, "Install the wipnote Gemini extension (idempotent)")
	cmd.Flags().BoolVar(&continue_, "continue", false, "Resume the latest Gemini session")
	cmd.Flags().BoolVar(&dev, "dev", false, "Link packages/gemini-extension/ as a live pointer and launch Gemini")
	cmd.Flags().BoolVar(&force, "force", false, "With --init: reinstall even if already installed")
	cmd.Flags().BoolVar(&isolate, "isolate", false, "With --dev: pass -e wipnote to suppress other extensions")
	cmd.Flags().BoolVar(&listSessions, "list-sessions", false, "Pass-through to gemini --list-sessions")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would happen without executing")
	cmd.Flags().BoolVar(&noWorktree, "no-worktree", false, "Skip worktree creation; run in project root (alias for --in-place)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "Intentional in-place mutation; records opt-out of isolation")
	cmd.Flags().StringVar(&resumeIndex, "resume", "", "Resume a specific Gemini session by index (e.g. --resume 3)")
	cmd.Flags().StringVar(&ref, "ref", "", "With --init: override the extension ref (default: gemini-extension-v<version>)")
	cmd.Flags().StringVar(&trackID, "track", "", "Track ID to work on (e.g., trk-3719d8f3)")
	cmd.Flags().StringVar(&featureID, "feature", "", "Feature ID to work on (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&worktreePath, "worktree", "", "Explicit worktree path (overrides --track/--feature resolution)")
	cmd.Flags().StringVar(&workItem, "work-item", "", "Work item ID for attribution prefix (e.g., feat-15c458aa)")
	cmd.Flags().StringVar(&baseBranch, "base", "", "Base branch for managed worktree (advanced; default: current HEAD)")

	return cmd
}
