package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/core/sessionledger"
)

const (
	backfillArchivedSession = "019e4d87f58b4e932cf9001d92a8"         // 28-hex shape, archived only
	backfillHTMLSession     = "aaaabbbb-cccc-dddd-eeee-ffff00002222" // session HTML survives
	backfillSubagentSession = "bbbbcccc-dddd-eeee-ffff-000011113333" // subagent record — must be skipped
	backfillBothSession     = "ccccdddd-eeee-ffff-0000-111122224444" // HTML and an archive
)

func backfillTestProject(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	for _, sub := range []string{"sessions", filepath.Join("archive", "2026-05")} {
		if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote", sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return projectDir
}

// writeBackfillSessionHTML writes a canonical session HTML record carrying the
// attributes the backfill reads.
func writeBackfillSessionHTML(t *testing.T, wipnoteDir, id, started, ended string, subagent bool) {
	t.Helper()
	html := fmt.Sprintf(`<!DOCTYPE html><html><body>
<article id="%s" data-type="session" data-status="completed"
         data-agent="claude-code" data-project-dir="."
         data-started-at="%s" data-ended-at="%s"
         data-event-count="412" data-is-subagent="%t">
</article></body></html>`, id, started, ended, subagent)
	if err := os.WriteFile(filepath.Join(wipnoteDir, "sessions", id+".html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write session html %s: %v", id, err)
	}
}

// writeBackfillArchive writes a session tarball whose single entry is an
// events.ndjson beginning with a session-start line, matching the real archive
// layout that retention produces.
func writeBackfillArchive(t *testing.T, wipnoteDir, id, firstTS, lastTS string, mtime time.Time) string {
	t.Helper()
	payload := fmt.Sprintf(
		`{"kind":"log","harness":"wipnote","ts":"%s","session_id":"%s","canonical":"session_start"}`+"\n"+
			`{"kind":"log","harness":"wipnote","ts":"%s","session_id":"%s","canonical":"tool_use"}`+"\n",
		firstTS, id, lastTS, id)

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: id + "/events.ndjson",
		Mode: 0o644,
		Size: int64(len(payload)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(payload)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	path := filepath.Join(wipnoteDir, "archive", "2026-05", id+".tar.gz")
	if err := os.WriteFile(path, gzBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive %s: %v", id, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", id, err)
	}
	return path
}

func runBackfill(t *testing.T, wipnoteDir string, dryRun bool) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := runSessionLedgerBackfill(cmd, wipnoteDir, dryRun); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return out.String()
}

