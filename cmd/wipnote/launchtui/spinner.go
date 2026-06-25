package launchtui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// isTTYWriter reports whether w is a character device (an interactive terminal).
// Animation and ANSI cursor control are only emitted for real TTYs; everything
// else (pipes, files, io.Discard, test buffers) takes a plain passthrough path
// so log/CI output is never polluted with control sequences.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// RunWithSpinner runs fn while showing an animated bubbles/spinner labelled
// `label`, giving the user live feedback during an otherwise-blank wait instead
// of a frozen static line. fn receives an io.Writer for its own status output.
//
//   - On a TTY: the spinner animates on one line while fn runs in a goroutine;
//     fn's output is captured and rendered as a composed block beneath a final
//     ✓ / ✗ resolution line once fn returns.
//   - Off a TTY (pipe, file, io.Discard, tests): NO animation and NO extra
//     chrome — a static label line is emitted before fn is called with w directly.
//     fn's output then follows. This preserves the operational signal (the label)
//     while keeping log/CI output plain and readable (feat-e97607b3, bug-7be9b180).
//
// The spinner uses bubbles/spinner frame data driven by a plain ticker rather
// than a full bubbletea program, so it never enters raw mode or the alternate
// screen and cannot disturb the terminal state the launched harness inherits.
func RunWithSpinner(w io.Writer, label string, fn func(io.Writer) error) error {
	if !isTTYWriter(w) {
		s := NewStyles()
		fmt.Fprintln(w, s.StatusGreen.Render("✓ "+label))
		return fn(w)
	}

	s := NewStyles()
	sp := spinner.Dot
	if len(sp.Frames) == 0 {
		sp = spinner.Line
	}
	if sp.FPS <= 0 {
		sp.FPS = time.Second / 10
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- fn(&buf) }()

	// Deliberately do NOT hide the cursor: Go does not run defers when a default
	// SIGINT/SIGTERM handler terminates the process, so a hidden cursor could be
	// left behind if the user interrupts the worktree op (roborev 569). A cursor
	// resting at the end of the spinner line is a fine, leak-proof alternative.
	ticker := time.NewTicker(sp.FPS)
	defer ticker.Stop()

	var err error
	frame := 0
loop:
	for {
		select {
		case err = <-done:
			break loop
		case <-ticker.C:
			glyph := sp.Frames[frame%len(sp.Frames)]
			frame++
			fmt.Fprintf(w, "\r%s %s", s.Accent.Render(glyph), s.TextPrimary.Render(label+"…"))
		}
	}

	fmt.Fprint(w, "\r\x1b[K") // clear the spinner line

	resolution := s.StatusGreen.Render("✓ " + label)
	if err != nil {
		resolution = s.StatusRed.Render("✗ " + label)
	}

	// Compose the resolution line above fn's captured detail rows as one
	// left-aligned unit (lipgloss JoinVertical), matching the banner layout.
	rows := []string{resolution}
	if details := strings.TrimRight(buf.String(), "\n"); details != "" {
		rows = append(rows, details)
	}
	fmt.Fprintln(w, lipgloss.JoinVertical(lipgloss.Left, rows...))

	return err
}
