package hooks

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/eventsink"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
	"github.com/shakestzd/wipnote/core/provenance"
	"github.com/shakestzd/wipnote/core/worktree"
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

var allowedHarnesses = map[string]struct{}{
	"claude":      {},
	"codex":       {},
	"gemini":      {},
	"antigravity": {},
}

// bareLaunchNudge returns an extra startup hint when Claude was started without
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
	return "Start sessions with `wipnote claude` for automatic orchestrator mode."
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
	if RetentionSweepFn != nil {
		go RetentionSweepFn(projectDir, sessionID)
	}

	// Propagate session ID to downstream hooks while git is running.
	writeEnvVars(sessionID, projectDir)

	// Emit the Rosetta correlation event: maps launcher-minted OTel session ID
	// to Claude Code's own session_id so the dashboard can follow --resume flows.
	// WIPNOTE_OTEL_SESSION_ID carries the OTel collector's 28-char hex ID minted
	// by the launcher; WIPNOTE_SESSION_ID is now reserved for the real Claude Code
	// session identity (set by writeEnvVars after this point). See bug-b262d303.
	emitRosettaEvent(projectDir, os.Getenv("WIPNOTE_OTEL_SESSION_ID"), event.SessionID)

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
		GitRemoteURL:    paths.GetGitRemoteURL(projectDir),
		// Normalize to repo-relative so session records remain stable across
		// worktrees and machines. Local sessions get a relative path (e.g. ".");
		// sessions ingested from foreign machines (where the canonical root
		// differs from the local repo) are stored with an "unresolved:" prefix
		// so they are queryable without silently mangling the original path.
		ProjectDir:       paths.NormalizeProjectDir(projectDir),
		ExecWorktreePath: execWorktreeRelPath(event.CWD, projectDir),
		Branch:           gitBranch(execDirOrDefault(event.CWD, projectDir)),
		Harness:          resolveHarness(),
		ContinuedFrom:    strings.TrimSpace(os.Getenv("WIPNOTE_CONTINUED_FROM")),
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

	// Persist the session row and (for subagents) its lineage traces through the
	// writer daemon (enqueue-only) instead of a direct writable transaction.
	//
	// TX-atomicity note (plan-2390966a slice-3): the old code wrapped the session
	// upsert + lineage inserts in ONE SQLite transaction. The daemon apply loop
	// does NOT auto-wrap a transaction, and a multi-statement batch cannot be a
	// single OpTypeSQL op, so we route each statement as its OWN enqueue-only op.
	// Atomicity-of-effect is preserved differently: every op lands on the SINGLE
	// writer in FIFO order, so the session insert always applies before the
	// lineage traces that reference it — a reader never sees a lineage row whose
	// session is missing. The all-or-nothing guarantee of a SQL transaction is
	// relaxed to "FIFO-ordered single-writer + canonical NDJSON/reindex backstop",
	// which is the established hot-hook contract (RouteHookWrite, slice-2).
	txStart := time.Now()
	routeSessionUpsert(projectDir, sessionID, s)
	if inp != nil {
		routeLineageTraces(projectDir, sessionID, inp)
	}
	LogTimed(projectDir, "session-start", map[string]string{
		"phase":   "db-tx",
		"session": shortID,
	}, txStart, "session+lineage routed")

	// Record the session process-liveness anchor (feat-0a7db952, slice-1).
	// Writing the owning pid (+ /proc start-time) lets the reaper distinguish a
	// crashed session from a long-idle LIVE one. Best-effort: a write failure
	// must NEVER block or fail the hook — when the owner is unresolvable we omit
	// .session-pid entirely so the session safe-degrades to LIVE (never reaped).
	if ownerPID, ok := resolveOwningPID(projectDir); ok {
		sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
		if err := writeSessionPID(sessDir, ownerPID); err != nil {
			debugLog(projectDir, "[session-start] writeSessionPID failed (session=%s pid=%d): %v", shortID, ownerPID, err)
		}
	}

	// Write canonical session HTML file (non-critical, errors silently logged).
	CreateSessionHTML(projectDir, s)

	// Sweep orphans from any previous sessions in this project — closes out
	// tool calls that crashed mid-flight so session history stays consistent.
	// BOUNDED on the hot path (bug-504095f2): cap the number of orphans
	// processed so a large crash backlog cannot stall the launcher behind a
	// multi-second sweep. The serve-side periodic drain clears any remainder
	// out-of-band; when the cap is hit we log the residual for observability.
	if appended, discovered := SweepOrphanedEventsForProjectCapped(database, projectDir, SessionStartSweepCap); discovered >= SessionStartSweepCap {
		debugLog(projectDir, "[session-start] capped orphan sweep at %d (appended=%d); backlog drains via serve",
			SessionStartSweepCap, appended)
	}

	LogTimed(projectDir, "session-start", map[string]string{
		"session": shortID,
	}, handlerStart, "handler complete")

	// Store transcript path if provided by CloudEvent. Routed enqueue-only so a
	// held write lock never stalls the post-selection critical path.
	if event.TranscriptPath != "" {
		RouteHookWrite("session-start", projectDir, sessionID,
			`UPDATE sessions SET transcript_path = ? WHERE session_id = ?`,
			event.TranscriptPath, sessionID)
	}

	// Persist the session-start event to agent_events for dashboard activity feed.
	// Routed enqueue-only through the daemon (no direct writable Exec). The event
	// has no ParentEventID, so db.InsertEvent's parent-agent lookup is a no-op and
	// the plain parameterized INSERT below is equivalent.
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
	routeInsertEvent(projectDir, sessionID, ev)

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
		//
		// roborev-473 finding 5: this update has an IMMEDIATE in-process consumer —
		// routeFamilyAttribution (below) reads the family's members via
		// db.GetSessionsByFamily, which selects on session_family_id. An ENQUEUE-ONLY
		// write would not yet be applied when that read runs, so this row would be
		// missing from the member set and sibling work-item attribution would be
		// silently skipped. This is a once-per-subagent-session, low-frequency write
		// whose consumer is coupled to it, so CORRECTNESS WINS over the <1s target:
		// route it APPLIED-ACK via apply.RouteSQL so the row is committed (and thus
		// visible to the propagation read) before we proceed. apply.RouteSQL is
		// bounded by CLISubmitBudget (~2s) — acceptable here. On a daemon miss
		// (RouteSQL returns false) fall back to the direct applied write through the
		// already-held handle so the row is still committed synchronously before the
		// read; canonical NDJSON + reindex remain the durability backstop.
		// familyID is already non-empty (defaulted to sessionID above).
		//
		// On a daemon MISS (RouteSQL returns false) the applied-ack guarantee can't
		// come from the daemon, so we write directly through a BOUNDED own handle
		// (routeFamilyIDDirectBounded, ~750ms busy_timeout) rather than the passed
		// handle's default 5s busy_timeout — under a held external write lock the
		// applied write can't land anyway, and the immediately-following family read
		// would not see it either way, so failing fast keeps session-start responsive
		// instead of stalling 5s. canonical NDJSON + reindex remain the backstop.
		const setFamilySQL = `UPDATE sessions SET session_family_id = ? WHERE session_id = ?`
		if !routeSQLApplied(projectDir, setFamilySQL, familyID, sessionID) {
			routeViaOwnBoundedHandle("session-start", projectDir, sessionID, setFamilySQL, familyID, sessionID)
		}
		// File writes: family index + per-session state (harness-neutral).
		agentID := s.AgentAssigned
		_ = agent.RegisterSessionFamily(projectDir, sessionID, familyID)
		_ = agent.WriteSessionState(projectDir, sessionID, agentID, familyID)

		// Forward-propagate work-item attribution across the family. Claude Code
		// splits a session: this hook fires on the short parent stub (which holds
		// active_feature_id), but the real long-running child session IDs never get
		// a SessionStart the hook observes. Reconciling here on every launch copies
		// the family's work item onto any sibling that still lacks one, so the
		// chooser, dashboard, and analytics all attribute the split children.
		//
		// The reconcile reads members + donor directly (reads stay direct) and
		// routes each recipient UPDATE through the daemon enqueue-only path. We use
		// the count of routed recipients to gate the (best-effort, idempotent) HTML
		// persistence; enqueue-only acks carry no RowsAffected, so this is the
		// would-update count rather than the applied count.
		if n := routeFamilyAttribution(projectDir, database, familyID); n > 0 {
			persistFamilyAttributionHTML(projectDir, database, familyID)
			debugLog(projectDir, "[session-start] propagated work item to %d family sibling(s) of %s", n, shortID)
		}
	}

	// Surface (and consume) any durable reconcile warnings persisted by a
	// prior Gemini/Codex session exit (slice-5, feat-f93fe770). This is the
	// non-blocking counterpart to the Claude exit-2 path: the user-never-
	// returns case is recorded at session exit and rendered here on return.
	reconcilePrefix := DrainReconcileWarnings(projectDir)
	continuePrefix := continueHandoffContextFromEnv()

	// Warn the user when the CLI and plugin versions have drifted.
	warning := versionMismatchWarning()
	if warning != "" {
		debugLog(projectDir, "[session-start] version mismatch detected: %s", warning)
		return &HookResult{AdditionalContext: joinSessionStartContext(continuePrefix, reconcilePrefix, warning)}, nil
	}

	// Emit concise startup guidance at session start. Open-item rosters stay
	// on-demand via the CLI to keep startup context quiet.
	attribution := buildSessionStartAttribution()
	if nudge := bareLaunchNudge(projectDir); nudge != "" {
		attribution = joinSessionStartContext(attribution, nudge)
	}
	return &HookResult{AdditionalContext: joinSessionStartContext(continuePrefix, reconcilePrefix, attribution)}, nil
}

