package db

import (
	"database/sql"
	"fmt"
)

// Per-work-item outcome rollup reads (feat-7ee73444).
//
// These are the derived-index half of the rollup that Collection.Complete
// persists into a work item's canonical HTML. They are deliberately shaped as
// *counted* aggregates rather than bare values: every metric ships with the
// number of rows that actually supplied it and the number of rows that were
// eligible to, because the writer's contract is "omit, never zero" — a metric
// with no underlying rows must be absent from the artifact, and a metric with
// thin coverage must say so rather than pass itself off as complete.
//
// All of these read the generic item ID column (`feature_id`), which is keyed
// by work-item ID regardless of type — features, bugs and spikes alike.

// itemOutcomeEligibleSQL is the WHERE fragment restricting the failure-rate
// denominator to the (kind, canonical) pairs where an outcome is semantically
// defined at all. Names mirror the constants in observe/otel/signal.go; the
// mapping from harness-native event names is in observe/otel/adapter/claude.go:
//
//   - log/tool_result     — claude_code.tool_result's "success" attribute,
//     the one non-beta outcome signal (SuccessSourceLogStable).
//   - span/tool_execution — claude_code.tool.execution's "success" boolean
//     (beta, but the log above reports the same outcome).
//   - span/api_request    — claude_code.llm_request's "success" boolean. Beta
//     with no stable counterpart; still the only report of API-call outcome.
//   - log/api_request + log/api_error — the structural pair: reaching an
//     api_request log at all implies success, and a failed call is emitted as
//     a separate api_error. Both halves must be in the same denominator or the
//     rate is structurally pinned to zero.
//
// Everything else is excluded because it structurally never carries an
// outcome: span/tool_result (claude_code.tool) and metric/token_usage were
// measured at 0 non-null `success` across ~17k live rows, and counting them
// would deflate the rate toward zero rather than leave it unknown.
const itemOutcomeEligibleSQL = `(
	(kind = 'log'  AND canonical IN ('tool_result', 'api_request', 'api_error')) OR
	(kind = 'span' AND canonical IN ('tool_execution', 'api_request'))
)`

// ItemOutcomeStats is the telemetry half of a work item's rollup. Every value
// is paired with the counts needed to state its coverage honestly.
type ItemOutcomeStats struct {
	// Signals is every otel_signals row attributed to the item. Zero means
	// the item has no telemetry at all (a Codex or Antigravity item, or a
	// Claude item completed with telemetry disabled) and the whole telemetry
	// half must be omitted.
	Signals int

	// OutcomeEligible counts rows in the itemOutcomeEligibleSQL set;
	// OutcomeObserved counts the subset that actually carried a non-null
	// success. Their ratio is the failure rate's coverage.
	OutcomeEligible int
	OutcomeObserved int
	Failures        int

	// DurationRows/DurationMsTotal sum span durations. Concurrent agents
	// produce overlapping spans, so the total is NOT elapsed wall clock —
	// see ElapsedMs for that.
	DurationRows    int
	DurationMsTotal int64

	// ElapsedMs is last-signal minus first-signal: real elapsed wall clock
	// across the item's telemetry, valid whenever Signals > 0.
	ElapsedMs int64

	// AttemptRows counts rows carrying an attempt number; Retries counts
	// those past the first try.
	AttemptRows int
	Retries     int

	// CostRows/CostUSDTotal are the known-degraded cost figures: only a
	// minority of signals carry cost_usd, so the total systematically
	// under-reports. Callers must surface CostRows as coverage.
	CostRows     int
	CostUSDTotal float64
}

