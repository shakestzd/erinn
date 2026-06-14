package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestHarnessList_Output verifies that runHarnessList writes a header row and
// one row per registered harness to the provided writer.
func TestHarnessList_Output(t *testing.T) {
	var buf bytes.Buffer
	if err := runHarnessList(&buf); err != nil {
		t.Fatalf("runHarnessList returned error: %v", err)
	}

	out := buf.String()

	// Header row
	for _, col := range []string{"ID", "AgentID", "ServiceNames", "SessionAttr", "HookEventNames"} {
		if !strings.Contains(out, col) {
			t.Errorf("output missing header column %q\n\nGot:\n%s", col, out)
		}
	}

	// claude_code row
	if !strings.Contains(out, "claude_code") {
		t.Errorf("output missing claude_code row\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("output missing claude AgentID\n\nGot:\n%s", out)
	}

	// codex row
	if !strings.Contains(out, "codex") {
		t.Errorf("output missing codex row\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "codex-cli") {
		t.Errorf("output missing codex-cli service name\n\nGot:\n%s", out)
	}

	// gemini_cli row
	if !strings.Contains(out, "gemini_cli") {
		t.Errorf("output missing gemini_cli row\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "gemini") {
		t.Errorf("output missing gemini AgentID\n\nGot:\n%s", out)
	}

	// antigravity_cli row
	if !strings.Contains(out, "antigravity_cli") {
		t.Errorf("output missing antigravity_cli row\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "antigravity") {
		t.Errorf("output missing antigravity AgentID\n\nGot:\n%s", out)
	}

	// BeforeAgent is one of Gemini/Antigravity's HookEventNames
	if !strings.Contains(out, "BeforeAgent") {
		t.Errorf("output missing BeforeAgent (HookEventName)\n\nGot:\n%s", out)
	}
}

// TestHarnessList_RowCount verifies there are exactly 5 lines in the output:
// 1 header row + 4 harness rows (claude_code, codex, gemini_cli, antigravity_cli).
func TestHarnessList_RowCount(t *testing.T) {
	var buf bytes.Buffer
	if err := runHarnessList(&buf); err != nil {
		t.Fatalf("runHarnessList returned error: %v", err)
	}

	// Split on newline, drop trailing empty entry from final newline.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines (1 header + 4 harness rows), got %d:\n%s",
			len(lines), buf.String())
	}
}
