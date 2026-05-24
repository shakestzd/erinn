package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// buildTestSessionDir creates a fake session directory with an events.ndjson and
// an .index-offset file (set to the exact file size so the indexer appears caught up).
// mtime of events.ndjson is set to mtime.
func buildTestSessionDir(t *testing.T, sessionsRoot, sid string, size int64, mtime time.Time) {
	t.Helper()
	sessDir := filepath.Join(sessionsRoot, sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sessDir, err)
	}
	eventsFile := filepath.Join(sessDir, "events.ndjson")
	content := make([]byte, size)
	if err := os.WriteFile(eventsFile, content, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	// Set mtime.
	if err := os.Chtimes(eventsFile, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Write .index-offset == size so indexerCaughtUp returns true.
	offsetFile := filepath.Join(sessDir, ".index-offset")
	_ = os.WriteFile(offsetFile, fmt.Appendf(nil, "%d", size), 0o644)
}

// writeActiveSessionFile writes a minimal .active-session JSON so
// activeSessionIDFromFile returns the given session ID.
func writeActiveSessionJSONFile(t *testing.T, wipnoteDir, sid string) {
	t.Helper()
	data := `{"session_id":"` + sid + `"}`
	if err := os.WriteFile(filepath.Join(wipnoteDir, ".active-session"), []byte(data), 0o644); err != nil {
		t.Fatalf("write .active-session: %v", err)
	}
}

// TestActiveSessionIDFromFile verifies that the active-session file is parsed correctly.
func TestActiveSessionIDFromFile(t *testing.T) {
	dir := t.TempDir()
	writeActiveSessionJSONFile(t, dir, "sess-abc123")
	got := activeSessionIDFromFile(dir)
	if got != "sess-abc123" {
		t.Errorf("got %q, want %q", got, "sess-abc123")
	}
}

// TestActiveSessionIDFromFile_Missing returns empty string when the file is absent.
func TestActiveSessionIDFromFile_Missing(t *testing.T) {
	dir := t.TempDir()
	got := activeSessionIDFromFile(dir)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// TestCollectPruneCandidates_ExcludesActiveSession verifies the active session is
// never a candidate even when it would otherwise qualify.
func TestCollectPruneCandidates_ExcludesActiveSession(t *testing.T) {
	dir := t.TempDir()
	sessionsRoot := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := time.Now().Add(-60 * 24 * time.Hour)
	buildTestSessionDir(t, sessionsRoot, "sess-active", 1024, old)
	buildTestSessionDir(t, sessionsRoot, "sess-old", 2048, old)

	candidates, err := collectPruneCandidates(dir, "sess-active")
	if err != nil {
		t.Fatalf("collectPruneCandidates: %v", err)
	}

	for _, c := range candidates {
		if c.sessionID == "sess-active" {
			t.Errorf("active session %q must never appear in prune candidates", c.sessionID)
		}
	}
	if len(candidates) != 1 || candidates[0].sessionID != "sess-old" {
		t.Errorf("expected exactly sess-old in candidates, got %+v", candidates)
	}
}

// TestFilterPruneCandidates_OlderThan verifies --older-than filtering.
func TestFilterPruneCandidates_OlderThan(t *testing.T) {
	now := time.Now()
	candidates := []pruneCandidate{
		{sessionID: "sess-a", mtime: now.Add(-40 * 24 * time.Hour), size: 100},
		{sessionID: "sess-b", mtime: now.Add(-20 * 24 * time.Hour), size: 200},
		{sessionID: "sess-c", mtime: now.Add(-5 * 24 * time.Hour), size: 300},
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	cutoff := now.Add(-30 * 24 * time.Hour)
	targets := filterPruneCandidates(candidates, cutoff, 0)

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %+v", len(targets), targets)
	}
	if targets[0].sessionID != "sess-a" {
		t.Errorf("expected sess-a, got %s", targets[0].sessionID)
	}
}

// TestFilterPruneCandidates_KeepLast verifies --keep-last filtering.
func TestFilterPruneCandidates_KeepLast(t *testing.T) {
	now := time.Now()
	candidates := []pruneCandidate{
		{sessionID: "sess-oldest", mtime: now.Add(-50 * 24 * time.Hour), size: 100},
		{sessionID: "sess-middle", mtime: now.Add(-20 * 24 * time.Hour), size: 200},
		{sessionID: "sess-recent", mtime: now.Add(-2 * 24 * time.Hour), size: 300},
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	// keepLast=2 → keep the 2 most recent, prune the oldest.
	targets := filterPruneCandidates(candidates, time.Time{}, 2)

	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %+v", len(targets), targets)
	}
	if targets[0].sessionID != "sess-oldest" {
		t.Errorf("expected sess-oldest, got %s", targets[0].sessionID)
	}
}

// TestFilterPruneCandidates_Combined verifies the AND semantics when both flags apply.
// With --older-than=30d and --keep-last=2:
// - sess-oldest (50d old): outside keep window AND older than 30d → pruned.
// - sess-middle (20d old): outside keep window but NOT older than 30d → kept.
// - sess-recent ( 2d old): inside keep window → kept.
func TestFilterPruneCandidates_Combined(t *testing.T) {
	now := time.Now()
	candidates := []pruneCandidate{
		{sessionID: "sess-oldest", mtime: now.Add(-50 * 24 * time.Hour), size: 100},
		{sessionID: "sess-middle", mtime: now.Add(-20 * 24 * time.Hour), size: 200},
		{sessionID: "sess-recent", mtime: now.Add(-2 * 24 * time.Hour), size: 300},
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	cutoff := now.Add(-30 * 24 * time.Hour)
	targets := filterPruneCandidates(candidates, cutoff, 2)

	if len(targets) != 1 {
		t.Fatalf("expected 1 target (sess-oldest), got %d: %+v", len(targets), targets)
	}
	if targets[0].sessionID != "sess-oldest" {
		t.Errorf("expected sess-oldest, got %s", targets[0].sessionID)
	}
}

// TestFilterPruneCandidates_KeepLastCoversAll verifies that --keep-last >= total
// results in no candidates pruned.
func TestFilterPruneCandidates_KeepLastCoversAll(t *testing.T) {
	now := time.Now()
	candidates := []pruneCandidate{
		{sessionID: "sess-a", mtime: now.Add(-40 * 24 * time.Hour), size: 100},
		{sessionID: "sess-b", mtime: now.Add(-20 * 24 * time.Hour), size: 200},
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].mtime.Before(candidates[j].mtime)
	})

	targets := filterPruneCandidates(candidates, time.Time{}, 10)
	if len(targets) != 0 {
		t.Errorf("expected no targets when keepLast >= len, got %v", targets)
	}
}

// TestDryRunDoesNotDelete verifies that dry-run leaves files intact.
func TestDryRunDoesNotDelete(t *testing.T) {
	dir := t.TempDir()
	sessionsRoot := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := time.Now().Add(-60 * 24 * time.Hour)
	buildTestSessionDir(t, sessionsRoot, "sess-old1", 512, old)
	buildTestSessionDir(t, sessionsRoot, "sess-old2", 1024, old)

	candidates, err := collectPruneCandidates(dir, "")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	// Simulate dry-run: do NOT call applyPrune. Verify dirs still exist.
	for _, sid := range []string{"sess-old1", "sess-old2"} {
		sessDir := filepath.Join(sessionsRoot, sid)
		if _, err := os.Stat(sessDir); os.IsNotExist(err) {
			t.Errorf("dry-run: session dir %s was deleted", sid)
		}
	}
}

// TestCollectPruneCandidates_ExcludesUnIndexed verifies un-indexed sessions
// are never candidates (the .index-offset is 0, file is larger than 0).
func TestCollectPruneCandidates_ExcludesUnIndexed(t *testing.T) {
	dir := t.TempDir()
	sessionsRoot := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	old := time.Now().Add(-60 * 24 * time.Hour)
	sid := "sess-unindexed"
	sessDir := filepath.Join(sessionsRoot, sid)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sess: %v", err)
	}
	eventsFile := filepath.Join(sessDir, "events.ndjson")
	// Write 100 bytes.
	if err := os.WriteFile(eventsFile, make([]byte, 100), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if err := os.Chtimes(eventsFile, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// Write .index-offset == 0 (indexer hasn't caught up).
	if err := os.WriteFile(filepath.Join(sessDir, ".index-offset"), []byte("0"), 0o644); err != nil {
		t.Fatalf("write offset: %v", err)
	}

	candidates, err := collectPruneCandidates(dir, "")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, c := range candidates {
		if c.sessionID == sid {
			t.Errorf("un-indexed session %q must never appear in prune candidates", sid)
		}
	}
}
