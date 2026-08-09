package workitem_test

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// Per-work-item rollup tests (feat-7ee73444).
//
// The two properties under test are the two the feature exists to guarantee:
// a metric with no underlying rows is ABSENT from the canonical HTML rather
// than written as a zero, and every metric that is present names its source.
// Both are asserted against the file on disk after a re-parse, not against the
// in-memory node, because the artifact is the contract — an attribute that
// does not survive the write/parse round-trip is not persisted at all.

// seedSession inserts the session and feature rows that otel_signals and
// agent_events reference by foreign key.
func seedSession(t *testing.T, database *sql.DB, sessionID, itemID string) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, agent_assigned, status) VALUES (?, 'claude-code', 'active')`,
		sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := database.Exec(
		`INSERT OR IGNORE INTO features (id, type, title, status) VALUES (?, 'feature', 'seeded', 'in-progress')`,
		itemID); err != nil {
		t.Fatalf("seed feature row: %v", err)
	}
}

// seedSignal inserts one otel_signals row attributed to itemID. success is
// nullable so a test can seed the "row exists but carries no outcome" shape
// that the coverage ratio exists to expose.
func seedSignal(t *testing.T, database *sql.DB, id, sessionID, itemID, kind, canonical string,
	tsMicros int64, success *bool, durationMs *int64, cost *float64, attempt *int) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO otel_signals
			(signal_id, harness, session_id, feature_id, kind, canonical, native,
			 ts_micros, success, duration_ms, cost_usd, attempt, attrs_json)
		VALUES (?, 'claude-code', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')`,
		id, sessionID, itemID, kind, canonical, canonical, tsMicros,
		success, durationMs, cost, attempt)
	if err != nil {
		t.Fatalf("seed signal %s: %v", id, err)
	}
}

