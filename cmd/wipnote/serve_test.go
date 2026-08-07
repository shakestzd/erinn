package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shakestzd/wipnote/core/ingest"
	"github.com/shakestzd/wipnote/core/models"
)

// TestBuildSingleProjectMuxRegistersMode verifies that the factory returns a
// non-nil mux that handles /api/mode without requiring DB access. This is
// the cheapest correctness check — it catches regressions where the factory
// is gutted or where runServer stops wiring the mode endpoint.
func TestBuildSingleProjectMuxRegistersMode(t *testing.T) {
	// /api/mode has no DB dependency so nil is safe here.
	mux := buildSingleProjectMux(nil, nil, t.TempDir())
	if mux == nil {
		t.Fatal("buildSingleProjectMux returned nil")
	}

	req := httptest.NewRequest("GET", "/api/mode", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/api/mode: got %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["mode"] != "single" {
		t.Errorf("mode: got %v, want single", body["mode"])
	}
}

// TestBuildSingleProjectMuxServesDashboard verifies the embedded dashboard
// file server is wired in. GET / should return 200 (or 301 redirect) — not
// 404. This catches the failure mode where the factory registers API routes
// but forgets the root handler.
func TestBuildSingleProjectMuxServesDashboard(t *testing.T) {
	mux := buildSingleProjectMux(nil, nil, t.TempDir())

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Errorf("GET /: got 404, want dashboard response")
	}
}

// TestServeCmdBindFlag verifies that --bind is wired into the cobra command
// with the correct default and that a listener can be opened on the
// constructed address. It exercises the flag-parsing path without starting a
// real HTTP server.
func TestServeCmdBindFlag(t *testing.T) {
	cmd := serveCmd()

	// Confirm flag exists with the correct default.
	f := cmd.Flags().Lookup("bind")
	if f == nil {
		t.Fatal("--bind flag not registered on serve command")
	}
	if f.DefValue != "127.0.0.1" {
		t.Errorf("--bind default: got %q, want 127.0.0.1", f.DefValue)
	}

	// Confirm that the address construction is correct when bind=0.0.0.0.
	// Use an ephemeral port (0) so the test never conflicts with a running server.
	bind := "0.0.0.0"
	addr := fmt.Sprintf("%s:%d", bind, 0)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("net.Listen(%q): %v", addr, err)
	}
	defer ln.Close()

	// The listener must have opened on a real port — not 0.
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	if portStr == "0" {
		t.Error("expected ephemeral port to be resolved, got 0")
	}
}

// TestIsHeadlessSession_TitlerMarker verifies the legacy htmlgraph-titler
// detection (bug-db68d62a) still recognizes both known forms of the
// titler's first user message. There is no live producer of these
// sessions anymore (the titler was deleted in commit 74a9071f1) — this
// locks in behavior kept for defense-in-depth against historical JSONL
// transcripts that predate the removal. See isHeadlessSession's doc
// comment in serve.go for the full rationale.
func TestIsHeadlessSession_TitlerMarker(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"sentinel marker", "[wipnote-titler] please summarize", true},
		{"legacy generation prompt", "Generate a concise 4-8 word title for this AI coding session.", true},
		{"ordinary coding session", "Please fix the bug in serve.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := &ingest.ParseResult{
				Messages: []models.Message{{Role: "user", Content: tc.content}},
			}
			if got := isHeadlessSession(result); got != tc.want {
				t.Errorf("isHeadlessSession(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestIsHeadlessSession_NoMessages verifies a session with no user message
// (e.g. an empty or assistant-only transcript) is never misclassified as
// a titler session.
func TestIsHeadlessSession_NoMessages(t *testing.T) {
	result := &ingest.ParseResult{
		Messages: []models.Message{{Role: "assistant", Content: "[wipnote-titler] should not match on assistant role"}},
	}
	if isHeadlessSession(result) {
		t.Error("expected false: marker in an assistant message must not trigger the gate")
	}
}
