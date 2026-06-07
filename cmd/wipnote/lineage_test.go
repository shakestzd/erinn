package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupLineageDB opens an in-memory DB for lineage routing tests.
func setupLineageDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open lineage db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// seedEdge inserts a minimal graph_edges row for lineage walks.
func seedEdge(t *testing.T, db *sql.DB, edgeID, from, fromType, to, toType, rel string) {
	t.Helper()
	if err := dbpkg.InsertEdge(db, edgeID, from, fromType, to, toType, rel, nil); err != nil {
		t.Fatalf("InsertEdge %s: %v", edgeID, err)
	}
}

// TestLineageRouting verifies detectLineageKind dispatches each ID prefix
// to the correct walker kind. This is a pure routing test — no DB walk.
func TestLineageRouting(t *testing.T) {
	cases := []struct {
		arg  string
		want lineageKind
	}{
		{"feat-11223344", kindFeature},
		{"bug-aabbccdd", kindBug},
		{"spk-deadbeef", kindSpike},
		{"plan-3b0d5133", kindPlan},
		{"trk-0677c709", kindTrack},
		{"sess-abc123", kindSession},
		{"abcdef0123456789abcdef0123456789abcdef01", kindCommit}, // 40 hex
		{"abc1234", kindCommit},                                  // short hex still routes to commit
		{"internal/db/schema.go", kindFile},
		{"cmd/wipnote/main.go", kindFile},
	}
	for _, tc := range cases {
		got := detectLineageKind(tc.arg)
		if got != tc.want {
			t.Errorf("detectLineageKind(%q) = %v, want %v", tc.arg, got, tc.want)
		}
	}
}

// TestLineageDepthLimit seeds a 10-deep forward chain and asserts that
// running with --depth 3 does NOT include node-4 or beyond.
func TestLineageDepthLimit(t *testing.T) {
	db := setupLineageDB(t)

	// Seed chain: feat-d0 -> feat-d1 -> ... -> feat-d10 (each "implements" the next)
	for i := 0; i < 10; i++ {
		from := fmt.Sprintf("feat-d%02d", i)
		to := fmt.Sprintf("feat-d%02d", i+1)
		seedEdge(t, db, fmt.Sprintf("e-%d", i), from, "feature", to, "feature", "implements")
	}

	var buf bytes.Buffer
	opts := lineageOpts{depth: 3}
	if err := runLineage(&buf, db, "feat-d00", opts); err != nil {
		t.Fatalf("runLineage: %v", err)
	}

	out := buf.String()
	// depth 3: should reach d01, d02, d03 but NOT d04+
	for _, want := range []string{"feat-d01", "feat-d02", "feat-d03"} {
		if !strings.Contains(out, want) {
			t.Errorf("depth-3 output should contain %q\n%s", want, out)
		}
	}
	for _, bad := range []string{"feat-d04", "feat-d05", "feat-d06", "feat-d07"} {
		if strings.Contains(out, bad) {
			t.Errorf("depth-3 output must NOT contain %q\n%s", bad, out)
		}
	}
}

// TestLineageBranchingChains verifies branching ancestors/descendants render
// as multiple branches in the tree, not just one.
func TestLineageBranchingChains(t *testing.T) {
	db := setupLineageDB(t)

	// Pivot: feat-pivot
	// Two parents: feat-parentA, feat-parentB (both -> pivot via implements)
	// Two children: feat-childA, feat-childB (pivot -> both via implements)
	seedEdge(t, db, "e-pa", "feat-parenta", "feature", "feat-pivot00", "feature", "implements")
	seedEdge(t, db, "e-pb", "feat-parentb", "feature", "feat-pivot00", "feature", "implements")
	seedEdge(t, db, "e-ca", "feat-pivot00", "feature", "feat-childaa", "feature", "implements")
	seedEdge(t, db, "e-cb", "feat-pivot00", "feature", "feat-childbb", "feature", "implements")

	var buf bytes.Buffer
	if err := runLineage(&buf, db, "feat-pivot00", lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"feat-parenta", "feat-parentb", "feat-childaa", "feat-childbb"} {
		if !strings.Contains(out, want) {
			t.Errorf("branching output missing %q\n%s", want, out)
		}
	}
}

