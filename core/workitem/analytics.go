package workitem

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/shakestzd/wipnote/core/graph"
	"github.com/shakestzd/wipnote/core/models"
)

// Bottleneck describes a stalled work item or overloaded track.
type Bottleneck struct {
	ItemID   string
	Title    string
	Type     string // "track" or item type
	Reason   string // human-readable explanation
	Duration time.Duration
}

// Recommendation describes a suggested next work item.
type Recommendation struct {
	ItemID   string
	Title    string
	TrackID  string
	Priority string
	Reason   string
}

// ParallelSet groups items that can be worked on simultaneously.
type ParallelSet struct {
	TrackID string
	Items   []*models.Node
}

// FindBottlenecks returns stale in-progress items and overloaded tracks.
//
// Stale: in-progress for more than 3 days without update.
// Overloaded track: more than 2 in-progress items belonging to same track.
func FindBottlenecks(projectDir string) ([]Bottleneck, error) {
	nodes, err := loadAllNodes(projectDir)
	if err != nil {
		return nil, fmt.Errorf("find bottlenecks: %w", err)
	}
	return FindBottlenecksIn(nodes), nil
}

// FindBottlenecksIn is FindBottlenecks over an already-loaded node set.
// Callers that have run graph.LoadAll should use this rather than paying a
// second full corpus parse (bug-1a51ab15).
func FindBottlenecksIn(nodes []*models.Node) []Bottleneck {
	stale := staleBottlenecks(nodes)
	overloaded := overloadedTrackBottlenecks(nodes)

	return append(stale, overloaded...)
}

// RecommendNextWork returns up to 5 suggested todo items, ordered by track
// priority and item priority.
func RecommendNextWork(projectDir string) ([]Recommendation, error) {
	nodes, err := loadAllNodes(projectDir)
	if err != nil {
		return nil, fmt.Errorf("recommend next work: %w", err)
	}
	return RecommendNextWorkIn(nodes), nil
}

// RecommendNextWorkIn is RecommendNextWork over an already-loaded node set.
func RecommendNextWorkIn(nodes []*models.Node) []Recommendation {
	trackPriority := buildTrackPriorityMap(nodes)
	var recs []Recommendation

	for _, n := range nodes {
		if n.Status != models.StatusTodo || n.Type == "track" {
			continue
		}
		reason := recommendationReason(n, trackPriority)
		recs = append(recs, Recommendation{
			ItemID:   n.ID,
			Title:    n.Title,
			TrackID:  n.TrackID,
			Priority: string(n.Priority),
			Reason:   reason,
		})
	}

	sortRecommendations(recs, trackPriority)

	if len(recs) > 5 {
		recs = recs[:5]
	}
	return recs
}

// GetParallelWork returns groups of todo items in the same track that can
// be worked on simultaneously (no cross-item blocking edges).
func GetParallelWork(projectDir string) ([]ParallelSet, error) {
	nodes, err := loadAllNodes(projectDir)
	if err != nil {
		return nil, fmt.Errorf("get parallel work: %w", err)
	}
	return GetParallelWorkIn(nodes), nil
}

