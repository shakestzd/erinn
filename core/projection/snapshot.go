package projection

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shakestzd/wipnote/core/arch"
	"github.com/shakestzd/wipnote/core/claimledger"
	"github.com/shakestzd/wipnote/core/gateledger"
	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"gopkg.in/yaml.v3"
)

type Node struct {
	ID       string
	Type     string
	Title    string
	Status   string
	Priority string
	TrackID  string
	Agent    string
	// Kind and CreatedBy are populated for "arch" nodes only (architecture
	// memory cards) — see addArchNodes. Empty for every other node type.
	Kind      string
	CreatedBy string
}

type Edge struct {
	FromID       string
	FromType     string
	ToID         string
	ToType       string
	Relationship string
	Metadata     map[string]string
}

type Snapshot struct {
	WipnoteDir string
	Nodes      map[string]Node
	NodeOrder  []string
	Edges      []Edge
	Out        map[string][]Edge
	In         map[string][]Edge
	Sessions   []sessionledger.Record
	Claims     []claimledger.Episode
	Gates      []gateledger.Record
	// edgeKeys dedups on the same composite (from, rel, to) key the SQL-era
	// graph_edges.edge_id PRIMARY KEY used, so a declaration repeated in
	// canonical HTML (e.g. `wipnote link add` run twice) is collapsed to one
	// edge instead of double-counting in Bottlenecks/Hubs. See addEdge.
	edgeKeys map[string]bool
}

// Load builds the canonical in-memory projection. Node registration happens
// in two phases before any DECLARED edge is added: work items, plan YAML
// meta nodes, arch cards, and finally session/claim/gate ledger nodes. Only
// once every node-contributing source has run does addDeclaredEdges apply
// the target-validity gate (graph.ClassifyEdgeTarget) to each work item's own
// graph edges — the gate needs the FULL node set to tell a live target from a
// pruned session from a genuinely dangling reference, exactly as the SQL
// reindex pass requires validIDs to be fully collected before indexing edges
// (cmd/wipnote/reindex.go).
func Load(wipnoteDir string) (*Snapshot, error) {
	nodes, err := graph.LoadAll(wipnoteDir)
	if err != nil {
		return nil, err
	}
	s := emptySnapshot(wipnoteDir)
	for _, n := range nodes {
		s.addWorkItemNode(n)
	}
	s.addPlanYAMLNodes()
	s.addArchNodes()
	if err := s.addLedgers(); err != nil {
		return nil, err
	}
	s.addDeclaredEdges(nodes)
	s.addTrackEdges(nodes)
	s.addPlanYAMLEdges()
	s.addClaimImplementationEdges()
	s.addReverseImplements()
	sort.Strings(s.NodeOrder)
	return s, nil
}

func emptySnapshot(wipnoteDir string) *Snapshot {
	return &Snapshot{
		WipnoteDir: wipnoteDir,
		Nodes:      map[string]Node{},
		Out:        map[string][]Edge{},
		In:         map[string][]Edge{},
		edgeKeys:   map[string]bool{},
	}
}

// addWorkItemNode registers a work item's node only. Its declared edges are
// added later by addDeclaredEdges, once every node-contributing pass
// (ledgers, arch, plan YAML) has run — see Load's doc comment for why the
// ordering matters.
func (s *Snapshot) addWorkItemNode(n *models.Node) {
	if n == nil || n.ID == "" {
		return
	}
	s.addNode(Node{
		ID:       n.ID,
		Type:     n.Type,
		Title:    n.Title,
		Status:   string(n.Status),
		Priority: string(n.Priority),
		TrackID:  n.TrackID,
		Agent:    n.AgentAssigned,
	})
}

// addDeclaredEdges adds every work item's own graph edges — the edges
// literally declared in canonical HTML — applying the same target-validity
// gate the SQL reindex pass has always used (graph.ClassifyEdgeTarget,
// cmd/wipnote/reindex.go:indexNodeEdges). Without this, an item→session
// implemented_in edge whose session has since been pruned would render as a
// blank, unmarked neighbour instead of the tombstoned caveat
// core/graph/tombstone.go exists to preserve (bug-10e166d8), and a target
// that resolves to nothing at all — not even a session shape — would be kept
// forever instead of dropped as the dangling reference it is.
//
// Must run after every node-registering pass (work items, plan YAML, arch,
// ledgers) so validIDs reflects the FULL node set; running it any earlier
// would misclassify a live plan/session/arch target as dangling.
func (s *Snapshot) addDeclaredEdges(nodes []*models.Node) {
	validIDs := make(map[string]bool, len(s.Nodes))
	for id := range s.Nodes {
		validIDs[id] = true
	}
	for _, n := range nodes {
		if n == nil || n.ID == "" {
			continue
		}
		for rel, edges := range n.Edges {
			for _, e := range edges {
				relName := string(e.Relationship)
				if relName == "" {
					relName = rel
				}
				meta := cloneMeta(e.Properties)
				switch graph.ClassifyEdgeTarget(e.TargetID, validIDs) {
				case graph.EdgeTargetDangling:
					continue
				case graph.EdgeTargetTombstoned:
					meta = graph.MarkEdgeTombstoned(meta)
				case graph.EdgeTargetLive:
					// Unchanged: a live target indexes exactly as declared.
				}
				s.addEdge(Edge{
					FromID:       n.ID,
					FromType:     n.Type,
					ToID:         e.TargetID,
					ToType:       inferType(e.TargetID),
					Relationship: relName,
					Metadata:     meta,
				})
			}
		}
	}
}