// TestLineageIncludesPlannedIn verifies that plan -> planned_in -> feature
// edges are walked. This is the headline plan -> feature demo.
func TestLineageIncludesPlannedIn(t *testing.T) {
	db := setupLineageDB(t)

	// plan-aaaaaaaa --planned_in--> feat-77777777
	seedEdge(t, db, "e-plan", "plan-aaaaaaaa", "plan", "feat-77777777", "feature", "planned_in")

	var buf bytes.Buffer
	if err := runLineage(&buf, db, "feat-77777777", lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "plan-aaaaaaaa") {
		t.Errorf("lineage on feature should surface plan ancestor via planned_in\n%s", out)
	}
}

// TestLineageJSONSchema verifies the JSON output contains the documented
// stable schema: {root, forward, backward}.
func TestLineageJSONSchema(t *testing.T) {
	db := setupLineageDB(t)
	seedEdge(t, db, "e-1", "feat-aaaa0001", "feature", "feat-bbbb0002", "feature", "implements")
	seedEdge(t, db, "e-2", "feat-cccc0003", "feature", "feat-aaaa0001", "feature", "implements")

	var buf bytes.Buffer
	if err := runLineage(&buf, db, "feat-aaaa0001", lineageOpts{depth: 5, jsonOut: true}); err != nil {
		t.Fatalf("runLineage json: %v", err)
	}

	var got lineageJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal lineage json: %v\nraw:\n%s", err, buf.String())
	}
	if got.Root != "feat-aaaa0001" {
		t.Errorf("Root = %q, want feat-aaaa0001", got.Root)
	}
	if len(got.Forward) == 0 {
		t.Errorf("Forward chain should be non-empty (expected feat-bbbb0002)")
	}
	if len(got.Backward) == 0 {
		t.Errorf("Backward chain should be non-empty (expected feat-cccc0003)")
	}
	// Validate node fields are populated.
	if len(got.Forward) > 0 {
		n := got.Forward[0]
		if n.ID == "" || n.EdgeType == "" || n.Depth == 0 {
			t.Errorf("forward node missing fields: %+v", n)
		}
	}
}

// TestLineageAgentTreeForSession verifies that when input is a session ID,
// the agent spawn tree (RenderAgentTree) is appended to the output.
func TestLineageAgentTreeForSession(t *testing.T) {
	db := setupLineageDB(t)

	rootSession := "sess-root-0001"
	childSession := "sess-chld-0002"

	// Seed lineage traces so RenderAgentTree has something to walk.
	rootTrace := &models.LineageTrace{
		TraceID:       "trc-root",
		RootSessionID: rootSession,
		SessionID:     rootSession,
		AgentName:     "orchestrator-x",
		Depth:         0,
		Path:          []string{rootSession},
		FeatureID:     "feat-rrr00001",
		StartedAt:     time.Now().UTC(),
		Status:        "active",
	}
	childTrace := &models.LineageTrace{
		TraceID:       "trc-chld",
		RootSessionID: rootSession,
		SessionID:     childSession,
		AgentName:     "coder-x",
		Depth:         1,
		Path:          []string{rootSession, childSession},
		FeatureID:     "feat-ccc00001",
		StartedAt:     time.Now().UTC(),
		Status:        "active",
	}
	if err := dbpkg.InsertLineageTrace(db, rootTrace); err != nil {
		t.Fatalf("insert root trace: %v", err)
	}
	if err := dbpkg.InsertLineageTrace(db, childTrace); err != nil {
		t.Fatalf("insert child trace: %v", err)
	}

	var buf bytes.Buffer
	if err := runLineage(&buf, db, rootSession, lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage session: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Agent spawn chain") {
		t.Errorf("session lineage should include 'Agent spawn chain' header\n%s", out)
	}
	if !strings.Contains(out, "orchestrator-x") {
		t.Errorf("session lineage should include the rendered agent tree\n%s", out)
	}
}

