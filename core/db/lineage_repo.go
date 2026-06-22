package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

// Execer is satisfied by both *sql.DB and *sql.Tx, enabling transaction-aware helpers.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// InsertLineageTrace inserts a new lineage trace row.
func InsertLineageTrace(db *sql.DB, trace *models.LineageTrace) error {
	return insertLineageTrace(db, trace)
}

// InsertLineageTraceExecer inserts a lineage trace row using an Execer (e.g. *sql.Tx).
func InsertLineageTraceExecer(ex Execer, trace *models.LineageTrace) error {
	return insertLineageTrace(ex, trace)
}

func insertLineageTrace(ex Execer, trace *models.LineageTrace) error {
	query, args, err := InsertLineageTraceStmt(trace)
	if err != nil {
		return err
	}
	if _, err := ex.Exec(query, args...); err != nil {
		return fmt.Errorf("insert lineage trace %s: %w", trace.TraceID, err)
	}
	return nil
}

// InsertLineageTraceStmt builds the parameterized INSERT that insertLineageTrace
// Execs, WITHOUT executing it, so the hot-path subagent-start hook can route the
// exact same statement through the daemon's enqueue-only seam instead of
// blocking a direct writable handle under a held external write lock
// (bug-c9ec25a4). The (sql, args) produces the SAME database effect as the
// direct Exec. The error is the json.Marshal(trace.Path) failure; on a non-nil
// error the caller MUST skip routing (never enqueue a half-built statement).
//
// JSON-TRANSPORT SAFETY: the direct path binds session_id / agent_name /
// feature_id as sql.NullString (nullStr) — which the daemon CANNOT JSON-encode
// and re-bind. Here those three are normalized through nullableStr, which
// returns nil for the empty string and the plain string otherwise — the exact
// same SQLite NULL-vs-text binding the sql.NullString produces, but a
// transport-safe primitive over the wire. depth is an int, path is a JSON
// STRING (string(pathJSON)), and started_at is an RFC3339 STRING — all
// transport-safe. No sql.NullString / time.Time crosses the wire.
func InsertLineageTraceStmt(trace *models.LineageTrace) (string, []any, error) {
	pathJSON, err := json.Marshal(trace.Path)
	if err != nil {
		return "", nil, fmt.Errorf("marshal lineage path: %w", err)
	}
	query := `
		INSERT INTO agent_lineage_trace
			(trace_id, root_session_id, session_id, agent_name, depth, path,
			 feature_id, started_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	args := []any{
		trace.TraceID, trace.RootSessionID, nullableStr(trace.SessionID),
		nullableStr(trace.AgentName), trace.Depth, string(pathJSON),
		nullableStr(trace.FeatureID),
		trace.StartedAt.UTC().Format(time.RFC3339),
		trace.Status,
	}
	return query, args, nil
}

// GetLineageByRoot returns all lineage traces rooted at a given session,
// ordered by depth ascending.
func GetLineageByRoot(db *sql.DB, rootSessionID string) ([]models.LineageTrace, error) {
	rows, err := db.Query(`
		SELECT trace_id, root_session_id, session_id, agent_name, depth, path,
		       feature_id, started_at, completed_at, status
		FROM agent_lineage_trace
		WHERE root_session_id = ?
		ORDER BY depth ASC`, rootSessionID)
	if err != nil {
		return nil, fmt.Errorf("get lineage by root %s: %w", rootSessionID, err)
	}
	defer rows.Close()
	return scanLineageRows(rows)
}

// GetLineageBySession returns the lineage trace for a specific session, if any.
func GetLineageBySession(db *sql.DB, sessionID string) (*models.LineageTrace, error) {
	row := db.QueryRow(`
		SELECT trace_id, root_session_id, session_id, agent_name, depth, path,
		       feature_id, started_at, completed_at, status
		FROM agent_lineage_trace
		WHERE session_id = ?
		LIMIT 1`, sessionID)
	traces, err := scanLineageRows(singleRowToRows(row))
	if err != nil {
		return nil, fmt.Errorf("get lineage by session %s: %w", sessionID, err)
	}
	if len(traces) == 0 {
		return nil, nil
	}
	return &traces[0], nil
}

// CompleteLineageTrace marks a session's lineage trace as completed.
func CompleteLineageTrace(db *sql.DB, sessionID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE agent_lineage_trace
		SET status = 'completed', completed_at = ?
		WHERE session_id = ?`,
		now, sessionID,
	)
	return err
}

