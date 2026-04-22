// Package main — the parent HtmlGraph doorway server.
//
// After the per-project process isolation re-architecture (plan-237fb251),
// the parent server holds ZERO SQLite handles. It serves only three things:
//
//   - the landing SPA (embedded dashboard files)
//   - a tiny JSON API (/api/mode, /api/projects, /api/health) that reads
//     the registry file only — no DB access
//   - the /p/<id>/* reverse proxy to per-project child processes
//     (registered in serve_parent.go)
//
// All per-project data — features, sessions, events, plans, transcripts
// — is served exclusively by the child process for that project and
// reaches the browser via the child's reverse proxy. There is no
// dispatchByProject, no projectCache, no aggregate stats, no cross-
// project SSE fan-out. This guarantees strict cross-project isolation:
// it is architecturally impossible for a request to project A to observe
// or mutate project B's database, because the parent never touches
// either.
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shakestzd/htmlgraph/internal/paths"
	"github.com/shakestzd/htmlgraph/internal/registry"
)

// serveHealthState holds in-memory state for /api/health. Written once at
// server startup; read on every health request — no locks needed after init.
// Uses an atomic pointer so the handler sees a consistent snapshot even if
// initServeHealthState is called concurrently (it shouldn't be, but defensive).
type serveHealthState struct {
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	ProjectDir string    `json:"project_dir"`
	HTTPPort   int       `json:"ports_http"`
	OtelPort   int       `json:"ports_otel"`
	Version    string    `json:"version"`
}

// serveHealthPtr holds *serveHealthState atomically.
var serveHealthPtr atomic.Pointer[serveHealthState]

// initServeHealthState sets the health snapshot. Called once from
// runParentServer before ListenAndServe so the health endpoint is
// immediately accurate.
func initServeHealthState(projectDir string, httpPort, otelPort int) {
	s := &serveHealthState{
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		ProjectDir: projectDir,
		HTTPPort:   httpPort,
		OtelPort:   otelPort,
		Version:    version,
	}
	serveHealthPtr.Store(s)
}

// projectSummary is the JSON shape returned by /api/projects entries.
// Fields are sourced exclusively from the registry — no DB access,
// no counts. Counts and per-project data live on the child and are
// fetched via /p/<id>/api/stats.
type projectSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Dir          string `json:"dir"`
	LastSeen     string `json:"lastSeen"`
	GitRemoteURL string `json:"gitRemoteURL,omitempty"`
}

// buildGlobalMux constructs the http.ServeMux for the parent doorway
// server. No DB access. Registry JSON only. Per-project data comes from
// the child via /p/<id>/api/*.
func buildGlobalMux() *http.ServeMux {
	mux := http.NewServeMux()

	// /api/mode — dashboard calls this on startup to detect global mode.
	mux.Handle("/api/mode", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, map[string]any{"mode": "global"})
	}))

	// /api/projects — registry list only. No DB access. Counts and
	// per-project data come from the child via /p/<id>/api/*.
	mux.Handle("/api/projects", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, listRegisteredProjects())
	}))

	// /api/health — lightweight liveness + identity endpoint. No DB
	// access. Returns in-memory state set at startup so callers can
	// verify which project this serve instance owns and which OTel port
	// it listens on. Used by the autostart discovery logic in
	// ensureServeForOtel to avoid false "serve not ready" warnings when
	// a healthy serve is already running.
	mux.Handle("/api/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h := serveHealthPtr.Load()
		if h == nil {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		now := time.Now()
		respondJSON(w, map[string]any{
			"pid":            h.PID,
			"started_at":     h.StartedAt.Format(time.RFC3339),
			"uptime_seconds": int(now.Sub(h.StartedAt).Seconds()),
			"version":        h.Version,
			"project_dir":    h.ProjectDir,
			"ports": map[string]int{
				"http": h.HTTPPort,
				"otel": h.OtelPort,
			},
		})
	}))

	// Serve the embedded dashboard SPA (index.html, css/, js/,
	// components/). The frontend calls /api/mode on startup to detect
	// global mode and render the projects landing.
	mux.Handle("/", http.FileServer(http.FS(dashboardSub())))

	return mux
}

