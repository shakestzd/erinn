// Package main is the entry point for the wipnote CLI.
package main

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/daemon/apply"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/shakestzd/wipnote/core/worktree"
	cliinternal "github.com/shakestzd/wipnote/internal/cli"
	"github.com/shakestzd/wipnote/internal/registry"
	versionpkg "github.com/shakestzd/wipnote/internal/version"
	"github.com/spf13/cobra"

	// Side-effect import: registers the otel-backed core/eventsink factory so
	// lifecycle hooks emit telemetry without importing otel directly (feat-f87e93a6).
	_ "github.com/shakestzd/wipnote/observe/otel/eventsink"
	// Side-effect import: registers the otel/pluginbuild-backed lifecycle hook
	// implementations (retention, materialize, port-drift) so core hooks invoke
	// them without importing otel/pluginbuild directly (feat-331927fb).
	_ "github.com/shakestzd/wipnote/observe/register"
)

// selfHealGitdirIfStale runs a best-effort repair on the current directory's
// linked-worktree gitdir pointer. Git worktrees created on one host (macOS
// /Users/<user>/…) and reopened on another (Linux Codespace /workspaces/…)
// leave stale absolute paths that break every subsequent git command. If
// WIPNOTE_PROJECT_DIR points at the main repo, we can rewrite the .git
// pointer in place so the user doesn't hit cryptic "not a git repository"
// errors before wipnote even starts.
func selfHealGitdirIfStale() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	mainRoot := os.Getenv("WIPNOTE_PROJECT_DIR")
	if mainRoot == "" {
		return // no reliable anchor; skip silently
	}
	if cwd == mainRoot {
		return // not a linked worktree
	}
	if repaired, err := worktree.RepairGitdirIfStale(cwd, mainRoot); err == nil && repaired {
		fmt.Fprintf(os.Stderr, "wipnote: repaired stale worktree gitdir at %s\n", filepath.Join(cwd, ".git"))
	}
}

// version is set at build time via ldflags.
var version = "dev"

// projectDirFlag holds the value of the --project-dir persistent flag.
var projectDirFlag string

// getGitRemoteURLFn is a package-level indirection for paths.GetGitRemoteURL
// so tests can stub it and count invocations. Production code calls the real
// implementation.
var getGitRemoteURLFn = paths.GetGitRemoteURL

