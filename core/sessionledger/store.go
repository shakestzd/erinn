package sessionledger

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/filelock"
)

// FileName is the git-tracked canonical ledger, a sibling of
// .wipnote/architecture.html and named to sit beside — not inside — the
// gitignored .wipnote/sessions/ telemetry tree it outlives.
const FileName = "sessions-ledger.html"

// ErrNoRow is returned by Close when the ledger holds no row for the session.
// Callers treat it as benign: it means the session started before the ledger
// existed, or its start row was never written, not that anything failed.
var ErrNoRow = errors.New("sessionledger: no row for session")

// OnCommit, when non-nil, is invoked after a successful ledger mutation with
// the wipnote dir, the repo-relative path of the file that changed, and the
// transition verb. cmd/wipnote sets it to the commit-queue producer so ledger
// writes get committed like every other canonical artifact.
//
// It is a package-level seam (the same shape as claimledger.OnCommit) because
// core/ may not import internal/commitqueue, while every writer — CLI, hook
// handler, and retention sweep alike — runs inside the wipnote binary that does.
var OnCommit func(wipnoteDir, relPath, action string)

// Store reads and writes the sessions ledger for one project.
type Store struct {
	wipnoteDir string
	path       string
}

// NewStore returns a Store for the ledger under wipnoteDir. The file is created
// lazily on first write — a read-only consumer must not leave an empty ledger
// behind.
func NewStore(wipnoteDir string) *Store {
	return &Store{wipnoteDir: wipnoteDir, path: filepath.Join(wipnoteDir, FileName)}
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

// ReadAll returns every recorded session, ordered by start. A ledger that does
// not exist yet reads as empty rather than as an error.
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

// Get returns the row for one session.
func (s *Store) Get(sessionID string) (Record, bool, error) {
	recs, err := s.ReadAll()
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range recs {
		if r.SessionID == sessionID {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Open records the START of a session: one appended row with no end.
//
// It is IDEMPOTENT, which is the property that keeps the ledger one-row-per-
// session. SessionStart fires again on every --resume and --continue of the same
// session id, and a replayed hook must not append a second row; Open therefore
// writes NOTHING when a row for the id already exists.
//
// Returns whether a new row was written.
func (s *Store) Open(r Record) (bool, error) {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	r.EndedAt = time.Time{}
	r.ArchivePath = ""
	r.Events = 0
	if err := r.Validate(); err != nil {
		return false, err
	}

	release := filelock.Guard(s.path)
	defer release()

	existing, err := s.readLocked()
	if err != nil {
		return false, err
	}
	if _, found := findRecord(existing, r.SessionID); found >= 0 {
		// Already recorded — a resume or a replayed hook, not a new session.
		return false, nil
	}

	if err := appendRowLocked(s.path, r); err != nil {
		return false, err
	}
	s.notify("session start")
	return true, nil
}

// Close records the END of a session, in place so one session stays one row.
//
// It is rarer than Open by construction (a session is opened before it can be
// closed) and never fires more than once per session end, so paying the
// serialize cost here keeps it off the frequent path.
//
// An already-closed row is left untouched and reported as no change: the first
// recorded end is the true one, and a later reconcile pass must not move it.
// Returns ErrNoRow when the session predates the ledger.
func (s *Store) Close(sessionID string, endedAt time.Time) error {
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}

	release := filelock.Guard(s.path)
	defer release()

	records, err := s.readLocked()
	if err != nil {
		return err
	}
	_, idx := findRecord(records, sessionID)
	if idx < 0 {
		return ErrNoRow
	}
	if !records[idx].IsOpen() {
		return nil
	}
	if endedAt.Before(records[idx].StartedAt) {
		endedAt = records[idx].StartedAt
	}
	records[idx].EndedAt = endedAt
	records[idx].EndSource = EndSourceLiveClose
	if err := writeAllLocked(s.path, records); err != nil {
		return err
	}
	s.notify("session end")
	return nil
}

// Enrichment is what a later pass learns about a session it did not start.
// Zero fields mean "nothing new"; Enrich never clears a value that is already
// recorded, so a backfill can run repeatedly without degrading a richer row.
type Enrichment struct {
	Harness    string
	ProjectDir string
	StartedAt  time.Time
	EndedAt    time.Time
	// EndSource names where EndedAt came from. It is recorded alongside the end
	// so a later Correct can tell a tarball mtime from a real close without
	// guessing; leaving it unset marks the end as unattributed, which ranks
	// lowest and invites re-derivation.
	EndSource   EndSource
	ArchivePath string
	Events      int
}

// Enrich fills in what a later pass learned about a session, and CREATES the
// row when none exists.
//
// The create case is the backfill path and the reason this is not just an
// update: a session archived before the ledger existed has no start row, but the
// archive proves it existed and carries its end time. Writing the row is what
// turns that proof into a resolvable edge target. A row created here records
// only what the source actually knew — the start time falls back to the end
// time rather than being invented, since a row with a fabricated start would
// misreport the interval to every consumer that joins on it.
//
// Returns whether the ledger changed.
func (s *Store) Enrich(sessionID string, e Enrichment) (bool, error) {
	release := filelock.Guard(s.path)
	defer release()

	records, err := s.readLocked()
	if err != nil {
		return false, err
	}

	_, idx := findRecord(records, sessionID)
	if idx < 0 {
		started := e.StartedAt
		if started.IsZero() {
			started = e.EndedAt
		}
		created := Record{
			SessionID:   sessionID,
			Harness:     e.Harness,
			ProjectDir:  e.ProjectDir,
			StartedAt:   started,
			EndedAt:     e.EndedAt,
			ArchivePath: e.ArchivePath,
			Events:      e.Events,
		}
		if !created.IsOpen() {
			created.EndSource = e.EndSource
		}
		if err := created.Validate(); err != nil {
			return false, err
		}
		if err := appendRowLocked(s.path, created); err != nil {
			return false, err
		}
		s.notify("session record")
		return true, nil
	}

	before := records[idx]
	merged := before
	if merged.Harness == "" {
		merged.Harness = e.Harness
	}
	if merged.ProjectDir == "" {
		merged.ProjectDir = e.ProjectDir
	}
	if merged.StartedAt.IsZero() && !e.StartedAt.IsZero() {
		merged.StartedAt = e.StartedAt
	}
	if merged.IsOpen() && !e.EndedAt.IsZero() {
		merged.EndedAt = e.EndedAt
		merged.EndSource = e.EndSource
	}
	if merged.ArchivePath == "" {
		merged.ArchivePath = e.ArchivePath
	}
	if merged.Events == 0 && e.Events > 0 {
		merged.Events = e.Events
	}
	if !merged.EndedAt.IsZero() && merged.EndedAt.Before(merged.StartedAt) {
		merged.EndedAt = merged.StartedAt
	}
	if merged == before {
		return false, nil
	}

	records[idx] = merged
	if err := writeAllLocked(s.path, records); err != nil {
		return false, err
	}
	s.notify("session record")
	return true, nil
}

// Correction describes one proposed change to a row's end, whether it would be
// applied, and why. It is returned by both the dry-run and the apply path so a
// caller can report the same decision it would make.
type Correction struct {
	SessionID string

	OldEnd    time.Time
	OldSource EndSource
	NewEnd    time.Time
	NewSource EndSource

	// Applied is true when the change was written. A correction that is refused
	// (weaker source) or redundant (same value) is still returned, with Applied
	// false and Reason saying which, so a repair pass can explain its no-ops
	// rather than silently skipping them.
	Applied bool
	// WouldApply is true when a dry-run passed every guard and only the dry-run
	// flag stopped the write. It exists so a caller can report the decision this
	// function made instead of re-deriving it — a duplicated rule is a rule that
	// can drift out of agreement with the one that actually governs writes.
	WouldApply bool
	Reason     string
}

// Changed reports whether the correction alters the recorded end VALUE.
// Gaining provenance for an end that was already correct is not a change to the
// session's history, and a repair pass reports the two separately.
func (c Correction) Changed() bool { return !c.NewEnd.Equal(c.OldEnd) }

// AttributionOnly reports whether the only difference is provenance: the end
// was already right, but the row could not say where it came from.
func (c Correction) AttributionOnly() bool {
	return !c.Changed() && c.NewSource != c.OldSource
}

// Correct replaces a row's end when — and only when — the incoming source is
// more trustworthy than the one already recorded.
//
// This is the deliberate counterpart to Enrich, not a loosening of it. Enrich
// fills gaps and never moves a value that is already there, which is right for
// a reconcile pass that must not disturb a real close. But a value recorded
// from a weak source is not a fact to protect, and with no way to correct it a
// canonical artifact has no repair path at all — the gap this exists to close.
//
// The trust ordering (EndSource.Rank) is the whole rule. There is no threshold
// and no inference from the values themselves, because the one value-based rule
// that looks general — "an end after the last recorded event is wrong" — is
// false whenever SessionEnd fires after the last tool event, which is normal.
//
// dryRun computes and returns the decision without writing, so the caller can
// show exactly what would change.
func (s *Store) Correct(sessionID string, end time.Time, src EndSource, dryRun bool) (Correction, error) {
	release := filelock.Guard(s.path)
	defer release()

	records, err := s.readLocked()
	if err != nil {
		return Correction{}, err
	}
	_, idx := findRecord(records, sessionID)
	if idx < 0 {
		return Correction{}, ErrNoRow
	}

	before := records[idx]
	c := Correction{
		SessionID: sessionID,
		OldEnd:    before.EndedAt,
		OldSource: before.EndSource,
		NewEnd:    end,
		NewSource: src,
	}

	switch {
	case end.IsZero():
		c.Reason = "no end available from any source"
		return c, nil
	case end.Before(before.StartedAt):
		// Never record an end before the start; that is corrupt whatever its
		// provenance says.
		c.Reason = "candidate end precedes the recorded start"
		return c, nil
	case !src.OutranksRecorded(before.EndSource):
		c.Reason = "recorded end came from " + describeSource(before.EndSource) +
			", which is at least as trustworthy as " + describeSource(src)
		return c, nil
	case !c.Changed() && !c.AttributionOnly():
		c.Reason = "already correct"
		return c, nil
	}

	if dryRun {
		c.WouldApply = true
		if c.AttributionOnly() {
			c.Reason = "would record this end as " + describeSource(src) + "; the value is already correct"
		} else {
			c.Reason = "would replace " + describeSource(before.EndSource) + " with " + describeSource(src)
		}
		return c, nil
	}

	attributionOnly := c.AttributionOnly()
	records[idx].EndedAt = end
	records[idx].EndSource = src
	if err := writeAllLocked(s.path, records); err != nil {
		return Correction{}, err
	}
	s.notify("session repair")
	c.Applied = true
	if attributionOnly {
		c.Reason = "recorded this end as " + describeSource(src) + "; the value was already correct"
	} else {
		c.Reason = "replaced " + describeSource(before.EndSource) + " with " + describeSource(src)
	}
	return c, nil
}

// describeSource renders a source for operator-facing output, naming the
// unattributed case explicitly rather than printing an empty string.
func describeSource(s EndSource) string {
	if s == EndSourceUnknown {
		return "an unattributed end"
	}
	return string(s)
}

// readLocked reads the ledger while the caller holds the write guard.
func (s *Store) readLocked() ([]Record, error) {
	recs, err := ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return recs, nil
}

// findRecord returns the record for sessionID and its index, or index -1.
func findRecord(recs []Record, sessionID string) (Record, int) {
	for i := range recs {
		if recs[i].SessionID == sessionID {
			return recs[i], i
		}
	}
	return Record{}, -1
}

func (s *Store) notify(action string) {
	if OnCommit == nil {
		return
	}
	OnCommit(s.wipnoteDir, s.RelPath(), action)
}
