package gateledger

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
// footer against the file's trailing bytes — any drift here must be a deliberate
// format change, and would be caught by the round-trip tests.
const ledgerHeader = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Gate Runs</title></head><body>
<main>
<table data-gate-ledger="true">
<thead><tr><th>Record</th><th>Work Item</th><th>Session</th><th>Harness</th><th>Project Type</th><th>Gate Command</th><th>Status</th><th>Checked</th><th>Source</th><th>Guards</th><th>Profile</th><th>Allowlist Hits</th><th>Summary</th></tr></thead>
<tbody>
`

const ledgerFooter = `</tbody>
</table>
</main>
</body></html>
`

// rowSelector is the parse contract: only <tr> elements inside the gate-ledger
// table that carry a record id are records. A torn tail that goquery renders as
// some other element is therefore ignored rather than mis-parsed.
const rowSelector = `table[data-gate-ledger] tbody tr[data-record-id]`

// tornScanWindow bounds how far back the repair scan looks for a clean line
// boundary. A torn write can only have lost part of ONE row plus the footer, so
// a window far larger than either is enough; a file with no newline inside the
// window is corrupt in a way this package will not guess at.
const tornScanWindow = 64 * 1024

// marshalRow renders one gate run as exactly ONE line terminated by '\n'.
//
// The single-line invariant is what makes the torn-write repair sound: the
// repair truncates back to the last newline, which is always a row boundary. It
// is enforced by sanitizing every field, not merely assumed — and this ledger
// carries the fields most likely to test it. GateCommand is a joined shell
// command line, OutputSummary is gate stdout, and AllowlistHitsJSON embeds
// operator-written justification prose; any of the three can arrive with
// embedded newlines, and one such write would otherwise swallow every row after
// it.
func marshalRow(r Record) []byte {
	var buf bytes.Buffer
	buf.WriteString("<tr")
	writeAttr(&buf, "id", r.ID)
	writeAttr(&buf, "data-record-id", r.ID)
	writeAttr(&buf, "data-work-item", r.WorkItemID)
	writeAttr(&buf, "data-session", r.SessionID)
	writeAttr(&buf, "data-harness", r.Harness)
	writeAttr(&buf, "data-project-type", r.ProjectType)
	writeAttr(&buf, "data-gate-command", r.GateCommand)
	writeAttr(&buf, "data-status", r.Status)
	writeAttr(&buf, "data-checked-at", FormatTime(r.CheckedAt))
	writeAttr(&buf, "data-signature", r.Signature)
	writeAttr(&buf, "data-source", r.Source)
	writeAttr(&buf, "data-guards-run", r.GuardsRunJSON)
	writeAttr(&buf, "data-profile-signature", r.ProfileSignature)
	writeAttr(&buf, "data-allowlist-hits", r.AllowlistHitsJSON)
	writeAttr(&buf, "data-allowlist-hit-count", strconv.Itoa(r.AllowlistHitCount))
	writeAttr(&buf, "data-output-summary", r.OutputSummary)
	buf.WriteString(">")

	writeCell(&buf, "record-id", r.ID)
	writeCell(&buf, "work-item", r.WorkItemID)
	writeCell(&buf, "session", r.SessionID)
	writeCell(&buf, "harness", r.Harness)
	writeCell(&buf, "project-type", r.ProjectType)
	writeCell(&buf, "gate-command", r.GateCommand)
	writeCell(&buf, "status", r.Status)
	writeCell(&buf, "checked-at", FormatTime(r.CheckedAt))
	writeCell(&buf, "source", r.Source)
	writeCell(&buf, "guards-run", r.GuardsRunJSON)
	writeCell(&buf, "profile-signature", r.ProfileSignature)
	writeCell(&buf, "allowlist-hits", r.AllowlistHitsJSON)
	writeCell(&buf, "output-summary", r.OutputSummary)

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

// ReadFile parses the ledger into records, ordered by checked-at. A missing file
// reports os.IsNotExist so callers can treat it as empty.
//
// Rows are read leniently: a row that fails validation is skipped rather than
// failing the whole file, so one bad row can never hide every other gate run.
// This matches the claim and sessions ledgers.
func ReadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("gateledger: parse %s: %w", path, err)
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
		ID:                strings.TrimSpace(attr(row, "data-record-id")),
		WorkItemID:        strings.TrimSpace(attr(row, "data-work-item")),
		SessionID:         strings.TrimSpace(attr(row, "data-session")),
		Harness:           strings.TrimSpace(attr(row, "data-harness")),
		ProjectType:       strings.TrimSpace(attr(row, "data-project-type")),
		GateCommand:       attr(row, "data-gate-command"),
		Status:            strings.TrimSpace(attr(row, "data-status")),
		Signature:         strings.TrimSpace(attr(row, "data-signature")),
		Source:            strings.TrimSpace(attr(row, "data-source")),
		GuardsRunJSON:     strings.TrimSpace(attr(row, "data-guards-run")),
		ProfileSignature:  strings.TrimSpace(attr(row, "data-profile-signature")),
		AllowlistHitsJSON: strings.TrimSpace(attr(row, "data-allowlist-hits")),
		OutputSummary:     attr(row, "data-output-summary"),
	}
	checked, err := ParseTime(attr(row, "data-checked-at"))
	if err != nil {
		return Record{}, false
	}
	r.CheckedAt = checked
	if n, convErr := strconv.Atoi(strings.TrimSpace(attr(row, "data-allowlist-hit-count"))); convErr == nil && n > 0 {
		r.AllowlistHitCount = n
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

// sortRecords orders by checked-at, then by id so the order is total and stable
// (two gate runs in different worktrees can share a timestamp). Callers that
// want "the latest" read from the tail.
func sortRecords(recs []Record) {
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].CheckedAt.Equal(recs[j].CheckedAt) {
			return recs[i].CheckedAt.Before(recs[j].CheckedAt)
		}
		return recs[i].ID < recs[j].ID
	})
}

// appendRowLocked appends one row at the tail of path in CONSTANT time: it
// overwrites the trailing footer with row+footer instead of parsing and
// reserializing every existing row.
//
// This is the ONLY writer in the package. A gate run is immutable once recorded,
// so unlike core/claimledger and core/sessionledger there is no update path and
// no atomic temp-then-rename rewriter to pair with it.
//
// The caller must already hold the write guard for path.
//
// TORN-WRITE GUARD (the reason this is not a naive seek-to-end append): a crash
// mid-append leaves a file whose tail is neither a complete row nor the footer.
// Appending after that tail would merge the new row INTO the corrupt fragment —
// goquery parses `<tr data-recor` + `<tr …>` as one garbage element and SWALLOWS
// the new row. So before writing we locate a known-good boundary: the footer if
// the file ends with it, otherwise the last newline, which is by construction the
// end of the last complete row.
func appendRowLocked(path string, r Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gateledger: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("gateledger: open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("gateledger: stat %s: %w", path, err)
	}

	off, err := appendOffset(f, fi.Size())
	if err != nil {
		return fmt.Errorf("gateledger: %s: %w", path, err)
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
		return fmt.Errorf("gateledger: truncate %s: %w", path, err)
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("gateledger: seek %s: %w", path, err)
	}
	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("gateledger: write %s: %w", path, err)
	}
	// fsync before returning is what makes the write-then-read case sound: a gate
	// run recorded by `wipnote check --gate` must be readable by the completion
	// that follows in a DIFFERENT process, long before the deferred commit queue
	// flushes. Only the git commit is deferred; the bytes are not.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("gateledger: fsync %s: %w", path, err)
	}
	return nil
}

// appendOffset returns the byte offset at which a new row line may begin.
//
//   - size 0, or smaller than the header: 0 (rewrite the document).
//   - file ends with the exact footer: the footer's start (the clean case).
//   - otherwise the write was torn: the byte after the last newline, dropping the
//     incomplete tail. Every earlier row is preserved; only the fragment that was
//     never fsynced is lost.
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