func main() {
	// Propagate the compiled CLI version into the provenance package so every
	// session/work item written from this process records the binary identity.
	// (Set early so any code path — including direct subcommand handlers that
	// bypass persistentPreRunE — sees the right version.)
	provenance.SetCLIVersion(version)

	selfHealGitdirIfStale()
	root := buildRoot()
	if err := root.Execute(); err != nil {
		msg := err.Error()
		// Cobra's "unknown command" error doesn't tell the agent what to do
		// next when no close-match suggestion exists. Append a recovery hint.
		if strings.HasPrefix(msg, "unknown command") && !strings.Contains(msg, "Did you mean") {
			msg += "\nRun 'wipnote help --compact' to see all commands."
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
}

// buildRoot constructs and returns a fully-registered root cobra command,
// but does NOT call Execute(). It is the single source of truth for all
// command registration — both main() and tests use this function so the
// command tree cannot drift.
func buildRoot() *cobra.Command {
	spike := workitemCmd("spike", "spikes")
	spike.AddCommand(spikeResetCmd())
	spike.AddCommand(linkCommitCmd("spike"))

	bug := workitemCmd("bug", "bugs")
	bug.AddCommand(bugResetCmd())
	bug.AddCommand(linkCommitCmd("bug"))

	return cliinternal.BuildRoot(cliinternal.RootOptions{
		ProjectDirFlag:    &projectDirFlag,
		PersistentPreRunE: persistentPreRunE,
		WorkItems: []cliinternal.GroupedCommand{
			{GroupID: "workitems", Command: featureCmdWithExtras()},
			{GroupID: "workitems", Command: spike},
			{GroupID: "workitems", Command: bug},
			{GroupID: "workitems", Command: trackCmdWithExtras()},
			{GroupID: "workitems", Command: planCmdWithExtras()},
			{GroupID: "workitems", Command: archCmd()},
		},
		Query: []cliinternal.GroupedCommand{
			{GroupID: "query", Command: recapCmd()},
			{GroupID: "query", Command: findCmd()},
			{GroupID: "query", Command: wipCmd()},
			{GroupID: "query", Command: statusCmd()},
			{GroupID: "query", Command: snapshotCmd()},
			{GroupID: "query", Command: linkCmd()},
			{GroupID: "query", Command: sessionCmd()},
			{GroupID: "query", Command: sessionsCmd()},
			{GroupID: "query", Command: analyticsCmd()},
			{GroupID: "query", Command: recommendCmd()},
			{GroupID: "query", Command: relevantCmd()},
			{GroupID: "query", Command: newHistoryCmd()},
			{GroupID: "query", Command: newLineageCmd()},
			{GroupID: "query", Command: blameCmd()},
			{GroupID: "query", Command: codeAreasCmd()},
			{GroupID: "query", Command: contextPackCmd()},
			{GroupID: "query", Command: executePreviewCmd()},
			{GroupID: "query", Command: searchCmd()},
			{GroupID: "query", Command: whoCmd()},
		},
		Quality: []cliinternal.GroupedCommand{
			{GroupID: "quality", Command: checkCmd()},
			{GroupID: "quality", Command: healthCmd()},
			{GroupID: "quality", Command: specCmd()},
			{GroupID: "quality", Command: tddCmd()},
			{GroupID: "quality", Command: reviewCmd()},
			{GroupID: "quality", Command: complianceCmd()},
			{GroupID: "quality", Command: reconcileCmd()},
			{GroupID: "quality", Command: launcherCmd()},
			{GroupID: "quality", Command: guardCmd()},
		},
		Data: []cliinternal.GroupedCommand{
			{GroupID: "data", Command: batchCmd()},
			{GroupID: "data", Command: ingestCmd()},
			{GroupID: "data", Command: backfillCmd()},
			{GroupID: "data", Command: sweepCmd()},
			{GroupID: "data", Command: reindexCmd()},
			{GroupID: "data", Command: migrateCmd()},
			{GroupID: "data", Command: migrateTracksCmd()},
			{GroupID: "data", Command: cleanCmd()},
			{GroupID: "data", Command: cleanupCmd()},
			{GroupID: "data", Command: archiveCmd()},
			{GroupID: "data", Command: cacheCmd()},
			{GroupID: "data", Command: syncCmd()},
			{GroupID: "data", Command: pruneCmd()},
			{GroupID: "data", Command: registryCmd()},
			{GroupID: "data", Command: commitQueueCmd()},
		},
		Dev: []cliinternal.GroupedCommand{
			{GroupID: "dev", Command: yoloCmd()},
			{GroupID: "dev", Command: upgradeCmd()},
			{GroupID: "dev", Command: buildCmd()},
			{GroupID: "dev", Command: serveCmd()},
			{GroupID: "dev", Command: agentInitCmd()},
			{GroupID: "dev", Command: shCmd()},
		},
		Ungrouped: []*cobra.Command{
			versionCmd(),
			statuslineCmd(),
			serveChildCmd(),
			otelCollectCmd(),
			hookCmd(),
			claudeCmd(),
			codexCmd(),
			antigravityCmd(),
			orchestratorCmd(),
			installHooksCmd(),
			reportCmd(),
			budgetCmd(),
			ciCmd(),
			helpCmd(),
			claimCmd(),
			purgeSpikesCmd(),
			traceCmd(),
			graphCmd(),
			queryCmd(),
			devCmd(),
			pluginCmd(),
			projectsCmd(),
			initCmd(),
			setupCmd(),
			setupCLICmd(),
			shellAliasCmd(),
			pricingCmd(),
			harnessCmd(),
		},
	})
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("wipnote %s (go)\n", version)
			if latest, newer, _ := versionpkg.CheckForUpdate(version); newer {
				fmt.Printf("Update available: v%s → run `wipnote build` or check https://github.com/shakestzd/wipnote/releases\n", latest)
			}
		},
	}
}

// ensureSessionPreRunTimeout bounds the best-effort session-row write done in
// persistentPreRunE. It must be short enough that a contended SQLite write lock
// (held by serve / a per-session otel collector / a concurrent session) cannot
// stall an interactive command — notably the `wipnote claude` launch chooser —
// for the full 5s busy_timeout. The write is idempotent and self-healing, so a
// fast skip on contention is safe. See bug-504095f2.
const ensureSessionPreRunTimeout = 500 * time.Millisecond

