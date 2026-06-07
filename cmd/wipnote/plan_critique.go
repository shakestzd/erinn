package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/internal/planyaml"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// critiqueProjectOpener is the function used to open the workitem project. It
// is a package-level variable so tests can inject a spy to assert that the DB
// is never opened for YAML plans.
var critiqueProjectOpener = workitem.Open

// critiqueOutput is the structured JSON output from plan critique.
type critiqueOutput struct {
	PlanID            string          `json:"plan_id"`
	Title             string          `json:"title"`
	Description       string          `json:"description,omitempty"`
	Status            string          `json:"status"`
	Complexity        string          `json:"complexity"`
	SliceCount        int             `json:"slice_count"`
	CritiqueWarranted bool            `json:"critique_warranted"`
	Slices            []critiqueSlice `json:"slices,omitempty"`
}

type critiqueSlice struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// planCritiqueCmd extracts plan content for AI critique.
func planCritiqueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "critique <plan-id>",
		Short: "Extract plan content for AI review",
		Long: `Read a plan and output structured JSON for AI critique.

Complexity-gated: plans with fewer than 3 slices output
critique_warranted=false.

Example:
  wipnote plan critique plan-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			return runPlanCritique(wipnoteDir, args[0])
		},
	}
}

func runPlanCritique(wipnoteDir, planID string) error {
	out, err := extractCritiqueData(wipnoteDir, planID)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// extractCritiqueData reads a plan and extracts structured data for critique.
//
// For v2 plans (those with a .yaml file), all data is read directly from YAML
// — no SQLite/workitem.Open call is made. Legacy HTML-only plans fall back to
// the previous workitem path.
func extractCritiqueData(wipnoteDir, planID string) (*critiqueOutput, error) {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if plan, err := planyaml.Load(yamlPath); err == nil {
		return extractCritiqueFromYAML(planID, plan), nil
	}

	// Legacy fallback: HTML-only plan — open workitem project (requires SQLite).
	return extractCritiqueFromWorkitem(wipnoteDir, planID)
}

// extractCritiqueFromYAML builds the critiqueOutput entirely from a PlanYAML
// with no database access.
func extractCritiqueFromYAML(planID string, plan *planyaml.PlanYAML) *critiqueOutput {
	out := &critiqueOutput{
		PlanID:      planID,
		Title:       plan.Meta.Title,
		Description: plan.Meta.Description,
		Status:      plan.Meta.Status,
	}
	for _, s := range plan.Slices {
		out.Slices = append(out.Slices, critiqueSlice{
			Number: s.Num,
			Title:  s.Title,
		})
	}
	out.SliceCount = len(out.Slices)
	out.Complexity, out.CritiqueWarranted = classifyComplexity(out.SliceCount)
	return out
}

// extractCritiqueFromWorkitem reads plan data via workitem.Open (legacy path
// for HTML-only plans that predate the YAML schema).
func extractCritiqueFromWorkitem(wipnoteDir, planID string) (*critiqueOutput, error) {
	p, err := critiqueProjectOpener(wipnoteDir, agentForClaim())
	if err != nil {
		return nil, fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	node, err := p.Plans.Get(planID)
	if err != nil {
		return nil, fmt.Errorf("plan %q not found: %w", planID, err)
	}

	// Extract description from plan HTML, not node.Content.
	// For CRISPI plans, the description is in the <p> tag after the <h1>.
	description := extractPlanDescription(wipnoteDir, planID)

	out := &critiqueOutput{
		PlanID:      planID,
		Title:       strings.TrimPrefix(node.Title, "Plan: "),
		Description: description,
		Status:      string(node.Status),
	}

	// Extract slices from HTML steps; YAML fallback is not needed here because
	// extractCritiqueData already tried YAML first and it was absent.
	for i, step := range node.Steps {
		out.Slices = append(out.Slices, critiqueSlice{
			Number: i + 1,
			Title:  step.Description,
		})
	}

	// Complexity gate.
	out.SliceCount = len(out.Slices)
	out.Complexity, out.CritiqueWarranted = classifyComplexity(out.SliceCount)

	return out, nil
}

// extractPlanDescription reads the plan HTML file and extracts the description
// from the <p> tag immediately after the <h1> in the header.
func extractPlanDescription(wipnoteDir, planID string) string {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	data, err := os.ReadFile(planPath)
	if err != nil {
		return ""
	}

	htmlContent := string(data)

	// Find the <h1> tag.
	h1Start := strings.Index(htmlContent, "<h1>")
	if h1Start < 0 {
		return ""
	}

	// Find the end of <h1> tag.
	h1End := strings.Index(htmlContent[h1Start:], "</h1>")
	if h1End < 0 {
		return ""
	}

	// Search for <p (allowing for attributes like <p style="...">).
	searchStart := h1Start + h1End + 5 // 5 = len("</h1>")
	pStart := strings.Index(htmlContent[searchStart:], "<p")
	if pStart < 0 {
		return ""
	}

	// Extract text between the closing > and </p>.
	pStart += searchStart
	closeTagIdx := strings.Index(htmlContent[pStart:], ">")
	if closeTagIdx < 0 {
		return ""
	}
	rest := htmlContent[pStart+closeTagIdx+1:]
	pEnd := strings.Index(rest, "</p>")
	if pEnd < 0 {
		return ""
	}

	desc := strings.TrimSpace(rest[:pEnd])

	// Strip HTML tags if present (e.g., <strong>, <em>).
	desc = strings.ReplaceAll(desc, "<strong>", "")
	desc = strings.ReplaceAll(desc, "</strong>", "")
	desc = strings.ReplaceAll(desc, "<em>", "")
	desc = strings.ReplaceAll(desc, "</em>", "")
	desc = strings.ReplaceAll(desc, "<i>", "")
	desc = strings.ReplaceAll(desc, "</i>", "")
	desc = strings.ReplaceAll(desc, "<b>", "")
	desc = strings.ReplaceAll(desc, "</b>", "")

	return desc
}

// classifyComplexity determines plan complexity and whether critique is warranted.
func classifyComplexity(sliceCount int) (complexity string, warranted bool) {
	switch {
	case sliceCount < 3:
		return "low", false
	case sliceCount < 6:
		return "medium", true
	default:
		return "high", true
	}
}
