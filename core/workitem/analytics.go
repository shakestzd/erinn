package workitem

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	// Rollup is the item's outcome signal (feat-7ee73444), nil for track-type
	// bottlenecks (tracks carry no rollup of their own) and for item
	// bottlenecks whose rollup was never measured. A caller must treat nil
	// as "unmeasured", never as "clean" — see ItemRollupSignal.
	Rollup *ItemRollupSignal
}

// Recommendation describes a suggested next work item.
type Recommendation struct {
	ItemID   string
	Title    string
	TrackID  string
	Priority string
	Reason   string
	// Rollup is set only when the item itself carries a measured outcome
	// rollup (feat-7ee73444) — in practice a todo item that was previously
	// completed and reopened, since Complete is the only path that writes
	// one. nil means unmeasured and must never be treated as a clean/healthy
	// signal, nor does its absence penalise the item; see ItemRollupSignal.
	Rollup *ItemRollupSignal
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
	thrashing := thrashBottlenecks(nodes)
	overloaded := overloadedTrackBottlenecks(nodes)

	out := append(stale, thrashing...)
	return append(out, overloaded...)
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
//
// A todo item normally carries no rollup — Complete is the only path that
// writes one (core/workitem/rollup.go), so the common case is "never
// measured" and this function must not reward or punish that absence either
// way. The one real case a todo item DOES carry a rollup is a reopen that
// was later reset back to todo (e.g. `wipnote feature reset`): the item was
// completed once, thrashed, and is now back up for grabs. That history is
// real and is surfaced in Reason and used to sort the item behind its
// same-tier, unmeasured-or-clean peers rather than silently ranking level
// with them (feat-f9118b9c).
func RecommendNextWorkIn(nodes []*models.Node) []Recommendation {
	trackPriority := buildTrackPriorityMap(nodes)
	var recs []Recommendation

	for _, n := range nodes {
		if n.Status != models.StatusTodo || n.Type == "track" {
			continue
		}
		sig := RollupSignalFor(n)
		reason := recommendationReason(n, trackPriority)
		var rollup *ItemRollupSignal
		if sig.Measured {
			rollup = &sig
			if note := thrashNote(sig); note != "" {
				reason = "prior-run thrash: " + note + " — " + reason
			}
		}
		recs = append(recs, Recommendation{
			ItemID:   n.ID,
			Title:    n.Title,
			TrackID:  n.TrackID,
			Priority: string(n.Priority),
			Reason:   reason,
			Rollup:   rollup,
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
		reason := fmt.Sprintf("in-progress for %.0f hours without update", age.Hours())
		var rollup *ItemRollupSignal
		// A stale item can be a reopen of previously-completed work (Start
		// does not touch rollup properties — core/workitem/collection.go), in
		// which case it still carries the outcome from its last completion.
		// Surface it as extra context on the stale reason rather than a
		// second bottleneck entry.
		if sig := RollupSignalFor(n); sig.Measured {
			rollup = &sig
			if note := thrashNote(sig); note != "" {
				reason += "; previously " + note
			}
		}
		out = append(out, Bottleneck{
			ItemID:   n.ID,
			Title:    n.Title,
			Type:     n.Type,
			Reason:   reason,
			Duration: age,
			Rollup:   rollup,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Duration > out[j].Duration
	})
	return out
}

// thrashBottlenecks flags in-progress items whose rollup (feat-7ee73444)
// already carries a nonzero outcome signal from an earlier completion, even
// when the item is too fresh to be stale. Start leaves rollup properties
// alone (core/workitem/collection.go), so a reopened item keeps the numbers
// its last Complete wrote: a measured failure rate, a retry, or edit churn is
// real history that predates this run, not something inferred from staleness.
// Absence of a rollup here means unmeasured and is never flagged either way.
func thrashBottlenecks(nodes []*models.Node) []Bottleneck {
	var out []Bottleneck
	for _, n := range nodes {
		if n.Status != models.StatusInProgress {
			continue
		}
		age := time.Now().UTC().Sub(n.UpdatedAt)
		if age >= staleThreshold {
			continue // already reported (with the same rollup context) by staleBottlenecks
		}
		sig := RollupSignalFor(n)
		note := thrashNote(sig)
		if note == "" {
			continue
		}
		rollup := sig
		out = append(out, Bottleneck{
			ItemID: n.ID,
			Title:  n.Title,
			Type:   n.Type,
			Reason: "prior-run thrash: " + note,
			Rollup: &rollup,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ItemID < out[j].ItemID
	})
	return out
}

// thrashNote renders the distinct, individually-sourced signals that fired on
// sig into one human-readable clause, or "" when none did. It never blends
// the metrics into a single number — each clause names its own value and
// coverage — matching feat-7ee73444's per-metric provenance rule.
func thrashNote(sig ItemRollupSignal) string {
	var parts []string
	if sig.HasFailureRate && sig.FailureRate > 0 {
		parts = append(parts, fmt.Sprintf("failure rate %.1f%% (%s, %s)",
			sig.FailureRate*100, sig.FailureRateCoverage, sig.FailureRateSource))
	}
	if sig.HasRetries && sig.Retries > 0 {
		parts = append(parts, fmt.Sprintf("%d retries (%s)", sig.Retries, sig.RetriesCoverage))
	}
	if sig.HasChurnFiles && sig.ChurnFiles > 0 {
		parts = append(parts, fmt.Sprintf("%d churned files (%s)", sig.ChurnFiles, sig.ChurnFilesCoverage))
	}
	return strings.Join(parts, "; ")
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

// recThrashed reports whether r's own rollup (if any) shows prior-run
// thrash. Absence of a rollup is never treated as thrashed — it is simply
// not a tiebreak factor, consistent with "unmeasured is not unhealthy".
func recThrashed(r Recommendation) bool {
	return r.Rollup != nil && r.Rollup.Thrashed()
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
		if pi != pj {
			return pi > pj
		}
		// Same track tier, same priority: an item with measured prior-run
		// thrash (feat-f9118b9c) sorts after an item with no such signal —
		// clean and unmeasured items are treated identically and both rank
		// ahead of a demonstrated thrasher. Compared as ints (not the raw
		// bools) so the less-function stays a strict, consistent ordering.
		ti2, tj2 := 0, 0
		if recThrashed(recs[i]) {
			ti2 = 1
		}
		if recThrashed(recs[j]) {
			tj2 = 1
		}
		return ti2 < tj2
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