func init() {
	// Wire the daemon-route seam for the cold-path session INSERT so
	// EnsureSessionRouted can route through the per-project writer daemon
	// without importing apply directly in core/agent (which is kept
	// dependency-free to avoid import cycles). The adapter builds the minimal
	// models.Session needed by RouteSessionInsert from the flat field args.
	agent.RouteSessionInsertFn = func(projectRoot, sessionID, agentID, now, model, projectDir, gitRemoteURL string) bool {
		s := &models.Session{
			SessionID:     sessionID,
			AgentAssigned: agentID,
			Status:        "active",
			Model:         model,
			ProjectDir:    projectDir,
			GitRemoteURL:  gitRemoteURL,
		}
		if t, err := time.Parse(time.RFC3339, now); err == nil {
			s.CreatedAt = t
		}
		// ENQUEUE-ONLY (bug-d792aee6 finding 1): the launcher's new-session cold
		// insert runs on the interactive critical path. The applied-ack
		// RouteSessionInsert waited the full CLISubmitBudget (~2.4s) under a held
		// external write lock because the daemon cannot apply while the lock is
		// held. Enqueue-only acks sub-millisecond once the daemon is warm; the
		// op applies FIFO afterwards, and SessionStart's idempotent INSERT OR
		// IGNORE upsert (slice-3) + canonical NDJSON reindex are the backstop.
		return apply.RouteSessionInsertAsync(projectRoot, s)
	}
}

// persistentPreRunE is attached to rootCmd and runs before every command. It
// performs two side-effects: (1) ensures a session row exists for the current
// agent attribution chain, and (2) upserts the current project into the
// cross-project registry at ~/.local/share/wipnote/projects.json. Both
// operations degrade gracefully — registration failures never block a CLI
// command from running.
func persistentPreRunE(cmd *cobra.Command, _ []string) error {
	// Skip commands that must work without .wipnote/.
	switch cmd.Name() {
	case "version", "help", "init", "build", "install-hooks", "setup", "setup-cli", "projects", "upgrade", "update":
		return nil
	// Internal process commands: otel-collect and _serve-child are spawned as
	// child processes by the parent supervisor. They must not open the SQLite DB
	// in persistentPreRunE because:
	//   1. otel-collect must print its handshake line within 3s of being spawned.
	//      Opening the DB (and applying pragmas) can block for up to busy_timeout
	//      (5s) when stale wipnote processes hold the write lock, causing all
	//      3 spawn retries to time out and the launcher to exit FATAL.
	//   2. _serve-child opens its own DB connection explicitly in runServeChild.
	// Neither command participates in agent session tracking or the project registry.
	case "otel-collect", "_serve-child":
		return nil
	}
	// Skip hook subtree — hooks manage their own session lifecycle.
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "hook" {
			return nil
		}
	}
	// Skip the `launcher` diagnostic subtree (e.g. `wipnote launcher doctor`).
	// These commands are advertised as non-destructive: they must NOT trigger
	// the legacy-DB clean, opportunistic cache prune, session-row write, or
	// registry upsert that persistentPreRunE performs for normal commands.
	if isLauncherDiagnosticSubtree(cmd) {
		return nil
	}
	// Degrade gracefully: commands must not fail because session
	// registration is unavailable.
	hgDir, err := findWipnoteDir()
	if err != nil {
		return nil
	}
	projectDir := filepath.Dir(hgDir)
	storage.CleanLegacyDBIfSafe(projectDir, os.Stderr)
	// Opportunistic prune is destructive; skip it for the `cache` subtree so
	// `wipnote cache prune --dry-run` reports the disk's actual state, and
	// pass the active project's cache dir as protected so the LRU sweep can't
	// pull the read-index out from under the very command that's about to run.
	// Also skip for launcher commands (yolo, claude, codex, antigravity):
	// the 1-3s cache scan adds silent latency before the harness starts, and
	// launchers are not the prune path of record (feat-caa02f9a Fix 2).
	if !inCacheSubtree(cmd) && !isHarnessLauncherCmd(cmd) {
		if cacheRoot, cerr := storage.CacheRoot(); cerr == nil {
			storage.OpportunisticPrune(cacheRoot, projectDir, os.Stderr)
		}
	}
	launchTiming("persistentPreRunE: before openDB+EnsureSession")

	// For harness launcher commands, wrap the session init in a styled spinner step
	// instead of plain stderr output (feat-caa02f9a Fix 4a). RunWithSpinner handles
	// TTY detection internally: on TTY it animates and resolves to a checkmark; off TTY
	// it degrades gracefully with no animation (passes through to fn).
	if isHarnessLauncherCmd(cmd) {
		_ = launchtui.RunWithSpinner(os.Stderr, "Initializing", func(_ io.Writer) error {
			return initSessionWithDB(hgDir, projectDir, cmd)
		})
	} else {
		// Non-launcher commands run session init without spinner.
		_ = initSessionWithDB(hgDir, projectDir, cmd)
	}

	launchTiming("persistentPreRunE: after openDB+EnsureSession")
	// Registry upsert — silent, cached git remote lookup.
	if reg, regErr := registry.Load(defaultRegistryPath()); regErr == nil {
		var cachedRemote string
		for _, e := range reg.List() {
			if filepath.Clean(e.ProjectDir) == filepath.Clean(projectDir) {
				cachedRemote = e.GitRemoteURL
				break
			}
		}
		remoteURL := cachedRemote
		if remoteURL == "" {
			remoteURL = getGitRemoteURLFn(projectDir)
		}
		reg.Upsert(projectDir, filepath.Base(projectDir), remoteURL)
		// Opportunistic worktree cleanup: registry entries created by
		// older binaries (before findWipnoteDir started resolving
		// linked worktrees to their main repo) persist as duplicate
		// project cards in the doorway. Drop any entry whose path is
		// inside a linked worktree of a registered main repo.
		reg.DropLinkedWorktrees(paths.ResolveViaGitCommonDir)
		_ = reg.Save()
	}
	return nil
}

