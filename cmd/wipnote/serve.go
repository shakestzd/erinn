package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/hooks"
	"github.com/shakestzd/wipnote/core/ingest"
	daemonpkg "github.com/shakestzd/wipnote/internal/daemon"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	return daemonpkg.NewServeCommand(runServer)
}

// runServer is now the parent-with-reverse-proxy entry point. The old
// single-project in-process server has moved to the hidden _serve-child
// subcommand (slice 1); runServer spawns one child per project via the
// childproc supervisor and forwards /p/<id>/* traffic to it.
//
// This function is a thin wrapper around runParentServer defined in
// serve_parent.go — kept here so the cobra command definition above does
// not need to import a different package or refer to a free-standing
// function.
func runServer(bind string, port int) error {
	return runParentServer(bind, port)
}

// buildSingleProjectMux constructs the HTTP routes for a single-project
// wipnote server. It does NOT start the HTTP server and does NOT launch
// background goroutines — the caller is responsible for both.
//
// This factory is shared by:
//   - runServer (legacy single-project path; slice 3 repurposes runServer)
//   - runServeChild (the hidden _serve-child subcommand the parent spawns
//     for per-project process isolation in multi-project mode)
//
// dashboardFS is accessed via the package-level dashboardSub() helper and
// is intentionally not a parameter.
// buildSingleProjectMux wires the dashboard HTTP routes.
//
// bug-74a7bda7 (roborev HIGH follow-up): the dashboard child opens TWO
// handles — a read-only `database` (mode=ro, the cornerstone fix that
// removes shared→reserved lock escalation on every request) and a single
// writable `writeDB` owned by the child for the background maintenance
// loops. The vast majority of HTTP routes are SELECT-only and stay on the
// read-only handle. A small set of mutating endpoints (manual session
// ingest, plan feedback/finalize/delete/chat) perform INSERT/UPDATE/DELETE
// and MUST receive `writeDB` or they fail with SQLITE_READONLY. This
// function is the single place that decides per-route which handle a
// handler gets; do NOT pass `writeDB` to a read-only route (that would
// reintroduce a fully-writable mux and the original contention).
//
// `writeDB` may be nil in unit tests that only exercise read routes; the
// mutating handlers are only reachable when their routes are hit, so a nil
// writeDB is safe for read-only test coverage.
func buildSingleProjectMux(database, writeDB *sql.DB, wipnoteDir string) *http.ServeMux {
	mux := http.NewServeMux()

	// /api/mode — lets the dashboard detect single vs global mode on load.
	// The child includes the project name (derived from the project dir's
	// basename) so the UI can label the header when drilled in via a proxy.
	projectDir := filepath.Dir(wipnoteDir)
	projectName := filepath.Base(projectDir)
	mux.Handle("/api/mode", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, map[string]any{
			"mode":        "single",
			"projectName": projectName,
			"projectRoot": projectDir,
		})
	}))

	// API endpoints registered before file server so they take precedence.
	// Merge note: main dropped corsMiddleware(...) wrappers as part of
	// the local HTTP server security hardening (commit 6ad59d1ae). The
	// feature branch's new /api/graph/agents, /api/provenance/ and
	// /api/graph/{commits,files,sessions} routes are registered
	// WITHOUT the wrapper to stay consistent with that hardening.
	mux.Handle("/api/events/recent", recentEventsHandler(database))
	mux.Handle("/api/events/feed", eventsFeedHandler(database))
	mux.Handle("/api/events/tree", treeHandler(database))
	mux.Handle("/api/sessions/family", familyTreeHandler(database))
	mux.Handle("/api/sessions/family-info", sessionFamilyHandler(database))
	mux.Handle("/api/events/stream", sseHandler(database))
	mux.Handle("/api/events/subagent", subagentEventsHandler(database))
	mux.Handle("/api/sessions", sessionsHandler(database, projectDir, wipnoteDir))
	mux.Handle("/api/sessions/resumable", resumableSessionsHandler(database, wipnoteDir))
	mux.Handle("/api/sessions/adherence-trend", sessionAdherenceTrendHandler(database, projectDir, wipnoteDir))
	mux.Handle("/api/features", featuresHandler(database, wipnoteDir))
	mux.Handle("/api/stats", statsHandler(database, wipnoteDir))
	mux.Handle("/api/initial-stats", initialStatsHandler(database))
	mux.Handle("/api/timeline", timelineHandler(database))
	mux.Handle("/api/transcript", transcriptHandler(database, wipnoteDir))
	mux.Handle("/api/sessions/parallel", parallelSessionsHandler(database))
	// sessionIngestHandler serves a read-only /preview sub-route AND a
	// POST /ingest that writes (DeleteSessionMessages/ensureSession/
	// storeParseResult/UpdateTranscriptSync). It needs both handles:
	// read-only for preview, writable for the ingest write path.
	mux.Handle("/api/sessions/", sessionIngestHandler(database, writeDB))
	mux.Handle("/api/features/", featureActivityRouter(database, wipnoteDir))
	mux.Handle("/api/graph", graphAPIHandler(database))
	mux.Handle("/api/graph/agents", agentsHandler(database))
	mux.Handle("/api/provenance/", provenanceHandler(database, wipnoteDir))
	mux.Handle("/api/graph/commits", commitsForFeatureHandler(database))
	mux.Handle("/api/graph/files", filesForFeatureHandler(database))
	mux.Handle("/api/graph/sessions", sessionsForFeatureHandler(database))

	// OTel telemetry endpoints — query otel_signals and otel_session_rollup
	// populated by the per-session otel-collect subprocess writing directly to the canonical DB.
	mux.Handle("/api/otel/rollup", otelRollupHandler(database))
	mux.Handle("/api/otel/prompts", otelPromptsHandler(database))
	mux.Handle("/api/otel/cost", otelCostHandler(database))
	mux.Handle("/api/otel/spans", otelSpansHandler(database))
	mux.Handle("/api/otel/logs", otelLogsHandler(database))
	mux.Handle("/api/otel/status", collectorStatusHandler(projectDir))

	// CRISPI plan routes — list route must precede the per-plan catch-all.
	mux.Handle("/api/plans", plansListHandler(wipnoteDir, database))
	mux.Handle("/plans/", planFileHandler(wipnoteDir))
	// planRouter fans out to 9 sub-handlers; the mutating ones
	// (feedback POST, finalize, delete, chat) need writeDB while the
	// read-only ones (status/render/events/amendments/yaml/feedback GET)
	// stay on the read-only handle. planRouter wires this internally.
	mux.Handle("/api/plans/", planRouter(database, writeDB, wipnoteDir))

	// Terminal sidecar routes — spawn/stop ttyd processes for the embedded
	// interactive terminal. Must be registered before the catch-all "/" below.
	// Gated behind WIPNOTE_TERMINAL: the routes are only registered when the
	// env var is exactly "1". Any other value (including "0", "false", or empty)
	// leaves the routes unregistered so they return 404. Strict match avoids the
	// surprise where WIPNOTE_TERMINAL=0 would otherwise enable the feature.
	if os.Getenv("WIPNOTE_TERMINAL") == "1" {
		mux.Handle("/api/terminal/start", handleTerminalStart(projectDir))
		mux.Handle("/api/terminal/sessions", handleTerminalSessions())
		mux.Handle("/api/terminal/stop", handleTerminalStop())
		mux.Handle("/api/terminal/stop-all", handleTerminalStopAll())
	}

	// Serve embedded dashboard (index.html, css/, js/, components/)
	mux.Handle("/", http.FileServer(http.FS(dashboardSub())))

	return mux
}

