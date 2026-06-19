package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/shakestzd/wipnote/cmd/wipnote/launchtui"
	"github.com/shakestzd/wipnote/core/agent"
	dbpkg "github.com/shakestzd/wipnote/core/db"
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
	// Skip the chooser only when there is genuinely nothing to resume. The
	// current-session slot counts: it is the whole point of this path for a
	// split-child session with no (or a completed) work item, which never
	// appears in the same/cross-harness groups.
	if !hasResumableOptions(grouped) {
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
	threshold := dbpkg.LivenessStalenessThreshold(root)
	grouped, err := dbpkg.ListHarnessGroupedResumableSessions(db, threshold, harness)
	if err != nil {
		return grouped, err
	}
	// Surface the session the user is launching from as a first-class slot. This
	// bypasses the work_item_id <> '' gate the grouped listing applies, which
	// otherwise hides session-split children that never got work-item attribution.
	// Degrades cleanly: on any error or no resolvable current session, the grouped
	// listing is returned unchanged.
	if current, cerr := dbpkg.GetCurrentSessionResumable(db, threshold, resolveCurrentSessionIDs(root)); cerr == nil && current != nil && isActionableCurrentSession(*current, harness) {
		grouped = withCurrentSession(grouped, current)
	}
	return grouped, nil
}

// isActionableCurrentSession reports whether the current-session slot would
// resolve to a meaningful launch intent for the target harness. A same-harness
// row needs a resume session ID (it resumes the live transcript); a cross-harness
// row is only useful as a handoff when it carries a work item or worktree, since
// cross-harness resume IDs are intentionally suppressed. Offering a cross-harness
// row with neither would yield a continue intent with empty resume ID AND empty
// work item — which a launcher can misread as "continue latest" and resume an
// unrelated session.
func isActionableCurrentSession(row dbpkg.ResumableSession, harness string) bool {
	if strings.EqualFold(strings.TrimSpace(row.Harness), strings.TrimSpace(harness)) {
		return strings.TrimSpace(row.LastSessionID) != ""
	}
	return strings.TrimSpace(row.WorkItemID) != "" || strings.TrimSpace(row.ExecWorktreePath) != ""
}

// resolveCurrentSessionIDs returns the candidate session IDs for "the session the
// user is launching from": the harness/launcher session env vars plus every
// member of that session's family. Including the family expands the short parent
// stub to the long-running split-child IDs, so whichever one carries the live
// transcript can be surfaced. Returns nil when nothing is resolvable.
func resolveCurrentSessionIDs(projectRoot string) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	for _, env := range []string{
		"WIPNOTE_SESSION_ID",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_SESSION_ID",
		"WIPNOTE_PARENT_SESSION",
	} {
		add(os.Getenv(env))
	}

	famID := strings.TrimSpace(os.Getenv("WIPNOTE_SESSION_FAMILY_ID"))
	if famID == "" {
		for _, id := range ids {
			if f := agent.SessionFamilyFor(projectRoot, id); f != "" {
				famID = f
				break
			}
		}
	}
	if famID != "" {
		add(famID)
		if idx, err := agent.ReadSessionFamilyIndex(projectRoot); err == nil {
			for sid, fid := range idx {
				if fid == famID {
					add(sid)
				}
			}
		}
	}
	return ids
}

// withCurrentSession sets the current-session slot and removes any duplicate of
// it from the same/cross-harness groups so it is never listed twice.
func withCurrentSession(grouped dbpkg.HarnessGroupedResumableSessions, current *dbpkg.ResumableSession) dbpkg.HarnessGroupedResumableSessions {
	grouped.Current = current
	grouped.SameHarness = dropSessionByID(grouped.SameHarness, current.LastSessionID)
	grouped.CrossHarness = dropSessionByID(grouped.CrossHarness, current.LastSessionID)
	return grouped
}