// listRegisteredProjects re-reads the registry on each call and returns
// one summary per entry. No filesystem access beyond reading the registry
// JSON and a per-entry git-common-dir probe to filter out worktrees. No
// DB opens. No counts. If the registry file is missing or unreadable,
// returns an empty slice (not an error) so the dashboard landing renders
// an empty state rather than 500.
//
// Worktree filter: an entry whose ProjectDir is inside a linked git
// worktree (resolveViaGitCommonDir returns a different main-repo path)
// is NOT a standalone project — it's a working copy of an already
// registered project. We exclude it from the landing so the user sees
// one card per real project, not one card per worktree branch.
func listRegisteredProjects() []projectSummary {
	reg, err := registry.Load(registry.DefaultPath())
	if err != nil {
		return []projectSummary{}
	}
	entries := reg.List()
	out := make([]projectSummary, 0, len(entries))
	for _, e := range entries {
		if isLinkedWorktree(e.ProjectDir) {
			continue
		}
		out = append(out, projectSummary{
			ID:           e.ID,
			Name:         e.Name,
			Dir:          filepath.Base(e.ProjectDir),
			LastSeen:     e.LastSeen,
			GitRemoteURL: shortenGitRemote(e.GitRemoteURL),
		})
	}
	return out
}

// isLinkedWorktree returns true when dir is inside a git linked worktree
// (i.e. a `git worktree add`-created sibling checkout, not the main
// working copy). Used by the doorway project listing to collapse
// worktree branches under their parent project.
//
// Implementation: paths.ResolveViaGitCommonDir returns a non-empty
// string ONLY when dir is in a linked worktree AND the main repo root
// has a .htmlgraph/ directory. If the resolved path differs from the
// entry's own ProjectDir, the entry is a worktree.
func isLinkedWorktree(dir string) bool {
	mainRoot := paths.ResolveViaGitCommonDir(dir)
	if mainRoot == "" {
		return false
	}
	return filepath.Clean(mainRoot) != filepath.Clean(dir)
}

// shortenGitRemote converts a raw Git remote URL into an owner/repo slug.
// Examples:
//
//	https://github.com/owner/repo.git  → owner/repo
//	git@github.com:owner/repo.git      → owner/repo
//	ssh://git@github.com/owner/repo    → owner/repo
//
// If raw is empty or doesn't match a known pattern, it is returned as-is.
func shortenGitRemote(raw string) string {
	if raw == "" {
		return raw
	}
	s := raw

	// Guard: handle local-path and file:// remotes (don't leak filesystem paths).
	if remainder, ok := strings.CutPrefix(s, "file://"); ok {
		return filepath.Base(strings.TrimSuffix(remainder, ".git"))
	}
	if strings.HasPrefix(s, "/") || (len(s) > 1 && s[1] == ':') {
		// Unix absolute path or Windows absolute path (e.g. C:/...).
		return filepath.Base(strings.TrimSuffix(s, ".git"))
	}

	// Strip ssh://git@ prefix.
	s = strings.TrimPrefix(s, "ssh://git@")

	// Strip git@ prefix (SCP-style).
	s = strings.TrimPrefix(s, "git@")

	// Strip https:// or http:// prefix.
	if after, ok := strings.CutPrefix(s, "https://"); ok {
		s = after
	} else if after, ok := strings.CutPrefix(s, "http://"); ok {
		s = after
	}

	// Strip host (everything up to the first '/' or ':').
	if idx := strings.IndexAny(s, "/:"); idx >= 0 {
		s = s[idx+1:]
	}

	// Strip trailing .git.
	s = strings.TrimSuffix(s, ".git")

	if s == "" {
		return raw
	}
	return s
}
