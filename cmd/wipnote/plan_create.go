package main

import (
	"fmt"
	"html"
	htmltemplate "html/template"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/plantmpl"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

// planCreateFromTopicCmd creates a plan node directly from a topic,
// without requiring a pre-existing track or feature.
func planCreateFromTopicCmd() *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a plan from a topic",
		Long: `Create a plan node from a title and optional description.

Unlike 'plan generate' (which scaffolds from an existing work item), this
creates a standalone plan for design-first workflows. Add slices with
'plan add-slice', questions with 'plan add-question', then review and finalize.

Example:
  wipnote plan create "Auth Middleware Rewrite" --description "Rewrite for compliance"`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wipnoteDir, err := findWipnoteDir()
			if err != nil {
				return err
			}
			planID, err := createPlanFromTopic(wipnoteDir, args[0], description)
			if err != nil {
				return err
			}
			yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
			htmlPath := filepath.Join(wipnoteDir, "plans", planID+".html")
			fmt.Printf("Edit here:  %s\n", yamlPath)
			fmt.Printf("Then:       wipnote plan finalize %s\n", planID)
			fmt.Println(htmlPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "plan description")
	return cmd
}

// createPlanFromTopic creates a plan node and scaffolds the CRISPI interactive
// template. Returns the plan ID (e.g. plan-a1b2c3d4).
func createPlanFromTopic(wipnoteDir, title, description string) (string, error) {
	p, err := workitem.Open(wipnoteDir, agentForClaim())
	if err != nil {
		return "", fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	opts := []workitem.PlanOption{
		workitem.PlanWithPriority("medium"),
	}
	if description != "" {
		opts = append(opts, workitem.PlanWithContent(description))
	}

	node, err := p.Plans.Create(title, opts...)
	if err != nil {
		return "", fmt.Errorf("create plan: %w", err)
	}

	// Create the YAML scaffold (source of truth for plan content).
	yamlPath := filepath.Join(wipnoteDir, "plans", node.ID+".yaml")
	if _, err := os.Stat(yamlPath); os.IsNotExist(err) {
		emptyPlan := planyaml.NewPlan(node.ID, title, description)
		if err := planyaml.Save(yamlPath, emptyPlan); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create YAML scaffold: %v\n", err)
		}
	}

	// HTML stays minimal (workitem registration only).
	// Use `plan render <id>` to generate full HTML from YAML.
	return node.ID, nil
}

// scaffoldCRISPIPlanFromNode regenerates the CRISPI template from a full node,
// including any existing slices (steps). Used by the re-scaffold path.
func scaffoldCRISPIPlanFromNode(wipnoteDir string, node *models.Node) error {
	plansDir := filepath.Join(wipnoteDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}

	page := plantmpl.BuildFromWorkItem(
		node.ID, node.TrackID, node.Title, node.Content,
		time.Now().UTC().Format("2006-01-02"),
	)

	// Convert node steps into typed SliceCards and GraphNodes.
	for i, step := range node.Steps {
		num := i + 1
		page.Slices = append(page.Slices, plantmpl.SliceCard{
			Num:    num,
			Title:  step.Description,
			Status: "pending",
		})
		page.Graph = ensureGraph(page.Graph)
		page.Graph.Nodes = append(page.Graph.Nodes, plantmpl.GraphNode{
			Num:    num,
			Name:   step.Description,
			Status: "pending",
		})
	}

	// Enrich with Questions and Critique from the YAML file if present.
	enrichPageFromYAML(wipnoteDir, node.ID, page)

	outPath := filepath.Join(plansDir, node.ID+".html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create plan file: %w", err)
	}
	defer f.Close()

	return page.Render(f)
}

// ensureGraph lazily initializes the DependencyGraph if nil.
func ensureGraph(g *plantmpl.DependencyGraph) *plantmpl.DependencyGraph {
	if g == nil {
		return &plantmpl.DependencyGraph{}
	}
	return g
}

