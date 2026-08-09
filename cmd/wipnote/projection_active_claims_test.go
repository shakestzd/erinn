package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// TestProjectionDerivesActiveClaimsFromLedger proves the hydration chain that
// feat-fc3cc9e0 left broken: an OPEN claim-ledger episode must surface as an
// active_work_items row AND as sessions.active_feature_id in any freshly built
// projection.
//
// Before this pass, active_work_items was written only by the hooks at runtime
// and persisted in the project database. With the projection rebuilt per
// process, nothing populated it — so every CLI read of "what is this agent on"
// returned empty, and both resumable-session queries (which LEFT JOIN it) made
// every `wipnote continue` report "no resumable session metadata found".
//
// It seeds ONLY canonical artifacts, deliberately: a fixture that seeded a DB
// handle would prove nothing, because every openDB call returns its own private
// in-memory database.
func TestProjectionDerivesActiveClaimsFromLedger(t *testing.T) {
	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	for _, sub := range []string{"features", "sessions", "claims"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	const sessionID = "019ee378-abcd-7000-8000-0000000000aa"
	const workItemID = "feat-ab12cd34"

	html := `<!DOCTYPE html><html><body><article id="` + workItemID +
		`" data-type="feature" data-status="in-progress" data-priority="medium">` +
		`<h1>Claimed feature</h1></article></body></html>`
	if err := os.WriteFile(filepath.Join(wipnoteDir, "features", workItemID+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID,
		Harness:   "claude-code",
		StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed session ledger: %v", err)
	}
	seedEpisode(t, wipnoteDir, sessionID, sessionID, dbpkg.AgentRootSentinel, workItemID,
		time.Now().UTC().Add(-30*time.Minute), time.Time{}, "")

	database, err := openDB(wipnoteDir)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()

	if got := dbpkg.GetActiveWorkItem(database, sessionID, dbpkg.AgentRootSentinel); got != workItemID {
		t.Errorf("active_work_items: got %q, want %q", got, workItemID)
	}
	if got := dbpkg.GetActiveFeatureIDForSession(database, sessionID); got != workItemID {
		t.Errorf("sessions.active_feature_id: got %q, want %q", got, workItemID)
	}
}

// TestProjectionIgnoresClosedClaims is the negative half: a CLOSED episode is
// history, not current state, and must not appear as a live claim.
func TestProjectionIgnoresClosedClaims(t *testing.T) {
	projectRoot := t.TempDir()
	wipnoteDir := filepath.Join(projectRoot, ".wipnote")
	for _, sub := range []string{"features", "sessions", "claims"} {
		if err := os.MkdirAll(filepath.Join(wipnoteDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	const sessionID = "019ee378-abcd-7000-8000-0000000000bb"
	const workItemID = "feat-ab12cd35"

	if _, err := sessionledger.NewStore(wipnoteDir).Open(sessionledger.Record{
		SessionID: sessionID, Harness: "claude-code", StartedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed session ledger: %v", err)
	}
	started := time.Now().UTC().Add(-30 * time.Minute)
	seedEpisode(t, wipnoteDir, sessionID, sessionID, dbpkg.AgentRootSentinel, workItemID,
		started, started.Add(10*time.Minute), claimledger.OutcomeCompleted)

	database, err := openDB(wipnoteDir)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()

	if got := dbpkg.GetActiveWorkItem(database, sessionID, dbpkg.AgentRootSentinel); got != "" {
		t.Errorf("a closed episode must not be a live claim; got %q", got)
	}
	if got := dbpkg.GetActiveFeatureIDForSession(database, sessionID); got != "" {
		t.Errorf("a closed episode must not set active_feature_id; got %q", got)
	}
}
