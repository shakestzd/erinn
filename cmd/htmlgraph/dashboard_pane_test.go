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
