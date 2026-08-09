// Package sessionledger is the canonical, git-tracked record that a session
// EXISTED — the durable half of a session's identity, separated from its
// ephemeral telemetry.
//
// # Why it exists
//
// Work items are permanent and git-tracked; sessions are ephemeral and
// prunable. The edge bridging them (item --implemented_in--> session) is
// declared in the permanent artifact, so when the session's raw events age out
// the declaration is still true but the target resolves to nothing. Measured on
// this repo (bug-10e166d8): canonical HTML declared 833 implemented_in edges
// across 54 distinct session ids, of which 3 still resolved. Provenance was
// decaying on a retention timer.
//
// The tombstone policy (core/graph/tombstone.go) kept those edges from being
// erased. This package removes the cause: a row is written when the session
// STARTS, so the target of an implemented_in edge is durable before any edge
// can name it, and stays durable after the events are pruned, archived, or the
// cache is wiped. Tombstones remain only for sessions that predate the ledger
// and left no archive behind.
//
// # Storage — ONE file, not shards
//
// .wipnote/sessions-ledger.html: one <table>, one <tr> per ROOT session, each
// row carrying id="<session-id>" so any session is addressable as
// .wipnote/sessions-ledger.html#<session-id>.
//
// This diverges deliberately from core/claimledger, which shards per root
// session to keep concurrent worktrees off the same file. Three reasons the
// same answer does not apply here:
//
//  1. Write rate. A claim shard absorbs one row per claim and one rewrite per
//     release — many per session. This ledger takes exactly ONE row per session,
//     written once at start.
//  2. Fragment addressability. Sharding a per-session record can only shard BY
//     session, which degenerates to one single-row file per session — a
//     git-tracked mirror of the gitignored sessions directory, with no table
//     left to read and no stable fragment url that a reader can construct from
//     a session id alone.
//  3. Precedent. .wipnote/architecture.html is the established shared canonical
//     table with the same append-a-row-at-the-tail write shape, and this repo
//     already accepts its merge behaviour.
//
// The reader globs nothing, so adding shards later would be additive rather
// than a format break.
//
// # Root sessions only
//
// Subagents get synthetic sessions rows keyed by agent id (core/hooks/
// subagent_start.go). Those ids are bookkeeping, never edge targets — every
// dangling target measured in bug-10e166d8 was a root-session id — so admitting
// them would fill the ledger with stubs for no gain. The SessionStart writer
// gates on IsSubagent.
//
// # Concurrency
//
// Writes go through core/filelock.Guard: an in-process mutex keyed by path plus
// a blocking cross-process flock on a sidecar. Open APPENDS at the tail with a
// torn-write check (constant cost); Close and Enrich do a read-modify-write and
// an atomic temp-then-rename.
//
// On Windows filelock.CrossProcess is false (bug-68f3593b), so concurrent
// wipnote PROCESSES are not excluded there. Nothing here may assume that lock:
// the torn-write repair in appendRowLocked is what keeps a partially-written
// tail from swallowing the row after it, and it is independent of the lock.
package sessionledger

import (
	"fmt"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
)

// TimeFormat is the on-disk timestamp format: RFC3339 in UTC with a FIXED nine
// fractional digits, matching core/claimledger. Fixed width matters because the
// SQLite read index compares these as TEXT and time.RFC3339Nano's
// variable-length fraction is not lexicographically ordered.
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

// EndSource names where a row's EndedAt came from.
//
// It exists because several passes can supply an end and they are NOT equally
// trustworthy, and without recording which one won there is no sound way to
// correct a bad value later. That was not hypothetical: the first backfill in
// this repo stamped the archive tarball's mtime as the end, turning a 2-day
// session into a 47-day one, and no rule available at repair time could
// distinguish those rows from good ones. The only rule that looked general —
// "an end later than the last recorded event is wrong" — is FALSE in normal
// operation, because SessionEnd legitimately fires after the last tool event.
// Provenance replaces that guesswork with a fact.
//
// Repair uses this and nothing else: a value is replaced only when the incoming
// source outranks the recorded one. No thresholds, no heuristics.
type EndSource string

