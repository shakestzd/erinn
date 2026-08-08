package workitem

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/paths"
)

// nodeWriteMu serialises concurrent writes that touch the same work item HTML
// file in a single process. Keyed by node path (string) → *sync.Mutex.
// This prevents lost-update races between writers — canonical work item
// mutations, `compliance auto`'s findings writer, and `spec generate --insert`'s
// spec writer all acquire the same per-item lock via LockFeatureForWrite.
var nodeWriteMu sync.Map

// LockFeatureForWrite acquires both an in-process mutex AND a cross-process
// advisory file lock so multiple writers cannot race on the same work item
// HTML. The file lock guards a sidecar at `<featurePath>.lock` (created on first
// use, never deleted — flocks survive removal anyway, and an always-present
// sidecar means we never re-create a contested file).
// Callers MUST defer the returned release function.
//
// The acquire-read-modify-write window must be inside the lock; the
// underlying atomic temp+rename keeps single writes safe on its own. This
// closes the lost-update race when `compliance auto`, `spec generate
// --insert`, and `WriteNodeHTML` (status transitions) target the same
// feature concurrently — including from separate `wipnote` CLI processes.
//
// On flock acquisition errors, falls back to in-process-only locking and
// logs nothing; this preserves single-process behavior for tests on file
// systems that don't support flock.
func LockFeatureForWrite(featurePath string) (release func()) {
	muVal, _ := nodeWriteMu.LoadOrStore(featurePath, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()

	lockPath := featurePath + ".lock"
	f, ferr := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if ferr != nil {
		// In-process lock only — degrade gracefully on filesystem errors.
		return mu.Unlock
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return mu.Unlock
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		mu.Unlock()
	}
}

// atomicWriteCounter provides a unique sequence number per atomic write call,
// used to make temp filenames unique even when called from multiple goroutines
// in the same process (which all share the same PID).
var atomicWriteCounter atomic.Int64

// --- ID generation -----------------------------------------------------------

// prefixes maps node types to their short ID prefix.
// Matches Python wipnote.ids.PREFIXES.
var prefixes = map[string]string{
	"feature": "feat",
	"bug":     "bug",
	"chore":   "chr",
	"spike":   "spk",
	"epic":    "epc",
	"session": "sess",
	"track":   "trk",
	"phase":   "phs",
	"agent":   "agt",
	"spec":    "spec",
	"plan":    "plan",
	"event":   "evt",
}

// GenerateID creates a collision-resistant ID matching the Python format.
//
// Format: {prefix}-{hex8} (e.g., feat-a1b2c3d4)
//
// The hash combines: title + UTC timestamp (nanosecond) + 4 random bytes.
func GenerateID(nodeType, title string) string {
	prefix, ok := prefixes[nodeType]
	if !ok && len(nodeType) >= 4 {
		prefix = nodeType[:4]
	} else if !ok {
		prefix = nodeType
	}

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	entropy := make([]byte, 4)
	_, _ = rand.Read(entropy) // crypto/rand never errors on supported platforms

	content := append([]byte(fmt.Sprintf("%s:%s", title, ts)), entropy...)
	hash := sha256.Sum256(content)

	return fmt.Sprintf("%s-%x", prefix, hash[:4])
}

// --- HTML writing ------------------------------------------------------------

//go:embed templates/node.gohtml
var templateFS embed.FS

var nodeTmpl = template.Must(
	template.ParseFS(templateFS, "templates/node.gohtml"),
)

// WriteNodeHTML serialises a Node to the canonical wipnote HTML format and
// writes it to the collection directory. The output MUST be parseable by
// htmlparse.ParseFile to ensure round-trip fidelity.
//
// Writes are atomic: the content is rendered to a temp file, fsynced, then
// renamed over the target path (POSIX rename is atomic). A per-node lock also
// serialises concurrent cross-process writes to the same node file.
//
// Supplemental sections (`<section class="spec">` and
// `<section class="compliance-findings">`) are preserved across writes:
// callers like `compliance auto` and `spec generate --insert` append these
// outside the templated render, and we don't want a status transition (which
// re-renders the whole file from the Node) to silently delete them.
//
// Returns the absolute path of the written file.
func WriteNodeHTML(dir string, node *models.Node) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, node.ID+".html")
	defer LockFeatureForWrite(path)()
	return writeNodeHTMLUnlocked(dir, node)
}

