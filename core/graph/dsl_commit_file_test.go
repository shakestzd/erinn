package graph_test

import (
	"testing"

	corearch "github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/core/graph"
)

// --- isNodeType and normalizeNodeType ---

func TestIsNodeType_Commit(t *testing.T) {
	for _, s := range []string{"commit", "commits"} {
		if !graph.IsNodeType(s) {
			t.Errorf("expected IsNodeType(%q) to be true", s)
		}
	}
}

func TestIsNodeType_File(t *testing.T) {
	for _, s := range []string{"file", "files"} {
		if !graph.IsNodeType(s) {
			t.Errorf("expected IsNodeType(%q) to be true", s)
		}
	}
}

func TestIsNodeType_Session(t *testing.T) {
	for _, s := range []string{"session", "sessions"} {
		if !graph.IsNodeType(s) {
			t.Errorf("expected IsNodeType(%q) to be true", s)
		}
	}
}

func TestNormalizeNodeType_Commit(t *testing.T) {
	if got := graph.NormalizeNodeType("commits"); got != "commit" {
		t.Errorf("expected 'commit', got %q", got)
	}
	if got := graph.NormalizeNodeType("commit"); got != "commit" {
		t.Errorf("expected 'commit', got %q", got)
	}
}

func TestNormalizeNodeType_File(t *testing.T) {
	if got := graph.NormalizeNodeType("files"); got != "file" {
		t.Errorf("expected 'file', got %q", got)
	}
	if got := graph.NormalizeNodeType("file"); got != "file" {
		t.Errorf("expected 'file', got %q", got)
	}
}

func TestNormalizeNodeType_Session(t *testing.T) {
	if got := graph.NormalizeNodeType("sessions"); got != "session" {
		t.Errorf("expected 'session', got %q", got)
	}
	if got := graph.NormalizeNodeType("session"); got != "session" {
		t.Errorf("expected 'session', got %q", got)
	}
}

// --- ExecuteDSL with commit type ---

func TestExecuteDSL_CommitType(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, message) VALUES (?, ?, ?)`,
		"abc123", "sess-1", "fix: some bug",
	)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, message) VALUES (?, ?, ?)`,
		"def456", "sess-1", "feat: new feature",
	)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "commits")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 commits, got %d", len(results))
	}
}

func TestExecuteDSL_CommitTypeSingular(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, message) VALUES (?, ?, ?)`,
		"abc123", "sess-1", "fix: some bug",
	)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "commit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 commit, got %d", len(results))
	}
}

func TestExecuteDSL_FileType(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, operation) VALUES (?, ?, ?, ?)`,
		"ff-1", "feat-a", "internal/graph/dsl.go", "modified",
	)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, operation) VALUES (?, ?, ?, ?)`,
		"ff-2", "feat-a", "internal/graph/querybuilder.go", "modified",
	)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 files, got %d", len(results))
	}
}

func TestExecuteDSL_SessionType(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-1", "claude", "active",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-2", "claude", "completed",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "sessions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(results))
	}
}

func TestExecuteDSL_SessionWithFilter(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-1", "claude", "active",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-2", "claude", "completed",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "sessions[status=active]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "sess-1" {
		t.Errorf("expected [sess-1], got %v", results)
	}
}

func TestExecuteDSL_FeatureToCommitChain(t *testing.T) {
	database := openTestDB(t)
	seedFeature(t, database, "feat-a", "Feature A", "done")
	_, err := database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, message) VALUES (?, ?, ?)`,
		"abc123", "sess-1", "feat: implement A",
	)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	seedEdge(t, database, "feat-a", "feature", "abc123", "commit", "committed_for")

	results, err := graph.ExecuteDSL(database, nil, "features -> committed_for -> commits")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "abc123" {
		t.Errorf("expected [abc123], got %v", results)
	}
}

// --- resolveNodes includes commit/file/session metadata ---