// TestLineageCommitDispatch verifies that a commit SHA routes through the
// TraceCommit primitive instead of returning an empty graph walk. This is the
// regression for roborev HIGH finding #1 (lineage.go kindCommit).
func TestLineageCommitDispatch(t *testing.T) {
	db := setupLineageDB(t)

	sha := "deadbeef12345678"
	if _, err := db.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		sha, "sess-lineage-cmt", "feat-cmtdispatch", "lineage commit dispatch", time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed git_commits: %v", err)
	}

	var buf bytes.Buffer
	if err := runLineage(&buf, db, sha, lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage commit: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[commit]") {
		t.Errorf("commit lineage output should label kind [commit]\n%s", out)
	}
	if !strings.Contains(out, "sess-lineage-cmt") {
		t.Errorf("commit lineage should surface the attributed session\n%s", out)
	}
}

// TestLineageFileDispatch verifies file paths route through TraceFile instead
// of the graph walker (which would return empty for a file path).
func TestLineageFileDispatch(t *testing.T) {
	db := setupLineageDB(t)

	featureID := "feat-filedisp1"
	filePath := "internal/db/lineage_file_dispatch.go"
	if _, err := db.Exec(
		`INSERT INTO features (id, type, title, status) VALUES (?, ?, ?, ?)`,
		featureID, "feature", "File dispatch test", "in-progress",
	); err != nil {
		t.Fatalf("seed feature: %v", err)
	}
	if err := dbpkg.UpsertFeatureFile(db, &models.FeatureFile{
		ID: "ff-filedisp", FeatureID: featureID, FilePath: filePath, Operation: "edit",
	}); err != nil {
		t.Fatalf("seed feature_file: %v", err)
	}

	var buf bytes.Buffer
	if err := runLineage(&buf, db, filePath, lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage file: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[file]") {
		t.Errorf("file lineage output should label kind [file]\n%s", out)
	}
	if !strings.Contains(out, featureID) {
		t.Errorf("file lineage should surface attributed feature %q\n%s", featureID, out)
	}
}

// TestLineageCommitRejectsUnsupportedFlags guards the contract that --timeline
// and --depth are not honored for commit inputs (since TraceCommit is a flat
// attribution query, not a graph walk).
func TestLineageCommitRejectsUnsupportedFlags(t *testing.T) {
	db := setupLineageDB(t)
	if _, err := db.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		"abcdef0123", "sess-rej", "feat-rej", "rejection test", time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	var buf bytes.Buffer
	err := runLineage(&buf, db, "abcdef0123", lineageOpts{depth: 5, timelineSet: true})
	if err == nil {
		t.Error("expected --timeline on commit input to return an error")
	}

	buf.Reset()
	err = runLineage(&buf, db, "abcdef0123", lineageOpts{depth: 10, depthSet: true})
	if err == nil {
		t.Error("expected --depth on commit input to return an error")
	}
}

// TestLineageSessionJSONIncludesAgentTree covers the regression where session
// --json dropped the agent spawn tree that the text output displayed.
func TestLineageSessionJSONIncludesAgentTree(t *testing.T) {
	db := setupLineageDB(t)
	rootSession := "sess-jsontree"
	childSession := "sess-jsontree-child"
	if err := dbpkg.InsertLineageTrace(db, &models.LineageTrace{
		TraceID: "lt-jt-root", RootSessionID: rootSession, SessionID: rootSession,
		AgentName: "orchestrator-jt", Depth: 0, Path: []string{rootSession},
		FeatureID: "feat-jt", StartedAt: time.Now().UTC(), Status: "active",
	}); err != nil {
		t.Fatalf("seed root trace: %v", err)
	}
	if err := dbpkg.InsertLineageTrace(db, &models.LineageTrace{
		TraceID: "lt-jt-child", RootSessionID: rootSession, SessionID: childSession,
		AgentName: "sub-jt", Depth: 1, Path: []string{rootSession, childSession},
		FeatureID: "feat-jt", StartedAt: time.Now().UTC(), Status: "active",
	}); err != nil {
		t.Fatalf("seed child trace: %v", err)
	}

	var buf bytes.Buffer
	if err := runLineage(&buf, db, rootSession, lineageOpts{depth: 5, jsonOut: true}); err != nil {
		t.Fatalf("runLineage session --json: %v", err)
	}
	var got lineageJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.AgentTree == "" {
		t.Error("session --json should include agent_tree, got empty")
	}
	if !strings.Contains(got.AgentTree, "orchestrator-jt") {
		t.Errorf("agent_tree should include the orchestrator, got:\n%s", got.AgentTree)
	}
}