// ItemOutcomeRollup aggregates every otel_signals row attributed to itemID
// into a single scan of the idx_otel_feature_ts index.
//
// Returns a zero-valued ItemOutcomeStats (not an error) when the item has no
// telemetry rows: absence of telemetry is an expected, reportable state, not a
// failure.
func ItemOutcomeRollup(database *sql.DB, itemID string) (ItemOutcomeStats, error) {
	var s ItemOutcomeStats
	var minTS, maxTS sql.NullInt64

	err := database.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN `+itemOutcomeEligibleSQL+` THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN `+itemOutcomeEligibleSQL+` AND success IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN `+itemOutcomeEligibleSQL+` AND success = 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN duration_ms IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(duration_ms), 0),
			COALESCE(SUM(CASE WHEN attempt IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN attempt > 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN cost_usd IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(cost_usd), 0),
			MIN(ts_micros),
			MAX(ts_micros)
		FROM otel_signals
		WHERE feature_id = ?`, itemID).Scan(
		&s.Signals,
		&s.OutcomeEligible, &s.OutcomeObserved, &s.Failures,
		&s.DurationRows, &s.DurationMsTotal,
		&s.AttemptRows, &s.Retries,
		&s.CostRows, &s.CostUSDTotal,
		&minTS, &maxTS,
	)
	if err != nil {
		return ItemOutcomeStats{}, fmt.Errorf("otel rollup for %s: %w", itemID, err)
	}
	if minTS.Valid && maxTS.Valid && maxTS.Int64 > minTS.Int64 {
		s.ElapsedMs = (maxTS.Int64 - minTS.Int64) / 1000
	}
	return s, nil
}

// ItemEditChurn is the per-file thrash signal for a work item.
//
// It is derived from edit tool calls rather than from commits, because there
// is no commit-to-file join in the derived index at all: git_commits is keyed
// (commit_hash, session_id, feature_id) with no path column, and feature_files
// is UNIQUE(feature_id, file_path), which collapses repeat touches by
// construction. Edit-call multiplicity is the only surviving record of "the
// same file was rewritten more than once", so that is what this measures —
// and the caller labels it as such rather than claiming commit-level churn.
type ItemEditChurn struct {
	// EditEvents is every edit-shaped tool call attributed to the item,
	// including those whose tool_input was not persisted. FilesResolved is
	// the number of distinct paths recoverable from tool_input; their ratio
	// is this metric's coverage.
	EditEvents    int
	FilesResolved int

	// ChurnedFiles counts resolved paths edited more than once, and
	// MaxEditsPerFile is the deepest single-file rewrite count.
	ChurnedFiles    int
	MaxEditsPerFile int
}

// itemEditToolNames are the tool calls that rewrite a file in place. Kept as a
// literal IN list rather than a parameterised one because agent_events has no
// feature_id index — the query is already a scan, and a constant list lets
// SQLite short-circuit most rows on tool_name before touching the JSON.
const itemEditToolNames = `('Edit', 'Write', 'MultiEdit', 'NotebookEdit')`

// ItemEditChurnStats computes the per-file rewrite counts for itemID.
//
// Returns a zero value (not an error) when nothing is attributed to the item.
func ItemEditChurnStats(database *sql.DB, itemID string) (ItemEditChurn, error) {
	var c ItemEditChurn

	if err := database.QueryRow(`
		SELECT COUNT(*)
		FROM agent_events
		WHERE feature_id = ? AND event_type = 'tool_call'
		  AND tool_name IN `+itemEditToolNames, itemID).Scan(&c.EditEvents); err != nil {
		return ItemEditChurn{}, fmt.Errorf("edit churn events for %s: %w", itemID, err)
	}
	if c.EditEvents == 0 {
		return c, nil
	}

	err := database.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN n > 1 THEN 1 ELSE 0 END), 0),
			COALESCE(MAX(n), 0)
		FROM (
			SELECT COUNT(*) AS n
			FROM agent_events
			WHERE feature_id = ? AND event_type = 'tool_call'
			  AND tool_name IN `+itemEditToolNames+`
			  AND json_extract(tool_input, '$.file_path') IS NOT NULL
			GROUP BY json_extract(tool_input, '$.file_path')
		)`, itemID).Scan(&c.FilesResolved, &c.ChurnedFiles, &c.MaxEditsPerFile)
	if err != nil {
		return ItemEditChurn{}, fmt.Errorf("edit churn paths for %s: %w", itemID, err)
	}
	return c, nil
}
