package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

// backfillScanBytes and backfillMaxLine bound the scan of an archived
// events.ndjson. The whole stream is read because the LAST event is what gives
// a true end time, and these caps keep a corrupt or adversarial archive from
// pulling an unbounded stream into memory; a scan that hits either limit keeps
// the timestamps it has already seen rather than failing.
const (
	backfillScanBytes = 256 << 20 // 256 MiB of decompressed events
	backfillMaxLine   = 4 << 20   // 4 MiB per event
)

// sessionLedgerCmd returns `wipnote session ledger`.
func sessionLedgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Inspect and repair the canonical sessions ledger",
	}
	cmd.AddCommand(sessionLedgerBackfillCmd())
	cmd.AddCommand(sessionLedgerRepairCmd())
	cmd.AddCommand(sessionLedgerListCmd())
	return cmd
}

// sessionLedgerBackfillCmd returns `wipnote session ledger backfill`.
func sessionLedgerBackfillCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Reconstruct ledger rows for sessions that predate the ledger",
		Long: `Recover canonical rows for sessions that ran before the sessions ledger
existed, from the durable evidence they left behind.

Two sources, merged (neither overwrites what the other already recorded):

  .wipnote/sessions/<id>.html
      The canonical session record. Carries harness, project, start, end and
      event count — everything a row needs. Subagent records are skipped: the
      ledger holds root sessions only.

  .wipnote/archive/<yyyy-mm>/<id>.tar.gz
      The archived raw events. The filename is the session id and the file
      mtime is the last observed activity; the first line of the compressed
      events.ndjson supplies the real start time when it parses.

Sessions with neither remain unrecoverable, and edges naming them keep their
tombstone — which is the honest outcome, not a gap: nothing durable records
that they ever ran.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			return runSessionLedgerBackfill(cmd, wipnoteDir, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be recorded without writing")
	return cmd
}

// sessionLedgerListCmd returns `wipnote session ledger list`.
func sessionLedgerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the sessions recorded in the canonical ledger",
		RunE: func(cmd *cobra.Command, _ []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			records, err := sessionledger.NewStore(wipnoteDir).ReadAll()
			if err != nil {
				return err
			}
			if len(records) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "sessions ledger is empty")
				return nil
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-38s %-14s %-20s %s\n", "SESSION", "HARNESS", "START", "END")
			for _, r := range records {
				end := "(open)"
				if !r.IsOpen() {
					end = r.EndedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(out, "%-38s %-14s %-20s %s\n",
					r.SessionID, orDash(r.Harness), r.StartedAt.UTC().Format(time.RFC3339), end)
			}
			fmt.Fprintf(out, "\n%d session(s)\n", len(records))
			return nil
		},
	}
}

// runSessionLedgerBackfill reconstructs rows from every durable source and
// reports what each contributed.
func runSessionLedgerBackfill(cmd *cobra.Command, wipnoteDir string, dryRun bool) error {
	candidates := collectBackfillCandidates(wipnoteDir)
	if len(candidates) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "wipnote session ledger backfill: no recoverable sessions found.")
		return nil
	}

	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintf(out, "%-38s %-14s %-20s %s\n", "SESSION", "HARNESS", "START", "SOURCE")
		for _, id := range ids {
			c := candidates[id]
			fmt.Fprintf(out, "%-38s %-14s %-20s %s\n",
				id, orDash(c.enrich.Harness), backfillTimeLabel(c.enrich), c.source)
		}
		fmt.Fprintf(out, "\n%d session(s) recoverable (dry-run: nothing written)\n", len(ids))
		return nil
	}

	store := sessionledger.NewStore(wipnoteDir)
	changed, skipped := 0, 0
	for _, id := range ids {
		wrote, err := store.Enrich(id, candidates[id].enrich)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: %s: %v\n", id, err)
			skipped++
			continue
		}
		if wrote {
			changed++
		}
	}
	fmt.Fprintf(out, "Backfilled %d session(s) into the canonical ledger (%d already recorded)\n",
		changed, len(ids)-changed-skipped)
	if skipped > 0 {
		fmt.Fprintf(out, "%d session(s) skipped — see warnings above\n", skipped)
	}
	fmt.Fprintln(out, "Run 'wipnote reindex --full' to resolve edges against the new rows.")
	return nil
}

func backfillTimeLabel(e sessionledger.Enrichment) string {
	t := e.StartedAt
	if t.IsZero() {
		t = e.EndedAt
	}
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// backfillCandidate is one recoverable session plus the evidence that found it.
type backfillCandidate struct {
	enrich sessionledger.Enrichment
	source string
}

// collectBackfillCandidates gathers every session with durable evidence that it
// ran, merging the richer source into the poorer one per id.
//
// Session HTML is read FIRST because it is the fuller record; the archive pass
// then adds the tarball path (and an end time) for sessions whose HTML is gone.
// A session can legitimately appear in both — archiving removes the raw events
// directory but leaves the HTML — so the merge is a fill-the-gaps union rather
// than a precedence rule.
func collectBackfillCandidates(wipnoteDir string) map[string]backfillCandidate {
	candidates := map[string]backfillCandidate{}
	collectBackfillFromSessionHTML(wipnoteDir, candidates)
	collectBackfillFromArchives(wipnoteDir, candidates)
	return candidates
}

// collectBackfillFromSessionHTML reads .wipnote/sessions/*.html.
func collectBackfillFromSessionHTML(wipnoteDir string, into map[string]backfillCandidate) {
	files, _ := filepath.Glob(filepath.Join(wipnoteDir, "sessions", "*.html"))
	for _, path := range files {
		rec, ok := parseSessionHTMLForLedger(path)
		if !ok {
			continue
		}
		mergeBackfillCandidate(into, rec.SessionID, sessionledger.Enrichment{
			Harness:    rec.Harness,
			ProjectDir: rec.ProjectDir,
			StartedAt:  rec.StartedAt,
			EndedAt:    rec.EndedAt,
			EndSource:  sessionledger.EndSourceSessionRecord,
			Events:     rec.Events,
		}, "session html")
	}
}

// parseSessionHTMLForLedger extracts a ledger record from a canonical session
// HTML file. Returns false for anything that must not become a ledger row:
// subagent records (the ledger is root sessions only), ids that are not
// session-shaped, and files with no usable timestamp.
func parseSessionHTMLForLedger(path string) (sessionledger.Record, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionledger.Record{}, false
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return sessionledger.Record{}, false
	}
	article := doc.Find("article[id]").First()
	if article.Length() == 0 {
		return sessionledger.Record{}, false
	}

	id, _ := article.Attr("id")
	id = strings.TrimSpace(id)
	if !graph.IsSessionShapedID(id) {
		return sessionledger.Record{}, false
	}
	if attrOrDefault(article, "data-is-subagent", "false") == "true" {
		return sessionledger.Record{}, false
	}

	rec := sessionledger.Record{
		SessionID:  id,
		Harness:    strings.TrimSpace(attrOrDefault(article, "data-agent", "")),
		ProjectDir: strings.TrimSpace(attrOrDefault(article, "data-project-dir", "")),
	}
	if t, err := sessionledger.ParseTime(attrOrDefault(article, "data-started-at", "")); err == nil {
		rec.StartedAt = t
	}
	if t, err := sessionledger.ParseTime(attrOrDefault(article, "data-ended-at", "")); err == nil {
		rec.EndedAt = t
	}
	if n, err := strconv.Atoi(strings.TrimSpace(attrOrDefault(article, "data-event-count", ""))); err == nil && n > 0 {
		rec.Events = n
	}
	if rec.StartedAt.IsZero() && rec.EndedAt.IsZero() {
		return sessionledger.Record{}, false
	}
	return rec, true
}

// collectBackfillFromArchives walks .wipnote/archive/<yyyy-mm>/*.tar.gz.
//
// The tarball is proof the session ran and its filename IS the session id. The
// interval comes from the events themselves — first and last timestamp in the
// compressed events.ndjson.
//
// That range is the ACTIVITY RECORDED UNDER THE SESSION ID, which is the best
// fact available here but is not always an interactive session's duration. The
// ndjson is the collector's log for that session directory, and at least one
// archive in this repo (3295369c…) holds 159 events that are ALL session_start
// correlation records for OTHER sessions, giving a 3-day range for what was
// never a 3-day session. Readers should treat it as bounds on observed
// activity, not as a duration to sum.
//
// The tarball's own mtime is NOT an end at all and is only a last resort. It
// records when retention CREATED the archive, which can be months after the
// activity stopped: measured on this repo, a May session archived in July reads
// as a 47-day session if you trust the mtime, versus 2 days from its events. It
// is kept solely so a session whose events are unreadable still gets a row with
// some end rather than none.
func collectBackfillFromArchives(wipnoteDir string, into map[string]backfillCandidate) {
	archiveRoot := filepath.Join(wipnoteDir, "archive")
	months, err := os.ReadDir(archiveRoot)
	if err != nil {
		return
	}
	repoRoot := filepath.Dir(wipnoteDir)
	for _, m := range months {
		if !m.IsDir() {
			continue
		}
		tarballs, _ := filepath.Glob(filepath.Join(archiveRoot, m.Name(), "*.tar.gz"))
		for _, path := range tarballs {
			id := strings.TrimSuffix(filepath.Base(path), ".tar.gz")
			if !graph.IsSessionShapedID(id) {
				continue
			}
			e := sessionledger.Enrichment{}
			if rel, relErr := filepath.Rel(repoRoot, path); relErr == nil {
				e.ArchivePath = filepath.ToSlash(rel)
			}
			started, ended := archivedSessionSpan(path)
			e.StartedAt = started
			e.EndedAt = ended
			e.EndSource = sessionledger.EndSourceLastActivity
			if e.EndedAt.IsZero() {
				if info, statErr := os.Stat(path); statErr == nil {
					e.EndedAt = info.ModTime().UTC()
					e.EndSource = sessionledger.EndSourceArchiveMtime
				}
			}
			if e.StartedAt.IsZero() && e.EndedAt.IsZero() {
				continue
			}
			mergeBackfillCandidate(into, id, e, "archive tarball")
		}
	}
}

// archivedSessionSpan returns the first and last event timestamps in an
// archived session's events.ndjson — the session's real interval.
//
// Either return may be zero when the archive is unreadable or holds no parsable
// timestamp; the caller treats a zero end as "fall back to the file mtime" and
// a zero start as "no start evidence" rather than inventing one.
func archivedSessionSpan(tarballPath string) (first, last time.Time) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, tarErr := tr.Next()
		if tarErr != nil {
			return first, last
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".ndjson") {
			continue
		}

		scanner := bufio.NewScanner(io.LimitReader(tr, backfillScanBytes))
		scanner.Buffer(make([]byte, 0, 64*1024), backfillMaxLine)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var evt struct {
				TS string `json:"ts"`
			}
			if json.Unmarshal([]byte(line), &evt) != nil {
				continue
			}
			t, parseErr := sessionledger.ParseTime(evt.TS)
			if parseErr != nil || t.IsZero() {
				continue
			}
			if first.IsZero() || t.Before(first) {
				first = t
			}
			if t.After(last) {
				last = t
			}
		}
		// A scan error (over-long line, truncated stream, or the byte cap) keeps
		// whatever was already read: a partial interval beats no row at all.
		return first, last
	}
}

// mergeBackfillCandidate folds new evidence into whatever is already known
// about id, never replacing a field that is already populated.
func mergeBackfillCandidate(into map[string]backfillCandidate, id string, e sessionledger.Enrichment, source string) {
	existing, found := into[id]
	if !found {
		into[id] = backfillCandidate{enrich: e, source: source}
		return
	}
	merged := existing.enrich
	if merged.Harness == "" {
		merged.Harness = e.Harness
	}
	if merged.ProjectDir == "" {
		merged.ProjectDir = e.ProjectDir
	}
	if merged.StartedAt.IsZero() {
		merged.StartedAt = e.StartedAt
	}
	if merged.EndedAt.IsZero() {
		merged.EndedAt = e.EndedAt
		merged.EndSource = e.EndSource
	} else if e.EndSource.OutranksRecorded(merged.EndSource) && !e.EndedAt.IsZero() {
		// Both sources have an end: take the better-grounded one. Session HTML
		// beats an archive-derived span, which beats a tarball mtime.
		merged.EndedAt = e.EndedAt
		merged.EndSource = e.EndSource
	}
	if merged.ArchivePath == "" {
		merged.ArchivePath = e.ArchivePath
	}
	if merged.Events == 0 {
		merged.Events = e.Events
	}
	into[id] = backfillCandidate{enrich: merged, source: existing.source + "+" + source}
}
