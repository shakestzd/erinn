package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/internal/agent"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/internal/otel"
	"github.com/shakestzd/wipnote/internal/otel/sink/ndjson"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/internal/worktree"
)

// ActiveSessionData is the JSON structure written to .wipnote/.active-session
// as a fallback propagation mechanism when CLAUDE_ENV_FILE is unset (worktree
// subagents). All fields mirror what writeEnvVars() exports via CLAUDE_ENV_FILE.
type ActiveSessionData struct {
	SessionID     string  `json:"session_id"`
	ParentSession string  `json:"parent_session,omitempty"`
	ParentAgent   string  `json:"parent_agent,omitempty"`
	NestingDepth  int     `json:"nesting_depth"`
	ProjectDir    string  `json:"project_dir,omitempty"`
	GitRemoteURL  string  `json:"git_remote_url,omitempty"`
	Timestamp     float64 `json:"timestamp"`
}

// WriteActiveSession writes session context to .wipnote/.active-session so
// worktree subagent hooks can read session ID even when CLAUDE_ENV_FILE is unset.
//
// Writes are atomic (write-to-temp + rename) so concurrent readers never see
// a torn/empty file, and concurrent writers cannot corrupt each other
// (bug-d2d3fb3f: parallel agents stomped .active-session).
func WriteActiveSession(sessionID, projectDir string) {
	if projectDir == "" {
		return
	}
	data := ActiveSessionData{
		SessionID:     sessionID,
		ParentSession: sessionID,
		ParentAgent:   "claude-code",
		NestingDepth:  0,
		ProjectDir:    projectDir,
		GitRemoteURL:  paths.GetGitRemoteURL(projectDir),
		Timestamp:     float64(time.Now().UnixNano()) / 1e9,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	dir := filepath.Join(projectDir, ".wipnote")
	target := filepath.Join(dir, ".active-session")
	tmp, err := os.CreateTemp(dir, ".active-session.tmp-*")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	_ = os.Chmod(tmpPath, 0o644)
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
	}
}

// ReadActiveSession reads session context from .wipnote/.active-session.
// Returns nil when the file doesn't exist or can't be parsed.
func ReadActiveSession(projectDir string) *ActiveSessionData {
	if projectDir == "" {
		return nil
	}
	path := filepath.Join(projectDir, ".wipnote", ".active-session")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data ActiveSessionData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	return &data
}

// launchModeFile is the JSON structure written to .wipnote/.launch-mode by
// `wipnote claude`. It records how the current Claude session was started.
type launchModeFile struct {
	Mode      string `json:"mode"`
	PID       int    `json:"pid"`
	Timestamp string `json:"timestamp"`
}

// bareLaunchNudge returns a context nudge when Claude was started without
// `wipnote claude` (i.e. .launch-mode is missing or older than 30 seconds).
// Returns an empty string when the orchestrator system prompt is already active.
func bareLaunchNudge(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	path := filepath.Join(projectDir, ".wipnote", ".launch-mode")
	info, err := os.Stat(path)
	if err == nil {
		// File exists — check if it was written within the last 30 seconds.
		if time.Since(info.ModTime()) <= 30*time.Second {
			return ""
		}
	}
	return "wipnote plugin is active in this project. For the best experience with orchestrated delegation, " +
		"work tracking, and quality gates, use the /wipnote:orchestrator-directives-skill for guidance " +
		"on how to delegate work, select models, and manage tasks. You can also start sessions with " +
		"`wipnote claude` for automatic orchestrator mode."
}

