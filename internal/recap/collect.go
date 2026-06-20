package recap

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/lineage"
)

// defaultDepth is the lineage walk depth used when Options.Depth is unset.
const defaultDepth = 5

// Options configures a Collect call.
type Options struct {
	// Input is the single resolvable input: a work-item id (feat-/bug-/spk-),
	// a git range (e.g. "main..HEAD"), or a session id (sess-...).
	Input string
	// ProjectDir is the repository root used for all git subprocess calls.
	ProjectDir string
	// Depth bounds the lineage walk. Zero falls back to defaultDepth.
	Depth int
}

// Collect resolves Options.Input into grounded RecapData. It dispatches on the
// input's shape — work-item ids and session ids walk the lineage graph and read
// the git_commits read index; a bare git range diffs directly with no work-item
// grounding (LineageChain is omitted).
func Collect(db *sql.DB, opts Options) (*RecapData, error) {
	input := strings.TrimSpace(opts.Input)
	if input == "" {
		return nil, fmt.Errorf("recap: empty input")
	}
	if opts.Depth <= 0 {
		opts.Depth = defaultDepth
	}

	switch detectKind(input) {
	case InputWorkItem:
		return collectWorkItem(db, opts, input)
	case InputSession:
		return collectSession(db, opts, input)
	default:
		return collectRange(opts, input)
	}
}

// detectKind classifies the input string. Work-item and session ids are prefix
// routed; everything else is treated as a git range.
func detectKind(input string) InputKind {
	switch {
	case strings.HasPrefix(input, "feat-"),
		strings.HasPrefix(input, "bug-"),
		strings.HasPrefix(input, "spk-"):
		return InputWorkItem
	case strings.HasPrefix(input, "sess-"):
		return InputSession
	default:
		return InputRange
	}
}

// collectWorkItem resolves a feat-/bug-/spk- id: its commits (from the
// git_commits read index, keyed type-agnostically by feature_id) plus the
// bidirectional lineage chain.
func collectWorkItem(db *sql.DB, opts Options, id string) (*RecapData, error) {
	commits, err := dbpkg.GetCommitsByFeature(db, id)
	if err != nil {
		return nil, fmt.Errorf("recap: commits for %s: %w", id, err)
	}
	chain := walkLineage(db, id, opts.Depth)
	data, gitRange, err := buildFromCommits(opts.ProjectDir, commits)
	if err != nil {
		return nil, err
	}
	data.Outcome = resolveTitle(db, id)
	data.LineageChain = chain
	data.Provenance = Provenance{Kind: InputWorkItem, Input: id, GitRange: gitRange, Grounded: true}
	return data, nil
}

// collectSession resolves a session id: the commits produced in that session
// plus the lineage chain rooted at the session. Grounding is true only when at
// least one commit carries a feature_id.
func collectSession(db *sql.DB, opts Options, id string) (*RecapData, error) {
	commits, err := dbpkg.GetCommitsBySession(db, id)
	if err != nil {
		return nil, fmt.Errorf("recap: commits for %s: %w", id, err)
	}
	chain := walkLineage(db, id, opts.Depth)
	data, gitRange, err := buildFromCommits(opts.ProjectDir, commits)
	if err != nil {
		return nil, err
	}
	data.Outcome = fmt.Sprintf("Session %s", id)
	data.LineageChain = chain
	data.Provenance = Provenance{Kind: InputSession, Input: id, GitRange: gitRange, Grounded: hasFeature(commits)}
	return data, nil
}

// collectRange resolves a bare git range with no work-item grounding. Lineage is
// omitted; only diff/file/commit data is returned.
func collectRange(opts Options, gitRange string) (*RecapData, error) {
	commits, err := commitsInRange(opts.ProjectDir, gitRange)
	if err != nil {
		return nil, err
	}
	files, err := diffRange(opts.ProjectDir, gitRange)
	if err != nil {
		return nil, err
	}
	return &RecapData{
		Outcome: fmt.Sprintf("Changes in %s", gitRange),
		Files:   files,
		Commits: commits,
		Provenance: Provenance{
			Kind:     InputRange,
			Input:    gitRange,
			GitRange: gitRange,
			Grounded: false,
		},
	}, nil
}