// CloseLineageTraceByTraceIDStmt builds the parameterized UPDATE that closes
// the lineage row keyed by trace_id (the subagent's agent_id — see
// insertSubagentLineage), WITHOUT executing it, so the subagent-STOP hook can
// route the close through the daemon's enqueue-only seam instead of issuing a
// DIRECT Exec (roborev-473 finding 4). Routing the close enqueue-only — like the
// subagent-START insert — makes both writes land on the daemon's single writer
// in FIFO order: SubagentStart fires before SubagentStop, so the start insert is
// enqueued first and the close UPDATE applies AFTER it. That eliminates the
// orphaned-`active`-row race where a DIRECT close UPDATE ran before the still-
// queued start insert had applied (the UPDATE matched 0 rows, then the insert
// landed an `active` row that nothing ever closed).
//
// JSON-TRANSPORT SAFETY: completed_at is an RFC3339 STRING and trace_id a plain
// string — both transport-safe primitives the daemon can JSON-encode and re-bind
// identically. The statement produces the SAME effect as the direct Exec in
// closeSubagentLineage.
func CloseLineageTraceByTraceIDStmt(traceID string) (string, []any) {
	query := `
		UPDATE agent_lineage_trace
		   SET completed_at = ?, status = 'completed'
		 WHERE trace_id = ? AND completed_at IS NULL`
	args := []any{time.Now().UTC().Format(time.RFC3339), traceID}
	return query, args
}

// scanLineageRows scans a set of lineage rows into a slice of LineageTrace.
func scanLineageRows(rows lineageScanner) ([]models.LineageTrace, error) {
	var traces []models.LineageTrace
	for rows.Next() {
		var t models.LineageTrace
		var sessionID, agentName, featureID, completedAt sql.NullString
		var startedStr, pathJSON string

		if err := rows.Scan(
			&t.TraceID, &t.RootSessionID, &sessionID, &agentName,
			&t.Depth, &pathJSON, &featureID, &startedStr, &completedAt, &t.Status,
		); err != nil {
			return nil, err
		}
		t.SessionID = sessionID.String
		t.AgentName = agentName.String
		t.FeatureID = featureID.String
		t.StartedAt, _ = time.Parse(time.RFC3339, startedStr)
		if completedAt.Valid && completedAt.String != "" {
			ts, _ := time.Parse(time.RFC3339, completedAt.String)
			t.CompletedAt = &ts
		}
		_ = json.Unmarshal([]byte(pathJSON), &t.Path)
		traces = append(traces, t)
	}
	return traces, rows.Err()
}

// lineageScanner abstracts *sql.Rows so scanLineageRows works for both
// multi-row queries and the single-row wrapper.
type lineageScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// singleRowResult wraps *sql.Row into the lineageScanner interface.
type singleRowResult struct {
	row     *sql.Row
	scanned bool
	err     error
}

func singleRowToRows(row *sql.Row) lineageScanner {
	return &singleRowResult{row: row}
}

func (s *singleRowResult) Next() bool {
	if s.scanned {
		return false
	}
	return true
}

func (s *singleRowResult) Scan(dest ...any) error {
	s.scanned = true
	s.err = s.row.Scan(dest...)
	if s.err == sql.ErrNoRows {
		s.err = nil
	}
	return s.err
}

func (s *singleRowResult) Err() error { return s.err }

// InsertGitCommit records a git commit linked to a session and optional feature.
func InsertGitCommit(database *sql.DB, commit *models.GitCommit) error {
	_, err := insertGitCommit(database, commit)
	return err
}

// InsertGitCommitResult records a git commit and returns the number of rows
// actually inserted (0 when the row already existed, 1 when new).
func InsertGitCommitResult(database *sql.DB, commit *models.GitCommit) (int64, error) {
	return insertGitCommit(database, commit)
}

