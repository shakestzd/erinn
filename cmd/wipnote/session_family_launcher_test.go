package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/agent"
)

// TestResolveSessionFamilyID_FreshLaunch verifies that a new session without
// any prior state gets its own ID as the family (self-as-family).
func TestResolveSessionFamilyID_FreshLaunch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	got := resolveSessionFamilyID(dir, "new-sess-001", "", false)
	if got != "new-sess-001" {
		t.Errorf("fresh launch family = %q, want %q", got, "new-sess-001")
	}
}

// TestResolveSessionFamilyID_InheritEnv verifies that when
// WIPNOTE_SESSION_FAMILY_ID is already set, it is reused.
func TestResolveSessionFamilyID_InheritEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "existing-family-xyz")

	got := resolveSessionFamilyID(dir, "new-sess-002", "", false)
	if got != "existing-family-xyz" {
		t.Errorf("env inherit family = %q, want %q", got, "existing-family-xyz")
	}
}

// TestResolveSessionFamilyID_ResumeInheritsFamily verifies that on resume,
// an existing family from the project index is inherited.
func TestResolveSessionFamilyID_ResumeInheritsFamily(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	// Pre-seed the family index with an existing session.
	if err := agent.RegisterSessionFamily(dir, "old-sess", "family-abc"); err != nil {
		t.Fatalf("RegisterSessionFamily: %v", err)
	}

	got := resolveSessionFamilyID(dir, "new-resumed-sess", "", true /* isResume */)
	if got != "family-abc" {
		t.Errorf("resume family = %q, want %q", got, "family-abc")
	}
}

// TestResolveSessionFamilyID_ResumeConcreteSession verifies that when a
// concrete resumed session ID is known, its OWN family is inherited even when
// other unrelated parallel-root families exist in the index. This is the
// regression guard for the map-iteration bug: resuming sess-x must yield
// family-x, never the family of an unrelated parallel root.
func TestResolveSessionFamilyID_ResumeConcreteSession(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	// Two unrelated parallel roots plus the one we will resume.
	if err := agent.RegisterSessionFamily(dir, "root-1", "family-1"); err != nil {
		t.Fatalf("register root-1: %v", err)
	}
	if err := agent.RegisterSessionFamily(dir, "sess-x", "family-x"); err != nil {
		t.Fatalf("register sess-x: %v", err)
	}
	if err := agent.RegisterSessionFamily(dir, "root-2", "family-2"); err != nil {
		t.Fatalf("register root-2: %v", err)
	}

	// Resuming the concrete session sess-x must yield family-x deterministically
	// regardless of map iteration order over {root-1, sess-x, root-2}.
	for i := 0; i < 20; i++ {
		got := resolveSessionFamilyID(dir, "new-sess", "sess-x", true /* isResume */)
		if got != "family-x" {
			t.Fatalf("iter %d: resume concrete sess-x family = %q, want %q", i, got, "family-x")
		}
	}
}

// TestResolveSessionFamilyID_ResumeLastIsMostRecent verifies that a "resume
// last" launch (no concrete session ID) inherits the family of the
// most-recently-registered session, not an arbitrary map entry.
func TestResolveSessionFamilyID_ResumeLastIsMostRecent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	if err := agent.RegisterSessionFamily(dir, "old-root", "family-old"); err != nil {
		t.Fatalf("register old-root: %v", err)
	}
	if err := agent.RegisterSessionFamily(dir, "mid-root", "family-mid"); err != nil {
		t.Fatalf("register mid-root: %v", err)
	}
	if err := agent.RegisterSessionFamily(dir, "new-root", "family-new"); err != nil {
		t.Fatalf("register new-root: %v", err)
	}

	for i := 0; i < 20; i++ {
		got := resolveSessionFamilyID(dir, "fresh-sess", "", true /* isResume */)
		if got != "family-new" {
			t.Fatalf("iter %d: resume-last family = %q, want %q (most recent)", i, got, "family-new")
		}
	}
}

// TestPersistLauncherSessionFamily_Codex verifies that the Codex launcher
// concrete write path persists session state and family index.
func TestPersistLauncherSessionFamily_Codex(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	persistLauncherSessionFamily(dir, "codex-sess-001", "codex", "codex-family-001")

	// Family index should have this session.
	idx, err := agent.ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}
	if idx["codex-sess-001"] != "codex-family-001" {
		t.Errorf("codex family = %q, want %q", idx["codex-sess-001"], "codex-family-001")
	}

	// Per-session state should be written.
	st, err := agent.ReadSessionState(dir, "codex-sess-001")
	if err != nil {
		t.Fatalf("ReadSessionState: %v", err)
	}
	if st == nil {
		t.Fatal("ReadSessionState returned nil")
	}
	if st.AgentID != "codex" {
		t.Errorf("agent_id = %q, want %q", st.AgentID, "codex")
	}
	if st.SessionFamilyID != "codex-family-001" {
		t.Errorf("family_id = %q, want %q", st.SessionFamilyID, "codex-family-001")
	}
}

// TestPersistLauncherSessionFamily_Gemini verifies that the Gemini launcher
// concrete write path persists session state and family index.
func TestPersistLauncherSessionFamily_Gemini(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	persistLauncherSessionFamily(dir, "gemini-sess-001", "gemini", "gemini-family-001")

	idx, err := agent.ReadSessionFamilyIndex(dir)
	if err != nil {
		t.Fatalf("ReadSessionFamilyIndex: %v", err)
	}
	if idx["gemini-sess-001"] != "gemini-family-001" {
		t.Errorf("gemini family = %q, want %q", idx["gemini-sess-001"], "gemini-family-001")
	}

	st, err := agent.ReadSessionState(dir, "gemini-sess-001")
	if err != nil {
		t.Fatalf("ReadSessionState: %v", err)
	}
	if st == nil {
		t.Fatal("ReadSessionState returned nil")
	}
	if st.AgentID != "gemini" {
		t.Errorf("agent_id = %q, want %q", st.AgentID, "gemini")
	}
}
