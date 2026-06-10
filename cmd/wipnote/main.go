// Package main is the entry point for the wipnote CLI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/shakestzd/wipnote/core/worktree"
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
	root := &cobra.Command{
		Use:           "wipnote",
		Short:         "Causal lineage and observability for AI-assisted development",
		Long:          "wipnote — trace causal lineage across work items, commits, sessions, and agent spawns. Local-first observability and coordination for AI-assisted development.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// --project-dir overrides all other project-root detection strategies.
	root.PersistentFlags().StringVar(
		&projectDirFlag,
		"project-dir",
		"",
		"explicit project root containing .wipnote/ (overrides CLAUDE_PROJECT_DIR and CWD walk-up)",
	)

	// Lazy session registration + passive project registration: every CLI
	// command self-heals attribution chains and upserts the current project
	// into the cross-project registry.
	root.PersistentPreRunE = persistentPreRunE

	// Register cobra groups. Registration order determines display order in
	// renderCompactHelp. Commands assigned to a group ID appear in their group;
	// ungrouped commands are treated as internal plumbing and omitted.
	root.AddGroup(&cobra.Group{ID: "workitems", Title: "Work Items"})
	root.AddGroup(&cobra.Group{ID: "query", Title: "Query & Status"})
	root.AddGroup(&cobra.Group{ID: "quality", Title: "Quality"})
	root.AddGroup(&cobra.Group{ID: "data", Title: "Data"})
	root.AddGroup(&cobra.Group{ID: "dev", Title: "Dev"})

	// workitems group
	feature := featureCmdWithExtras()
	feature.GroupID = "workitems"
	root.AddCommand(feature)

	spike := workitemCmd("spike", "spikes")
	spike.AddCommand(spikeResetCmd())
	spike.AddCommand(linkCommitCmd("spike"))
	spike.GroupID = "workitems"
	root.AddCommand(spike)

	bug := workitemCmd("bug", "bugs")
	bug.AddCommand(bugResetCmd())
	bug.AddCommand(linkCommitCmd("bug"))
	bug.GroupID = "workitems"
	root.AddCommand(bug)

	track := trackCmdWithExtras()
	track.GroupID = "workitems"
	root.AddCommand(track)

	plan := planCmdWithExtras()
	plan.GroupID = "workitems"
	root.AddCommand(plan)

	// query group
	find := findCmd()
	find.GroupID = "query"
	root.AddCommand(find)

	wip := wipCmd()
	wip.GroupID = "query"
	root.AddCommand(wip)

	status := statusCmd()
	status.GroupID = "query"
	root.AddCommand(status)

	snapshot := snapshotCmd()
	snapshot.GroupID = "query"
	root.AddCommand(snapshot)

	link := linkCmd()
	link.GroupID = "query"
	root.AddCommand(link)

	session := sessionCmd()
	session.GroupID = "query"
	root.AddCommand(session)

	analytics := analyticsCmd()
	analytics.GroupID = "query"
	root.AddCommand(analytics)

	recommend := recommendCmd()
	recommend.GroupID = "query"
	root.AddCommand(recommend)

	relevant := relevantCmd()
	relevant.GroupID = "query"
	root.AddCommand(relevant)

	history := newHistoryCmd()
	history.GroupID = "query"
	root.AddCommand(history)

	lineage := newLineageCmd()
	lineage.GroupID = "query"
	root.AddCommand(lineage)

	blameC := blameCmd()
	blameC.GroupID = "query"
	root.AddCommand(blameC)

	codeAreas := codeAreasCmd()
	codeAreas.GroupID = "query"
	root.AddCommand(codeAreas)

	contextPack := contextPackCmd()
	contextPack.GroupID = "query"
	root.AddCommand(contextPack)

	executePreview := executePreviewCmd()
	executePreview.GroupID = "query"
	root.AddCommand(executePreview)

	search := searchCmd()
	search.GroupID = "query"
	root.AddCommand(search)

	// quality group
	check := checkCmd()
	check.GroupID = "quality"
	root.AddCommand(check)

	health := healthCmd()
	health.GroupID = "quality"
	root.AddCommand(health)

	spec := specCmd()
	spec.GroupID = "quality"
	root.AddCommand(spec)

	tdd := tddCmd()
	tdd.GroupID = "quality"
	root.AddCommand(tdd)

	review := reviewCmd()
	review.GroupID = "quality"
	root.AddCommand(review)

	compliance := complianceCmd()
	compliance.GroupID = "quality"
	root.AddCommand(compliance)

	reconcile := reconcileCmd()
	reconcile.GroupID = "quality"
	root.AddCommand(reconcile)

	// data group
	batch := batchCmd()
	batch.GroupID = "data"
	root.AddCommand(batch)

	ingest := ingestCmd()
	ingest.GroupID = "data"
	root.AddCommand(ingest)

	backfill := backfillCmd()
	backfill.GroupID = "data"
	root.AddCommand(backfill)

	sweep := sweepCmd()
	sweep.GroupID = "data"
	root.AddCommand(sweep)

	reindex := reindexCmd()
	reindex.GroupID = "data"
	root.AddCommand(reindex)

	migrate := migrateCmd()
	migrate.GroupID = "data"
	root.AddCommand(migrate)

	migrateTracks := migrateTracksCmd()
	migrateTracks.GroupID = "data"
	root.AddCommand(migrateTracks)

	cleanup := cleanupCmd()
	cleanup.GroupID = "data"
	root.AddCommand(cleanup)

	cache := cacheCmd()
	cache.GroupID = "data"
	root.AddCommand(cache)

	sync := syncCmd()
	sync.GroupID = "data"
	root.AddCommand(sync)

	prune := pruneCmd()
	prune.GroupID = "data"
	root.AddCommand(prune)

	// dev group
	yolo := yoloCmd()
	yolo.GroupID = "dev"
	root.AddCommand(yolo)

	upgrade := upgradeCmd()
	upgrade.GroupID = "dev"
	root.AddCommand(upgrade)

	build := buildCmd()
	build.GroupID = "dev"
	root.AddCommand(build)

	serve := serveCmd()
	serve.GroupID = "dev"
	root.AddCommand(serve)

	agentInit := agentInitCmd()
	agentInit.GroupID = "dev"
	root.AddCommand(agentInit)

	sh := shCmd()
	sh.GroupID = "dev"
	root.AddCommand(sh)

	// ungrouped (internal plumbing — omitted from compact help)
	root.AddCommand(versionCmd())
	root.AddCommand(statuslineCmd())
	root.AddCommand(serveChildCmd())
	root.AddCommand(otelCollectCmd())
	root.AddCommand(hookCmd())
	root.AddCommand(claudeCmd())
	root.AddCommand(codexCmd())
	root.AddCommand(geminiCmd())
	root.AddCommand(orchestratorCmd())
	root.AddCommand(installHooksCmd())
	root.AddCommand(reportCmd())
	root.AddCommand(budgetCmd())
	root.AddCommand(ciCmd())
	root.AddCommand(helpCmd())
	root.AddCommand(claimCmd())
	who := whoCmd()
	who.GroupID = "query"
	root.AddCommand(who)
	root.AddCommand(purgeSpikesCmd())
	root.AddCommand(traceCmd())
	root.AddCommand(graphCmd())
	root.AddCommand(queryCmd())
	root.AddCommand(devCmd())
	root.AddCommand(pluginCmd())
	root.AddCommand(projectsCmd())
	root.AddCommand(initCmd())
	root.AddCommand(setupCmd())
	root.AddCommand(setupCLICmd())
	root.AddCommand(shellAliasCmd())
	root.AddCommand(pricingCmd())
	root.AddCommand(harnessCmd())
	launcher := launcherCmd()
	launcher.GroupID = "quality"
	root.AddCommand(launcher)

	guard := guardCmd()
	guard.GroupID = "quality"
	root.AddCommand(guard)

	commitQueue := commitQueueCmd()
	commitQueue.GroupID = "data"
	root.AddCommand(commitQueue)

	return root
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
	if !inCacheSubtree(cmd) {
		if cacheRoot, cerr := storage.CacheRoot(); cerr == nil {
			storage.OpportunisticPrune(cacheRoot, projectDir, os.Stderr)
		}
	}
	if database, dberr := openDB(hgDir); dberr == nil {
		_, _ = agent.EnsureSession(database, projectDir)
		database.Close()
	}
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