func continueHandoffContextFromEnv() string {
	raw := strings.TrimSpace(os.Getenv("WIPNOTE_CONTINUE_HANDOFF_B64"))
	if raw == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(decoded))
}

func joinSessionStartContext(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return strings.Join(filtered, "\n\n")
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

// routeSessionUpsert enqueues the session-row upsert through the writer daemon
// (RouteHookWrite, enqueue-only). The statement is the exact INSERT OR IGNORE the
// pre-daemon upsertSessionTx ran, so a replay/resume is idempotent (the writer
// IGNOREs the conflicting row). NO direct writable Exec is performed when the
// daemon is reachable; a held lock degrades to the bounded direct fallback inside
// RouteHookWrite (<1s) and ultimately to canonical NDJSON + reindex.
func routeSessionUpsert(projectDir, sessionID string, s *models.Session) {
	RouteHookWrite("session-start", projectDir, sessionID, `
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, parent_session_id, parent_event_id,
			 created_at, status, start_commit, is_subagent, model, active_feature_id,
			 git_remote_url, project_dir, exec_worktree_path, branch, harness, continued_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		nullableStr(s.ExecWorktreePath),
		nullableStr(s.Branch),
		nullableStr(s.Harness),
		nullableStr(s.ContinuedFrom),
	)
}

// routeLineageTraces enqueues the subagent lineage trace inserts through the
// writer daemon, in the SAME order the transactional path wrote them (root seed
// first, then the child trace). FIFO ordering on the single writer guarantees
// they apply after routeSessionUpsert's session row. Each insert is its own
// enqueue-only op (RouteHookWrite) — no direct writable Exec.
func routeLineageTraces(projectDir, sessionID string, inp *lineageInputs) {
	now := time.Now().UTC()
	if inp.needsRootSeed {
		routeLineageTrace(projectDir, sessionID, &models.LineageTrace{
			TraceID:       inp.parentSessionID,
			RootSessionID: inp.parentSessionID,
			SessionID:     inp.parentSessionID,
			AgentName:     inp.parentAgent,
			Depth:         0,
			Path:          []string{inp.parentAgent},
			FeatureID:     inp.featureID,
			StartedAt:     now,
			Status:        "active",
		})
	}
	routeLineageTrace(projectDir, sessionID, &models.LineageTrace{
		TraceID:       sessionID,
		RootSessionID: inp.rootSessionID,
		SessionID:     sessionID,
		AgentName:     inp.myAgent,
		Depth:         inp.depth,
		Path:          inp.path,
		FeatureID:     inp.featureID,
		StartedAt:     now,
		Status:        "active",
	})
}

// routeLineageTrace enqueues a single agent_lineage_trace INSERT, mirroring
// db.insertLineageTrace's statement and arg marshalling (path → JSON).
func routeLineageTrace(projectDir, sessionID string, trace *models.LineageTrace) {
	pathJSON, err := json.Marshal(trace.Path)
	if err != nil {
		debugLog(projectDir, "[session-start] marshal lineage path (trace=%s): %v", trace.TraceID, err)
		return
	}
	RouteHookWrite("session-start", projectDir, sessionID, `
		INSERT INTO agent_lineage_trace
			(trace_id, root_session_id, session_id, agent_name, depth, path,
			 feature_id, started_at, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		trace.TraceID,
		trace.RootSessionID,
		nullableStr(trace.SessionID),
		nullableStr(trace.AgentName),
		trace.Depth,
		string(pathJSON),
		nullableStr(trace.FeatureID),
		trace.StartedAt.UTC().Format(time.RFC3339),
		trace.Status,
	)
}

// routeInsertEvent enqueues the session-start agent_events row through the writer
// daemon, mirroring db.InsertEvent's statement. The session-start event has no
// ParentEventID, so InsertEvent's parent-agent lookup is skipped here (it would
// be a no-op). No direct writable Exec.
func routeInsertEvent(projectDir, sessionID string, e *models.AgentEvent) {
	RouteHookWrite("session-start", projectDir, sessionID, `
		INSERT INTO agent_events (
			event_id, agent_id, event_type, timestamp, tool_name,
			input_summary, tool_input, output_summary, session_id, feature_id,
			parent_agent_id, parent_event_id, subagent_type,
			cost_tokens, execution_duration_seconds, status,
			model, claude_task_id, source, step_id,
			created_at, updated_at
		) VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?)`,
		e.EventID, e.AgentID, string(e.EventType),
		e.Timestamp.UTC().Format(time.RFC3339), nullableStr(e.ToolName),
		nullableStr(e.InputSummary), nullableStr(e.ToolInput), nullableStr(e.OutputSummary),
		e.SessionID, nullableStr(e.FeatureID),
		nullableStr(e.ParentAgentID), nullableStr(e.ParentEventID),
		nullableStr(e.SubagentType),
		e.CostTokens, e.ExecDuration, e.Status,
		nullableStr(e.Model), nullableStr(e.ClaudeTaskID),
		e.Source, nullableStr(e.StepID),
		e.CreatedAt.UTC().Format(time.RFC3339),
		e.UpdatedAt.UTC().Format(time.RFC3339),
	)
}

// routeFamilyAttribution is the daemon-routed analogue of
// db.PropagateFamilyAttribution. It performs the read-modify-write reconcile with
// reads against the read handle and routes each recipient UPDATE through the
// writer daemon (enqueue-only) instead of writing directly. It returns the number
// of recipients it ROUTED an update for (the would-update count) so the caller can
// gate best-effort HTML persistence; because enqueue-only acks carry no
// RowsAffected, this is not guaranteed to equal the applied-row count, but the
// reconcile and HTML persist are both idempotent.
func routeFamilyAttribution(projectDir string, database *sql.DB, familyID string) int {
	if database == nil || strings.TrimSpace(familyID) == "" {
		return 0
	}
	members, err := db.GetSessionsByFamily(database, familyID)
	if err != nil {
		debugLog(projectDir, "[session-start] propagate family attribution (members): %v", err)
		return 0
	}
	if len(members) < 2 {
		return 0 // a family of one has no sibling to donate to or from
	}
	donor := ""
	for _, sid := range members {
		if wi := db.GetActiveWorkItemWithFallback(database, sid, db.AgentRootSentinel); wi != "" {
			donor = wi
			break
		}
	}
	if donor == "" {
		return 0
	}
	now := time.Now().UTC().Format(time.RFC3339)
	routed := 0
	for _, sid := range members {
		if db.GetActiveWorkItemWithFallback(database, sid, db.AgentRootSentinel) != "" {
			continue // already attributed — never clobber
		}
		// Same TOCTOU-guarded predicate as db.PropagateFamilyAttribution: only
		// write when the recipient is still unattributed in BOTH stores. The
		// daemon Execs this as one parameterized statement; the guard runs at
		// apply time so a claim landing between our read and the apply is honored.
		RouteHookWrite("session-start", projectDir, sid,
			`UPDATE sessions SET active_feature_id = ?, updated_at = ?
			 WHERE session_id = ?
			   AND (active_feature_id IS NULL OR active_feature_id = '')
			   AND NOT EXISTS (
			       SELECT 1 FROM active_work_items awi
			       WHERE awi.session_id = sessions.session_id AND awi.agent_id = ?
			   )`,
			donor, now, sid, db.AgentRootSentinel,
		)
		routed++
	}
	return routed
}

func persistFamilyAttributionHTML(projectDir string, database *sql.DB, familyID string) {
	members, err := db.GetSessionsByFamily(database, familyID)
	if err != nil {
		return
	}
	for _, sid := range members {
		featureID := db.GetActiveWorkItemWithFallback(database, sid, db.AgentRootSentinel)
		if featureID == "" {
			continue
		}
		if err := SetSessionHTMLActiveFeature(projectDir, sid, featureID); err != nil {
			debugLog(projectDir, "[session-start] persist family attribution html %s: %v", sid, err)
		}
	}
}

// upsertSessionTx inserts the session row within a transaction,
// ignoring duplicate-key conflicts (session may already exist on resume).
func upsertSessionTx(tx *sql.Tx, s *models.Session) error {
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO sessions
			(session_id, agent_assigned, parent_session_id, parent_event_id,
			 created_at, status, start_commit, is_subagent, model, active_feature_id,
			 git_remote_url, project_dir, exec_worktree_path, branch, harness, continued_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
		nullableStr(s.ExecWorktreePath),
		nullableStr(s.Branch),
		nullableStr(s.Harness),
		nullableStr(s.ContinuedFrom),
	)
	return err
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

func normalizeHarness(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	if _, ok := allowedHarnesses[h]; ok {
		return h
	}
	return ""
}

// resolveHarness returns the effective harness for the current session.
// Priority: WIPNOTE_HARNESS (launcher-stamped) → CLAUDE_CODE_ENTRYPOINT
// (Claude Code sets this in every hook invocation; its presence means the
// Claude launcher was used without stamping WIPNOTE_HARNESS, so default to
// "claude").
func resolveHarness() string {
	if h := normalizeHarness(os.Getenv("WIPNOTE_HARNESS")); h != "" {
		return h
	}
	if os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" {
		return "claude"
	}
	return ""
}

func execDirOrDefault(cwd, projectDir string) string {
	if cwd != "" {
		return cwd
	}
	return projectDir
}

func gitBranch(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitTopLevel returns the git top-level directory for dir, or "" on error.
func gitTopLevel(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// execWorktreeRelPath returns the repo-relative path when cwd is a REAL linked
// git worktree (i.e. its git top-level differs from projectDir). Plain
// subdirectories of projectDir share the same git top-level and therefore get
// an empty return so they are not misrecorded as worktrees.
func execWorktreeRelPath(cwd, projectDir string) string {
	if cwd == "" || cwd == projectDir {
		return ""
	}
	if !filepath.IsAbs(projectDir) || !filepath.IsAbs(cwd) {
		return ""
	}
	// Only record a path when cwd is a real linked worktree: its git
	// top-level must differ from projectDir. A plain subdirectory of the
	// main repo shares the same top-level and is NOT a worktree.
	cwdTop := gitTopLevel(cwd)
	if cwdTop == "" || cwdTop == projectDir {
		return ""
	}
	rel, err := filepath.Rel(projectDir, cwdTop)
	if err != nil {
		return ""
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
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

// nullableStr maps an empty string to a typed SQL NULL (nil) and any non-empty
// string to itself, returning `any`.
//
// CRITICAL JSON-transport contract (bug-a782badf, roborev-476 finding 1): this
// helper feeds BOTH the in-process transactional path (upsertSessionTx) AND the
// daemon-routed path (routeSessionUpsert / routeLineageTrace / routeInsertEvent
// → RouteHookWrite → apply.RouteSQLAsync). The routed path JSON-encodes every
// bind arg (core/daemon/apply.DerivedOp.Args) and the daemon decodes it back. A
// sql.NullString JSON-marshals to the OBJECT {"String":...,"Valid":...}, which
// decodes to a map the SQLite driver CANNOT bind — so the routed Exec silently
// FAILS and the session/lineage/event row never applies via the daemon (only a
// reindex recovers it). A plain string / nil binds identically on the daemon's
// ExecContext AND on any direct-fallback Exec, so it is the only
// JSON-transport-safe choice. This mirrors core/db/otel_schema.go's nullableStr
// and core/hooks/dbgate.go's nullableArg (same NULL semantics across the process
// boundary). NEVER change this back to returning sql.NullString.
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetActiveFeatureID looks up the active_feature_id for a session.
func GetActiveFeatureID(database *sql.DB, sessionID string) string {
	// Nil-DB-safe (roborev-478 finding 1): the read-only hook dispatch may run
	// the handler with a nil DB so the DB-independent guards still execute.
	if database == nil {
		return ""
	}
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

// buildSessionStartAttribution returns concise session-start guidance. It keeps
// startup context quiet by directing agents to fetch work context on demand
// rather than eagerly listing open items and titles.
func buildSessionStartAttribution() string {
	lines := []string{
		"wipnote plugin is active in this project.",
		"",
		"Retrieve context on demand:",
		"- `wipnote wip` for open and in-progress items",
		"- `wipnote relevant <topic>` before creating new work so you search open and completed lineage first",
		"- `wipnote help --compact` for a command overview",
		"",
		"For orchestrated delegation, work tracking, and quality gates, use `/wipnote:orchestrator-directives-skill`.",
	}
	return joinLines(lines)
}

// emitRosettaEvent writes a session_start NDJSON line correlating the
// launcher-minted OTel session ID (WIPNOTE_OTEL_SESSION_ID) with Claude Code's
// own session_id. This is the "Rosetta stone" record that lets the dashboard
// map a `claude --resume <id>` back to the originating wipnote session.
//
// The event is written only when wipnoteSID is set (i.e. WIPNOTE_OTEL_SESSION_ID
// was populated by the launcher, meaning the session was started via `wipnote
// claude`). If it is unset, or if the session directory cannot be created, the
// function returns silently.
func emitRosettaEvent(projectDir, wipnoteSID, claudeSessionID string) {
	if wipnoteSID == "" {
		return // not a launcher-managed session; skip silently
	}

	// Emit through the core eventsink boundary rather than importing otel
	// directly (feat-f87e93a6). The telemetry implementation is registered out
	// of band by internal/otel/eventsink; with no sink registered this is a
	// no-op, matching the best-effort semantics of the rosetta record.
	snk, err := eventsink.New(projectDir, wipnoteSID)
	if err != nil {
		debugLog(projectDir, "[session-start] rosetta: create event sink: %v", err)
		return
	}
	// Close flushes the in-memory buffer + fsyncs before the hook process exits.
	// Without this a single-event write stays in memory and is lost — the 2s
	// periodic ticker never fires in a short-lived hook process.
	defer func() {
		if err := snk.Close(); err != nil {
			debugLog(projectDir, "[session-start] rosetta: close event sink: %v", err)
		}
	}()

	ev := eventsink.Event{
		Harness:       "wipnote",
		SignalID:      "session-start-" + wipnoteSID,
		Kind:          eventsink.KindLog,
		CanonicalName: eventsink.CanonicalSessionStart,
		NativeName:    "session_start",
		Timestamp:     time.Now().UTC(),
		SessionID:     wipnoteSID,
		Attrs: map[string]any{
			"wipnote_sid":       wipnoteSID,
			"claude_session_id": claudeSessionID,
		},
	}
	if err := snk.EmitEvent(context.Background(), ev); err != nil {
		debugLog(projectDir, "[session-start] rosetta: write event: %v", err)
	}
}

// ensure db package is referenced (used via db.nullStr in other files).
var _ = db.InsertSession
