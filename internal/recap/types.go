// Package recap collects grounded diff data for a recap. It resolves ONE of
// three inputs — a work-item id (feat/bug/spike), a git range (e.g. main..HEAD),
// or a session id — into a typed RecapData value describing the commits, the
// per-file changes, and (when grounding exists) the lineage chain that ties the
// change back to a work item.
//
// This package owns COLLECTION ONLY. It performs no rendering and emits no HTML;
// the recap renderer and the committed artifact are separate slices that consume
// RecapData. All data is derived from git + the .wipnote read index — nothing
// external.
package recap

import "github.com/shakestzd/wipnote/internal/lineage"

// ChangeType classifies how a file changed across the resolved commit set.
type ChangeType string

const (
	// ChangeAdd marks a file introduced by the change set.
	ChangeAdd ChangeType = "add"
	// ChangeModify marks a file whose contents changed.
	ChangeModify ChangeType = "modify"
	// ChangeDelete marks a file removed by the change set.
	ChangeDelete ChangeType = "delete"
)

// InputKind records which of the three resolvable inputs produced a RecapData.
type InputKind string

const (
	// InputWorkItem is a feat-/bug-/spk- work-item id.
	InputWorkItem InputKind = "work-item"
	// InputRange is a bare git revision range such as "main..HEAD".
	InputRange InputKind = "range"
	// InputSession is a session id (sess-...).
	InputSession InputKind = "session"
)

// RecapData is the typed payload emitted by the collector. It is grounded
// entirely in git + .wipnote/*.html and carries everything the renderer needs:
// the outcome summary, the per-file diff data, the contributing commits, the
// lineage chain (when grounding exists), and provenance describing how the data
// was resolved.
type RecapData struct {
	// Outcome is a human-readable summary of what the change set achieved. For
	// work-item inputs it is the work item's title; for ranges/sessions it is a
	// best-effort description derived from the inputs.
	Outcome string `json:"outcome"`
	// Files is the union of changed files across the resolved commit set, sorted
	// deterministically by path.
	Files []FileChange `json:"files"`
	// Commits is the ordered list of commits that make up the change set.
	Commits []Commit `json:"commits"`
	// LineageChain is the bidirectional lineage walk rooted at the work item or
	// session. It is nil/empty for bare git ranges with no work-item grounding —
	// lineage is the value-add when grounding exists, not a hard requirement.
	LineageChain []lineage.Node `json:"lineage_chain,omitempty"`
	// Provenance records how this RecapData was resolved.
	Provenance Provenance `json:"provenance"`
}

// FileChange describes one file in the change set: its path, how it changed, and
// the minimal diff hunks (with before/after content) for the renderer.
type FileChange struct {
	Path   string     `json:"path"`
	Change ChangeType `json:"change"`
	Hunks  []Hunk     `json:"hunks,omitempty"`
}

// Hunk is one contiguous region of a unified diff. OldStart/OldLines and
// NewStart/NewLines mirror the @@ -a,b +c,d @@ header. Before and After carry
// the removed and added line content respectively so the renderer can show a
// side-by-side or inline diff without re-running git.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Header   string   `json:"header,omitempty"`
	Before   []string `json:"before,omitempty"`
	After    []string `json:"after,omitempty"`
}

// Commit is one commit in the resolved change set.
type Commit struct {
	Hash      string `json:"hash"`
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
	FeatureID string `json:"feature_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Provenance records how a RecapData was resolved: which input kind, the literal
// input string, and the git range that was ultimately diffed (when one applies).
type Provenance struct {
	Kind     InputKind `json:"kind"`
	Input    string    `json:"input"`
	GitRange string    `json:"git_range,omitempty"`
	// Grounded is true when the recap is tied to a work item (directly, or via a
	// session whose commits carry a feature_id). Bare ranges are not grounded.
	Grounded bool `json:"grounded"`
}
