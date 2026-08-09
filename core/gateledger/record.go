// Package gateledger is the canonical, git-tracked record that a quality gate
// RAN — the durable home for the facts the completion gate decides on.
//
// # Why it exists
//
// A gate record is decision-affecting data: `wipnote feature complete` refuses
// to complete a work item that has no valid passing gate record. Until this
// package existed those records lived ONLY in the derived SQLite index, which
// sits in the operating system's cache directory and may be purged at any time
// (bug-550c1cd8). A purge therefore silently changed whether completions were
// permitted, and nothing could rebuild the records — measured on this repo:
// seventy-five records across forty-one work items with no canonical source.
//
// # Why this is the simplest of the three ledgers
//
// A gate run is a COMPLETED, IMMUTABLE fact the moment it is written. There is
// no open-close lifecycle (core/claimledger), no later enrichment
// (core/sessionledger), and nothing ever updates a row. That is why this package
// has an append path and no read-modify-write path at all: the atomic
// temp-then-rename writer both siblings need has no caller here.
//
// # Storage — ONE file, not shards
//
// .wipnote/gate-ledger.html: one <table>, one <tr> per gate run, each row
// carrying id="<record-id>" so any record is addressable as
// .wipnote/gate-ledger.html#gr-….
//
// This follows core/sessionledger and diverges deliberately from
// core/claimledger's per-root-session shards, for three reasons:
//
//  1. Write rate. Claim shards absorb a row per claim plus a rewrite per release
//     — many per session, from concurrent worktrees. Gate runs are orders of
//     magnitude rarer: one row per `wipnote check --gate`, seventy-five rows in
//     this repo's entire history.
//  2. Query shape. The completion gate's cross-session fallback asks "is there a
//     recent passing record for THIS WORK ITEM, from any session". Sharding by
//     session would force every completion to glob and parse every shard to
//     answer it; a work-item shard would instead split the session-scoped lookup
//     the same way. One file answers both in one parse.
//  3. Precedent. .wipnote/architecture.html and .wipnote/sessions-ledger.html are
//     already shared canonical tables with this same append-at-the-tail shape,
//     and this repo already accepts their merge behaviour.
//
// The reader globs nothing, so adding shards later would be additive rather than
// a format break.
//
// # Concurrency
//
// Writes go through core/filelock.Guard: an in-process mutex keyed by path plus a
// blocking cross-process flock on a sidecar. Append writes at the tail with a
// torn-write check, in constant time.
//
// On Windows filelock.CrossProcess is false (bug-68f3593b), so concurrent
// wipnote PROCESSES are not excluded there. Nothing here may assume that lock:
// the torn-write repair in appendRowLocked is what keeps a partially-written tail
// from swallowing the row after it, and it is independent of the lock.
package gateledger

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FileName is the git-tracked canonical ledger, a sibling of
// .wipnote/architecture.html and .wipnote/sessions-ledger.html.
const FileName = "gate-ledger.html"

// TimeFormat is the on-disk timestamp format: RFC3339 in UTC with a FIXED nine
// fractional digits, matching core/claimledger and core/sessionledger. Fixed
// width matters because the SQLite read index compares these as TEXT and
// time.RFC3339Nano's variable-length fraction is not lexicographically ordered.
//
// Nine digits is also exactly Go's nanosecond resolution, which is what lets a
// record's Signature survive the round trip: the signed payload formats
// CheckedAt with time.RFC3339Nano, so the checksum only re-verifies if the
// on-disk form loses no precision.
const TimeFormat = "2006-01-02T15:04:05.000000000Z"

// StatusPass and StatusFail are the only statuses a record may carry.
//
// They mirror the derived index's CHECK(status IN ('pass','fail')) constraint
// exactly. A gate run that never executed anything (no supported manifest, no
// approved guard profile) records StatusFail, never a third value — see
// gate.RunSession and bug-1b2b1529: a skip must never be mistakable for a pass.
// Adding a status here without also relaxing that constraint would produce
// canonical rows the index silently refuses.
const (
	StatusPass = "pass"
	StatusFail = "fail"
)

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

