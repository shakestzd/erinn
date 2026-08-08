package workitem

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
)

// PlanOption configures a new plan during creation.
type PlanOption func(*planConfig)

type planConfig struct {
	priority string
	status   string
	trackID  string
	steps    []string
	content  string
}

// PlanWithPriority sets the plan's priority.
func PlanWithPriority(p string) PlanOption {
	return func(c *planConfig) { c.priority = p }
}

// PlanWithTrack links the plan to a track.
func PlanWithTrack(trackID string) PlanOption {
	return func(c *planConfig) { c.trackID = trackID }
}

// PlanWithSteps adds implementation steps.
func PlanWithSteps(steps ...string) PlanOption {
	return func(c *planConfig) { c.steps = steps }
}

// PlanWithContent sets the description body.
func PlanWithContent(content string) PlanOption {
	return func(c *planConfig) { c.content = content }
}

// PlanCollection provides CRUD operations for plans.
type PlanCollection struct {
	*Collection
}

// NewPlanCollection creates a PlanCollection bound to the given Base.
func NewPlanCollection(base *Base) *PlanCollection {
	return &PlanCollection{Collection: newCollection(base, "plans", "plan")}
}

// Create builds a new plan node, writes HTML, and optionally inserts into SQLite.
func (pc *PlanCollection) Create(title string, opts ...PlanOption) (*models.Node, error) {
	if title == "" {
		return nil, fmt.Errorf("plan title must not be empty")
	}

	cfg := &planConfig{priority: "medium", status: "todo"}
	for _, opt := range opts {
		opt(cfg)
	}

	now := time.Now().UTC()
	id := GenerateID("plan", title)

	var steps []models.Step
	for i, desc := range cfg.steps {
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", id, i),
			Description: desc,
		})
	}

	node := &models.Node{
		ID:            id,
		Title:         title,
		Type:          "plan",
		Status:        models.NodeStatus(cfg.status),
		Priority:      models.Priority(cfg.priority),
		CreatedAt:     now,
		UpdatedAt:     now,
		AgentAssigned: pc.base.Agent,
		TrackID:       cfg.trackID,
		Steps:         steps,
		Content:       cfg.content,
	}

	if _, err := pc.writeNode(node); err != nil {
		return nil, fmt.Errorf("create plan: %w", err)
	}

	if pc.base.DB != nil {
		dbFeat := &dbpkg.Feature{
			ID:         id,
			Type:       "plan",
			Title:      title,
			Status:     cfg.status,
			Priority:   cfg.priority,
			AssignedTo: pc.base.Agent,
			TrackID:    cfg.trackID,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		// UpsertFeature overwrites any placeholder row from ensureFeatureRow (bug-7f4a1a9c).
		_ = dbpkg.UpsertFeature(pc.base.DB, dbFeat)
	}

	return node, nil
}

// writeNode overrides Collection.writeNode for plans: if the file already
// exists and is a CRISPI interactive plan (detected by data-zone= attribute),
// only the data-status attribute and <nav data-graph-edges> section are
// updated. All other content (design discussion, slices, JS, CSS) is preserved.
// For new plans or generic plan nodes, the standard WriteNodeHTML path is used.
func (pc *PlanCollection) writeNode(node *models.Node) (string, error) {
	path := filepath.Join(pc.Dir(), node.ID+".html")
	if isCRISPIPlanFile(path) {
		return path, patchPlanHTML(path, node)
	}
	return WriteNodeHTML(pc.Dir(), node)
}

// writeNodeUnlocked writes without acquiring the per-item lock. Callers must
// hold LockFeatureForWrite for the target path before calling.
func (pc *PlanCollection) writeNodeUnlocked(node *models.Node) (string, error) {
	path := filepath.Join(pc.Dir(), node.ID+".html")
	if isCRISPIPlanFile(path) {
		return path, patchPlanHTML(path, node)
	}
	return writeNodeHTMLUnlocked(pc.Dir(), node)
}