func (s *Snapshot) addTrackEdges(nodes []*models.Node) {
	for _, n := range nodes {
		if n == nil || n.ID == "" || n.TrackID == "" {
			continue
		}
		if !s.hasEdge(n.ID, n.TrackID, "part_of") {
			s.addEdge(Edge{FromID: n.ID, FromType: n.Type, ToID: n.TrackID, ToType: "track", Relationship: "part_of"})
		}
		if !s.hasEdge(n.TrackID, n.ID, "contains") {
			s.addEdge(Edge{FromID: n.TrackID, FromType: "track", ToID: n.ID, ToType: n.Type, Relationship: "contains"})
		}
	}
}

func (s *Snapshot) hasEdge(fromID, toID, relationship string) bool {
	for _, e := range s.Out[fromID] {
		if e.ToID == toID && e.Relationship == relationship {
			return true
		}
	}
	return false
}

func (s *Snapshot) addLedgers() error {
	sessions, err := sessionledger.NewStore(s.WipnoteDir).ReadAll()
	if err != nil {
		return fmt.Errorf("read session ledger: %w", err)
	}
	for _, r := range sessions {
		status := "done"
		if r.IsOpen() {
			status = "active"
		}
		s.addNode(Node{
			ID:     r.SessionID,
			Type:   "session",
			Title:  r.Label(),
			Status: status,
			Agent:  r.Harness,
		})
	}
	s.Sessions = sessions
	claims, err := claimledger.NewStore(s.WipnoteDir).ReadAll()
	if err != nil {
		return fmt.Errorf("read claim ledger: %w", err)
	}
	s.Claims = claims
	gates, err := gateledger.NewStore(s.WipnoteDir).ReadAll()
	if err != nil {
		return fmt.Errorf("read gate ledger: %w", err)
	}
	s.Gates = gates
	return nil
}

type planYAMLProjection struct {
	Meta struct {
		ID       string `yaml:"id"`
		Title    string `yaml:"title"`
		Status   string `yaml:"status"`
		Priority string `yaml:"priority"`
	} `yaml:"meta"`
	Slices []struct {
		ID        string `yaml:"id"`
		FeatureID string `yaml:"feature_id"`
		Num       int    `yaml:"num"`
		Deps      []int  `yaml:"deps"`
	} `yaml:"slices"`
}

// addPlanYAMLNodes ensures every YAML plan has a registered node BEFORE
// addDeclaredEdges runs, so a work item's own declared edge pointing at a
// plan ID (e.g. planned_in) classifies as live rather than dangling. This is
// the node-registration half of addPlanYAMLProjection; addPlanYAMLEdges
// (called later, after addDeclaredEdges) does the edge-deriving half. Split
// out for defect 1 (feat-fc3cc9e0) — see Load's doc comment for the ordering
// rationale.
func (s *Snapshot) addPlanYAMLNodes() {
	for _, p := range s.loadPlanYAMLProjections() {
		planID := strings.TrimSpace(p.Meta.ID)
		if planID == "" {
			continue
		}
		if _, ok := s.Nodes[planID]; !ok {
			s.addNode(Node{ID: planID, Type: "plan", Title: p.Meta.Title, Status: p.Meta.Status, Priority: p.Meta.Priority})
		}
	}
}

func (s *Snapshot) addPlanYAMLEdges() {
	for _, p := range s.loadPlanYAMLProjections() {
		s.addPlanYAMLProjection(p)
	}
}

func (s *Snapshot) loadPlanYAMLProjections() []planYAMLProjection {
	files, _ := filepath.Glob(filepath.Join(s.WipnoteDir, "plans", "*.yaml"))
	var out []planYAMLProjection
	for _, path := range files {
		p, err := readPlanYAMLProjection(path)
		if err == nil {
			out = append(out, p)
		}
	}
	return out
}