func seedEdit(t *testing.T, database *sql.DB, id, sessionID, itemID, path string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO agent_events (event_id, agent_id, event_type, tool_name, tool_input, session_id, feature_id)
		VALUES (?, 'claude-code', 'tool_call', 'Edit', ?, ?, ?)`,
		id, `{"file_path":"`+path+`"}`, sessionID, itemID)
	if err != nil {
		t.Fatalf("seed edit %s: %v", id, err)
	}
}

// completeAndReparse completes id and returns both the re-parsed node and the
// raw HTML, so a test can assert on absence of an attribute (which a parsed
// map cannot distinguish from a value that never round-tripped).
func completeAndReparse(t *testing.T, p *workitem.Project, id string) (*models.Node, string) {
	t.Helper()
	if _, err := p.Features.Complete(id); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	path := p.ProjectDir + "/features/" + id + ".html"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return node, string(raw)
}

func mustCreateFeature(t *testing.T, p *workitem.Project, title string) string {
	t.Helper()
	node, err := p.Features.Create(title)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return node.ID
}

// TestRollup_OmitsMetricsWhenTelemetryAbsent is the load-bearing test for the
// "omit, never zero" rule. A work item completed with no telemetry rows at all
// — the shape of every Codex and Antigravity item today, and of any Claude
// item completed with telemetry disabled — must carry NO failure rate rather
// than a fabricated 0.0000, and must say so with an explicit marker.
func TestRollup_OmitsMetricsWhenTelemetryAbsent(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "No telemetry at all")

	node, raw := completeAndReparse(t, p, id)

	for _, absent := range []string{
		"data-rollup-failure-rate",
		"data-rollup-cost-usd",
		"data-rollup-duration-ms",
		"data-rollup-elapsed-ms",
		"data-rollup-retries",
	} {
		if strings.Contains(raw, absent) {
			t.Errorf("telemetry-free item wrote %s; a metric with no rows must be omitted, not zeroed", absent)
		}
	}

	props := workitem.RollupProps(node)
	if got := props["telemetry"]; got != workitem.MarkerUnavailable {
		t.Errorf("rollup-telemetry = %q, want %q — absence must be marked, not silent", got, workitem.MarkerUnavailable)
	}
	if got := props["git"]; got != workitem.MarkerUnavailable {
		t.Errorf("rollup-git = %q, want %q", got, workitem.MarkerUnavailable)
	}
	if props["computed-at"] == "" {
		t.Error("rollup-computed-at missing: a rollup that ran must timestamp itself even when both halves are empty")
	}
}

// TestRollup_OmitsUnmeasurableMetricsDespiteTelemetry is the sharper half of
// "omit, never zero", and the one that actually exercises the guard: the item
// HAS telemetry, so the telemetry branch runs, but not one row in it carries
// an outcome, a duration, a cost or an attempt. Without the per-metric
// omission test this is exactly where a fabricated 0.0000 failure rate would
// be written — a number that reads as "nothing failed" when the truth is
// "nothing was measured". Elapsed time is the one metric that survives here,
// because it needs only that signals exist.
func TestRollup_OmitsUnmeasurableMetricsDespiteTelemetry(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Telemetry with nothing measurable")
	seedSession(t, p.DB, "sess-blank", id)

	// token_usage carries no outcome, cost, duration or attempt by
	// construction — it is the densest signal wipnote records and the
	// emptiest for rollup purposes.
	seedSignal(t, p.DB, "blank-1", "sess-blank", id, "metric", "token_usage", 1_000_000, nil, nil, nil, nil)
	seedSignal(t, p.DB, "blank-2", "sess-blank", id, "metric", "token_usage", 3_000_000, nil, nil, nil, nil)

	node, raw := completeAndReparse(t, p, id)
	props := workitem.RollupProps(node)

	for _, metric := range []string{"failure-rate", "duration-ms", "retries", "cost-usd"} {
		if got, ok := props[metric]; ok {
			t.Errorf("%s = %q; no row supplied it, so it must be omitted rather than zeroed", metric, got)
		}
		if strings.Contains(raw, "data-rollup-"+metric+"=") {
			t.Errorf("artifact wrote data-rollup-%s for an unmeasurable metric", metric)
		}
	}
	// Elapsed is measurable from timestamps alone and must still be present.
	if got := props["elapsed-ms"]; got != "2000" {
		t.Errorf("elapsed-ms = %q, want %q", got, "2000")
	}
	// Telemetry exists, so the half-marker must NOT claim otherwise.
	if got := props["telemetry"]; got != "" {
		t.Errorf("rollup-telemetry = %q, but the item has signals", got)
	}
}

// TestRollup_PresentMetricsCarryProvenance pins the other non-negotiable:
// every number names its source, and cost names itself degraded. The seeded
// telemetry deliberately mixes outcome-bearing rows with rows that carry no
// success at all, so the coverage ratio has something real to report.
func TestRollup_PresentMetricsCarryProvenance(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Seeded telemetry")
	seedSession(t, p.DB, "sess-rollup", id)

	yes, no := true, false
	dur := int64(500)
	cost := 1.25
	att1, att3 := 1, 3

	// Outcome-bearing: three observed outcomes, one of them a failure.
	seedSignal(t, p.DB, "sig-1", "sess-rollup", id, "log", "tool_result", 1_000_000, &yes, &dur, nil, nil)
	seedSignal(t, p.DB, "sig-2", "sess-rollup", id, "log", "tool_result", 2_000_000, &no, &dur, nil, nil)
	seedSignal(t, p.DB, "sig-3", "sess-rollup", id, "span", "tool_execution", 3_000_000, &yes, nil, nil, nil)
	// Eligible but carrying no outcome — must widen coverage's denominator
	// without touching the rate.
	seedSignal(t, p.DB, "sig-4", "sess-rollup", id, "span", "api_request", 4_000_000, nil, nil, nil, &att1)
	// A retry, and the only row carrying cost.
	seedSignal(t, p.DB, "sig-5", "sess-rollup", id, "span", "api_request", 5_000_000, nil, nil, &cost, &att3)
	// Structurally outcome-free: must not enter the denominator at all.
	seedSignal(t, p.DB, "sig-6", "sess-rollup", id, "metric", "token_usage", 6_000_000, nil, nil, nil, nil)

	node, _ := completeAndReparse(t, p, id)
	props := workitem.RollupProps(node)

	// 1 failure of 3 observed outcomes.
	if got := props["failure-rate"]; got != "0.3333" {
		t.Errorf("failure-rate = %q, want %q", got, "0.3333")
	}
	// 3 observed of 5 eligible: token_usage is excluded from both.
	if got := props["failure-rate-coverage"]; got != "3/5" {
		t.Errorf("failure-rate-coverage = %q, want %q", got, "3/5")
	}
	if got := props["failure-rate-source"]; got != workitem.SourceOtelSuccess {
		t.Errorf("failure-rate-source = %q, want %q", got, workitem.SourceOtelSuccess)
	}

	// Cost must be unmistakably marked degraded — it under-reports at
	// current density and ships only because the marker says so.
	if got := props["cost-usd"]; got != "1.2500" {
		t.Errorf("cost-usd = %q, want %q", got, "1.2500")
	}
	if got := props["cost-usd-source"]; got != workitem.SourceOtelCost {
		t.Errorf("cost-usd-source = %q, want %q", got, workitem.SourceOtelCost)
	}
	if !strings.Contains(props["cost-usd-source"], "degraded") {
		t.Errorf("cost-usd-source %q does not mark the metric degraded", props["cost-usd-source"])
	}
	if got := props["cost-usd-coverage"]; got != "1/6" {
		t.Errorf("cost-usd-coverage = %q, want %q", got, "1/6")
	}

	// One row past the first attempt.
	if got := props["retries"]; got != "1" {
		t.Errorf("retries = %q, want %q", got, "1")
	}
	if got := props["retries-coverage"]; got != "2/6" {
		t.Errorf("retries-coverage = %q, want %q", got, "2/6")
	}

	// 5s between first and last signal.
	if got := props["elapsed-ms"]; got != "5000" {
		t.Errorf("elapsed-ms = %q, want %q", got, "5000")
	}
	if got := props["duration-ms"]; got != "1000" {
		t.Errorf("duration-ms = %q, want %q", got, "1000")
	}

	if props["telemetry"] != "" {
		t.Errorf("rollup-telemetry marker present alongside real telemetry: %q", props["telemetry"])
	}

	// Every value key must have a source sibling — the provenance rule
	// stated as an invariant rather than a per-metric assertion, so a new
	// metric added without a source fails here.
	for k, v := range props {
		if strings.HasSuffix(k, "-source") || strings.HasSuffix(k, "-coverage") {
			continue
		}
		if k == "computed-at" || k == "telemetry" || k == "git" || k == "unavailable" {
			continue
		}
		if props[k+"-source"] == "" {
			t.Errorf("metric %s=%q has no -source sibling", k, v)
		}
	}
}

// TestRollup_GitHalfWithoutTelemetry is the cross-harness case stated in the
// brief: an item with commits and edits but no telemetry keeps the git half
// and gets the telemetry-unavailable marker, rather than losing both.
func TestRollup_GitHalfWithoutTelemetry(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Git only")
	seedSession(t, p.DB, "sess-git", id)

	for _, h := range []string{"aaa111", "bbb222"} {
		if _, err := p.DB.Exec(
			`INSERT INTO git_commits (commit_hash, session_id, feature_id, message) VALUES (?, 'sess-git', ?, 'msg')`,
			h, id); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
	}
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		if _, err := p.DB.Exec(
			`INSERT INTO feature_files (id, feature_id, file_path, operation) VALUES (?, ?, ?, 'edit')`,
			id+"-"+f, id, f); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	// a.go edited twice, b.go once: one churned file out of two resolved.
	seedEdit(t, p.DB, "ev-1", "sess-git", id, "a.go")
	seedEdit(t, p.DB, "ev-2", "sess-git", id, "a.go")
	seedEdit(t, p.DB, "ev-3", "sess-git", id, "b.go")

	node, raw := completeAndReparse(t, p, id)
	props := workitem.RollupProps(node)

	if got := props["telemetry"]; got != workitem.MarkerUnavailable {
		t.Errorf("rollup-telemetry = %q, want %q", got, workitem.MarkerUnavailable)
	}
	if strings.Contains(raw, "data-rollup-failure-rate") {
		t.Error("telemetry-free item wrote a failure rate")
	}
	if props["git"] != "" {
		t.Errorf("rollup-git marked unavailable despite commits and files: %q", props["git"])
	}
	if got := props["commits"]; got != "2" {
		t.Errorf("commits = %q, want %q", got, "2")
	}
	if got := props["files"]; got != "3" {
		t.Errorf("files = %q, want %q", got, "3")
	}
	if got := props["churn-files"]; got != "1" {
		t.Errorf("churn-files = %q, want %q", got, "1")
	}
	if got := props["churn-files-coverage"]; got != "2/3" {
		t.Errorf("churn-files-coverage = %q, want %q", got, "2/3")
	}
	if got := props["churn-files-source"]; got != workitem.SourceEditChurn {
		t.Errorf("churn-files-source = %q, want %q", got, workitem.SourceEditChurn)
	}
}

// TestRollup_RecomputeReplacesOnReopen pins the reopen contract: Start leaves
// the rollup alone, and the next Complete recomputes from full history and
// OVERWRITES. A merge would leave a stale number next to a fresh one with no
// way to tell them apart, so this asserts both the new value and that the key
// appears exactly once in the artifact.
func TestRollup_RecomputeReplacesOnReopen(t *testing.T) {
	p := newTestProject(t)
	id := mustCreateFeature(t, p, "Reopened item")
	seedSession(t, p.DB, "sess-reopen", id)

	if _, err := p.DB.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message) VALUES ('c1', 'sess-reopen', ?, 'first')`,
		id); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	node, _ := completeAndReparse(t, p, id)
	first := workitem.RollupProps(node)
	if got := first["commits"]; got != "1" {
		t.Fatalf("first completion: commits = %q, want %q", got, "1")
	}
	// The first completion ran before any telemetry existed, so it left the
	// unavailable marker. That marker is what must NOT survive a recompute
	// that does find telemetry — an artifact carrying both a failure rate and
	// a note saying telemetry was unavailable contradicts itself.
	if got := first["telemetry"]; got != workitem.MarkerUnavailable {
		t.Fatalf("first completion: rollup-telemetry = %q, want %q", got, workitem.MarkerUnavailable)
	}

	// Reopen. Start must not disturb the existing rollup.
	if _, err := p.Features.Start(id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	reopened, err := p.Features.Get(id)
	if err != nil {
		t.Fatalf("Get after Start: %v", err)
	}
	if got := workitem.RollupProps(reopened)["commits"]; got != "1" {
		t.Errorf("Start clobbered the rollup: commits = %q, want %q", got, "1")
	}

	// Fresh work lands during the reopen: another commit, and telemetry that
	// did not exist the first time round.
	if _, err := p.DB.Exec(
		`INSERT INTO git_commits (commit_hash, session_id, feature_id, message) VALUES ('c2', 'sess-reopen', ?, 'second')`,
		id); err != nil {
		t.Fatalf("seed second commit: %v", err)
	}
	yes := true
	seedSignal(t, p.DB, "reopen-sig", "sess-reopen", id, "log", "tool_result", 1_000_000, &yes, nil, nil, nil)

	node2, raw := completeAndReparse(t, p, id)
	second := workitem.RollupProps(node2)

	if got := second["commits"]; got != "2" {
		t.Errorf("re-completion did not recompute: commits = %q, want %q", got, "2")
	}
	if n := strings.Count(raw, "data-rollup-commits="); n != 1 {
		t.Errorf("data-rollup-commits appears %d times; recompute must replace, not duplicate", n)
	}
	// The stale marker must be gone, not merged alongside the new metric.
	if got := second["telemetry"]; got != "" {
		t.Errorf("rollup-telemetry = %q survived a recompute that found telemetry", got)
	}
	if strings.Contains(raw, "data-rollup-telemetry") {
		t.Error("artifact still carries data-rollup-telemetry next to a computed failure rate")
	}
	if got := second["failure-rate"]; got != "0.0000" {
		t.Errorf("failure-rate = %q, want %q — one observed outcome, no failures", got, "0.0000")
	}
}

// TestRollup_NilDBDegradesToMarker covers the completion path that runs
// without a read index. Both rollup sources are derived-index reads, so
// nothing is computable — but completion still succeeds, and the artifact
// records why it has no numbers.
func TestRollup_NilDBDegradesToMarker(t *testing.T) {
	node := &models.Node{ID: "feat-nodb", Properties: map[string]any{"rollup-commits": "9", "keep": "me"}}

	workitem.ApplyRollup(node, nil, "feat-nodb")

	if got := node.Properties[workitem.RollupUnavailableKey]; got != workitem.ReasonNoReadIndex {
		t.Errorf("rollup-unavailable = %v, want %q", got, workitem.ReasonNoReadIndex)
	}
	if _, stale := node.Properties["rollup-commits"]; stale {
		t.Error("stale rollup key survived a nil-DB recompute")
	}
	if node.Properties["keep"] != "me" {
		t.Error("ApplyRollup cleared a non-rollup property")
	}
}
