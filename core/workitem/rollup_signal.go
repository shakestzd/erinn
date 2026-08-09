package workitem

import (
	"strconv"

	"github.com/shakestzd/wipnote/core/models"
)

// ItemRollupSignal is a read-only, consumer-facing view over a node's rollup
// properties (feat-7ee73444, rollup.go). It exists so callers outside this
// package — recommend and compliance auto (feat-f9118b9c) — can reason about
// an item's historical outcome without re-deriving anything from otel_signals
// or git tables, and without collapsing distinct metrics into one composite
// score: cost is deliberately not surfaced here at all (it ships
// SourceOtelCost-tagged as ":degraded_under_report", and a consumer that
// cannot represent that caveat should not consume the number).
//
// Every metric keeps its own Has* presence flag. A false flag means the
// metric was never measured — it must never be read as a clean/healthy
// result, and callers must not substitute a zero value in its place. See
// rollup.go's "OMIT, NEVER ZERO" doc comment; this type is the read side of
// that same rule.
type ItemRollupSignal struct {
	// Measured is true only when an outcome rollup actually ran for this item
	// (ApplyRollup completed without hitting the whole-rollup escape hatch).
	// False covers BOTH "never completed" and "rollup computation failed" —
	// a consumer cannot and must not try to tell those apart; both mean "no
	// evidence", not "clean".
	Measured bool

	HasFailureRate      bool
	FailureRate         float64
	FailureRateSource   string
	FailureRateCoverage string

	HasRetries      bool
	Retries         int
	RetriesCoverage string

	HasChurnFiles      bool
	ChurnFiles         int
	ChurnFilesCoverage string

	// TelemetryUnavailable / GitUnavailable mirror the rollup-telemetry /
	// rollup-git markers: that half of the rollup was never computable for
	// this item (e.g. a harness that emits no telemetry), as opposed to
	// computed-and-clean.
	TelemetryUnavailable bool
	GitUnavailable       bool

	// ComputedAt is the rollup-computed-at timestamp, RFC3339. Empty when
	// Measured is false.
	ComputedAt string
}

// RollupSignalFor reads node's rollup properties, if any, into an
// ItemRollupSignal. A nil node, a node with no rollup properties, or a node
// whose rollup hit the whole-rollup escape hatch (rollup-unavailable) all
// return the zero value: Measured is false and every metric is absent. It
// never returns an error — a value that failed to parse (which should not
// happen against artifacts this package itself wrote) is treated the same as
// an absent one rather than surfaced as a fault, consistent with the rest of
// this type's "absence, not zero" contract.
func RollupSignalFor(node *models.Node) ItemRollupSignal {
	var sig ItemRollupSignal

	props := RollupProps(node)
	if props == nil {
		return sig
	}
	if props["unavailable"] != "" {
		// The whole-rollup escape hatch: no read index or a compute error.
		// Every other rollup key is absent whenever this one is set (see
		// ApplyRollup), so there is nothing else to read.
		return sig
	}

	sig.ComputedAt = props["computed-at"]
	sig.Measured = sig.ComputedAt != ""
	sig.TelemetryUnavailable = props["telemetry"] == MarkerUnavailable
	sig.GitUnavailable = props["git"] == MarkerUnavailable

	if v, ok := props["failure-rate"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			sig.HasFailureRate = true
			sig.FailureRate = f
			sig.FailureRateSource = props["failure-rate-source"]
			sig.FailureRateCoverage = props["failure-rate-coverage"]
		}
	}
	if v, ok := props["retries"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			sig.HasRetries = true
			sig.Retries = n
			sig.RetriesCoverage = props["retries-coverage"]
		}
	}
	if v, ok := props["churn-files"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			sig.HasChurnFiles = true
			sig.ChurnFiles = n
			sig.ChurnFilesCoverage = props["churn-files-coverage"]
		}
	}

	return sig
}

// Thrashed reports whether sig carries any measured signal of a prior rough
// run: a nonzero failure rate, a retry, or a churned file. It is a
// convenience predicate over already-distinct fields, not a new metric —
// callers that need the specifics should read the individual Has*/value
// fields rather than branch on this alone. A false result means either
// "measured clean" or "not measured"; callers that need to tell those apart
// must check Measured too.
func (sig ItemRollupSignal) Thrashed() bool {
	return (sig.HasFailureRate && sig.FailureRate > 0) ||
		(sig.HasRetries && sig.Retries > 0) ||
		(sig.HasChurnFiles && sig.ChurnFiles > 0)
}
