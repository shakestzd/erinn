package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/sessionledger"
)

// Session ids in these fixtures are UUID-shaped on purpose: the ledger refuses
// anything else (core/graph.IsSessionShapedID), because id shape is the only
// signal the edge-target gate has for telling a pruned session from a dangling
// reference to a work item that never existed.

// setupSessionEndFixture creates a bare project with a .wipnote directory,
// chdirs into it, and neutralises the environment that would otherwise make
// findWipnoteDir resolve to the developer's real repo. Returns the .wipnote dir.
func setupSessionEndFixture(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	// t.TempDir + chdir is NOT isolation. paths.ResolveProjectDir consults
	// WIPNOTE_PROJECT_DIR and CLAUDE_PROJECT_DIR ahead of the working
	// directory, and both are set in an agent session — so without this a test
	// that drives a command entry point writes to the REAL .wipnote/.
	isolateProjectDir(t, projectDir)
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(projectDir))
	return wipnoteDir
}

func openLedgerSession(t *testing.T, wipnoteDir, sessionID string, startedAt time.Time) {
	t.Helper()
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID:  sessionID,
		Harness:    "claude_code",
		ProjectDir: filepath.Dir(wipnoteDir),
		StartedAt:  startedAt,
	}); err != nil {
		t.Fatalf("open ledger session %s: %v", sessionID, err)
	}
}

func requireLedgerClosed(t *testing.T, wipnoteDir, sessionID string, wantClosed bool) {
	t.Helper()
	rec, found, err := sessionledger.NewStore(wipnoteDir).Get(sessionID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !found {
		t.Fatalf("session %s vanished from the ledger", sessionID)
	}
	if wantClosed && rec.IsOpen() {
		t.Errorf("session %s is still open in %s — `wipnote session end` reported success without recording the end anywhere durable (bug-aa0bbd43)",
			sessionID, sessionledger.NewStore(wipnoteDir).RelPath())
	}
	if !wantClosed && !rec.IsOpen() {
		t.Errorf("session %s was closed, want left open", sessionID)
	}
}

// TestRunSessionEnd_ClosesCanonicalLedgerWithoutDaemon is the bug-aa0bbd43
// regression. No writer daemon is running in this test, which is exactly the
// daemon-miss case that used to be untested: runSessionEnd fell through to
// dbpkg.UpdateSessionStatus against the throwaway in-memory projection openDB
// hands out, closed moments later. The command printed "Ended session" and the
// session stayed open forever.
//
// The assertion is on canonical state — the sessions ledger — because that is
// the only place an end can survive the process.
func TestRunSessionEnd_ClosesCanonicalLedgerWithoutDaemon(t *testing.T) {
	wipnoteDir := setupSessionEndFixture(t)
	const sessionID = "11111111-1111-4111-8111-111111111111"
	openLedgerSession(t, wipnoteDir, sessionID, time.Now().UTC().Add(-time.Hour))

	if err := runSessionEnd(sessionID); err != nil {
		t.Fatalf("runSessionEnd: %v", err)
	}
	requireLedgerClosed(t, wipnoteDir, sessionID, true)
}

// TestRunSessionEnd_NoArgClosesMostRecentOpenSession covers the implicit form.
// The session to end must be resolved from the ledger too: resolving it from
// the projection would have worked only for as long as something rebuilt the
// projection, and would disagree with the ledger the close is written to.
func TestRunSessionEnd_NoArgClosesMostRecentOpenSession(t *testing.T) {
	wipnoteDir := setupSessionEndFixture(t)
	now := time.Now().UTC()
	openLedgerSession(t, wipnoteDir, "22222222-2222-4222-8222-222222222222", now.Add(-2*time.Hour))
	openLedgerSession(t, wipnoteDir, "33333333-3333-4333-8333-333333333333", now.Add(-10*time.Minute))

	if err := runSessionEnd(""); err != nil {
		t.Fatalf("runSessionEnd(\"\"): %v", err)
	}
	requireLedgerClosed(t, wipnoteDir, "33333333-3333-4333-8333-333333333333", true)
	requireLedgerClosed(t, wipnoteDir, "22222222-2222-4222-8222-222222222222", false)
}

// TestRunSessionEnd_AlreadyClosedIsNotMovedForward guards the ledger's
// first-end-wins rule through the command: re-ending a closed session must not
// rewrite its end time.
func TestRunSessionEnd_AlreadyClosedIsNotMovedForward(t *testing.T) {
	wipnoteDir := setupSessionEndFixture(t)
	const sessionID = "44444444-4444-4444-8444-444444444444"
	openLedgerSession(t, wipnoteDir, sessionID, time.Now().UTC().Add(-time.Hour))

	if err := runSessionEnd(sessionID); err != nil {
		t.Fatalf("first runSessionEnd: %v", err)
	}
	first, _, err := sessionledger.NewStore(wipnoteDir).Get(sessionID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	if err := runSessionEnd(sessionID); err != nil {
		t.Fatalf("second runSessionEnd: %v", err)
	}
	second, _, err := sessionledger.NewStore(wipnoteDir).Get(sessionID)
	if err != nil {
		t.Fatalf("re-read ledger: %v", err)
	}
	if !second.EndedAt.Equal(first.EndedAt) {
		t.Errorf("end time moved from %s to %s on a second end — the first recorded end is the true one",
			first.EndedAt, second.EndedAt)
	}
}

// TestRunSessionEnd_UnknownSessionIsAnError: a session with no ledger row
// cannot be ended, and saying so beats reporting success. Before the fix this
// path wrote to a discarded projection and printed "Ended session".
func TestRunSessionEnd_UnknownSessionIsAnError(t *testing.T) {
	setupSessionEndFixture(t)

	err := runSessionEnd("55555555-5555-4555-8555-555555555555")
	if err == nil {
		t.Fatal("runSessionEnd on an unknown session returned nil — it reported success for a session it did not end")
	}
}

// TestRunSessionStart_WritesCanonicalLedger is the twin regression to the
// session-end tests above. `wipnote session start` used to route via
// apply.RouteSessionInsert into the daemon's ephemeral projection and fall back
// to a local handle it immediately closed, so it printed a session id that no
// later command could find.
func TestRunSessionStart_WritesCanonicalLedger(t *testing.T) {
	wipnoteDir := setupSessionEndFixture(t)

	if err := runSessionStart("claude-code"); err != nil {
		t.Fatalf("runSessionStart: %v", err)
	}

	open, err := sessionsFromLedger(wipnoteDir, true, 0)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ledger holds %d open session(s), want 1 — session start recorded nothing durable", len(open))
	}
	if open[0].AgentAssigned != "claude-code" {
		t.Errorf("harness = %q, want %q", open[0].AgentAssigned, "claude-code")
	}

	// The id must be one `wipnote session end` can act on — that round trip is
	// the whole point, and a non-session-shaped id would have been rejected by
	// the ledger rather than merely looking odd.
	if err := runSessionEnd(open[0].SessionID); err != nil {
		t.Fatalf("runSessionEnd on the id session start minted: %v", err)
	}
	requireLedgerClosed(t, wipnoteDir, open[0].SessionID, true)
}
