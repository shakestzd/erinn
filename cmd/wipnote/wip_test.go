//go:build sqlitelegacy

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

func TestWipResetWithoutForceError(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "In-progress Feature", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Try to reset without --force
	err := runWipReset(false)
	if err == nil {
		t.Fatal("expected error when calling runWipReset without --force, got nil")
	}

	// Check that error message contains count and --force hint
	errMsg := err.Error()
	if !stringContainsSubstring(errMsg, "--force") {
		t.Errorf("error message should mention --force: %q", errMsg)
	}
	if !stringContainsSubstring(errMsg, "1") {
		t.Errorf("error message should contain item count (1): %q", errMsg)
	}
}

func TestWipResetWithoutForceErrorMultipleItems(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "Feature 1", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature 1: %v", err)
	}
	if err := testCreate("feature", "Feature 2", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature 2: %v", err)
	}
	if err := testCreate("bug", "Bug 1", trackID, "high", true, false); err != nil {
		t.Fatalf("create bug 1: %v", err)
	}

	// Try to reset without --force
	err := runWipReset(false)
	if err == nil {
		t.Fatal("expected error when calling runWipReset without --force, got nil")
	}

	// Check that error message contains count (3) and --force hint
	errMsg := err.Error()
	if !stringContainsSubstring(errMsg, "--force") {
		t.Errorf("error message should mention --force: %q", errMsg)
	}
	if !stringContainsSubstring(errMsg, "3") {
		t.Errorf("error message should contain item count (3): %q", errMsg)
	}
}

func TestWipResetWithForceSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("drives full work-item lifecycle")
	}

	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projectDirFlag = tmpDir
	defer func() { projectDirFlag = "" }()

	trackID := testSetupTrack(t, hgDir)

	if err := testCreate("feature", "In-progress Feature", trackID, "high", true, false); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Reset with --force should succeed
	err := runWipReset(true)
	if err != nil {
		t.Fatalf("expected success with --force, got error: %v", err)
	}
}

// stringContainsSubstring is a helper to check if a string contains a substring
func stringContainsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- helpers for the new WIP-limit redesign tests ---

// makeInProgressNode writes an in-progress node HTML with an optional
// implemented_in edge pointing to ownerSession (empty = no edge).
func makeInProgressNode(t *testing.T, dir, nodeType, id, title, ownerSession string) {
	t.Helper()
	n := &models.Node{
		ID:     id,
		Type:   nodeType,
		Title:  title,
		Status: models.StatusInProgress,
		Edges:  map[string][]models.Edge{},
	}
	if ownerSession != "" {
		n.Edges[string(models.RelImplementedIn)] = []models.Edge{
			{
				TargetID:     ownerSession,
				Relationship: models.RelImplementedIn,
				Title:        "session " + ownerSession,
				Since:        time.Now().UTC(),
			},
		}
	}
	subDir := filepath.Join(dir, nodeType+"s")
	if _, err := workitem.WriteNodeHTML(subDir, n); err != nil {
		t.Fatalf("WriteNodeHTML %s: %v", id, err)
	}
}

// captureWipShow runs runWipShow and returns captured stdout.
func captureWipShow(t *testing.T) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := runWipShow()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if runErr != nil {
		t.Fatalf("runWipShow: %v", runErr)
	}
	return buf.String()
}

// setupWipDir creates a minimal .wipnote directory and sets projectDirFlag.
// Returns the hgDir path.
func setupWipDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	hgDir := filepath.Join(tmpDir, ".wipnote")
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(hgDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = "" })
	return hgDir
}

// TestWipGroupBySession verifies that items are grouped by owner session and
// that items with no implemented_in edge land in the "unknown" bucket.
func TestWipGroupBySession(t *testing.T) {
	hgDir := setupWipDir(t)

	// Two items owned by sess-A, one by sess-B, one with no session.
	makeInProgressNode(t, hgDir, "feature", "feat-group001", "Feature A1", "sess-A")
	makeInProgressNode(t, hgDir, "feature", "feat-group002", "Feature A2", "sess-A")
	makeInProgressNode(t, hgDir, "bug", "bug-group001", "Bug B1", "sess-B")
	makeInProgressNode(t, hgDir, "spike", "spk-group001", "Spike Unknown", "") // no session

	items, err := scanInProgress(hgDir)
	if err != nil {
		t.Fatalf("scanInProgress: %v", err)
	}

	bySession := wipGroupBySession(items)

	if len(bySession["sess-A"]) != 2 {
		t.Errorf("sess-A: want 2 items, got %d", len(bySession["sess-A"]))
	}
	if len(bySession["sess-B"]) != 1 {
		t.Errorf("sess-B: want 1 item, got %d", len(bySession["sess-B"]))
	}
	if len(bySession["unknown"]) != 1 {
		t.Errorf("unknown: want 1 item, got %d; items without session must land in 'unknown'", len(bySession["unknown"]))
	}
	// Verify the unknown bucket holds the node with no edge (not a named session).
	if len(bySession["unknown"]) > 0 && bySession["unknown"][0].ID != "spk-group001" {
		t.Errorf("unknown bucket should hold spk-group001, got %s", bySession["unknown"][0].ID)
	}
}

