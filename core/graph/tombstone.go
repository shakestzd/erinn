package graph

import "regexp"

// EdgeMetaTombstoned is the graph_edges.metadata key that marks an edge whose
// canonical declaration survives but whose TARGET no longer resolves to any
// indexed node.
//
// It exists because work items and sessions have different lifetimes. A work
// item is permanent and git-tracked; a session is ephemeral and prunable. The
// edge bridging them (item --implemented_in--> session) is declared in the
// permanent artifact, so when the session ages out the declaration is still
// true — the work really was implemented in that session — but the target is
// gone. Dropping the edge would erase a provenance fact that canonical HTML
// still asserts (bug-10e166d8: 746 of 828 declared implemented_in edges were
// being erased exactly this way).
//
// The value names the node kind the target was expected to be, so a reader can
// say "pruned session" rather than "missing something". Today the policy admits
// only session-shaped targets — see IsSessionShapedID — so the value is always
// EdgeTombstoneSession.
//
// Only the target may be tombstoned. An edge whose SOURCE does not resolve is
// not a canonical declaration at all and is still purged.
const EdgeMetaTombstoned = "tombstoned"

// EdgeTombstoneSession is the EdgeMetaTombstoned value for a target that is
// session-shaped but absent from the sessions table.
const EdgeTombstoneSession = "session"

// sessionIDPattern matches the two session-identifier shapes that occur in
// practice, with an optional "sess-" prefix for the spelling `wipnote lineage`
// accepts:
//
//	8-4-4-4-12 dashed hex — the RFC 4122 textual UUID, 36 chars
//	28 undashed hex chars — the shorter form other harnesses emit
//
// Both were measured against this repo's canonical store: of 173 distinct
// implemented_in targets declared in work-item HTML, 156 are dashed UUIDs and
// 16 are the 28-hex form, and .wipnote/sessions/ currently holds live records
// in BOTH shapes. Covering only the UUID would have left the format most of
// the on-disk sessions actually use to keep losing provenance silently — the
// same defect, narrowed rather than fixed.
//
// The check is otherwise deliberately strict, because it is the whole boundary
// between "target is a pruned session, keep the edge as a tombstone" and
// "target is a dangling reference, drop the edge". One declared target,
// `c4efb206`, is 8 bare hex chars and is intentionally NOT admitted: at that
// length the token is indistinguishable from an abbreviated commit SHA, and a
// pattern loose enough to catch it would resurrect the dangling-reference class
// the gate exists to refuse.
var sessionIDPattern = regexp.MustCompile(
	`^(?:sess-)?(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9a-fA-F]{28})$`,
)

// IsSessionShapedID reports whether id has the textual shape of a session
// identifier. It says nothing about whether that session exists — that is the
// caller's validity check, and the combination of "session-shaped" plus "does
// not resolve" is precisely what makes an edge a tombstone.
//
// Work-item ids (feat-/bug-/spk-/trk-/plan-/spec-) never match, so an edge
// pointing at a work item that is not in the index stays a genuine dangling
// reference and is still dropped.
//
// Shape is the only signal available here. The canonical HTML does record the
// target's kind structurally — the anchor href is `../sessions/<id>.html` — but
// htmlparse discards the directory when it derives TargetID (core/htmlparse/
// parser.go), so nothing downstream of the parser can see it. Carrying that
// through would be the more robust gate and is the natural next step if a
// third id format appears.
func IsSessionShapedID(id string) bool {
	return sessionIDPattern.MatchString(id)
}

// MarkEdgeTombstoned returns a copy of props carrying the tombstone marker.
// The input map is never mutated: it belongs to the parsed HTML node and is
// reused across passes.
func MarkEdgeTombstoned(props map[string]string) map[string]string {
	marked := make(map[string]string, len(props)+1)
	for k, v := range props {
		marked[k] = v
	}
	marked[EdgeMetaTombstoned] = EdgeTombstoneSession
	return marked
}

// EdgeTargetDisposition is what the target-validity gate decides about one
// declared edge.
type EdgeTargetDisposition int

const (
	// EdgeTargetLive — the target resolves to an indexed node. Index the edge
	// exactly as declared.
	EdgeTargetLive EdgeTargetDisposition = iota
	// EdgeTargetTombstoned — the target does not resolve but is session-shaped.
	// Index the edge with the tombstone marker so provenance outlives session
	// retention.
	EdgeTargetTombstoned
	// EdgeTargetDangling — the target does not resolve and is not session-shaped.
	// Drop the edge; it is a reference to something that never existed or was
	// deleted outright.
	EdgeTargetDangling
)

// ClassifyEdgeTarget applies the target-validity gate to one declared edge.
// validIDs is the set of node ids the current reindex pass has registered
// (work items, tracks, plans, and live sessions).
//
// This is the single definition of the policy: both the indexing pass and the
// stale-edge purge consult it, so a tombstone written by one cannot be deleted
// by the other on the next rebuild.
func ClassifyEdgeTarget(targetID string, validIDs map[string]bool) EdgeTargetDisposition {
	switch {
	case validIDs[targetID]:
		return EdgeTargetLive
	case IsSessionShapedID(targetID):
		return EdgeTargetTombstoned
	default:
		return EdgeTargetDangling
	}
}
