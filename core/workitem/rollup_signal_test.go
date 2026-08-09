package workitem_test

import (
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// TestRollupSignalFor_NilAndEmpty pins the absence side: a node with no
// rollup at all must report Measured=false and Thrashed=false, not a
// zero-valued "clean" reading. This is the case every todo item and every
// not-yet-completed item hits, and it must never be indistinguishable from
// "measured, zero failures".
func TestRollupSignalFor_NilAndEmpty(t *testing.T) {
	if sig := workitem.RollupSignalFor(nil); sig.Measured || sig.Thrashed() {
		t.Errorf("nil node: got Measured=%v Thrashed=%v, want both false", sig.Measured, sig.Thrashed())
	}

	empty := &models.Node{ID: "feat-empty"}
	sig := workitem.RollupSignalFor(empty)
	if sig.Measured {
		t.Error("node with no properties: Measured=true, want false")
	}
	if sig.HasFailureRate || sig.HasRetries || sig.HasChurnFiles {
		t.Error("node with no properties reported a measured metric")
	}
}

// TestRollupSignalFor_WholeRollupUnavailable covers the escape hatch: a node
// whose completion ran with no read index or hit a compute error must read
// identically to "never measured", even though the node DOES carry a rollup
// key (rollup-unavailable). Absence must never be conflated with health, and
// this is the case most likely to be miscoded as "has data" by a naive
// len(props) > 0 check.
func TestRollupSignalFor_WholeRollupUnavailable(t *testing.T) {
	node := &models.Node{
		ID: "feat-nodb",
		Properties: map[string]any{
			"rollup-unavailable": "no_read_index",
		},
	}
	sig := workitem.RollupSignalFor(node)
	if sig.Measured {
		t.Error("rollup-unavailable node: Measured=true, want false")
	}
	if sig.Thrashed() {
		t.Error("rollup-unavailable node: Thrashed=true, want false")
	}
}

// TestRollupSignalFor_MixedHalves is the reopen shape from
// TestRollup_GitHalfWithoutTelemetry in rollup_test.go: git data present,
// telemetry half marked unavailable. The signal must expose Measured=true
// (a rollup DID run) while keeping every telemetry-derived metric absent —
// a consumer must not read TelemetryUnavailable as "zero failures".
func TestRollupSignalFor_MixedHalves(t *testing.T) {
	node := &models.Node{
		ID: "feat-git-only",
		Properties: map[string]any{
			"rollup-telemetry":   "unavailable",
			"rollup-commits":     "2",
			"rollup-computed-at": "2026-08-09T00:00:00Z",
		},
	}
	sig := workitem.RollupSignalFor(node)
	if !sig.Measured {
		t.Error("git-only rollup: Measured=false, want true — a rollup did run")
	}
	if !sig.TelemetryUnavailable {
		t.Error("git-only rollup: TelemetryUnavailable=false, want true")
	}
	if sig.HasFailureRate {
		t.Error("git-only rollup: HasFailureRate=true despite telemetry-unavailable marker")
	}
	if sig.Thrashed() {
		t.Error("git-only rollup: Thrashed=true with no measured outcome metric")
	}
}

// TestRollupSignalFor_PresentMetrics is the sharp positive case: measured
// values round-trip with their source and coverage siblings attached, and
// Thrashed reports true for a nonzero failure rate.
func TestRollupSignalFor_PresentMetrics(t *testing.T) {
	node := &models.Node{
		ID: "feat-thrashed",
		Properties: map[string]any{
			"rollup-failure-rate":          "0.1538",
			"rollup-failure-rate-source":   workitem.SourceOtelSuccess,
			"rollup-failure-rate-coverage": "13/575",
			"rollup-retries":               "3",
			"rollup-retries-coverage":      "250/1507",
			"rollup-churn-files":           "2",
			"rollup-churn-files-coverage":  "5/10",
			"rollup-computed-at":           "2026-08-09T00:00:00Z",
		},
	}
	sig := workitem.RollupSignalFor(node)
	if !sig.Measured {
		t.Fatal("expected Measured=true")
	}
	if !sig.HasFailureRate || sig.FailureRate != 0.1538 {
		t.Errorf("FailureRate = %v (has=%v), want 0.1538", sig.FailureRate, sig.HasFailureRate)
	}
	if sig.FailureRateSource != workitem.SourceOtelSuccess {
		t.Errorf("FailureRateSource = %q, want %q", sig.FailureRateSource, workitem.SourceOtelSuccess)
	}
	if sig.FailureRateCoverage != "13/575" {
		t.Errorf("FailureRateCoverage = %q, want %q", sig.FailureRateCoverage, "13/575")
	}
	if !sig.HasRetries || sig.Retries != 3 {
		t.Errorf("Retries = %v (has=%v), want 3", sig.Retries, sig.HasRetries)
	}
	if !sig.HasChurnFiles || sig.ChurnFiles != 2 {
		t.Errorf("ChurnFiles = %v (has=%v), want 2", sig.ChurnFiles, sig.HasChurnFiles)
	}
	if !sig.Thrashed() {
		t.Error("expected Thrashed=true for a nonzero failure rate")
	}
}

// TestRollupSignalFor_MeasuredZeroIsNotThrashed pins the counterpart: a real
// measured zero (attempts recorded, none failed or retried) is a genuine
// clean reading, distinct from absence, and must not trip Thrashed.
func TestRollupSignalFor_MeasuredZeroIsNotThrashed(t *testing.T) {
	node := &models.Node{
		ID: "feat-clean",
		Properties: map[string]any{
			"rollup-failure-rate":          "0.0000",
			"rollup-failure-rate-source":   workitem.SourceOtelSuccess,
			"rollup-failure-rate-coverage": "5/5",
			"rollup-retries":               "0",
			"rollup-retries-coverage":      "5/5",
			"rollup-computed-at":           "2026-08-09T00:00:00Z",
		},
	}
	sig := workitem.RollupSignalFor(node)
	if !sig.Measured {
		t.Fatal("expected Measured=true")
	}
	if !sig.HasFailureRate || sig.FailureRate != 0 {
		t.Errorf("FailureRate = %v (has=%v), want 0 (measured)", sig.FailureRate, sig.HasFailureRate)
	}
	if sig.Thrashed() {
		t.Error("measured zero failure rate and zero retries must not read as thrashed")
	}
}