const (
	// EndSourceUnknown — no provenance recorded. Rows written before this field
	// existed read as this, which is why it ranks lowest: a row that cannot say
	// where its end came from is exactly the row repair should re-derive.
	EndSourceUnknown EndSource = ""
	// EndSourceArchiveMtime — the archive tarball's file mtime. This is when
	// retention CREATED the archive, not when anything stopped, so it is a loose
	// upper bound and never a real end. Used only when a session's events cannot
	// be read at all.
	EndSourceArchiveMtime EndSource = "archive-mtime"
	// EndSourceLastActivity — the last activity observed for the session: the
	// final timestamp in its events.ndjson, or that file's mtime at archive time.
	// A true end-of-activity signal, though for a collector log it bounds the
	// activity recorded under the session id rather than one interactive session.
	EndSourceLastActivity EndSource = "last-activity"
	// EndSourceSessionRecord — data-ended-at from the canonical session HTML,
	// written by the session lifecycle itself.
	EndSourceSessionRecord EndSource = "session-record"
	// EndSourceLiveClose — stamped by the SessionEnd hook as the session ended.
	// The most trustworthy end there is, and the one repair must never move.
	EndSourceLiveClose EndSource = "live-close"
)

// endSourceRank orders sources by trustworthiness. A higher rank may replace a
// lower one; equal or lower never overwrites.
var endSourceRank = map[EndSource]int{
	EndSourceUnknown:       0,
	EndSourceArchiveMtime:  1,
	EndSourceLastActivity:  2,
	EndSourceSessionRecord: 3,
	EndSourceLiveClose:     4,
}

// Rank returns the source's trust level. An unrecognised value ranks lowest, so
// a hand-edited or future spelling can never silently outrank a known source.
func (s EndSource) Rank() int { return endSourceRank[s] }

// OutranksRecorded reports whether s is trustworthy enough to replace recorded.
func (s EndSource) OutranksRecorded(recorded EndSource) bool {
	return s.Rank() > recorded.Rank()
}

// Record is one root session's durable identity.
//
// EndedAt is zero while the session is running. Everything past StartedAt is
// enrichment: absent fields mean "not known", never "known to be empty", which
// is what lets a backfilled row (id + end time from an archive tarball) coexist
// with a live row without either overwriting the other.
type Record struct {
	// SessionID is the session's identity, the HTML fragment anchor, and the id
	// that work-item edges name. It must satisfy graph.IsSessionShapedID — the
	// SAME predicate the target-validity gate applies — so the ledger can never
	// make an id valid that the gate would not have recognised as a session.
	SessionID string

	// Harness is the CLI that ran the session (claude-code, codex, …).
	Harness string
	// ProjectDir is the repo-relative project path, normalized by core/paths so
	// rows stay stable across worktrees and machines. Absolute host paths must
	// never reach a canonical artifact.
	ProjectDir string

	StartedAt time.Time
	EndedAt   time.Time
	// EndSource records which pass supplied EndedAt, so a later repair can
	// replace a weakly-grounded end without guessing. Zero while the row is open.
	EndSource EndSource

	// ArchivePath is the repo-relative path of the tarball holding this
	// session's raw events, set when retention archives them. It is the bridge
	// back to the detail this row deliberately does not carry.
	ArchivePath string
	// Events is the distilled event total when it was cheaply available at
	// archive time; zero means unknown, never "no events".
	Events int
}

// IsOpen reports whether the session has no recorded end.
func (r Record) IsOpen() bool { return r.EndedAt.IsZero() }

// Validate rejects a record that could not serve as an edge target.
func (r Record) Validate() error {
	id := strings.TrimSpace(r.SessionID)
	if id == "" {
		return fmt.Errorf("sessionledger: record missing session id")
	}
	if !graph.IsSessionShapedID(id) {
		return fmt.Errorf("sessionledger: %q is not a session-shaped id", id)
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("sessionledger: session %s missing start", id)
	}
	if !r.IsOpen() && r.EndedAt.Before(r.StartedAt) {
		return fmt.Errorf("sessionledger: session %s ends before it starts", id)
	}
	return nil
}

// Label is the human-readable title a reader shows for this session.
//
// It is load-bearing, not cosmetic. Session titles are resolved from the
// sessions TABLE in three independent readers (resolveNodes in
// core/graph/querybuilder.go, resolveProvenanceNode, loadGraphNodes), so a
// session that becomes VALID from the ledger without gaining a title renders as
// an unlabelled node — strictly worse than the tombstone it replaced, which at
// least announced itself. This is the label that projection writes into
// sessions.title so a ledger-only session renders as something a reader can
// read. See the hazard card edge-target-validity-and-renderability-are-separate.
func (r Record) Label() string {
	short := r.SessionID
	if len(short) > 8 {
		short = short[:8]
	}
	harness := strings.TrimSpace(r.Harness)
	if harness == "" {
		harness = "session"
	}
	when := r.StartedAt
	if when.IsZero() {
		when = r.EndedAt
	}
	if when.IsZero() {
		return harness + " · " + short
	}
	return harness + " " + when.UTC().Format("2006-01-02 15:04") + " · " + short
}
