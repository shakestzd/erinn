// Package htmlparse reads wipnote HTML work item files into Go structs.
//
// Each .wipnote/features/*.html file follows a well-defined structure:
//
//	<article id="feat-xxx" data-type="feature" data-status="todo" ...>
//	  <header><h1>Title</h1></header>
//	  <nav data-graph-edges>
//	    <section data-edge-type="blocks"><ul><li><a href="...">...</a></li></ul></section>
//	  </nav>
//	  <section data-steps><ol><li data-completed="true">...</li></ol></section>
//	  <section data-content><p>Body text</p></section>
//	</article>
//
// Edge anchors may additionally carry edge properties; see edge_props.go for
// that format.
package htmlparse

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/shakestzd/wipnote/core/models"
)

var emojiPrefix = regexp.MustCompile(`^[\x{2705}\x{23F3}\x{274C}\x{1F504}]\s*`)

// ParseFile reads an HTML work item file and returns a Node.
func ParseFile(path string) (*models.Node, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, fmt.Errorf("parse HTML %s: %w", path, err)
	}

	return parseDocument(doc)
}

// ParseString parses an HTML string and returns a Node.
func ParseString(html string) (*models.Node, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse HTML string: %w", err)
	}
	return parseDocument(doc)
}

func parseDocument(doc *goquery.Document) (*models.Node, error) {
	article := doc.Find("article[id]").First()
	if article.Length() == 0 {
		return nil, fmt.Errorf("no <article id=...> found")
	}

	node := &models.Node{}

	// Core attributes
	node.ID, _ = article.Attr("id")
	node.Type = attrOr(article, "data-type", "node")
	node.Status = models.NodeStatus(attrOr(article, "data-status", "todo"))
	node.Priority = models.Priority(attrOr(article, "data-priority", "medium"))
	node.AgentAssigned = attrOr(article, "data-agent-assigned", "")
	node.TrackID = attrOr(article, "data-track-id", "")
	node.PlanTaskID = attrOr(article, "data-plan-task-id", "")
	node.SpikeSubtype = attrOr(article, "data-spike-subtype", "")
	node.ClaimedAt = attrOr(article, "data-claimed-at", "")
	node.ClaimedBySession = attrOr(article, "data-claimed-by-session", "")

	// Provenance attributes (added in feat-40ef1333). Pre-existing items
	// without these attributes round-trip as empty strings.
	node.CreatedByAgent = attrOr(article, "data-created-by-agent", "")
	node.CreatedByModel = attrOr(article, "data-created-by-model", "")
	node.CreatedByRole = attrOr(article, "data-created-by-role", "")
	node.CreatedByCLIVersion = attrOr(article, "data-created-by-cli-version", "")

	// Timestamps
	node.CreatedAt = parseTime(attrOr(article, "data-created", ""))
	node.UpdatedAt = parseTime(attrOr(article, "data-updated", ""))

	// Title from <h1>
	h1 := article.Find("header h1").First()
	if h1.Length() > 0 {
		node.Title = strings.TrimSpace(h1.Text())
	}
	if node.Title == "" {
		node.Title = strings.TrimSpace(doc.Find("title").First().Text())
	}

	// Edges
	node.Edges = parseEdges(doc)

	// Steps
	node.Steps = parseSteps(doc)

	// Content
	node.Content = parseContent(doc)

	// Properties — arbitrary key/value metadata set via EditBuilder.SetProperty
	// (e.g. standalone_reason, created_in_session). See node_props.go for the
	// wire format. Absent on all pre-existing HTML, which parses as no
	// properties.
	node.Properties = parseNodeProps(article)

	return node, nil
}

func parseEdges(doc *goquery.Document) map[string][]models.Edge {
	edges := make(map[string][]models.Edge)

	doc.Find("nav[data-graph-edges] section[data-edge-type]").Each(func(_ int, sec *goquery.Selection) {
		relType, _ := sec.Attr("data-edge-type")
		if relType == "" {
			relType = "related"
		}

		sec.Find("a[href]").Each(func(_ int, link *goquery.Selection) {
			href, _ := link.Attr("href")
			targetID := href
			targetID = strings.TrimSuffix(targetID, ".html")
			if idx := strings.LastIndex(targetID, "/"); idx >= 0 {
				targetID = targetID[idx+1:]
			}

			edge := models.Edge{
				TargetID:     targetID,
				Relationship: models.RelationshipType(attrOr(link, "data-relationship", relType)),
				Title:        strings.TrimSpace(link.Text()),
			}

			if since := attrOr(link, "data-since", ""); since != "" {
				edge.Since = parseTime(since)
			}

			// Edge properties — see edge_props.go for the wire format.
			// Absent on all pre-existing HTML, which parses as no properties.
			edge.Properties = parseEdgeProps(link)

			edges[relType] = append(edges[relType], edge)
		})
	})

	return edges
}

func parseSteps(doc *goquery.Document) []models.Step {
	var steps []models.Step

	doc.Find("section[data-steps] ol li").Each(func(_ int, li *goquery.Selection) {
		completed := strings.EqualFold(attrOr(li, "data-completed", "false"), "true")
		agent := attrOr(li, "data-agent", "")
		stepID := attrOr(li, "data-step-id", "")

		text := strings.TrimSpace(li.Text())
		text = emojiPrefix.ReplaceAllString(text, "")

		var dependsOn []string
		if raw := attrOr(li, "data-depends-on", ""); raw != "" {
			for _, d := range strings.Split(raw, ",") {
				d = strings.TrimSpace(d)
				if d != "" {
					dependsOn = append(dependsOn, d)
				}
			}
		}

		// Provenance — attribute set added in feat-40ef1333. Step.Agent is
		// the harness identity and doubles as data-created-by-agent on write.
		createdByModel := attrOr(li, "data-created-by-model", "")
		createdByRole := attrOr(li, "data-created-by-role", "")
		createdByCLIVersion := attrOr(li, "data-created-by-cli-version", "")

		steps = append(steps, models.Step{
			StepID:              stepID,
			Description:         text,
			Completed:           completed,
			Agent:               agent,
			DependsOn:           dependsOn,
			CreatedByModel:      createdByModel,
			CreatedByRole:       createdByRole,
			CreatedByCLIVersion: createdByCLIVersion,
		})
	})

	return steps
}

func parseContent(doc *goquery.Document) string {
	// Try section[data-content] first.
	sec := doc.Find("section[data-content]").First()
	if sec.Length() > 0 {
		var parts []string
		sec.Children().Each(func(_ int, child *goquery.Selection) {
			if goquery.NodeName(child) == "h3" {
				return
			}
			text := strings.TrimSpace(child.Text())
			if text != "" {
				parts = append(parts, text)
			}
		})
		if result := strings.Join(parts, "\n"); result != "" {
			return result
		}
	}

	// Fall back to section[data-findings] .findings-content (spike files).
	findingsEl := doc.Find("section[data-findings] .findings-content").First()
	if findingsEl.Length() > 0 {
		if text := strings.TrimSpace(findingsEl.Text()); text != "" {
			return text
		}
	}

	return ""
}

// attrOr returns the named attribute value, or fallback if absent/empty.
func attrOr(sel *goquery.Selection, name, fallback string) string {
	if v, ok := sel.Attr(name); ok && v != "" {
		return v
	}
	return fallback
}

// parseTime attempts to parse an ISO-8601 timestamp.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Normalise "Z" suffix for Go parsing.
	s = strings.Replace(s, "Z", "+00:00", 1)

	// Try layouts in order of likelihood.
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
