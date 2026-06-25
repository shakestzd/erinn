package main

import (
	"database/sql"
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// openGraphTestDB opens an in-memory SQLite database with full schema applied.
func openGraphTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Shared-cache in-memory DSN (NOT plain ":memory:"): the graph helpers issue a
	// nested query while an outer *sql.Rows is still open, so database/sql may open
	// a second connection. With plain ":memory:" each connection gets its own private
	// empty DB, dropping nodes/edges; "file::memory:?cache=shared" makes every
	// connection share ONE in-memory DB. db.Open still routes it to the in-memory
	// path because the DSN contains the ":memory:" substring. (roborev #599)
	//
	// SERIAL-ONLY: this DSN names a PROCESS-GLOBAL shared in-memory DB. It is safe
	// here only because every caller runs serially (no t.Parallel) and registers a
	// Close that tears the DB down before the next test opens it. Two tests holding
	// this exact name open at the same time would share ONE DB and cross-contaminate
	// — and openTreeTestDB uses the IDENTICAL name, so the collision spans files.
	// Before slice-3 (feat-6ce99108) adds t.Parallel, give each test a UNIQUE
	// shared-cache name, e.g. fmt.Sprintf("file:graphtest_%s?mode=memory&cache=shared", t.Name()).
	db, err := dbpkg.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestLoadGraphNodes_CommitNodesReturned verifies that loadGraphNodes returns
// nodes with type="commit" when git_commits has data.
// TestLoadGraphNodes_CommitNodesOmitted verifies that git_commits rows do
// NOT surface as graph nodes. Commits are sub-attributes of the session or
// feature that produced them (visible via the provenance panel and the
// /api/graph/commits endpoint), not standalone nodes. Design decision:
// graph clutter reduction.
func TestLoadGraphNodes_CommitNodesOmitted(t *testing.T) {
	db := openGraphTestDB(t)
	_, err := db.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES ('abc123def456', 'sess-001', 'feat-abc', 'Fix a bug', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert commit: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}
	for _, n := range nodes {
		if n.Type == "commit" {
			t.Errorf("expected no commit nodes, got %s", n.ID)
		}
	}
}

// TestLoadGraphNodes_FileNodesReturned verifies that loadGraphNodes returns
// nodes with type="file" when feature_files has data.
func TestLoadGraphNodes_FileNodesReturned(t *testing.T) {
	db := openGraphTestDB(t)

	// Insert a feature_file row (id is primary key, use file_path as id).
	_, err := db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, operation)
		VALUES ('ff-001', 'internal/graph/dsl.go', 'feat-xyz', 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	var fileNodes []graphNode
	for _, n := range nodes {
		if n.Type == "file" {
			fileNodes = append(fileNodes, n)
		}
	}

	if len(fileNodes) == 0 {
		t.Fatal("expected at least one file node, got none")
	}

	found := false
	for _, n := range fileNodes {
		if n.ID == "internal/graph/dsl.go" {
			found = true
			if n.Title != "dsl.go" {
				t.Errorf("file node title: got %q, want %q", n.Title, "dsl.go")
			}
			if n.Status != "" {
				t.Errorf("file node status: got %q, want empty", n.Status)
			}
		}
	}
	if !found {
		t.Errorf("file node with path internal/graph/dsl.go not found in nodes")
	}
}

// TestLoadGraphNodes_CommitDeduplication verifies that the same commit hash
// inserted with two different session_ids produces only one commit node.
// The old TestLoadGraphNodes_CommitDeduplication tested dedup behavior
// for commit nodes. Since commit nodes are no longer emitted at all
// (see TestLoadGraphNodes_CommitNodesOmitted), dedup is irrelevant.

// TestLoadGraphNodes_FileDeduplication verifies that the same file_path
// inserted for different features produces only one file node.
func TestLoadGraphNodes_FileDeduplication(t *testing.T) {
	db := openGraphTestDB(t)

	// Insert two feature_files rows with the same file_path (different feature_ids).
	_, err := db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, operation)
		VALUES ('ff-a1', 'cmd/main.go', 'feat-a', 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file 1: %v", err)
	}
	_, err = db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, operation)
		VALUES ('ff-b1', 'cmd/main.go', 'feat-b', 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file 2: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	count := 0
	for _, n := range nodes {
		if n.Type == "file" && n.ID == "cmd/main.go" {
			count++
		}
	}

	if count != 1 {
		t.Errorf("expected exactly 1 file node for deduplicated path, got %d", count)
	}
}

func TestLoadGraphNodes_ArchNodesReturned(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO arch_cards (slug, kind, created_by, retired, body)
		VALUES ('auth-learning', 'decision', 'agent', 0, 'Prefer explicit auth boundaries.')`)
	if err != nil {
		t.Fatalf("insert arch card: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	for _, n := range nodes {
		if n.ID != "arch:auth-learning" {
			continue
		}
		if n.Type != "arch" {
			t.Fatalf("arch node type = %q, want arch", n.Type)
		}
		if n.Title != "auth-learning" {
			t.Fatalf("arch node title = %q, want auth-learning", n.Title)
		}
		if n.Status != "active" {
			t.Fatalf("arch node status = %q, want active", n.Status)
		}
		if n.Kind != "decision" {
			t.Fatalf("arch node kind = %q, want decision", n.Kind)
		}
		return
	}

	t.Fatal("expected arch:auth-learning node in graph nodes")
}

func TestLoadGraphNodes_SupersededArchNodeIsRetired(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO arch_cards (slug, kind, created_by, superseded_by, retired, body)
		VALUES ('old-learning', 'decision', 'agent', 'new-learning', 0, 'Older guidance.')`)
	if err != nil {
		t.Fatalf("insert arch card: %v", err)
	}

	nodes, _, err := loadGraphNodes(db)
	if err != nil {
		t.Fatalf("loadGraphNodes: %v", err)
	}

	for _, n := range nodes {
		if n.ID == "arch:old-learning" {
			if n.Status != "retired" {
				t.Fatalf("superseded arch status = %q, want retired", n.Status)
			}
			return
		}
	}
	t.Fatal("expected arch:old-learning node in graph nodes")
}

