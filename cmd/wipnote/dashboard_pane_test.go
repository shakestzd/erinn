package main

import (
	"os"
	"strings"
	"testing"
)

// TestPaneRegistryExposed verifies that all required multi-pane symbols are
// present in app.js.
func TestPaneRegistryExposed(t *testing.T) {
	data, err := os.ReadFile("dashboard/js/app.js")
	if err != nil {
		t.Fatalf("read dashboard/js/app.js: %v", err)
	}
	src := string(data)
	for _, marker := range []string{
		"paneRegistry",
		"buildPaneElement",
		"renderPane",
		"renderAllPanes",
		"navigator.sendBeacon",
		"/api/terminal/stop-all",
		"/api/terminal/sessions",
	} {
		if !strings.Contains(src, marker) {
			t.Errorf("app.js missing marker %q", marker)
		}
	}
}

// TestPaneContainerInHTML verifies the pane-layer container is present in the
// dashboard HTML.
func TestPaneContainerInHTML(t *testing.T) {
	data, err := os.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatalf("read dashboard/index.html: %v", err)
	}
	if !strings.Contains(string(data), `id="pane-layer"`) {
		t.Fatal("pane-layer container missing from dashboard HTML")
	}
}

func TestDashboardHTMLUsesWipnoteBranding(t *testing.T) {
	data, err := os.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatalf("read dashboard/index.html: %v", err)
	}
	html := string(data)

	for _, legacy := range []string{
		"HtmlGraph",
		"htmlgraph status",
		"htmlgraph plan generate",
	} {
		if strings.Contains(html, legacy) {
			t.Errorf("dashboard HTML still contains legacy visible branding %q", legacy)
		}
	}
	for _, want := range []string{
		"<title>wipnote Dashboard</title>",
		`<span id="brand-text">wipnote</span>`,
		"wipnote status",
		"wipnote plan generate",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard HTML missing wipnote branding %q", want)
		}
	}
}

func TestResumeViewPresentInHTML(t *testing.T) {
	data, err := os.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatalf("read dashboard/index.html: %v", err)
	}
	html := string(data)

	// The standalone Resume tab (data-view="resume" nav button + id="v-resume"
	// panel) was intentionally removed in feat-021786c9 (commit 213062732),
	// which merged the resumable work-items surface into the Sessions tab as a
	// "Resumable Work Items" section. The resume content markers still live in
	// the HTML, now inside sessions-list-view — assert those remain present.
	for _, want := range []string{
		`id="resume-count"`,
		`id="resume-body"`,
		`id="resume-empty"`,
		`id="resume-loading"`,
		`id="resume-error"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard HTML missing resumable-section marker %q", want)
		}
	}
}

func TestResumeViewJSHooksPresent(t *testing.T) {
	data, err := os.ReadFile("dashboard/js/app.js")
	if err != nil {
		t.Fatalf("read dashboard/js/app.js: %v", err)
	}
	js := string(data)

	for _, want := range []string{
		"var resumableSessions = [];",
		"function fetchResumableSessions()",
		"function renderResumableSessions()",
		"buildProjectUrl('sessions/resumable')",
		"resume-loading",
		"resume-error",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing resume view marker %q", want)
		}
	}
}

// TestPaneStyles verifies the required CSS classes for floating panes exist in
// components.css.
func TestPaneStyles(t *testing.T) {
	data, err := os.ReadFile("dashboard/css/components.css")
	if err != nil {
		t.Fatalf("read dashboard/css/components.css: %v", err)
	}
	for _, cls := range []string{".terminal-pane", ".pane-titlebar", ".pane-close", ".pane-body"} {
		if !strings.Contains(string(data), cls) {
			t.Errorf("components.css missing %q", cls)
		}
	}
}

func TestEventTreeAccentRails(t *testing.T) {
	jsData, err := os.ReadFile("dashboard/components/event-tree.js")
	if err != nil {
		t.Fatalf("read dashboard/components/event-tree.js: %v", err)
	}
	js := string(jsData)
	for _, marker := range []string{
		"_rowAccentClass(evt)",
		"_spanAccentClass(span)",
		"accent-claude",
		"accent-codex",
		"accent-gemini",
		"accent-user",
		"accent-system",
		"event-row-otel-detail depth-' + depth + ' ' + this._spanAccentClass(span)",
	} {
		if !strings.Contains(js, marker) {
			t.Errorf("event-tree.js missing accent marker %q", marker)
		}
	}

	cssData, err := os.ReadFile("dashboard/css/components.css")
	if err != nil {
		t.Fatalf("read dashboard/css/components.css: %v", err)
	}
	css := string(cssData)
	for _, cls := range []string{
		".event-row.accent-claude",
		".event-row.accent-codex",
		".event-row.accent-gemini",
		".event-row.accent-user",
		".event-row.accent-system",
		"border-left: 3px solid transparent",
	} {
		if !strings.Contains(css, cls) {
			t.Errorf("components.css missing accent marker %q", cls)
		}
	}
}
