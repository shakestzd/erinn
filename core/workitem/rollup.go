package workitem

import (
	"strings"

	"github.com/shakestzd/wipnote/core/models"
)

// Per-work-item outcome rollups (feat-7ee73444).
//
// At completion, a work item's telemetry and git history are collapsed into a
// handful of numbers and written into its canonical HTML through
// models.Node.Properties, which renders each attribute-safe string key as its
// own data-<key> attribute on <article>. That placement is the whole point:
// the rollup is git-tracked, survives a derived-index wipe, and is readable by
// every CLI path that already parses the artifact — no new store, no new
// write path, no reindex dependency.
//
// Two rules govern everything below.
//
// PER-METRIC PROVENANCE. No number appears without a sibling naming where it
// came from, following the success-source tagging pattern in
// observe/otel/adapter. Metrics whose underlying rows are sparse also carry a
// coverage ratio, so a reader can tell a complete measurement from a sample.
// Cost is marked degraded outright: only a minority of signals carry cost_usd
// today, so the total is a systematic under-report and ships only because the
// marker says so.
//
// OMIT, NEVER ZERO. A metric with no underlying rows is absent from the HTML
// entirely, and its absence is explained by a marker (rollup-telemetry /
// rollup-git = "unavailable") rather than by a fabricated zero. A Codex or
// Antigravity item, which emits no usable telemetry today, therefore gets the
// git half and an explicit telemetry-unavailable marker — not a 0% failure
// rate it never earned. A *measured* zero is different and is written: when
// attempts were recorded and none was a retry, rollup-retries="0" is a real
// observation, distinguishable from omission because the metric is present.

// RollupPropPrefix namespaces every rollup key inside Node.Properties. It is
// also the deletion key for recompute: Start leaves rollups alone, and the
// next Complete drops every key under this prefix before recomputing from
// full history, so re-completion after a reopen replaces rather than
// accumulates.
const RollupPropPrefix = "rollup-"

// Marker keys and values. These explain an absence; they are never emitted
// alongside the metric they excuse.
const (
	// RollupUnavailableKey is the whole-rollup escape hatch: no read index,
	// or the computation itself failed. When present it is the only rollup
	// key on the node.
	RollupUnavailableKey = RollupPropPrefix + "unavailable"
	// RollupTelemetryKey / RollupGitKey mark one half as absent while the
	// other half was computed normally.
	RollupTelemetryKey = RollupPropPrefix + "telemetry"
	RollupGitKey       = RollupPropPrefix + "git"
	// RollupComputedAtKey timestamps the computation so a stale rollup on a
	// long-reopened item is recognisable as stale.
	RollupComputedAtKey = RollupPropPrefix + "computed-at"

	// MarkerUnavailable is the value of the two half-markers.
	MarkerUnavailable = "unavailable"
	// ReasonNoReadIndex: completion ran without a derived projection. Both
	// sources are projection reads, so neither half is computable.
	ReasonNoReadIndex = "no_read_index"
	// ReasonComputeError: a query failed or panicked. The completion still
	// succeeded — this marker is the durable, git-tracked record that it
	// did so without a rollup.
	ReasonComputeError = "compute_error"
)

// Source tokens. Each names the column or table the number came from, and
// appends a ":caveat" suffix where the number does not mean quite what its
// key suggests.
const (
	// SourceOtelSuccess: the otel_signals.success column, counted only over
	// the (kind, canonical) pairs where an outcome is semantically defined
	// (see itemOutcomeEligibleSQL in core/db/rollup_repo.go).
	SourceOtelSuccess = "otel_success"
	// SourceOtelDuration: SUM(duration_ms). Concurrent agents emit
	// overlapping spans, so this is machine time burned, not elapsed time —
	// the caveat is in the token because the key alone would mislead.
	SourceOtelDuration = "otel_duration_ms:summed_overlapping_spans"
	// SourceOtelElapsed: last signal minus first signal. This is the real
	// elapsed wall clock, with no coverage caveat: it needs only that the
	// item has signals at all.
	SourceOtelElapsed = "otel_ts_micros:first_to_last"
	// SourceOtelAttempt: rows whose attempt number is past the first try.
	SourceOtelAttempt = "otel_attempt"
	// SourceOtelCost: KNOWN DEGRADED. cost_usd is populated on a minority of
	// signals, so the total under-reports by roughly 4x at current density.
	// It ships because this token says so; strip the token and the number
	// becomes a lie.
	SourceOtelCost = "otel_cost_usd:degraded_under_report"
	// SourceGitCommits / SourceFeatureFiles: harness-neutral git attribution,
	// complete by construction — every row is the metric, so neither carries
	// a coverage ratio.
	SourceGitCommits   = "git_commits"
	SourceFeatureFiles = "feature_files"
	// SourceEditChurn: per-file rewrite counts from edit tool calls. NOT
	// commit-level churn — the derived index has no commit-to-file join, so
	// "the same file touched more than once" is only observable at edit
	// granularity. The caveat is in the token so nobody reads it as commits.
	SourceEditChurn = "agent_events_edit_multiplicity:not_commit_level"
)

// ApplyRollup recomputes itemID's outcome rollup and installs it on node,
// replacing any rollup left by an earlier completion.
//
// It is called from inside Collection.Complete's mutate closure, before the
// HTML write and while the per-item write lock is held, so the rollup lands in
// the same read-modify-write window as the status transition — one locked
// write, no second write path, no window where the artifact says "done"
// without its numbers.
//
// It never returns an error and never panics out. A rollup failure degrades to
// a marked absence: tonight's history is full of gates that punished users for
// infrastructure state, and a completion blocked because a derived index was
// mid-rebuild would be one more. The failure is still loud — a stderr warning
// for the human running the command, and RollupUnavailableKey persisted into
// the artifact so the gap is visible long after the terminal scrolls away.
func ApplyRollup(node *models.Node, itemID string) {
	if node == nil {
		return
	}
	clearRollupProps(node)
	_ = itemID
	setRollupProp(node, RollupUnavailableKey, ReasonNoReadIndex)
}

// clearRollupProps drops every rollup-prefixed key. This is what makes
// recompute idempotent: a reopened item that is completed again gets a rollup
// computed from full history, not the old one merged with the new.
func clearRollupProps(node *models.Node) {
	for k := range node.Properties {
		if strings.HasPrefix(k, RollupPropPrefix) {
			delete(node.Properties, k)
		}
	}
}

func setRollupProp(node *models.Node, key, value string) {
	if node.Properties == nil {
		node.Properties = map[string]any{}
	}
	node.Properties[key] = value
}

// RollupProps returns node's rollup keys, sorted, with the prefix stripped.
// Presentation helper for `wipnote <type> show`; returns nil when the node
// carries no rollup.
func RollupProps(node *models.Node) map[string]string {
	if node == nil || len(node.Properties) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range node.Properties {
		if !strings.HasPrefix(k, RollupPropPrefix) {
			continue
		}
		if s, ok := v.(string); ok {
			out[strings.TrimPrefix(k, RollupPropPrefix)] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