func TestResolveNodes_CommitMetadata(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, message) VALUES (?, ?, ?)`,
		"abc123", "sess-1", "fix: resolve the issue with long message padding",
	)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "commits")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ID != "abc123" {
		t.Errorf("expected ID abc123, got %q", r.ID)
	}
	if r.Type != "commit" {
		t.Errorf("expected type 'commit', got %q", r.Type)
	}
	if r.Title == "" {
		t.Errorf("expected non-empty title for commit")
	}
	if r.Status != "done" {
		t.Errorf("expected status 'done' for commit, got %q", r.Status)
	}
}

func TestResolveNodes_FileMetadata(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO feature_files (id, feature_id, file_path, operation) VALUES (?, ?, ?, ?)`,
		"ff-1", "feat-a", "internal/graph/dsl.go", "modified",
	)
	if err != nil {
		t.Fatalf("seed file: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ID != "internal/graph/dsl.go" {
		t.Errorf("expected ID internal/graph/dsl.go, got %q", r.ID)
	}
	if r.Type != "file" {
		t.Errorf("expected type 'file', got %q", r.Type)
	}
	if r.Title != "internal/graph/dsl.go" {
		t.Errorf("expected title to be file path, got %q", r.Title)
	}
}

func TestResolveNodes_SessionMetadata(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-1", "claude", "active",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "sessions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.ID != "sess-1" {
		t.Errorf("expected ID sess-1, got %q", r.ID)
	}
	if r.Type != "session" {
		t.Errorf("expected type 'session', got %q", r.Type)
	}
	if r.Status != "active" {
		t.Errorf("expected status 'active', got %q", r.Status)
	}
}

func TestIsNodeType_Agent(t *testing.T) {
	for _, s := range []string{"agent", "agents"} {
		if !graph.IsNodeType(s) {
			t.Errorf("expected IsNodeType(%q) to be true", s)
		}
	}
}

func TestNormalizeNodeType_Agent(t *testing.T) {
	if got := graph.NormalizeNodeType("agents"); got != "agent" {
		t.Errorf("expected 'agent', got %q", got)
	}
	if got := graph.NormalizeNodeType("agent"); got != "agent" {
		t.Errorf("expected 'agent', got %q", got)
	}
}

func TestIsNodeType_Arch(t *testing.T) {
	for _, s := range []string{"arch", "architecture", "architectures"} {
		if !graph.IsNodeType(s) {
			t.Errorf("expected IsNodeType(%q) to be true", s)
		}
	}
}

func TestNormalizeNodeType_Arch(t *testing.T) {
	for _, s := range []string{"arch", "architecture", "architectures"} {
		if got := graph.NormalizeNodeType(s); got != "arch" {
			t.Errorf("NormalizeNodeType(%q) = %q, want arch", s, got)
		}
	}
}

// Regression: ExecuteDSL(..., "agents") must return actual agent names,
// not fall through to the features table and silently return nothing.
func TestExecuteDSL_AgentType(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO agent_lineage_trace (trace_id, session_id, root_session_id, agent_name) VALUES (?, ?, ?, ?)`,
		"tr-1", "sess-1", "sess-1", "wipnote:feature-coder",
	)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, ?)`,
		"sess-2", "wipnote:architect-coder", "active",
	)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "agents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 agent results, got %d: %+v", len(results), results)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
		if r.Type != "agent" {
			t.Errorf("expected type 'agent', got %q for id=%q", r.Type, r.ID)
		}
	}
	if !seen["wipnote:feature-coder"] || !seen["wipnote:architect-coder"] {
		t.Errorf("missing expected agent names in results: %+v", results)
	}
}

func TestExecuteDSL_AgentTypeSingular(t *testing.T) {
	database := openTestDB(t)
	_, err := database.Exec(
		`INSERT INTO agent_lineage_trace (trace_id, session_id, root_session_id, agent_name) VALUES (?, ?, ?, ?)`,
		"tr-1", "sess-1", "sess-1", "wipnote:researcher",
	)
	if err != nil {
		t.Fatalf("seed lineage: %v", err)
	}

	results, err := graph.ExecuteDSL(database, nil, "agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "wipnote:researcher" {
		t.Errorf("expected ID 'wipnote:researcher', got %q", results[0].ID)
	}
	if results[0].Type != "agent" {
		t.Errorf("expected type 'agent', got %q", results[0].Type)
	}
}

// fakeArchSource serves architecture cards from memory. It stands in for
// core/arch.Store, which reads the canonical .wipnote/architecture.html
// ledger. Arch cards are deliberately not mirrored into SQLite, so seeding a
// table is no longer a way to make them visible to a query (spk-e6e82b5a).
type fakeArchSource []*corearch.Card

func (f fakeArchSource) List(bool) ([]*corearch.Card, error) { return f, nil }