// SessionStart handles the SessionStart Claude Code hook event.
// It upserts a session row in SQLite and writes environment variables for
// downstream hooks via CLAUDE_ENV_FILE.
func SessionStart(event *CloudEvent, database *sql.DB, projectDir string) (*HookResult, error) {
	handlerStart := time.Now()

	// Install .wipnote/.gitignore on every session start (harness-neutral, idempotent).
	// This ensures adopter projects receive the runtime-artifact gitignore policy on
	// their first launch after wipnote is installed or upgraded, without requiring any
	// manual step. The call is a no-op when the file already exists.
	worktree.EnsureWipnoteGitignore(projectDir)

	sessionID := resolveSessionIDWithHarness(event)
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	now := time.Now().UTC()
	shortID := sessionID[:minSessionLen(sessionID)]

	// Repair stale worktree .git gitdir pointer before any git operations.
	// When a worktree is created on one machine (e.g. macOS) and opened on
	// another (e.g. Linux devcontainer), the absolute path in the .git file
	// becomes stale. Repair it now so all downstream git commands succeed.
	if event.CWD != "" && event.CWD != projectDir {
		if err := worktree.RepairGitdirFromRepoRoot(event.CWD, projectDir); err != nil {
			debugLog(projectDir, "[session-start] worktree gitdir repair failed (cwd=%s): %v", event.CWD, err)
		}
	}

	// Launch headCommit in a goroutine — I/O-bound, no data dependency with writeEnvVars.
	commitCh := make(chan string, 1)
	go func() {
		commitCh <- headCommit(projectDir)
	}()

	// Fire-and-forget disk-retention sweep: rotate oversized logs and
	// archive+prune raw events.ndjson for OLD, inactive, fully-ingested
	// sessions. This is the natural session-start cleanup point so reclamation
	// happens even when `wipnote serve` is not running. The current session is
	// passed as active so its own ndjson is never touched; the sweep is
	// idempotent and fail-safe (no-op on any error). Backgrounded so it never
	// blocks the hot hook path.
	go runRetentionSweep(projectDir, sessionID)

	// Propagate session ID to downstream hooks while git is running.
	writeEnvVars(sessionID, projectDir)

	// Emit the Rosetta correlation event: maps launcher-minted WIPNOTE_SESSION_ID
	// to Claude Code's own session_id so the dashboard can follow --resume flows.
	emitRosettaEvent(projectDir, os.Getenv("WIPNOTE_SESSION_ID"), event.SessionID)

	// Wait for git result — upsertSession needs the commit hash.
	startCommit := <-commitCh

	s := &models.Session{
		SessionID:     sessionID,
		AgentAssigned: resolveEventAgentID(event),
		Status:        "active",
		CreatedAt:     now,
		StartCommit:   startCommit,
		IsSubagent:    isSubagentEvent(event) || isSubagent(),
		Model:         os.Getenv("CLAUDE_MODEL"),
		// TODO(bug-cb4918d8): remove after lineage wiring verified end-to-end.
		// These env vars are NEVER set in subagent hook contexts (confirmed via
		// /tmp/wipnote-hook-trace.jsonl); lineage now flows through the
		// subagent-start hook writing sessions+agent_lineage_trace directly.
		ParentSessionID: os.Getenv("WIPNOTE_PARENT_SESSION"),
		ParentEventID:   os.Getenv("WIPNOTE_PARENT_EVENT"),
		GitRemoteURL: paths.GetGitRemoteURL(projectDir),
		// Normalize to repo-relative so session records remain stable across
		// worktrees and machines. Local sessions get a relative path (e.g. ".");
		// sessions ingested from foreign machines (where the canonical root
		// differs from the local repo) are stored with an "unresolved:" prefix
		// so they are queryable without silently mangling the original path.
		ProjectDir: paths.NormalizeProjectDir(projectDir),
	}

	// Prefer CloudEvent fields over env vars (more reliable).
	if event.Model != "" {
		s.Model = event.Model
	}

	// Provenance — capture which harness/model/role/CLI started this session
	// so downstream consumers can attribute it across handoffs (feat-40ef1333).
	prov := provenance.Detect()
	if prov.Agent == "" {
		prov.Agent = s.AgentAssigned
	}
	if event.Model != "" {
		prov.Model = event.Model
	}
	s.CreatedByAgent = prov.Agent
	s.CreatedByModel = prov.Model
	s.CreatedByRole = prov.Role
	s.CreatedByCLIVersion = prov.CLIVersion

	// Resolve lineage inputs before opening the transaction (read-only queries).
	var inp *lineageInputs
	if s.IsSubagent && s.ParentSessionID != "" {
		featureID := GetActiveFeatureID(database, s.SessionID)
		inp = resolveParentLineage(event, database, s.ParentSessionID, featureID)
	}

	// Batch all writes into a single transaction: session upsert + lineage inserts.
	txStart := time.Now()
	if err := runSessionTransaction(database, s, inp); err != nil {
		debugLog(projectDir, "[session-start] transaction failed (session=%s): %v", shortID, err)
	}
	LogTimed(projectDir, "session-start", map[string]string{
		"phase":   "db-tx",
		"session": shortID,
	}, txStart, "transaction complete")

	// Write canonical session HTML file (non-critical, errors silently logged).
	CreateSessionHTML(projectDir, s)

	// Sweep orphans from any previous sessions in this project — closes out
	// tool calls that crashed mid-flight so session history stays consistent.
	SweepOrphanedEventsForProject(database, projectDir)

	LogTimed(projectDir, "session-start", map[string]string{
		"session": shortID,
	}, handlerStart, "handler complete")

	// Store transcript path if provided by CloudEvent.
	if event.TranscriptPath != "" {
		_, _ = database.Exec(`UPDATE sessions SET transcript_path = ? WHERE session_id = ?`,
			event.TranscriptPath, sessionID)
	}

	// Persist the session-start event to agent_events for dashboard activity feed.
	ev := &models.AgentEvent{
		EventID:      uuid.New().String(),
		AgentID:      s.AgentAssigned,
		EventType:    models.EventStart,
		Timestamp:    now,
		ToolName:     "SessionStart",
		InputSummary: "Session started",
		SessionID:    sessionID,
		Status:       "recorded",
		Source:       "hook",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.InsertEvent(database, ev); err != nil {
		debugLog(projectDir, "[error] handler=session-start session=%s: insert event: %v", shortID, err)
	}

	// Session-family continuity (slice-4, feat-a225ce7c):
	// Wire the harness-neutral session_family_id for Claude, Codex, and Gemini.
	// Launchers inject WIPNOTE_SESSION_FAMILY_ID; the hook is the authoritative
	// DB write path for all harnesses. Per-session state + the family index are
	// also written here as a durability layer (they survive hook restarts).
	{
		familyID := os.Getenv("WIPNOTE_SESSION_FAMILY_ID")
		if familyID == "" {
			// No family env — treat this session as its own family.
			familyID = sessionID
		}
		// DB write: set session_family_id on this session row.
		if err := db.SetSessionFamilyID(database, sessionID, familyID); err != nil {
			debugLog(projectDir, "[session-start] set session_family_id: %v", err)
		}
		// File writes: family index + per-session state (harness-neutral).
		agentID := s.AgentAssigned
		_ = agent.RegisterSessionFamily(projectDir, sessionID, familyID)
		_ = agent.WriteSessionState(projectDir, sessionID, agentID, familyID)
	}

	// Surface (and consume) any durable reconcile warnings persisted by a
	// prior Gemini/Codex session exit (slice-5, feat-f93fe770). This is the
	// non-blocking counterpart to the Claude exit-2 path: the user-never-
	// returns case is recorded at session exit and rendered here on return.
	reconcilePrefix := DrainReconcileWarnings(projectDir)

	// Warn the user when the CLI and plugin versions have drifted.
	warning := versionMismatchWarning()
	if warning != "" {
		debugLog(projectDir, "[session-start] version mismatch detected: %s", warning)
		return &HookResult{AdditionalContext: joinReconcileContext(reconcilePrefix, warning)}, nil
	}

	// Emit full attribution block at session start (once per session).
	// This includes: intro + open work items roster + CLI quick-ref + required flags.
	attribution := buildSessionStartAttribution(database)
	if attribution != "" {
		return &HookResult{AdditionalContext: joinReconcileContext(reconcilePrefix, attribution)}, nil
	}

	// Fallback nudge if no attribution block was generated (no open items).
	// This nudge uses the same "wipnote plugin is active..." message.
	if nudge := bareLaunchNudge(projectDir); nudge != "" {
		return &HookResult{AdditionalContext: joinReconcileContext(reconcilePrefix, nudge)}, nil
	}

	if reconcilePrefix != "" {
		return &HookResult{AdditionalContext: reconcilePrefix}, nil
	}

	return &HookResult{}, nil
}

// joinReconcileContext prepends a non-empty durable-reconcile warning block to
// the rest of the SessionStart additionalContext, separated by a blank line.
// Returns rest unchanged when there is no reconcile prefix.
func joinReconcileContext(reconcilePrefix, rest string) string {
	if reconcilePrefix == "" {
		return rest
	}
	if rest == "" {
		return reconcilePrefix
	}
	return reconcilePrefix + "\n\n" + rest
}

// lineageInputs holds pre-resolved data needed to insert lineage traces inside
// the transaction. All fields are computed from read-only DB queries before
// the transaction begins.
type lineageInputs struct {
	featureID       string
	rootSessionID   string
	parentSessionID string
	depth           int
	path            []string
	parentAgent     string
	myAgent         string
	needsRootSeed   bool
}

// resolveParentLineage reads the parent's lineage record and builds the inputs
// for the child trace. Pure reads — must be called before the transaction.
func resolveParentLineage(event *CloudEvent, database *sql.DB, parentSessionID, featureID string) *lineageInputs {
	parent, _ := db.GetLineageBySession(database, parentSessionID)
	inp := &lineageInputs{
		myAgent:         resolveEventAgentID(event),
		featureID:       featureID,
		parentSessionID: parentSessionID,
	}

	if parent != nil {
		inp.rootSessionID = parent.RootSessionID
		inp.depth = parent.Depth + 1
		inp.path = make([]string, len(parent.Path)+1)
		copy(inp.path, parent.Path)
		inp.path[len(parent.Path)] = inp.myAgent
	} else {
		// No parent trace: treat parent as root and seed its entry.
		inp.rootSessionID = parentSessionID
		inp.depth = 1
		inp.parentAgent = "claude-code"
		inp.path = []string{inp.parentAgent, inp.myAgent}
		inp.needsRootSeed = true
	}
	return inp
}

// runSessionTransaction batches the session upsert and optional lineage inserts
// into a single SQLite transaction, reducing per-operation journal sync overhead.
func runSessionTransaction(database *sql.DB, s *models.Session, inp *lineageInputs) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if err := upsertSessionTx(tx, s); err != nil {
		return err
	}

	if inp != nil {
		if err := insertLineageTracesTx(tx, inp, s.SessionID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// upsertSessionTx inserts the session row within a transaction,
// ignoring duplicate-key conflicts (session may already exist on resume).
func upsertSessionTx(tx *sql.Tx, s *models.Session) error {
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, parent_session_id, parent_event_id,
			 created_at, status, start_commit, is_subagent, model, active_feature_id,
			 git_remote_url, project_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		nullableStr(s.GitRemoteURL),
		nullableStr(s.ProjectDir),
	)
	return err
}

// insertLineageTracesTx inserts lineage trace rows within an existing transaction.
func insertLineageTracesTx(tx *sql.Tx, inp *lineageInputs, sessionID string) error {
	now := time.Now().UTC()

	if inp.needsRootSeed {
		rootTrace := &models.LineageTrace{
			TraceID:       inp.parentSessionID,
			RootSessionID: inp.parentSessionID,
			SessionID:     inp.parentSessionID,
			AgentName:     inp.parentAgent,
			Depth:         0,
			Path:          []string{inp.parentAgent},
			FeatureID:     inp.featureID,
			StartedAt:     now,
			Status:        "active",
		}
		if err := db.InsertLineageTraceExecer(tx, rootTrace); err != nil {
			return err
		}
	}

	trace := &models.LineageTrace{
		TraceID:       sessionID,
		RootSessionID: inp.rootSessionID,
		SessionID:     sessionID,
		AgentName:     inp.myAgent,
		Depth:         inp.depth,
		Path:          inp.path,
		FeatureID:     inp.featureID,
		StartedAt:     now,
		Status:        "active",
	}
	return db.InsertLineageTraceExecer(tx, trace)
}

// upsertSession inserts the session row, ignoring duplicate-key conflicts.
// Kept for test compatibility (session_start_test.go calls it directly).
func upsertSession(database *sql.DB, s *models.Session) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := upsertSessionTx(tx, s); err != nil {
		return err
	}
	return tx.Commit()
}

// writeEnvVars appends session context exports to CLAUDE_ENV_FILE and always
// writes .wipnote/.active-session as a backup. The .active-session file
// ensures downstream hooks can resolve the session ID even when CLAUDE_ENV_FILE
// is unavailable (YOLO mode, worktree subagents, plugin-dir launches).
func writeEnvVars(sessionID, projectDir string) {
	// Always write .active-session as backup — prevents stale session IDs.
	WriteActiveSession(sessionID, projectDir)

	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		debugLog(projectDir, "[wipnote] CLAUDE_ENV_FILE unset — using .active-session only (session_id=%s)", sessionID)
		return
	}
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		debugLog(projectDir, "[wipnote] failed to create CLAUDE_ENV_FILE dir %s: %v", filepath.Dir(envFile), err)
		return
	}
	f, err := os.OpenFile(envFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		debugLog(projectDir, "[wipnote] failed to open CLAUDE_ENV_FILE %s: %v", envFile, err)
		return
	}
	defer f.Close()

	lines := []string{
		"export WIPNOTE_SESSION_ID=" + sessionID,
		"export WIPNOTE_PARENT_SESSION=" + sessionID,
		"export WIPNOTE_PARENT_AGENT=claude-code",
		"export WIPNOTE_NESTING_DEPTH=0",
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

// isSubagent returns true when env vars indicate this is a spawned subagent.
// Falls back to checking .active-session when env vars are absent (worktrees).
func isSubagent() bool {
	if os.Getenv("WIPNOTE_PARENT_SESSION") != "" {
		return os.Getenv("WIPNOTE_NESTING_DEPTH") != "0"
	}
	// Env vars not set — check if .active-session was written by the parent.
	// Worktree subagents get a fresh environment so WIPNOTE_PARENT_SESSION
	// won't be propagated, but the .active-session file is project-scoped.
	return false
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

// buildSessionStartAttribution returns the full attribution block: open work
// items roster, CLI quick-ref, and required flags reminder. Emitted once per
// session in SessionStart. Returns empty string if there are no open work items
// to reference, allowing bareLaunchNudge to decide whether to emit a nudge.
func buildSessionStartAttribution(database *sql.DB) string {
	// List all open work items.
	open := listOpenWorkItems(database)
	if len(open) == 0 {
		// No open items — return empty to let SessionStart fall through to
		// bareLaunchNudge, which decides whether to emit the nudge based on
		// launch-mode detection.
		return ""
	}

	// Build the intro.
	lines := []string{
		"wipnote plugin is active in this project. For the best experience with orchestrated delegation, " +
			"work tracking, and quality gates, use the /wipnote:orchestrator-directives-skill for guidance " +
			"on how to delegate work, select models, and manage tasks. You can also start sessions with " +
			"`wipnote claude` for automatic orchestrator mode.",
		"",
	}

	lines = append(lines, "## Work Item Attribution (CIGS)", "")
	lines = append(lines, "**Open work items** — run `wipnote feature start <id>`:")
	for _, item := range open {
		lines = append(lines, fmt.Sprintf("  `%s` — %s [%s]", item.id, item.title, item.status))
	}
	lines = append(lines, "", compactCLIRef)

	return joinLines(lines)
}

// emitRosettaEvent writes a session_start NDJSON line correlating the
// launcher-minted WIPNOTE_SESSION_ID with Claude Code's own session_id.
// This is the "Rosetta stone" record that lets the dashboard map a
// `claude --resume <id>` back to the originating wipnote session.
//
// The event is written only when WIPNOTE_SESSION_ID is set (i.e. the
// session was started via `wipnote claude`). If it is unset, or if the
// session directory cannot be created, the function returns silently.
func emitRosettaEvent(projectDir, wipnoteSID, claudeSessionID string) {
	if wipnoteSID == "" {
		return // not a launcher-managed session; skip silently
	}

	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", wipnoteSID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		debugLog(projectDir, "[session-start] rosetta: mkdir session dir: %v", err)
		return
	}

	snk, err := ndjson.New(projectDir, wipnoteSID)
	if err != nil {
		debugLog(projectDir, "[session-start] rosetta: create ndjson sink: %v", err)
		return
	}
	// Close flushes the in-memory buffer + fsyncs before the hook process exits.
	// Without this a single-event write stays in memory and is lost — the 2s
	// periodic ticker never fires in a short-lived hook process.
	defer func() {
		if err := snk.Close(); err != nil {
			debugLog(projectDir, "[session-start] rosetta: close ndjson sink: %v", err)
		}
	}()

	sig := otel.UnifiedSignal{
		Harness:       "wipnote",
		SignalID:      "session-start-" + wipnoteSID,
		Kind:          otel.KindLog,
		CanonicalName: otel.CanonicalSessionStart,
		NativeName:    "session_start",
		Timestamp:     time.Now().UTC(),
		SessionID:     wipnoteSID,
		RawAttrs: map[string]any{
			"wipnote_sid":       wipnoteSID,
			"claude_session_id": claudeSessionID,
		},
	}
	if err := snk.WriteBatch(context.Background(), "wipnote", nil, []otel.UnifiedSignal{sig}); err != nil {
		debugLog(projectDir, "[session-start] rosetta: write event: %v", err)
	}
}

// ensure db package is referenced (used via db.nullStr in other files).
var _ = db.InsertSession
