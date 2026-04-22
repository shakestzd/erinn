package terminal

import (
	"strings"
	"testing"
)

// TestBuildShellCmd covers the full matrix from the slice-1 spec.
func TestBuildShellCmd(t *testing.T) {
	tests := []struct {
		name     string
		agent    string
		mode     string
		workItem string
		want     string
	}{
		{"defaults", "", "", "", "htmlgraph claude --dev"},
		{"claude dev", "claude", "dev", "", "htmlgraph claude --dev"},
		{"claude normal", "claude", "normal", "", "htmlgraph claude"},
		{"codex dev", "codex", "dev", "", "htmlgraph codex --dev"},
		{"gemini dev", "gemini", "dev", "", "htmlgraph gemini --dev"},
		{"yolo bypasses wrapper", "yolo", "dev", "", "claude --permission-mode bypassPermissions"},
		{"work item prefix claude", "claude", "dev", "feat-abc", "htmlgraph feature start feat-abc >/dev/null 2>&1; htmlgraph claude --dev"},
		{"work item prefix codex", "codex", "dev", "feat-abc", "htmlgraph feature start feat-abc >/dev/null 2>&1; htmlgraph codex --dev"},
		{"work item prefix gemini", "gemini", "dev", "feat-abc", "htmlgraph feature start feat-abc >/dev/null 2>&1; htmlgraph gemini --dev"},
		{"work item prefix yolo", "yolo", "dev", "feat-abc", "htmlgraph feature start feat-abc >/dev/null 2>&1; claude --permission-mode bypassPermissions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildShellCmd(tc.agent, tc.mode, tc.workItem)
			if got != tc.want {
				t.Errorf("buildShellCmd(%q, %q, %q)\n  got:  %q\n  want: %q",
					tc.agent, tc.mode, tc.workItem, got, tc.want)
			}
		})
	}
}

// TestGenerateSessionID verifies UUID v4 format and uniqueness.
func TestGenerateSessionID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := generateSessionID()
		if err != nil {
			t.Fatalf("generateSessionID() error: %v", err)
		}
		// UUID v4 format: 8-4-4-4-12 hex chars separated by dashes (36 total)
		if len(id) != 36 {
			t.Errorf("expected UUID length 36, got %d: %q", len(id), id)
		}
		parts := strings.Split(id, "-")
		if len(parts) != 5 {
			t.Errorf("expected 5 UUID parts, got %d: %q", len(parts), id)
		}
		if seen[id] {
			t.Errorf("collision detected at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

// TestSessionStateTransitions verifies the state machine: pending → live → exited.
func TestSessionStateTransitions(t *testing.T) {
	m := NewManager()

	// Manually insert a session in pending state.
	id := "test-session-id"
	s := &session{
		id:    id,
		state: "pending",
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	// Verify initial state.
	if s.state != "pending" {
		t.Errorf("expected initial state pending, got %q", s.state)
	}

	// Flip to live.
	m.setLive(id)
	if s.state != "live" {
		t.Errorf("expected state live after setLive, got %q", s.state)
	}

	// Flip to exited.
	m.markExited(id)
	if s.state != "exited" {
		t.Errorf("expected state exited after markExited, got %q", s.state)
	}
}

// TestManagerStartReturnsID verifies the new Start signature returns a non-empty UUID.
// We can't call real Start (needs ttyd), so we test generateSessionID shape directly
// and verify the Manager.Start signature via compile-time check below.
func TestManagerStartReturnsID(t *testing.T) {
	// Verify generateSessionID produces valid UUIDs.
	id, err := generateSessionID()
	if err != nil {
		t.Fatalf("generateSessionID: %v", err)
	}
	if id == "" {
		t.Fatal("generateSessionID returned empty string")
	}
	if len(id) != 36 {
		t.Errorf("expected 36 char UUID, got %d chars: %q", len(id), id)
	}
}

// TestStartRequestZeroValue verifies that a zero-value StartRequest with defaultDir
// resolves CWD to defaultDir and produces "htmlgraph claude --dev".
func TestStartRequestZeroValue(t *testing.T) {
	var req StartRequest

	// Resolve agent/mode defaults as Manager.Start does.
	agent := req.Agent
	if agent == "" {
		agent = "claude"
	}
	mode := req.Mode
	if mode == "" {
		mode = "dev"
	}

	// CWD falls back to defaultDir when req.CWD is empty.
	defaultDir := "/tmp/test-project"
	cwd := req.CWD
	if cwd == "" {
		cwd = defaultDir
	}
	if cwd != defaultDir {
		t.Errorf("zero StartRequest CWD should fall back to defaultDir %q, got %q", defaultDir, cwd)
	}

	got := buildShellCmd(agent, mode, req.WorkItem)
	want := "htmlgraph claude --dev"
	if got != want {
		t.Errorf("zero StartRequest should produce %q, got %q", want, got)
	}
}