// NewRecordID mints a fragment-safe gate record identifier.
func NewRecordID() string { return "gr-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16] }

// Record is one gate run: a completed, immutable fact.
//
// Every field is decision-relevant, and the two easiest to mistake for
// decoration are not:
//
//   - AllowlistHitsJSON is the REASON a failure was forgiven.
//     gate.GateCommandAllowlisted converts a FAILED gate command into a pass when
//     its failure matches an allowlist entry, so the hit detail names which
//     entries did the forgiving. Only AllowlistHitCount is consumed today (session
//     adherence renders "passed with N allowlist hit(s)"), but without the detail
//     every historical pass is indistinguishable from a clean one, permanently —
//     and nobody can later ask whether a pass was forgiven for a reason since
//     fixed, or whether an allowlist entry has gone stale.
//   - ProfileSignature is the guard profile the commands came from, which is what
//     lets drift reporting say "this passed against a contract that has since
//     changed" rather than just "this passed".
type Record struct {
	// ID is the record's identity and the HTML fragment anchor.
	ID string

	// WorkItemID may be empty: a gate can run with no resolvable active work
	// item, and the run is still a fact worth recording.
	WorkItemID string
	// SessionID is required — a record with no session cannot be matched to the
	// session-scoped lookup the completion gate performs first.
	SessionID string
	Harness   string

	ProjectType string
	GateCommand string
	Status      string
	CheckedAt   time.Time

	// Signature is the record-integrity checksum over SignatureInput. The
	// completion gate rejects a record whose signature does not re-verify, so a
	// hand-edited row cannot forge a pass.
	Signature string

	// GuardsRunJSON is a JSON array of the guard names that ran. "[]" when none
	// (autodetection or a no-op run).
	GuardsRunJSON string
	Source        string
	OutputSummary string
	// ProfileSignature is the guardprofile.Signature of the APPROVED profile that
	// supplied this gate's commands, or "" when the gate ran via manifest
	// autodetection. It is provenance, deliberately NOT part of the Signature.
	ProfileSignature string

	AllowlistHitsJSON string
	AllowlistHitCount int
}

// SignatureInput is the exact field set the record-integrity checksum covers.
//
// It exists as a named type rather than a method body because TWO
// representations of a gate record must agree on it byte for byte — this
// package's canonical Record and the derived index's db.GateRecord. A signature
// computed over one and verified against the other is the whole point, so the
// algorithm lives in exactly one place and both build this struct to reach it.
//
// ProfileSignature and GuardsRunJSON are provenance and are excluded on purpose:
// re-approving a guard profile must not retroactively invalidate the signature
// of a record that already ran.
type SignatureInput struct {
	SessionID         string
	WorkItemID        string
	Harness           string
	ProjectType       string
	GateCommand       string
	Status            string
	CheckedAt         time.Time
	AllowlistHitsJSON string
	Source            string
	OutputSummary     string
}

// Payload renders the signable bytes. The field order and the "\n" separator are
// the format — changing either invalidates every record ever written.
func (in SignatureInput) Payload() string {
	return strings.Join([]string{
		in.SessionID,
		in.WorkItemID,
		in.Harness,
		in.ProjectType,
		in.GateCommand,
		in.Status,
		in.CheckedAt.UTC().Format(time.RFC3339Nano),
		in.AllowlistHitsJSON,
		in.Source,
		in.OutputSummary,
	}, "\n")
}

// Sum is the checksum over Payload.
func (in SignatureInput) Sum() string {
	sum := sha256.Sum256([]byte(in.Payload()))
	return fmt.Sprintf("%x", sum[:])
}

// SignatureInput returns the fields this record's signature covers.
func (r Record) SignatureInput() SignatureInput {
	return SignatureInput{
		SessionID:         r.SessionID,
		WorkItemID:        r.WorkItemID,
		Harness:           r.Harness,
		ProjectType:       r.ProjectType,
		GateCommand:       r.GateCommand,
		Status:            r.Status,
		CheckedAt:         r.CheckedAt,
		AllowlistHitsJSON: r.AllowlistHitsJSON,
		Source:            r.Source,
		OutputSummary:     r.OutputSummary,
	}
}

// ComputeSignature returns the checksum this record should carry.
func (r Record) ComputeSignature() string { return r.SignatureInput().Sum() }

// EnsureSignature stamps the record with its computed checksum.
func (r *Record) EnsureSignature() { r.Signature = r.ComputeSignature() }

// SignatureValid reports whether the recorded checksum re-verifies.
func (r Record) SignatureValid() bool {
	if strings.TrimSpace(r.Signature) == "" {
		return false
	}
	return r.Signature == r.ComputeSignature()
}

// Passed reports whether this run permits a completion.
func (r Record) Passed() bool { return r.Status == StatusPass }

// Normalize fills the defaults a caller may legitimately leave zero, so that a
// record built by hand and one built by the gate runner serialize identically.
// It never invents a status or a session — those are the caller's facts.
func (r *Record) Normalize() {
	if r.ID == "" {
		r.ID = NewRecordID()
	}
	if r.CheckedAt.IsZero() {
		r.CheckedAt = time.Now().UTC()
	}
	if r.AllowlistHitsJSON == "" {
		r.AllowlistHitsJSON = "[]"
	}
	if r.GuardsRunJSON == "" {
		r.GuardsRunJSON = "[]"
	}
	// AllowlistHitCount is deliberately NOT derived from AllowlistHitsJSON. A
	// backfilled row from the legacy index knows its count but has no detail to
	// recount, and recomputing would silently rewrite that count to zero.
}

// Validate rejects a record that could not serve as gate evidence.
func (r Record) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("gateledger: record missing id")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return fmt.Errorf("gateledger: record %s missing session id", r.ID)
	}
	if r.Status != StatusPass && r.Status != StatusFail {
		return fmt.Errorf("gateledger: record %s has invalid status %q", r.ID, r.Status)
	}
	if r.CheckedAt.IsZero() {
		return fmt.Errorf("gateledger: record %s missing checked-at", r.ID)
	}
	return nil
}
