package main

import (
	"database/sql"
	"fmt"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// setupHierarchyDB opens an in-memory SQLite DB with the full schema for
// session hierarchy and agent lineage tests.
func setupHierarchyDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open hierarchy test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// seedSession inserts a minimal sessions row.
func seedSession(t *testing.T, db *sql.DB, sessionID, parentSessionID, status string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO sessions (session_id, agent_assigned, parent_session_id, status, created_at)
		VALUES (?, 'claude', ?, ?, '2026-04-16T00:00:00Z')`,
		sessionID, nullableString(parentSessionID), status)
	if err != nil {
		t.Fatalf("seedSession %s: %v", sessionID, err)
	}
}

// nullableString returns nil for empty strings (for nullable TEXT columns).
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// seedAgentEventForHierarchy inserts a minimal agent_events row.
func seedAgentEventForHierarchy(t *testing.T, db *sql.DB, sessionID, featureID, suffix string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO agent_events (event_id, session_id, agent_id, feature_id, event_type, created_at)
		VALUES (?, ?, 'agent-test', ?, 'tool_call', '2026-04-16T00:00:00Z')`,
		sessionID+"-evt-"+suffix, sessionID, featureID)
	if err != nil {
		t.Fatalf("seedAgentEventForHierarchy session=%s feature=%s: %v", sessionID, featureID, err)
	}
}

// seedLineageTrace inserts a minimal agent_lineage_trace row.
func seedLineageTrace(t *testing.T, db *sql.DB, traceID, sessionID, rootSessionID, featureID string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO agent_lineage_trace (trace_id, session_id, root_session_id, feature_id)
		VALUES (?, ?, ?, ?)`,
		traceID, sessionID, rootSessionID, nullableString(featureID))
	if err != nil {
		t.Fatalf("seedLineageTrace trace=%s: %v", traceID, err)
	}
}

// TestLoadSessionHierarchyEdges_SpawnedEdge verifies that a parent→child
// session pair produces a "spawned" edge.
func TestLoadSessionHierarchyEdges_SpawnedEdge(t *testing.T) {
	db := setupHierarchyDB(t)
	seedSession(t, db, "sess-parent", "", "completed")
	seedSession(t, db, "sess-child", "sess-parent", "completed")

	edges := loadSessionHierarchyEdges(db)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.Source != "sess-parent" {
		t.Errorf("Source: got %q, want %q", e.Source, "sess-parent")
	}
	if e.Target != "sess-child" {
		t.Errorf("Target: got %q, want %q", e.Target, "sess-child")
	}
	if e.Type != "spawned" {
		t.Errorf("Type: got %q, want %q", e.Type, "spawned")
	}
}

// TestLoadSessionHierarchyEdges_NoEdgeWhenNoParent verifies that a session
// without a parent_session_id produces no edge.
func TestLoadSessionHierarchyEdges_NoEdgeWhenNoParent(t *testing.T) {
	db := setupHierarchyDB(t)
	seedSession(t, db, "sess-alone", "", "completed")

	edges := loadSessionHierarchyEdges(db)
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for session with no parent, got %d: %+v", len(edges), edges)
	}
}

// TestLoadAgentLineageEdges_SpawnedAndWorkedOn verifies that agent_lineage_trace
// rows produce "spawned" edges from root→child and "worked_on" edges where
// feature_id is set.
func TestLoadAgentLineageEdges_SpawnedAndWorkedOn(t *testing.T) {
	db := setupHierarchyDB(t)
	seedSession(t, db, "sess-root", "", "completed")
	seedSession(t, db, "sess-sub", "sess-root", "completed")
	seedLineageTrace(t, db, "trace-001", "sess-sub", "sess-root", "feat-abc123")

	edges := loadAgentLineageEdges(db)

	var spawned, workedOn int
	for _, e := range edges {
		switch e.Type {
		case "spawned":
			spawned++
			if e.Source != "sess-root" || e.Target != "sess-sub" {
				t.Errorf("spawned edge: got src=%q tgt=%q, want src=sess-root tgt=sess-sub", e.Source, e.Target)
			}
		case "worked_on":
			workedOn++
			if e.Source != "sess-sub" || e.Target != "feat-abc123" {
				t.Errorf("worked_on edge: got src=%q tgt=%q, want src=sess-sub tgt=feat-abc123", e.Source, e.Target)
			}
		}
	}
	if spawned != 1 {
		t.Errorf("expected 1 spawned edge, got %d", spawned)
	}
	if workedOn != 1 {
		t.Errorf("expected 1 worked_on edge, got %d", workedOn)
	}
}

// TestLoadAgentLineageEdges_SameSessionAsRootProducesNoSpawnedEdge verifies
// that lineage rows where session_id == root_session_id produce no spawned edge.
func TestLoadAgentLineageEdges_SameSessionAsRootProducesNoSpawnedEdge(t *testing.T) {
	db := setupHierarchyDB(t)
	seedSession(t, db, "sess-root", "", "completed")
	// session_id == root_session_id — should be filtered by the WHERE clause
	seedLineageTrace(t, db, "trace-self", "sess-root", "sess-root", "")

	edges := loadAgentLineageEdges(db)
	for _, e := range edges {
		if e.Type == "spawned" {
			t.Errorf("unexpected spawned edge when session_id == root_session_id: %+v", e)
		}
	}
}

// TestLoadGraphNodes_IncludesSubagentSessions verifies that sessions present in
// agent_lineage_trace are included as nodes even when they have <=5 agent_events
// and no messages.
func TestLoadGraphNodes_IncludesSubagentSessions(t *testing.T) {
	db := setupHierarchyDB(t)

	// Seed the feature referenced by agent_events below.
	_, err := db.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-rootfeat', 'feature', 'root feat', 'done')`)
	if err != nil {
		t.Fatalf("insert feature: %v", err)
	}

	// Root session with enough events to pass old filter.
	seedSession(t, db, "sess-root-inc", "", "completed")
	for i := 0; i < 6; i++ {
		seedAgentEventForHierarchy(t, db, "sess-root-inc", "feat-rootfeat", fmt.Sprintf("%d", i))
	}
	_, err = db.Exec(`INSERT INTO messages (session_id, ordinal, role, content) VALUES ('sess-root-inc', 1, 'user', 'hello')`)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	// Subagent session with only 2 events and no messages — would be excluded
	// by the old filter but included because it's in agent_lineage_trace.
	seedSession(t, db, "sess-sub-inc", "sess-root-inc", "completed")
	seedAgentEventForHierarchy(t, db, "sess-sub-inc", "feat-rootfeat", "0")
	seedAgentEventForHierarchy(t, db, "sess-sub-inc", "feat-rootfeat", "1")
	seedLineageTrace(t, db, "trace-sub-inc", "sess-sub-inc", "sess-root-inc", "feat-rootfeat")

	nodes, _, err := loadGraphNodes(db, nil)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	found := false
	for _, n := range nodes {
		if n.ID == "sess-sub-inc" && n.Type == "session" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("subagent session sess-sub-inc not found in graph nodes; nodes: %+v", nodes)
	}
}