// enrichPageFromYAML loads the YAML plan file (if it exists) and populates
// page.Questions and page.Critique from the YAML data. This bridges the
// planyaml data model to the plantmpl rendering structs.
// If the YAML file does not exist the function is a no-op.
func enrichPageFromYAML(wipnoteDir, planID string, page *plantmpl.PlanPage) {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		if _, statErr := os.Stat(yamlPath); os.IsNotExist(statErr) {
			log.Printf("enrichPageFromYAML: no YAML found for %s (expected %s)", planID, yamlPath)
		}
		return
	}

	// Map meta fields when the page was built with empty/default values.
	if page.Title == "" && plan.Meta.Title != "" {
		page.Title = plan.Meta.Title
	}
	if page.Description == "" && plan.Meta.Description != "" {
		page.Description = plan.Meta.Description
	}
	if plan.Meta.Status != "" {
		page.Status = plan.Meta.Status
	}
	if plan.Meta.Priority != "" {
		page.Priority = plan.Meta.Priority
	}
	if plan.Meta.TrackID != "" && page.FeatureID == "" {
		page.FeatureID = plan.Meta.TrackID
	}
	// Always prefer the original creation date from the YAML over the render-time
	// default set by BuildFromTopic/BuildFromWorkItem. The render-time default was
	// misleading on re-render (header read "Created [today]" even for old plans).
	if plan.Meta.CreatedAt != "" {
		page.Date = plan.Meta.CreatedAt
	}
	// Surface the monotonic version counter from meta.version so the header can
	// show the plan's current revision (v1, v2, v3, ...).
	if plan.Meta.Version > 0 {
		page.Version = plan.Meta.Version
	}

	// Map design section (problem + goals + constraints → HTML).
	if plan.Design.Problem != "" || len(plan.Design.Goals) > 0 {
		var b strings.Builder
		if plan.Design.Problem != "" {
			b.WriteString("<h4>Problem</h4>\n<div data-markdown>")
			b.WriteString(html.EscapeString(plan.Design.Problem))
			b.WriteString("</div>\n")
		}
		if len(plan.Design.Goals) > 0 {
			b.WriteString("<h4>Goals</h4>\n<ol>\n")
			for _, g := range plan.Design.Goals {
				b.WriteString("<li data-markdown>")
				b.WriteString(html.EscapeString(g))
				b.WriteString("</li>\n")
			}
			b.WriteString("</ol>\n")
		}
		if len(plan.Design.Constraints) > 0 {
			b.WriteString("<h4>Constraints</h4>\n<ul>\n")
			for _, c := range plan.Design.Constraints {
				b.WriteString("<li data-markdown>")
				b.WriteString(html.EscapeString(c))
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul>\n")
		}
		page.Design = &plantmpl.DesignSection{
			Content: htmltemplate.HTML(b.String()), //nolint:gosec
		}
	}

	// Map slices from YAML (overrides any existing slices from node steps).
	if len(plan.Slices) > 0 {
		page.Slices = nil
		page.Graph = &plantmpl.DependencyGraph{}
		isV2 := false
		for _, s := range plan.Slices {
			sc := plantmpl.SliceCardFromPlanSlice(s)
			page.Slices = append(page.Slices, sc)
			// Detect v2: any slice with questions or critic_revisions marks the plan as v2.
			if len(s.Questions) > 0 || len(s.CriticRevisions) > 0 {
				isV2 = true
			}
			depsStr := sc.Deps
			page.Graph.Nodes = append(page.Graph.Nodes, plantmpl.GraphNode{
				Num:       s.Num,
				Name:      s.Title,
				Deps:      depsStr,
				Files:     len(s.Files),
				Status:    plantmpl.ApprovalStatusToGraphStatus(s.ApprovalStatus),
				Issues:    len(s.CriticRevisions),
				Questions: len(s.Questions),
			})
		}
		page.IsV2 = isV2

		// Build the visual overview zone from all slices' blocks.
		page.VisualOverview = &plantmpl.VisualOverviewZone{Slices: plan.Slices}
	}

	// Map questions to DecisionCards.
	// Only set Selected when a human has explicitly answered (q.Answer != nil).
	// Recommended is highlighted but not pre-selected.
	if len(plan.Questions) > 0 {
		var cards []plantmpl.DecisionCard
		for _, q := range plan.Questions {
			var opts []string
			for _, o := range q.Options {
				opts = append(opts, o.Label)
			}
			// Map answer key to label for template comparison
			selected := ""
			if q.Answer != nil {
				for _, o := range q.Options {
					if o.Key == *q.Answer {
						selected = o.Label
						break
					}
				}
			}
			// Find the recommended option's label
			recommended := ""
			if q.Recommended != "" {
				for _, o := range q.Options {
					if o.Key == q.Recommended {
						recommended = o.Label
						break
					}
				}
			}
			cards = append(cards, plantmpl.DecisionCard{
				ID:          q.ID,
				Text:        q.Text,
				Options:     opts,
				Selected:    selected,
				Recommended: recommended,
			})
		}
		page.Questions = &plantmpl.QuestionsSection{Cards: cards}
	}

	// Map critique section.
	if plan.Critique != nil {
		cz := &plantmpl.CritiqueZone{}

		for _, a := range plan.Critique.Assumptions {
			cz.Assumptions = append(cz.Assumptions, plantmpl.AssumptionResult{
				Text:     a.Text,
				Badge:    a.Status,
				Evidence: a.Evidence,
			})
		}

		for _, r := range plan.Critique.Risks {
			cz.RiskTable = append(cz.RiskTable, plantmpl.RiskRow{
				Risk:       r.Risk,
				Severity:   r.Severity,
				Mitigation: r.Mitigation,
			})
		}

		if plan.Critique.Synthesis != "" {
			cz.Synthesis = htmltemplate.HTML(html.EscapeString(plan.Critique.Synthesis)) //nolint:gosec
		}

		// Positional mapping: Critics[0] → first critic, Critics[1] → second critic.
		// Use reviewer names from YAML as titles, falling back to critic titles.
		if len(plan.Critique.Critics) > 0 {
			cz.GeminiCritique = renderCriticSectionHTML(plan.Critique.Critics[0])
			if len(plan.Critique.Reviewers) > 0 {
				cz.GeminiTitle = plan.Critique.Reviewers[0]
			} else {
				cz.GeminiTitle = plan.Critique.Critics[0].Title
			}
		}
		if len(plan.Critique.Critics) > 1 {
			cz.CopilotCritique = renderCriticSectionHTML(plan.Critique.Critics[1])
			if len(plan.Critique.Reviewers) > 1 {
				cz.CopilotTitle = plan.Critique.Reviewers[1]
			} else {
				cz.CopilotTitle = plan.Critique.Critics[1].Title
			}
		}

		page.Critique = cz
	}
}

