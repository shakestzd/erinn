package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GetCurrentSessionResumable resolves the resumable row for the session the user
// is launching from, given the candidate session IDs (the current session plus
// its session-family members). Unlike ListResumableSessions /
// ListHarnessGroupedResumableSessions, it applies NO work_item_id <> '' gate and
// NO item_status filter: the current session is surfaced even when it carries no
// work-item attribution (the common session-split case) or when its work item is
// already completed. The most recently active candidate wins.
//
// Returns (nil, nil) when none of the IDs match a known session, so callers can
// simply omit the "Resume this session" slot and degrade to the grouped listing.
func GetCurrentSessionResumable(db *sql.DB, threshold time.Duration, sessionIDs []string) (*ResumableSession, error) {
	if db == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339)

	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, cutoff)

	row := db.QueryRow(`
WITH session_work AS (
	SELECT
		s.session_id,
		s.created_at,
		COALESCE(s.exec_worktree_path, '') AS exec_worktree_path,
		COALESCE(s.branch, '') AS branch,
		COALESCE(s.harness, '') AS harness,
		COALESCE(s.agent_assigned, '') AS agent_assigned,
		COALESCE(awi.work_item_id, s.active_feature_id, '') AS work_item_id
	FROM sessions s
	-- Scope to the root agent's claim: a session may carry several
	-- active_work_items rows (one per subagent); joining on session_id alone
	-- would duplicate the session as a candidate and could surface a subagent's
	-- work item. Root attribution is what "resume this session" means, matching
	-- GetActiveWorkItemWithFallback's __root__ semantics.
	LEFT JOIN active_work_items awi
		ON awi.session_id = s.session_id AND awi.agent_id = '`+AgentRootSentinel+`'
	WHERE s.session_id IN (`+placeholders+`)
),
enriched AS (
	SELECT
		sw.session_id,
		sw.work_item_id,
		COALESCE(f.title, t.title, '') AS title,
		COALESCE(NULLIF(f.type, ''), CASE WHEN t.id IS NOT NULL THEN 'track' ELSE '' END) AS type,
		sw.branch,
		sw.exec_worktree_path,
		sw.harness,
		sw.agent_assigned,
		COALESCE((
			SELECT MAX(ts)
			FROM (
				SELECT MAX(ae.timestamp) AS ts FROM agent_events ae WHERE ae.session_id = sw.session_id
				UNION ALL
				SELECT MAX(m.timestamp) AS ts FROM messages m WHERE m.session_id = sw.session_id
				UNION ALL
				SELECT MAX(c.last_heartbeat_at) AS ts FROM claims c WHERE c.owner_session_id = sw.session_id
				UNION ALL
				SELECT sw.created_at AS ts
			)
		), sw.created_at) AS last_activity,
		(SELECT MAX(c.last_heartbeat_at) FROM claims c WHERE c.owner_session_id = sw.session_id) AS last_heartbeat_at
	FROM session_work sw
	LEFT JOIN features f ON f.id = sw.work_item_id
	LEFT JOIN tracks t ON t.id = sw.work_item_id
)
SELECT
	work_item_id,
	title,
	type,
	branch,
	exec_worktree_path,
	harness,
	last_activity,
	session_id,
	CASE
		WHEN last_heartbeat_at IS NOT NULL AND last_heartbeat_at >= ? THEN 1
		ELSE 0
	END AS live,
	agent_assigned
FROM enriched
ORDER BY last_activity DESC, session_id DESC
LIMIT 1`, args...)

	var item ResumableSession
	var live int
	var agentAssigned string
	if err := row.Scan(
		&item.WorkItemID,
		&item.Title,
		&item.Type,
		&item.Branch,
		&item.ExecWorktreePath,
		&item.Harness,
		&item.LastActivity,
		&item.LastSessionID,
		&live,
		&agentAssigned,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get current session resumable: %w", err)
	}
	item.Live = live != 0
	item.Harness = normalizeSessionHarness(item.Harness, agentAssigned)
	return &item, nil
}
