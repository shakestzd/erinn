package workitem

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"

	dbpkg "github.com/shakestzd/wipnote/core/db"
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
	// ReasonNoReadIndex: completion ran without a SQLite handle. Both
	// sources are derived-index reads, so neither half is computable.
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
func ApplyRollup(node *models.Node, database *sql.DB, itemID string) {
	if node == nil {
		return
	}
	clearRollupProps(node)

	if database == nil {
		setRollupProp(node, RollupUnavailableKey, ReasonNoReadIndex)
		return
	}

	props, err := computeRollupProps(database, itemID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wipnote: rollup for %s skipped: %v\n", itemID, err)
		setRollupProp(node, RollupUnavailableKey, ReasonComputeError)
		return
	}
	for k, v := range props {
		setRollupProp(node, k, v)
	}
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

// computeRollupProps builds the full key/value set for one item.
//
// Values are strings, not numbers, so that every key renders as its own
// readable data-<key> attribute rather than folding into the data-node-props
// JSON escape hatch (renderPropAttrs only gives a key the attribute form when
// the value is a string). The artifact is meant to be greppable and
// diff-readable; a JSON blob would defeat both.
//
// The deferred recover is belt-and-braces for a hot, user-facing, lock-holding
// path: a scan panic here would otherwise take a completion down with it.
func computeRollupProps(database *sql.DB, itemID string) (props map[string]string, err error) {
	defer func() {
		if r := recover(); r != nil {
			props, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()

	w := rollupProps{out: map[string]string{}}

	stats, err := dbpkg.ItemOutcomeRollup(database, itemID)
	if err != nil {
		return nil, err
	}
	if stats.Signals == 0 {
		// The explicit unavailable marker. Every Codex and Antigravity item
		// lands here today, as does any Claude item completed with telemetry
		// off — all of which must read as "unknown", never as "clean".
		w.out[RollupTelemetryKey] = MarkerUnavailable
	} else {
		w.partial("failure-rate",
			fmt.Sprintf("%.4f", ratio(stats.Failures, stats.OutcomeObserved)),
			SourceOtelSuccess, stats.OutcomeObserved, stats.OutcomeEligible)
		w.partial("duration-ms",
			fmt.Sprintf("%d", stats.DurationMsTotal),
			SourceOtelDuration, stats.DurationRows, stats.Signals)
		w.complete("elapsed-ms",
			fmt.Sprintf("%d", stats.ElapsedMs),
			SourceOtelElapsed, int(stats.ElapsedMs))
		// A measured zero: attempts were recorded and none was a retry.
		// Present-with-value-0 is a real observation; omission is not.
		w.partial("retries",
			fmt.Sprintf("%d", stats.Retries),
			SourceOtelAttempt, stats.AttemptRows, stats.Signals)
		w.partial("cost-usd",
			fmt.Sprintf("%.4f", stats.CostUSDTotal),
			SourceOtelCost, stats.CostRows, stats.Signals)
	}

	commits, files, churn, err := gitRollup(database, itemID)
	if err != nil {
		return nil, err
	}
	if commits == 0 && files == 0 && churn.EditEvents == 0 {
		w.out[RollupGitKey] = MarkerUnavailable
	} else {
		w.complete("commits", fmt.Sprintf("%d", commits), SourceGitCommits, commits)
		w.complete("files", fmt.Sprintf("%d", files), SourceFeatureFiles, files)
		w.partial("churn-files",
			fmt.Sprintf("%d", churn.ChurnedFiles),
			SourceEditChurn, churn.FilesResolved, churn.EditEvents)
	}

	w.out[RollupComputedAtKey] = time.Now().UTC().Format(time.RFC3339)
	return w.out, nil
}

// gitRollup reads the harness-neutral half through the existing repository
// functions rather than new SQL: GetCommitsByFeature and ListFilesByFeature
// are the attribution-corrected readers the rest of the codebase already
// trusts. Both are keyed by the generic item ID column, so this works for
// bugs and spikes as well as features. Distinct counting happens here because
// git_commits is keyed (commit_hash, session_id, feature_id) and can hold the
// same commit under more than one session.
func gitRollup(database *sql.DB, itemID string) (commits, files int, churn dbpkg.ItemEditChurn, err error) {
	rows, err := dbpkg.GetCommitsByFeature(database, itemID)
	if err != nil {
		return 0, 0, churn, err
	}
	seenCommit := map[string]struct{}{}
	for _, c := range rows {
		seenCommit[c.CommitHash] = struct{}{}
	}

	fileRows, err := dbpkg.ListFilesByFeature(database, itemID)
	if err != nil {
		return 0, 0, churn, err
	}
	seenFile := map[string]struct{}{}
	for _, f := range fileRows {
		seenFile[f.FilePath] = struct{}{}
	}

	churn, err = dbpkg.ItemEditChurnStats(database, itemID)
	if err != nil {
		return 0, 0, churn, err
	}
	return len(seenCommit), len(seenFile), churn, nil
}

// rollupProps accumulates the key/source/coverage triples, enforcing
// omit-never-zero at the single point where a metric is written.
type rollupProps struct {
	out map[string]string
}

// partial writes a metric whose supporting rows are a subset of what could
// have supplied it, tagging both the source and the observed/eligible ratio.
// A metric with zero observations is omitted entirely — that is the "omit,
// never zero" rule, and it lives here so no caller can bypass it.
func (w rollupProps) partial(metric, value, source string, observed, eligible int) {
	if observed <= 0 {
		return
	}
	w.out[RollupPropPrefix+metric] = value
	w.out[RollupPropPrefix+metric+"-source"] = source
	w.out[RollupPropPrefix+metric+"-coverage"] = fmt.Sprintf("%d/%d", observed, eligible)
}

// complete writes a metric that is exhaustive by construction — every row that
// exists is the measurement, so there is no coverage ratio to state. count is
// the omission test only; it is not written.
func (w rollupProps) complete(metric, value, source string, count int) {
	if count <= 0 {
		return
	}
	w.out[RollupPropPrefix+metric] = value
	w.out[RollupPropPrefix+metric+"-source"] = source
}

func ratio(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den)
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