// TestWipSoftLimitTriggersAtThreshold verifies [SOFT LIMIT] appears in the
// session summary table at exactly wipPerSessionSoftLimit items, but NOT below.
func TestWipSoftLimitTriggersAtThreshold(t *testing.T) {
	hgDir := setupWipDir(t)

	// Create wipPerSessionSoftLimit-1 items for sess-low — should NOT trigger.
	for i := 0; i < wipPerSessionSoftLimit-1; i++ {
		id := "feat-low" + string(rune('a'+i))
		makeInProgressNode(t, hgDir, "feature", id, "Low item "+string(rune('a'+i)), "sess-low")
	}

	out := captureWipShow(t)
	if strings.Contains(out, "SOFT LIMIT") {
		t.Errorf("SOFT LIMIT must NOT appear with %d items (threshold is %d); output:\n%s",
			wipPerSessionSoftLimit-1, wipPerSessionSoftLimit, out)
	}

	// Add one more item to reach exactly wipPerSessionSoftLimit.
	makeInProgressNode(t, hgDir, "feature", "feat-lowZ", "Low item Z", "sess-low")

	out = captureWipShow(t)
	if !strings.Contains(out, "SOFT LIMIT") {
		t.Errorf("[SOFT LIMIT] must appear at exactly %d items for a session; output:\n%s",
			wipPerSessionSoftLimit, out)
	}
}

// TestWipAdvisoryTriggersAtGlobalThreshold verifies [ADVISORY] appears in the
// global header at exactly wipGlobalAdvisoryLimit items, but NOT below.
func TestWipAdvisoryTriggersAtGlobalThreshold(t *testing.T) {
	hgDir := setupWipDir(t)

	// Create wipGlobalAdvisoryLimit-1 items across different sessions so no
	// session hits SOFT LIMIT independently.
	for i := 0; i < wipGlobalAdvisoryLimit-1; i++ {
		sess := "sess-g" + string(rune('a'+i))
		id := "feat-g" + string(rune('a'+i)) + "00"
		makeInProgressNode(t, hgDir, "feature", id, "Global item "+string(rune('a'+i)), sess)
	}

	out := captureWipShow(t)
	if strings.Contains(out, "ADVISORY") {
		t.Errorf("ADVISORY must NOT appear with %d items (threshold is %d); output:\n%s",
			wipGlobalAdvisoryLimit-1, wipGlobalAdvisoryLimit, out)
	}

	// Add one more to hit exactly wipGlobalAdvisoryLimit.
	makeInProgressNode(t, hgDir, "feature", "feat-gZ00", "Global item Z", "sess-gZ")

	out = captureWipShow(t)
	if !strings.Contains(out, "ADVISORY") {
		t.Errorf("[ADVISORY] must appear at exactly %d total items; output:\n%s",
			wipGlobalAdvisoryLimit, out)
	}
}

// TestWipSessionDeadFlag verifies that an owner session not present in the
// live session list is flagged SESSION DEAD?, and a live session is NOT flagged.
func TestWipSessionDeadFlag(t *testing.T) {
	hgDir := setupWipDir(t)

	// Item owned by a session that is NOT in the DB (no DB at all in this test).
	makeInProgressNode(t, hgDir, "feature", "feat-dead001", "Dead session item", "sess-gone")

	// Point DB at a nonexistent path so openReadOnlyDB gracefully returns empty live set.
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(t.TempDir(), "nonexistent", "wipnote.db"))

	out := captureWipShow(t)
	// With no DB, no session is "live" → sess-gone must be marked SESSION DEAD?
	if !strings.Contains(out, "SESSION DEAD?") {
		t.Errorf("SESSION DEAD? must appear for session not in live list; output:\n%s", out)
	}
}

