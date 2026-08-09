package sessionledger

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ledgerHeader and ledgerFooter bracket the row lines. They are byte-stable
// constants because the append path finds the write offset by matching the
// footer against the file's trailing bytes — any drift here must be a
// deliberate format change, and would be caught by the round-trip tests.
const ledgerHeader = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Sessions</title></head><body>
<main>
<table data-session-ledger="true">
<thead><tr><th>Session</th><th>Harness</th><th>Project</th><th>Start</th><th>End</th><th>End source</th><th>Archive</th><th>Events</th></tr></thead>
<tbody>
`

const ledgerFooter = `</tbody>
</table>
</main>
</body></html>
`

// rowSelector is the parse contract: only <tr> elements inside the
// session-ledger table that carry a session id are records. A torn tail that
// goquery renders as some other element is therefore ignored rather than
// mis-parsed.
const rowSelector = `table[data-session-ledger] tbody tr[data-session-id]`

// tornScanWindow bounds how far back the repair scan looks for a clean line
// boundary. A torn write can only have lost part of ONE row plus the footer, so
// a window far larger than either is enough; a file with no newline inside the
// window is corrupt in a way this package will not guess at.
const tornScanWindow = 64 * 1024

// marshalRow renders one session as exactly ONE line terminated by '\n'.
//
// The single-line invariant is what makes the torn-write repair sound: the
// repair truncates back to the last newline, which is always a row boundary. It
// is enforced by sanitizing every field, not merely assumed — a harness name or
// archive path carrying a newline would otherwise let one corrupt write swallow
// the rows after it.
func marshalRow(r Record) []byte {
	var buf bytes.Buffer
	buf.WriteString("<tr")
	writeAttr(&buf, "id", r.SessionID)
	writeAttr(&buf, "data-session-id", r.SessionID)
	writeAttr(&buf, "data-harness", r.Harness)
	writeAttr(&buf, "data-project", r.ProjectDir)
	writeAttr(&buf, "data-start", FormatTime(r.StartedAt))
	endStr := ""
	endSrc := ""
	if !r.IsOpen() {
		endStr = FormatTime(r.EndedAt)
		endSrc = string(r.EndSource)
	}
	writeAttr(&buf, "data-end", endStr)
	writeAttr(&buf, "data-end-source", endSrc)
	writeAttr(&buf, "data-archive", r.ArchivePath)
	eventsStr := ""
	if r.Events > 0 {
		eventsStr = strconv.Itoa(r.Events)
	}
	writeAttr(&buf, "data-events", eventsStr)
	buf.WriteString(">")

	writeCell(&buf, "session-id", r.SessionID)
	writeCell(&buf, "harness", r.Harness)
	writeCell(&buf, "project", r.ProjectDir)
	writeCell(&buf, "start", FormatTime(r.StartedAt))
	writeCell(&buf, "end", endStr)
	writeCell(&buf, "end-source", endSrc)
	writeCell(&buf, "archive", r.ArchivePath)
	writeCell(&buf, "events", eventsStr)

	buf.WriteString("</tr>\n")
	return buf.Bytes()
}

func writeAttr(buf *bytes.Buffer, name, value string) {
	buf.WriteByte(' ')
	buf.WriteString(name)
	buf.WriteString(`="`)
	buf.WriteString(html.EscapeString(sanitize(value)))
	buf.WriteByte('"')
}

func writeCell(buf *bytes.Buffer, field, value string) {
	buf.WriteString(`<td data-field="`)
	buf.WriteString(field)
	buf.WriteString(`">`)
	buf.WriteString(html.EscapeString(sanitize(value)))
	buf.WriteString("</td>")
}

// sanitize collapses anything that would break the one-row-one-line invariant.
func sanitize(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, v)
}

// ReadFile parses the ledger into records, ordered by start time. A missing
// file reports os.IsNotExist so callers can treat it as empty.
//
// Rows are read leniently: a row that fails validation is skipped rather than
// failing the whole file, so one bad row can never hide every other session.
// This matches the claim ledger's and the archive ledger's behaviour.
func ReadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("sessionledger: parse %s: %w", path, err)
	}

	var records []Record
	doc.Find(rowSelector).Each(func(_ int, row *goquery.Selection) {
		if r, ok := parseRow(row); ok {
			records = append(records, r)
		}
	})
	sortRecords(records)
	return records, nil
}

// parseLedgerString parses ledger markup that is already in memory. It shares
// the row selector and row parser with ReadFile, so a format the tests accept
// is exactly a format the reader accepts.
func parseLedgerString(markup string) ([]Record, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(markup))
	if err != nil {
		return nil, fmt.Errorf("sessionledger: parse markup: %w", err)
	}
	var records []Record
	doc.Find(rowSelector).Each(func(_ int, row *goquery.Selection) {
		if r, ok := parseRow(row); ok {
			records = append(records, r)
		}
	})
	sortRecords(records)
	return records, nil
}