// AddEdge overrides Collection.AddEdge for plans to preserve CRISPI HTML.
func (pc *PlanCollection) AddEdge(id string, e models.Edge) (*models.Node, error) {
	path := filepath.Join(pc.Dir(), id+".html")
	release := LockFeatureForWrite(path)
	defer release()

	node, err := pc.Get(id)
	if err != nil {
		return nil, fmt.Errorf("add edge %s: %w", id, err)
	}
	node.AddEdge(e)
	if _, err := pc.writeNodeUnlocked(node); err != nil {
		return nil, fmt.Errorf("add edge %s: %w", id, err)
	}

	if pc.base.DB != nil {
		edgeID := fmt.Sprintf("%s-%s-%s", id, string(e.Relationship), e.TargetID)
		_ = dbpkg.InsertEdge(
			pc.base.DB,
			edgeID, id, "plan",
			e.TargetID, inferNodeType(e.TargetID),
			string(e.Relationship),
			e.Properties,
		)
	}

	return node, nil
}

// RemoveEdge overrides Collection.RemoveEdge for plans to preserve CRISPI HTML.
func (pc *PlanCollection) RemoveEdge(id, targetID string, relType models.RelationshipType) (*models.Node, bool, error) {
	path := filepath.Join(pc.Dir(), id+".html")
	release := LockFeatureForWrite(path)
	defer release()

	node, err := pc.Get(id)
	if err != nil {
		return nil, false, fmt.Errorf("remove edge %s: %w", id, err)
	}
	removed := node.RemoveEdge(targetID, relType)
	if _, err := pc.writeNodeUnlocked(node); err != nil {
		return nil, false, fmt.Errorf("remove edge %s: %w", id, err)
	}

	if removed && pc.base.DB != nil {
		_ = dbpkg.DeleteEdge(pc.base.DB, id, targetID, string(relType))
	}

	return node, removed, nil
}

// Start overrides Collection.Start for plans to preserve CRISPI HTML.
func (pc *PlanCollection) Start(id string) (*models.Node, error) {
	path := filepath.Join(pc.Dir(), id+".html")
	release := LockFeatureForWrite(path)
	defer release()

	node, err := pc.Get(id)
	if err != nil {
		return nil, err
	}
	node.Status = models.StatusInProgress
	node.AgentAssigned = pc.base.Agent
	node.UpdatedAt = time.Now().UTC()
	if _, err := pc.writeNodeUnlocked(node); err != nil {
		return nil, err
	}
	if pc.base.DB != nil {
		_ = dbpkg.UpdateFeatureStatus(pc.base.DB, id, "in-progress")
	}
	return node, nil
}

// Complete overrides Collection.Complete for plans to preserve CRISPI HTML.
func (pc *PlanCollection) Complete(id string) (*models.Node, error) {
	path := filepath.Join(pc.Dir(), id+".html")
	release := LockFeatureForWrite(path)
	defer release()

	node, err := pc.Get(id)
	if err != nil {
		return nil, err
	}
	for i := range node.Steps {
		if !node.Steps[i].Completed {
			node.Steps[i].Completed = true
			node.Steps[i].Agent = pc.base.Agent
			node.Steps[i].Timestamp = time.Now().UTC()
		}
	}
	node.Status = models.StatusDone
	node.UpdatedAt = time.Now().UTC()
	if _, err := pc.writeNodeUnlocked(node); err != nil {
		return nil, err
	}
	if pc.base.DB != nil {
		_ = dbpkg.UpdateFeatureStatus(pc.base.DB, id, "done")
	}
	return node, nil
}

// Get overrides Collection.Get for plans: after the standard parse it
// synthesizes steps from CRISPI slice cards when node.Steps is empty.
// CRISPI HTML doesn't use section[data-steps], so htmlparse returns no
// steps for CRISPI files. This override recovers the slice list from the
// slice-title elements so callers (critique, finalize, validate) work correctly.
func (pc *PlanCollection) Get(id string) (*models.Node, error) {
	node, err := pc.Collection.Get(id)
	if err != nil {
		return nil, err
	}
	if len(node.Steps) == 0 {
		node.Steps = parseCRISPISteps(filepath.Join(pc.Dir(), id+".html"), id)
	}
	return node, nil
}

