package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// recapListItem is a single entry in the GET /api/recaps response.
type recapListItem struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	WorkItem string    `json:"workItem"`
	Created  time.Time `json:"created"`
}

// recapEmbedScope is the CSS selector for the SPA container that rendered recap
// HTML is injected into. The recap's stylesheet is re-scoped under this selector
// by scopePlanCSS so it applies to the embedded recap only, matching the
// standalone page without bleeding into dashboard chrome.
const recapEmbedScope = ".recap-detail-body"

// recapsListHandler returns a JSON array of all recaps, read from the SQLite
// recaps table (populated by reindex). Sorted by created_at descending.
// GET /api/recaps
func recapsListHandler(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rows, err := dbpkg.ListRecaps(database)
		if err != nil {
			http.Error(w, fmt.Sprintf("listing recaps: %v", err), http.StatusInternalServerError)
			return
		}

		items := make([]recapListItem, 0, len(rows))
		for _, row := range rows {
			item := recapListItem{
				ID:       row.ID,
				Title:    row.Title,
				WorkItem: row.WorkItemID,
			}
			if row.CreatedAt != nil {
				item.Created = *row.CreatedAt
			}
			if item.Title == "" {
				item.Title = row.Outcome
			}
			if item.Title == "" {
				item.Title = row.ID
			}
			items = append(items, item)
		}

		respondJSON(w, items)
	}
}

// recapRouter dispatches /api/recaps/{id}/{action} to the appropriate handler.
// Registered under /api/recaps/ in serve.go.
func recapRouter(wipnoteDir string) http.HandlerFunc {
	renderH := recapRenderHandler(wipnoteDir)
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/render"):
			renderH(w, r)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

// recapRenderHandler serves scoped HTML for a single recap artifact, suitable
// for embedding in the dashboard SPA panel. The committed HTML file is read
// from .wipnote/recaps/<id>.html; its CSS is re-scoped under recapEmbedScope
// so it doesn't bleed into dashboard chrome.
// GET /api/recaps/{id}/render
func recapRenderHandler(wipnoteDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		recapID, err := extractRecapID(r.URL.Path, "/render")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		htmlPath := filepath.Join(wipnoteDir, "recaps", recapID+".html")
		data, err := os.ReadFile(htmlPath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, fmt.Sprintf("recap %s not found", recapID), http.StatusNotFound)
				return
			}
			http.Error(w, fmt.Sprintf("read recap: %v", err), http.StatusInternalServerError)
			return
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
		if err != nil {
			http.Error(w, "parse error", http.StatusInternalServerError)
			return
		}

		var out strings.Builder

		// Emit the recap's stylesheet, scoped under recapEmbedScope so it
		// renders at full fidelity without leaking into the dashboard shell.
		// Uses scopePlanCSS (same function as the plan embed path) — the
		// scoping logic is selector-level and applies identically to recap CSS.
		doc.Find("style").Each(func(_ int, s *goquery.Selection) {
			css := s.Text()
			if css == "" {
				return
			}
			out.WriteString("<style>")
			out.WriteString(scopePlanCSS(css, recapEmbedScope))
			out.WriteString("</style>\n")
		})

		// Include CDN link tags (fonts, highlight.js) verbatim.
		doc.Find("link[rel='stylesheet'], link[rel='preconnect']").Each(func(_ int, s *goquery.Selection) {
			outerHTML, _ := goquery.OuterHtml(s)
			out.WriteString(outerHTML)
			out.WriteString("\n")
		})

		// Emit CONTENT ONLY — no outer html/body shell. The dashboard owns
		// all chrome. Prefer the .recap-layout wrapper if present; fall back
		// to the full body content.
		body := doc.Find("body")
		layout, _ := goquery.OuterHtml(body.Find(".recap-layout").First())
		if layout == "" {
			layout, _ = body.Html()
		}
		out.WriteString(layout)

		// Include scripts from the artifact (inline only; no external re-fetch
		// needed as recap artifacts embed no external scripts today).
		doc.Find("script").Each(func(_ int, s *goquery.Selection) {
			outerHTML, _ := goquery.OuterHtml(s)
			out.WriteString(outerHTML)
			out.WriteString("\n")
		})

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, out.String())
	}
}

// extractRecapID parses a recap ID from URL paths of the form
// /api/recaps/{id}/{suffix}. Returns an error if the ID is missing or invalid.
func extractRecapID(urlPath, suffix string) (string, error) {
	const prefix = "/api/recaps/"
	path := strings.TrimSuffix(urlPath, "/")
	if !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("unexpected path: %s", urlPath)
	}
	mid := path[len(prefix):]
	mid = strings.TrimSuffix(mid, suffix)
	if mid == "" || strings.Contains(mid, "/") {
		return "", fmt.Errorf("missing or invalid recap ID in path: %s", urlPath)
	}
	return mid, nil
}
