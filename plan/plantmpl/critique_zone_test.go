package plantmpl_test

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/plantmpl"
)

// ---------------------------------------------------------------------------
// CritiqueZone.Render — structural output
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderContainerAttributes(t *testing.T) {
	cz := &plantmpl.CritiqueZone{}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `id="critique"`) {
		t.Error("output missing id=\"critique\"")
	}
	if !strings.Contains(html, `class="section-card"`) {
		t.Error("output missing class=\"section-card\"")
	}
}

// ---------------------------------------------------------------------------
// AssumptionResult.BadgeClass — badge CSS classes
// ---------------------------------------------------------------------------

func TestAssumptionBadgeClassVerified(t *testing.T) {
	a := plantmpl.AssumptionResult{Badge: "verified"}
	if got := a.BadgeClass(); got != "badge-approved" {
		t.Errorf("BadgeClass verified: got %q, want badge-approved", got)
	}
}

func TestAssumptionBadgeClassFalsified(t *testing.T) {
	a := plantmpl.AssumptionResult{Badge: "falsified"}
	if got := a.BadgeClass(); got != "badge-blocked" {
		t.Errorf("BadgeClass falsified: got %q, want badge-blocked", got)
	}
}

func TestAssumptionBadgeClassUnknown(t *testing.T) {
	a := plantmpl.AssumptionResult{Badge: "unknown"}
	if got := a.BadgeClass(); got != "badge-pending" {
		t.Errorf("BadgeClass unknown: got %q, want badge-pending", got)
	}
}

func TestAssumptionBadgeClassEmptyString(t *testing.T) {
	a := plantmpl.AssumptionResult{Badge: ""}
	if got := a.BadgeClass(); got != "badge-pending" {
		t.Errorf("BadgeClass empty: got %q, want badge-pending", got)
	}
}

// ---------------------------------------------------------------------------
// RiskRow.SeverityClass — severity CSS classes
// ---------------------------------------------------------------------------

func TestRiskRowSeverityClassHigh(t *testing.T) {
	r := plantmpl.RiskRow{Severity: "High"}
	if got := r.SeverityClass(); got != "badge-blocked" {
		t.Errorf("SeverityClass High: got %q, want badge-blocked", got)
	}
}

func TestRiskRowSeverityClassMedium(t *testing.T) {
	r := plantmpl.RiskRow{Severity: "Medium"}
	if got := r.SeverityClass(); got != "badge-revision" {
		t.Errorf("SeverityClass Medium: got %q, want badge-revision", got)
	}
}

func TestRiskRowSeverityClassMed(t *testing.T) {
	r := plantmpl.RiskRow{Severity: "Med"}
	if got := r.SeverityClass(); got != "badge-revision" {
		t.Errorf("SeverityClass Med: got %q, want badge-revision", got)
	}
}