// TestWipSessionLiveNotFlagged verifies that a session present in the DB is
// NOT flagged SESSION DEAD?. This test creates a real DB with the session row.
func TestWipSessionLiveNotFlagged(t *testing.T) {
	hgDir := setupWipDir(t)

	t.Setenv("WIPNOTE_SESSION_ID", "live-sess-001")
	makeInProgressNode(t, hgDir, "feature", "feat-live001", "Live session item", "live-sess-001")

	// Start a feature via the normal path so the session is recorded in the DB.
	// We rely on wiSetStatusWithAgent to write the session row via p.DB (if available).
	// Since the DB is ephemeral here, we instead open the project and insert directly.
	p, err := workitem.Open(hgDir, "claude-code")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	defer p.Close()

	if p.DB != nil {
		// Insert the session row so ListSessions returns it.
		_, err := p.DB.Exec(
			`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, created_at, status) VALUES (?, ?, ?, ?)`,
			"live-sess-001", "test-agent", time.Now().UTC().Format("2006-01-02T15:04:05Z"), "active",
		)
		if err != nil {
			t.Fatalf("insert session: %v", err)
		}
	} else {
		t.Skip("no DB available — cannot test live session liveness check")
	}

	out := captureWipShow(t)
	// The live session must NOT be flagged dead.
	if strings.Contains(out, "SESSION DEAD?") {
		t.Errorf("SESSION DEAD? must NOT appear for a session in the live DB; output:\n%s", out)
	}
}

// TestRecommendJSONWipFields verifies that --json output emits the new field
// names advisory_limit + per_session_soft_limit and no longer "limit".
func TestRecommendJSONWipFields(t *testing.T) {
	hgDir := setupWipDir(t)

	// One in-progress item to make the WIP section non-trivial.
	makeInProgressNode(t, hgDir, "feature", "feat-rjson01", "Recommend JSON Feature", "sess-rj")

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := runRecommend(5, true)
	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if runErr != nil {
		t.Fatalf("runRecommend: %v", runErr)
	}

	raw := buf.String()

	// Parse the outer JSON and reach into .wip.
	var out struct {
		WIP json.RawMessage `json:"wip"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse outer JSON: %v\nraw: %s", err, raw)
	}

	var wip map[string]json.RawMessage
	if err := json.Unmarshal(out.WIP, &wip); err != nil {
		t.Fatalf("parse wip JSON: %v", err)
	}

	// advisory_limit must be present with the correct value.
	if _, ok := wip["advisory_limit"]; !ok {
		t.Errorf("wip JSON must contain 'advisory_limit' key; keys: %v", wipKeys(wip))
	} else {
		var al int
		if err := json.Unmarshal(wip["advisory_limit"], &al); err != nil || al != wipGlobalAdvisoryLimit {
			t.Errorf("advisory_limit: want %d, got raw=%s err=%v", wipGlobalAdvisoryLimit, wip["advisory_limit"], err)
		}
	}

	// per_session_soft_limit must be present with the correct value.
	if _, ok := wip["per_session_soft_limit"]; !ok {
		t.Errorf("wip JSON must contain 'per_session_soft_limit' key; keys: %v", wipKeys(wip))
	} else {
		var psl int
		if err := json.Unmarshal(wip["per_session_soft_limit"], &psl); err != nil || psl != wipPerSessionSoftLimit {
			t.Errorf("per_session_soft_limit: want %d, got raw=%s err=%v", wipPerSessionSoftLimit, wip["per_session_soft_limit"], err)
		}
	}

	// The old "limit" key must NOT be present.
	if _, ok := wip["limit"]; ok {
		t.Errorf("wip JSON must NOT contain deprecated 'limit' key; keys: %v", wipKeys(wip))
	}
}

func wipKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestWipSessionDeadFlagInDetailTable verifies the per-item SESSION column
// shows "[dead?]" for items owned by a dead session.
func TestWipSessionDeadFlagInDetailTable(t *testing.T) {
	hgDir := setupWipDir(t)

	makeInProgressNode(t, hgDir, "feature", "feat-dt001", "Detail Table Feature", "sess-dead-dt")

	// No DB → all sessions are dead.
	t.Setenv("WIPNOTE_DB_PATH", filepath.Join(t.TempDir(), "nonexistent", "wipnote.db"))

	out := captureWipShow(t)
	if !strings.Contains(out, "[dead?]") {
		t.Errorf("per-item SESSION column must show [dead?] for dead session; output:\n%s", out)
	}
}

// TestWipCompletedSessionFlaggedDead verifies that a session with status='completed'
// (present in the DB but not active) is flagged SESSION DEAD?, not treated as live.
// This is the M1 bug regression: before the fix, any row in the DB was "live".
func TestWipCompletedSessionFlaggedDead(t *testing.T) {
	hgDir := setupWipDir(t)

	makeInProgressNode(t, hgDir, "feature", "feat-comp001", "Completed-session item", "sess-completed")

	p, err := workitem.Open(hgDir, "claude-code")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	defer p.Close()

	if p.DB == nil {
		t.Skip("no DB available — cannot test completed-session liveness")
	}

	// Insert a session row with status='completed' — exists in DB, but not active.
	_, err = p.DB.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, created_at, status) VALUES (?, ?, ?, ?)`,
		"sess-completed", "test-agent", time.Now().UTC().Format("2006-01-02T15:04:05Z"), "completed",
	)
	if err != nil {
		t.Fatalf("insert completed session: %v", err)
	}

	out := captureWipShow(t)
	// A completed session must NOT be treated as live — it should be flagged dead.
	if !strings.Contains(out, "SESSION DEAD?") {
		t.Errorf("SESSION DEAD? must appear for completed (non-active) session; output:\n%s", out)
	}
}