// addArchNodes registers architecture memory cards as "arch" nodes, mirroring
// core/graph/dsl.go's ArchSource-backed arch support — the type `wipnote
// query` lost when it moved onto this projection (defect 3, feat-fc3cc9e0).
// Cards come straight from the canonical .wipnote/architecture.html ledger,
// the same store every `wipnote arch` command reads (see
// cmd/wipnote/arch_source.go's archSourceFor/archGraphNodes, which this
// mirrors). A missing or unreadable store yields no arch nodes rather than
// failing the whole projection load — the same degrade-not-fail policy the
// SQL path used when the arch_cards table was absent.
//
// Must run before addDeclaredEdges: a work item can carry a learned_from
// edge to an arch card, and that target needs to resolve as live rather than
// dangling.
func (s *Snapshot) addArchNodes() {
	store, err := arch.NewStore(s.WipnoteDir)
	if err != nil {
		return
	}
	cards, err := store.List(true)
	if err != nil {
		return
	}
	for _, c := range cards {
		if c == nil || c.Name == "" {
			continue
		}
		status := "active"
		if c.Retired || c.SupersededBy != "" {
			status = "retired"
		}
		s.addNode(Node{
			ID:        arch.ArchNodeID(c.Name),
			Type:      "arch",
			Title:     c.Name,
			Status:    status,
			Kind:      string(c.Kind),
			CreatedBy: c.CreatedBy,
		})
	}
}

func readPlanYAMLProjection(path string) (planYAMLProjection, error) {
	var p planYAMLProjection
	data, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	return p, yaml.Unmarshal(data, &p)
}

func (s *Snapshot) addPlanYAMLProjection(p planYAMLProjection) {
	planID := strings.TrimSpace(p.Meta.ID)
	if planID == "" {
		return
	}
	if _, ok := s.Nodes[planID]; !ok {
		s.addNode(Node{ID: planID, Type: "plan", Title: p.Meta.Title, Status: p.Meta.Status, Priority: p.Meta.Priority})
	}
	byNum := map[int]string{}
	for _, sl := range p.Slices {
		if id := planSliceNodeID(sl.ID, sl.FeatureID); sl.Num > 0 && id != "" {
			byNum[sl.Num] = id
		}
	}
	for _, sl := range p.Slices {
		id := planSliceNodeID(sl.ID, sl.FeatureID)
		if id == "" {
			continue
		}
		s.addPlannedInEdge(id, planID, sl.Num)
		for _, depNum := range sl.Deps {
			if depID := byNum[depNum]; depID != "" {
				s.addPlanBlockedByEdge(id, depID, planID, sl.Num, depNum)
			}
		}
	}
}

func planSliceNodeID(id, featureID string) string {
	if strings.TrimSpace(featureID) != "" {
		return strings.TrimSpace(featureID)
	}
	return strings.TrimSpace(id)
}

func (s *Snapshot) addPlannedInEdge(id, planID string, num int) {
	if s.hasEdge(id, planID, "planned_in") {
		return
	}
	meta := map[string]string{"slice_num": strconv.Itoa(num)}
	s.addEdge(Edge{FromID: id, FromType: inferType(id), ToID: planID, ToType: "plan", Relationship: "planned_in", Metadata: meta})
}

func (s *Snapshot) addPlanBlockedByEdge(id, depID, planID string, num, depNum int) {
	if s.hasEdge(id, depID, "blocked_by") {
		return
	}
	meta := map[string]string{
		"origin":        graph.EdgeOriginPlanSlice,
		"plan_id":       planID,
		"slice_num":     strconv.Itoa(num),
		"dep_slice_num": strconv.Itoa(depNum),
	}
	s.addEdge(Edge{FromID: id, FromType: inferType(id), ToID: depID, ToType: inferType(depID), Relationship: "blocked_by", Metadata: meta})
}

func (s *Snapshot) addClaimImplementationEdges() {
	for _, c := range s.Claims {
		if c.WorkItemID == "" || c.SessionID == "" {
			continue
		}
		meta := map[string]string{"origin": "claim_ledger"}
		if !s.hasEdge(c.WorkItemID, c.SessionID, "implemented_in") {
			s.addEdge(Edge{
				FromID: c.WorkItemID, FromType: inferType(c.WorkItemID),
				ToID: c.SessionID, ToType: "session", Relationship: "implemented_in",
				Metadata: meta,
			})
		}
	}
}