// resolvePluginDir finds the go-plugin directory using a priority-ordered
// search strategy. This decouples plugin discovery from the binary's
// filesystem location, supporting Homebrew, go install, curl install, and
// dev-mode symlink workflows.
//
// Search order:
//  1. CLAUDE_PLUGIN_ROOT env var (always set by Claude Code in hook/plugin context)
//  2. WIPNOTE_PLUGIN_DIR env var (explicit user override)
//  3. installed_plugins.json installPath (marketplace: ~/.claude/plugins/cache/...)
//  4. Symlink walk-up from binary (dev mode: binary lives inside plugin tree)
//  5. Project-root detection (CWD walk-up: find .wipnote/ + plugin/)
func resolvePluginDir() string {
	// 1. CLAUDE_PLUGIN_ROOT — set by Claude Code whenever a hook runs.
	//    This is the authoritative source in hook and plugin context, and
	//    works correctly for both dev-mode symlinks and marketplace installs.
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		if _, err := os.Stat(filepath.Join(root, ".claude-plugin", "plugin.json")); err == nil {
			return root
		}
	}

	// 2. Explicit user override — useful for non-standard installs or testing.
	if dir := os.Getenv("WIPNOTE_PLUGIN_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
			return dir
		}
	}

	// 3. Read installed_plugins.json to find the marketplace install path.
	//    Claude Code stores plugins at ~/.claude/plugins/cache/<marketplace>/<name>/<version>/
	//    and records the exact path in installed_plugins.json.  Iterating the
	//    file is more robust than hard-coding a path that varies by version.
	if dir := resolveMarketplacePluginDir(); dir != "" {
		return dir
	}

	// 4. Symlink walk-up from binary — works for dev mode where the binary
	//    lives at plugin/hooks/bin/wipnote (two levels up is
	//    the plugin root).  Fails gracefully when the binary is at
	//    ~/.local/bin/wipnote (standalone) or inside the marketplace cache
	//    (already handled above), because those paths have no plugin.json.
	binPath, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}
	pluginDir := filepath.Join(filepath.Dir(binPath), "..", "..")
	pluginDir, _ = filepath.Abs(pluginDir)
	if _, err := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); err == nil {
		return pluginDir
	}

	// 5. Project-root detection — walk up from CWD to find .wipnote/,
	//    then check for plugin/ relative to the project root.
	//    This makes dev mode work from a fresh clone or fork without
	//    needing a marketplace install first.
	if projectPlugin := resolveProjectPluginDir(); projectPlugin != "" {
		return projectPlugin
	}

	return ""
}