func TestRiskRowSeverityClassLow(t *testing.T) {
	r := plantmpl.RiskRow{Severity: "Low"}
	if got := r.SeverityClass(); got != "badge-pending" {
		t.Errorf("SeverityClass Low: got %q, want badge-pending", got)
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — assumption verification section
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderAssumptionBadges(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		Assumptions: []plantmpl.AssumptionResult{
			{Text: "Cache is warm", Badge: "verified", Evidence: "load test shows p99"},
			{Text: "Redis available", Badge: "falsified", Evidence: "staging uses in-memory"},
			{Text: "Auth optional", Badge: "unknown"},
		},
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	// Text content
	if !strings.Contains(html, "Cache is warm") {
		t.Error("missing assumption text: Cache is warm")
	}
	if !strings.Contains(html, "Redis available") {
		t.Error("missing assumption text: Redis available")
	}
	if !strings.Contains(html, "Auth optional") {
		t.Error("missing assumption text: Auth optional")
	}

	// Badge classes
	if !strings.Contains(html, "badge-approved") {
		t.Error("verified assumption should use badge-approved class")
	}
	if !strings.Contains(html, "badge-blocked") {
		t.Error("falsified assumption should use badge-blocked class")
	}
	if !strings.Contains(html, "badge-pending") {
		t.Error("unknown assumption should use badge-pending class")
	}

	// Evidence
	if !strings.Contains(html, "load test shows p99") {
		t.Error("missing evidence text")
	}
}

func TestCritiqueZoneRenderAssumptionWithoutEvidence(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		Assumptions: []plantmpl.AssumptionResult{
			{Text: "No evidence here", Badge: "unknown"},
		},
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "No evidence here") {
		t.Error("missing assumption text")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — dual critic columns
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderDualCriticColumns(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		GeminiCritique:  template.HTML("<p>Gemini says this is risky.</p>"),
		CopilotCritique: template.HTML("<p>Copilot agrees but notes latency.</p>"),
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Design Critique") {
		t.Error("missing Design Critique heading")
	}
	if !strings.Contains(html, "Feasibility Critique") {
		t.Error("missing Feasibility Critique heading")
	}
	if !strings.Contains(html, "Gemini says this is risky.") {
		t.Error("Gemini critique content not rendered")
	}
	if !strings.Contains(html, "Copilot agrees but notes latency.") {
		t.Error("Copilot critique content not rendered")
	}
	// Both columns present — verify grid layout
	if !strings.Contains(html, "grid-template-columns") {
		t.Error("dual-column layout missing grid-template-columns style")
	}
}

func TestCritiqueZoneRenderOnlyGeminiCritique(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		GeminiCritique: template.HTML("<p>Only Gemini ran.</p>"),
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Design Critique") {
		t.Error("missing Design Critique heading when only first critic present")
	}
	if strings.Contains(html, "Feasibility Critique") {
		t.Error("Feasibility Critique should not appear when empty")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — synthesis section
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderSynthesis(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		Synthesis: template.HTML("<p>Both models agree on risk mitigation.</p>"),
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "Synthesis") {
		t.Error("missing Synthesis heading")
	}
	if !strings.Contains(html, "Both models agree on risk mitigation.") {
		t.Error("Synthesis content not rendered")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — risk table
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderRiskTable(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		RiskTable: []plantmpl.RiskRow{
			{Risk: "DB migration fails", Severity: "High", Mitigation: "Rollback script"},
			{Risk: "Cache stampede", Severity: "Medium", Mitigation: "Jitter plus backoff"},
			{Risk: "Minor perf dip", Severity: "Low", Mitigation: "Monitor metrics"},
		},
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	// Table headers
	if !strings.Contains(html, "<th>Risk</th>") {
		t.Error("missing Risk column header")
	}
	if !strings.Contains(html, "<th>Severity</th>") {
		t.Error("missing Severity column header")
	}
	if !strings.Contains(html, "<th>Mitigation</th>") {
		t.Error("missing Mitigation column header")
	}

	// Row content
	if !strings.Contains(html, "DB migration fails") {
		t.Error("missing risk row: DB migration fails")
	}
	if !strings.Contains(html, "Rollback script") {
		t.Error("missing mitigation: Rollback script")
	}

	// Severity badges
	if !strings.Contains(html, "badge-blocked") {
		t.Error("High severity should use badge-blocked class")
	}
	if !strings.Contains(html, "badge-revision") {
		t.Error("Medium severity should use badge-revision class")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — empty / placeholder state
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderEmptyShowsPlaceholder(t *testing.T) {
	cz := &plantmpl.CritiqueZone{}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "wipnote plan critique") {
		t.Error("empty critique should show placeholder with wipnote plan critique command")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — partial critique (only assumptions)
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderPartialAssumptionsOnly(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		Assumptions: []plantmpl.AssumptionResult{
			{Text: "Service mesh installed", Badge: "verified"},
		},
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	if !strings.Contains(html, "Service mesh installed") {
		t.Error("missing assumption text")
	}
	// No critic sections expected
	if strings.Contains(html, "Gemini Critique") {
		t.Error("Gemini Critique should not appear when empty")
	}
	if strings.Contains(html, "Copilot Critique") {
		t.Error("Copilot Critique should not appear when empty")
	}
	if strings.Contains(html, "Synthesis") {
		t.Error("Synthesis should not appear when empty")
	}
	// No placeholder expected when we have content
	if strings.Contains(html, "wipnote plan critique") {
		t.Error("placeholder should not appear when assumptions are present")
	}
}

// ---------------------------------------------------------------------------
// CritiqueZone.Render — HTML content not escaped
// ---------------------------------------------------------------------------

func TestCritiqueZoneRenderHTMLNotEscaped(t *testing.T) {
	cz := &plantmpl.CritiqueZone{
		GeminiCritique:  template.HTML("<strong>Bold Gemini</strong>"),
		CopilotCritique: template.HTML("<em>Italic Copilot</em>"),
		Synthesis:       template.HTML("<ul><li>Point one</li></ul>"),
	}

	var buf bytes.Buffer
	if err := cz.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := buf.String()

	// template.HTML values must not be escaped
	if !strings.Contains(html, "<strong>Bold Gemini</strong>") {
		t.Error("GeminiCritique HTML was escaped — expected raw <strong> tags")
	}
	if !strings.Contains(html, "<em>Italic Copilot</em>") {
		t.Error("CopilotCritique HTML was escaped — expected raw <em> tags")
	}
	if !strings.Contains(html, "<ul><li>Point one</li></ul>") {
		t.Error("Synthesis HTML was escaped — expected raw <ul> tags")
	}
}