func (s *Snapshot) addReverseImplements() {
	edges := append([]Edge(nil), s.Edges...)
	for _, e := range edges {
		if e.Relationship != "implemented_in" || s.hasEdge(e.ToID, e.FromID, "implements") {
			continue
		}
		s.addEdge(Edge{
			FromID: e.ToID, FromType: e.ToType,
			ToID: e.FromID, ToType: e.FromType,
			Relationship: "implements", Metadata: cloneMeta(e.Metadata),
		})
	}
}

func (s *Snapshot) addNode(n Node) {
	if _, exists := s.Nodes[n.ID]; !exists {
		s.NodeOrder = append(s.NodeOrder, n.ID)
	}
	s.Nodes[n.ID] = n
}

// addEdge appends e unless (FromID, Relationship, ToID) was already added —
// the composite key that used to be graph_edges.edge_id's PRIMARY KEY (defect
// 2, feat-fc3cc9e0). models.Node.AddEdge is an unconditional append, so a
// declaration repeated in canonical HTML (e.g. `wipnote link add` run twice)
// reaches here twice; without this guard it would double-count in
// Bottlenecks/Hubs and disagree with the dashboard's deduplicated edge list.
// The first declaration wins on a repeat — every current caller passes
// identical (from, rel, to, metadata) on a repeat anyway, so which copy wins
// is not observable in practice.
func (s *Snapshot) addEdge(e Edge) {
	if e.FromID == "" || e.ToID == "" || e.Relationship == "" {
		return
	}
	if e.FromType == "" {
		e.FromType = inferType(e.FromID)
	}
	if e.ToType == "" {
		e.ToType = inferType(e.ToID)
	}
	key := e.FromID + "\x00" + e.Relationship + "\x00" + e.ToID
	if s.edgeKeys[key] {
		return
	}
	s.edgeKeys[key] = true
	s.Edges = append(s.Edges, e)
	s.Out[e.FromID] = append(s.Out[e.FromID], e)
	s.In[e.ToID] = append(s.In[e.ToID], e)
}

func cloneMeta(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func inferType(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "feature"
	case strings.HasPrefix(id, "bug-"):
		return "bug"
	case strings.HasPrefix(id, "spk-"):
		return "spike"
	case strings.HasPrefix(id, "trk-"):
		return "track"
	case strings.HasPrefix(id, "plan-"):
		return "plan"
	case strings.HasPrefix(id, "spec-"):
		return "spec"
	case strings.HasPrefix(id, "sess-") || graph.IsSessionShapedID(id):
		return "session"
	case strings.HasPrefix(id, arch.ArchNodePrefix):
		return "arch"
	default:
		return ""
	}
}

type Store struct {
	wipnoteDir string
	mu         sync.Mutex
	sig        string
	snapshot   *Snapshot
}

func NewStore(wipnoteDir string) *Store {
	return &Store{wipnoteDir: wipnoteDir}
}

func (s *Store) Snapshot() (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sig := signature(s.wipnoteDir)
	if s.snapshot != nil && sig == s.sig {
		return s.snapshot, nil
	}
	snap, err := Load(s.wipnoteDir)
	if err != nil {
		return nil, err
	}
	s.sig, s.snapshot = sig, snap
	return snap, nil
}

func signature(wipnoteDir string) string {
	var parts []string
	for _, root := range projectionRoots(wipnoteDir) {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !projectionFile(path) {
				return nil
			}
			if st, statErr := d.Info(); statErr == nil {
				parts = append(parts, fmt.Sprintf("%s:%d:%d", path, st.Size(), st.ModTime().UnixNano()))
			}
			return nil
		})
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func projectionFile(path string) bool {
	return strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".yaml")
}

func projectionRoots(wipnoteDir string) []string {
	return []string{
		filepath.Join(wipnoteDir, "features"),
		filepath.Join(wipnoteDir, "bugs"),
		filepath.Join(wipnoteDir, "spikes"),
		filepath.Join(wipnoteDir, "tracks"),
		filepath.Join(wipnoteDir, "plans"),
		filepath.Join(wipnoteDir, "specs"),
		filepath.Join(wipnoteDir, claimledger.DirName),
		filepath.Join(wipnoteDir, sessionledger.FileName),
		filepath.Join(wipnoteDir, gateledger.FileName),
		// addArchNodes (defect 3, feat-fc3cc9e0) reads both of these, so the
		// Store cache signature must watch them too or an edited/added arch
		// card would go unnoticed and Store.Snapshot would keep serving a
		// stale cached snapshot. WalkDir accepts a bare file as root
		// (architecture.html), calling the visitor once for it.
		arch.LedgerPath(wipnoteDir),
		filepath.Join(wipnoteDir, "arch"),
	}
}