// initSessionWithDB performs the session initialization that was previously done
// inline in persistentPreRunE. It opens the DB (read-only or writable as needed)
// and ensures a session row exists. This is factored out so it can be wrapped
// in a spinner for harness launcher commands (bug-b48c354f).
func initSessionWithDB(hgDir, projectDir string, cmd *cobra.Command) error {
	// slice-5 (feat-1ad54813): split-handle daemon-routed session ensure.
	//   • Hot path (session exists): read-only SELECT — never acquires a write
	//     lock. WAL readers and writers never block each other, so the launch
	//     chooser renders <1s even under a held external write lock.
	//   • Cold path (new session): routed through the per-project writer daemon
	//     via agent.RouteSessionInsertFn (ENQUEUE-ONLY, bounded AsyncEnqueueBudget
	//     — bug-d792aee6 finding 1: applied-ack stalled ~2.4s under a held lock).
	//     No writable handle is opened when the daemon acks.
	//   • Last-resort fallback: daemon unreachable → EnsureSessionWithTimeout on
	//     the writable handle with ensureSessionPreRunTimeout, matching pre-slice-5
	//     behaviour (busy_timeout-bounded, INSERT OR IGNORE idempotent).
	//
	// For harness launcher commands we open with a short 500ms busy_timeout so
	// WAL contention from a running serve daemon never stalls the interactive
	// launch path for the full 5s default (feat-caa02f9a Fix 1).
	roOpenFn := func(dbPath string) (*sql.DB, error) {
		if isHarnessLauncherCmd(cmd) {
			return dbpkg.OpenReadOnlyFast(dbPath, 500)
		}
		return dbpkg.OpenReadOnly(dbPath)
	}
	dbPath, pathErr := storage.CanonicalDBPath(projectDir)
	if pathErr == nil {
		roDB, roErr := roOpenFn(dbPath)
		if roErr == nil {
			// Read-only open succeeded — warm DB. Use the routed path which
			// opens NO writable handle when the daemon acks (hot or acked-cold).
			//
			// roborev-473 finding 2: pass a LAZY writable opener instead of
			// eagerly opening the writable handle here. The warm/exists path and
			// the daemon-acked cold path never invoke it, so we no longer pay a
			// writable open + migration under contention for the common case;
			// EnsureSessionRouted opens (and closes) the handle ONLY on a daemon
			// miss where the direct fallback is actually needed.
			_, _ = agent.EnsureSessionRouted(roDB, func() (*sql.DB, error) {
				launchTiming("persistentPreRunE: lazy openDB (daemon-miss fallback)")
				return openDB(hgDir)
			}, projectDir, projectDir, ensureSessionPreRunTimeout)
			roDB.Close()
		} else {
			// Read-only open failed. Distinguish a genuinely-missing DB (first
			// launch — must create it) from a lock/contention error. For harness
			// launchers the writable openDB uses the default 5s busy_timeout, so
			// falling back on contention would reintroduce the launch stall the
			// 500ms fast-fail just avoided (roborev-612). Only take the writable
			// fallback when the DB is actually missing, or for non-launcher cmds.
			_, statErr := os.Stat(dbPath)
			dbMissing := os.IsNotExist(statErr)
			if dbMissing || !isHarnessLauncherCmd(cmd) {
				if database, dberr := openDB(hgDir); dberr == nil {
					launchTiming("persistentPreRunE: after openDB (before EnsureSession)")
					_, _ = agent.EnsureSessionWithTimeout(database, projectDir, ensureSessionPreRunTimeout)
					database.Close()
				}
			} else {
				// Launcher + DB exists but read-only open failed (lock/contention):
				// skip best-effort writable session init to keep launch fast; the
				// SessionStart hook / writer daemon still records the session.
				launchTiming("persistentPreRunE: skip writable fallback under contention (launcher)")
			}
		}
	}
	return nil
}

