package db

import (
	"testing"
	"time"
)

// TestLatestPassingGateRecordForWorkItem covers the bug-35857288 cross-session
// fallback query: it must return the most recent PASSING record for a work item
// regardless of session, honour the recency window, and skip failing records.
func TestLatestPassingGateRecordForWorkItem(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	mustInsert := func(rec *GateRecord) {
		t.Helper()
		if err := InsertGateRecord(database, rec); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	now := time.Now().UTC()

	// Older passing record from a different session.
	mustInsert(&GateRecord{
		SessionID: "sess-old", WorkItemID: "feat-x", ProjectType: "go",
		GateCommand: "go test ./...", Status: "pass", Source: "check",
		CheckedAt: now.Add(-2 * time.Hour),
	})
	// Newer passing record from yet another session — this is the expected hit.
	mustInsert(&GateRecord{
		SessionID: "sess-new", WorkItemID: "feat-x", ProjectType: "go",
		GateCommand: "go test ./...", Status: "pass", Source: "check",
		CheckedAt: now.Add(-30 * time.Minute),
	})
	// A failing record for the same item must never be returned even if newest.
	mustInsert(&GateRecord{
		SessionID: "sess-fail", WorkItemID: "feat-x", ProjectType: "go",
		GateCommand: "go test ./...", Status: "fail", Source: "check",
		CheckedAt: now.Add(-1 * time.Minute),
	})
	// A passing record for a DIFFERENT item must not leak across work items.
	mustInsert(&GateRecord{
		SessionID: "sess-other", WorkItemID: "feat-y", ProjectType: "go",
		GateCommand: "go test ./...", Status: "pass", Source: "check",
		CheckedAt: now,
	})

	got, err := LatestPassingGateRecordForWorkItem(database, "feat-x", 6*time.Hour)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil {
		t.Fatal("expected a passing record")
	}
	if got.SessionID != "sess-new" {
		t.Errorf("session: got %q want sess-new", got.SessionID)
	}
	if got.Status != "pass" {
		t.Errorf("status: got %q want pass", got.Status)
	}

	// Recency window excludes everything older than 10 minutes -> the newest
	// passing record (30m old) falls outside the window -> nil.
	stale, err := LatestPassingGateRecordForWorkItem(database, "feat-x", 10*time.Minute)
	if err != nil {
		t.Fatalf("lookup (window): %v", err)
	}
	if stale != nil {
		t.Errorf("expected nil for narrow window, got record from %s", stale.SessionID)
	}

	// Unknown work item -> nil, no error.
	none, err := LatestPassingGateRecordForWorkItem(database, "feat-missing", 6*time.Hour)
	if err != nil {
		t.Fatalf("lookup (missing): %v", err)
	}
	if none != nil {
		t.Error("expected nil for unknown work item")
	}

	// Empty work item id and nil db are defensive no-ops.
	if rec, err := LatestPassingGateRecordForWorkItem(database, "", 6*time.Hour); err != nil || rec != nil {
		t.Errorf("empty work item: got rec=%v err=%v", rec, err)
	}
	if rec, err := LatestPassingGateRecordForWorkItem(nil, "feat-x", 6*time.Hour); err != nil || rec != nil {
		t.Errorf("nil db: got rec=%v err=%v", rec, err)
	}
}

// TestMostRecentInProgressWorkItem covers feat-cecb2f2b: the last-resort
// fallback that resolves work_item_id when session attribution is unavailable.
func TestMostRecentInProgressWorkItem(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// No in-progress items yet — must return "".
	if got := MostRecentInProgressWorkItem(database); got != "" {
		t.Errorf("empty db: got %q want empty", got)
	}

	// Insert two in-progress features; the one with the later updated_at wins.
	_, err = database.Exec(`INSERT INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES ('feat-older', 'feature', 'Older', 'in-progress', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:01:00Z'),
		       ('feat-newer', 'feature', 'Newer', 'in-progress', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:02:00Z')`)
	if err != nil {
		t.Fatalf("insert features: %v", err)
	}

	got := MostRecentInProgressWorkItem(database)
	if got != "feat-newer" {
		t.Errorf("got %q, want feat-newer", got)
	}

	// A done item must not be returned even if it's more recent.
	_, err = database.Exec(`INSERT INTO features (id, type, title, status, priority, created_at, updated_at)
		VALUES ('feat-done', 'feature', 'Done', 'done', 'medium', '2026-06-10T00:00:00Z', '2026-06-10T00:10:00Z')`)
	if err != nil {
		t.Fatalf("insert done feature: %v", err)
	}

	got = MostRecentInProgressWorkItem(database)
	if got != "feat-newer" {
		t.Errorf("after done insert: got %q, want feat-newer", got)
	}

	// Nil db defensive no-op.
	if got := MostRecentInProgressWorkItem(nil); got != "" {
		t.Errorf("nil db: got %q, want empty", got)
	}
}
