// Package materialize aggregates OTel signals for a completed session
// and persists the rollup to otel_session_rollup plus a
// <section data-otel-rollup> block inside the session HTML file.
//
// Called from the SessionEnd hook after FinalizeSessionHTML completes.
// Empty sessions (no OTel signals) are a no-op.
//
// Design:
//   - Aggregations only count log-kind signals to avoid double-counting
//     metrics that roll up the same values (e.g. claude_code.token.usage
//     metric + claude_code.api_request log both report tokens).
//   - HTML injection acquires the same flock as hooks.AppendEventToSessionHTML
//     so concurrent finalization and sweep passes serialize naturally.
//   - MaterializeRollup is idempotent: re-running on a session with an
//     existing rollup replaces the SQLite row and regenerates the HTML
//     section in place (existing section is removed before re-inserting).
package materialize

import (
	"database/sql"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Rollup is the aggregated per-session view the dashboard reads.
// All token counts are nullable in the DB (to distinguish "reported 0"
// from "not applicable") but the aggregate always computes a concrete
// sum, so these fields are plain int64 here.
type Rollup struct {
	SessionID                string
	Harness                  string
	TotalCostUSD             float64
	TotalTokensIn            int64
	TotalTokensOut           int64
	TotalTokensCacheRead     int64
	TotalTokensCacheCreation int64
	TotalTokensThought       int64
	TotalTokensTool          int64
	TotalTokensReasoning     int64
	TotalTurns               int64
	TotalToolCalls           int64
	TotalAPICalls            int64
	TotalAPIErrors           int64
	MaxAttempt               int64
	MaterializedAt           time.Time
}

// Session queries otel_signals for a session and returns the aggregated
// Rollup. Returns (nil, nil) if no signals exist for this session —
// callers should skip rollup writes for empty sessions rather than
// persist a zeroed row.
//
// Cost and token aggregates include only log-kind api_request signals
// to avoid double-counting the metric rollups Claude Code emits in
// parallel (claude_code.token.usage, claude_code.cost.usage).
func Session(db *sql.DB, sessionID string) (*Rollup, error) {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM otel_signals WHERE session_id = ?`, sessionID,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("count otel_signals: %w", err)
	}
	if count == 0 {
		return nil, nil
	}

	r := &Rollup{SessionID: sessionID, MaterializedAt: time.Now().UTC()}
	err := db.QueryRow(`
		SELECT
			COALESCE(MIN(harness), ''),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN cost_usd END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_in END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_out END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_cache_read END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_cache_creation END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_thought END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_tool END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_reasoning END), 0),
			COUNT(DISTINCT CASE WHEN prompt_id IS NOT NULL AND prompt_id != '' THEN prompt_id END),
			SUM(CASE WHEN canonical='tool_result' AND kind='log' THEN 1 ELSE 0 END),
			SUM(CASE WHEN canonical='api_request' AND kind='log' THEN 1 ELSE 0 END),
			SUM(CASE WHEN canonical='api_error' AND kind='log' THEN 1 ELSE 0 END),
			COALESCE(MAX(attempt), 0)
		FROM otel_signals
		WHERE session_id = ?`, sessionID,
	).Scan(
		&r.Harness,
		&r.TotalCostUSD,
		&r.TotalTokensIn,
		&r.TotalTokensOut,
		&r.TotalTokensCacheRead,
		&r.TotalTokensCacheCreation,
		&r.TotalTokensThought,
		&r.TotalTokensTool,
		&r.TotalTokensReasoning,
		&r.TotalTurns,
		&r.TotalToolCalls,
		&r.TotalAPICalls,
		&r.TotalAPIErrors,
		&r.MaxAttempt,
	)
	if err != nil {
		return nil, fmt.Errorf("aggregate otel_signals: %w", err)
	}
	return r, nil
}

// PromptBreakdown is one row of per-prompt aggregates. The dashboard's
// event-tree view renders these next to each turn.
type PromptBreakdown struct {
	PromptID            string
	FirstTs             int64
	DurationMs          int64
	CostUSD             float64
	TokensIn            int64
	TokensOut           int64
	TokensCacheRead     int64
	TokensCacheCreation int64
	APICalls            int64
	ToolCalls           int64
	APIErrors           int64
}

// Prompts returns per-prompt aggregates for a session, ordered by the
// earliest signal timestamp (chronological turn order). Skips signals
// without a prompt_id.
func Prompts(db *sql.DB, sessionID string) ([]PromptBreakdown, error) {
	rows, err := db.Query(`
		SELECT
			prompt_id,
			MIN(ts_micros),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN duration_ms END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN cost_usd END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_in END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_out END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_cache_read END), 0),
			COALESCE(SUM(CASE WHEN canonical='api_request' AND kind='log' THEN tokens_cache_creation END), 0),
			SUM(CASE WHEN canonical='api_request' AND kind='log' THEN 1 ELSE 0 END),
			SUM(CASE WHEN canonical='tool_result' AND kind='log' THEN 1 ELSE 0 END),
			SUM(CASE WHEN canonical='api_error' AND kind='log' THEN 1 ELSE 0 END)
		FROM otel_signals
		WHERE session_id = ? AND prompt_id IS NOT NULL AND prompt_id != ''
		GROUP BY prompt_id
		ORDER BY MIN(ts_micros) ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query prompts: %w", err)
	}
	defer rows.Close()

	var out []PromptBreakdown
	for rows.Next() {
		var p PromptBreakdown
		if err := rows.Scan(
			&p.PromptID, &p.FirstTs, &p.DurationMs, &p.CostUSD,
			&p.TokensIn, &p.TokensOut, &p.TokensCacheRead, &p.TokensCacheCreation,
			&p.APICalls, &p.ToolCalls, &p.APIErrors,
		); err != nil {
			return nil, fmt.Errorf("scan prompt: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// writeSQLite persists the rollup into otel_session_rollup, replacing
// any prior row for the same session_id.
func writeSQLite(db *sql.DB, r *Rollup) error {
	_, err := db.Exec(`
		INSERT INTO otel_session_rollup (
			session_id, harness, total_cost_usd,
			total_tokens_in, total_tokens_out,
			total_tokens_cache_read, total_tokens_cache_creation,
			total_tokens_thought, total_tokens_tool, total_tokens_reasoning,
			total_turns, total_tool_calls, total_api_calls, total_api_errors,
			max_attempt, materialized_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			harness = excluded.harness,
			total_cost_usd = excluded.total_cost_usd,
			total_tokens_in = excluded.total_tokens_in,
			total_tokens_out = excluded.total_tokens_out,
			total_tokens_cache_read = excluded.total_tokens_cache_read,
			total_tokens_cache_creation = excluded.total_tokens_cache_creation,
			total_tokens_thought = excluded.total_tokens_thought,
			total_tokens_tool = excluded.total_tokens_tool,
			total_tokens_reasoning = excluded.total_tokens_reasoning,
			total_turns = excluded.total_turns,
			total_tool_calls = excluded.total_tool_calls,
			total_api_calls = excluded.total_api_calls,
			total_api_errors = excluded.total_api_errors,
			max_attempt = excluded.max_attempt,
			materialized_at = excluded.materialized_at`,
		r.SessionID, r.Harness, r.TotalCostUSD,
		r.TotalTokensIn, r.TotalTokensOut,
		r.TotalTokensCacheRead, r.TotalTokensCacheCreation,
		r.TotalTokensThought, r.TotalTokensTool, r.TotalTokensReasoning,
		r.TotalTurns, r.TotalToolCalls, r.TotalAPICalls, r.TotalAPIErrors,
		r.MaxAttempt, r.MaterializedAt.UnixMicro(),
	)
	if err != nil {
		return fmt.Errorf("upsert rollup: %w", err)
	}
	return nil
}

// existingRollupRe matches a previously-written rollup section so
// re-materialization can remove it before inserting the new one.
// Matches <section data-otel-rollup...> with any attribute suffix.
var existingRollupRe = regexp.MustCompile(`(?s)\s*<section data-otel-rollup(?:\s[^>]*)?>.*?</section>\s*`)

// articleOpenTagRe captures the opening <article ...> tag so we can add
// or update OTel rollup attributes on it.
var articleOpenTagRe = regexp.MustCompile(`<article\s+[^>]*>`)

// writeHTML injects or replaces the <section data-otel-rollup> block in
// the session HTML file and updates article-level data attributes.
// Acquires the same exclusive flock as hooks.AppendEventToSessionHTML
// so concurrent writers do not lose updates.
//
// Non-fatal: a missing HTML file is not an error — the hook pipeline
// may not have created one for an OTel-only session. In that case the
// SQLite rollup is still written and the HTML is simply skipped.
func writeHTML(projectDir string, r *Rollup, prompts []PromptBreakdown) error {
	htmlPath := filepath.Join(projectDir, ".wipnote", "sessions", r.SessionID+".html")
	f, err := os.OpenFile(htmlPath, os.O_RDWR, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // OK: the rollup lives in SQLite only for this session
		}
		return fmt.Errorf("open %s: %w", htmlPath, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", htmlPath, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", htmlPath, err)
	}
	content := string(data)

	// Remove any prior rollup section before inserting a fresh one.
	content = existingRollupRe.ReplaceAllString(content, "\n")

	// Build the rollup section.
	section := renderRollupSection(r, prompts)

	// Insert just before </article>.
	marker := "</article>"
	idx := strings.LastIndex(content, marker)
	if idx == -1 {
		return fmt.Errorf("no </article> marker in %s", htmlPath)
	}
	content = content[:idx] + section + content[idx:]

	// Update (or add) article-level data-* attributes so the dashboard
	// can render totals without parsing the section body.
	content = updateArticleAttrs(content, r)

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate %s: %w", htmlPath, err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek %s: %w", htmlPath, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		return fmt.Errorf("write %s: %w", htmlPath, err)
	}
	return nil
}

// renderRollupSection produces the <section data-otel-rollup> block
// the dashboard renders.
func renderRollupSection(r *Rollup, prompts []PromptBreakdown) string {
	var b strings.Builder
	b.WriteString("    <section data-otel-rollup")
	b.WriteString(` data-harness="` + html.EscapeString(r.Harness) + `"`)
	b.WriteString(`>` + "\n")
	b.WriteString(`        <header>` + "\n")
	b.WriteString(fmt.Sprintf(`            <div class="metadata">` + "\n"))
	b.WriteString(fmt.Sprintf(`                <span class="badge">$%.4f</span>`+"\n", r.TotalCostUSD))
	b.WriteString(fmt.Sprintf(`                <span class="badge">%s tokens</span>`+"\n", humanTokens(r.TotalTokensIn+r.TotalTokensOut+r.TotalTokensCacheRead+r.TotalTokensCacheCreation)))
	b.WriteString(fmt.Sprintf(`                <span class="badge">%d turns</span>`+"\n", r.TotalTurns))
	b.WriteString(fmt.Sprintf(`                <span class="badge">%d tool calls</span>`+"\n", r.TotalToolCalls))
	if r.TotalAPIErrors > 0 {
		b.WriteString(fmt.Sprintf(`                <span class="badge">%d API errors</span>`+"\n", r.TotalAPIErrors))
	}
	if r.MaxAttempt > 1 {
		b.WriteString(fmt.Sprintf(`                <span class="badge">max attempt %d</span>`+"\n", r.MaxAttempt))
	}
	b.WriteString(`            </div>` + "\n")
	b.WriteString(`        </header>` + "\n")
	if len(prompts) > 0 {
		b.WriteString(`        <ol data-prompt-rollups>` + "\n")
		for _, p := range prompts {
			b.WriteString(renderPromptRollup(p))
		}
		b.WriteString(`        </ol>` + "\n")
	}
	b.WriteString(`    </section>` + "\n")
	return b.String()
}

func renderPromptRollup(p PromptBreakdown) string {
	return fmt.Sprintf(
		`            <li data-prompt-id=%q data-duration-ms="%d" data-cost-usd="%.6f" data-tokens-in="%d" data-tokens-out="%d" data-api-calls="%d" data-tool-calls="%d">%d tools · %d API · $%.4f</li>`+"\n",
		html.EscapeString(p.PromptID), p.DurationMs, p.CostUSD,
		p.TokensIn, p.TokensOut, p.APICalls, p.ToolCalls,
		p.ToolCalls, p.APICalls, p.CostUSD,
	)
}

// humanTokens formats a token count with k/M suffix for readability.
func humanTokens(n int64) string {
	switch {
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
}

// updateArticleAttrs adds or replaces data-has-otel, data-total-cost-usd,
// and data-total-tokens on the first <article> tag. Preserves existing
// attributes and their order.
func updateArticleAttrs(content string, r *Rollup) string {
	return articleOpenTagRe.ReplaceAllStringFunc(content, func(tag string) string {
		tag = stripDataAttr(tag, "data-has-otel")
		tag = stripDataAttr(tag, "data-total-cost-usd")
		tag = stripDataAttr(tag, "data-total-tokens")
		totalTokens := r.TotalTokensIn + r.TotalTokensOut + r.TotalTokensCacheRead + r.TotalTokensCacheCreation
		inject := fmt.Sprintf(` data-has-otel="true" data-total-cost-usd="%.6f" data-total-tokens="%d"`,
			r.TotalCostUSD, totalTokens)
		return strings.TrimSuffix(tag, ">") + inject + ">"
	})
}

// stripDataAttr removes a single data-* attribute from an HTML open tag,
// leaving a single trailing space so re-joining stays clean. A no-op
// when the attribute is absent.
func stripDataAttr(tag, name string) string {
	re := regexp.MustCompile(`\s+` + regexp.QuoteMeta(name) + `="[^"]*"`)
	return re.ReplaceAllString(tag, "")
}

// Materialize is the one-call entry point used by the SessionEnd hook.
// Aggregates, writes SQLite rollup, injects HTML section. All errors
// are returned for the caller to log — the caller treats failures as
// non-critical since the session is already finalized.
//
// Idempotent: callers may invoke Materialize multiple times on the same
// session (e.g. after a retroactive reindex) and the latest data wins.
func Materialize(db *sql.DB, projectDir, sessionID string) error {
	r, err := Session(db, sessionID)
	if err != nil {
		return err
	}
	if r == nil {
		return nil // no OTel data: nothing to materialize
	}
	if err := writeSQLite(db, r); err != nil {
		return err
	}
	prompts, err := Prompts(db, sessionID)
	if err != nil {
		return err
	}
	if err := writeHTML(projectDir, r, prompts); err != nil {
		return err
	}
	return nil
}