// renderCriticSectionHTML converts a planyaml.CriticSection into safe HTML
// for embedding in the CritiqueZone template slots.
func renderCriticSectionHTML(c planyaml.CriticSection) htmltemplate.HTML {
	var b strings.Builder
	for _, sub := range c.Sections {
		b.WriteString("<h5>")
		b.WriteString(html.EscapeString(sub.Heading))
		b.WriteString("</h5>\n<ul>\n")
		for _, item := range sub.Items {
			b.WriteString(`  <li><span class="badge badge-`)
			b.WriteString(html.EscapeString(item.Kind))
			b.WriteString(`">`)
			b.WriteString(html.EscapeString(item.Badge))
			b.WriteString("</span> ")
			b.WriteString(html.EscapeString(item.Text))
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	return htmltemplate.HTML(b.String()) //nolint:gosec
}

// ---- plan render ------------------------------------------------------------

// planRenderCmd regenerates plan HTML from YAML using the dashboard template.
func planRenderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "render <plan-id>",
		Short: "Regenerate plan HTML from YAML source",
		Long: `Render plan HTML from YAML using the same template that powers the
dashboard plan detail view. The YAML file is the source of truth; the
HTML file is the rendered artifact.

Example:
  wipnote plan render plan-abc12345`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanRender(args[0])
		},
	}
}

func runPlanRender(planID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	return renderPlanToFile(wipnoteDir, planID)
}

