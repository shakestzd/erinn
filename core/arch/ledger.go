package arch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	// LedgerFilename is the canonical HTML ledger for architectural memory.
	LedgerFilename = "architecture.html"

	// ArchNodePrefix is the stable graph-node prefix for architecture facts.
	ArchNodePrefix = "arch:"
)

// LedgerPath returns the canonical HTML ledger path under .wipnote/.
func LedgerPath(wipnoteDir string) string {
	return filepath.Join(wipnoteDir, LedgerFilename)
}

// ArchNodeID converts a card slug into the stable graph node ID.
func ArchNodeID(slug string) string {
	return ArchNodePrefix + slug
}

// ArchSlugFromNodeID extracts the card slug from an arch node ID.
func ArchSlugFromNodeID(nodeID string) (string, bool) {
	if !strings.HasPrefix(nodeID, ArchNodePrefix) {
		return "", false
	}
	slug := strings.TrimPrefix(nodeID, ArchNodePrefix)
	if !isValidSlug(slug) {
		return "", false
	}
	return slug, true
}

// ReadLedger parses the canonical architecture ledger HTML into cards.
func ReadLedger(path string) ([]*Card, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("parse architecture ledger: %w", err)
	}

	var cards []*Card
	var parseErr error
	doc.Find(`table[data-architecture-ledger] tbody tr[data-arch-id]`).Each(func(_ int, row *goquery.Selection) {
		if parseErr != nil {
			return
		}
		card, rowErr := parseLedgerRow(row)
		if rowErr != nil {
			parseErr = rowErr
			return
		}
		cards = append(cards, card)
	})
	if parseErr != nil {
		return nil, parseErr
	}
	return cards, nil
}

func parseLedgerRow(row *goquery.Selection) (*Card, error) {
	nodeID, ok := row.Attr("data-arch-id")
	if !ok || strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("ledger row missing data-arch-id")
	}
	slug, ok := ArchSlugFromNodeID(strings.TrimSpace(nodeID))
	if !ok {
		return nil, fmt.Errorf("ledger row has invalid data-arch-id %q", nodeID)
	}

	var paths, links []string
	if raw, ok := row.Attr("data-paths"); ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &paths); err != nil {
			return nil, fmt.Errorf("decode data-paths for %s: %w", slug, err)
		}
	}
	if raw, ok := row.Attr("data-links"); ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &links); err != nil {
			return nil, fmt.Errorf("decode data-links for %s: %w", slug, err)
		}
	}

	card := &Card{
		Name:         slug,
		Kind:         Kind(strings.TrimSpace(attrOr(row, "data-kind"))),
		Paths:        paths,
		VerifiedAt:   strings.TrimSpace(attrOr(row, "data-verified-at")),
		Links:        links,
		CreatedBy:    strings.TrimSpace(attrOr(row, "data-created-by")),
		SupersededBy: strings.TrimSpace(attrOr(row, "data-superseded-by")),
		Retired:      strings.EqualFold(strings.TrimSpace(attrOr(row, "data-retired")), "true"),
		Body:         strings.TrimSpace(row.Find(`td[data-field="body"]`).First().Text()),
	}

	if v := strings.TrimSpace(attrOr(row, "data-created-at")); v != "" {
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
			card.CreatedAt = ts
		}
	}
	if v := strings.TrimSpace(attrOr(row, "data-updated-at")); v != "" {
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
			card.UpdatedAt = ts
		}
	}
	return card, nil
}

func attrOr(sel *goquery.Selection, name string) string {
	if v, ok := sel.Attr(name); ok {
		return v
	}
	return ""
}

// WriteLedger rewrites the canonical architecture ledger HTML atomically.
func WriteLedger(path string, cards []*Card) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create architecture ledger dir: %w", err)
	}

	sorted := make([]*Card, len(cards))
	copy(sorted, cards)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	htmlDoc, err := marshalLedger(sorted)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, htmlDoc, 0o644); err != nil {
		return fmt.Errorf("write architecture ledger: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename architecture ledger: %w", err)
	}
	return nil
}

