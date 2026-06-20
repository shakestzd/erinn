package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/workitem"
	commandspkg "github.com/shakestzd/wipnote/internal/commands"
	"github.com/shakestzd/wipnote/plan/plantmpl"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

//go:embed templates/plan-template.html
var planTemplateFS embed.FS

var _ = planTemplateFS // referenced via go:embed; keep until plan template code is wired up

// planCmdWithExtras builds the standard workitem commands for plans,
// then replaces the generic create with the CRISPI-aware version and
// adds plan-specific subcommands.
func planCmdWithExtras() *cobra.Command {
	cmd := workitemCmd("plan", "plans")
	// Replace the generic create with CRISPI plan create.
	commandspkg.RemoveSubcommand(cmd, "create")
	// Replace the generic show with a plan-aware version that warns on YAML/HTML drift.
	commandspkg.RemoveSubcommand(cmd, "show")
	cmd.AddCommand(planShowCmd())
	cmd.AddCommand(planCreateFromTopicCmd())
	cmd.AddCommand(planGenerateCmd())
	cmd.AddCommand(planOpenCmd())
	cmd.AddCommand(planWaitCmd())
	cmd.AddCommand(planReadFeedbackCmd())
	cmd.AddCommand(planValidateCmd())
	cmd.AddCommand(planFinalizeFromYAMLCmd())
	cmd.AddCommand(planReopenCmd())
	cmd.AddCommand(planCritiqueCmd())
	cmd.AddCommand(planCreateYAMLCmd())
	cmd.AddCommand(planAddSliceYAMLCmd())
	cmd.AddCommand(planListYAMLCmd())
	cmd.AddCommand(planAddQuestionYAMLCmd())
	cmd.AddCommand(planSetCritiqueYAMLCmd())
	cmd.AddCommand(planValidateYAMLCmd())
	cmd.AddCommand(planSetDesignYAMLCmd())
	cmd.AddCommand(planReadFeedbackYAMLCmd())
	cmd.AddCommand(planFinalizeYAMLCmd())
	cmd.AddCommand(planWireCmd())
	cmd.AddCommand(planReviewCmd())
	cmd.AddCommand(planRewriteYAMLCmd())
	cmd.AddCommand(planRenderCmd())
	cmd.AddCommand(planFeedbackCmd())
	cmd.AddCommand(planSetStatusCmd())
	cmd.AddCommand(planMigrateOrphansCmd())
	// slice-4: slice lifecycle commands
	cmd.AddCommand(planApproveSliceCmd())
	cmd.AddCommand(planRejectSliceCmd())
	cmd.AddCommand(planAnswerSliceQuestionCmd())
	cmd.AddCommand(planSetSliceStatusCmd())
	// slice-5: incremental slice promotion
	cmd.AddCommand(planPromoteSliceCmd())
	// CRISPI: cross-harness decisions elicitation (feat-0fd7c8bc)
	cmd.AddCommand(planElicitDecisionsCmd())
	// Cross-harness web-form interview prototype (feat-2852d0c8)
	cmd.AddCommand(planInterviewCmd())
	cmd.AddCommand(planInterviewQuestionsCmd())
	// slice-6: visual-block catalog (native get-plan-blocks equivalent)
	cmd.AddCommand(planBlocksCmd())
	return cmd
}

// planGenerateCmd scaffolds a plan HTML file from a work item ID or topic title.
func planGenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "generate <work-item-id-or-topic>",
		Short: "Scaffold a plan HTML file from a work item or topic title",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanGenerate(args[0])
		},
	}
}

