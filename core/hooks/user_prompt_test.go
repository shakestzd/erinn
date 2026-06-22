package hooks

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// setupTestDB creates a per-test on-disk SQLite DB with schema and a session.
// Each call gets its own isolated database to prevent UNIQUE constraint
// violations when tests share the same in-memory connection cache.
// Also clears the featureIDCache to prevent cross-test pollution.
func setupTestDB(t *testing.T) *testDB {
	t.Helper()

	// plan-2390966a slice-4: the hot hooks now route their derived-index writes
	// through the daemon-first enqueue seam. Disable auto-spawn so the route
	// fast-fails to the in-handler direct fallback (writing to the *sql.DB this
	// helper returns) instead of forking a headless writer + dial-waiting in a
	// unit test. The rows the tests assert on land on the returned handle.
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")

	// Reset the global feature ID cache to prevent interference between tests
	// (each test may use the same session ID "test-sess" but with different
	// active features or no active feature).
	featureIDCache = featureIDCacheEntry{}

	dbPath := filepath.Join(t.TempDir(), "wipnote.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Now().UTC()

	sess := &models.Session{
		SessionID:     "test-sess",
		AgentAssigned: "claude-code",
		CreatedAt:     now,
		Status:        "active",
		Model:         "sonnet-4",
	}
	if err := db.InsertSession(database, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	return &testDB{DB: database, now: now, t: t}
}

type testDB struct {
	DB  *sql.DB
	now time.Time
	t   *testing.T
}

func (td *testDB) addTrack(id, title string) {
	td.t.Helper()
	now := td.now.Format(time.RFC3339)
	_, err := td.DB.Exec(
		`INSERT INTO tracks (id, title, status, created_at, updated_at) VALUES (?,?,?,?,?)`,
		id, title, "active", now, now,
	)
	if err != nil {
		td.t.Fatalf("insert track: %v", err)
	}
}

func (td *testDB) addFeature(id, ftype, title, status string) {
	td.t.Helper()
	feat := &db.Feature{
		ID:        id,
		Type:      ftype,
		Title:     title,
		Status:    status,
		Priority:  "medium",
		CreatedAt: td.now,
		UpdatedAt: td.now,
	}
	if err := db.InsertFeature(td.DB, feat); err != nil {
		td.t.Fatalf("InsertFeature(%s): %v", id, err)
	}
}

// addSession inserts a bare session row, optionally linked to a parent and an
// active feature. Empty parentID / activeFeatureID are stored as NULL.
func (td *testDB) addSession(id, parentID, activeFeatureID string) {
	td.t.Helper()
	var parent, feat interface{}
	if parentID != "" {
		parent = parentID
	}
	if activeFeatureID != "" {
		feat = activeFeatureID
	}
	_, err := td.DB.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, parent_session_id, active_feature_id, created_at, status)
		 VALUES (?,?,?,?,?,?)`,
		id, "claude-code", parent, feat, td.now.Format(time.RFC3339), "active",
	)
	if err != nil {
		td.t.Fatalf("addSession(%s): %v", id, err)
	}
}

func (td *testDB) setActiveFeature(sessionID, featureID string) {
	td.t.Helper()
	_, err := td.DB.Exec(
		`UPDATE sessions SET active_feature_id = ? WHERE session_id = ?`,
		featureID, sessionID,
	)
	if err != nil {
		td.t.Fatalf("setActiveFeature: %v", err)
	}
}

func TestUserPrompt_EmptyPrompt(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	event := &CloudEvent{SessionID: "test-sess", Prompt: ""}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}
	if !result.Continue {
		t.Error("expected Continue=true for empty prompt")
	}
}

func TestUserPrompt_InsertsUserQuery(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	event := &CloudEvent{SessionID: "test-sess", Prompt: "implement a new API endpoint"}
	_, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	// Verify a UserQuery event was inserted.
	var count int
	if err := td.DB.QueryRow(
		`SELECT COUNT(*) FROM agent_events WHERE session_id = 'test-sess' AND tool_name = 'UserQuery'`,
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 UserQuery event, got %d", count)
	}
}

func TestUserPrompt_WithOpenItems_ReturnsAttribution(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	// Add features so open items exist.
	td.addFeature("feat-aaa", "feature", "Auth System", "in-progress")
	td.addFeature("feat-bbb", "feature", "Dashboard", "todo")
	td.setActiveFeature("test-sess", "feat-aaa")

	// Prompt that triggers exploration intent — should include classification + active one-liner
	event := &CloudEvent{SessionID: "test-sess", Prompt: "show me all the files in the codebase"}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	if result.AdditionalContext == "" {
		t.Fatal("expected AdditionalContext with classification signals")
	}
	// Per-turn context should be terse, not include the full "Open work items" roster
	if strings.Contains(result.AdditionalContext, "Open work items") {
		t.Errorf("UserPrompt should be terse (no full attribution), got: %s", result.AdditionalContext)
	}
	// Should reference the active feature in one-liner
	if !strings.Contains(result.AdditionalContext, "feat-aaa") {
		t.Errorf("guidance should reference active feature in one-liner, got: %s", result.AdditionalContext)
	}
}

func TestUserPrompt_ImplementationWithSpike_WarnsAboutSpike(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	td.addFeature("spk-001", "spike", "Research caching", "in-progress")
	_, err := td.DB.Exec(`UPDATE sessions SET active_feature_id = 'spk-001' WHERE session_id = 'test-sess'`)
	if err != nil {
		t.Fatalf("setActiveFeature: %v", err)
	}

	event := &CloudEvent{SessionID: "test-sess", Prompt: "implement the caching layer"}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	if result.AdditionalContext == "" {
		t.Fatal("expected AdditionalContext with spike warning")
	}
	if !strings.Contains(result.AdditionalContext, "spike") {
		t.Errorf("guidance should warn about spike, got: %s", result.AdditionalContext)
	}
}

func TestUserPrompt_Dedup(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	event := &CloudEvent{SessionID: "test-sess", Prompt: "hello world"}
	_, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second identical call within 5s should be deduped.
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !result.Continue {
		t.Error("expected Continue=true for deduped prompt")
	}
}

func TestUserPrompt_SanitizesXMLBlocks(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	prompt := "<system-reminder>internal stuff</system-reminder>implement auth"
	event := &CloudEvent{SessionID: "test-sess", Prompt: prompt}
	_, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	// Verify the stored summary does not contain the XML block.
	var summary string
	if err := td.DB.QueryRow(
		`SELECT input_summary FROM agent_events WHERE session_id = 'test-sess' AND tool_name = 'UserQuery'`,
	).Scan(&summary); err != nil {
		t.Fatalf("query: %v", err)
	}
	if strings.Contains(summary, "system-reminder") {
		t.Errorf("stored summary should not contain XML block, got: %s", summary)
	}
	if !strings.Contains(summary, "implement auth") {
		t.Errorf("stored summary should contain actual prompt, got: %s", summary)
	}
}

func TestCompactCLIRef_MentionsTrackRequirement(t *testing.T) {
	if !strings.Contains(compactCLIRef, "--track") {
		t.Error("compactCLIRef should mention --track requirement")
	}
	if !strings.Contains(compactCLIRef, "--description") {
		t.Error("compactCLIRef should mention --description requirement")
	}
}

func TestGetActiveWorkItemType(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	td.addFeature("feat-001", "feature", "Auth", "in-progress")
	td.addFeature("spk-001", "spike", "Research", "in-progress")

	if got := getActiveWorkItemType(td.DB, "feat-001"); got != "feature" {
		t.Errorf("expected 'feature', got %q", got)
	}
	if got := getActiveWorkItemType(td.DB, "spk-001"); got != "spike" {
		t.Errorf("expected 'spike', got %q", got)
	}
	if got := getActiveWorkItemType(td.DB, "nonexistent"); got != "" {
		t.Errorf("expected empty for nonexistent, got %q", got)
	}
	if got := getActiveWorkItemType(td.DB, ""); got != "" {
		t.Errorf("expected empty for empty ID, got %q", got)
	}
}

func TestUserPrompt_TerseAdditionalContext(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	// Add features
	td.addFeature("feat-aaa", "feature", "Auth System", "in-progress")
	td.addFeature("feat-bbb", "feature", "Dashboard", "todo")
	td.setActiveFeature("test-sess", "feat-aaa")

	// Use a prompt that triggers exploration (has "search" keyword)
	event := &CloudEvent{SessionID: "test-sess", Prompt: "search for all error handling patterns"}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	if result.AdditionalContext == "" {
		t.Fatal("expected AdditionalContext with classification + active item hint")
	}
	// Should be terse — check that it does NOT contain "Open work items"
	if strings.Contains(result.AdditionalContext, "Open work items") {
		t.Errorf("UserPrompt should be terse (no open items roster), got: %s", result.AdditionalContext)
	}
	// Should not contain the full CLI ref
	if strings.Contains(result.AdditionalContext, "wipnote CLI") {
		t.Errorf("UserPrompt should be terse (no CLI ref), got: %s", result.AdditionalContext)
	}
	// Should contain the active item one-liner
	if !strings.Contains(result.AdditionalContext, "ACTIVE:") {
		t.Errorf("UserPrompt should mention active item, got: %s", result.AdditionalContext)
	}
	// Check character count is reasonable (terse, not ~500+ lines)
	if len(result.AdditionalContext) > 500 {
		t.Errorf("UserPrompt additionalContext too long (%d chars, expected <500), got: %s",
			len(result.AdditionalContext), result.AdditionalContext)
	}
}

func TestUserPrompt_ActiveOnelinerIncluded(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	td.addFeature("bug-xyz", "bug", "Fix login crash", "in-progress")
	td.setActiveFeature("test-sess", "bug-xyz")

	event := &CloudEvent{SessionID: "test-sess", Prompt: "continue with the fix"}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	if !strings.Contains(result.AdditionalContext, "ACTIVE: bug-xyz") {
		t.Errorf("guidance should include 'ACTIVE: bug-xyz', got: %s", result.AdditionalContext)
	}
	if !strings.Contains(result.AdditionalContext, "Fix login crash") {
		t.Errorf("guidance should include feature title, got: %s", result.AdditionalContext)
	}
}

func TestUserPrompt_NoActiveNoOneliner(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	os.Setenv("WIPNOTE_SESSION_ID", "test-sess")
	defer os.Unsetenv("WIPNOTE_SESSION_ID")

	td.addFeature("feat-aaa", "feature", "Auth System", "in-progress")
	// Don't set active feature
	// Use a prompt that triggers investigation keyword without bug keywords
	event := &CloudEvent{SessionID: "test-sess", Prompt: "can you investigate the performance bottleneck?"}
	result, err := UserPrompt(event, td.DB)
	if err != nil {
		t.Fatalf("UserPrompt: %v", err)
	}

	// When no active item, the ACTIVE line should not appear in additionalContext
	if result.AdditionalContext != "" && strings.Contains(result.AdditionalContext, "ACTIVE:") {
		t.Errorf("UserPrompt should not have ACTIVE line when no active feature, got: %s", result.AdditionalContext)
	}
	// But it should still have investigation guidance
	if result.AdditionalContext == "" || !strings.Contains(result.AdditionalContext, "Investigation") {
		t.Errorf("UserPrompt should mention investigation intent, got: %s", result.AdditionalContext)
	}
}

func TestBuildActiveItemOneLiner_WithTitle(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	td.addFeature("feat-001", "feature", "Refactor DB layer", "in-progress")
	hint := buildActiveItemOneLiner(td.DB, "feat-001")

	expected := "ACTIVE: feat-001 — Refactor DB layer"
	if hint != expected {
		t.Errorf("expected %q, got %q", expected, hint)
	}
}

func TestBuildActiveItemOneLiner_WithoutTitle(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	td.addFeature("feat-001", "feature", "", "in-progress")
	hint := buildActiveItemOneLiner(td.DB, "feat-001")

	expected := "ACTIVE: feat-001"
	if hint != expected {
		t.Errorf("expected %q, got %q", expected, hint)
	}
}

func TestBuildActiveItemOneLiner_NotFound(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	hint := buildActiveItemOneLiner(td.DB, "nonexistent-id")
	expected := "ACTIVE: nonexistent-id"
	if hint != expected {
		t.Errorf("expected %q, got %q", expected, hint)
	}
}

func TestBuildActiveItemOneLiner_Empty(t *testing.T) {
	td := setupTestDB(t)
	defer td.DB.Close()

	hint := buildActiveItemOneLiner(td.DB, "")
	if hint != "" {
		t.Errorf("expected empty string for empty ID, got %q", hint)
	}
}