func parseRow(row *goquery.Selection) (Record, bool) {
	r := Record{
		SessionID:   strings.TrimSpace(attr(row, "data-session-id")),
		Harness:     strings.TrimSpace(attr(row, "data-harness")),
		ProjectDir:  strings.TrimSpace(attr(row, "data-project")),
		ArchivePath: strings.TrimSpace(attr(row, "data-archive")),
	}
	started, err := ParseTime(attr(row, "data-start"))
	if err != nil {
		return Record{}, false
	}
	r.StartedAt = started
	ended, err := ParseTime(attr(row, "data-end"))
	if err != nil {
		return Record{}, false
	}
	r.EndedAt = ended
	// Rows written before provenance existed have no data-end-source and read as
	// EndSourceUnknown, which ranks lowest — so repair re-derives exactly those.
	if !r.IsOpen() {
		r.EndSource = EndSource(strings.TrimSpace(attr(row, "data-end-source")))
	}
	if n, convErr := strconv.Atoi(strings.TrimSpace(attr(row, "data-events"))); convErr == nil && n > 0 {
		r.Events = n
	}
	if r.Validate() != nil {
		return Record{}, false
	}
	return r, true
}

func attr(sel *goquery.Selection, name string) string {
	if v, ok := sel.Attr(name); ok {
		return v
	}
	return ""
}

// sortRecords orders by start, then by id so the order is total and stable
// across rewrites (two sessions can share a start timestamp).
func sortRecords(recs []Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].StartedAt.Equal(recs[j].StartedAt) {
			return recs[i].StartedAt.Before(recs[j].StartedAt)
		}
		return recs[i].SessionID < recs[j].SessionID
	})
}

// appendRowLocked appends one row at the tail of path in CONSTANT time: it
// overwrites the trailing footer with row+footer instead of parsing and
// reserializing every existing row.
//
// The caller must already hold the write guard for path.
//
// TORN-WRITE GUARD (the reason this is not a naive seek-to-end append): a crash
// mid-append leaves a file whose tail is neither a complete row nor the footer.
// Appending after that tail would merge the new row INTO the corrupt fragment —
// goquery parses `<tr data-sessi` + `<tr …>` as one garbage element and
// SWALLOWS the new row. So before writing we locate a known-good boundary: the
// footer if the file ends with it, otherwise the last newline, which is by
// construction the end of the last complete row.
func appendRowLocked(path string, r Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sessionledger: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("sessionledger: open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("sessionledger: stat %s: %w", path, err)
	}

	off, err := appendOffset(f, fi.Size())
	if err != nil {
		return fmt.Errorf("sessionledger: %s: %w", path, err)
	}

	var payload []byte
	if off == 0 {
		// Empty, or truncated to less than a full header: no complete row can
		// exist, so rewrite the whole document rather than guess at a boundary.
		payload = append(payload, ledgerHeader...)
	}
	payload = append(payload, marshalRow(r)...)
	payload = append(payload, ledgerFooter...)

	// Truncate FIRST so a crash between the two leaves a file that ends on a
	// clean row boundary with no footer — which the next append repairs — rather
	// than one with stale bytes past the new content.
	if err := f.Truncate(off); err != nil {
		return fmt.Errorf("sessionledger: truncate %s: %w", path, err)
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("sessionledger: seek %s: %w", path, err)
	}
	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("sessionledger: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sessionledger: fsync %s: %w", path, err)
	}
	return nil
}

// appendOffset returns the byte offset at which a new row line may begin.
//
//   - size 0, or smaller than the header: 0 (rewrite the document).
//   - file ends with the exact footer: the footer's start (the clean case).
//   - otherwise the write was torn: the byte after the last newline, dropping
//     the incomplete tail. Every earlier row is preserved; only the fragment
//     that was never fsynced is lost.
func appendOffset(f *os.File, size int64) (int64, error) {
	if size < int64(len(ledgerHeader)) {
		return 0, nil
	}

	footerLen := int64(len(ledgerFooter))
	tail := make([]byte, footerLen)
	if _, err := f.ReadAt(tail, size-footerLen); err != nil {
		return 0, fmt.Errorf("read trailing bytes: %w", err)
	}
	if bytes.Equal(tail, []byte(ledgerFooter)) {
		return size - footerLen, nil
	}

	window := int64(tornScanWindow)
	if window > size {
		window = size
	}
	buf := make([]byte, window)
	if _, err := f.ReadAt(buf, size-window); err != nil {
		return 0, fmt.Errorf("read repair window: %w", err)
	}
	nl := bytes.LastIndexByte(buf, '\n')
	if nl < 0 {
		return 0, fmt.Errorf("torn ledger has no line boundary within %d bytes of the tail", window)
	}
	boundary := size - window + int64(nl) + 1
	// Never rewind into the header: a boundary inside it means the file was torn
	// during creation and holds no complete row.
	if boundary < int64(len(ledgerHeader)) {
		return 0, nil
	}
	return boundary, nil
}

// writeAllLocked rewrites path with exactly the given records, atomically via
// temp-then-rename. This is the UPDATE path: recording an end or an archive
// path on an existing row is inherently a read-modify-write, so it pays the
// full serialize cost. Session starts — the frequent operation — never come
// through here.
//
// The caller must already hold the write guard for path.
func writeAllLocked(path string, records []Record) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sessionledger: mkdir %s: %w", dir, err)
	}

	sorted := make([]Record, len(records))
	copy(sorted, records)
	sortRecords(sorted)

	var buf bytes.Buffer
	buf.WriteString(ledgerHeader)
	for _, r := range sorted {
		buf.Write(marshalRow(r))
	}
	buf.WriteString(ledgerFooter)

	tmp, err := os.CreateTemp(dir, ".sessions-ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("sessionledger: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("sessionledger: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sessionledger: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sessionledger: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("sessionledger: rename temp over %s: %w", path, err)
	}
	return nil
}
