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

func chooseClaudeLaunchIntent(projectRoot, canonicalRoot string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
	return chooseLaunchIntent(projectRoot, canonicalRoot, "claude", in, out)
}

func chooseLaunchIntent(projectRoot, canonicalRoot, harness string, in io.Reader, out io.Writer) (launcher.LaunchIntent, error) {
	rows, err := listResumableSessionsForRoot(projectRoot, canonicalRoot)
	if err != nil {
		return launcher.NewWorkIntent(), err
	}
	if len(rows) == 0 {
		return launcher.NewWorkIntent(), nil
	}
	return promptLaunchIntent(in, out, harness, rows)
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

func promptLaunchIntent(in io.Reader, out io.Writer, harness string, rows []dbpkg.ResumableSession) (launcher.LaunchIntent, error) {
	if len(rows) == 0 {
		return launcher.NewWorkIntent(), nil
	}

	fmt.Fprintln(out, "Choose how to launch:")
	fmt.Fprintln(out, "  1. Start something new")
	for i, row := range rows {
		fmt.Fprintf(out, "  %d. Continue %s\n", i+2, describeResumableSession(row))
	}
	fmt.Fprint(out, "Select [1-", len(rows)+1, "] (default 1): ")

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
		if convErr == nil && n >= 2 && n <= len(rows)+1 {
			return continueIntentForHarness(rows[n-2], harness), nil
		}
		if attempts == 2 {
			return launcher.NewWorkIntent(), fmt.Errorf("invalid selection %q", line)
		}
		fmt.Fprint(out, "Enter a number from 1 to ", len(rows)+1, ": ")
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

func describeResumableSession(row dbpkg.ResumableSession) string {
	var parts []string
	parts = append(parts, row.WorkItemID)
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
