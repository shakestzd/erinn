package apply

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/db/writequeue"
	"github.com/shakestzd/wipnote/core/models"
)

func seedSession(t *testing.T, database *sql.DB, sid string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.InsertSession(database, &models.Session{
		SessionID: sid, AgentAssigned: "agent-1", CreatedAt: now, Status: "active",
	}); err != nil {
		t.Fatalf("seed session %s: %v", sid, err)
	}
}

func sampleEvent(id string) *models.AgentEvent {
	now := time.Now().UTC().Truncate(time.Second)
	return &models.AgentEvent{
		EventID:   id,
		AgentID:   "agent-1",
		EventType: models.EventToolCall,
		Timestamp: now,
		ToolName:  "Bash",
		SessionID: "sess-abc",
		Status:    "completed",
		Source:    "claude",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

type eventRow struct {
	EventID, AgentID, EventType, ToolName, SessionID, Status, Source string
}

func getEvent(t *testing.T, database *sql.DB, id string) eventRow {
	t.Helper()
	var r eventRow
	err := database.QueryRow(
		`SELECT event_id, agent_id, event_type, COALESCE(tool_name,''),
		        session_id, status, source FROM agent_events WHERE event_id = ?`, id,
	).Scan(&r.EventID, &r.AgentID, &r.EventType, &r.ToolName, &r.SessionID, &r.Status, &r.Source)
	if err != nil {
		t.Fatalf("query event %s: %v", id, err)
	}
	return r
}

func waitForSocket(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", sock, 50*time.Millisecond); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", sock)
}

// TestApplierRoundTrip submits a hook-derived op over the socket and asserts
// it produces the IDENTICAL agent_events row as a direct db.UpsertEvent
// control on a separate DB.
func TestApplierRoundTrip(t *testing.T) {
	wdir := t.TempDir()
	wDB, err := db.Open(filepath.Join(wdir, "writer.db"))
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	defer wDB.Close()

	cdir := t.TempDir()
	cDB, err := db.Open(filepath.Join(cdir, "control.db"))
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	defer cDB.Close()

	q := writequeue.New(writequeue.Config{Capacity: 16})
	if err := q.Start(context.Background()); err != nil {
		t.Fatalf("queue start: %v", err)
	}
	defer q.Stop(time.Second)

	if err := os.MkdirAll(filepath.Join(wdir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	sock := daemon.SocketPath(wdir)
	ln, err := daemon.NewListener(daemon.ListenerConfig{
		SocketPath: sock, Queue: q, Applier: NewApplier(wDB),
	})
	if err != nil {
		t.Fatalf("new listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ln.Serve(ctx) }()
	defer ln.Close()
	waitForSocket(t, sock)

	seedSession(t, wDB, "sess-abc")
	seedSession(t, cDB, "sess-abc")

	ev := sampleEvent("evt-roundtrip")

	// Direct control write.
	if err := db.UpsertEvent(cDB, ev); err != nil {
		t.Fatalf("control upsert: %v", err)
	}

	// Daemon-routed write.
	payload, err := Encode(DerivedOp{Type: OpTypeAgentEventUpsert, Event: ev})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	client := daemon.NewWriterClientForSocket(sock)
	subCtx, subCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer subCancel()
	ack, err := client.Submit(subCtx, daemon.Envelope{
		OpID: OpID("sess-abc", 1), OpType: OpTypeAgentEventUpsert, Payload: payload,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ack.Status != daemon.AckApplied {
		t.Fatalf("ack = %q want applied (err=%q)", ack.Status, ack.Error)
	}

	got := getEvent(t, wDB, "evt-roundtrip")
	want := getEvent(t, cDB, "evt-roundtrip")
	if got != want {
		t.Fatalf("daemon row != direct row:\n daemon=%+v\n direct=%+v", got, want)
	}
}

// TestUnknownOpType asserts an unknown op_type is rejected (error, no write).
func TestUnknownOpType(t *testing.T) {
	a := NewApplier(nil)
	payload, _ := Encode(DerivedOp{Type: "nope"})
	if _, err := a(daemon.Envelope{OpType: "nope", Payload: payload}); err == nil {
		t.Fatal("unknown op_type must error")
	}
}

// TestOpIDDeterministic asserts op_id is stable for the same (session, seq)
// and differs across seq — the dedup key contract (plan slice-4).
func TestOpIDDeterministic(t *testing.T) {
	if OpID("s", 1) != OpID("s", 1) {
		t.Fatal("op_id not deterministic for same session+seq")
	}
	if OpID("s", 1) == OpID("s", 2) {
		t.Fatal("op_id must differ across seq")
	}
}
