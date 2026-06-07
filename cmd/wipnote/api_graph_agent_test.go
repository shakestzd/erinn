package main

import (
	"database/sql"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

func setupAgentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open agent test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestLoadGraphNodesIncludesAgents verifies that loadGraphNodes returns agent
// nodes derived from the agent_lineage_trace table.
// TestLoadGraphNodes_AgentNodesOmitted verifies that agent names do NOT
// surface as graph nodes. Agents are the actor driving work, not a
// thing in the graph — they're exposed via the "Filter by agent"
// dropdown which scopes the graph to the work a given agent touched.
// Design decision: graph clutter reduction.
func TestLoadGraphNodes_AgentNodesOmitted(t *testing.T) {
	db := setupAgentTestDB(t)
	_, err := db.Exec(`
		INSERT INTO agent_lineage_trace (trace_id, root_session_id, session_id, agent_name, feature_id)
		VALUES ('t1', 'root-1', 'sess-1', 'wipnote:researcher', 'feat-aaa')`)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}
	for _, n := range nodes {
		if n.Type == "agent" {
			t.Errorf("expected no agent nodes, got %s", n.ID)
		}
	}
}

// TestLoadAgentEdgesRanAs verifies that loadAgentEdges produces ran_as edges
// from agent to session.
func TestLoadAgentEdgesRanAs(t *testing.T) {
	db := setupAgentTestDB(t)

	_, err := db.Exec(`
		INSERT INTO agent_lineage_trace (trace_id, root_session_id, session_id, agent_name, feature_id)
		VALUES
			('t1', 'root-1', 'sess-a', 'wipnote:researcher', ''),
			('t2', 'root-1', 'sess-b', 'wipnote:feature-coder', '')`)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}

	edges := loadAgentEdges(db)

	ranAs := make(map[string]string) // agent -> session
	for _, e := range edges {
		if e.Type == "ran_as" {
			ranAs[e.Source] = e.Target
		}
	}

	if ranAs["wipnote:researcher"] != "sess-a" {
		t.Errorf("ran_as edge: want wipnote:researcher -> sess-a, got %v", ranAs["wipnote:researcher"])
	}
	if ranAs["wipnote:feature-coder"] != "sess-b" {
		t.Errorf("ran_as edge: want wipnote:feature-coder -> sess-b, got %v", ranAs["wipnote:feature-coder"])
	}
}

// TestLoadAgentEdgesWorkedOn verifies that loadAgentEdges produces worked_on
// edges from agent to feature when feature_id is set.
func TestLoadAgentEdgesWorkedOn(t *testing.T) {
	db := setupAgentTestDB(t)

	_, err := db.Exec(`
		INSERT INTO agent_lineage_trace (trace_id, root_session_id, session_id, agent_name, feature_id)
		VALUES
			('t1', 'root-1', 'sess-a', 'wipnote:researcher', 'feat-111'),
			('t2', 'root-1', 'sess-b', 'wipnote:feature-coder', ''),
			('t3', 'root-2', 'sess-c', 'wipnote:researcher', 'feat-222')`)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}

	edges := loadAgentEdges(db)

	workedOn := make(map[string][]string) // agent -> []feature
	for _, e := range edges {
		if e.Type == "worked_on" {
			workedOn[e.Source] = append(workedOn[e.Source], e.Target)
		}
	}

	// wipnote:researcher should have worked_on edges for feat-111 and feat-222.
	researcherFeatures := workedOn["wipnote:researcher"]
	if len(researcherFeatures) != 2 {
		t.Errorf("wipnote:researcher worked_on: want 2 features, got %d: %v", len(researcherFeatures), researcherFeatures)
	}

	// wipnote:feature-coder has no feature_id, so no worked_on edge.
	if len(workedOn["wipnote:feature-coder"]) != 0 {
		t.Errorf("wipnote:feature-coder worked_on: want 0, got %v", workedOn["wipnote:feature-coder"])
	}
}
