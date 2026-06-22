package apply

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
)

func seedFeature(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.UpsertFeature(database, &db.Feature{
		ID: id, Type: "feature", Title: "t", Status: "todo", Priority: "medium",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed feature %s: %v", id, err)
	}
}

func featureStatus(t *testing.T, database *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := database.QueryRow(`SELECT status FROM features WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("query feature %s: %v", id, err)
	}
	return s
}

func sessionStatus(t *testing.T, database *sql.DB, sid string) string {
	t.Helper()
	var s string
	if err := database.QueryRow(`SELECT status FROM sessions WHERE session_id = ?`, sid).Scan(&s); err != nil {
		t.Fatalf("query session %s: %v", sid, err)
	}
	return s
}

// startListener brings up a writer daemon over a socket bound to a temp
// project root, returning the writer DB, the project root, and the socket.
func startListener(t *testing.T) (wDB *sql.DB, projectRoot, sock string) {
	t.Helper()
	projectRoot = t.TempDir()
	var err error
	wDB, err = db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	t.Cleanup(func() { q.Stop(time.Second) })

	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock = daemon.SocketPath(projectRoot)
	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: sock, Queue: q, Applier: NewApplier(wDB),
	})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ln.Serve(ctx) }()
	t.Cleanup(func() { ln.Close() })
	waitForSocket(t, sock)
	return wDB, projectRoot, sock
}

// TestApplierRoundTrip_FeatureStatus submits the MVP-4 feature.status op over
// the socket and asserts it produces the IDENTICAL features.status as a direct
// db.UpdateFeatureStatus control on a separate DB.
func TestApplierRoundTrip_FeatureStatus(t *testing.T) {
	wDB, _, sock := startListener(t)

	cdir := t.TempDir()
	cDB, err := db.Open(filepath.Join(cdir, "control.db"))
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	defer cDB.Close()

	seedFeature(t, wDB, "feat-1")
	seedFeature(t, cDB, "feat-1")

	// Direct control write.
	if err := db.UpdateFeatureStatus(cDB, "feat-1", "done"); err != nil {
		t.Fatalf("control update: %v", err)
	}

	// Daemon-routed write.
	payload, err := Encode(DerivedOp{Type: OpTypeFeatureStatus, FeatureID: "feat-1", Status: "done"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	client := daemon.NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ack, err := client.Submit(ctx, daemon.Envelope{
		OpID: cliOpID(OpTypeFeatureStatus, "feat-1", "done"), OpType: OpTypeFeatureStatus, Payload: payload,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != daemon.AckApplied {
		t.Fatalf("ack = %q want applied (err=%q)", ack.Status, ack.Error)
	}

	if got, want := featureStatus(t, wDB, "feat-1"), featureStatus(t, cDB, "feat-1"); got != want {
		t.Fatalf("daemon status %q != direct status %q", got, want)
	}
}

// TestApplierRoundTrip_SessionInsertAndStatus submits the MVP-4 session.insert
// then session.status ops over the socket and asserts the row matches a direct
// InsertSession + UpdateSessionStatus control on a separate DB.
func TestApplierRoundTrip_SessionInsertAndStatus(t *testing.T) {
	wDB, _, sock := startListener(t)

	cdir := t.TempDir()
	cDB, err := db.Open(filepath.Join(cdir, "control.db"))
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	defer cDB.Close()

	now := time.Now().UTC().Truncate(time.Second)
	s := &models.Session{SessionID: "sess-mvp4", AgentAssigned: "agent-1", CreatedAt: now, Status: "active"}

	// Direct control writes.
	if err := db.InsertSession(cDB, s); err != nil {
		t.Fatalf("control insert: %v", err)
	}
	if err := db.UpdateSessionStatus(cDB, "sess-mvp4", "completed"); err != nil {
		t.Fatalf("control status: %v", err)
	}

	client := daemon.NewWriterClientForSocket(sock)

	// Daemon-routed insert.
	insPayload, _ := Encode(DerivedOp{Type: OpTypeSessionInsert, Session: s})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ack, err := client.Submit(ctx, daemon.Envelope{
		OpID: cliOpID(OpTypeSessionInsert, s.SessionID, s.Status), OpType: OpTypeSessionInsert, Payload: insPayload,
	})
	if err != nil || ack.Status != daemon.AckApplied {
		t.Fatalf("insert submit: ack=%q err=%v", ack.Status, err)
	}

	// Daemon-routed status.
	stPayload, _ := Encode(DerivedOp{Type: OpTypeSessionStatus, SessionID: "sess-mvp4", Status: "completed"})
	ack, err = client.Submit(ctx, daemon.Envelope{
		OpID: cliOpID(OpTypeSessionStatus, "sess-mvp4", "completed"), OpType: OpTypeSessionStatus, Payload: stPayload,
	})
	if err != nil || ack.Status != daemon.AckApplied {
		t.Fatalf("status submit: ack=%q err=%v", ack.Status, err)
	}

	if got, want := sessionStatus(t, wDB, "sess-mvp4"), sessionStatus(t, cDB, "sess-mvp4"); got != want {
		t.Fatalf("daemon session status %q != direct %q", got, want)
	}
}

// TestRouteFeatureStatus_LiveDaemon asserts the high-level CLI route helper
// applies through a live daemon (auto-spawn disabled — we already have a
// listener bound to the socket, so SubmitOrSpawn takes the fast path).
func TestRouteFeatureStatus_LiveDaemon(t *testing.T) {
	wDB, projectRoot, _ := startListener(t)
	seedFeature(t, wDB, "feat-live")

	if !RouteFeatureStatus(projectRoot, "feat-live", "in-progress") {
		t.Fatal("RouteFeatureStatus returned false with a live daemon")
	}
	if got := featureStatus(t, wDB, "feat-live"); got != "in-progress" {
		t.Fatalf("status = %q want in-progress", got)
	}
}

// TestRouteFeatureStatus_FallbackBounded asserts that with NO daemon and
// auto-spawn forbidden, the route helper returns false promptly (within the
// CLI budget + slack) so the caller can fall back to the direct write. This is
// the live-session-safety contract: never hang.
func TestRouteFeatureStatus_FallbackBounded(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1") // deterministic: no spawn → straight to miss
	projectRoot := t.TempDir()              // no writer.sock here

	start := time.Now()
	applied := RouteFeatureStatus(projectRoot, "feat-x", "done")
	elapsed := time.Since(start)

	if applied {
		t.Fatal("RouteFeatureStatus returned true with no daemon")
	}
	if elapsed > CLISubmitBudget+2*time.Second {
		t.Fatalf("RouteFeatureStatus took %v, exceeds bounded budget (must not hang)", elapsed)
	}
}

// TestSessionInsert_UpgradesEnsureSessionPlaceholder applies ops OUT OF ORDER:
// an agent_event.upsert for session "S1" arrives first, which causes
// EnsureSession to create a placeholder row with agent_assigned="__hook__".
// Then session.insert for "S1" arrives with real metadata (agent_assigned
// "real-agent"). After both ops the sessions row must carry the real metadata,
// proving UpsertSession upgraded the placeholder rather than failing with a PK
// conflict.
func TestSessionInsert_UpgradesEnsureSessionPlaceholder(t *testing.T) {
	// Use a short path to stay under the Unix socket path limit (~104 chars).
	projectRoot, err := os.MkdirTemp("", "wn-upsert")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(projectRoot) })

	wDB, err := db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	t.Cleanup(func() { q.Stop(time.Second) })

	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := daemon.SocketPath(projectRoot)
	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: sock, Queue: q, Applier: NewApplier(wDB),
	})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	lnCtx, lnCancel := context.WithCancel(context.Background())
	t.Cleanup(lnCancel)
	go func() { _ = ln.Serve(lnCtx) }()
	t.Cleanup(func() { ln.Close() })
	waitForSocket(t, sock)

	const sid = "S1-ooo"

	// --- Op 1: agent_event.upsert arrives BEFORE the session row exists.
	// The applier calls EnsureSession, which inserts agent_assigned="__hook__".
	now := time.Now().UTC().Truncate(time.Second)
	ev := &models.AgentEvent{
		EventID:   "evt-ooo-1",
		AgentID:   "agent-ooo",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: sid,
		Status:    "completed",
		Source:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}
	evPayload, encErr := Encode(DerivedOp{Type: OpTypeAgentEventUpsert, Event: ev})
	if encErr != nil {
		t.Fatalf("encode event op: %v", encErr)
	}
	client := daemon.NewWriterClientForSocket(sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ack, submitErr := client.Submit(ctx, daemon.Envelope{
		OpID: OpID(sid, 1), OpType: OpTypeAgentEventUpsert, Payload: evPayload,
	})
	if submitErr != nil {
		t.Fatalf("submit event op: %v", submitErr)
	}
	if ack.Status != daemon.AckApplied {
		t.Fatalf("event op ack = %q (err=%q), want applied", ack.Status, ack.Error)
	}

	// Confirm the placeholder exists with agent_assigned="__hook__".
	var placeholderAgent string
	if err := wDB.QueryRow(`SELECT agent_assigned FROM sessions WHERE session_id = ?`, sid).Scan(&placeholderAgent); err != nil {
		t.Fatalf("placeholder row missing after agent_event.upsert: %v", err)
	}
	if placeholderAgent != "__hook__" {
		t.Fatalf("expected placeholder agent_assigned=__hook__, got %q", placeholderAgent)
	}

	// --- Op 2: session.insert for the same session with real metadata.
	s := &models.Session{
		SessionID:     sid,
		AgentAssigned: "real-agent",
		CreatedAt:     now,
		Status:        "active",
	}
	sessPayload, encErr2 := Encode(DerivedOp{Type: OpTypeSessionInsert, Session: s})
	if encErr2 != nil {
		t.Fatalf("encode session op: %v", encErr2)
	}
	ack, submitErr2 := client.Submit(ctx, daemon.Envelope{
		OpID: cliOpID(OpTypeSessionInsert, sid, s.Status), OpType: OpTypeSessionInsert, Payload: sessPayload,
	})
	if submitErr2 != nil {
		t.Fatalf("submit session op: %v", submitErr2)
	}
	if ack.Status != daemon.AckApplied {
		t.Fatalf("session op ack = %q (err=%q), want applied (placeholder upgrade failed)", ack.Status, ack.Error)
	}

	// Assert: the sessions row now has agent_assigned="real-agent", not "__hook__".
	var finalAgent string
	if err := wDB.QueryRow(`SELECT agent_assigned FROM sessions WHERE session_id = ?`, sid).Scan(&finalAgent); err != nil {
		t.Fatalf("final session row missing: %v", err)
	}
	if finalAgent != "real-agent" {
		t.Fatalf("session placeholder was not upgraded: agent_assigned=%q, want real-agent", finalAgent)
	}
}

// TestSessionInsert_DoesNotClobberRealSession verifies that UpsertSession does NOT
// overwrite an existing real session row when a replay/late session.insert arrives.
// This is the regression test for the MEDIUM bug where a guarded upsert was added
// to prevent completed sessions from being reverted to active.
func TestSessionInsert_DoesNotClobberRealSession(t *testing.T) {
	// Use a short path to stay under the Unix socket path limit (~104 chars).
	projectRoot, err := os.MkdirTemp("", "wn-noclobber")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(projectRoot) })

	wDB, err := db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })

	const sid = "S2-noclobber"
	now := time.Now().UTC().Truncate(time.Second)

	// --- Directly insert a REAL session (not a placeholder) in "completed" state.
	realSession := &models.Session{
		SessionID:     sid,
		AgentAssigned: "real-agent",
		CreatedAt:     now,
		Status:        "completed",
	}
	if err := db.InsertSession(wDB, realSession); err != nil {
		t.Fatalf("insert real session: %v", err)
	}

	// Set completed_at on the session to simulate a truly completed session.
	completedTime := now.Format(time.RFC3339)
	if _, err := wDB.Exec(`UPDATE sessions SET completed_at = ? WHERE session_id = ?`, completedTime, sid); err != nil {
		t.Fatalf("set completed_at: %v", err)
	}

	// Verify the real session state before the upsert attempt.
	var beforeStatus, beforeAgent, beforeCompletedAt string
	row := wDB.QueryRow(`SELECT status, agent_assigned, completed_at FROM sessions WHERE session_id = ?`, sid)
	if err := row.Scan(&beforeStatus, &beforeAgent, &beforeCompletedAt); err != nil {
		t.Fatalf("query initial state: %v", err)
	}
	if beforeStatus != "completed" || beforeAgent != "real-agent" || beforeCompletedAt != completedTime {
		t.Fatalf("initial state invalid: status=%q (want completed), agent=%q (want real-agent), completed_at=%q (want %q)",
			beforeStatus, beforeAgent, beforeCompletedAt, completedTime)
	}

	// --- Now attempt to upsert with "active" status (the replay/late case).
	replayedSession := &models.Session{
		SessionID:     sid,
		AgentAssigned: "some-other-agent",
		CreatedAt:     now,
		Status:        "active", // This should NOT overwrite completed status.
	}
	if err := db.UpsertSession(wDB, replayedSession); err != nil {
		t.Fatalf("upsert replayed session: %v", err)
	}

	// --- Assert: the row status is STILL "completed" and agent_assigned is STILL "real-agent".
	var afterStatus, afterAgent, afterCompletedAt string
	row = wDB.QueryRow(`SELECT status, agent_assigned, completed_at FROM sessions WHERE session_id = ?`, sid)
	if err := row.Scan(&afterStatus, &afterAgent, &afterCompletedAt); err != nil {
		t.Fatalf("query final state: %v", err)
	}

	if afterStatus != "completed" {
		t.Fatalf("session status was clobbered: got %q, want completed", afterStatus)
	}
	if afterAgent != "real-agent" {
		t.Fatalf("session agent_assigned was clobbered: got %q, want real-agent", afterAgent)
	}
	if afterCompletedAt != completedTime {
		t.Fatalf("session completed_at was clobbered: got %q, want %q", afterCompletedAt, completedTime)
	}

	// Also verify that placeholder upgrade still works by running the existing test
	// inline to confirm it still passes.
	t.Run("PlaceholderUpgradeStillWorks", func(t *testing.T) {
		const placeholderSid = "S3-placeholder"

		// Create a placeholder with agent_assigned="__hook__".
		ph := &models.Session{
			SessionID:     placeholderSid,
			AgentAssigned: "__hook__",
			CreatedAt:     now,
			Status:        "active",
		}
		if err := db.InsertSession(wDB, ph); err != nil {
			t.Fatalf("insert placeholder: %v", err)
		}

		// Now upsert with real metadata.
		real := &models.Session{
			SessionID:     placeholderSid,
			AgentAssigned: "real-agent-2",
			CreatedAt:     now,
			Status:        "active",
		}
		if err := db.UpsertSession(wDB, real); err != nil {
			t.Fatalf("upsert real over placeholder: %v", err)
		}

		// Verify upgrade happened.
		var agent string
		row := wDB.QueryRow(`SELECT agent_assigned FROM sessions WHERE session_id = ?`, placeholderSid)
		if err := row.Scan(&agent); err != nil {
			t.Fatalf("query placeholder: %v", err)
		}
		if agent != "real-agent-2" {
			t.Fatalf("placeholder was not upgraded: got %q, want real-agent-2", agent)
		}
	})
}

// TestSessionInsert_LateInsertDoesNotRevertProgressedPlaceholder is the regression
// test for the MEDIUM bug: A placeholder (agent_assigned='__hook__') created by
// EnsureSession can progress to 'completed' status before the real session.insert
// arrives. When the late/replayed session.insert arrives carrying status='active',
// the ON CONFLICT DO UPDATE must NOT revert the status back to 'active'. The
// lifecycle status is owned by session.status ops; the upgrade-only branch must
// preserve existing status and completed_at while attaching real identity metadata.
func TestSessionInsert_LateInsertDoesNotRevertProgressedPlaceholder(t *testing.T) {
	// Use a short path to stay under the Unix socket path limit (~104 chars).
	projectRoot, err := os.MkdirTemp("", "wn-prog")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(projectRoot) })

	wDB, err := db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })

	const sid = "S3-prog"
	now := time.Now().UTC().Truncate(time.Second)

	// --- Step 1: Create placeholder via EnsureSession (simulating agent_event.upsert arriving first).
	if err := db.EnsureSession(wDB, sid); err != nil {
		t.Fatalf("ensure placeholder: %v", err)
	}

	// Verify placeholder exists with status='active' and agent_assigned='__hook__'.
	var placeholderStatus, placeholderAgent string
	row := wDB.QueryRow(`SELECT status, agent_assigned FROM sessions WHERE session_id = ?`, sid)
	if err := row.Scan(&placeholderStatus, &placeholderAgent); err != nil {
		t.Fatalf("query placeholder: %v", err)
	}
	if placeholderStatus != "active" || placeholderAgent != "__hook__" {
		t.Fatalf("placeholder state invalid: status=%q (want active), agent=%q (want __hook__)",
			placeholderStatus, placeholderAgent)
	}

	// --- Step 2: Mark placeholder as progressed to 'completed' state.
	// This simulates a session.status op arriving after the placeholder but before the real session.insert.
	if err := db.UpdateSessionStatus(wDB, sid, "completed"); err != nil {
		t.Fatalf("mark placeholder completed: %v", err)
	}

	// Verify progression succeeded.
	var progressedStatus, progressedCompletedAt string
	row = wDB.QueryRow(`SELECT status, completed_at FROM sessions WHERE session_id = ?`, sid)
	if err := row.Scan(&progressedStatus, &progressedCompletedAt); err != nil {
		t.Fatalf("query progressed state: %v", err)
	}
	if progressedStatus != "completed" || progressedCompletedAt == "" {
		t.Fatalf("progression failed: status=%q (want completed), completed_at=%q (want non-empty)",
			progressedStatus, progressedCompletedAt)
	}

	// --- Step 3: Late/replayed session.insert arrives with status='active' and real agent.
	// This is the bug scenario: the UpsertSession must NOT revert status back to 'active'.
	replayedSession := &models.Session{
		SessionID:     sid,
		AgentAssigned: "real-agent",
		CreatedAt:     now,
		Status:        "active", // MUST NOT overwrite progressed 'completed' status.
	}
	if err := db.UpsertSession(wDB, replayedSession); err != nil {
		t.Fatalf("upsert late session.insert: %v", err)
	}

	// --- Assert: status is STILL 'completed', completed_at is STILL set, AND agent_assigned
	// was upgraded to 'real-agent' (identity attachment succeeded).
	var finalStatus, finalAgent, finalCompletedAt string
	row = wDB.QueryRow(`SELECT status, agent_assigned, completed_at FROM sessions WHERE session_id = ?`, sid)
	if err := row.Scan(&finalStatus, &finalAgent, &finalCompletedAt); err != nil {
		t.Fatalf("query final state: %v", err)
	}

	if finalStatus != "completed" {
		t.Fatalf("bug: session status was reverted by late session.insert: got %q, want completed", finalStatus)
	}
	if finalCompletedAt != progressedCompletedAt {
		t.Fatalf("bug: completed_at was lost: got %q, want %q", finalCompletedAt, progressedCompletedAt)
	}
	if finalAgent != "real-agent" {
		t.Fatalf("identity attachment failed: got %q, want real-agent", finalAgent)
	}
}

// TestRouteSession_FallbackBounded mirrors the bounded-miss contract for the
// session route helpers.
func TestRouteSession_FallbackBounded(t *testing.T) {
	t.Setenv("WIPNOTE_NO_AUTO_WRITER", "1")
	projectRoot := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	s := &models.Session{SessionID: "sess-x", AgentAssigned: "a", CreatedAt: now, Status: "active"}

	if RouteSessionInsert(projectRoot, s) {
		t.Fatal("RouteSessionInsert returned true with no daemon")
	}
	if RouteSessionStatus(projectRoot, "sess-x", "completed") {
		t.Fatal("RouteSessionStatus returned true with no daemon")
	}
	// Nil session must not panic and must return false (caller falls back).
	if RouteSessionInsert(projectRoot, nil) {
		t.Fatal("RouteSessionInsert(nil) returned true")
	}
}

// startGatedSessionListener brings up a writer daemon over a FULLY-MIGRATED DB
// (sessions schema present) whose single-writer worker blocks on `gate` before
// applying each op. This wedges the writer so the SECOND op can only be acked
// enqueue-only — the fixture for RouteSessionInsertAsync's timing contract.
func startGatedSessionListener(t *testing.T, gate <-chan struct{}) (wDB *sql.DB, projectRoot string) {
	t.Helper()
	projectRoot = t.TempDir()
	var err error
	wDB, err = db.Open(filepath.Join(projectRoot, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	t.Cleanup(func() { wDB.Close() })

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	t.Cleanup(func() { q.Stop(time.Second) })

	if err := os.MkdirAll(filepath.Join(projectRoot, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	base := NewApplier(wDB)
	wrapped := func(env daemon.Envelope) (writequeue.WriteOp, error) {
		op, err := base(env)
		if err != nil {
			return nil, err
		}
		if gate == nil {
			return op, nil
		}
		return func(ctx context.Context) error {
			<-gate
			return op(ctx)
		}, nil
	}

	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: daemon.SocketPath(projectRoot), Queue: q, Applier: wrapped,
	})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ln.Serve(ctx) }()
	t.Cleanup(func() { ln.Close() })
	waitForSocket(t, daemon.SocketPath(projectRoot))
	return wDB, projectRoot
}

// TestRouteSessionInsertAsync_AcksOnEnqueue_NotApply wedges the daemon's single
// writer with a blocked first op, then routes a SECOND session insert via
// RouteSessionInsertAsync. The helper must return true in WELL under the
// applied-ack (~2s CLISubmitBudget) budget — proving it acks on enqueue, not on
// apply. This is bug-d792aee6 finding 1: the launcher cold insert must not wait
// the full applied-ack budget under a held lock. Mirrors
// TestRouteSQLAsync_AcksOnEnqueue_NotApply.
func TestRouteSessionInsertAsync_AcksOnEnqueue_NotApply(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	gate := make(chan struct{})
	_, projectRoot := startGatedSessionListener(t, gate)

	// Op 1: enters the worker and blocks on the gate, occupying the writer.
	occupy := &models.Session{SessionID: "async-occupy", AgentAssigned: "a", CreatedAt: now, Status: "active"}
	if !RouteSessionInsertAsync(projectRoot, occupy) {
		close(gate)
		t.Fatal("RouteSessionInsertAsync(occupy) returned false with a live daemon")
	}

	// Op 2: the writer is now wedged applying op 1, so op 2 is enqueued-only.
	// Must return true promptly (enqueue-only), nowhere near the applied budget.
	busy := &models.Session{SessionID: "async-while-busy", AgentAssigned: "a", CreatedAt: now, Status: "active"}
	start := time.Now()
	ok := RouteSessionInsertAsync(projectRoot, busy)
	elapsed := time.Since(start)
	if !ok {
		close(gate)
		t.Fatal("RouteSessionInsertAsync(while-busy) returned false; enqueue should still succeed")
	}
	if elapsed >= CLISubmitBudget {
		close(gate)
		t.Fatalf("RouteSessionInsertAsync took %v (>= applied-ack budget %v) — it waited for apply, not enqueue", elapsed, CLISubmitBudget)
	}

	// Release the writer; both ops should eventually apply (FIFO).
	close(gate)

	// Nil session must be a safe false (no panic), matching RouteSessionInsert.
	if RouteSessionInsertAsync(projectRoot, nil) {
		t.Fatal("RouteSessionInsertAsync(nil) returned true")
	}
}

// TestRouteSessionInsertAsync_LiveDaemon_Applies confirms the enqueue-only path
// still ultimately APPLIES the session row through the single writer when it is
// NOT wedged: the row lands in the DB shortly after the (immediate) enqueue ack.
func TestRouteSessionInsertAsync_LiveDaemon_Applies(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	wDB, projectRoot := startGatedSessionListener(t, nil)

	s := &models.Session{SessionID: "async-live", AgentAssigned: "a", CreatedAt: now, Status: "active"}
	if !RouteSessionInsertAsync(projectRoot, s) {
		t.Fatal("RouteSessionInsertAsync returned false with a live unblocked daemon")
	}
	// The ack is enqueue-only, so poll briefly for the async apply to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := wDB.QueryRow(`SELECT status FROM sessions WHERE session_id = ?`, "async-live").Scan(&status); err == nil {
			if status != "active" {
				t.Fatalf("session status = %q, want active", status)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("RouteSessionInsertAsync session row never applied within deadline")
}
