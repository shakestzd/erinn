package launchtui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestRunWithSpinner_NonTTYPassthrough verifies the graceful-degrade contract:
// off a TTY, RunWithSpinner emits the label as a static line and calls fn with
// the caller's writer directly. No animation or spinner glyphs are added, but the
// label and fn output both appear (feat-e97607b3, bug-7be9b180).
func TestRunWithSpinner_NonTTYPassthrough(t *testing.T) {
	var buf bytes.Buffer
	calls := 0
	err := RunWithSpinner(&buf, "Preparing worktree", func(w io.Writer) error {
		calls++
		_, _ = io.WriteString(w, "  Worktree: /tmp/wt (branch: feat-x)\n")
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn should be called exactly once, got %d", calls)
	}
	output := buf.String()
	// The output should now contain both the label line and fn's output
	if !strings.Contains(output, "Preparing worktree") {
		t.Errorf("off-TTY output must include the label; got %q", output)
	}
	if !strings.Contains(output, "Worktree: /tmp/wt (branch: feat-x)") {
		t.Errorf("off-TTY output must include fn's output; got %q", output)
	}
	// The label should be prefixed with ✓ to match TTY resolution style
	if !strings.Contains(output, "✓ Preparing worktree") {
		t.Errorf("off-TTY label should be prefixed with ✓; got %q", output)
	}
}

// TestRunWithSpinner_NonTTYErrorPropagates verifies fn's error is returned
// unchanged and fn's output still passes through.
func TestRunWithSpinner_NonTTYErrorPropagates(t *testing.T) {
	var buf bytes.Buffer
	sentinel := errors.New("worktree add failed")
	err := RunWithSpinner(&buf, "Preparing worktree", func(w io.Writer) error {
		_, _ = io.WriteString(w, "  Warning: boom\n")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
	if !strings.Contains(buf.String(), "Warning: boom") {
		t.Errorf("fn output should still pass through on error; got %q", buf.String())
	}
}

// TestRunWithSpinner_NonTTYEmitsLabel verifies that on a non-TTY writer,
// RunWithSpinner emits the label as a static line (with ✓ prefix) before
// calling fn, even when fn produces no output (bug-7be9b180).
func TestRunWithSpinner_NonTTYEmitsLabel(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{name: "Initializing", label: "Initializing"},
		{name: "session telemetry collector", label: "starting session telemetry collector..."},
		{name: "Preparing worktree", label: "Preparing worktree"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := RunWithSpinner(&buf, tt.label, func(w io.Writer) error {
				// fn produces no output; we should still see the label line
				return nil
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			output := buf.String()
			expected := "✓ " + tt.label
			if !strings.Contains(output, expected) {
				t.Errorf("off-TTY output must contain label line %q; got %q", expected, output)
			}
		})
	}
}

// TestIsTTYWriter_NonTTY verifies common non-terminal writers are detected as
// non-TTY so the spinner takes the plain passthrough path.
func TestIsTTYWriter_NonTTY(t *testing.T) {
	if isTTYWriter(&bytes.Buffer{}) {
		t.Error("bytes.Buffer must not be reported as a TTY")
	}
	if isTTYWriter(io.Discard) {
		t.Error("io.Discard must not be reported as a TTY")
	}
}
