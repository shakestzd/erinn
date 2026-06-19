package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
	"github.com/shakestzd/wipnote/internal/launcher"
)

type chooserEligibility struct {
	TTY              bool
	CI               bool
	ResumeID         string
	WorkItem         string
	Targeted         bool
	InPlace          bool
	ExplicitContinue bool
	Yolo             bool
	ExtraArgs        []string
}

type claudeIntentResult struct {
	mode     string
	resumeID string
	workItem string
	intent   launcher.LaunchIntent
}

func shouldOfferLaunchIntentChooser(opts chooserEligibility) bool {
	if !opts.TTY || opts.CI {
		return false
	}
	if opts.ResumeID != "" || opts.WorkItem != "" || opts.Targeted || opts.InPlace || opts.ExplicitContinue || opts.Yolo {
		return false
	}
	return len(opts.ExtraArgs) == 0
}

func isInteractiveTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

var chooseLaunchIntentFn = chooseLaunchIntent

func chooseLaunchIntent(projectRoot, canonicalRoot, harness string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
	grouped, err := listGroupedResumableSessionsForRoot(projectRoot, canonicalRoot, harness)
	if err != nil {
		return launcher.NewWorkIntent(), err
	}
	if len(grouped.SameHarness) == 0 && len(grouped.CrossHarness) == 0 {
		return launcher.NewWorkIntent(), nil
	}
	return promptLaunchIntent(in, out, harness, grouped)
}

func resolveLaunchIntentForDefaultLaunch(projectRoot, canonicalRoot, harness string, opts chooserEligibility, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
	if !shouldOfferLaunchIntentChooser(opts) {
		return launcher.NewWorkIntent(), nil
	}
	return chooseLaunchIntentFn(projectRoot, canonicalRoot, harness, in, out)
}

func listResumableSessionsForRoot(projectRoot, canonicalRoot string) ([]dbpkg.ResumableSession, error) {
	root := canonicalRoot
	if root == "" {
		root = projectRoot
	}
	if root == "" {
		return nil, nil
	}
	wipnoteDir := root + string(os.PathSeparator) + ".wipnote"
	db, err := openReadOnlyDB(wipnoteDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return dbpkg.ListResumableSessions(db, dbpkg.LivenessStalenessThreshold(root))
}

func listGroupedResumableSessionsForRoot(projectRoot, canonicalRoot, harness string) (dbpkg.HarnessGroupedResumableSessions, error) {
	root := canonicalRoot
	if root == "" {
		root = projectRoot
	}
	if root == "" {
		return dbpkg.HarnessGroupedResumableSessions{}, nil
	}
	wipnoteDir := root + string(os.PathSeparator) + ".wipnote"
	db, err := openReadOnlyDB(wipnoteDir)
	if err != nil {
		return dbpkg.HarnessGroupedResumableSessions{}, err
	}
	defer db.Close()
	return dbpkg.ListHarnessGroupedResumableSessions(db, dbpkg.LivenessStalenessThreshold(root), harness)
}

// runSelectTUIFn is the seam for tests: replace it to drive selection without a live TTY.
// It receives the writer, harness name, and pre-built options; returns the chosen int index.
var runSelectTUIFn = runSelectTUI

// runSelectTUI runs a huh Select form and returns the chosen option index.
// Returns an error when the TUI cannot run (non-TTY writer, accessible mode fail, etc.).
func runSelectTUI(in io.Reader, out io.Writer, harness string, opts []huh.Option[int]) (int, error) {
	selected := 0
	sel := huh.NewSelect[int]().
		Title(fmt.Sprintf("Choose how to launch %s:", formatHarnessName(harness))).
		Options(opts...).
		Value(&selected)

	accessible := os.Getenv("ACCESSIBLE") != ""
	form := huh.NewForm(huh.NewGroup(sel)).
		WithTheme(launchtui.WipnoteTheme()).
		WithInput(in).
		WithOutput(out).
		WithAccessible(accessible)

	if err := form.Run(); err != nil {
		return 0, err
	}
	return selected, nil
}

// buildSelectOptions constructs the huh option list for the chooser.
// Index 0 = NewWork; index i>=1 = orderedRows[i-1].
func buildSelectOptions(harness string, grouped dbpkg.HarnessGroupedResumableSessions) []huh.Option[int] {
	st := launchtui.NewStyles()
	opts := []huh.Option[int]{
		huh.NewOption(st.AccentText.Render("Start something new"), 0),
	}
	idx := 1
	for _, row := range grouped.SameHarness {
		label := fmt.Sprintf("Resume in %s: %s", formatHarnessName(harness), describeResumableSession(row, true))
		opts = append(opts, huh.NewOption(label, idx))
		idx++
	}
	for _, row := range grouped.CrossHarness {
		label := fmt.Sprintf("Continue from other harnesses: %s", describeResumableSession(row, false))
		opts = append(opts, huh.NewOption(label, idx))
		idx++
	}
	return opts
}

// mapIndexToIntent maps a 0-based option index to a LaunchIntent.
// Index 0 => NewWorkIntent; index i>=1 => continueIntentForHarness(orderedRows[i-1]).
func mapIndexToIntent(idx int, orderedRows []dbpkg.ResumableSession, harness string) launcher.LaunchIntent {
	if idx <= 0 || idx > len(orderedRows) {
		return launcher.NewWorkIntent()
	}
	return continueIntentForHarness(orderedRows[idx-1], harness)
}

// isTTYWriter returns true when w is a *os.File backed by a char-device terminal.
// Used to guard TUI rendering: huh's bubbletea backend hangs on non-TTY writers.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isInteractiveTerminalFile(f)
}

