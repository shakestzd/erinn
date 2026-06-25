package graph

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// Package-level ledger format for ARCHIVED work items.
//
// Old DONE work items are compacted out of individual .wipnote/<type>s/*.html
// files into a single type-specific HTML table ledger so the per-item file
// count stays bounded as the project accumulates history. The ledger reuses the
// architecture.html precedent (core/arch/ledger.go): a <table> with one <tr>
// per row, queryable data-* attributes, goquery for parsing, html.EscapeString
// for writing, and an atomic .tmp+rename on save.
//
// CRITICAL — lossless: each row preserves the FULL original work-item HTML in a
// <td data-field="html"><pre>…</pre></td> cell. Reindex reconstructs the exact
// *models.Node via htmlparse.ParseString of that preserved HTML, so archived
// rows index, query, and participate in lineage identically to file-backed
// items. Nothing is dropped on archive.

const (
	// ArchiveDirName is the subdirectory under .wipnote/ that holds work-item
	// archive ledgers. NOTE: this is distinct from the session-tarball archive
	// (.wipnote/archive/<month>/*.tar.gz) which is an unrelated mechanism; the
	// two coexist in the same directory without collision because work-item
	// ledgers are named "<type>s.html" and session tarballs are "<id>.tar.gz".
	ArchiveDirName = "archive"
)

// LedgerEntry is one archived work item: the queryable scalar fields plus the
// full original HTML that round-trips back to an exact *models.Node.
type LedgerEntry struct {
	ID         string
	Type       string
	Title      string
	Status     string
	Priority   string
	TrackID    string
	CreatedBy  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt time.Time

	// HTML is the verbatim original work-item file content. It is the canonical
	// payload — every other field above is a denormalized convenience copy for
	// scanning without a full parse. Reconstruct the Node with Node().
	HTML string
}

// Node reconstructs the original work item by parsing the preserved HTML.
func (e *LedgerEntry) Node() (*models.Node, error) {
	return htmlparse.ParseString(e.HTML)
}

// ArchiveLedgerCollections lists the work-item collection directory names whose
// DONE items may be compacted into archive ledgers. It is the single source of
// truth for which ledgers exist; reindex and the archive command iterate it.
// Slice 1 ships features only — adding "bugs"/"spikes" here extends archiving,
// reindex, and canonical-read coverage with no other code change.
var ArchiveLedgerCollections = []string{"features"}

// ArchiveLedgerPath returns the canonical archive ledger path for a work-item
// collection directory name (e.g. "features" -> .wipnote/archive/features.html).
func ArchiveLedgerPath(wipnoteDir, collectionDir string) string {
	return filepath.Join(wipnoteDir, ArchiveDirName, collectionDir+".html")
}

// LoadArchivedNodes reconstructs every archived work item across all archive
// ledgers under wipnoteDir into *models.Node values. A missing ledger is not an
// error. This is what makes archived items visible to canonical-first readers
// via LoadAll. Each node is parsed from the lossless preserved HTML, so it is
// identical to what an individual file would have yielded.
func LoadArchivedNodes(wipnoteDir string) ([]*models.Node, error) {
	var out []*models.Node
	for _, col := range ArchiveLedgerCollections {
		nodes, err := LoadArchivedNodesForCollection(wipnoteDir, col)
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
	}
	return out, nil
}

// LoadArchivedNodesForCollection reconstructs the archived work items for a
// single collection directory (e.g. "features") from its archive ledger. A
// missing ledger, or a collection with no ledger, yields no nodes and no error.
// Unparseable rows are skipped so one corrupt row cannot hide the rest.
func LoadArchivedNodesForCollection(wipnoteDir, collectionDir string) ([]*models.Node, error) {
	entries, err := ReadLedger(ArchiveLedgerPath(wipnoteDir, collectionDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*models.Node
	for _, e := range entries {
		node, nodeErr := e.Node()
		if nodeErr != nil {
			// Skip unparseable rows (matches LoadDir's lenient behaviour);
			// a corrupt row must not hide every other archived item.
			continue
		}
		out = append(out, node)
	}
	return out, nil
}

// ReadLedger parses an archive ledger HTML file into entries. A missing file is
// reported via os.IsNotExist on the returned error so callers can treat "no
// ledger yet" as an empty set.
func ReadLedger(path string) ([]*LedgerEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("parse work-item ledger %s: %w", path, err)
	}

	var entries []*LedgerEntry
	var parseErr error
	doc.Find(`table[data-workitem-ledger] tbody tr[data-id]`).Each(func(_ int, row *goquery.Selection) {
		if parseErr != nil {
			return
		}
		entry, rowErr := parseLedgerRow(row)
		if rowErr != nil {
			parseErr = rowErr
			return
		}
		entries = append(entries, entry)
	})
	if parseErr != nil {
		return nil, parseErr
	}
	return entries, nil
}

func parseLedgerRow(row *goquery.Selection) (*LedgerEntry, error) {
	id := strings.TrimSpace(ledgerAttr(row, "data-id"))
	if id == "" {
		return nil, fmt.Errorf("ledger row missing data-id")
	}
	entry := &LedgerEntry{
		ID:        id,
		Type:      strings.TrimSpace(ledgerAttr(row, "data-type")),
		Title:     strings.TrimSpace(ledgerAttr(row, "data-title")),
		Status:    strings.TrimSpace(ledgerAttr(row, "data-status")),
		Priority:  strings.TrimSpace(ledgerAttr(row, "data-priority")),
		TrackID:   strings.TrimSpace(ledgerAttr(row, "data-track-id")),
		CreatedBy: strings.TrimSpace(ledgerAttr(row, "data-created-by")),
		HTML:      row.Find(`td[data-field="html"]`).First().Text(),
	}
	entry.CreatedAt = parseLedgerTime(ledgerAttr(row, "data-created-at"))
	entry.UpdatedAt = parseLedgerTime(ledgerAttr(row, "data-updated-at"))
	entry.ArchivedAt = parseLedgerTime(ledgerAttr(row, "data-archived-at"))
	if entry.HTML == "" {
		return nil, fmt.Errorf("ledger row %s has no preserved HTML payload", id)
	}
	return entry, nil
}

func ledgerAttr(sel *goquery.Selection, name string) string {
	if v, ok := sel.Attr(name); ok {
		return v
	}
	return ""
}

func parseLedgerTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return ts
	}
	return time.Time{}
}