// resolveProjectPluginDir walks up from CWD looking for a directory containing
// .wipnote/ and plugin/.claude-plugin/plugin.json. Returns the
// plugin dir path or "" if not found.
func resolveProjectPluginDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolveProjectPluginDirFrom(cwd)
}

// resolveProjectPluginDirFrom walks up from startDir (at most 5 levels) looking
// for a directory that contains both .wipnote/ and plugin/.claude-plugin/plugin.json.
// Returns the absolute path to the plugin/ directory, or "" if not found.
// This variant accepts an explicit starting path so dev-mode callers can anchor
// the search to the source repo root rather than CWD (which may be a worktree).
func resolveProjectPluginDirFrom(startDir string) string {
	dir := startDir
	for range 5 {
		// Check if this directory has both .wipnote/ and plugin/
		pluginDir := filepath.Join(dir, "plugin")
		if _, err := os.Stat(filepath.Join(dir, ".wipnote")); err == nil {
			if _, err := os.Stat(filepath.Join(pluginDir, ".claude-plugin", "plugin.json")); err == nil {
				return pluginDir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return ""
}

// resolveMarketplacePluginDir reads ~/.claude/plugins/installed_plugins.json
// and returns the first installPath that has a valid .claude-plugin/plugin.json
// and whose key contains "wipnote". Returns "" on any error or miss.
func resolveMarketplacePluginDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return ""
	}

	var registry struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return ""
	}

	for key, entries := range registry.Plugins {
		if !strings.Contains(key, "wipnote") {
			continue
		}
		for _, e := range entries {
			if e.InstallPath == "" {
				continue
			}
			candidate := e.InstallPath
			// Resolve symlinks (dev-mode swap replaces the cache dir with a
			// symlink to the source tree — we want the real plugin root).
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
				candidate = resolved
			}
			if _, err := os.Stat(filepath.Join(candidate, ".claude-plugin", "plugin.json")); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// autoIngestLoop runs transcript ingestion immediately, then every 60 seconds.
// wipnoteDir is the .wipnote/ directory of the project being served;
// its parent is used as the project-root filter for session discovery. This
// removes the previous os.Getwd() dependency so the child process spawned
// by the parent server (slice 2) still scopes ingestion to the correct
// project regardless of the child's CWD.
//
// onFirstComplete runs after the initial ingest cycle returns. It is used to
// sequence one-time startup work (e.g. the ai-title backfill) so that such
// work observes a fully-populated sessions table on the very first launch
// after upgrade.
func autoIngestLoop(database *sql.DB, wipnoteDir string, onFirstComplete func()) {
	autoIngestOnce(database, wipnoteDir)
	if onFirstComplete != nil {
		onFirstComplete()
	}
	for {
		time.Sleep(60 * time.Second)
		autoIngestOnce(database, wipnoteDir)
	}
}

// autoIngestOnce discovers session files and ingests any that are new.
func autoIngestOnce(database *sql.DB, wipnoteDir string) {
	// Filter to current project only — derive project root from wipnoteDir
	// (parent of .wipnote/) so the child-process server still scopes
	// ingestion correctly.
	projectFilter := filepath.Dir(wipnoteDir)
	files, err := ingest.DiscoverSessions(projectFilter)
	if err != nil {
		return
	}
	for _, sf := range files {
		// Check if re-ingest is needed: skip if file hasn't changed since last sync.
		needsIngest := false
		count, _ := dbpkg.CountMessages(database, sf.SessionID)
		if count == 0 {
			needsIngest = true
		} else {
			// Re-ingest if JSONL file modified after last sync.
			var syncedAt string
			database.QueryRow(`SELECT COALESCE(transcript_synced, '') FROM sessions WHERE session_id = ?`,
				sf.SessionID).Scan(&syncedAt)
			if syncedAt != "" {
				if info, err := os.Stat(sf.Path); err == nil {
					synced, _ := time.Parse(time.RFC3339, syncedAt)
					if info.ModTime().After(synced) {
						needsIngest = true
					}
				}
			}
		}
		if !needsIngest {
			continue
		}

		result, err := ingest.ParseFile(sf.Path)
		if err != nil || len(result.Messages) == 0 {
			continue
		}
		if isHeadlessSession(result) {
			continue
		}

		// Clear old messages before re-ingest to avoid duplicates.
		if count > 0 {
			_ = dbpkg.DeleteSessionMessages(database, sf.SessionID)
		}

		sessionSourceDir := decodeProjectDirFromSessionFile(sf)
		ensureSession(database, sf.SessionID, result, sessionSourceDir)
		msgCount, toolCount := storeParseResult(database, sf.SessionID, "", result)
		_ = dbpkg.UpdateTranscriptSync(database, sf.SessionID, sf.Path)
		if rerr := hooks.RenderIngestedSessionHTML(wipnoteDir, sf.SessionID, sessionSourceDir, result, false); rerr != nil {
			log.Printf("auto-ingest: render HTML for %s: %v\n", truncate(sf.SessionID, 14), rerr)
		}
		if msgCount > 0 {
			log.Printf("auto-ingest: %s — %d msgs, %d tools\n",
				truncate(sf.SessionID, 14), msgCount, toolCount)
		}
	}

	// Update session status from JSONL file mtime (source of truth).
	// "active" if file modified < 5 min ago, "completed" otherwise.
	// Also tag active sessions with launch_mode from .launch-mode file.
	launchMode := ""
	if data, err := os.ReadFile(filepath.Join(wipnoteDir, ".launch-mode")); err == nil {
		if strings.Contains(string(data), `"yolo`) {
			launchMode = "yolo"
		}
	}

	for _, sf := range files {
		info, err := os.Stat(sf.Path)
		if err != nil {
			continue
		}
		status := "completed"
		if time.Since(info.ModTime()) < 5*time.Minute {
			status = "active"
			// Tag active sessions with the current launch mode
			if launchMode != "" {
				database.Exec(`UPDATE sessions SET metadata = json_set(COALESCE(metadata, '{}'), '$.launch_mode', ?) WHERE session_id = ?`,
					launchMode, sf.SessionID)
			}
		}
		database.Exec(`UPDATE sessions SET status = ? WHERE session_id = ?`, status, sf.SessionID)
	}

}

// isHeadlessSession returns true if the session was created by the
// wipnote titler (claude -p calls). Detected by the [wipnote-titler]
// marker in the first user message.
func isHeadlessSession(result *ingest.ParseResult) bool {
	for _, m := range result.Messages {
		if m.Role == "user" {
			return strings.Contains(m.Content, "[wipnote-titler]") ||
				strings.Contains(m.Content, "Generate a concise 4-8 word title for this AI coding session")
		}
	}
	return false
}
