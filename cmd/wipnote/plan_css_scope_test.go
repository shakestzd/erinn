package main

import (
	"strings"
	"testing"
)

// TestScopePlanCSS_RootMapsToScope verifies that :root custom properties are
// re-anchored onto the scope container so the plan's theme variables resolve
// for the embedded content instead of being stripped (the prior lossy behavior).
func TestScopePlanCSS_RootMapsToScope(t *testing.T) {
	in := ":root{--accent:#cdff00;--bg:#0f0f13}"
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, ".plan-detail-body{--accent:#cdff00;--bg:#0f0f13}") {
		t.Errorf(":root not scoped onto container.\ngot: %s", got)
	}
	// The raw, unscoped :root must NOT leak into the document.
	if strings.Contains(got, ":root{") {
		t.Errorf("unscoped :root leaked into output: %s", got)
	}
}

// TestScopePlanCSS_DataThemeScoped verifies [data-theme] rules keep gating on
// the attribute while being confined to the scope.
func TestScopePlanCSS_DataThemeScoped(t *testing.T) {
	in := `[data-theme="light"]{--bg:#fff}` +
		"\n" + `[data-theme="light"] .badge-approved{color:#16a34a}`
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, `[data-theme="light"] .plan-detail-body{--bg:#fff}`) {
		t.Errorf("bare [data-theme] not scoped.\ngot: %s", got)
	}
	if !strings.Contains(got, `[data-theme="light"] .plan-detail-body .badge-approved{color:#16a34a}`) {
		t.Errorf("[data-theme] descendant not scoped.\ngot: %s", got)
	}
}

// TestScopePlanCSS_HtmlBodyMapped verifies html/body document-level selectors
// map onto the container (so they cannot reset the dashboard shell), while
// body-state compounds re-anchor their leading body to the container.
func TestScopePlanCSS_HtmlBodyMapped(t *testing.T) {
	in := "html{font-size:15px}" +
		"body{background:var(--bg);display:grid}" +
		"body.left-nav-collapsed .plan-sidebar{width:48px}"
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, ".plan-detail-body{font-size:15px}") {
		t.Errorf("html not mapped to scope.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body{background:var(--bg);display:grid}") {
		t.Errorf("body not mapped to scope.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body.left-nav-collapsed .plan-sidebar{width:48px}") {
		t.Errorf("body-state compound not re-anchored.\ngot: %s", got)
	}
	// Document-level html{ / body{ must not leak unscoped.
	if strings.Contains(got, "}html{") || strings.HasPrefix(got, "html{") {
		t.Errorf("unscoped html leaked: %s", got)
	}
}

// TestScopePlanCSS_UniversalReset verifies the universal box-sizing reset is
// confined to descendants of the container and never resets the shell.
func TestScopePlanCSS_UniversalReset(t *testing.T) {
	in := "*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}"
	got := scopePlanCSS(in, ".plan-detail-body")
	want := ".plan-detail-body *,.plan-detail-body *::before,.plan-detail-body *::after{box-sizing:border-box;margin:0;padding:0}"
	if !strings.Contains(got, want) {
		t.Errorf("universal reset not scoped.\nwant: %s\ngot:  %s", want, got)
	}
}

// TestScopePlanCSS_NormalSelectorPrefixed verifies ordinary component selectors
// (the ones whose loss degraded slice cards) are prefixed with the scope so the
// full component styling applies inside the embed.
func TestScopePlanCSS_NormalSelectorPrefixed(t *testing.T) {
	in := ".slice-card{border:1px solid var(--border)}" +
		".plan-sidebar .nav-triage-badge,.slice-approval-segment{color:var(--accent)}"
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, ".plan-detail-body .slice-card{border:1px solid var(--border)}") {
		t.Errorf(".slice-card not prefixed.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body .plan-sidebar .nav-triage-badge") {
		t.Errorf("triage-badge selector not prefixed.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body .slice-approval-segment") {
		t.Errorf("approval-segment selector not prefixed.\ngot: %s", got)
	}
}

// TestScopePlanCSS_MediaQueryInnerScoped verifies @media blocks keep their
// query verbatim while inner selectors get scoped.
func TestScopePlanCSS_MediaQueryInnerScoped(t *testing.T) {
	in := "@media(max-width:768px){body{grid-template-columns:1fr}.plan-sidebar{display:none}}"
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, "@media(max-width:768px){") {
		t.Errorf("media query not preserved.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body{grid-template-columns:1fr}") {
		t.Errorf("media inner body not mapped.\ngot: %s", got)
	}
	if !strings.Contains(got, ".plan-detail-body .plan-sidebar{display:none}") {
		t.Errorf("media inner selector not scoped.\ngot: %s", got)
	}
}

// TestScopePlanCSS_KeyframesUntouched verifies @keyframes / @font-face at-rules
// are passed through unchanged (no selectors to scope, and scoping their
// percentage keyframe selectors would break the animation).
func TestScopePlanCSS_KeyframesUntouched(t *testing.T) {
	in := "@keyframes spin{0%{transform:rotate(0)}100%{transform:rotate(360deg)}}"
	got := scopePlanCSS(in, ".plan-detail-body")
	if !strings.Contains(got, "@keyframes spin{0%{transform:rotate(0)}100%{transform:rotate(360deg)}}") {
		t.Errorf("@keyframes altered.\ngot: %s", got)
	}
	if strings.Contains(got, ".plan-detail-body 0%") || strings.Contains(got, ".plan-detail-body 100%") {
		t.Errorf("keyframe selectors wrongly scoped: %s", got)
	}
}

// TestScopePlanCSS_StringLiteralBracesIgnored verifies braces inside string
// literals (e.g. content: "{") do not confuse the brace matcher.
func TestScopePlanCSS_StringLiteralBracesIgnored(t *testing.T) {
	in := `.nav-link::after{content:"}"}` + ".x{color:red}"
	got := scopePlanCSS(in, ".scope")
	if !strings.Contains(got, `.scope .nav-link::after{content:"}"}`) {
		t.Errorf("string-literal brace mishandled.\ngot: %s", got)
	}
	if !strings.Contains(got, ".scope .x{color:red}") {
		t.Errorf("rule after string-literal brace lost.\ngot: %s", got)
	}
}
