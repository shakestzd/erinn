package claimledger

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ledgerHeader and ledgerFooter bracket the row lines. They are byte-stable
// constants because the append path finds the write offset by matching the
// footer against the file's trailing bytes — any drift here must be a
// deliberate format change, and would be caught by the round-trip tests.
const ledgerHeader = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Claim Episodes</title></head><body>
<main>
<table data-claim-ledger="true">
<thead><tr><th>Episode</th><th>Work Item</th><th>Session</th><th>Agent</th><th>Start</th><th>End</th><th>Outcome</th></tr></thead>
<tbody>
`

const ledgerFooter = `</tbody>
</table>
</main>
</body></html>
`

// rowSelector is the parse contract: only <tr> elements inside the claim-ledger
// table that carry an episode id are records. A torn tail that goquery renders
// as some other element is therefore ignored rather than mis-parsed.
const rowSelector = `table[data-claim-ledger] tbody tr[data-episode-id]`

// tornScanWindow bounds how far back the repair scan looks for a clean line
// boundary. A torn write can only have lost part of ONE row plus the footer, so
// a window far larger than either is enough; a file with no newline inside the
// window is corrupt in a way this package will not guess at.
const tornScanWindow = 64 * 1024

// marshalRow renders one episode as exactly ONE line terminated by '\n'.
//
// The single-line invariant is what makes the torn-write repair sound: the
// repair truncates back to the last newline, which is always a row boundary. It
// is enforced by sanitizing every field, not merely assumed — an ID or agent
// name carrying a newline would otherwise let one corrupt write swallow the
// rows after it.
func marshalRow(e Episode) []byte {
	var buf bytes.Buffer
	buf.WriteString("<tr")
	writeAttr(&buf, "id", e.ID)
	writeAttr(&buf, "data-episode-id", e.ID)
	writeAttr(&buf, "data-work-item", e.WorkItemID)
	writeAttr(&buf, "data-session", e.SessionID)
	writeAttr(&buf, "data-root-session", e.RootSessionID)
	writeAttr(&buf, "data-agent", e.AgentID)
	writeAttr(&buf, "data-start", FormatTime(e.StartedAt))
	endStr := ""
	if !e.IsOpen() {
		endStr = FormatTime(e.EndedAt)
	}
	writeAttr(&buf, "data-end", endStr)
	writeAttr(&buf, "data-outcome", string(e.Outcome))
	buf.WriteString(">")

	writeCell(&buf, "episode-id", e.ID)
	writeCell(&buf, "work-item", e.WorkItemID)
	writeCell(&buf, "session", e.SessionID)
	writeCell(&buf, "agent", e.AgentID)
	writeCell(&buf, "start", FormatTime(e.StartedAt))
	writeCell(&buf, "end", endStr)
	writeCell(&buf, "outcome", string(e.Outcome))

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

// ReadFile parses one claim-ledger file into episodes, ordered by start time.
// A missing file reports os.IsNotExist so callers can treat it as empty.
//
// Rows are read leniently: a row missing its required fields is skipped rather
// than failing the whole file, so one bad row can never hide a session's other
// episodes. This matches the archive ledger's behaviour.
func ReadFile(path string) ([]Episode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("claimledger: parse %s: %w", path, err)
	}

	var episodes []Episode
	doc.Find(rowSelector).Each(func(_ int, row *goquery.Selection) {
		e, ok := parseRow(row)
		if ok {
			episodes = append(episodes, e)
		}
	})
	sortEpisodes(episodes)
	return episodes, nil
}

func parseRow(row *goquery.Selection) (Episode, bool) {
	e := Episode{
		ID:            strings.TrimSpace(attr(row, "data-episode-id")),
		WorkItemID:    strings.TrimSpace(attr(row, "data-work-item")),
		SessionID:     strings.TrimSpace(attr(row, "data-session")),
		RootSessionID: strings.TrimSpace(attr(row, "data-root-session")),
		AgentID:       strings.TrimSpace(attr(row, "data-agent")),
		Outcome:       Outcome(strings.TrimSpace(attr(row, "data-outcome"))),
	}
	started, err := ParseTime(attr(row, "data-start"))
	if err != nil {
		return Episode{}, false
	}
	e.StartedAt = started
	ended, err := ParseTime(attr(row, "data-end"))
	if err != nil {
		return Episode{}, false
	}
	e.EndedAt = ended
	if e.Validate() != nil {
		return Episode{}, false
	}
	return e, true
}

func attr(sel *goquery.Selection, name string) string {
	if v, ok := sel.Attr(name); ok {
		return v
	}
	return ""
}

// sortEpisodes orders by start, then by ID so the order is total and stable
// across rewrites (two episodes can share a start timestamp).
func sortEpisodes(eps []Episode) {
	sort.Slice(eps, func(i, j int) bool {
		if !eps[i].StartedAt.Equal(eps[j].StartedAt) {
			return eps[i].StartedAt.Before(eps[j].StartedAt)
		}
		return eps[i].ID < eps[j].ID
	})
}

// appendRowLocked appends one episode row at the tail of path in CONSTANT time:
// it overwrites the trailing footer with row+footer instead of parsing and
// reserializing every existing row the way the arch-card writer does.
//
// The caller must already hold the write guard for path.
//
// TORN-WRITE GUARD (the reason this is not a naive seek-to-end append): a crash
// mid-append leaves a file whose tail is neither a complete row nor the footer.
// Appending after that tail would merge the new row INTO the corrupt fragment —
// goquery would parse `<tr data-work-i` + `<tr …>` as one garbage element and
// SWALLOW the new row, exactly the failure internal/commitqueue's appendLineLocked
// guards against for NDJSON. So before writing we locate a known-good boundary:
// the footer if the file ends with it, otherwise the last newline, which is by
// construction the end of the last complete row.
func appendRowLocked(path string, e Episode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("claimledger: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("claimledger: open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("claimledger: stat %s: %w", path, err)
	}

	off, err := appendOffset(f, fi.Size())
	if err != nil {
		return fmt.Errorf("claimledger: %s: %w", path, err)
	}

	var payload []byte
	if off == 0 {
		// Empty, or truncated to less than a full header: no complete row can
		// exist, so rewrite the whole document rather than guess at a boundary.
		payload = append(payload, ledgerHeader...)
	}
	payload = append(payload, marshalRow(e)...)
	payload = append(payload, ledgerFooter...)

	// Truncate FIRST so a crash between the two leaves a file that ends on a
	// clean row boundary with no footer — which the next append repairs — rather
	// than one with stale bytes past the new content.
	if err := f.Truncate(off); err != nil {
		return fmt.Errorf("claimledger: truncate %s: %w", path, err)
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("claimledger: seek %s: %w", path, err)
	}
	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("claimledger: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("claimledger: fsync %s: %w", path, err)
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

// writeAllLocked rewrites path with exactly the given episodes, atomically via
// temp-then-rename. This is the CLOSE path: recording an end on an existing row
// is inherently a read-modify-write, so it pays the full serialize cost. Opens
// — the frequent operation — never come through here.
//
// The caller must already hold the write guard for path.
func writeAllLocked(path string, episodes []Episode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("claimledger: mkdir %s: %w", dir, err)
	}

	sorted := make([]Episode, len(episodes))
	copy(sorted, episodes)
	sortEpisodes(sorted)

	var buf bytes.Buffer
	buf.WriteString(ledgerHeader)
	for _, e := range sorted {
		buf.Write(marshalRow(e))
	}
	buf.WriteString(ledgerFooter)

	tmp, err := os.CreateTemp(dir, ".claims-*.tmp")
	if err != nil {
		return fmt.Errorf("claimledger: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("claimledger: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("claimledger: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claimledger: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("claimledger: rename temp over %s: %w", path, err)
	}
	return nil
}
