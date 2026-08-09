package gateledger

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/filelock"
)

// OnCommit, when non-nil, is invoked after a successful ledger append with the
// wipnote dir, the repo-relative path of the file that changed, and the action
// verb. cmd/wipnote sets it to the commit-queue producer so ledger writes get
// committed like every other canonical artifact.
//
// It is a package-level seam (the same shape as claimledger.OnCommit and
// sessionledger.OnCommit) because core/ may not import internal/commitqueue,
// while every writer runs inside the wipnote binary that does.
var OnCommit func(wipnoteDir, relPath, action string)

// Store reads and appends the gate ledger for one project.
//
// # Reads are coupled to working-tree git state — deliberately
//
// The completion gate resolves its evidence through this Store rather than
// through the derived index (feat-0e5ca43e), which makes gate decisions a
// function of the CHECKED-OUT ledger file. Checking out an older commit
// therefore surfaces that commit's gate records, and a completion judged at that
// commit sees the evidence that existed there.
//
// That is the intended semantics, not a leak: a gate record is a fact about a
// tree state, so evaluating it against the tree you are actually on is more
// correct than evaluating it against a machine-local cache that outlives every
// checkout. It is nonetheless a BEHAVIOUR CHANGE from the index-backed gate,
// which answered identically regardless of HEAD, and callers reasoning about
// gate outcomes across branches need to know it.
type Store struct {
	wipnoteDir string
	path       string
}

// NewStore returns a Store for the ledger under wipnoteDir. The file is created
// lazily on first append — a read-only consumer must not leave an empty ledger
// behind.
func NewStore(wipnoteDir string) *Store {
	return &Store{wipnoteDir: wipnoteDir, path: filepath.Join(wipnoteDir, FileName)}
}

// StoreForProject returns a Store for the project rooted at projectRoot.
func StoreForProject(projectRoot string) *Store {
	return NewStore(filepath.Join(projectRoot, ".wipnote"))
}

// Path returns the absolute ledger path.
func (s *Store) Path() string { return s.path }

// RelPath returns the ledger path relative to the repo root (the parent of
// .wipnote), for git staging and commit intents. Absolute host paths must never
// leak into wipnote descriptions or commit messages, so callers use this.
func (s *Store) RelPath() string {
	repoRoot := filepath.Dir(s.wipnoteDir)
	if rel, err := filepath.Rel(repoRoot, s.path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(filepath.Join(filepath.Base(s.wipnoteDir), FileName))
}

// Append records one gate run and returns the stored record.
//
// Unlike the claim and sessions ledgers this needs no read-before-write and no
// idempotency check: every gate run is a distinct fact, two runs of the same
// commands in the same session are two records, and nothing ever revises one.
// The append is therefore constant-time — it never parses the existing rows.
//
// The record's ID, timestamp and JSON defaults are filled in when absent, and the
// signature is stamped when the caller has not already computed one.
func (s *Store) Append(r Record) (Record, error) {
	r.Normalize()
	if r.Signature == "" {
		r.EnsureSignature()
	}
	if err := r.Validate(); err != nil {
		return Record{}, err
	}

	release := filelock.Guard(s.path)
	defer release()

	if err := appendRowLocked(s.path, r); err != nil {
		return Record{}, err
	}
	s.notify("gate run")
	return r, nil
}

// ReadAll returns every recorded gate run, ordered by checked-at. A ledger that
// does not exist yet reads as empty rather than as an error.
func (s *Store) ReadAll() ([]Record, error) {
	recs, err := ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return recs, nil
}

// LatestForSession returns the most recent gate run recorded by sessionID, or
// nil when the session has run none. It is the canonical counterpart of
// db.LatestGateRecordForSession, and the completion gate's FIRST lookup.
func (s *Store) LatestForSession(sessionID string) (*Record, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	// Records are sorted ascending, so the last match is the latest.
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].SessionID == sessionID {
			r := records[i]
			return &r, nil
		}
	}
	return nil, nil
}

// LatestPassingForWorkItem returns the most recent PASSING gate run for
// workItemID from any session, provided it was checked within `within`. A nil
// record with no error means no qualifying run exists.
//
// This is the cross-session fallback (bug-35857288): a work item validated by a
// passing gate in one session must be completable from another session, instead
// of being rejected for lacking a session-scoped record. It is the canonical
// counterpart of db.LatestPassingGateRecordForWorkItem, and applies the recency
// filter itself rather than leaving it to the caller.
func (s *Store) LatestPassingForWorkItem(workItemID string, within time.Duration) (*Record, error) {
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		return nil, nil
	}
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].WorkItemID != workItemID || !records[i].Passed() {
			continue
		}
		if within > 0 && time.Since(records[i].CheckedAt) > within {
			// This is the newest passing run for the item, so every remaining
			// candidate is older still and nothing can qualify.
			return nil, nil
		}
		r := records[i]
		return &r, nil
	}
	return nil, nil
}

// Signatures returns the set of record signatures already in the ledger.
//
// It exists for the one-shot backfill of legacy index rows written before this
// ledger did: the signature is a content checksum over a run's decision-relevant
// fields, so a match means the same run is already canonical and must not be
// appended twice. Returning the whole set keeps that backfill a single parse
// rather than one per candidate row.
func (s *Store) Signatures() (map[string]bool, error) {
	records, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(records))
	for _, r := range records {
		if r.Signature != "" {
			out[r.Signature] = true
		}
	}
	return out, nil
}

func (s *Store) notify(action string) {
	if OnCommit == nil {
		return
	}
	OnCommit(s.wipnoteDir, s.RelPath(), action)
}
