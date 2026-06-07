package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestActiveSessionIndex_ParallelFamilies verifies that two root sessions
// coexist in the family index without clobbering each other.
func TestActiveSessionIndex_ParallelFamilies(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := RegisterSessionFamily(dir, "sess-a", "family-a"); err != nil {
		t.Fatalf("register family-a: %v", err)
	}
	if err := RegisterSessionFamily(dir, "sess-b", "family-b"); err != nil {
		t.Fatalf("register family-b: %v", err)
	}

	index, err := ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}

	if index["sess-a"] != "family-a" {
		t.Errorf("sess-a family = %q, want %q", index["sess-a"], "family-a")
	}
	if index["sess-b"] != "family-b" {
		t.Errorf("sess-b family = %q, want %q", index["sess-b"], "family-b")
	}
}

// TestSessionFamily_ContinuedFrom verifies that resumed sessions link to the
// same family without harness-specific IDs.
func TestSessionFamily_ContinuedFrom(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := RegisterSessionFamily(dir, "sess-orig", "family-orig"); err != nil {
		t.Fatalf("register orig: %v", err)
	}
	if err := RegisterSessionFamily(dir, "sess-resumed", "family-orig"); err != nil {
		t.Fatalf("register resumed: %v", err)
	}

	index, err := ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}

	if index["sess-orig"] != "family-orig" {
		t.Errorf("sess-orig family = %q, want %q", index["sess-orig"], "family-orig")
	}
	if index["sess-resumed"] != "family-orig" {
		t.Errorf("sess-resumed family = %q, want %q", index["sess-resumed"], "family-orig")
	}
}

// TestPerSessionFallback verifies per-session state isolation.
func TestPerSessionFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := WriteSessionState(dir, "sess-1", "claude", "family-f"); err != nil {
		t.Fatalf("WriteSessionState sess-1: %v", err)
	}
	if err := WriteSessionState(dir, "sess-2", "codex", "family-f"); err != nil {
		t.Fatalf("WriteSessionState sess-2: %v", err)
	}

	st1, err := ReadSessionState(dir, "sess-1")
	if err != nil {
		t.Fatalf("ReadSessionState sess-1: %v", err)
	}
	st2, err := ReadSessionState(dir, "sess-2")
	if err != nil {
		t.Fatalf("ReadSessionState sess-2: %v", err)
	}

	if st1.AgentID != "claude" {
		t.Errorf("sess-1 agent = %q, want %q", st1.AgentID, "claude")
	}
	if st2.AgentID != "codex" {
		t.Errorf("sess-2 agent = %q, want %q", st2.AgentID, "codex")
	}
	if st1.SessionFamilyID != "family-f" || st2.SessionFamilyID != "family-f" {
		t.Errorf("family mismatch: sess-1=%q sess-2=%q", st1.SessionFamilyID, st2.SessionFamilyID)
	}
}

// TestOldActiveSessionStillReadable verifies backward compat with legacy file.
func TestOldActiveSessionStillReadable(t *testing.T) {
	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	legacy := map[string]any{
		"session_id":     "old-sess-abc",
		"parent_session": "old-sess-abc",
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(wipnoteDir, ".active-session"), b, 0o644); err != nil {
		t.Fatalf("write .active-session: %v", err)
	}

	sid := readActiveSessionID(dir)
	if sid != "old-sess-abc" {
		t.Errorf("legacy .active-session read = %q, want %q", sid, "old-sess-abc")
	}
}