// walkLineage performs the bidirectional lineage walk and concatenates the
// backward (ancestors) and forward (descendants) chains. A walk failure is
// non-fatal: lineage is a value-add, so we degrade to an empty chain.
func walkLineage(db *sql.DB, root string, depth int) []lineage.Node {
	backward, err := lineage.BackwardWalk(db, root, lineage.AllRels, depth)
	if err != nil {
		backward = nil
	}
	forward, err := lineage.ForwardWalk(db, root, lineage.AllRels, depth)
	if err != nil {
		forward = nil
	}
	if len(backward) == 0 && len(forward) == 0 {
		return nil
	}
	// Tag direction so renderers can split ancestry (above the pivot) from
	// what the work produced (below it). The pure walk leaves Direction empty.
	for i := range backward {
		backward[i].Direction = "ancestor"
	}
	for i := range forward {
		forward[i].Direction = "descendant"
	}
	chain := make([]lineage.Node, 0, len(backward)+len(forward))
	chain = append(chain, backward...)
	chain = append(chain, forward...)
	return chain
}

// buildFromCommits converts read-index commits into RecapData commit entries and
// diffs the selected commits individually. It returns a label for the selected
// commits (empty when no commit hashes resolve). When there are no commits the
// result has empty file and commit sets so callers can still emit a valid
// (empty) recap.
func buildFromCommits(projectDir string, commits []models.GitCommit) (*RecapData, string, error) {
	data := &RecapData{Commits: toCommits(commits)}
	hashes := existingCommitHashes(projectDir, commits)
	if len(hashes) == 0 {
		return data, "", nil
	}
	files, err := diffCommits(projectDir, hashes)
	if err != nil {
		return nil, "", err
	}
	data.Files = files
	return data, strings.Join(hashes, ","), nil
}

// toCommits maps DB commit rows to the recap Commit shape.
func toCommits(commits []models.GitCommit) []Commit {
	if len(commits) == 0 {
		return nil
	}
	out := make([]Commit, 0, len(commits))
	for _, c := range commits {
		out = append(out, Commit{
			Hash:      c.CommitHash,
			Message:   c.Message,
			SessionID: c.SessionID,
			FeatureID: c.FeatureID,
			Timestamp: c.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out
}

// existingCommitHashes returns selected commit hashes that resolve in the
// repository. Commit rows are timestamp-DESC, so reverse them to diff oldest to
// newest without constructing a continuous range across unrelated commits.
func existingCommitHashes(projectDir string, commits []models.GitCommit) []string {
	var hashes []string
	for _, c := range commits {
		if h := strings.TrimSpace(c.CommitHash); h != "" && commitExists(projectDir, h) {
			hashes = append(hashes, h)
		}
	}
	for i, j := 0, len(hashes)-1; i < j; i, j = i+1, j-1 {
		hashes[i], hashes[j] = hashes[j], hashes[i]
	}
	return hashes
}

// resolveTitle returns the work item's display title for the recap outcome.
// It uses the feature title from the read index when available, falling back to
// the id.
func resolveTitle(db *sql.DB, id string) string {
	var title sql.NullString
	_ = db.QueryRow(`SELECT title FROM features WHERE id = ? LIMIT 1`, id).Scan(&title)
	if title.Valid && strings.TrimSpace(title.String) != "" {
		return title.String
	}
	return id
}

// hasFeature reports whether any commit carries a feature_id (i.e. the session's
// work is grounded to a work item).
func hasFeature(commits []models.GitCommit) bool {
	for _, c := range commits {
		if strings.TrimSpace(c.FeatureID) != "" {
			return true
		}
	}
	return false
}

// sortFiles orders FileChange entries deterministically by path.
func sortFiles(files []FileChange) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