// parseCRISPISteps reads slice titles from a CRISPI plan HTML file and
// returns them as Step values. Returns nil if the file is not a CRISPI file
// or has no slice cards. Supports both the current format
// (<span class="slice-name">) and the legacy format (<strong class="slice-title">).
func parseCRISPISteps(path, nodeID string) []models.Step {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)

	// Try current format first, fall back to legacy.
	steps := extractSliceTitles(content, nodeID, `<span class="slice-name">`, `</span>`)
	if len(steps) == 0 {
		steps = extractSliceTitles(content, nodeID, `<strong class="slice-title">`, `</strong>`)
	}
	return steps
}

// extractSliceTitles scans content for slice titles delimited by openTag/closeTag.
func extractSliceTitles(content, nodeID, openTag, closeTag string) []models.Step {
	var steps []models.Step
	i := 0
	for {
		start := strings.Index(content[i:], openTag)
		if start < 0 {
			break
		}
		start += i + len(openTag)
		end := strings.Index(content[start:], closeTag)
		if end < 0 {
			break
		}
		title := content[start : start+end]
		stepIdx := len(steps)
		steps = append(steps, models.Step{
			StepID:      fmt.Sprintf("step-%s-%d", nodeID, stepIdx),
			Description: title,
		})
		i = start + end + len(closeTag)
	}
	return steps
}

// richPlanThreshold is the minimum byte size below which a file cannot
// contain substantial plan content. A minimal WriteNodeHTML shell is
// roughly 1-2 KB; real CRISPI plans run several hundred KB.
const richPlanThreshold = 8 * 1024 // 8 KiB

// richPlanMarkers are content patterns that definitively identify a file as
// a rich/CRISPI plan that must never be overwritten by the minimal
// WriteNodeHTML path. Any one match is sufficient.
var richPlanMarkers = []string{
	`data-zone=`,         // CRISPI zone sections
	`class="slice-card"`, // CRISPI slice cards (current format)
	`data-slice=`,        // CRISPI slice data attributes
	`data-approval=`,     // CRISPI approval attributes
}

// isCRISPIPlanFile returns true when the file at path is an existing rich or
// CRISPI interactive plan that must be protected from full regeneration.
// Detection looks for any of several canonical markers AND also triggers on
// files that exceed a content-size threshold, catching rich plan docs that may
// be missing a single marker token.
func isCRISPIPlanFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false // file doesn't exist — use standard WriteNodeHTML
	}
	content := string(data)
	for _, marker := range richPlanMarkers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	// Treat any substantially-sized plan file as protected even if none of
	// the above markers matched (e.g., a bespoke hand-authored plan).
	return len(data) >= richPlanThreshold
}

// patchPlanHTML surgically updates only the data-status attribute on the
// <article> element and the <nav data-graph-edges> section of a CRISPI plan
// file. All other content (design sections, slices, JS, CSS) is preserved.
func patchPlanHTML(path string, node *models.Node) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("patch plan html read %s: %w", path, err)
	}
	content := string(data)

	// --- 1. Update data-status on the <article> element ---
	content = patchDataStatus(content, string(node.Status))

	// --- 2. Replace the <nav data-graph-edges> section ---
	content = patchEdgesNav(content, node)

	return os.WriteFile(path, []byte(content), 0o644)
}

// patchDataStatus replaces data-status="..." in the <article> element.
// It replaces only the first occurrence so we don't touch <details data-status>.
func patchDataStatus(content, newStatus string) string {
	const articlePrefix = `<article `
	articleIdx := strings.Index(content, articlePrefix)
	if articleIdx < 0 {
		return content
	}
	// Find the end of the <article ...> opening tag.
	tagEnd := strings.Index(content[articleIdx:], ">")
	if tagEnd < 0 {
		return content
	}
	openTag := content[articleIdx : articleIdx+tagEnd+1]

	// Replace data-status="..." within the opening tag only.
	for _, old := range []string{"todo", "draft", "in-progress", "done", "finalized"} {
		oldAttr := fmt.Sprintf(`data-status="%s"`, old)
		if strings.Contains(openTag, oldAttr) {
			newAttr := fmt.Sprintf(`data-status="%s"`, newStatus)
			newTag := strings.Replace(openTag, oldAttr, newAttr, 1)
			return content[:articleIdx] + newTag + content[articleIdx+tagEnd+1:]
		}
	}
	return content
}