func TestExecuteDSL_ArchTypeAndAliases(t *testing.T) {
	database := openTestDB(t)
	archSrc := fakeArchSource{{
		Name:      "auth-learning",
		Kind:      corearch.KindDecision,
		CreatedBy: "agent",
		Body:      "Prefer explicit auth boundaries.",
	}}

	for _, query := range []string{"arch", "architecture", "architectures"} {
		results, err := graph.ExecuteDSL(database, archSrc, query)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", query, err)
		}
		if len(results) != 1 {
			t.Fatalf("%s: expected 1 result, got %d", query, len(results))
		}
		if results[0].ID != corearch.ArchNodeID("auth-learning") {
			t.Fatalf("%s: expected arch node id, got %q", query, results[0].ID)
		}
		if results[0].Type != "arch" {
			t.Fatalf("%s: expected arch type, got %q", query, results[0].Type)
		}
		if results[0].Title != "auth-learning" {
			t.Fatalf("%s: expected slug title, got %q", query, results[0].Title)
		}
	}
}

func TestExecuteDSL_ArchFilters(t *testing.T) {
	database := openTestDB(t)
	archSrc := fakeArchSource{
		{
			Name:      "auth-learning",
			Kind:      corearch.KindDecision,
			CreatedBy: "agent",
			Body:      "Prefer explicit auth boundaries.",
		},
		{
			Name:         "old-learning",
			Kind:         corearch.KindHazard,
			CreatedBy:    "reviewer",
			SupersededBy: "new-learning",
			Body:         "This guidance is retired.",
		},
	}

	results, err := graph.ExecuteDSL(database, archSrc, "arch[kind=decision]")
	if err != nil {
		t.Fatalf("arch[kind=decision]: %v", err)
	}
	if len(results) != 1 || results[0].ID != corearch.ArchNodeID("auth-learning") {
		t.Fatalf("arch[kind=decision] = %+v, want auth-learning", results)
	}

	results, err = graph.ExecuteDSL(database, archSrc, "arch[status=retired]")
	if err != nil {
		t.Fatalf("arch[status=retired]: %v", err)
	}
	if len(results) != 1 || results[0].ID != corearch.ArchNodeID("old-learning") {
		t.Fatalf("arch[status=retired] = %+v, want old-learning", results)
	}

	results, err = graph.ExecuteDSL(database, archSrc, "arch[created_by=agent]")
	if err != nil {
		t.Fatalf("arch[created_by=agent]: %v", err)
	}
	if len(results) != 1 || results[0].ID != corearch.ArchNodeID("auth-learning") {
		t.Fatalf("arch[created_by=agent] = %+v, want auth-learning", results)
	}
}

// TestExecuteDSL_ArchIgnoresSQLiteMirror pins the direction of the migration:
// a row sitting in the retired arch_cards table must not make a card visible.
// Only the canonical store can.
func TestExecuteDSL_ArchIgnoresSQLiteMirror(t *testing.T) {
	database := openTestDB(t)
	if _, err := database.Exec(
		`INSERT INTO arch_cards (slug, kind, created_by, retired, body) VALUES (?, ?, ?, ?, ?)`,
		"stale-mirror-card", "decision", "agent", 0, "Only in SQLite.",
	); err != nil {
		t.Fatalf("seed arch_cards row: %v", err)
	}

	results, err := graph.ExecuteDSL(database, fakeArchSource{}, "arch")
	if err != nil {
		t.Fatalf("arch: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("arch returned %+v — the DSL is still reading the arch_cards "+
			"mirror instead of the canonical card store", results)
	}
}

// TestExecuteDSL_RejectsWrongTypeField is a regression test for the
// per-type filter column whitelist. Before the fix, fields were
// validated against a single global whitelist, so features[message=X]
// (message belongs to git_commits, not features) would pass validation
// and then fail at SQL execution with an opaque "no such column"
// error. Now the DSL rejects it at parse time with a meaningful
// message. See roborev job 109 finding #3.
func TestExecuteDSL_RejectsWrongTypeField(t *testing.T) {
	database := openTestDB(t)

	// features[message=X] — message isn't a features column.
	_, err := graph.ExecuteDSL(database, nil, "features[message=hello]")
	if err == nil {
		t.Fatal("expected error for features[message=X], got nil")
	}
	// sessions[type=Y] — type isn't a sessions column.
	_, err = graph.ExecuteDSL(database, nil, "sessions[type=foo]")
	if err == nil {
		t.Fatal("expected error for sessions[type=Y], got nil")
	}
	// commits[status=Z] — status isn't a commits column.
	_, err = graph.ExecuteDSL(database, nil, "commits[status=done]")
	if err == nil {
		t.Fatal("expected error for commits[status=Z], got nil")
	}
}