// loadPlanEdges reads the graph edges currently recorded in a plan's HTML
// file, if any. Edges live only in the HTML <nav data-graph-edges> section —
// they are never part of the plan YAML body — so a YAML-driven re-render must
// snapshot them first or they are silently dropped (bug-b12b537b, GH#157).
// Returns nil (not an error) when the file doesn't exist yet or has no edges.
func loadPlanEdges(htmlPath string) map[string][]models.Edge {
	node, err := htmlparse.ParseFile(htmlPath)
	if err != nil {
		return nil
	}
	return node.Edges
}

// renderPlanCore regenerates plan HTML from the YAML source, preserving any
// graph edges already recorded in the existing HTML file across the rebuild
// (bug-b12b537b, GH#157). Shared by renderPlanToFileQuiet and renderPlanToFile.
func renderPlanCore(wipnoteDir, planID string) (*plantmpl.PlanPage, error) {
	page := plantmpl.BuildFromTopic(planID, "", "", time.Now().UTC().Format("2006-01-02"))
	enrichPageFromYAML(wipnoteDir, planID, page)

	if page.Title == "" {
		page.Title = planID
	}

	outPath := filepath.Join(wipnoteDir, "plans", planID+".html")

	// Snapshot existing edges BEFORE os.Create truncates the file. The
	// plantmpl template has no concept of graph edges, so without this the
	// rebuild below silently drops them.
	existingEdges := loadPlanEdges(outPath)

	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("create HTML file: %w", err)
	}
	renderErr := page.Render(f)
	closeErr := f.Close()
	if renderErr != nil {
		return nil, fmt.Errorf("render plan: %w", renderErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close HTML file: %w", closeErr)
	}

	if len(existingEdges) > 0 {
		if err := workitem.PatchGraphEdgesNav(outPath, existingEdges); err != nil {
			return nil, fmt.Errorf("restore graph edges: %w", err)
		}
	}

	return page, nil
}

// renderPlanToFileQuiet regenerates plan HTML from the YAML source without
// printing to stdout. Used by commitPlanChange to re-render before staging so
// the committed HTML is always in sync with the YAML (Fix 2 of bug-365a84d9).
func renderPlanToFileQuiet(wipnoteDir, planID string) error {
	_, err := renderPlanCore(wipnoteDir, planID)
	return err
}

// renderPlanToFile regenerates plan HTML from the YAML source using the
// dashboard template. Called by the CLI command and tests.
func renderPlanToFile(wipnoteDir, planID string) error {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		return fmt.Errorf("YAML source not found: %s\nRun 'wipnote plan generate <track-id>' to create a plan", yamlPath)
	}

	page, err := renderPlanCore(wipnoteDir, planID)
	if err != nil {
		return err
	}

	outPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	fmt.Printf("Rendered %s → %s (%d slices)\n", planID, outPath, len(page.Slices))
	return nil
}

// ---- helpers for finalize (slice parsing from node steps) --------------------

// parsePlanStepsAsSlices converts plan node steps into planSlice structs
// for the finalize workflow.
func parsePlanStepsAsSlices(node *models.Node) []planSlice {
	var slices []planSlice
	for i, step := range node.Steps {
		slices = append(slices, planSlice{
			num:   i + 1,
			name:  step.StepID,
			title: step.Description,
		})
	}
	return slices
}

// findPlanFile returns the path to a plan's HTML file, or empty string.
func findPlanFile(wipnoteDir, planID string) string {
	p := filepath.Join(wipnoteDir, "plans", planID+".html")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// updatePlanStatus sets meta.status in a plan's YAML source of truth.
// It does NOT touch the HTML file and does NOT commit — callers are
// responsible for re-rendering and committing via commitPlanChange.
func updatePlanStatus(wipnoteDir, planID, newStatus string) error {
	htmlPath := findPlanFile(wipnoteDir, planID)
	if htmlPath == "" {
		return fmt.Errorf("plan file not found: %s\nRun 'wipnote plan list' to see valid plan IDs", planID)
	}
	yamlPath := strings.TrimSuffix(htmlPath, ".html") + ".yaml"

	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return fmt.Errorf("load plan YAML: %w", err)
	}

	plan.Meta.Status = newStatus

	if err := planyaml.Save(yamlPath, plan); err != nil {
		return fmt.Errorf("save plan YAML: %w", err)
	}
	return nil
}