func insertGitCommit(database *sql.DB, commit *models.GitCommit) (int64, error) {
	res, err := database.Exec(`
		INSERT OR IGNORE INTO git_commits (
			commit_hash, session_id, feature_id, tool_event_id, message, timestamp
		) VALUES (?, ?, ?, ?, ?, ?)`,
		commit.CommitHash,
		commit.SessionID,
		nullStr(commit.FeatureID),
		nullStr(commit.ToolEventID),
		nullStr(commit.Message),
		commit.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert git commit %s: %w", commit.CommitHash, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// GetCommitsByFeature returns all git commits linked to a feature, ordered by timestamp DESC.
func GetCommitsByFeature(database *sql.DB, featureID string) ([]models.GitCommit, error) {
	rows, err := database.Query(`
		SELECT commit_hash, session_id, feature_id, tool_event_id, message, timestamp
		FROM git_commits
		WHERE feature_id = ?
		ORDER BY timestamp DESC`, featureID)
	if err != nil {
		return nil, fmt.Errorf("get commits for feature %s: %w", featureID, err)
	}
	defer rows.Close()

	var commits []models.GitCommit
	for rows.Next() {
		var c models.GitCommit
		var tsStr string
		var featID, toolEventID, message sql.NullString
		if err := rows.Scan(
			&c.CommitHash, &c.SessionID, &featID, &toolEventID, &message, &tsStr,
		); err != nil {
			return nil, err
		}
		c.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
		c.FeatureID = featID.String
		c.ToolEventID = toolEventID.String
		c.Message = message.String
		commits = append(commits, c)
	}
	return commits, rows.Err()
}

// CodeBearingPaths returns the distinct non-.wipnote file paths recorded
// against an item in feature_files. The feature_files table is keyed by a
// generic item ID column (feature_id), so this works type-agnostically for
// features, bugs, and spikes alike.
//
// projectRoot is the absolute path to the repository root (i.e. the parent
// of the .wipnote/ directory). Paths that are outside the project root
// (e.g. /tmp/foo.yaml) and paths with the "unresolved:" sentinel prefix are
// excluded in addition to .wipnote-scoped paths.
//
// An item is "code-bearing" iff this returns a non-empty slice: its trace
// touched at least one source path outside .wipnote/. Pure-.wipnote/doc
// items (or items with no recorded files) return an empty slice and are
// exempt from the provenance completion gate.
func CodeBearingPaths(database *sql.DB, featureID string, projectRoot string) ([]string, error) {
	rows, err := database.Query(`
		SELECT DISTINCT file_path
		FROM feature_files
		WHERE feature_id = ?
		ORDER BY file_path`, featureID)
	if err != nil {
		return nil, fmt.Errorf("code-bearing paths for %s: %w", featureID, err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if isWipnoteScopedPath(p) {
			continue
		}
		if isOutsideProjectPath(p, projectRoot) {
			continue
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// isWipnoteScopedPath reports whether a recorded file path is internal to
// the .wipnote canonical store (or its rendered dashboard assets) and so
// does NOT count as source code for provenance purposes.
func isWipnoteScopedPath(p string) bool {
	p = strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
	return p == ".wipnote" || strings.HasPrefix(p, ".wipnote/")
}

// isOutsideProjectPath reports whether a recorded file path should be excluded
// because it is clearly outside the project repository. Two cases are handled:
//
//  1. The "unresolved:" sentinel prefix — these are paths that couldn't be
//     resolved to the repo at recording time and are not project source.
//  2. Absolute paths that are not under projectRoot — e.g. /tmp/foo.yaml or
//     a scratch file in a different home directory.
//
// Relative paths (the common case for in-project files) are always considered
// in-project and return false. If projectRoot is empty the absolute-path check
// is skipped.
func isOutsideProjectPath(p, projectRoot string) bool {
	if strings.HasPrefix(p, "unresolved:") {
		return true
	}
	if filepath.IsAbs(p) && projectRoot != "" {
		clean := filepath.Clean(p)
		root := filepath.Clean(projectRoot)
		// Accept the root itself or anything strictly under it.
		if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// TraceResult holds the result of tracing a commit back through the attribution chain.
type TraceResult struct {
	CommitHash string
	Message    string
	SessionID  string
	FeatureID  string
	TrackID    string
}

// TraceCommit looks up a commit SHA (prefix match) and returns the attribution
// chain: commit → session → feature → track.
func TraceCommit(database *sql.DB, sha string) ([]TraceResult, error) {
	rows, err := database.Query(`
		SELECT gc.commit_hash, COALESCE(gc.message, ''),
		       gc.session_id, COALESCE(gc.feature_id, ''),
		       COALESCE(f.track_id, '')
		FROM git_commits gc
		LEFT JOIN features f ON f.id = gc.feature_id
		WHERE gc.commit_hash LIKE ? || '%'
		ORDER BY gc.timestamp DESC`, sha)
	if err != nil {
		return nil, fmt.Errorf("trace commit %s: %w", sha, err)
	}
	defer rows.Close()

	var results []TraceResult
	for rows.Next() {
		var r TraceResult
		if err := rows.Scan(&r.CommitHash, &r.Message, &r.SessionID, &r.FeatureID, &r.TrackID); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// CommitAttributionRate returns (total commits, commits with non-empty feature_id).
func CommitAttributionRate(database *sql.DB) (total, attributed int) {
	database.QueryRow(`SELECT COUNT(*) FROM git_commits`).Scan(&total)
	database.QueryRow(`SELECT COUNT(*) FROM git_commits WHERE feature_id IS NOT NULL AND feature_id != ''`).Scan(&attributed)
	return
}

// GetCommitsBySession returns all git commits linked to a session,
// ordered by timestamp DESC.
func GetCommitsBySession(database *sql.DB, sessionID string) ([]models.GitCommit, error) {
	rows, err := database.Query(`
		SELECT commit_hash, session_id, feature_id, tool_event_id, message, timestamp
		FROM git_commits WHERE session_id = ?
		ORDER BY timestamp DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get commits for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var commits []models.GitCommit
	for rows.Next() {
		var c models.GitCommit
		var tsStr string
		var featID, toolEventID, message sql.NullString
		if err := rows.Scan(
			&c.CommitHash, &c.SessionID, &featID, &toolEventID, &message, &tsStr,
		); err != nil {
			return nil, err
		}
		c.Timestamp, _ = time.Parse(time.RFC3339, tsStr)
		c.FeatureID = featID.String
		c.ToolEventID = toolEventID.String
		c.Message = message.String
		commits = append(commits, c)
	}
	return commits, rows.Err()
}
