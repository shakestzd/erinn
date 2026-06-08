package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

func planShowCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show <plan-id>",
		Short: "Show plan details (warns on YAML/HTML drift)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runPlanShowWithFormat(args[0], format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: json or text")
	return cmd
}

// runPlanShowWithFormat shows a plan in the requested format (text or json),
// checking for YAML/HTML drift in text mode.
func runPlanShowWithFormat(rawID, format string) error {
	if format != "json" {
		// Only check drift for human-readable output (avoids confusing stderr noise in JSON mode).
		if wipnoteDir, err := findWipnoteDir(); err == nil {
			resolved, err := resolveID(wipnoteDir, rawID)
			if err == nil && strings.HasPrefix(resolved, "plan-") {
				yamlPath := filepath.Join(wipnoteDir, "plans", resolved+".yaml")
				htmlPath := filepath.Join(wipnoteDir, "plans", resolved+".html")
				checkPlanDrift(yamlPath, htmlPath, os.Stderr)
			}
		}
	}
	return runWiShowWithFormat(rawID, format)
}

var (
	planArticleStatusRe = regexp.MustCompile(`<article[^>]*id="(plan-[^"]+)"[^>]*data-status="([^"]+)"`)
	planTitleRe         = regexp.MustCompile(`(?s)<title>\s*Plan:\s*(.*?)\s*</title>`)
	planSliceRe         = regexp.MustCompile(`data-slice="\d+"`)
)

// checkPlanDrift compares plan YAML against its rendered HTML and writes
// a warning line for each drifted field. Non-fatal; silent if files are
// missing or unreadable (drift-check is best-effort).
func checkPlanDrift(yamlPath, htmlPath string, w io.Writer) {
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return
	}
	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		return
	}
	html := string(htmlBytes)
	planID := plan.Meta.ID

	if m := planArticleStatusRe.FindStringSubmatch(html); len(m) == 3 {
		if m[2] != plan.Meta.Status {
			fmt.Fprintf(w, "warning: %s YAML/HTML drift — status: yaml=%q html=%q. HTML is stale; re-render with 'wipnote plan generate %s'.\n",
				planID, plan.Meta.Status, m[2], planID)
		}
	}

	if m := planTitleRe.FindStringSubmatch(html); len(m) == 2 {
		htmlTitle := strings.TrimSpace(m[1])
		if htmlTitle != plan.Meta.Title {
			fmt.Fprintf(w, "warning: %s YAML/HTML drift — title: yaml=%q html=%q. HTML is stale; re-render with 'wipnote plan generate %s'.\n",
				planID, plan.Meta.Title, htmlTitle, planID)
		}
	}

	htmlSliceCount := len(planSliceRe.FindAllString(html, -1))
	yamlSliceCount := len(plan.Slices)
	if htmlSliceCount != yamlSliceCount {
		fmt.Fprintf(w, "warning: %s YAML/HTML drift — slice count: yaml=%d html=%d. HTML is stale; re-render with 'wipnote plan generate %s'.\n",
			planID, yamlSliceCount, htmlSliceCount, planID)
	}
}
