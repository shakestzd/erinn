package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
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
	cmd := &cobra.Command{
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
	cmd.AddCommand(planCritiqueReviseCmd())
	return cmd
}

// planCritiqueReviseCmd registers `wipnote plan critique revise <plan-id>` —
// the critique write path. It rewrites a slice's prose fields directly, in
// place, instead of appending an entry to the now-deprecated
// critic_revisions list (see planyaml.PlanSlice.CriticRevisions).
//
// Appending was the measured mechanism behind per-slice word inflation: 77%
// growth in words-per-slice across 45 plans / 282 slices while
// slices-per-plan fell 22%. Rewriting a plain string field in place cannot
// accumulate the same way, and superseded wording is not lost — it stays
// recoverable via `wipnote history <plan-id>`, which walks the plan YAML's
// git history (see history.go — plan- ids resolve to .yaml, not .html).
func planCritiqueReviseCmd() *cobra.Command {
	var sliceNum int
	var what, why, tests string

	cmd := &cobra.Command{
		Use:   "revise <plan-id>",
		Short: "Rewrite a slice's prose fields in place (the critique write path)",
		Long: `Rewrite one or more of a slice's prose fields (what/why/tests)
directly, in place. Any flag left empty leaves that field unchanged.

This replaces hand-editing the YAML to append a critic_revisions entry.
critic_revisions is deprecated: it still parses and renders for plans written
before this change, but this command never reads or writes it.

Example:
  wipnote plan critique revise plan-a1b2c3d4 --slice 3 --what "revised text"`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			return runPlanCritiqueRevise(wipnoteDir, args[0], sliceNum, what, why, tests)
		},
	}
	cmd.Flags().IntVar(&sliceNum, "slice", 0, "slice number to revise (required)")
	cmd.Flags().StringVar(&what, "what", "", "replacement text for the slice's what field")
	cmd.Flags().StringVar(&why, "why", "", "replacement text for the slice's why field")
	cmd.Flags().StringVar(&tests, "tests", "", "replacement text for the slice's tests field")
	_ = cmd.MarkFlagRequired("slice")
	return cmd
}

// runPlanCritiqueRevise loads the plan, rewrites the named slice's prose
// fields in place, and saves + autocommits so the prior wording is preserved
// in git history rather than accumulated in the YAML itself.
func runPlanCritiqueRevise(wipnoteDir, planID string, sliceNum int, what, why, tests string) error {
	planPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(planPath)
	if err != nil {
		return fmt.Errorf("load plan %q: %w", planID, err)
	}

	if err := reviseSliceInPlace(plan, sliceNum, what, why, tests); err != nil {
		return err
	}

	if err := planyaml.Save(planPath, plan); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	if err := commitPlanChange(planPath, fmt.Sprintf("plan(%s): critique revise slice %d", planID, sliceNum)); err != nil {
		return fmt.Errorf("autocommit critique revise: %w", err)
	}

	fmt.Printf("Slice %d revised in place for %s\n", sliceNum, planID)
	return nil
}

// reviseSliceInPlace overwrites the given prose fields on the slice numbered
// sliceNum, leaving any field passed as "" unchanged. It never reads or
// writes CriticRevisions — nothing in this write path appends to that
// (deprecated) list. Returns an error if no slice with that number exists.
func reviseSliceInPlace(plan *planyaml.PlanYAML, sliceNum int, what, why, tests string) error {
	for i := range plan.Slices {
		if plan.Slices[i].Num != sliceNum {
			continue
		}
		if what != "" {
			plan.Slices[i].What = what
		}
		if why != "" {
			plan.Slices[i].Why = why
		}
		if tests != "" {
			plan.Slices[i].Tests = tests
		}
		return nil
	}
	return fmt.Errorf("slice %d not found in plan %s", sliceNum, plan.Meta.ID)
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
