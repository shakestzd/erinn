package db_test

import (
	"database/sql"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
)

// openTestDB creates an in-memory SQLite database with the full wipnote schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestInsertEdge_Basic(t *testing.T) {
	database := openTestDB(t)

	err := db.InsertEdge(
		database,
		"feat-a-blocks-feat-b", "feat-a", "feature", "feat-b", "feature",
		"blocks", nil,
	)
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE edge_id = ?`,
		"feat-a-blocks-feat-b").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

func TestInsertEdge_Idempotent(t *testing.T) {
	database := openTestDB(t)

	args := []any{
		"feat-a-blocked_by-feat-b", "feat-a", "feature", "feat-b", "feature",
		"blocked_by", map[string]string{"reason": "waiting"},
	}
	insert := func() {
		err := db.InsertEdge(
			database,
			args[0].(string), args[1].(string), args[2].(string),
			args[3].(string), args[4].(string), args[5].(string),
			args[6].(map[string]string),
		)
		if err != nil {
			t.Fatalf("InsertEdge: %v", err)
		}
	}

	insert()
	insert() // second call must not error (INSERT OR REPLACE)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 row after two identical inserts, got %d", count)
	}
}

func TestInsertEdge_WithMetadata(t *testing.T) {
	database := openTestDB(t)

	metadata := map[string]string{"since": "2026-01-01", "source": "test"}
	err := db.InsertEdge(
		database,
		"spk-x-part_of-trk-y", "spk-x", "spike", "trk-y", "track",
		"part_of", metadata,
	)
	if err != nil {
		t.Fatalf("InsertEdge with metadata: %v", err)
	}

	var metaJSON string
	database.QueryRow(`SELECT metadata FROM graph_edges WHERE edge_id = ?`,
		"spk-x-part_of-trk-y").Scan(&metaJSON)
	if metaJSON == "" {
		t.Error("expected metadata JSON to be stored, got empty string")
	}
}

func TestDeleteEdge(t *testing.T) {
	database := openTestDB(t)

	_ = db.InsertEdge(
		database,
		"bug-a-caused_by-feat-b", "bug-a", "bug", "feat-b", "feature",
		"caused_by", nil,
	)

	err := db.DeleteEdge(database, "bug-a", "feat-b", "caused_by")
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after delete, got %d", count)
	}
}

func TestInsertEdge_SessionToWorkItem(t *testing.T) {
	database := openTestDB(t)

	// Bidirectional session-to-work-item edges.
	err := db.InsertEdge(
		database,
		"edge-feat-abc-sess-xyz-implemented_in",
		"feat-abc", "feature", "sess-xyz", "session",
		"implemented_in", nil,
	)
	if err != nil {
		t.Fatalf("InsertEdge (implemented_in): %v", err)
	}

	err = db.InsertEdge(
		database,
		"edge-sess-xyz-feat-abc-implements",
		"sess-xyz", "session", "feat-abc", "feature",
		"implements", nil,
	)
	if err != nil {
		t.Fatalf("InsertEdge (implements): %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM graph_edges
		WHERE relationship_type IN ('implemented_in','implements')`).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 session edges, got %d", count)
	}

	// Verify reverse lookup: session → features.
	var fromNode string
	database.QueryRow(`SELECT from_node_id FROM graph_edges
		WHERE relationship_type = 'implements' AND to_node_id = 'feat-abc'`).Scan(&fromNode)
	if fromNode != "sess-xyz" {
		t.Errorf("reverse lookup: got %q, want sess-xyz", fromNode)
	}
}

func TestDeleteEdge_NonExistent(t *testing.T) {
	database := openTestDB(t)

	// Deleting a row that does not exist must not return an error.
	err := db.DeleteEdge(database, "feat-x", "feat-y", "blocks")
	if err != nil {
		t.Errorf("DeleteEdge on non-existent row should not error: %v", err)
	}
}
