package main

import (
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/graph"
)

// TestSessionLedger_LedgerOnlySessionRenders is the renderability half of
// feat-1b08a194, and it exists because passing the target-validity gate is NOT
// the same thing as being readable.
//
// Session titles are resolved from the sessions TABLE by three independent
// readers. A change that made session targets valid from the ledger without
// giving those readers something to read would produce a node that indexes as
// ordinarily live, carries no tombstone marker, and still renders blank —
// strictly worse than the tombstone it replaced, which at least explained the
// blank (hazard card edge-target-validity-and-renderability-are-separate).
//
// So this test does not assert that the edge survived; the derivation census
// already does that. It asserts that a reader asking for the session's title
// gets a real one, through the production readers, with a tombstoned session as
// the control for what "unresolvable" still looks like.
func TestSessionLedger_LedgerOnlySessionRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildEdgeDerivationFixture(t)
	setupReindexTestEnv(t, projectDir)
	runReindexInDir(t, projectDir)

	database := openCachedDB(t, projectDir)
	defer database.Close()

	// Reader 1 — resolveProvenanceNode (/api/provenance and `wipnote lineage`).
	node, ok := resolveProvenanceNode(database, deriveLedgerSession, "")
	if !ok {
		t.Fatalf("resolveProvenanceNode did not resolve the ledger-only session %s at all",
			deriveLedgerSession)
	}
	if node.Type != "session" {
		t.Errorf("resolveProvenanceNode: type = %q, want %q", node.Type, "session")
	}
	assertRenderableTitle(t, "resolveProvenanceNode", deriveLedgerSession, node.Title)

	// Reader 2 — graph.QueryBuilder.resolveNodes, reached through the same
	// public entry point `wipnote lineage` uses.
	results, err := graph.NewQuery(database).From(deriveLedgerSession).Execute()
	if err != nil {
		t.Fatalf("graph query for %s: %v", deriveLedgerSession, err)
	}
	if len(results) != 1 {
		t.Fatalf("graph query for %s returned %d nodes, want 1", deriveLedgerSession, len(results))
	}
	if results[0].Type != "session" {
		t.Errorf("resolveNodes: type = %q, want %q", results[0].Type, "session")
	}
	assertRenderableTitle(t, "resolveNodes", deriveLedgerSession, results[0].Title)

	// CONTROL — a session with neither a ledger row nor telemetry must still be
	// unresolvable. Without this the assertions above would pass just as well
	// under a reader that invented a label for every id it was handed.
	if pruned, found := resolveProvenanceNode(database, derivePrunedSession, ""); found {
		t.Errorf("a pruned session with NO ledger row resolved anyway: %s title=%q.\n"+
			"The ledger is meant to be the thing that makes a session resolvable; if an unrecorded\n"+
			"session resolves too, the ledger is not what made the difference.",
			derivePrunedSession, pruned.Title)
	}
}

// assertRenderableTitle rejects the exact failure the hazard card describes: a
// node that resolves but has nothing to show. An empty title is the blank node;
// a title that is only the raw id is the same blank wearing a uuid.
func assertRenderableTitle(t *testing.T, reader, sessionID, title string) {
	t.Helper()
	if strings.TrimSpace(title) == "" {
		t.Errorf("%s: ledger-only session %s resolved with an EMPTY title — it indexes as live, "+
			"carries no tombstone marker, and renders blank, which is worse than the tombstone it replaced",
			reader, sessionID)
		return
	}
	if strings.TrimSpace(title) == sessionID {
		t.Errorf("%s: ledger-only session %s resolved with its own id as the title (%q) — "+
			"nothing a reader can read", reader, sessionID, title)
	}
}

// TestSessionLedger_ProjectionDoesNotClobberTelemetry pins the insert-if-absent
// rule. A session with live telemetry has a row carrying real event counts and
// a generated title; the projection filling four fields over the top of it would
// be a downgrade dressed as an enrichment.
func TestSessionLedger_ProjectionDoesNotClobberTelemetry(t *testing.T) {
	if testing.Short() {
		t.Skip("drives reindex integration flow")
	}

	projectDir := buildEdgeDerivationFixture(t)
	setupReindexTestEnv(t, projectDir)

	// Give the LIVE session — which has session HTML and events — a ledger row
	// too. That is the normal steady state once this feature has been running:
	// every session is in both places.
	writeFixtureSessionLedgerRow(t, projectDir+"/.wipnote", deriveLiveSession)

	runReindexInDir(t, projectDir)

	database := openCachedDB(t, projectDir)
	defer database.Close()

	var agent string
	var events int
	err := database.QueryRow(
		`SELECT COALESCE(agent_assigned,''), COALESCE(total_events,0) FROM sessions WHERE session_id = ?`,
		deriveLiveSession,
	).Scan(&agent, &events)
	if err != nil {
		t.Fatalf("read live session row: %v", err)
	}
	// parseSessionHTML writes the real agent from the session HTML; the
	// projection writes the placeholder "session". Seeing the placeholder means
	// the projection overwrote a telemetry-backed row.
	if agent == "session" {
		t.Errorf("the sessions-ledger projection overwrote a telemetry-backed row: "+
			"agent_assigned = %q for %s, which is the projection's placeholder, not the value "+
			"parseSessionHTML wrote", agent, deriveLiveSession)
	}
}
