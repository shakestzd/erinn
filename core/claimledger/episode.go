// Package claimledger is the durable record of which agent held which work
// item over which interval — the half of attribution that no other wipnote
// table holds.
//
// Every existing claim-bearing table (active_work_items, claims,
// sessions.active_feature_id) is single-mutable-slot CURRENT state: it answers
// "what does this agent hold right now" and forgets the moment the claim moves.
// A signal emitted at time T by agent A therefore has nothing to join against.
// This package records the intervals so that join is possible.
//
// # Storage
//
// One HTML file per ROOT session under .wipnote/claims/, structurally mirroring
// .wipnote/architecture.html: one <table>, one <tr> per record carrying data-*
// attributes plus <td data-field> cells. The directory is git-tracked, so
// history survives a cache wipe AND a fresh clone — unlike .wipnote/sessions/,
// which is gitignored runtime telemetry and would not.
//
// Sharding per root session is what keeps worktree merges conflict-free: two
// sessions never write the same file, so a merge is a union of new files rather
// than a textual conflict inside one.
//
// # Row shape
//
// One row per claim EPISODE, not per event. An episode is a time-bounded
// activity — work item, session, agent, start, end, outcome — so an interval is
// one row and needs no join to reconstruct. Rows are written on claim (open,
// no end) and updated on release (end + outcome). Heartbeat renewals produce
// NEITHER: see the package's write surface, which has no renewal entry point
// at all.
//
// # Concurrency
//
// Writes go through core/filelock.Guard: in-process mutex keyed by path plus a
// blocking cross-process flock on a sidecar. Opening an episode APPENDS at the
// tail with a torn-write check (constant cost); closing one does a
// read-modify-write and an atomic temp-then-rename. Both hold the same guard.
//
// On Windows filelock.CrossProcess is false — bug-68f3593b — so concurrent
// wipnote PROCESSES are not excluded there. Nothing in this package's
// correctness argument may assume that lock: the torn-write repair in
// appendRowLocked is what keeps a partially-written tail from corrupting or
// swallowing a row, and it is independent of the lock.
package claimledger

import (
	"fmt"
	"strings"
	"time"
)

// Outcome is the terminal disposition of a claim episode.
type Outcome string

const (
	// OutcomeCompleted — the agent finished the work item it held.
	OutcomeCompleted Outcome = "completed"
	// OutcomeReleased — the agent gave the item up deliberately without
	// completing it (explicit release / reassignment).
	OutcomeReleased Outcome = "released"
	// OutcomeAbandoned — the owning session ended while still holding the item.
	OutcomeAbandoned Outcome = "abandoned"
	// OutcomeExpired — the episode was closed by reconciliation because the
	// owning session died without ever reporting an end.
	OutcomeExpired Outcome = "expired"
)

// ValidOutcomes is the closed vocabulary of terminal outcomes.
var ValidOutcomes = []Outcome{OutcomeCompleted, OutcomeReleased, OutcomeAbandoned, OutcomeExpired}

func (o Outcome) valid() bool {
	for _, v := range ValidOutcomes {
		if o == v {
			return true
		}
	}
	return false
}

// TimeFormat is the on-disk timestamp format: RFC3339 in UTC with a FIXED nine
// fractional digits. Fixed width matters — the SQLite read index compares these
// as TEXT, and time.RFC3339Nano's variable-length fraction is not
// lexicographically ordered ("…:05.1Z" sorts after "…:05.05Z"). Values written
// in this format parse cleanly with time.RFC3339Nano.
const TimeFormat = "2006-01-02T15:04:05.000000000Z"

// FormatTime renders t in the on-disk format.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ParseTime parses an on-disk timestamp. It accepts any RFC3339 spelling so
// hand-edited or older rows still read back.
func ParseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

// Episode is one interval during which one agent held one work item.
//
// EndedAt and Outcome are zero while the episode is open. An open episode is
// queryable as open-ended — correct while its session is alive, and closed to
// OutcomeExpired by Reconcile once it is not (see store.go).
type Episode struct {
	// ID is the stable episode identity and the HTML fragment anchor: the row
	// carries id="<ID>", so any episode is addressable as
	// .wipnote/claims/<root-session>.html#<ID>.
	ID string

	WorkItemID string
	// SessionID is the session that actually holds the claim. It differs from
	// RootSessionID only for harnesses that spawn subagents into their own
	// sessions (Codex); under Claude Code subagents share the root's session ID
	// and are distinguished by AgentID.
	SessionID     string
	RootSessionID string
	// AgentID is the per-agent identity, db.AgentRootSentinel ("__root__") for
	// the top-level session owner. It is the join key for per-signal attribution.
	AgentID string

	StartedAt time.Time
	EndedAt   time.Time
	Outcome   Outcome
}

// IsOpen reports whether the episode has no recorded end.
func (e Episode) IsOpen() bool { return e.EndedAt.IsZero() }

// Validate rejects an episode that could never be joined against a signal.
func (e Episode) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("claimledger: episode missing id")
	}
	if strings.TrimSpace(e.WorkItemID) == "" {
		return fmt.Errorf("claimledger: episode %s missing work item", e.ID)
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return fmt.Errorf("claimledger: episode %s missing session", e.ID)
	}
	if strings.TrimSpace(e.AgentID) == "" {
		return fmt.Errorf("claimledger: episode %s missing agent", e.ID)
	}
	if e.StartedAt.IsZero() {
		return fmt.Errorf("claimledger: episode %s missing start", e.ID)
	}
	if !e.IsOpen() {
		if !e.Outcome.valid() {
			return fmt.Errorf("claimledger: episode %s has invalid outcome %q", e.ID, e.Outcome)
		}
		if e.EndedAt.Before(e.StartedAt) {
			return fmt.Errorf("claimledger: episode %s ends before it starts", e.ID)
		}
	} else if e.Outcome != "" {
		return fmt.Errorf("claimledger: episode %s has outcome %q but no end", e.ID, e.Outcome)
	}
	return nil
}
