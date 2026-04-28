package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/shakestzd/htmlgraph/internal/childproc"
	"github.com/shakestzd/htmlgraph/internal/registry"
)

// validProjectIDRE matches the 8-char SHA256 prefix the registry assigns
// to each project. Any request to /p/<id>/... with an id that does not
// match this regex is rejected with 400 before the registry lookup.
var validProjectIDRE = regexp.MustCompile(`^[a-f0-9]{4,64}$`)

// isValidProjectID rejects empty, ".", "..", path separators, null bytes,
// and anything outside the project-ID character set. A defense-in-depth
// guard against path traversal in the proxy router.
func isValidProjectID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, "/\\\x00") {
		return false
	}
	return validProjectIDRE.MatchString(id)
}

// proxyHandler parses /p/<id>/<rest>, validates the project ID, looks up
// the entry in the registry, asks the supervisor for a warm or cold
// child, and forwards the request via the child's pre-built reverse
// proxy.
//
// Status codes:
//
//	400 — invalid project ID (empty, traversal, bad chars)
//	404 — unknown project ID (not in registry)
//	500 — registry load failure
//	502 — child spawn or reach failure (supervisor error or proxy error)
func proxyHandler(sup *childproc.Supervisor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/p/")
		var projectID, remainder string
		if i := strings.Index(rest, "/"); i >= 0 {
			projectID = rest[:i]
			remainder = rest[i:]
		} else {
			projectID = rest
			remainder = "/"
		}

		if !isValidProjectID(projectID) {
			http.Error(w, "invalid project id", http.StatusBadRequest)
			return
		}

		reg, err := registry.Load(registry.DefaultPath())
		if err != nil {
			http.Error(w, "load registry: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var matched *registry.Entry
		for _, e := range reg.List() {
			if e.ID == projectID {
				e := e // capture
				matched = &e
				break
			}
		}
		if matched == nil {
			http.Error(w, "unknown project id", http.StatusNotFound)
			return
		}

		child, err := sup.GetOrSpawn(r.Context(), projectID, matched.ProjectDir)
		if err != nil {
			http.Error(w, "spawn child: "+err.Error(), http.StatusBadGateway)
			return
		}
		child.LastRequest.Store(time.Now().Unix())

		// Strip the /p/<id> prefix before forwarding so the child sees
		// the original URL path (e.g. /api/stats, /plans/plan-xyz.html).
		// Use path.Clean to normalise — rejects ".." escapes via the
		// Clean semantics, not our regex.
		r.URL.Path = path.Clean(remainder)
		if r.URL.Path == "." {
			r.URL.Path = "/"
		}

		child.Proxy.ServeHTTP(w, r)
	}
}

// autoDetectCurrentProject walks up from CWD looking for a .htmlgraph/
// directory. If found, registers it in the global registry (if not
// already present) and returns its registry entry. Returns nil if the
// current directory is outside any HtmlGraph project — in that case, the
// parent server still runs, it just doesn't print a deep-link URL.
func autoDetectCurrentProject() *registry.Entry {
	htmlgraphDir, err := findHtmlgraphDir()
	if err != nil {
		return nil
	}
	projectRoot := filepath.Dir(htmlgraphDir)
	id := registry.ComputeID(projectRoot)

	regPath := registry.DefaultPath()
	reg, _ := registry.Load(regPath)
	for _, e := range reg.List() {
		if e.ID == id {
			e := e
			return &e
		}
	}
	// Upsert and save; best-effort.
	reg.Upsert(projectRoot, filepath.Base(projectRoot), "")
	_ = reg.Save()
	for _, e := range reg.List() {
		if e.ID == id {
			e := e
			return &e
		}
	}
	return nil
}

// runParentServer replaces the old runGlobalServer / runServer single-
// project path with the new parent-with-proxy behaviour.
//
// Responsibilities:
//
//  1. Build the parent mux (landing SPA, /api/mode, /api/projects,
//     /p/<id>/* proxy — temporarily also wraps the legacy cross-project
//     aggregates until slice 4 deletes them).
//  2. Instantiate the childproc.Supervisor and start its idle reaper.
//  3. Auto-detect the current project and register it in the registry,
//     then print doorway + deep-link URLs.
//  4. Plumb SIGINT/SIGTERM through http.Server.RegisterOnShutdown to
//     SIGTERM all children cleanly before exit.
func runParentServer(bind string, port int) error {
	sup := childproc.NewSupervisor(childproc.Options{})
	reaperCtx, stopReaper := context.WithCancel(context.Background())
	defer stopReaper()
	go sup.RunIdleReaper(reaperCtx)

	mux := buildParentMux(sup)

	// One-time startup prune: self-heal a corrupted registry by removing
	// entries whose .htmlgraph/ directory no longer exists, then save.
	// This runs once at server startup so stale entries from previous test
	// runs or deleted projects don't accumulate indefinitely. Best-effort:
	// errors are silently ignored so a broken registry never blocks startup.
	if reg, err := registry.Load(registry.DefaultPath()); err == nil {
		_ = reg.Save() // Save calls Prune internally before writing.
	}

	addr := fmt.Sprintf("%s:%d", bind, port)

	// Auto-detect & print URLs.
	entry := autoDetectCurrentProject()
	fmt.Printf("HtmlGraph:            http://%s/\n", addr)
	if entry != nil {
		fmt.Printf("Current project:     http://%s/p/%s/\n", addr, entry.ID)
	}
	fmt.Println("Press Ctrl+C to stop.")

	// Write the per-project serve lockfile so concurrent launcher invocations
	// (from a second terminal opening another Claude session) detect a live
	// serve process and skip spawning a duplicate that would collide on :8080.
	// Best-effort: missing .htmlgraph/ or write errors are silently ignored.
	if htmlgraphDir, err := findHtmlgraphDir(); err == nil {
		projectDir := filepath.Dir(htmlgraphDir)
		writeServeLock(projectDir)
		defer removeServeLock(projectDir)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	server.RegisterOnShutdown(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sup.Shutdown(shutdownCtx)
	})

	// Signal handler for graceful shutdown.
	idleConnsClosed := make(chan struct{})
	go func() {
		sigC := make(chan os.Signal, 1)
		signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
		<-sigC
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		close(idleConnsClosed)
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-idleConnsClosed
	return nil
}

// buildParentMux wires the parent server routes on top of the existing
// buildGlobalMux (which slice 4 trims). The parent-exclusive addition is
// the /p/<id>/* reverse proxy handler registered here.
func buildParentMux(sup *childproc.Supervisor) *http.ServeMux {
	mux := buildGlobalMux()
	mux.Handle("/p/", proxyHandler(sup))
	return mux
}