// TestBackfillRecoversSessionsFromBothDurableSources is the backfill contract:
// every session with surviving durable evidence gets a row, and the two sources
// merge rather than compete.
func TestBackfillRecoversSessionsFromBothDurableSources(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")

	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)
	writeBackfillSessionHTML(t, wipnoteDir, backfillSubagentSession,
		"2026-08-01T09:10:00Z", "2026-08-01T09:20:00Z", true)
	// The mtime is deliberately WEEKS after the last event: retention archives
	// long after a session stops, so trusting the mtime as the end time would
	// report a 47-day session (measured on this repo's own archive).
	archiveMtime := time.Date(2026, 7, 8, 13, 23, 1, 0, time.UTC)
	writeBackfillArchive(t, wipnoteDir, backfillArchivedSession,
		"2026-05-22T02:33:36.413551295Z", "2026-05-22T03:10:00Z", archiveMtime)
	writeBackfillSessionHTML(t, wipnoteDir, backfillBothSession,
		"2026-05-23T08:00:00Z", "2026-05-23T10:00:00Z", false)
	writeBackfillArchive(t, wipnoteDir, backfillBothSession,
		"2026-05-23T08:00:00Z", "2026-05-23T09:55:00Z",
		time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC))

	runBackfill(t, wipnoteDir, false)

	store := sessionledger.NewStore(wipnoteDir)
	recs, err := store.ReadAll()
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	byID := map[string]sessionledger.Record{}
	for _, r := range recs {
		byID[r.SessionID] = r
	}

	// Archive-only: the tarball is the whole record. Its filename gives the id
	// and its events give the real interval — first timestamp to last.
	arch, ok := byID[backfillArchivedSession]
	if !ok {
		t.Fatalf("archive-only session %s was not recovered", backfillArchivedSession)
	}
	wantStart := time.Date(2026, 5, 22, 2, 33, 36, 413551295, time.UTC)
	if !arch.StartedAt.Equal(wantStart) {
		t.Errorf("archive-only start: got %v, want %v (the first event)", arch.StartedAt, wantStart)
	}
	wantEnd := time.Date(2026, 5, 22, 3, 10, 0, 0, time.UTC)
	if !arch.EndedAt.Equal(wantEnd) {
		t.Errorf("archive-only end: got %v, want %v (the LAST event). The tarball mtime is %v — "+
			"that is when retention created the archive, not when the session stopped, and using it "+
			"would report a %.0f-day session.",
			arch.EndedAt, wantEnd, archiveMtime, archiveMtime.Sub(wantStart).Hours()/24)
	}
	if arch.ArchivePath == "" {
		t.Error("archive-only session has no archive path — the pointer back to the raw events is lost")
	}

	// HTML-only: the fuller record, including the event count.
	htmlRec, ok := byID[backfillHTMLSession]
	if !ok {
		t.Fatalf("session %s with surviving HTML was not recovered", backfillHTMLSession)
	}
	if htmlRec.Harness != "claude-code" || htmlRec.Events != 412 {
		t.Errorf("HTML-sourced row lost detail: harness=%q events=%d", htmlRec.Harness, htmlRec.Events)
	}

	// Both sources: HTML's end wins over the later tarball mtime, and the
	// archive still contributes the path HTML does not know.
	both, ok := byID[backfillBothSession]
	if !ok {
		t.Fatalf("session %s present in both sources was not recovered", backfillBothSession)
	}
	if want := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC); !both.EndedAt.Equal(want) {
		t.Errorf("merged end: got %v, want the HTML end %v, not the archive mtime", both.EndedAt, want)
	}
	if both.ArchivePath == "" {
		t.Error("merged row did not pick up the archive path from the tarball")
	}

	// Root sessions only.
	if _, found := byID[backfillSubagentSession]; found {
		t.Errorf("subagent session %s got a ledger row; the ledger holds root sessions only",
			backfillSubagentSession)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)

	runBackfill(t, wipnoteDir, false)
	first, _ := sessionledger.NewStore(wipnoteDir).ReadAll()
	runBackfill(t, wipnoteDir, false)
	second, _ := sessionledger.NewStore(wipnoteDir).ReadAll()

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("row count changed across repeated backfills: %d then %d", len(first), len(second))
	}
	if first[0] != second[0] {
		t.Errorf("a repeated backfill changed the row:\n  first:  %+v\n  second: %+v", first[0], second[0])
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)

	out := runBackfill(t, wipnoteDir, true)
	if _, err := os.Stat(filepath.Join(wipnoteDir, sessionledger.FileName)); !os.IsNotExist(err) {
		t.Errorf("--dry-run created the ledger file (stat err = %v)", err)
	}
	if !bytes.Contains([]byte(out), []byte(backfillHTMLSession)) {
		t.Errorf("--dry-run did not report the recoverable session:\n%s", out)
	}
}

// TestBackfillDoesNotOverwriteALiveRow protects the sessions that are already
// recorded. Backfill runs against a ledger that is simultaneously being written
// by live sessions, so it must never correct a row the live path owns.
func TestBackfillDoesNotOverwriteALiveRow(t *testing.T) {
	projectDir := backfillTestProject(t)
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	store := sessionledger.NewStore(wipnoteDir)

	liveStart := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	if _, err := store.Open(sessionledger.Record{
		SessionID: backfillHTMLSession,
		Harness:   "codex",
		StartedAt: liveStart,
	}); err != nil {
		t.Fatalf("seed live row: %v", err)
	}
	// The HTML says something different about start and harness.
	writeBackfillSessionHTML(t, wipnoteDir, backfillHTMLSession,
		"2026-08-01T09:00:00Z", "2026-08-01T11:00:00Z", false)

	runBackfill(t, wipnoteDir, false)

	rec, ok, _ := store.Get(backfillHTMLSession)
	if !ok {
		t.Fatal("row disappeared")
	}
	if !rec.StartedAt.Equal(liveStart) {
		t.Errorf("backfill moved the start to %v; the live row's start %v is authoritative",
			rec.StartedAt, liveStart)
	}
	if rec.Harness != "codex" {
		t.Errorf("backfill overwrote the harness with %q; enrichment fills gaps, it does not correct",
			rec.Harness)
	}
	// The end was a genuine gap, so it should have been filled.
	if rec.IsOpen() {
		t.Error("backfill did not fill the missing end time, which was a real gap")
	}
}
