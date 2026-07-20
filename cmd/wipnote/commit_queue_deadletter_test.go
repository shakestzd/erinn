package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/commitqueue"
)

// withEmptyStdin redirects os.Stdin to an already-closed pipe for the
// duration of the test, so promptYesNo's ReadString hits an immediate EOF
// (answer "no") instead of blocking on whatever the test binary's real stdin
// happens to be (a TTY when run interactively).
func withEmptyStdin(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}

// withTestOutbox redirects the commitOutboxPath seam to a temp file for the
// duration of the test, mirroring TestRecordCommitIntentAppendsAfterCanonicalWrite.
func withTestOutbox(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "commit-outbox.ndjson")
	orig := commitOutboxPath
	commitOutboxPath = func(string) (string, error) { return path, nil }
	t.Cleanup(func() { commitOutboxPath = orig })
	return path
}

// seedDeadLetteredIntent appends one intent then dead-letters it via a
// single-attempt flush against an always-failing committer, so tests exercise
// the same code path production uses (rather than hand-writing the NDJSON).
func seedDeadLetteredIntent(t *testing.T, workItemID string) *commitqueue.Outbox {
	t.Helper()
	repoRoot, err := resolveProjectRoot()
	if err != nil {
		t.Fatalf("resolveProjectRoot: %v", err)
	}
	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		t.Fatalf("openCommitOutbox: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   repoRoot,
		RelPaths:   []string{".wipnote/features/" + workItemID + ".html"},
		Message:    "wipnote: complete " + workItemID,
		WorkItemID: workItemID,
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := ob.Flush(func(commitqueue.Intent) error {
		return fmt.Errorf("simulated git failure for %s", workItemID)
	}, 1); err != nil {
		t.Fatalf("seed flush: %v", err)
	}
	return ob
}

// runDeadLetterCmd executes a `dead-letter` subcommand and returns captured
// stdout plus any error.
func runDeadLetterCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := commitQueueDeadLetterCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestDeadLetterList_ShowsReasonAndCounts(t *testing.T) {
	withTestOutbox(t)
	seedDeadLetteredIntent(t, "feat-1")

	out, err := runDeadLetterCmd(t, "list")
	if err != nil {
		t.Fatalf("dead-letter list: %v", err)
	}
	if !strings.Contains(out, "work_item=feat-1") {
		t.Fatalf("list output missing work item: %q", out)
	}
	if !strings.Contains(out, "simulated git failure for feat-1") {
		t.Fatalf("list output missing failure reason: %q", out)
	}
	if !strings.Contains(out, "attempts=1") {
		t.Fatalf("list output missing attempt count: %q", out)
	}
}

func TestDeadLetterList_EmptyQueueSaysSo(t *testing.T) {
	withTestOutbox(t)
	out, err := runDeadLetterCmd(t, "list")
	if err != nil {
		t.Fatalf("dead-letter list: %v", err)
	}
	if !strings.Contains(out, "empty") {
		t.Fatalf("expected empty-queue message, got %q", out)
	}
}

func TestDeadLetterRetry_RoundTripsBackToPending(t *testing.T) {
	withTestOutbox(t)
	ob := seedDeadLetteredIntent(t, "feat-1")

	out, err := runDeadLetterCmd(t, "retry", "feat-1")
	if err != nil {
		t.Fatalf("dead-letter retry: %v", err)
	}
	if !strings.Contains(out, "re-enqueued 1") {
		t.Fatalf("retry output missing confirmation: %q", out)
	}

	dlDepth, _ := ob.DeadLetterDepth()
	if dlDepth != 0 {
		t.Fatalf("dead-letter depth after retry = %d, want 0", dlDepth)
	}
	pending, _ := ob.Pending()
	if len(pending) != 1 || pending[0].WorkItemID != "feat-1" {
		t.Fatalf("intent not re-enqueued: %+v", pending)
	}
}

func TestDeadLetterRetry_AllFlag(t *testing.T) {
	withTestOutbox(t)
	ob := seedDeadLetteredIntent(t, "feat-1")
	seedDeadLetteredIntentInto(t, ob, "feat-2")

	out, err := runDeadLetterCmd(t, "retry", "--all")
	if err != nil {
		t.Fatalf("dead-letter retry --all: %v", err)
	}
	if !strings.Contains(out, "re-enqueued 2") {
		t.Fatalf("retry --all output missing count: %q", out)
	}
	dlDepth, _ := ob.DeadLetterDepth()
	if dlDepth != 0 {
		t.Fatalf("dead-letter depth after retry --all = %d, want 0", dlDepth)
	}
}

func TestDeadLetterRetry_RejectsBothIDAndAll(t *testing.T) {
	withTestOutbox(t)
	seedDeadLetteredIntent(t, "feat-1")
	if _, err := runDeadLetterCmd(t, "retry", "feat-1", "--all"); err == nil {
		t.Fatal("expected error when both an id and --all are supplied")
	}
}

func TestDeadLetterRetry_RequiresIDOrAll(t *testing.T) {
	withTestOutbox(t)
	seedDeadLetteredIntent(t, "feat-1")
	if _, err := runDeadLetterCmd(t, "retry"); err == nil {
		t.Fatal("expected error when neither an id nor --all is supplied")
	}
}

func TestDeadLetterClear_RequiresConfirmation(t *testing.T) {
	withTestOutbox(t)
	withEmptyStdin(t)
	ob := seedDeadLetteredIntent(t, "feat-1")

	// No --yes and stdin is at EOF -> promptYesNo treats that as "no".
	out, err := runDeadLetterCmd(t, "clear", "feat-1")
	if err != nil {
		t.Fatalf("dead-letter clear: %v", err)
	}
	if !strings.Contains(out, "aborted") {
		t.Fatalf("expected abort message without confirmation, got %q", out)
	}
	dlDepth, _ := ob.DeadLetterDepth()
	if dlDepth != 1 {
		t.Fatalf("unconfirmed clear must not drop the intent, depth = %d", dlDepth)
	}
}

func TestDeadLetterClear_YesFlagDropsMatching(t *testing.T) {
	withTestOutbox(t)
	ob := seedDeadLetteredIntent(t, "feat-1")
	seedDeadLetteredIntentInto(t, ob, "feat-2")

	out, err := runDeadLetterCmd(t, "clear", "feat-1", "--yes")
	if err != nil {
		t.Fatalf("dead-letter clear --yes: %v", err)
	}
	if !strings.Contains(out, "dropped 1") {
		t.Fatalf("clear output missing count: %q", out)
	}
	dl, _ := ob.DeadLettered()
	if len(dl) != 1 || dl[0].WorkItemID != "feat-2" {
		t.Fatalf("clear removed the wrong intent: %+v", dl)
	}
}

func TestDeadLetterClear_NoMatchIsReportedNotErrored(t *testing.T) {
	withTestOutbox(t)
	out, err := runDeadLetterCmd(t, "clear", "does-not-exist", "--yes")
	if err != nil {
		t.Fatalf("dead-letter clear on empty queue: %v", err)
	}
	if !strings.Contains(out, "no matching") {
		t.Fatalf("expected no-match message, got %q", out)
	}
}

// seedDeadLetteredIntentInto appends and dead-letters a second intent into an
// already-open outbox (avoids re-resolving commitOutboxPath).
func seedDeadLetteredIntentInto(t *testing.T, ob *commitqueue.Outbox, workItemID string) {
	t.Helper()
	repoRoot, err := resolveProjectRoot()
	if err != nil {
		t.Fatalf("resolveProjectRoot: %v", err)
	}
	if err := ob.Append(commitqueue.Intent{
		RepoRoot:   repoRoot,
		RelPaths:   []string{".wipnote/features/" + workItemID + ".html"},
		Message:    "wipnote: complete " + workItemID,
		WorkItemID: workItemID,
		Action:     "complete",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := ob.Flush(func(commitqueue.Intent) error {
		return fmt.Errorf("simulated git failure for %s", workItemID)
	}, 1); err != nil {
		t.Fatalf("seed flush: %v", err)
	}
}

// --- flush / status dead-letter warning (GH#155) ---

func TestCommitQueueFlush_WarnsOnNonZeroDeadLetterDepth(t *testing.T) {
	withTestOutbox(t)
	seedDeadLetteredIntent(t, "feat-1")

	var buf bytes.Buffer
	cmd := commitQueueFlushCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dead-letter-depth=1") {
		t.Fatalf("flush output missing depth: %q", out)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "dead-letter list") {
		t.Fatalf("flush must print an actionable warning pointing at the inspect command, got %q", out)
	}
}

func TestCommitQueueFlush_NoWarningWhenDeadLetterEmpty(t *testing.T) {
	withTestOutbox(t)

	var buf bytes.Buffer
	cmd := commitQueueFlushCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("flush must not warn when dead-letter is empty, got %q", buf.String())
	}
}

func TestCommitQueueStatus_WarnsOnNonZeroDeadLetterDepth(t *testing.T) {
	withTestOutbox(t)
	seedDeadLetteredIntent(t, "feat-1")

	var buf bytes.Buffer
	cmd := commitQueueStatusCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "dead-letter list") {
		t.Fatalf("status must warn and point at the inspect command, got %q", out)
	}
}
