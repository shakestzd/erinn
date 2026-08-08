package claimledger

import (
	"fmt"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/filelock"
)

// LivePredicate reports whether a root session is still running. Reconcile and
// Archive take it as a parameter rather than reaching for the session table
// themselves: liveness is a db/hooks concern, and injecting it keeps this
// package free of that dependency and trivially testable.
type LivePredicate func(rootSessionID string) bool

// ReconcileResult reports what a reconcile pass closed.
type ReconcileResult struct {
	Sessions int
	Episodes int
}

// Reconcile closes the open episodes of every root session that is no longer
// live, with OutcomeExpired.
//
// This is what makes an open interval eventually queryable AS an interval. An
// agent that is killed (SIGKILL, machine crash, container teardown) fires
// neither SessionEnd nor the release path, so its row keeps an empty end
// forever. Readers treat an open episode as open-ended — correct while the
// session is alive — and this pass supplies the missing end once it is not.
func (s *Store) Reconcile(live LivePredicate, endedAt time.Time) (ReconcileResult, error) {
	var res ReconcileResult
	shards, err := s.Shards()
	if err != nil {
		return res, err
	}
	for _, shard := range shards {
		episodes, readErr := readShard(shard)
		if readErr != nil {
			return res, readErr
		}
		root := rawRootSession(shard, episodes)
		if root == "" {
			continue
		}
		if live != nil && live(root) {
			continue
		}
		end := endedAt
		if end.IsZero() {
			// Fall back to the shard's own last write: a dead session's episode
			// should not be stamped with a "now" that is hours after anything
			// actually happened in it.
			if fi, statErr := os.Stat(shard); statErr == nil {
				end = fi.ModTime().UTC()
			} else {
				end = time.Now().UTC()
			}
		}
		// Empty session filter: the whole tree under this root is dead, so every
		// open episode in the shard gets an end.
		closed, closeErr := s.CloseAllForSession(root, "", OutcomeExpired, end)
		if closeErr != nil {
			return res, closeErr
		}
		if closed > 0 {
			res.Sessions++
			res.Episodes += closed
		}
	}
	return res, nil
}

// ArchiveCandidate is one shard eligible for compaction.
type ArchiveCandidate struct {
	Path          string
	RootSessionID string
	Episodes      []Episode
	LastActivity  time.Time
}

// ArchiveResult reports what an archive pass rolled up.
type ArchiveResult struct {
	Candidates []ArchiveCandidate
	Episodes   int
	// ArchivePath is the file the episodes were merged into; empty in dry-run.
	ArchivePath string
}

// CollectArchiveCandidates returns shards eligible for compaction: every
// episode closed, the session not live, and no activity since cutoff.
//
// All three conditions are required. "Every episode closed" alone would archive
// a live session between two claims; "not live" alone would archive a session
// that just ended, whose shard is the one most likely to be read next.
func (s *Store) CollectArchiveCandidates(cutoff time.Time, live LivePredicate) ([]ArchiveCandidate, error) {
	shards, err := s.Shards()
	if err != nil {
		return nil, err
	}
	var out []ArchiveCandidate
	for _, shard := range shards {
		episodes, readErr := readShard(shard)
		if readErr != nil {
			return nil, readErr
		}
		if len(episodes) == 0 {
			continue
		}
		root := rawRootSession(shard, episodes)
		if root == "" {
			continue
		}
		if live != nil && live(root) {
			continue
		}
		last := time.Time{}
		allClosed := true
		for _, e := range episodes {
			if e.IsOpen() {
				allClosed = false
				break
			}
			if e.EndedAt.After(last) {
				last = e.EndedAt
			}
		}
		if !allClosed || last.IsZero() || last.After(cutoff) {
			continue
		}
		out = append(out, ArchiveCandidate{
			Path:          shard,
			RootSessionID: root,
			Episodes:      episodes,
			LastActivity:  last,
		})
	}
	return out, nil
}

// Archive merges eligible shards into the archive ledger and removes them.
//
// Ledger first, shard removal second: canonical data is never momentarily
// absent, which is the same ordering `wipnote archive` uses for work items.
// Archived episodes remain queryable because Files() includes the archive and
// the reindex pass reads it with the identical parser.
//
// With apply=false nothing is written; the returned result describes what would
// happen. Re-running is safe: a shard already merged is gone, and merging by
// episode ID makes a partial previous run converge rather than duplicate.
func (s *Store) Archive(cutoff time.Time, live LivePredicate, apply bool) (ArchiveResult, error) {
	var res ArchiveResult
	candidates, err := s.CollectArchiveCandidates(cutoff, live)
	if err != nil {
		return res, err
	}
	res.Candidates = candidates
	for _, c := range candidates {
		res.Episodes += len(c.Episodes)
	}
	if !apply || len(candidates) == 0 {
		return res, nil
	}

	archivePath := s.ArchivePath()
	if err := s.mergeIntoArchive(archivePath, candidates); err != nil {
		return res, err
	}
	for _, c := range candidates {
		if rmErr := os.Remove(c.Path); rmErr != nil && !os.IsNotExist(rmErr) {
			return res, fmt.Errorf("claimledger: remove archived shard %s: %w", c.Path, rmErr)
		}
		// The shard's lock sidecar is derived and gitignored; drop it with the
		// shard so the directory does not accumulate orphans.
		_ = os.Remove(c.Path + filelock.LockSuffix)
	}
	res.ArchivePath = archivePath
	s.notify(archivePath, "archive")
	return res, nil
}

// mergeIntoArchive rewrites the archive with its existing rows plus the
// candidates' episodes, keyed by episode ID so a re-run cannot duplicate.
func (s *Store) mergeIntoArchive(archivePath string, candidates []ArchiveCandidate) error {
	release := filelock.Guard(archivePath)
	defer release()

	existing, err := readShard(archivePath)
	if err != nil {
		return err
	}
	byID := make(map[string]Episode, len(existing))
	order := make([]string, 0, len(existing))
	add := func(e Episode) {
		if _, seen := byID[e.ID]; !seen {
			order = append(order, e.ID)
		}
		byID[e.ID] = e
	}
	for _, e := range existing {
		add(e)
	}
	for _, c := range candidates {
		for _, e := range c.Episodes {
			add(e)
		}
	}

	merged := make([]Episode, 0, len(order))
	for _, id := range order {
		merged = append(merged, byID[id])
	}
	return writeAllLocked(archivePath, merged)
}
