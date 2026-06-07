package materialize_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/internal/otel/materialize"
)

// seedSession creates a session row + four api_request log events across
// two prompts, matching the empirical Claude run captured in tests/
// fixtures. Returns the path to the freshly-created project dir.
func seedSession(t *testing.T) (projectDir string, database *sql.DB, sessionID string) {
	t.Helper()
	projectDir = t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	if err := os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(wipnoteDir, "wipnote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	sessionID = "sess-materialize-1"
	if _, err := database.Exec(
		`INSERT INTO sessions (session_id, agent_assigned, status) VALUES (?, ?, 'completed')`,
		sessionID, "claude-code",
	); err != nil {
		t.Fatal(err)
	}

	// Two prompts, each with two api_request log events that carry
	// tokens and cost. One tool_result per prompt. One api_error only
	// on prompt 2. Tokens/cost match the third empirical turn.
	signals := []struct {
		id, promptID, canonical, kind string
		ts                            int64
		tokensIn, tokensOut           int64
		cacheRead, cacheCreation      int64
		cost                          float64
		duration                      int64
		attempt                       int
		tool                          string
	}{
		{"sig1", "prompt-A", "api_request", "log", 1, 10, 577, 23276, 2261, 0.00804885, 5835, 1, ""},
		{"sig2", "prompt-A", "api_request", "log", 2, 5, 101, 16623, 888, 0.0032823, 1846, 1, ""},
		{"sig3", "prompt-A", "tool_result", "log", 3, 0, 0, 0, 0, 0, 6799, 0, "Bash"},

		{"sig4", "prompt-B", "api_request", "log", 10, 3, 87, 0, 16623, 0.02121675, 1635, 1, ""},
		{"sig5", "prompt-B", "tool_result", "log", 11, 0, 0, 0, 0, 0, 120, 0, "Read"},
		{"sig6", "prompt-B", "api_error", "log", 12, 0, 0, 0, 0, 0, 30000, 11, ""},
	}

	stmt, err := database.Prepare(`
		INSERT INTO otel_signals (
			signal_id, harness, session_id, prompt_id, kind, canonical, native,
			ts_micros, tool_name, tokens_in, tokens_out,
			tokens_cache_read, tokens_cache_creation,
			cost_usd, cost_source, duration_ms, attempt, attrs_json
		) VALUES (?, 'claude_code', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'vendor', ?, ?, '{}')`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for _, s := range signals {
		native := "claude_code." + s.canonical
		var tool any
		if s.tool != "" {
			tool = s.tool
		}
		if _, err := stmt.Exec(s.id, sessionID, s.promptID, s.kind, s.canonical, native,
			s.ts, tool, s.tokensIn, s.tokensOut, s.cacheRead, s.cacheCreation,
			s.cost, s.duration, s.attempt); err != nil {
			t.Fatal(err)
		}
	}

	// Create a session HTML file mirroring the real CreateSessionHTML output.
	htmlPath := filepath.Join(wipnoteDir, "sessions", sessionID+".html")
	seed := `<!DOCTYPE html>
<html>
<body>
    <article id="` + sessionID + `" data-type="session" data-status="completed" data-agent="claude-code" data-event-count="6">
        <header><h1>Session</h1></header>
        <nav data-graph-edges></nav>
        <section data-activity-log><ol reversed></ol></section>
    </article>
</body>
</html>
`
	if err := os.WriteFile(htmlPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectDir, database, sessionID
}

func TestSession_AggregatesFromEmpiricalSignals(t *testing.T) {
	_, database, sessionID := seedSession(t)

	r, err := materialize.Session(database, sessionID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if r == nil {
		t.Fatal("Rollup nil for session with 6 signals")
	}

	// Three api_request log events contribute cost/tokens.
	// 0.00804885 + 0.0032823 + 0.02121675 = 0.0325479
	wantCost := 0.00804885 + 0.0032823 + 0.02121675
	if absDiff(r.TotalCostUSD, wantCost) > 1e-9 {
		t.Errorf("TotalCostUSD = %v, want %v", r.TotalCostUSD, wantCost)
	}

	if r.TotalTokensIn != 10+5+3 {
		t.Errorf("TotalTokensIn = %d, want %d", r.TotalTokensIn, 10+5+3)
	}
	if r.TotalTokensOut != 577+101+87 {
		t.Errorf("TotalTokensOut = %d, want %d", r.TotalTokensOut, 577+101+87)
	}
	if r.TotalTokensCacheRead != 23276+16623+0 {
		t.Errorf("TotalTokensCacheRead = %d", r.TotalTokensCacheRead)
	}
	if r.TotalTurns != 2 {
		t.Errorf("TotalTurns = %d, want 2", r.TotalTurns)
	}
	if r.TotalAPICalls != 3 {
		t.Errorf("TotalAPICalls = %d, want 3", r.TotalAPICalls)
	}
	if r.TotalToolCalls != 2 {
		t.Errorf("TotalToolCalls = %d, want 2", r.TotalToolCalls)
	}
	if r.TotalAPIErrors != 1 {
		t.Errorf("TotalAPIErrors = %d, want 1", r.TotalAPIErrors)
	}
	if r.MaxAttempt != 11 {
		t.Errorf("MaxAttempt = %d, want 11", r.MaxAttempt)
	}
	if r.Harness != "claude_code" {
		t.Errorf("Harness = %q", r.Harness)
	}
}

func TestSession_EmptyReturnsNil(t *testing.T) {
	_, database, _ := seedSession(t)
	r, err := materialize.Session(database, "nonexistent")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil for empty session, got %+v", r)
	}
}

func TestPrompts_ChronologicalOrder(t *testing.T) {
	_, database, sessionID := seedSession(t)
	ps, err := materialize.Prompts(database, sessionID)
	if err != nil {
		t.Fatalf("Prompts: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d prompts, want 2", len(ps))
	}
	if ps[0].PromptID != "prompt-A" || ps[1].PromptID != "prompt-B" {
		t.Errorf("order = %s, %s", ps[0].PromptID, ps[1].PromptID)
	}
	if ps[0].APICalls != 2 {
		t.Errorf("prompt-A APICalls = %d, want 2", ps[0].APICalls)
	}
	if ps[1].APIErrors != 1 {
		t.Errorf("prompt-B APIErrors = %d, want 1", ps[1].APIErrors)
	}
}

func TestMaterialize_WritesSQLiteAndHTML(t *testing.T) {
	projectDir, database, sessionID := seedSession(t)

	if err := materialize.Materialize(database, projectDir, sessionID); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	// SQLite rollup row exists with correct totals.
	var cost float64
	var turns int64
	if err := database.QueryRow(
		`SELECT total_cost_usd, total_turns FROM otel_session_rollup WHERE session_id=?`, sessionID,
	).Scan(&cost, &turns); err != nil {
		t.Fatalf("rollup lookup: %v", err)
	}
	if absDiff(cost, 0.00804885+0.0032823+0.02121675) > 1e-9 {
		t.Errorf("rollup cost = %v", cost)
	}
	if turns != 2 {
		t.Errorf("rollup turns = %d", turns)
	}

	// HTML file has the rollup section and article attributes.
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	data, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	html := string(data)
	if !strings.Contains(html, "<section data-otel-rollup") {
		t.Error("missing <section data-otel-rollup>")
	}
	if !strings.Contains(html, `data-harness="claude_code"`) {
		t.Error("missing data-harness on rollup section")
	}
	if !strings.Contains(html, `data-has-otel="true"`) {
		t.Error("missing data-has-otel on article")
	}
	if !strings.Contains(html, `data-total-cost-usd="`) {
		t.Error("missing data-total-cost-usd on article")
	}
	if !strings.Contains(html, `data-prompt-id="prompt-A"`) {
		t.Error("missing prompt-A rollup entry")
	}
	if !strings.Contains(html, `data-prompt-id="prompt-B"`) {
		t.Error("missing prompt-B rollup entry")
	}
	// Badges rendered from the aggregate.
	if !strings.Contains(html, "2 turns") {
		t.Error("missing '2 turns' badge")
	}
	if !strings.Contains(html, "1 API errors") {
		t.Error("missing api_errors badge")
	}
	if !strings.Contains(html, "max attempt 11") {
		t.Error("missing max-attempt badge")
	}
}

// TestMaterialize_IdempotentReplacesPriorRollup verifies re-running
// on a session with an existing rollup replaces the HTML section in
// place rather than duplicating it. This is the reindex-safety property.
func TestMaterialize_IdempotentReplacesPriorRollup(t *testing.T) {
	projectDir, database, sessionID := seedSession(t)

	if err := materialize.Materialize(database, projectDir, sessionID); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	if err := materialize.Materialize(database, projectDir, sessionID); err != nil {
		t.Fatalf("second materialize: %v", err)
	}

	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	data, _ := os.ReadFile(htmlPath)
	if n := strings.Count(string(data), "<section data-otel-rollup"); n != 1 {
		t.Errorf("rollup section appears %d times, want 1", n)
	}
	// data-total-cost-usd must also appear exactly once (no duplication
	// from repeated article-attr injection).
	if n := strings.Count(string(data), `data-total-cost-usd=`); n != 1 {
		t.Errorf("data-total-cost-usd appears %d times, want 1", n)
	}
}

// TestMaterialize_NoOpWhenNoSignals confirms a session without OTel data
// produces neither an HTML section nor a rollup row. This is what every
// pre-Phase-1 session looks like.
func TestMaterialize_NoOpWhenNoSignals(t *testing.T) {
	projectDir := t.TempDir()
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	os.MkdirAll(filepath.Join(wipnoteDir, "sessions"), 0o755)
	database, err := db.Open(filepath.Join(wipnoteDir, "wipnote.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	sessionID := "sess-empty"
	database.Exec(`INSERT INTO sessions (session_id, agent_assigned) VALUES (?, ?)`, sessionID, "claude-code")
	htmlPath := filepath.Join(wipnoteDir, "sessions", sessionID+".html")
	os.WriteFile(htmlPath, []byte(`<!DOCTYPE html><html><body><article id="`+sessionID+`"></article></body></html>`), 0o644)

	if err := materialize.Materialize(database, projectDir, sessionID); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM otel_session_rollup WHERE session_id=?`, sessionID).Scan(&count)
	if count != 0 {
		t.Errorf("empty session wrote %d rollup rows, want 0", count)
	}
	data, _ := os.ReadFile(htmlPath)
	if strings.Contains(string(data), "<section data-otel-rollup") {
		t.Error("empty session injected rollup section")
	}
}

// TestMaterialize_MissingHTMLSurvives confirms that a session with
// OTel signals but no session HTML file still writes the SQLite rollup.
// This is the "OTel-only" case where the hook pipeline never fired.
func TestMaterialize_MissingHTMLSurvives(t *testing.T) {
	projectDir, database, sessionID := seedSession(t)

	// Delete the session HTML file so only the SQLite path exercises.
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", sessionID+".html")
	if err := os.Remove(htmlPath); err != nil {
		t.Fatalf("remove html: %v", err)
	}

	if err := materialize.Materialize(database, projectDir, sessionID); err != nil {
		t.Fatalf("missing HTML should be non-fatal, got: %v", err)
	}
	var rollupCount int
	database.QueryRow(`SELECT COUNT(*) FROM otel_session_rollup WHERE session_id=?`, sessionID).Scan(&rollupCount)
	if rollupCount != 1 {
		t.Errorf("rollup rows = %d, want 1", rollupCount)
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
