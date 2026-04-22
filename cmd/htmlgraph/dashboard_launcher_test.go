package main

import (
	"os"
	"strings"
	"testing"
)

// TestLauncherModalPresentInHTML verifies the launcher modal skeleton is
// present in the dashboard HTML and contains all required form controls.
func TestLauncherModalPresentInHTML(t *testing.T) {
	data, err := os.ReadFile("dashboard/index.html")
	if err != nil {
		t.Fatalf("read dashboard/index.html: %v", err)
	}
	html := string(data)

	checks := []struct {
		name    string
		contain string
	}{
		{"launcher modal container", `id="launcher-modal"`},
		{"launcher backdrop", `launcher-backdrop`},
		{"agent select", `name="agent"`},
		{"mode select", `name="mode"`},
		{"work item search input", `name="work_item_search"`},
		{"cwd kind select", `name="cwd_kind"`},
		{"work item hidden field", `id="launcher-work-item"`},
		{"dialog role", `role="dialog"`},
		{"aria-modal attribute", `aria-modal`},
	}

	for _, c := range checks {
		if !strings.Contains(html, c.contain) {
			t.Errorf("launcher modal missing %s: expected to find %q in dashboard/index.html", c.name, c.contain)
		}
	}
}

// TestBuildLaunchPayloadJSFunctionPresent verifies the pure helper function
// that assembles the POST body exists in app.js and is exported via window.
func TestBuildLaunchPayloadJSFunctionPresent(t *testing.T) {
	data, err := os.ReadFile("dashboard/js/app.js")
	if err != nil {
		t.Fatalf("read dashboard/js/app.js: %v", err)
	}
	js := string(data)

	checks := []struct {
		name    string
		contain string
	}{
		{"buildLaunchPayload function", "buildLaunchPayload"},
		{"openLauncherModal function", "openLauncherModal"},
		{"closeLauncherModal function", "closeLauncherModal"},
		{"debounce helper", "debounce"},
		{"searchWorkItems function", "searchWorkItems"},
		{"submitLauncher function", "submitLauncher"},
	}

	for _, c := range checks {
		if !strings.Contains(js, c.contain) {
			t.Errorf("app.js missing %s: expected to find %q", c.name, c.contain)
		}
	}
}