// WriteLedger rewrites an archive ledger HTML file atomically. Entries are
// sorted by ID for a stable, diff-friendly canonical form.
func WriteLedger(path string, entries []*LedgerEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create archive ledger dir: %w", err)
	}

	sorted := make([]*LedgerEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	doc := marshalLedger(sorted)

	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, doc, 0o644); err != nil {
		return fmt.Errorf("write archive ledger: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename archive ledger: %w", err)
	}
	return nil
}

func marshalLedger(entries []*LedgerEntry) []byte {
	var buf bytes.Buffer
	buf.WriteString("<!DOCTYPE html>\n")
	buf.WriteString("<html><head><meta charset=\"utf-8\"><title>Archived Work Items</title></head><body>\n")
	buf.WriteString("<main>\n")
	buf.WriteString("<table data-workitem-ledger=\"true\">\n")
	buf.WriteString("<thead><tr><th>ID</th><th>Type</th><th>Status</th><th>Priority</th><th>Title</th><th>Track</th><th>Created By</th><th>Archived</th><th>HTML</th></tr></thead>\n")
	buf.WriteString("<tbody>\n")
	for _, e := range entries {
		buf.WriteString("<tr")
		writeLedgerAttr(&buf, "data-id", e.ID)
		writeLedgerAttr(&buf, "data-type", e.Type)
		writeLedgerAttr(&buf, "data-title", e.Title)
		writeLedgerAttr(&buf, "data-status", e.Status)
		writeLedgerAttr(&buf, "data-priority", e.Priority)
		writeLedgerAttr(&buf, "data-track-id", e.TrackID)
		writeLedgerAttr(&buf, "data-created-by", e.CreatedBy)
		if !e.CreatedAt.IsZero() {
			writeLedgerAttr(&buf, "data-created-at", e.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
		if !e.UpdatedAt.IsZero() {
			writeLedgerAttr(&buf, "data-updated-at", e.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		if !e.ArchivedAt.IsZero() {
			writeLedgerAttr(&buf, "data-archived-at", e.ArchivedAt.UTC().Format(time.RFC3339Nano))
		}
		buf.WriteString(">")
		writeLedgerCell(&buf, "id", e.ID)
		writeLedgerCell(&buf, "type", e.Type)
		writeLedgerCell(&buf, "status", e.Status)
		writeLedgerCell(&buf, "priority", e.Priority)
		writeLedgerCell(&buf, "title", e.Title)
		writeLedgerCell(&buf, "track", e.TrackID)
		writeLedgerCell(&buf, "created-by", e.CreatedBy)
		buf.WriteString("<td data-field=\"archived\">")
		if !e.ArchivedAt.IsZero() {
			buf.WriteString(html.EscapeString(e.ArchivedAt.UTC().Format(time.RFC3339)))
		}
		buf.WriteString("</td>")
		// The preserved HTML is the lossless canonical payload. It is escaped
		// inside a <pre> so the surrounding ledger document remains a single,
		// well-formed HTML file and goquery reads the original markup back
		// verbatim as text.
		buf.WriteString("<td data-field=\"html\"><pre>")
		buf.WriteString(html.EscapeString(e.HTML))
		buf.WriteString("</pre></td>")
		buf.WriteString("</tr>\n")
	}
	buf.WriteString("</tbody>\n</table>\n</main>\n</body></html>\n")
	return buf.Bytes()
}

func writeLedgerAttr(buf *bytes.Buffer, name, value string) {
	buf.WriteByte(' ')
	buf.WriteString(name)
	buf.WriteString(`="`)
	buf.WriteString(html.EscapeString(value))
	buf.WriteByte('"')
}

func writeLedgerCell(buf *bytes.Buffer, field, value string) {
	buf.WriteString(`<td data-field="`)
	buf.WriteString(field)
	buf.WriteString(`">`)
	buf.WriteString(html.EscapeString(value))
	buf.WriteString("</td>")
}