// GetParallelWorkIn is GetParallelWork over an already-loaded node set.
func GetParallelWorkIn(nodes []*models.Node) []ParallelSet {
	byTrack := groupTodosByTrack(nodes)
	var sets []ParallelSet

	for trackID, items := range byTrack {
		parallel := filterNonBlocking(items)
		if len(parallel) >= 2 {
			sets = append(sets, ParallelSet{TrackID: trackID, Items: parallel})
		}
	}

	sort.Slice(sets, func(i, j int) bool {
		return sets[i].TrackID < sets[j].TrackID
	})
	return sets
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// loadAllNodes loads feature, bug, spike, and track nodes from projectDir.
func loadAllNodes(projectDir string) ([]*models.Node, error) {
	subdirs := []string{"features", "bugs", "spikes", "tracks"}
	var all []*models.Node

	for _, sub := range subdirs {
		dir := fmt.Sprintf("%s/%s", projectDir, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		nodes, err := graph.LoadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", sub, err)
		}
		all = append(all, nodes...)
	}
	return all, nil
}

const staleThreshold = 72 * time.Hour // 3 days

func staleBottlenecks(nodes []*models.Node) []Bottleneck {
	now := time.Now().UTC()
	var out []Bottleneck

	for _, n := range nodes {
		if n.Status != models.StatusInProgress {
			continue
		}
		age := now.Sub(n.UpdatedAt)
		if age < staleThreshold {
			continue
		}
		out = append(out, Bottleneck{
			ItemID:   n.ID,
			Title:    n.Title,
			Type:     n.Type,
			Reason:   fmt.Sprintf("in-progress for %.0f hours without update", age.Hours()),
			Duration: age,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Duration > out[j].Duration
	})
	return out
}

func overloadedTrackBottlenecks(nodes []*models.Node) []Bottleneck {
	trackActive := make(map[string]int)
	trackTitles := make(map[string]string)

	for _, n := range nodes {
		if n.Type == "track" {
			trackTitles[n.ID] = n.Title
			continue
		}
		if n.Status == models.StatusInProgress && n.TrackID != "" {
			trackActive[n.TrackID]++
		}
	}

	var out []Bottleneck
	for trackID, count := range trackActive {
		if count <= 2 {
			continue
		}
		title := trackTitles[trackID]
		if title == "" {
			title = trackID
		}
		out = append(out, Bottleneck{
			ItemID: trackID,
			Title:  title,
			Type:   "track",
			Reason: fmt.Sprintf("%d items in-progress (WIP limit exceeded)", count),
		})
	}
	return out
}

func buildTrackPriorityMap(nodes []*models.Node) map[string]int {
	order := map[models.Priority]int{
		models.PriorityCritical: 4,
		models.PriorityHigh:     3,
		models.PriorityMedium:   2,
		models.PriorityLow:      1,
	}
	m := make(map[string]int)
	for _, n := range nodes {
		if n.Type == "track" {
			m[n.ID] = order[n.Priority]
		}
	}
	return m
}

func priorityScore(p models.Priority) int {
	switch p {
	case models.PriorityCritical:
		return 4
	case models.PriorityHigh:
		return 3
	case models.PriorityMedium:
		return 2
	default:
		return 1
	}
}

func recommendationReason(n *models.Node, trackPriority map[string]int) string {
	if n.TrackID != "" {
		tp := trackPriority[n.TrackID]
		if tp >= 3 {
			return fmt.Sprintf("high-priority track (%s)", n.TrackID)
		}
	}
	if n.Priority == models.PriorityCritical || n.Priority == models.PriorityHigh {
		return fmt.Sprintf("%s priority item", n.Priority)
	}
	return "next available todo"
}

func sortRecommendations(recs []Recommendation, trackPriority map[string]int) {
	sort.Slice(recs, func(i, j int) bool {
		ti := trackPriority[recs[i].TrackID]
		tj := trackPriority[recs[j].TrackID]
		if ti != tj {
			return ti > tj
		}
		pi := priorityScore(models.Priority(recs[i].Priority))
		pj := priorityScore(models.Priority(recs[j].Priority))
		return pi > pj
	})
}

func groupTodosByTrack(nodes []*models.Node) map[string][]*models.Node {
	byTrack := make(map[string][]*models.Node)
	for _, n := range nodes {
		if n.Status != models.StatusTodo || n.Type == "track" || n.TrackID == "" {
			continue
		}
		byTrack[n.TrackID] = append(byTrack[n.TrackID], n)
	}
	return byTrack
}

// filterNonBlocking returns items that do not block each other.
func filterNonBlocking(items []*models.Node) []*models.Node {
	// Collect IDs of all items in this set.
	ids := make(map[string]bool, len(items))
	for _, n := range items {
		ids[n.ID] = true
	}

	// An item is blocking if it has a "blocks" edge pointing at another item
	// in the same set.
	blocking := make(map[string]bool)
	for _, n := range items {
		for _, edges := range n.Edges {
			for _, e := range edges {
				if e.Relationship == models.RelBlocks && ids[e.TargetID] {
					blocking[n.ID] = true
					blocking[e.TargetID] = true
				}
			}
		}
	}

	var out []*models.Node
	for _, n := range items {
		if !blocking[n.ID] {
			out = append(out, n)
		}
	}
	return out
}