// TestLineageTimelineEmptyTimestampsLast guards the timeline ordering
// regression: nodes without a timestamp used to sort BEFORE dated nodes
// because "" < any real timestamp string.
func TestLineageTimelineEmptyTimestampsLast(t *testing.T) {
	nodes := []lineageNode{
		{ID: "late", Timestamp: "2026-04-14T10:00:00Z"},
		{ID: "no-ts", Timestamp: ""},
		{ID: "early", Timestamp: "2026-01-01T00:00:00Z"},
		{ID: "no-ts-2", Timestamp: ""},
	}
	sortLineageTimeline(nodes)
	want := []string{"early", "late", "no-ts", "no-ts-2"}
	for i, n := range nodes {
		if n.ID != want[i] {
			t.Errorf("position %d: got %q, want %q (full order: %v)", i, n.ID, want[i], nodes)
		}
	}
}

// TestLineageTreeRendersRealParentage is the regression for the depth-only
// tree bug: with BFS, sibling branches can interleave so a grandchild may
// appear immediately after a cousin at the same indent. Without real parent
// tracking, the grandchild visually attaches to the wrong parent. This test
// seeds a branched walk where the BFS order and depth-based indent would
// collide, then asserts the rendered output keeps each child under its true
// parent.
func TestLineageTreeRendersRealParentage(t *testing.T) {
	db := setupLineageDB(t)

	// Graph:
	//   pivot -> A (edge: implements)
	//   pivot -> C (edge: implements)
	//   A     -> B (edge: implements)   // B's true parent is A
	//   C     -> D (edge: implements)   // D's true parent is C
	//
	// BFS forward from pivot visits: [A, C, B, D].
	// With depth-only rendering, B would print at indent 2 right after C at
	// indent 1 and visually attach to C — the wrong parent.
	seedEdge(t, db, "e1", "feat-pivot", "feature", "feat-aaaa", "feature", "implements")
	seedEdge(t, db, "e2", "feat-pivot", "feature", "feat-cccc", "feature", "implements")
	seedEdge(t, db, "e3", "feat-aaaa", "feature", "feat-bbbb", "feature", "implements")
	seedEdge(t, db, "e4", "feat-cccc", "feature", "feat-dddd", "feature", "implements")

	var buf bytes.Buffer
	if err := runLineage(&buf, db, "feat-pivot", lineageOpts{depth: 5}); err != nil {
		t.Fatalf("runLineage: %v", err)
	}
	out := buf.String()

	// Split into descendant lines: must preserve "A -> B" and "C -> D" locality.
	// Precise check: B must appear after A but BEFORE C (since DFS descends A
	// fully before visiting sibling C). Likewise D appears after C.
	idxA := strings.Index(out, "feat-aaaa")
	idxB := strings.Index(out, "feat-bbbb")
	idxC := strings.Index(out, "feat-cccc")
	idxD := strings.Index(out, "feat-dddd")
	if idxA < 0 || idxB < 0 || idxC < 0 || idxD < 0 {
		t.Fatalf("expected all four descendants in output, got:\n%s", out)
	}
	if !(idxA < idxB && idxB < idxC && idxC < idxD) {
		t.Errorf("descendants out of DFS order (A<B<C<D). A=%d B=%d C=%d D=%d\n%s",
			idxA, idxB, idxC, idxD, out)
	}
}

// TestLineageRegressionTraceUnchanged is a compile-time guarantee that the
// existing trace command surface is untouched. If trace.go's exported helpers
// disappear, this test fails to compile.
func TestLineageRegressionTraceUnchanged(t *testing.T) {
	_ = looksLikeFilePath("foo.go")
	_ = looksLikeWorkItemID("feat-11223344")
}