// inCacheSubtree reports whether cmd or any ancestor is the `cache` command.
// Used to bypass the destructive opportunistic prune in PersistentPreRunE so
// `wipnote cache prune --dry-run` reports the cache's actual state rather
// than what's left after the prune the pre-run hook just performed.
func inCacheSubtree(cmd *cobra.Command) bool {
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "cache" {
			return true
		}
	}
	return false
}

// isLauncherDiagnosticSubtree reports whether cmd or any ancestor is the
// `launcher` command. The launcher subtree (currently `launcher doctor`) is a
// read-only diagnostic surface; persistentPreRunE skips its destructive
// side-effects so `wipnote launcher doctor` never mutates the DB, cache, or
// project registry while it is merely reporting health.
func isLauncherDiagnosticSubtree(cmd *cobra.Command) bool {
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "launcher" {
			return true
		}
	}
	return false
}

// isHarnessLauncherCmd reports whether cmd is one of the interactive harness
// launcher commands (yolo, claude, codex, antigravity). These commands
// run the prerun session-route and registry-upsert (they benefit from session
// tracking), but skip the heavyweight OpportunisticPrune and use a short DB
// open timeout so WAL contention from the serve daemon never stalls launch
// (feat-caa02f9a Fix 1 + Fix 2).
func isHarnessLauncherCmd(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "yolo", "claude", "codex", "antigravity":
		return true
	}
	return false
}

// findWipnoteDir locates the .wipnote directory by delegating to the
// shared paths.ResolveProjectDir resolver (--project-dir flag → CLAUDE_PROJECT_DIR
// env → git worktree detection → CWD walk-up) and appending ".wipnote".
func findWipnoteDir() (string, error) {
	paths.CleanupGlobalHint() // Remove stale global hint from older versions
	root, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		ExplicitDir: projectDirFlag,
	})
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".wipnote"), nil
}

// printProjectHeaderIfDifferent prints a one-line "Project: <path>" header
// to stdout when the resolved project root differs from the current working
// directory. Project-level mutation commands (migrate, sweep, ingest) call
// this before touching data so the user can tell at a glance when env-var
// resolution (WIPNOTE_PROJECT_DIR / CLAUDE_PROJECT_DIR) or worktree
// detection is pointing them at a different project than the one they're
// sitting in. No-op when the user is already in the resolved project —
// keeps normal usage silent.
func printProjectHeaderIfDifferent(wipnoteDir string) {
	projectRoot := filepath.Dir(wipnoteDir)
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	// Resolve symlinks on both sides so /var/... and /private/var/... compare
	// equal on macOS and worktrees that traverse symlinked paths don't
	// trigger a false-positive "outside project" header.
	resolvedProject := resolveForCompare(projectRoot)
	resolvedWD := resolveForCompare(wd)
	if resolvedWD == resolvedProject {
		return
	}
	// Silent when CWD is inside the project (worktrees, subdirs).
	if rel, relErr := filepath.Rel(resolvedProject, resolvedWD); relErr == nil &&
		!strings.HasPrefix(rel, "..") && rel != "." {
		return
	}
	fmt.Fprintf(os.Stderr, "Project: %s  (CWD: %s — use --project-dir to override)\n",
		projectRoot, wd)
}

// resolveForCompare returns the absolute, symlink-resolved, cleaned path for
// directory comparison. Falls back to the absolute path when symlink
// resolution fails (e.g. the path does not exist).
func resolveForCompare(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// truncate shortens s to maxLen characters, appending "…" if cut.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
