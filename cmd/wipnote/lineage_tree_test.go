package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupLineageTestDB opens an in-memory DB for lineage tree tests.
func setupLineageTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// insertTrace is a helper to seed lineage rows concisely.
func insertTrace(t *testing.T, db *sql.DB, traceID, rootSession, session, agentName string, depth int, path []string, featureID string) {
	t.Helper()
	trace := &models.LineageTrace{
		TraceID:       traceID,
		RootSessionID: rootSession,
		SessionID:     session,
		AgentName:     agentName,
		Depth:         depth,
		Path:          path,
		FeatureID:     featureID,
		StartedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := dbpkg.InsertLineageTrace(db, trace); err != nil {
		t.Fatalf("InsertLineageTrace %s: %v", traceID, err)
	}
}

// TestRenderAgentTree_ThreeLevelTree verifies rendering of a 3-level hierarchy:
//
//	root -> child-A -> grandchild-A1
//	     -> child-B
func TestRenderAgentTree_ThreeLevelTree(t *testing.T) {
	db := setupLineageTestDB(t)

	rootSession := "root-sess-0001"
	childASession := "chld-sess-000a"
	grandchildSession := "gran-sess-001a"
	childBSession := "chld-sess-000b"

	// Root: path has only itself — treat as root (len(path) < 2).
	insertTrace(t, db, "trace-root", rootSession, rootSession, "orchestrator", 0, []string{rootSession}, "feat-root")
	// Child A: path = [root, childA].
	insertTrace(t, db, "trace-chA", rootSession, childASession, "coder-agent", 1, []string{rootSession, childASession}, "feat-child-a")
	// Grandchild under child A: path = [root, childA, grandchild].
	insertTrace(t, db, "trace-grA", rootSession, grandchildSession, "test-agent", 2, []string{rootSession, childASession, grandchildSession}, "feat-grand")
	// Child B: path = [root, childB].
	insertTrace(t, db, "trace-chB", rootSession, childBSession, "reviewer-agent", 1, []string{rootSession, childBSession}, "feat-child-b")

	output, err := RenderAgentTree(db, rootSession)
	if err != nil {
		t.Fatalf("RenderAgentTree: %v", err)
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// Collect node lines (lines that contain agent names).
	nodeLines := []string{}
	for _, l := range lines {
		if strings.Contains(l, "orchestrator") || strings.Contains(l, "coder-agent") ||
			strings.Contains(l, "test-agent") || strings.Contains(l, "reviewer-agent") {
			nodeLines = append(nodeLines, l)
		}
	}
	if len(nodeLines) != 4 {
		t.Errorf("expected 4 agent node lines, got %d:\n%s", len(nodeLines), output)
	}

	// Root (depth 0) should have no leading indent.
	if len(nodeLines) > 0 && !strings.HasPrefix(nodeLines[0], "orchestrator") {
		t.Errorf("root node should have no indent, got: %q", nodeLines[0])
	}

	// Depth-1 children should have exactly 2-space indent.
	for _, l := range nodeLines {
		if strings.Contains(l, "coder-agent") || strings.Contains(l, "reviewer-agent") {
			if !strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "    ") {
				t.Errorf("depth-1 node should be indented exactly 2 spaces, got: %q", l)
			}
		}
	}

	// Grandchild (depth 2) should have exactly 4-space indent.
	for _, l := range nodeLines {
		if strings.Contains(l, "test-agent") {
			if !strings.HasPrefix(l, "    ") {
				t.Errorf("depth-2 node should be indented 4 spaces, got: %q", l)
			}
		}
	}

	// Verify depth-first order: root, coder-agent, test-agent, reviewer-agent.
	if len(nodeLines) == 4 {
		order := []string{"orchestrator", "coder-agent", "test-agent", "reviewer-agent"}
		for i, want := range order {
			if !strings.Contains(nodeLines[i], want) {
				t.Errorf("DFS order[%d]: want %q, got %q", i, want, nodeLines[i])
			}
		}
	}

	// Each node line must contain a depth marker (d0, d1, or d2).
	for _, l := range nodeLines {
		hasDepth := strings.Contains(l, "d0") || strings.Contains(l, "d1") || strings.Contains(l, "d2")
		if !hasDepth {
			t.Errorf("node line missing depth marker: %q", l)
		}
	}

	// Short session IDs and feature IDs must appear in output.
	if !strings.Contains(output, "root-ses") {
		t.Errorf("output should contain short session ID for root, got:\n%s", output)
	}
	if !strings.Contains(output, "feat-root") {
		t.Errorf("output should contain feature_id for root, got:\n%s", output)
	}
}

// TestRenderAgentTree_SingleNode verifies a root-only tree with len(path)==1 does not panic.
func TestRenderAgentTree_SingleNode(t *testing.T) {
	db := setupLineageTestDB(t)

	rootSession := "solo-sess-0001"
	insertTrace(t, db, "trace-solo", rootSession, rootSession, "solo-agent", 0, []string{rootSession}, "feat-solo")

	output, err := RenderAgentTree(db, rootSession)
	if err != nil {
		t.Fatalf("RenderAgentTree single node: %v", err)
	}

	if !strings.Contains(output, "solo-agent") {
		t.Errorf("output should contain solo-agent, got:\n%s", output)
	}
}

// TestRenderAgentTree_ShortPath verifies that rows with path=[] and path=["self"]
// are both treated as roots and do not panic.
func TestRenderAgentTree_ShortPath(t *testing.T) {
	db := setupLineageTestDB(t)

	rootSession := "root-short-0001"

	// Row with empty path — must be treated as root.
	insertTrace(t, db, "trace-empty-path", rootSession, rootSession, "empty-path-agent", 0, []string{}, "feat-empty")
	// Row with single-element path — no valid parent derivable; treat as root.
	insertTrace(t, db, "trace-single-path", rootSession, "chld-short-0001", "single-path-agent", 1, []string{"chld-short-0001"}, "feat-single")

	// Must not panic.
	output, err := RenderAgentTree(db, rootSession)
	if err != nil {
		t.Fatalf("RenderAgentTree short path: %v", err)
	}

	if !strings.Contains(output, "empty-path-agent") {
		t.Errorf("output should contain empty-path-agent, got:\n%s", output)
	}
	if !strings.Contains(output, "single-path-agent") {
		t.Errorf("output should contain single-path-agent, got:\n%s", output)
	}
}

// TestRenderAgentTree_MissingParent covers the orphan promotion regression:
// a row whose derived parent session is absent from bySession must be
// promoted to a root instead of silently dropped. Partial lineage data
// should degrade gracefully.
func TestRenderAgentTree_MissingParent(t *testing.T) {
	db := setupLineageTestDB(t)

	rootSession := "root-orphan-0001"
	orphanSession := "chld-orphan-ghost"
	// Seed an orphan whose path claims parent "ghost-parent" — but "ghost-parent"
	// is NOT inserted. Without the fix this row disappears; with the fix it
	// renders as a root.
	insertTrace(t, db, "trace-orphan", rootSession, orphanSession, "orphan-agent", 2,
		[]string{rootSession, "ghost-parent", orphanSession}, "feat-orphan")
	// Also seed the actual root so GetLineageByRoot has something to return.
	insertTrace(t, db, "trace-orphan-root", rootSession, rootSession, "root-agent", 0,
		[]string{rootSession}, "feat-root")

	output, err := RenderAgentTree(db, rootSession)
	if err != nil {
		t.Fatalf("RenderAgentTree missing parent: %v", err)
	}
	if !strings.Contains(output, "orphan-agent") {
		t.Errorf("orphan row should still render (as a root), got:\n%s", output)
	}
	if !strings.Contains(output, "root-agent") {
		t.Errorf("actual root should render, got:\n%s", output)
	}
}