// makeInProgressNodeMultiEdge writes an in-progress node with multiple
// implemented_in edges. olderSession is the first (stale) owner; newerSession
// is the most-recent owner. The timestamps are separated by 1 hour so
// wipLatestImplementedInSession will unambiguously pick newerSession.
func makeInProgressNodeMultiEdge(t *testing.T, dir, nodeType, id, title, olderSession, newerSession string) {
	t.Helper()
	now := time.Now().UTC()
	n := &models.Node{
		ID:     id,
		Type:   nodeType,
		Title:  title,
		Status: models.StatusInProgress,
		Edges: map[string][]models.Edge{
			string(models.RelImplementedIn): {
				{
					TargetID:     olderSession,
					Relationship: models.RelImplementedIn,
					Title:        "session " + olderSession,
					Since:        now.Add(-1 * time.Hour), // stale
				},
				{
					TargetID:     newerSession,
					Relationship: models.RelImplementedIn,
					Title:        "session " + newerSession,
					Since:        now, // latest = current owner
				},
			},
		},
	}
	subDir := filepath.Join(dir, nodeType+"s")
	if _, err := workitem.WriteNodeHTML(subDir, n); err != nil {
		t.Fatalf("WriteNodeHTML multi-edge %s: %v", id, err)
	}
}

// TestWipGroupBySessionLatestEdge verifies that an item with multiple
// implemented_in edges is grouped under the MOST RECENT session (by Since
// timestamp), not the first one. This is the M2 bug regression: before the
// fix, the first edge was used, mis-attributing restarted/handed-off items.
func TestWipGroupBySessionLatestEdge(t *testing.T) {
	hgDir := setupWipDir(t)

	// feat-multi: started in sess-old, then handed to sess-new (latest edge).
	makeInProgressNodeMultiEdge(t, hgDir, "feature", "feat-multi001", "Multi-edge feature", "sess-old", "sess-new")
	// feat-single: owned by sess-new only (to make soft-limit count clear).
	makeInProgressNode(t, hgDir, "feature", "feat-single001", "Single edge feature", "sess-new")

	items, err := scanInProgress(hgDir)
	if err != nil {
		t.Fatalf("scanInProgress: %v", err)
	}

	bySession := wipGroupBySession(items)

	// feat-multi must be under sess-new (latest), NOT sess-old (first).
	if len(bySession["sess-old"]) != 0 {
		t.Errorf("sess-old must have 0 items after handoff; got %d — multi-edge item was mis-attributed to stale session", len(bySession["sess-old"]))
	}
	if len(bySession["sess-new"]) != 2 {
		t.Errorf("sess-new must own both items (latest edge wins); got %d", len(bySession["sess-new"]))
	}
}

// TestWipSoftLimitCountsLatestOwner verifies the per-session soft-limit count
// uses most-recent ownership: if an item has been handed off from sess-prev to
// sess-curr, it counts against sess-curr (not sess-prev) for the soft limit.
func TestWipSoftLimitCountsLatestOwner(t *testing.T) {
	hgDir := setupWipDir(t)

	// Add wipPerSessionSoftLimit items all handed-off TO sess-curr.
	// Each item has sess-prev as the stale first edge, sess-curr as latest.
	for i := 0; i < wipPerSessionSoftLimit; i++ {
		id := "feat-sl" + string(rune('a'+i))
		makeInProgressNodeMultiEdge(t, hgDir, "feature", id, "Soft-limit item "+string(rune('a'+i)), "sess-prev", "sess-curr")
	}

	out := captureWipShow(t)
	// sess-curr owns wipPerSessionSoftLimit items → SOFT LIMIT must appear.
	if !strings.Contains(out, "SOFT LIMIT") {
		t.Errorf("[SOFT LIMIT] must appear: sess-curr owns %d items via latest edge; output:\n%s",
			wipPerSessionSoftLimit, out)
	}
	// sess-prev must NOT appear as owner of any item (all handed off).
	if strings.Contains(out, "sess-prev") {
		t.Errorf("sess-prev must not appear in output after all items handed off to sess-curr; output:\n%s", out)
	}
}