// isWorkItemPrefix returns true when id begins with a known work item prefix
// (trk-, feat-, bug-, spk-, pln-, spc-).
func isWorkItemPrefix(id string) bool {
	for _, p := range []string{"trk-", "feat-", "bug-", "spk-", "pln-", "spc-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// runPlanGenerate detects the argument type and routes to the appropriate mode:
//   - plan-*        → re-scaffold existing plan (not yet implemented)
//   - trk-*, feat-*, bug-*, spk-* → retroactive mode: scaffold from work item
//   - free text     → plan-first mode: create from topic title
func runPlanGenerate(sourceID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	planID, err := routePlanGenerateByArg(wipnoteDir, sourceID)
	if err != nil {
		return err
	}
	fmt.Println(filepath.Join(wipnoteDir, "plans", planID+".html"))
	return nil
}

// routePlanGenerateByArg contains the routing logic extracted from runPlanGenerate
// so it can be called from tests without needing a real .wipnote directory on
// the file system search path.
func routePlanGenerateByArg(wipnoteDir, sourceID string) (string, error) {
	switch {
	case strings.HasPrefix(sourceID, "plan-"):
		// Re-scaffold mode: regenerate CRISPI HTML from current plan data.
		return rescaffoldExistingPlan(wipnoteDir, sourceID)

	case isWorkItemPrefix(sourceID):
		// Retroactive mode: resolve then scaffold from the work item.
		planID, err := runPlanGenerateFromWorkItem(wipnoteDir, sourceID)
		if err != nil {
			return "", err
		}
		return planID, nil

	default:
		// Plan-first mode: treat the argument as a free-text topic title.
		return createPlanFromTopic(wipnoteDir, sourceID, "")
	}
}

// rescaffoldExistingPlan re-reads a plan node and regenerates the CRISPI
// template with all current data (title, description, slices, questions).
func rescaffoldExistingPlan(wipnoteDir, planID string) (string, error) {
	p, err := workitem.Open(wipnoteDir, agentForClaim())
	if err != nil {
		return "", fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	node, err := p.Plans.Get(planID)
	if err != nil {
		return "", fmt.Errorf("plan %q not found: %w", planID, err)
	}

	if err := scaffoldCRISPIPlanFromNode(wipnoteDir, node); err != nil {
		return "", fmt.Errorf("re-scaffold %s: %w", planID, err)
	}

	return planID, nil
}

// runPlanGenerateFromWorkItem scaffolds a plan from an existing work item.
// If a plan for sourceID already exists it returns the existing plan ID without
// creating a duplicate.
func runPlanGenerateFromWorkItem(wipnoteDir, sourceID string) (string, error) {
	resolved, err := resolveID(wipnoteDir, sourceID)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", sourceID, err)
	}
	nodePath := resolveNodePath(wipnoteDir, resolved)
	if nodePath == "" {
		return "", fmt.Errorf("work item %q not found\nRun 'wipnote wip' to see active items or 'wipnote find <query>' to search.", resolved)
	}

	// Check whether a plan already exists for this source ID.
	if existing := findExistingPlanForSource(wipnoteDir, resolved); existing != "" {
		return existing, nil
	}

	info, err := parseNodeForPlan(nodePath)
	if err != nil {
		return "", fmt.Errorf("parse work item: %w", err)
	}

	planID := workitem.GenerateID("plan", info.title)
	plansDir := filepath.Join(wipnoteDir, "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		return "", fmt.Errorf("create plans dir: %w", err)
	}

	date := time.Now().UTC().Format("2006-01-02")
	page := plantmpl.BuildFromWorkItem(planID, resolved, info.title, info.description, date)

	// Populate zones from the work item's features.
	slices, graph := buildTypedPlanSections(nodePath, wipnoteDir)
	page.Slices = slices
	page.Graph = graph

	designHTML := buildDesignContent(info, nodePath, wipnoteDir)
	if designHTML != "" {
		page.Design = &plantmpl.DesignSection{Content: template.HTML(designHTML)}
	}
	outlineHTML := buildOutlineContent(nodePath, wipnoteDir)
	if outlineHTML != "" {
		page.Outline = &plantmpl.OutlineSection{Content: template.HTML(outlineHTML)}
	}

	outPath := filepath.Join(plansDir, planID+".html")
	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("create plan file: %w", err)
	}
	defer f.Close()

	if err := page.Render(f); err != nil {
		return "", fmt.Errorf("render plan: %w", err)
	}

	return planID, nil
}

// findExistingPlanForSource scans the plans directory for any HTML file that
// references sourceID in its data-feature-id attribute. Returns the plan ID
// (stem of the filename) if found, or an empty string.
func findExistingPlanForSource(wipnoteDir, sourceID string) string {
	plansDir := filepath.Join(wipnoteDir, "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return ""
	}
	needle := fmt.Sprintf(`data-feature-id="%s"`, sourceID)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(plansDir, e.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), needle) {
			return strings.TrimSuffix(e.Name(), ".html")
		}
	}
	return ""
}

// planOpenCmd opens a plan in the browser.
func planOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <plan-id>",
		Short: "Open a plan in the browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanOpen(args[0])
		},
	}
}

func runPlanOpen(planID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	planPath := filepath.Join(wipnoteDir, "plans", planID+".html")
	if _, err := os.Stat(planPath); err != nil {
		return fmt.Errorf("plan %q not found at %s", planID, planPath)
	}

	if !isServerRunning("http://localhost:8080") {
		// Auto-start server so plan feedback API works.
		cmd := exec.Command(os.Args[0], "serve", "-p", "8080")
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Start()
		time.Sleep(500 * time.Millisecond)
	}

	url := "http://localhost:8080/plans/" + planID + ".html"
	return openBrowser(url)
}

// planWaitCmd blocks until a plan is finalized.
func planWaitCmd() *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "wait <plan-id>",
		Short: "Block until a plan is finalized",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanWait(args[0], timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", time.Hour, "Maximum wait time (e.g. 30m, 1h)")
	return cmd
}

func runPlanWait(planID string, timeout time.Duration) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("Waiting for plan %s to be finalized", planID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return fmt.Errorf("timeout: plan %s was not finalized within %s", planID, timeout)
		case <-ticker.C:
			finalized, err := checkPlanFinalized(wipnoteDir, planID)
			if err != nil {
				fmt.Print(".")
				continue
			}
			if finalized {
				fmt.Println("\nPlan finalized.")
				return nil
			}
			fmt.Print(".")
		}
	}
}

// checkPlanFinalized returns true when the plan's status is "finalized".
// Prefers the live API; falls back to reading the HTML file directly.
func checkPlanFinalized(wipnoteDir, planID string) (bool, error) {
	if isServerRunning("http://localhost:8080") {
		status, err := fetchPlanStatusFromAPI(planID)
		if err == nil {
			return status == "finalized", nil
		}
	}

	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return false, fmt.Errorf("load plan YAML: %w", err)
	}
	return plan.Meta.Status == "finalized", nil
}

// fetchPlanStatusFromAPI calls GET /api/plans/{id}/status and returns the status.
func fetchPlanStatusFromAPI(planID string) (string, error) {
	url := "http://localhost:8080/api/plans/" + planID + "/status"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url) //nolint:gosec,noctx
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned %d", resp.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Status, nil
}

// ---- browser / server helpers -----------------------------------------------

// isServerRunning returns true when a GET to baseURL succeeds within 500ms.
func isServerRunning(baseURL string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(baseURL) //nolint:gosec,noctx
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// openBrowser opens the given URL or file path in the default OS browser.
func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	default:
		fmt.Println(target)
		return nil
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
