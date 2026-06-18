package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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

func promptLaunchIntent(in io.Reader, out io.Writer, harness string, grouped dbpkg.HarnessGroupedResumableSessions) (launcher.LaunchIntent, error) {
	totalRows := len(grouped.SameHarness) + len(grouped.CrossHarness)
	if totalRows == 0 {
		return launcher.NewWorkIntent(), nil
	}

	fmt.Fprintf(out, "Choose how to launch %s:\n", formatHarnessName(harness))
	fmt.Fprintln(out, "  1. Start something new")
	optionNumber := 2
	if len(grouped.SameHarness) > 0 {
		fmt.Fprintf(out, "\nResume in %s\n", formatHarnessName(harness))
		for _, row := range grouped.SameHarness {
			fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, true))
			optionNumber++
		}
	}
	if len(grouped.CrossHarness) > 0 {
		fmt.Fprintln(out, "\nContinue from other harnesses")
		for _, row := range grouped.CrossHarness {
			fmt.Fprintf(out, "  %d. %s\n", optionNumber, describeResumableSession(row, false))
			optionNumber++
		}
	}
	fmt.Fprint(out, "Select [1-", totalRows+1, "] (default 1): ")

	reader := bufio.NewReader(in)
	orderedRows := append([]dbpkg.ResumableSession{}, grouped.SameHarness...)
	orderedRows = append(orderedRows, grouped.CrossHarness...)
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
