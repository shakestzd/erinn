package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/shakestzd/wipnote/core/claimledger"
)

// ClaimEpisode is one row of the claim_episodes read index — a durable
// interval during which one agent held one work item.
type ClaimEpisode struct {
	EpisodeID     string
	WorkItemID    string
	SessionID     string
	RootSessionID string
	AgentID       string
	StartedAt     time.Time
	EndedAt       time.Time
	Outcome       string
	SourceFile    string
}

// IsOpen reports whether the episode has no recorded end.
func (e ClaimEpisode) IsOpen() bool { return e.EndedAt.IsZero() }

// UpsertClaimEpisode writes one episode into the read index. The canonical HTML
// is authoritative, so this is a blind upsert keyed by episode ID: reindex may
// replay the same row any number of times.
func UpsertClaimEpisode(db *sql.DB, e ClaimEpisode) error {
	endedAt := ""
	if !e.EndedAt.IsZero() {
		endedAt = claimledger.FormatTime(e.EndedAt)
	}
	_, err := db.Exec(`
		INSERT INTO claim_episodes (
			episode_id, work_item_id, session_id, root_session_id, agent_id,
			started_at, ended_at, outcome, source_file, indexed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(episode_id) DO UPDATE SET
			work_item_id    = excluded.work_item_id,
			session_id      = excluded.session_id,
			root_session_id = excluded.root_session_id,
			agent_id        = excluded.agent_id,
			started_at      = excluded.started_at,
			ended_at        = excluded.ended_at,
			outcome         = excluded.outcome,
			source_file     = excluded.source_file,
			indexed_at      = CURRENT_TIMESTAMP`,
		e.EpisodeID, e.WorkItemID, e.SessionID, e.RootSessionID, e.AgentID,
		claimledger.FormatTime(e.StartedAt), endedAt, e.Outcome, e.SourceFile,
	)
	if err != nil {
		return fmt.Errorf("upsert claim episode %s: %w", e.EpisodeID, err)
	}
	return nil
}

// PurgeClaimEpisodes empties the read index. Reindex calls this before an
// ingest pass so episodes whose canonical file was deleted or archived do not
// linger as ghosts.
func PurgeClaimEpisodes(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM claim_episodes`); err != nil {
		return fmt.Errorf("purge claim episodes: %w", err)
	}
	return nil
}

// intervalPredicate matches episodes covering an instant.
//
// The end bound is EXCLUSIVE so two back-to-back episodes — one ending exactly
// when the next begins — do not both match the boundary instant. An open
// episode (ended_at = ”) matches any instant at or after its start: correct
// while its session lives, and closed to "expired" by claimledger.Reconcile
// once it does not.
const intervalPredicate = `started_at <= ? AND (ended_at = '' OR ended_at > ?)`

const claimEpisodeColumns = `episode_id, work_item_id, session_id, root_session_id,
	agent_id, started_at, ended_at, outcome, source_file`

// WorkItemForAgentAt returns the work item that agentID held at instant at, or
// "" when the agent held nothing then.
//
// This is the query the claim ledger exists to serve: per-signal attribution
// knows the agent that emitted a signal and when, and needs the work item to
// charge it to. Prefer WorkItemForSessionAgentAt when the session is known —
// AgentRootSentinel ("__root__") is shared by every root session, so agent
// alone is ambiguous across concurrent sessions.
func WorkItemForAgentAt(db *sql.DB, agentID string, at time.Time) (string, error) {
	return WorkItemForSessionAgentAt(db, "", agentID, at)
}

// WorkItemForSessionAgentAt is WorkItemForAgentAt scoped to one session. An
// empty sessionID matches any session.
//
// When several episodes cover the instant (which should not happen for one
// agent, but a cache-wiped or hand-edited ledger can produce it) the most
// recently started wins, matching GetActiveClaim's newest-first semantics.
func WorkItemForSessionAgentAt(db *sql.DB, sessionID, agentID string, at time.Time) (string, error) {
	if db == nil || agentID == "" {
		return "", nil
	}
	ts := claimledger.FormatTime(at)
	query := `SELECT work_item_id FROM claim_episodes WHERE agent_id = ? AND ` + intervalPredicate
	args := []any{agentID, ts, ts}
	if sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY started_at DESC, episode_id DESC LIMIT 1`

	var workItem string
	err := db.QueryRow(query, args...).Scan(&workItem)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve work item for agent %s at %s: %w", agentID, ts, err)
	}
	return workItem, nil
}

// ClaimEpisodesAt returns every episode covering an instant, across all agents.
// Used by the dashboard and by `wipnote claims who` to show who held what at a
// moment in time.
func ClaimEpisodesAt(db *sql.DB, at time.Time) ([]ClaimEpisode, error) {
	ts := claimledger.FormatTime(at)
	return queryClaimEpisodes(db,
		`SELECT `+claimEpisodeColumns+` FROM claim_episodes WHERE `+intervalPredicate+
			` ORDER BY started_at, episode_id`, ts, ts)
}

// ClaimEpisodesForAgent returns an agent's episodes, newest first.
func ClaimEpisodesForAgent(db *sql.DB, agentID string, limit int) ([]ClaimEpisode, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryClaimEpisodes(db,
		`SELECT `+claimEpisodeColumns+` FROM claim_episodes WHERE agent_id = ?
		 ORDER BY started_at DESC, episode_id DESC LIMIT ?`, agentID, limit)
}

// ClaimEpisodesForWorkItem returns every episode recorded for a work item,
// oldest first — the per-item ownership history.
func ClaimEpisodesForWorkItem(db *sql.DB, workItemID string) ([]ClaimEpisode, error) {
	return queryClaimEpisodes(db,
		`SELECT `+claimEpisodeColumns+` FROM claim_episodes WHERE work_item_id = ?
		 ORDER BY started_at, episode_id`, workItemID)
}

// ListClaimEpisodes returns episodes, newest first.
func ListClaimEpisodes(db *sql.DB, limit int) ([]ClaimEpisode, error) {
	if limit <= 0 {
		limit = 100
	}
	return queryClaimEpisodes(db,
		`SELECT `+claimEpisodeColumns+` FROM claim_episodes
		 ORDER BY started_at DESC, episode_id DESC LIMIT ?`, limit)
}

func queryClaimEpisodes(db *sql.DB, query string, args ...any) ([]ClaimEpisode, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query claim episodes: %w", err)
	}
	defer rows.Close()

	var out []ClaimEpisode
	for rows.Next() {
		var (
			e                  ClaimEpisode
			startedStr, endStr string
		)
		if err := rows.Scan(
			&e.EpisodeID, &e.WorkItemID, &e.SessionID, &e.RootSessionID,
			&e.AgentID, &startedStr, &endStr, &e.Outcome, &e.SourceFile,
		); err != nil {
			return nil, fmt.Errorf("scan claim episode: %w", err)
		}
		if ts, perr := claimledger.ParseTime(startedStr); perr == nil {
			e.StartedAt = ts
		}
		if ts, perr := claimledger.ParseTime(endStr); perr == nil {
			e.EndedAt = ts
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
