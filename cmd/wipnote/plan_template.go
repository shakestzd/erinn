package main

import (
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/shakestzd/wipnote/core/htmlparse"
)

// ---- plan template types & helpers -----------------------------------------

type planNodeInfo struct {
	title       string
	description string
}

// parseNodeForPlan reads a work item HTML file and returns its title and description.
func parseNodeForPlan(nodePath string) (planNodeInfo, error) {
	data, err := os.ReadFile(nodePath)
	if err != nil {
		return planNodeInfo{}, err
	}
	return extractPlanNodeInfo(string(data)), nil
}

// extractPlanNodeInfo extracts title and description from raw HTML using
// simple string scanning — keeps this file free of goquery import.
func extractPlanNodeInfo(rawHTML string) planNodeInfo {
	info := planNodeInfo{}

	if _, rest, ok := strings.Cut(rawHTML, "<h1>"); ok {
		if before, _, ok := strings.Cut(rest, "</h1>"); ok {
			info.title = strings.TrimSpace(before)
		}
	}

	// Try data-section="description" first, fall back to first <p> after </header>.
	if s := strings.Index(rawHTML, `data-section="description"`); s >= 0 {
		rest := rawHTML[s:]
		if p := strings.Index(rest, "<p>"); p >= 0 {
			rest2 := rest[p+3:]
			if e := strings.Index(rest2, "</p>"); e >= 0 {
				info.description = strings.TrimSpace(rest2[:e])
			}
		}
	} else if headerEnd := strings.Index(rawHTML, "</header>"); headerEnd >= 0 {
		rest := rawHTML[headerEnd:]
		pIdx := strings.Index(rest, "<p>")
		navIdx := strings.Index(rest, "<nav")
		if pIdx >= 0 && (navIdx < 0 || pIdx < navIdx) {
			rest2 := rest[pIdx+3:]
			if e := strings.Index(rest2, "</p>"); e >= 0 {
				info.description = strings.TrimSpace(rest2[:e])
			}
		}
	}

	return info
}

// buildDesignContent generates the Design Discussion section from the source
// work item's description and a summary of contained features.
// The <!--PLAN_DESIGN_CONTENT--> marker appears first so that manually-set
// content (via plan set-section) renders above the auto-generated scope.
// The feature list is wrapped in a collapsible <details> grouped by status.
func buildDesignContent(info planNodeInfo, nodePath, wipnoteDir string) string {
	var b strings.Builder
	if info.description != "" {
		fmt.Fprintf(&b, "    <p>%s</p>\n", html.EscapeString(info.description))
	}

	// Marker goes first — manual content injected here appears above the scope.
	b.WriteString("    <!--PLAN_DESIGN_CONTENT-->\n")

	node, err := htmlparse.ParseFile(nodePath)
	if err != nil || len(node.Edges["contains"]) == 0 {
		return b.String()
	}

	// Classify features by status.
	type scopeItem struct {
		title, desc, status string
	}
	var done, todo []scopeItem
	for _, edge := range node.Edges["contains"] {
		title := strings.TrimSpace(edge.Title)
		if title == "" {
			title = edge.TargetID
		}
		// Skip plan features (meta-noise).
		if strings.HasPrefix(title, "Plan:") || strings.HasPrefix(edge.TargetID, "plan-") {
			continue
		}

		item := scopeItem{title: title, status: "todo"}
		if childPath := resolveNodePath(wipnoteDir, edge.TargetID); childPath != "" {
			if child, err := htmlparse.ParseFile(childPath); err == nil {
				item.status = string(child.Status)
				if child.Content != "" {
					desc := strings.ReplaceAll(child.Content, "<p>", "")
					desc = strings.ReplaceAll(desc, "</p>", "")
					desc = strings.TrimSpace(desc)
					if len(desc) > 120 {
						desc = desc[:117] + "..."
					}
					item.desc = desc
				}
			}
		}
		if item.status == "done" {
			done = append(done, item)
		} else {
			todo = append(todo, item)
		}
	}

	total := len(done) + len(todo)
	fmt.Fprintf(&b,
		"    <details style=\"margin-top:12px\"><summary style=\"cursor:pointer;font-size:.85rem;color:var(--text-dim)\">"+
			"Track Features (%d total, %d done, %d remaining)</summary>\n",
		total, len(done), len(todo))

	if len(todo) > 0 {
		b.WriteString("    <h4 style=\"margin-top:8px\">Remaining</h4>\n    <ul>\n")
		for _, it := range todo {
			if it.desc != "" {
				fmt.Fprintf(&b, "      <li><strong>%s</strong> &mdash; %s</li>\n",
					html.EscapeString(it.title), html.EscapeString(it.desc))
			} else {
				fmt.Fprintf(&b, "      <li>%s</li>\n", html.EscapeString(it.title))
			}
		}
		b.WriteString("    </ul>\n")
	}
	if len(done) > 0 {
		b.WriteString("    <h4 style=\"margin-top:8px\">Completed</h4>\n    <ul style=\"color:var(--text-muted)\">\n")
		for _, it := range done {
			fmt.Fprintf(&b, "      <li>&#10003; %s</li>\n", html.EscapeString(it.title))
		}
		b.WriteString("    </ul>\n")
	}

	b.WriteString("    </details>\n")
	return b.String()
}

// buildOutlineContent generates the Structure Outline section showing
// the dependency chain and execution order.
func buildOutlineContent(nodePath, wipnoteDir string) string {
	node, err := htmlparse.ParseFile(nodePath)
	if err != nil || len(node.Edges["contains"]) == 0 {
		return ""
	}

	// Build ID → title map and find dependencies.
	type item struct {
		id, title string
		deps      []string
	}
	var items []item
	for _, edge := range node.Edges["contains"] {
		title := edge.Title
		if title == "" {
			title = edge.TargetID
		}
		it := item{id: edge.TargetID, title: title}
		if childPath := resolveNodePath(wipnoteDir, edge.TargetID); childPath != "" {
			if child, err := htmlparse.ParseFile(childPath); err == nil {
				for _, dep := range child.Edges["blocked_by"] {
					it.deps = append(it.deps, dep.TargetID)
				}
			}
		}
		items = append(items, it)
	}

	// Separate into independent (no deps) and dependent.
	var independent, dependent []item
	for _, it := range items {
		if len(it.deps) == 0 {
			independent = append(independent, it)
		} else {
			dependent = append(dependent, it)
		}
	}

	var b strings.Builder
	if len(independent) > 0 {
		b.WriteString("    <h4>Independent (can run in parallel)</h4>\n    <ul>\n")
		for _, it := range independent {
			fmt.Fprintf(&b, "      <li>%s</li>\n", html.EscapeString(it.title))
		}
		b.WriteString("    </ul>\n")
	}
	if len(dependent) > 0 {
		idToTitle := make(map[string]string, len(items))
		for _, it := range items {
			idToTitle[it.id] = it.title
		}
		b.WriteString("    <h4>Sequential (has dependencies)</h4>\n    <ul>\n")
		for _, it := range dependent {
			var depNames []string
			for _, d := range it.deps {
				if name, ok := idToTitle[d]; ok {
					depNames = append(depNames, name)
				}
			}
			fmt.Fprintf(&b, "      <li>%s &larr; depends on: %s</li>\n",
				html.EscapeString(it.title), html.EscapeString(strings.Join(depNames, ", ")))
		}
		b.WriteString("    </ul>\n")
	}
	return b.String()
}