// patchEdgesNav replaces or inserts the <nav data-graph-edges> section.
// If the section already exists it is replaced. If it doesn't exist but
// </article> is present, the section is inserted before </article>.
func patchEdgesNav(content string, node *models.Node) string {
	newNav := buildEdgesNavHTML(node)

	const navOpen = `<nav data-graph-edges>`
	const navClose = `</nav>`
	const articleClose = `</article>`

	startIdx := strings.Index(content, navOpen)
	if startIdx >= 0 {
		// Find the matching </nav> after the opening tag.
		closeIdx := strings.Index(content[startIdx:], navClose)
		if closeIdx >= 0 {
			absEnd := startIdx + closeIdx + len(navClose)
			return content[:startIdx] + newNav + content[absEnd:]
		}
	}

	// No existing nav — insert before </article>.
	closeArticle := strings.LastIndex(content, articleClose)
	if closeArticle >= 0 {
		insert := "\n" + newNav + "\n"
		return content[:closeArticle] + insert + content[closeArticle:]
	}

	return content
}

// PatchGraphEdgesNav rewrites the <nav data-graph-edges> section of the HTML
// file at path so it reflects edges, leaving everything else in the file
// untouched. Exported for render pipelines outside this package that don't
// go through PlanCollection.writeNode (e.g. the YAML-driven plan re-render in
// cmd/wipnote, which fully regenerates the HTML from the plantmpl template and
// has no concept of graph edges). Without this, edges — which live only in the
// HTML and are never part of the plan YAML body — are silently dropped on
// every re-render (bug-b12b537b, GH#157).
func PatchGraphEdgesNav(path string, edges map[string][]models.Edge) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("patch graph edges nav read %s: %w", path, err)
	}
	content := patchEdgesNav(string(data), &models.Node{Edges: edges})
	return os.WriteFile(path, []byte(content), 0o644)
}

// buildEdgesNavHTML renders a <nav data-graph-edges> block from a node's edges.
// Returns an empty string if the node has no edges.
func buildEdgesNavHTML(node *models.Node) string {
	if len(node.Edges) == 0 {
		return ""
	}

	// Sorted for the same reason as buildEdgeGroups: map order would reshuffle
	// the nav on every rewrite of an unchanged plan.
	relTypes := make([]string, 0, len(node.Edges))
	for relType := range node.Edges {
		relTypes = append(relTypes, relType)
	}
	sort.Strings(relTypes)

	var sb strings.Builder
	sb.WriteString("<nav data-graph-edges>\n")
	for _, relType := range relTypes {
		edges := node.Edges[relType]
		if len(edges) == 0 {
			continue
		}
		relLabel := titleCase(strings.ReplaceAll(relType, "_", " "))
		sb.WriteString(fmt.Sprintf("  <section data-edge-type=%q>\n", relType))
		sb.WriteString(fmt.Sprintf("    <h3>%s:</h3>\n", relLabel))
		sb.WriteString("    <ul>\n")
		for _, e := range edges {
			label := e.Title
			if label == "" {
				label = e.TargetID
			}
			since := ""
			if !e.Since.IsZero() {
				since = fmt.Sprintf(` data-since=%q`, fmtTime(e.Since))
			}
			// Plan HTML is patched rather than re-rendered from the template,
			// so it needs the same property markup emitted by hand — a plan's
			// own edges must round-trip through a rebuild like any other
			// node's (bug-eb141e88).
			sb.WriteString(fmt.Sprintf(
				"      <li><a href=%q data-relationship=%q%s%s>%s</a></li>\n",
				e.TargetID+".html", string(e.Relationship), since,
				string(edgePropAttrs(e.Properties)), label,
			))
		}
		sb.WriteString("    </ul>\n")
		sb.WriteString("  </section>\n")
	}
	sb.WriteString("</nav>")
	return sb.String()
}