// TestLoadGraphNodes_SessionCappedAt500 verifies that the session node count
// does not exceed 500 even when more rows exist.
func TestLoadGraphNodes_SessionCappedAt500(t *testing.T) {
	db := setupHierarchyDB(t)

	// Insert 600 sessions all with parent_session_id set so they qualify via
	// the relaxed filter.
	seedSession(t, db, "sess-root-cap", "", "completed")
	for i := 0; i < 600; i++ {
		sid := fmt.Sprintf("sess-cap-%04d", i)
		_, err := db.Exec(`
			INSERT INTO sessions (session_id, agent_assigned, parent_session_id, status, created_at)
			VALUES (?, 'claude', 'sess-root-cap', 'completed', '2026-04-16T00:00:00Z')`, sid)
		if err != nil {
			t.Fatalf("insert session %s: %v", sid, err)
		}
	}

	nodes, _, err := loadGraphNodes(db, nil)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	sessionCount := 0
	for _, n := range nodes {
		if n.Type == "session" {
			sessionCount++
		}
	}
	if sessionCount > 500 {
		t.Errorf("session node count %d exceeds cap of 500", sessionCount)
	}
}

// TestDeduplicateEdges_NoDuplicatesBetweenHierarchyAndLineage verifies that
// edges produced by both loadSessionHierarchyEdges and loadAgentLineageEdges
// for the same relationship are deduplicated.
func TestDeduplicateEdges_NoDuplicatesBetweenHierarchyAndLineage(t *testing.T) {
	db := setupHierarchyDB(t)
	seedSession(t, db, "sess-root-dup", "", "completed")
	seedSession(t, db, "sess-child-dup", "sess-root-dup", "completed")
	seedLineageTrace(t, db, "trace-dup", "sess-child-dup", "sess-root-dup", "")

	hierarchyEdges := loadSessionHierarchyEdges(db)
	lineageEdges := loadAgentLineageEdges(db)

	all := append(hierarchyEdges, lineageEdges...)
	deduped := deduplicateEdges(all)

	spawnedCount := 0
	for _, e := range deduped {
		if e.Type == "spawned" && e.Source == "sess-root-dup" && e.Target == "sess-child-dup" {
			spawnedCount++
		}
	}
	if spawnedCount != 1 {
		t.Errorf("expected exactly 1 spawned edge after dedup, got %d; all deduped: %+v", spawnedCount, deduped)
	}
}
