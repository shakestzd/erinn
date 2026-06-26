package agent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/agent"
)

// --- Detect tests ---

func TestDetect_ExplicitAgentID(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "codex")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDE_MODEL", "")
	info := agent.Detect()
	if info.ID != "codex" {
		t.Errorf("got ID=%q, want %q", info.ID, "codex")
	}
}

func TestDetect_ClaudeCode(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("CLAUDE_CODE", "1")
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CLAUDE_MODEL", "")
	info := agent.Detect()
	if info.ID != "claude-code" {
		t.Errorf("got ID=%q, want %q", info.ID, "claude-code")
	}
}

// TestDetect_ClaudeCodeNoUnderscore covers the env var name shipped by
// Claude Code 2.x (`CLAUDECODE=1`, no underscore). Without this fallback
// the orchestrator's agent_id silently became "human", which the
// agent_events CHECK constraint then rejected for every non-UserQuery
// tool_call insert, blinding the research-first yolo guard.
func TestDetect_ClaudeCodeNoUnderscore(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_MODEL", "")
	info := agent.Detect()
	if info.ID != "claude-code" {
		t.Errorf("got ID=%q, want %q", info.ID, "claude-code")
	}
}

func TestDetect_Human(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDECODE", "")
	t.Setenv("CLAUDE_MODEL", "")
	info := agent.Detect()
	if info.ID != "human" {
		t.Errorf("got ID=%q, want %q", info.ID, "human")
	}
}

func TestDetect_Model(t *testing.T) {
	t.Setenv("WIPNOTE_AGENT_ID", "")
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("CLAUDE_MODEL", "opus-4")
	info := agent.Detect()
	if info.Model != "opus-4" {
		t.Errorf("got Model=%q, want %q", info.Model, "opus-4")
	}
}

func TestDetect_Priority(t *testing.T) {
	// WIPNOTE_AGENT_ID must win over CLAUDE_CODE
	t.Setenv("WIPNOTE_AGENT_ID", "codex")
	t.Setenv("CLAUDE_CODE", "1")
	t.Setenv("CLAUDE_MODEL", "")
	info := agent.Detect()
	if info.ID != "codex" {
		t.Errorf("got ID=%q, want %q (WIPNOTE_AGENT_ID should win)", info.ID, "codex")
	}
}

// --- ResolveSessionID tests ---

func TestResolveSessionID_ExplicitEnv(t *testing.T) {
	t.Setenv("WIPNOTE_SESSION_ID", "abc")
	t.Setenv("CLAUDE_SESSION_ID", "")
	result := agent.ResolveSessionID(t.TempDir())
	if result != "abc" {
		t.Errorf("got %q, want %q", result, "abc")
	}
}

func TestResolveSessionID_ClaudeSessionID(t *testing.T) {
	testUUID := "550e8400-e29b-41d4-a716-446655440000"
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "/mock/claude-501/-Users-testuser-/"+testUUID)
	result := agent.ResolveSessionID(t.TempDir())
	if result != testUUID {
		t.Errorf("got %q, want %q", result, testUUID)
	}
}

func TestResolveSessionID_ActiveSessionFile(t *testing.T) {
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")

	dir := t.TempDir()
	wipnoteDir := filepath.Join(dir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wantSessionID := "test-session-from-file-001"
	data := map[string]interface{}{
		"session_id": wantSessionID,
		"timestamp":  1.0,
	}
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipnoteDir, ".active-session"), b, 0o644); err != nil {
		t.Fatalf("write .active-session: %v", err)
	}

	result := agent.ResolveSessionID(dir)
	if result != wantSessionID {
		t.Errorf("got %q, want %q", result, wantSessionID)
	}
}

func TestResolveSessionID_GeneratesCLI(t *testing.T) {
	t.Setenv("WIPNOTE_SESSION_ID", "")
	t.Setenv("CLAUDE_SESSION_ID", "")
	// Use a temp dir with no .active-session file
	result := agent.ResolveSessionID(t.TempDir())
	if !strings.HasPrefix(result, "cli-") {
		t.Errorf("got %q, want prefix %q", result, "cli-")
	}
	// Should contain pid and timestamp separated by dashes: cli-<pid>-<ts>
	parts := strings.Split(result, "-")
	if len(parts) != 3 {
		t.Errorf("expected format cli-<pid>-<ts>, got %q (parts=%d)", result, len(parts))
	}
	_ = fmt.Sprintf("cli-PID-TS format: %q", result)
}

// --- HarnessNativeEnvSessionID tests ---

// TestHarnessNativeEnvSessionID verifies the canonical harness-native session-id
// resolver: WIPNOTE_HARNESS selects the right env var, and Claude / no-harness
// both fall through to "" so callers can then consult WIPNOTE_SESSION_ID.
func TestHarnessNativeEnvSessionID(t *testing.T) {
	tests := []struct {
		name         string
		harness      string
		codexThread  string
		geminiSess   string
		antigravSess string
		want         string
	}{
		{
			name:        "codex: CODEX_THREAD_ID returned",
			harness:     "codex",
			codexThread: "codex-thread-abc",
			want:        "codex-thread-abc",
		},
		{
			name:       "gemini: GEMINI_SESSION_ID returned",
			harness:    "gemini",
			geminiSess: "gemini-sess-xyz",
			want:       "gemini-sess-xyz",
		},
		{
			name:         "antigravity: ANTIGRAVITY_SESSION_ID returned",
			harness:      "antigravity",
			antigravSess: "antigravity-live-456",
			want:         "antigravity-live-456",
		},
		{
			name:    "claude harness: returns empty (falls through to WIPNOTE_SESSION_ID)",
			harness: "claude",
			want:    "",
		},
		{
			name:    "no harness set: returns empty",
			harness: "",
			want:    "",
		},
		{
			name:        "codex with empty CODEX_THREAD_ID: returns empty",
			harness:     "codex",
			codexThread: "",
			want:        "",
		},
		{
			name:        "whitespace-only CODEX_THREAD_ID is trimmed to empty",
			harness:     "codex",
			codexThread: "   ",
			want:        "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WIPNOTE_HARNESS", tc.harness)
			t.Setenv("CODEX_THREAD_ID", tc.codexThread)
			t.Setenv("GEMINI_SESSION_ID", tc.geminiSess)
			t.Setenv("ANTIGRAVITY_SESSION_ID", tc.antigravSess)
			got := agent.HarnessNativeEnvSessionID()
			if got != tc.want {
				t.Errorf("HarnessNativeEnvSessionID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- NormaliseSessionID tests ---

func TestNormaliseSessionID_PlainUUID(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	result := agent.NormaliseSessionID(uuid)
	if result != uuid {
		t.Errorf("plain UUID should pass through: got %q, want %q", result, uuid)
	}
}

func TestNormaliseSessionID_PathStyle(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	path := "/mock/claude-501/-Users-testuser-DevProjects/" + uuid
	result := agent.NormaliseSessionID(path)
	if result != uuid {
		t.Errorf("should extract UUID from path: got %q, want %q", result, uuid)
	}
}

func TestNormaliseSessionID_Empty(t *testing.T) {
	result := agent.NormaliseSessionID("")
	if result != "" {
		t.Errorf("empty input should return empty: got %q", result)
	}
}

func TestNormaliseSessionID_NoUUID(t *testing.T) {
	input := "/some/path/without/uuid"
	result := agent.NormaliseSessionID(input)
	if result != input {
		t.Errorf("no UUID in path should return original: got %q, want %q", result, input)
	}
}
