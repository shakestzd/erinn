package hooks

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
)

// SessionStart handles the SessionStart Claude Code hook event.
// It upserts a session row in SQLite and writes environment variables for
// downstream hooks via CLAUDE_ENV_FILE.
func SessionStart(event *CloudEvent, database *sql.DB, projectDir string) (*HookResult, error) {
	sessionID := NormaliseSessionID(event.SessionID)
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	now := time.Now().UTC()

	// Launch headCommit in a goroutine — I/O-bound, no data dependency with writeEnvVars.
	commitCh := make(chan string, 1)
	go func() {
		commitCh <- headCommit(projectDir)
	}()

	// Propagate session ID to downstream hooks while git is running.
	writeEnvVars(sessionID, projectDir)

	// Wait for git result — upsertSession needs the commit hash.
	startCommit := <-commitCh

	// Upsert: insert or ignore on conflict (session may already exist on resume).
	s := &models.Session{
		SessionID:       sessionID,
		AgentAssigned:   agentName(),
		Status:          "active",
		CreatedAt:       now,
		StartCommit:     startCommit,
		IsSubagent:      isSubagent(),
		Model:           os.Getenv("CLAUDE_MODEL"),
		ParentSessionID: os.Getenv("HTMLGRAPH_PARENT_SESSION"),
		ParentEventID:   os.Getenv("HTMLGRAPH_PARENT_EVENT"),
	}

	_ = upsertSession(database, s) // Non-fatal: never block Claude

	return &HookResult{
		Continue: true,
		AdditionalContext: fmt.Sprintf(
			"[HtmlGraph] Session %s started. Project: %s",
			sessionID[:8], projectDir,
		),
	}, nil
}

// upsertSession inserts the session row, ignoring duplicate-key conflicts.
func upsertSession(database *sql.DB, s *models.Session) error {
	_, err := database.Exec(`
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, parent_session_id, parent_event_id,
			 created_at, status, start_commit, is_subagent, model, active_feature_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID,
		s.AgentAssigned,
		nullableStr(s.ParentSessionID),
		nullableStr(s.ParentEventID),
		s.CreatedAt.UTC().Format(time.RFC3339),
		s.Status,
		nullableStr(s.StartCommit),
		s.IsSubagent,
		nullableStr(s.Model),
		nullableStr(s.ActiveFeatureID),
	)
	return err
}

// writeEnvVars appends session context exports to CLAUDE_ENV_FILE.
func writeEnvVars(sessionID, projectDir string) {
	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		return
	}
	f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	lines := []string{
		"export HTMLGRAPH_SESSION_ID=" + sessionID,
		"export HTMLGRAPH_PARENT_SESSION=" + sessionID,
		"export HTMLGRAPH_PARENT_AGENT=claude-code",
		"export HTMLGRAPH_NESTING_DEPTH=0",
	}
	if projectDir != "" {
		lines = append(lines, "export CLAUDE_PROJECT_DIR="+projectDir)
	}
	f.WriteString(strings.Join(lines, "\n") + "\n")
}

// headCommit returns the short HEAD git hash, or empty string on failure.
func headCommit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// agentName returns the agent identifier for this session.
func agentName() string {
	if v := os.Getenv("HTMLGRAPH_AGENT"); v != "" {
		return v
	}
	return "claude-code"
}

// isSubagent returns true when env vars indicate this is a spawned subagent.
func isSubagent() bool {
	return os.Getenv("HTMLGRAPH_PARENT_SESSION") != "" &&
		os.Getenv("HTMLGRAPH_NESTING_DEPTH") != "0"
}

// nullableStr converts an empty string to a typed nil for sql.NullString use.
// We pass the raw string and rely on the db.nullStr helper via the db package;
// here we return sql.NullString directly for convenience.
func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// GetActiveFeatureID looks up the active_feature_id for a session.
func GetActiveFeatureID(database *sql.DB, sessionID string) string {
	var featID sql.NullString
	row := database.QueryRow(
		`SELECT active_feature_id FROM sessions WHERE session_id = ?`, sessionID,
	)
	_ = row.Scan(&featID)
	return featID.String
}

// UpdateActiveFeature sets active_feature_id on the session row.
func UpdateActiveFeature(database *sql.DB, sessionID, featureID string) error {
	_, err := database.Exec(
		`UPDATE sessions SET active_feature_id = ?, updated_at = ? WHERE session_id = ?`,
		nullableStr(featureID), time.Now().UTC().Format(time.RFC3339), sessionID,
	)
	return err
}

// ensure db package is referenced (used via db.nullStr in other files).
var _ = db.InsertSession