// dropSessionByID returns rows with any entry whose LastSessionID equals
// sessionID removed. Returns rows unchanged when sessionID is empty.
func dropSessionByID(rows []dbpkg.ResumableSession, sessionID string) []dbpkg.ResumableSession {
	if sessionID == "" {
		return rows
	}
	out := make([]dbpkg.ResumableSession, 0, len(rows))
	for _, r := range rows {
		if r.LastSessionID == sessionID {
			continue
		}
		out = append(out, r)
	}
	return out
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
// Index 0 = NewWork; index i>=1 = orderedRows[i-1], where orderedRows is
// [Current?] + SameHarness + CrossHarness — matching orderedRowsFor below.
func buildSelectOptions(harness string, grouped dbpkg.HarnessGroupedResumableSessions) []huh.Option[int] {
	st := launchtui.NewStyles()
	opts := []huh.Option[int]{
		huh.NewOption(st.AccentText.Render("Start something new"), 0),
	}
	idx := 1
	if grouped.Current != nil {
		label := fmt.Sprintf("Resume this session: %s", describeCurrentSession(*grouped.Current))
		opts = append(opts, huh.NewOption(st.AccentText.Render(label), idx))
		idx++
	}
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

// hasResumableOptions reports whether a grouped listing has anything the chooser
// can offer. The current-session slot counts even when both harness groups are
// empty — it lives outside them, so the gate must not be derived from group
// emptiness alone (that would drop the slot in exactly the split-child case).
func hasResumableOptions(grouped dbpkg.HarnessGroupedResumableSessions) bool {
	return grouped.Current != nil || len(grouped.SameHarness) > 0 || len(grouped.CrossHarness) > 0
}

// orderedRowsFor flattens a grouped listing into the index order the chooser
// uses: the current-session slot first (when present), then same-harness, then
// cross-harness. mapIndexToIntent resolves option index i>=1 to orderedRows[i-1].
func orderedRowsFor(grouped dbpkg.HarnessGroupedResumableSessions) []dbpkg.ResumableSession {
	ordered := make([]dbpkg.ResumableSession, 0, len(grouped.SameHarness)+len(grouped.CrossHarness)+1)
	if grouped.Current != nil {
		ordered = append(ordered, *grouped.Current)
	}
	ordered = append(ordered, grouped.SameHarness...)
	ordered = append(ordered, grouped.CrossHarness...)
	return ordered
}

// mapIndexToIntent maps a 0-based option index to a LaunchIntent.
// Index 0 => NewWorkIntent; index i>=1 => continueIntentForHarness(orderedRows[i-1]).
func mapIndexToIntent(idx int, orderedRows []dbpkg.ResumableSession, harness string) launcher.LaunchIntent {
	if idx <= 0 || idx > len(orderedRows) {
		return launcher.NewWorkIntent()
	}
	return continueIntentForHarness(orderedRows[idx-1], harness)
}

// isTTYWriterFn is the seam for tests: replace it to force the TUI path on a
// non-terminal writer (e.g. bytes.Buffer) without a live char-device.
var isTTYWriterFn = isTTYWriter

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
	orderedRows := orderedRowsFor(grouped)
	totalRows := len(orderedRows)
	if totalRows == 0 {
		return launcher.NewWorkIntent(), nil
	}

	// Try the huh TUI only when out is a real terminal (bubbletea hangs otherwise).
	if isTTYWriterFn(out) {
		opts := buildSelectOptions(harness, grouped)
		idx, tuiErr := runSelectTUIFn(in, out, harness, opts)
		if tuiErr == nil {
			return mapIndexToIntent(idx, orderedRows, harness), nil
		}
		// An explicit user abort (Ctrl-C / Esc) cancels the launch: propagate the
		// error so the caller aborts, rather than silently starting new work.
		if errors.Is(tuiErr, huh.ErrUserAborted) {
			return launcher.NewWorkIntent(), tuiErr
		}
		// Other TUI errors (render / TTY failures) fall through to the numeric reader.
	}

	// Numeric text fallback: used for non-TTY out, accessible mode, or any TUI error.
	return promptLaunchIntentNumeric(in, out, harness, grouped, orderedRows, totalRows)
}

// promptLaunchIntentNumeric is the legacy numbered-menu fallback used when the
// huh TUI cannot render (e.g. non-TTY writer, ACCESSIBLE mode, or any Run error).
func promptLaunchIntentNumeric(in io.Reader, out io.Writer, harness string, grouped dbpkg.HarnessGroupedResumableSessions, orderedRows []dbpkg.ResumableSession, totalRows int) (launcher.LaunchIntent, error) {
	fmt.Fprintf(out, "Choose how to launch %s:\n", formatHarnessName(harness))
	fmt.Fprintln(out, "  1. Start something new")
	optionNumber := 2
	// Current-session slot first, with its own header, so the session the user is
	// launching from is unmistakable even in the numeric fallback path.
	if grouped.Current != nil {
		fmt.Fprintln(out, "\nResume this session")
		fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeCurrentSession(*grouped.Current))
		optionNumber++
	}
	sameCount := 0
	crossCount := 0
	for _, row := range grouped.SameHarness {
		if sameCount == 0 {
			fmt.Fprintf(out, "\nResume in %s\n", formatHarnessName(harness))
		}
		fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, true))
		sameCount++
		optionNumber++
	}
	for _, row := range grouped.CrossHarness {
		if crossCount == 0 {
			fmt.Fprintln(out, "\nContinue from other harnesses")
		}
		fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, false))
		crossCount++
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

// describeCurrentSession renders the "Resume this session" slot. Unlike
// describeResumableSession it tolerates a missing work item (the common
// session-split case) and leads with the session identity rather than a work
// item, since the whole point of the slot is "the session you're in right now".
func describeCurrentSession(row dbpkg.ResumableSession) string {
	var parts []string
	if row.WorkItemID != "" {
		parts = append(parts, "Resume transcript for", row.WorkItemID)
		if row.Title != "" {
			parts = append(parts, strconv.Quote(row.Title))
		}
	} else {
		parts = append(parts, "Resume current transcript")
	}
	meta := []string{}
	if row.Harness != "" {
		meta = append(meta, row.Harness)
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