// TestLoadCommitEdges_CommittedFor verifies that commit->feature edges
// (committed_for) are returned for commits with a feature_id.
func TestLoadCommitEdges_CommittedFor(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES ('hash-001', 'sess-001', 'feat-target', 'Some commit', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert commit: %v", err)
	}

	edges := loadCommitEdges(db)

	found := false
	for _, e := range edges {
		if e.Source == "hash-001" && e.Target == "feat-target" && e.Type == "committed_for" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected committed_for edge from hash-001 to feat-target; got: %v", edges)
	}
}

// TestLoadCommitEdges_MultipleSessionsAndFeatures verifies that when a single
// commit_hash appears with multiple session_ids (which is legitimate given
// git_commits' composite PK) all edges are returned — not silently dropped
// by GROUP BY. Regression for roborev finding on loadCommitEdges.
func TestLoadCommitEdges_MultipleSessionsAndFeatures(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-a', 'feature', 'A', 'done')`)
	if err != nil {
		t.Fatalf("seed feature A: %v", err)
	}
	_, err = db.Exec(`INSERT INTO features (id, type, title, status) VALUES ('feat-b', 'feature', 'B', 'done')`)
	if err != nil {
		t.Fatalf("seed feature B: %v", err)
	}

	// Same commit hash recorded under two different sessions AND two
	// different feature attributions (plausible when the same commit touches
	// work across a subagent boundary or is ingested twice).
	_, err = db.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES ('hash-dup', 'sess-A', 'feat-a', 'm', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert commit 1: %v", err)
	}
	_, err = db.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES ('hash-dup', 'sess-B', 'feat-b', 'm', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert commit 2: %v", err)
	}

	edges := loadCommitEdges(db)

	// Expect BOTH committed_for edges and BOTH produced_by edges — 4 total.
	seen := map[string]bool{}
	for _, e := range edges {
		seen[e.Source+"|"+e.Target+"|"+e.Type] = true
	}
	expect := []string{
		"hash-dup|feat-a|committed_for",
		"hash-dup|feat-b|committed_for",
		"hash-dup|sess-A|produced_by",
		"hash-dup|sess-B|produced_by",
	}
	for _, k := range expect {
		if !seen[k] {
			t.Errorf("missing edge %q; got edges: %+v", k, edges)
		}
	}
}

// TestLoadCommitEdges_ProducedBy verifies that commit->session edges
// (produced_by) are returned for commits with a session_id.
func TestLoadCommitEdges_ProducedBy(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp)
		VALUES ('hash-002', 'sess-xyz', '', 'Another commit', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert commit: %v", err)
	}

	edges := loadCommitEdges(db)

	found := false
	for _, e := range edges {
		if e.Source == "hash-002" && e.Target == "sess-xyz" && e.Type == "produced_by" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected produced_by edge from hash-002 to sess-xyz; got: %v", edges)
	}
}

// TestLoadFileEdges_ProducedIn verifies that file->session edges (produced_in)
// are returned for feature_files with a non-null session_id.
func TestLoadFileEdges_ProducedIn(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, session_id, operation)
		VALUES ('ff-p1', 'pkg/foo.go', 'feat-1', 'sess-aaa', 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file: %v", err)
	}

	edges := loadFileEdges(db)

	found := false
	for _, e := range edges {
		if e.Source == "pkg/foo.go" && e.Target == "sess-aaa" && e.Type == "produced_in" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected produced_in edge from pkg/foo.go to sess-aaa; got: %v", edges)
	}
}

// TestLoadFileEdges_TouchedBy verifies that file->feature edges (touched_by)
// are returned for feature_files with a feature_id.
func TestLoadFileEdges_TouchedBy(t *testing.T) {
	db := openGraphTestDB(t)

	_, err := db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, operation)
		VALUES ('ff-t1', 'pkg/bar.go', 'feat-2', 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file: %v", err)
	}

	edges := loadFileEdges(db)

	found := false
	for _, e := range edges {
		if e.Source == "pkg/bar.go" && e.Target == "feat-2" && e.Type == "touched_by" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected touched_by edge from pkg/bar.go to feat-2; got: %v", edges)
	}
}

// TestLoadFileEdges_NullSessionIDNoEdge verifies that a NULL session_id in
// feature_files does NOT produce a produced_in edge.
func TestLoadFileEdges_NullSessionIDNoEdge(t *testing.T) {
	db := openGraphTestDB(t)

	// Insert with explicit NULL session_id.
	_, err := db.Exec(`INSERT INTO feature_files (id, file_path, feature_id, session_id, operation)
		VALUES ('ff-n1', 'pkg/baz.go', 'feat-3', NULL, 'commit')`)
	if err != nil {
		t.Fatalf("insert feature_file: %v", err)
	}

	edges := loadFileEdges(db)

	for _, e := range edges {
		if e.Source == "pkg/baz.go" && e.Type == "produced_in" {
			t.Errorf("unexpected produced_in edge for NULL session_id: %+v", e)
		}
	}
}