func marshalLedger(cards []*Card) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("<!DOCTYPE html>\n")
	buf.WriteString("<html><head><meta charset=\"utf-8\"><title>Architecture Memory</title></head><body>\n")
	buf.WriteString("<main>\n")
	buf.WriteString("<table data-architecture-ledger=\"true\">\n")
	buf.WriteString("<thead><tr><th>ID</th><th>Kind</th><th>Status</th><th>Paths</th><th>Links</th><th>Verified</th><th>Created By</th><th>Body</th></tr></thead>\n")
	buf.WriteString("<tbody>\n")
	for _, card := range cards {
		pathsJSON, err := json.Marshal(nilToEmpty(card.Paths))
		if err != nil {
			return nil, fmt.Errorf("marshal ledger paths for %s: %w", card.Name, err)
		}
		linksJSON, err := json.Marshal(nilToEmpty(card.Links))
		if err != nil {
			return nil, fmt.Errorf("marshal ledger links for %s: %w", card.Name, err)
		}
		status := "active"
		if card.IsRetired() {
			status = "retired"
		}

		buf.WriteString(`<tr`)
		writeAttr(&buf, "data-arch-id", ArchNodeID(card.Name))
		writeAttr(&buf, "data-slug", card.Name)
		writeAttr(&buf, "data-kind", string(card.Kind))
		writeAttr(&buf, "data-status", status)
		writeAttr(&buf, "data-paths", string(pathsJSON))
		writeAttr(&buf, "data-links", string(linksJSON))
		writeAttr(&buf, "data-verified-at", card.VerifiedAt)
		writeAttr(&buf, "data-created-by", card.CreatedBy)
		writeAttr(&buf, "data-superseded-by", card.SupersededBy)
		writeAttr(&buf, "data-retired", fmt.Sprintf("%t", card.Retired))
		if !card.CreatedAt.IsZero() {
			writeAttr(&buf, "data-created-at", card.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
		if !card.UpdatedAt.IsZero() {
			writeAttr(&buf, "data-updated-at", card.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		buf.WriteString(">")
		buf.WriteString("<td data-field=\"id\">")
		buf.WriteString(html.EscapeString(ArchNodeID(card.Name)))
		buf.WriteString("</td>")
		buf.WriteString("<td data-field=\"kind\">")
		buf.WriteString(html.EscapeString(string(card.Kind)))
		buf.WriteString("</td>")
		buf.WriteString("<td data-field=\"status\">")
		buf.WriteString(html.EscapeString(status))
		buf.WriteString("</td>")
		buf.WriteString("<td data-field=\"paths\"><pre>")
		buf.WriteString(html.EscapeString(strings.Join(card.Paths, "\n")))
		buf.WriteString("</pre></td>")
		buf.WriteString("<td data-field=\"links\"><pre>")
		buf.WriteString(html.EscapeString(strings.Join(card.Links, "\n")))
		buf.WriteString("</pre></td>")
		buf.WriteString("<td data-field=\"verified-at\">")
		buf.WriteString(html.EscapeString(card.VerifiedAt))
		buf.WriteString("</td>")
		buf.WriteString("<td data-field=\"created-by\">")
		buf.WriteString(html.EscapeString(card.CreatedBy))
		buf.WriteString("</td>")
		buf.WriteString("<td data-field=\"body\"><pre>")
		buf.WriteString(html.EscapeString(strings.TrimSpace(card.Body)))
		buf.WriteString("</pre></td>")
		buf.WriteString("</tr>\n")
	}
	buf.WriteString("</tbody>\n</table>\n</main>\n</body></html>\n")
	return buf.Bytes(), nil
}

func writeAttr(buf *bytes.Buffer, name, value string) {
	buf.WriteByte(' ')
	buf.WriteString(name)
	buf.WriteString(`="`)
	buf.WriteString(html.EscapeString(value))
	buf.WriteByte('"')
}

func nilToEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