// promptLaunchIntent shows the chooser and returns the resolved LaunchIntent.
// It tries the huh TUI first when both in and out are real TTYs; if the TUI
// errors (or the writer is not a TTY) it falls through to the numeric reader.
// The upstream non-TTY gate (shouldOfferLaunchIntentChooser + isInteractiveTerminalFile)
// already ensures this function is only reached on interactive char-device stdin.
func promptLaunchIntent(in io.Reader, out io.Writer, harness string, grouped dbpkg.HarnessGroupedResumableSessions) (launcher.LaunchIntent, error) {
	totalRows := len(grouped.SameHarness) + len(grouped.CrossHarness)
	if totalRows == 0 {
		return launcher.NewWorkIntent(), nil
	}

	orderedRows := append([]dbpkg.ResumableSession{}, grouped.SameHarness...)
	orderedRows = append(orderedRows, grouped.CrossHarness...)

	// Try the huh TUI only when out is a real terminal (bubbletea hangs otherwise).
	if isTTYWriter(out) {
		opts := buildSelectOptions(harness, grouped)
		idx, tuiErr := runSelectTUIFn(in, out, harness, opts)
		if tuiErr == nil {
			return mapIndexToIntent(idx, orderedRows, harness), nil
		}
		// TUI errored — fall through to numeric reader.
	}

	// Numeric text fallback: used for non-TTY out, accessible mode, or any TUI error.
	return promptLaunchIntentNumeric(in, out, harness, orderedRows, totalRows)
}

// promptLaunchIntentNumeric is the legacy numbered-menu fallback used when the
// huh TUI cannot render (e.g. non-TTY writer, ACCESSIBLE mode, or any Run error).
func promptLaunchIntentNumeric(in io.Reader, out io.Writer, harness string, orderedRows []dbpkg.ResumableSession, totalRows int) (launcher.LaunchIntent, error) {
	fmt.Fprintf(out, "Choose how to launch %s:\n", formatHarnessName(harness))
	fmt.Fprintln(out, "  1. Start something new")
	optionNumber := 2
	sameCount := 0
	crossCount := 0
	for _, row := range orderedRows {
		if strings.EqualFold(strings.TrimSpace(row.Harness), strings.TrimSpace(harness)) {
			if sameCount == 0 {
				fmt.Fprintf(out, "\nResume in %s\n", formatHarnessName(harness))
			}
			fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, true))
			sameCount++
		} else {
			if crossCount == 0 {
				fmt.Fprintln(out, "\nContinue from other harnesses")
			}
			fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, false))
			crossCount++
		}
		optionNumber++
	}
	fmt.Fprint(out, "Select [1-", totalRows+1, "] (default 1): ")

	reader := bufio.NewReader(in)
	for attempts := 0; attempts < 3; attempts++ {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return launcher.NewWorkIntent(), nil
		}
		line = strings.TrimSpace(line)
		if line == "" || line == "1" {
			return launcher.NewWorkIntent(), nil
		}
		n, convErr := strconv.Atoi(line)
		if convErr == nil && n >= 2 && n <= totalRows+1 {
			return continueIntentForHarness(orderedRows[n-2], harness), nil
		}
		if attempts == 2 {
			return launcher.NewWorkIntent(), fmt.Errorf("invalid selection %q", line)
		}
		fmt.Fprint(out, "Enter a number from 1 to ", totalRows+1, ": ")
	}
	return launcher.NewWorkIntent(), nil
}

func continueIntentForHarness(row dbpkg.ResumableSession, harness string) launcher.LaunchIntent {
	return launcher.ContinueWorkIntent(
		row.WorkItemID,
		row.Harness,
		resumeSessionIDForHarness(row, harness),
		row.ExecWorktreePath,
		true,
	)
}

func resumeSessionIDForHarness(row dbpkg.ResumableSession, harness string) string {
	if strings.EqualFold(strings.TrimSpace(row.Harness), strings.TrimSpace(harness)) {
		return row.LastSessionID
	}
	return ""
}

func describeResumableSession(row dbpkg.ResumableSession, sameHarness bool) string {
	var parts []string
	if sameHarness {
		parts = append(parts, "Resume transcript for", row.WorkItemID)
	} else {
		parts = append(parts, "Fresh session with handoff for", row.WorkItemID)
	}
	if row.Title != "" {
		parts = append(parts, strconv.Quote(row.Title))
	}
	meta := []string{row.Harness}
	if row.Type != "" {
		meta = append(meta, row.Type)
	}
	if row.Live {
		meta = append(meta, "live")
	}
	if row.LastActivity != "" {
		meta = append(meta, "last "+row.LastActivity)
	}
	if row.ExecWorktreePath != "" {
		meta = append(meta, row.ExecWorktreePath)
	}
	if len(meta) > 0 {
		parts = append(parts, "("+strings.Join(meta, ", ")+")")
	}
	return strings.Join(parts, " ")
}

func formatHarnessName(harness string) string {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini"
	case "antigravity":
		return "Antigravity"
	default:
		h := strings.TrimSpace(harness)
		if h == "" {
			return "Harness"
		}
		return strings.ToUpper(h[:1]) + h[1:]
	}
}

func applyClaudeLaunchIntent(resumeID, workItem string, intent launcher.LaunchIntent) claudeIntentResult {
	result := claudeIntentResult{
		mode:     "default",
		resumeID: resumeID,
		workItem: workItem,
		intent:   intent,
	}
	if !intent.WantsContinue() {
		return result
	}
	result.mode = "continue"
	if result.workItem == "" && intent.WorkItemID != "" {
		result.workItem = intent.WorkItemID
	}
	if result.resumeID == "" {
		result.resumeID = intent.ResumeForHarness("claude")
	}
	return result
}
