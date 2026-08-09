package projection

import (
	"sort"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
)

func (s *Snapshot) ResolveToMap(ids []string) map[string]graph.NodeResult {
	out := make(map[string]graph.NodeResult, len(ids))
	for _, id := range ids {
		if n, ok := s.Nodes[id]; ok {
			out[id] = graph.NodeResult{ID: id, Type: n.Type, Title: n.Title, Status: n.Status}
		} else {
			out[id] = graph.NodeResult{ID: id}
		}
	}
	return out
}

func (s *Snapshot) Follow(ids []string, rel string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		for _, e := range s.Out[id] {
			if e.Relationship != rel || seen[e.ToID] {
				continue
			}
			seen[e.ToID] = true
			out = append(out, e.ToID)
		}
	}
	return out
}

func (s *Snapshot) Reachable(start string, maxHops int) []string {
	if maxHops <= 0 {
		return nil
	}
	type entry struct {
		id   string
		hops int
	}
	seen := map[string]bool{start: true}
	q := []entry{{start, 0}}
	var out []string
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if cur.id != start {
			out = append(out, cur.id)
		}
		if cur.hops >= maxHops {
			continue
		}
		for _, e := range s.Out[cur.id] {
			if !seen[e.ToID] {
				seen[e.ToID] = true
				q = append(q, entry{e.ToID, cur.hops + 1})
			}
		}
	}
	return out
}

func (s *Snapshot) ShortestPath(from, to string) []string {
	if from == to {
		return []string{from}
	}
	type entry struct {
		id   string
		path []string
	}
	seen := map[string]bool{from: true}
	q := []entry{{from, []string{from}}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		for _, e := range s.Out[cur.id] {
			if seen[e.ToID] {
				continue
			}
			path := append(append([]string{}, cur.path...), e.ToID)
			if e.ToID == to {
				return path
			}
			seen[e.ToID] = true
			q = append(q, entry{e.ToID, path})
		}
	}
	return nil
}

func (s *Snapshot) Cycles() [][]string {
	color := map[string]int{}
	var stack []string
	var cycles [][]string
	var dfs func(string)
	dfs = func(id string) {
		color[id] = 1
		stack = append(stack, id)
		for _, e := range s.Out[id] {
			switch color[e.ToID] {
			case 1:
				cycles = append(cycles, cycleFrom(stack, e.ToID))
			case 0:
				dfs(e.ToID)
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = 2
	}
	for _, id := range s.NodeOrder {
		if color[id] == 0 {
			dfs(id)
		}
	}
	return cycles
}

func cycleFrom(stack []string, target string) []string {
	start := 0
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == target {
			start = i
			break
		}
	}
	return append([]string{}, stack[start:]...)
}

func (s *Snapshot) Orphans() []string {
	var ids []string
	for _, id := range s.NodeOrder {
		n := s.Nodes[id]
		if !workItemType(n.Type) && n.Type != "track" {
			continue
		}
		if len(s.Out[id]) == 0 && len(s.In[id]) == 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Snapshot) Hubs(minEdges int) []graph.NodeResult {
	var rows []graph.NodeResult
	for _, id := range s.NodeOrder {
		count := len(s.Out[id]) + len(s.In[id])
		if count >= minEdges {
			rows = append(rows, s.ResolveToMap([]string{id})[id])
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		di := len(s.Out[rows[i].ID]) + len(s.In[rows[i].ID])
		dj := len(s.Out[rows[j].ID]) + len(s.In[rows[j].ID])
		return di > dj
	})
	return rows
}

func (s *Snapshot) Bottlenecks() []graph.BottleneckResult {
	counts := map[string]int{}
	for _, e := range s.Edges {
		if e.Relationship == "blocked_by" && e.Metadata["origin"] != graph.EdgeOriginPlanSlice {
			counts[e.ToID]++
		}
	}
	var out []graph.BottleneckResult
	for id, count := range counts {
		n := s.ResolveToMap([]string{id})[id]
		out = append(out, graph.BottleneckResult{ID: id, Title: n.Title, Status: n.Status, BlockCount: count})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BlockCount > out[j].BlockCount })
	return out
}

// SessionsForFeature returns ledger-backed sessions that claimed a feature.
func (s *Snapshot) SessionsForFeature(featureID string) []graph.SessionInfo {
	seen := map[string]bool{}
	records := map[string]graph.SessionInfo{}
	for _, r := range s.Sessions {
		status := "completed"
		if r.IsOpen() {
			status = "active"
		}
		records[r.SessionID] = graph.SessionInfo{
			SessionID: r.SessionID,
			Agent:     r.Harness,
			Status:    status,
			CreatedAt: r.StartedAt.Format(time.RFC3339),
		}
	}
	for _, c := range s.Claims {
		if c.WorkItemID == featureID {
			seen[c.SessionID] = true
			if c.RootSessionID != "" {
				seen[c.RootSessionID] = true
			}
		}
	}
	var out []graph.SessionInfo
	for id := range seen {
		info := records[id]
		if info.SessionID == "" {
			info.SessionID = id
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func workItemType(t string) bool {
	switch t {
	case "feature", "bug", "spike", "plan", "spec":
		return true
	default:
		return false
	}
}
