package retention

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SweepResult summarizes one ndjson retention sweep.
type SweepResult struct {
	Pruned         int   // session dirs archived + removed
	BytesReclaimed int64 // events.ndjson bytes reclaimed
	Skipped        int   // sessions left untouched (active/recent/un-ingested)
}

// sweepEnv injects the wall clock and the active-session id so tests can
// control liveness deterministically. activeSessionID is the current/live
// session that must never be pruned even if its mtime/age would otherwise
// qualify (e.g. a quiet session whose ndjson hasn't been written for >grace).
type sweepEnv struct {
	now             time.Time
	activeSessionID string
	grace           time.Duration
}

// SweepNDJSON archives + prunes raw events.ndjson for sessions that are BOTH
// inactive AND durably ingested into SQLite. It is the coverage that
// retention.Run (which only handles DB status='completed' sessions) misses:
// crashed/disconnected/stale-active sessions whose ndjson grows without bound
// but never reaches completed_at.
//
// A session is pruned only when ALL hold:
//   - it is NOT the active session (activeSessionID),
//   - its events.ndjson mtime is older than the active grace window (not being
//     appended to right now),
//   - the indexer has fully consumed it (.index-offset >= file size), i.e. the
//     queryable data is durably in the SQLite read index, and
//   - it is older than retainDays OR we are over the max-sessions cap.
//
// Pruning reuses archiveSession: the ndjson is tar.gz'd into
// .wipnote/archive/<yyyy-mm>/ before the live dir is removed, so the raw log is
// recoverable via ExtractArchive and no un-ingested data is ever lost.
func SweepNDJSON(wipnoteDir, activeSessionID string, cfg Config, dryRun bool) (SweepResult, error) {
	return sweepNDJSON(wipnoteDir, cfg, dryRun, sweepEnv{
		now:             time.Now(),
		activeSessionID: activeSessionID,
		grace:           time.Duration(activeGraceMinutes) * time.Minute,
	})
}

// sweepCandidate is an inactive, fully-ingested session eligible for pruning.
type sweepCandidate struct {
	sessionID  string
	sessDir    string
	eventsFile string
	mtime      time.Time
	size       int64
}

func sweepNDJSON(wipnoteDir string, cfg Config, dryRun bool, env sweepEnv) (SweepResult, error) {
	var res SweepResult
	sessionsRoot := filepath.Join(wipnoteDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("read sessions dir: %w", err)
	}

	var candidates []sweepCandidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		sessDir := filepath.Join(sessionsRoot, sid)
		eventsFile := filepath.Join(sessDir, "events.ndjson")

		info, err := os.Stat(eventsFile)
		if err != nil {
			continue // no events.ndjson — nothing to reclaim here
		}

		// SAFETY 1: never touch the active session.
		if sid == env.activeSessionID && env.activeSessionID != "" {
			res.Skipped++
			continue
		}
		// SAFETY 2: never touch a recently-modified (possibly-appending) log.
		if env.now.Sub(info.ModTime()) < env.grace {
			res.Skipped++
			continue
		}
		// SAFETY 3: never prune un-ingested data — the indexer must have caught
		// up to the full file size before we archive+remove it.
		if !indexerCaughtUp(sessDir, eventsFile) {
			res.Skipped++
			continue
		}
		candidates = append(candidates, sweepCandidate{
			sessionID:  sid,
			sessDir:    sessDir,
			eventsFile: eventsFile,
			mtime:      info.ModTime(),
			size:       info.Size(),
		})
	}

	// Oldest first, so the max-sessions cap prunes the stalest sessions.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	ageCutoff := env.now.Add(-time.Duration(cfg.NDJSONRetainDays) * 24 * time.Hour)
	// Number of sessions to force-prune to satisfy the max-sessions cap.
	overCap := 0
	if cfg.NDJSONMaxSessions > 0 && len(candidates) > cfg.NDJSONMaxSessions {
		overCap = len(candidates) - cfg.NDJSONMaxSessions
	}

	for i, c := range candidates {
		olderThanRetain := c.mtime.Before(ageCutoff)
		withinCap := i < overCap
		if !olderThanRetain && !withinCap {
			res.Skipped++
			continue
		}
		if err := archiveSession(wipnoteDir, c.sessionID, c.mtime, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "retention: sweep prune session %s: %v\n", c.sessionID, err)
			res.Skipped++
			continue
		}
		res.Pruned++
		res.BytesReclaimed += c.size
	}
	return res, nil
}