// writeNodeHTMLUnlocked writes node HTML without acquiring the per-item lock.
// Callers use this when a broader canonical read-modify-write operation already
// owns LockFeatureForWrite for the target path.
func writeNodeHTMLUnlocked(dir string, node *models.Node) (string, error) {
	path := filepath.Join(dir, node.ID+".html")
	html, err := renderNodeHTML(node)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", node.ID, err)
	}

	// Extract supplemental sections from the existing file (if any) and
	// splice them back into the freshly-rendered template output. Skip
	// silently when the file doesn't exist or has no supplemental sections.
	if existing, rerr := os.ReadFile(path); rerr == nil {
		if merged, ok := preserveSupplementalSections(string(existing), html); ok {
			html = merged
		}
	}

	if err := atomicWriteFile(path, []byte(html), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// preserveSupplementalSections finds known supplemental sections in the prior
// file content (sections that the node template does not emit — currently
// `<section class="spec">` and `<section class="compliance-findings">`) and
// re-inserts them into the freshly-rendered template just before `</body>`.
// Returns the merged HTML and ok=true when at least one section was carried
// over; otherwise returns ("", false) so the caller writes the raw render.
func preserveSupplementalSections(existing, rendered string) (string, bool) {
	classes := []string{"spec", "compliance-findings"}
	var preserved []string
	for _, class := range classes {
		if section, ok := extractSection(existing, class); ok {
			preserved = append(preserved, section)
		}
	}
	if len(preserved) == 0 {
		return "", false
	}
	insert := "\n" + strings.Join(preserved, "\n") + "\n"
	bodyClose := strings.LastIndex(rendered, "</body>")
	if bodyClose == -1 {
		return rendered + insert, true
	}
	return rendered[:bodyClose] + insert + rendered[bodyClose:], true
}

// extractSection returns the first `<section class="<class>"...></section>`
// block in html along with ok=true. Match is by leading attribute prefix so
// extra attributes (e.g. data-* on the compliance-findings section) are
// retained verbatim.
func extractSection(html, class string) (string, bool) {
	openPrefix := `<section class="` + class + `"`
	const closeTag = `</section>`
	start := strings.Index(html, openPrefix)
	if start == -1 {
		return "", false
	}
	end := strings.Index(html[start:], closeTag)
	if end == -1 {
		return "", false
	}
	end += start + len(closeTag)
	return html[start:end], true
}

// atomicWriteFile writes data to path atomically: it writes to a temp file in
// the same directory, calls Sync to flush to storage, then renames the temp
// file over the target. POSIX rename is atomic within the same filesystem.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	seq := atomicWriteCounter.Add(1)
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), seq)

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}

	_ = dir // ensure dir is always used (for documentation clarity)
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename temp to target: %w", err)
	}

	return nil
}

