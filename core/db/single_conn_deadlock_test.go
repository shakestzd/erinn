package db

import (
	"testing"
	"time"
)

// TestResumableReadersSurviveSingleConnection is the regression gate for
// bug-8e9ceb7b: `wipnote claude` aborted at launch with
// "all goroutines are asleep - deadlock!" because the resumable-session readers
// called SessionPromptLabel from INSIDE a `for rows.Next()` loop.
//
// SetMaxOpenConns(1) on the ephemeral projection is load-bearing, not
// arbitrary: each `:memory:` connection gets its OWN private database, so a
// second connection would see an empty schema (verified). While a cursor is
// open it holds that single connection, and a nested query waits forever for
// one that cannot free until the loop ends.
//
// The readers are exercised against MORE THAN ONE row, because a single row can
// mask the bug. The fixture is asserted to be non-trivial — an earlier version
// of this test silently skipped on a schema mismatch and proved nothing, which
// is the failure mode this assertion exists to prevent.
//
// Without the fix the failure mode is a hang, so every call is bounded and a
// timeout is reported as the deadlock it is.
func TestResumableReadersSurviveSingleConnection(t *testing.T) {
	database, err := OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("open ephemeral projection: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range []struct{ sid, item string }{
		{"deadlock-sess-1", "feat-dead0001"},
		{"deadlock-sess-2", "feat-dead0002"},
		{"deadlock-sess-3", "feat-dead0003"},
	} {
		if _, err := database.Exec(
			`INSERT INTO sessions (session_id, agent_assigned, created_at, status, harness, active_feature_id)
			 VALUES (?, 'claude', ?, 'active', 'claude', ?)`,
			s.sid, now, s.item,
		); err != nil {
			t.Fatalf("seed session %s: %v", s.sid, err)
		}
	}

	// The fixture must actually produce rows, or every assertion below is vacuous.
	seeded, err := ListResumableSessions(database, time.Hour)
	if err != nil {
		t.Fatalf("ListResumableSessions: %v", err)
	}
	if len(seeded) < 2 {
		t.Fatalf("fixture produced %d resumable rows, need >= 2 — a single row can mask "+
			"the nested-query deadlock, so this test would prove nothing", len(seeded))
	}

	readers := []struct {
		name string
		call func() error
	}{
		{"ListResumableSessions", func() error {
			_, err := ListResumableSessions(database, time.Hour)
			return err
		}},
		{"ListHarnessGroupedResumableSessions", func() error {
			_, err := ListHarnessGroupedResumableSessions(database, time.Hour, "claude")
			return err
		}},
		{"GetLatestHarnessSessionResumable", func() error {
			_, err := GetLatestHarnessSessionResumable(database, time.Hour, "claude")
			return err
		}},
	}

	for _, r := range readers {
		t.Run(r.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- r.call() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s returned an error: %v", r.name, err)
				}
			case <-time.After(20 * time.Second):
				t.Fatalf("%s did not return within 20s — it is almost certainly issuing a "+
					"nested query while its own cursor holds the single connection "+
					"(bug-8e9ceb7b). Buffer the rows and enrich after rows.Close().", r.name)
			}
		})
	}
}
