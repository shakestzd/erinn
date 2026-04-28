// Package main is the entry point for the htmlgraph CLI.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/agent"
	"github.com/shakestzd/htmlgraph/internal/paths"
	"github.com/shakestzd/htmlgraph/internal/registry"
	"github.com/shakestzd/htmlgraph/internal/storage"
	versionpkg "github.com/shakestzd/htmlgraph/internal/version"
	"github.com/shakestzd/htmlgraph/internal/worktree"
	"github.com/spf13/cobra"
)

// selfHealGitdirIfStale runs a best-effort repair on the current directory's
// linked-worktree gitdir pointer. Git worktrees created on one host (macOS
// /Users/<user>/…) and reopened on another (Linux Codespace /workspaces/…)
// leave stale absolute paths that break every subsequent git command. If
// HTMLGRAPH_PROJECT_DIR points at the main repo, we can rewrite the .git
// pointer in place so the user doesn't hit cryptic "not a git repository"
// errors before htmlgraph even starts.
func selfHealGitdirIfStale() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	mainRoot := os.Getenv("HTMLGRAPH_PROJECT_DIR")
	if mainRoot == "" {
		return // no reliable anchor; skip silently
	}
	if cwd == mainRoot {
		return // not a linked worktree
	}
	if repaired, err := worktree.RepairGitdirIfStale(cwd, mainRoot); err == nil && repaired {
		fmt.Fprintf(os.Stderr, "htmlgraph: repaired stale worktree gitdir at %s\n", filepath.Join(cwd, ".git"))
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
	selfHealGitdirIfStale()
	root := buildRoot()
	if err := root.Execute(); err != nil {
		msg := err.Error()
		// Cobra's "unknown command" error doesn't tell the agent what to do
		// next when no close-match suggestion exists. Append a recovery hint.
		if strings.HasPrefix(msg, "unknown command") && !strings.Contains(msg, "Did you mean") {
			msg += "\nRun 'htmlgraph help --compact' to see all commands."
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
		Use:           "htmlgraph",
		Short:         "Causal lineage and observability for AI-assisted development",
		Long:          "HtmlGraph — trace causal lineage across work items, commits, sessions, and agent spawns. Local-first observability and coordination for AI-assisted development.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// --project-dir overrides all other project-root detection strategies.
	root.PersistentFlags().StringVar(
		&projectDirFlag,
		"project-dir",
		"",
		"explicit project root containing .htmlgraph/ (overrides CLAUDE_PROJECT_DIR and CWD walk-up)",
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
	spike.GroupID = "workitems"
	root.AddCommand(spike)

	bug := workitemCmd("bug", "bugs")
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

	executePreview := executePreviewCmd()
	executePreview.GroupID = "query"
	root.AddCommand(executePreview)

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

	cleanup := cleanupCmd()
	cleanup.GroupID = "data"
	root.AddCommand(cleanup)

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
	root.AddCommand(pricingCmd())

	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("htmlgraph %s (go)\n", version)
			if latest, newer, _ := versionpkg.CheckForUpdate(version); newer {
				fmt.Printf("Update available: v%s → run `htmlgraph build` or check https://github.com/shakestzd/htmlgraph/releases\n", latest)
			}
		},
	}
}

// persistentPreRunE is attached to rootCmd and runs before every command. It
// performs two side-effects: (1) ensures a session row exists for the current
// agent attribution chain, and (2) upserts the current project into the
// cross-project registry at ~/.local/share/htmlgraph/projects.json. Both
// operations degrade gracefully — registration failures never block a CLI
// command from running.
func persistentPreRunE(cmd *cobra.Command, _ []string) error {
	// Skip commands that must work without .htmlgraph/.
	switch cmd.Name() {
	case "version", "help", "init", "build", "install-hooks", "setup", "setup-cli", "projects", "upgrade", "update":
		return nil
	}
	// Skip hook subtree — hooks manage their own session lifecycle.
	for p := cmd; p != nil; p = p.Parent() {
		if p.Name() == "hook" {
			return nil
		}
	}
	// Degrade gracefully: commands must not fail because session
	// registration is unavailable.
	hgDir, err := findHtmlgraphDir()
	if err != nil {
		return nil
	}
	projectDir := filepath.Dir(hgDir)
	storage.CleanLegacyDBIfSafe(projectDir, os.Stderr)
	if database, dberr := openDB(hgDir); dberr == nil {
		_, _ = agent.EnsureSession(database, projectDir)
		database.Close()
	}
	// Registry upsert — silent, cached git remote lookup.
	if reg, regErr := registry.Load(registry.DefaultPath()); regErr == nil {
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
		// older binaries (before findHtmlgraphDir started resolving
		// linked worktrees to their main repo) persist as duplicate
		// project cards in the doorway. Drop any entry whose path is
		// inside a linked worktree of a registered main repo.
		reg.DropLinkedWorktrees(paths.ResolveViaGitCommonDir)
		_ = reg.Save()
	}
	return nil
}

// findHtmlgraphDir locates the .htmlgraph directory by delegating to the
// shared paths.ResolveProjectDir resolver (--project-dir flag → CLAUDE_PROJECT_DIR
// env → git worktree detection → CWD walk-up) and appending ".htmlgraph".
func findHtmlgraphDir() (string, error) {
	paths.CleanupGlobalHint() // Remove stale global hint from older versions
	root, err := paths.ResolveProjectDir(paths.ProjectDirOptions{
		ExplicitDir: projectDirFlag,
	})
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".htmlgraph"), nil
}

// printProjectHeaderIfDifferent prints a one-line "Project: <path>" header
// to stdout when the resolved project root differs from the current working
// directory. Project-level mutation commands (migrate, sweep, ingest) call
// this before touching data so the user can tell at a glance when env-var
// resolution (HTMLGRAPH_PROJECT_DIR / CLAUDE_PROJECT_DIR) or worktree
// detection is pointing them at a different project than the one they're
// sitting in. No-op when the user is already in the resolved project —
// keeps normal usage silent.
func printProjectHeaderIfDifferent(htmlgraphDir string) {
	projectRoot := filepath.Dir(htmlgraphDir)
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