// renderNodeHTML produces the full HTML document for a node using
// html/template with an embedded .gohtml template.
func renderNodeHTML(n *models.Node) (string, error) {
	data := newNodeTemplateData(n)
	var buf bytes.Buffer
	if err := nodeTmpl.ExecuteTemplate(&buf, "node.gohtml", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// nodeTemplateData holds all pre-computed values for the node template.
// Fields that contain trusted HTML use template.HTML to bypass auto-escaping.
type nodeTemplateData struct {
	ID               string
	Title            string
	Type             string
	Status           string
	Priority         string
	CreatedAt        string
	UpdatedAt        string
	AgentAssigned    string
	TrackID          string
	SpikeSubtype     string
	ClaimedAt        string
	ClaimedBySession string

	// Provenance — rendered as data-created-by-* attributes on <article>.
	CreatedByAgent      string
	CreatedByModel      string
	CreatedByRole       string
	CreatedByCLIVersion string

	// PropAttrs carries models.Node.Properties as pre-rendered, pre-escaped
	// HTML attributes with a leading space (empty when the node has none).
	// Attribute *names* are dynamic, and html/template cannot compute an
	// output context for an action in attribute-name position, so the whole
	// attribute run is built by nodePropAttrs and injected as HTMLAttr — same
	// approach as edgeData.PropAttrs (defined below) uses for edges.
	PropAttrs template.HTMLAttr

	StatusLabel   string
	PriorityLabel string

	HasEdges   bool
	EdgeGroups []edgeGroupData

	HasSteps bool
	Steps    []stepData

	HasContent     bool
	TrustedContent template.HTML
}

// edgeGroupData holds one group of edges (same relationship type).
type edgeGroupData struct {
	RelType  string
	RelLabel string
	Edges    []edgeData
}

// edgeData holds one edge link for the template.
type edgeData struct {
	TargetID     string
	Href         string
	Relationship string
	Label        string
	HasSince     bool
	Since        string

	// PropAttrs carries models.Edge.Properties as pre-rendered, pre-escaped
	// HTML attributes with a leading space (empty when the edge has none).
	// Attribute *names* are dynamic, and html/template cannot compute an
	// output context for an action in attribute-name position, so the whole
	// attribute run is built by edgePropAttrs and injected as HTMLAttr.
	PropAttrs template.HTMLAttr
}

// renderPropAttrs renders a property map as the attribute run that goes
// inside an element's opening tag, sharing the wire format defined in
// core/htmlparse/edge_props.go (and, for nodes, core/htmlparse/node_props.go).
// isAttrSafe supplies the caller's own key-charset/reserved-name rules (edge
// vs node), and overflowAttr is the JSON escape-hatch attribute name to use
// for anything isAttrSafe rejects.
//
// props is map[string]any rather than map[string]string so this same core
// serves both edge properties (always strings) and node properties (any Go
// value SetProperty was called with). A key only gets the readable
// data-<key> form when BOTH isAttrSafe(key) and the value is a string —
// anything else (attr-unsafe key, or a non-string value that would lose its
// type if flattened to an attribute string) folds into the JSON payload,
// which preserves the value's shape across a parse round-trip.
//
// Injection safety: the returned string is marked HTMLAttr, so it bypasses
// html/template's contextual escaping and must therefore be self-escaping.
// Attribute names are only ever emitted for keys isAttrSafe accepts
// (lowercase [a-z0-9_-], no reserved names — nothing that can close a tag or
// start another attribute), and every value, including the JSON payload,
// goes through template.HTMLEscapeString, which escapes the double quote
// that delimits it.
//
// Keys are sorted, and encoding/json sorts map keys of its own accord, so a
// given property map always renders to the same bytes — a rewrite of an
// unchanged node produces no diff.
func renderPropAttrs(props map[string]any, isAttrSafe func(string) bool, overflowAttr string) template.HTMLAttr {
	if len(props) == 0 {
		return ""
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	overflow := make(map[string]any)
	for _, k := range keys {
		v := props[k]
		if s, ok := v.(string); ok && isAttrSafe(k) {
			fmt.Fprintf(&sb, ` data-%s="%s"`, k, template.HTMLEscapeString(s))
			continue
		}
		overflow[k] = v
	}

	if len(overflow) > 0 {
		if payload, err := json.Marshal(overflow); err == nil {
			fmt.Fprintf(&sb, ` %s="%s"`, overflowAttr,
				template.HTMLEscapeString(string(payload)))
		}
	}

	return template.HTMLAttr(sb.String()) // #nosec G203: escaped above
}

// edgePropAttrs renders an edge's properties as the attribute run that goes
// inside the edge's <a> tag. Edge.Properties is map[string]string (every
// value is already a string), so this is a thin adapter over the shared
// renderPropAttrs core. Returns "" for an edge with no properties.
func edgePropAttrs(props map[string]string) template.HTMLAttr {
	if len(props) == 0 {
		return ""
	}
	anyProps := make(map[string]any, len(props))
	for k, v := range props {
		anyProps[k] = v
	}
	return renderPropAttrs(anyProps, htmlparse.EdgePropKeyIsAttrSafe, htmlparse.EdgePropsAttr)
}

// nodePropAttrs renders a node's properties as the attribute run that goes
// inside <article>, using the wire format defined in
// core/htmlparse/node_props.go. Returns "" for a node with no properties.
func nodePropAttrs(props map[string]any) template.HTMLAttr {
	return renderPropAttrs(props, htmlparse.NodePropKeyIsAttrSafe, htmlparse.NodePropsAttr)
}

// edgeHref returns a collection-aware relative href for a link to targetID.
//
// Work-item HTML lives in per-collection subdirectories of .wipnote/
// (bugs/, features/, spikes/, tracks/, plans/, specs/, sessions/). A bare
// "<id>.html" href only resolves when source and target share a directory;
// cross-collection edges (e.g. a bug's part_of → a track, or implemented_in →
// a session) must hop up one level and into the target's collection
// ("../tracks/trk-….html"). The target collection is derived from the ID
// prefix; session IDs are bare UUIDs with no prefix (fix for bug-fddf5820,
// findings 1).
//
// The parser strips any leading directory component when reading the href back
// (core/htmlparse parser.go), so prefixed hrefs round-trip to the same target
// ID — making this change safe for re-ingest.
func edgeHref(targetID string) string {
	dir := collectionDirForID(targetID)
	if dir == "" {
		return targetID + ".html"
	}
	return "../" + dir + "/" + targetID + ".html"
}

// collectionDirForID maps a target ID to its .wipnote/ collection
// subdirectory based on the ID prefix. Session IDs (bare UUIDs, no prefix)
// resolve to "sessions". Returns "" when the prefix is unrecognized, signaling
// callers to fall back to a bare same-directory href.
func collectionDirForID(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "features"
	case strings.HasPrefix(id, "bug-"):
		return "bugs"
	case strings.HasPrefix(id, "spk-"):
		return "spikes"
	case strings.HasPrefix(id, "trk-"):
		return "tracks"
	case strings.HasPrefix(id, "plan-"), strings.HasPrefix(id, "pln-"):
		return "plans"
	case strings.HasPrefix(id, "spc-"):
		return "specs"
	case isSessionID(id):
		return "sessions"
	default:
		return ""
	}
}

// isSessionID reports whether id looks like a session identifier.
// Two formats are recognised:
//
//  1. RFC 4122 UUID (8-4-4-4-12 hex groups, 36 chars with hyphens), e.g.
//     "127926be-6a1c-4045-a347-e42785ec5839" — produced by Claude Code's own
//     session_id and by uuid.New() fallback in session-start.
//
//  2. OTel compact hex (28 lowercase hex chars, no hyphens), e.g.
//     "019ebc63ba7ae905adb1f8db7504" — produced by generateOtelSessionID()
//     in cmd/wipnote/claude_otel_collect_spawn.go (12-char ms timestamp +
//     16-char entropy). Without this branch, compact IDs fall through
//     collectionDirForID to the default "" case and edgeHref emits a bare
//     filename, breaking the ../sessions/ cross-collection link (bug-91e8aa4c).
//
// Session work items carry no recognised work-item prefix, so they must be
// identified structurally rather than by prefix.
func isSessionID(id string) bool {
	switch len(id) {
	case 36:
		// RFC 4122 UUID: 8-4-4-4-12 hex groups separated by hyphens.
		for i, r := range id {
			switch i {
			case 8, 13, 18, 23:
				if r != '-' {
					return false
				}
			default:
				if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
					return false
				}
			}
		}
		return true
	case 28:
		// OTel compact hex: 28 lowercase hex characters, no separators.
		for _, r := range id {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// stepData holds one implementation step for the template.
type stepData struct {
	CompletedStr string
	StepID       string
	Agent        string
	DependsOnStr string
	Icon         string
	Description  string

	// Provenance — rendered as data-created-by-* attributes on <li>. Agent
	// (above) doubles as the data-created-by-agent value.
	CreatedByModel      string
	CreatedByRole       string
	CreatedByCLIVersion string
}

// newNodeTemplateData converts a models.Node into template-ready data.
// User-supplied text fields (Title, Content, Step descriptions) are sanitized
// through SanitizeHostPaths before rendering so machine-local absolute path
// prefixes never appear in committed work-item HTML (bug-ff6a3286).
func newNodeTemplateData(n *models.Node) *nodeTemplateData {
	d := &nodeTemplateData{
		ID:               n.ID,
		Title:            paths.SanitizeHostPaths(n.Title),
		Type:             n.Type,
		Status:           string(n.Status),
		Priority:         string(n.Priority),
		CreatedAt:        fmtTime(n.CreatedAt),
		UpdatedAt:        fmtTime(n.UpdatedAt),
		AgentAssigned:    n.AgentAssigned,
		TrackID:          n.TrackID,
		SpikeSubtype:     n.SpikeSubtype,
		ClaimedAt:        n.ClaimedAt,
		ClaimedBySession: n.ClaimedBySession,

		CreatedByAgent:      n.CreatedByAgent,
		CreatedByModel:      n.CreatedByModel,
		CreatedByRole:       n.CreatedByRole,
		CreatedByCLIVersion: n.CreatedByCLIVersion,

		PropAttrs: nodePropAttrs(n.Properties),

		StatusLabel:   titleCase(strings.ReplaceAll(string(n.Status), "-", " ")),
		PriorityLabel: titleCase(string(n.Priority)),
	}

	d.EdgeGroups = buildEdgeGroups(n)
	d.HasEdges = len(d.EdgeGroups) > 0

	d.Steps = buildSteps(n.Steps)
	d.HasSteps = len(d.Steps) > 0

	if n.Content != "" {
		d.HasContent = true
		content := paths.SanitizeHostPaths(n.Content)
		// Wrap plain text in <p> so it survives the HTML round-trip.
		// The parser reads element children only, not text nodes.
		//
		// Plain-text descriptions are HTML-escaped first so angle-bracket
		// placeholders (e.g. "<id>", "<path>") are emitted as entities rather
		// than being parsed as tags and silently dropped on re-ingest
		// (bug-fddf5820, finding 2). Content that already begins with "<" is
		// treated as authored HTML and passed through verbatim.
		if !strings.HasPrefix(strings.TrimSpace(content), "<") {
			content = "<p>" + template.HTMLEscapeString(content) + "</p>"
		}
		d.TrustedContent = template.HTML(content) // #nosec: authored HTML
	}

	return d
}

// buildEdgeGroups converts a Node's edges map into template-ready groups.
func buildEdgeGroups(n *models.Node) []edgeGroupData {
	if len(n.Edges) == 0 {
		return nil
	}
	// Relationship types are sorted so a rewrite of an unchanged node emits
	// byte-identical HTML — Go map iteration order would otherwise reshuffle
	// the nav on every write and churn the committed artifact.
	relTypes := make([]string, 0, len(n.Edges))
	for relType := range n.Edges {
		relTypes = append(relTypes, relType)
	}
	sort.Strings(relTypes)

	groups := make([]edgeGroupData, 0, len(n.Edges))
	for _, relType := range relTypes {
		edges := n.Edges[relType]
		if len(edges) == 0 {
			continue
		}
		g := edgeGroupData{
			RelType:  relType,
			RelLabel: titleCase(strings.ReplaceAll(relType, "_", " ")),
			Edges:    make([]edgeData, 0, len(edges)),
		}
		for _, e := range edges {
			label := e.Title
			if label == "" {
				label = e.TargetID
			}
			ed := edgeData{
				TargetID:     e.TargetID,
				Href:         edgeHref(e.TargetID),
				Relationship: string(e.Relationship),
				Label:        label,
				PropAttrs:    edgePropAttrs(e.Properties),
			}
			if !e.Since.IsZero() {
				ed.HasSince = true
				ed.Since = fmtTime(e.Since)
			}
			g.Edges = append(g.Edges, ed)
		}
		groups = append(groups, g)
	}
	return groups
}

// buildSteps converts a slice of model Steps into template-ready data.
func buildSteps(steps []models.Step) []stepData {
	if len(steps) == 0 {
		return nil
	}
	result := make([]stepData, 0, len(steps))
	for _, s := range steps {
		icon := "\u23F3" // hourglass
		completed := "false"
		if s.Completed {
			icon = "\u2705" // checkmark
			completed = "true"
		}
		sd := stepData{
			CompletedStr:        completed,
			StepID:              s.StepID,
			Agent:               s.Agent,
			Icon:                icon,
			Description:         paths.SanitizeHostPaths(s.Description),
			CreatedByModel:      s.CreatedByModel,
			CreatedByRole:       s.CreatedByRole,
			CreatedByCLIVersion: s.CreatedByCLIVersion,
		}
		if len(s.DependsOn) > 0 {
			sd.DependsOnStr = strings.Join(s.DependsOn, ",")
		}
		result = append(result, sd)
	}
	return result
}

// fmtTime formats a time.Time in Python-compatible ISO-8601.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02T15:04:05.999999")
}

// titleCase capitalises the first letter of each word.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
