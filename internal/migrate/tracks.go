// Package migrate implements feature track-attribution backfill.
//
// The classifier matches each feature's feature_files paths against a glob
// rule catalog and re-attributes the feature to the value-aligned track whose
// code surface dominates. Ambiguous features (no clear dominant) are flagged
// for manual review rather than silently moved.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"gopkg.in/yaml.v3"
)

// Rule maps a path glob to a track ID with a precedence priority.
// Higher Priority wins when a single path matches multiple globs.
type Rule struct {
	Glob     string `yaml:"glob"`
	TrackID  string `yaml:"track_id"`
	Priority int    `yaml:"priority"`
}

// RuleSet is the loaded rule catalog from a YAML file.
type RuleSet struct {
	Rules []Rule `yaml:"rules"`
}

// Decision is the per-feature classifier output. Reason values:
//
//	"confident"      — dominant track exceeds threshold; safe to move
//	"ambiguous"      — files matched but no track exceeds threshold
//	"no-attribution" — feature has zero feature_files rows
//	"no-match"       — feature has files but none match any rule
//	"no-change"      — current_track is already the dominant track
type Decision struct {
	FeatureID     string  `json:"feature_id"`
	ItemType      string  `json:"item_type"` // "feature" | "bug"
	CurrentTrack  string  `json:"current_track"`
	ProposedTrack string  `json:"proposed_track"`
	DominantShare float64 `json:"dominant_share"`
	FileCount     int     `json:"file_count"`
	Ambiguous     bool    `json:"ambiguous"`
	Reason        string  `json:"reason"`
}

// LoadRules parses a YAML rule catalog from disk.
func LoadRules(path string) (*RuleSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules %s: %w", path, err)
	}
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse rules %s: %w", path, err)
	}
	return &rs, nil
}

// ClassifyFeature applies the rule catalog to a feature's file list and
// returns the migration Decision. threshold is the minimum dominant-track
// share (0..1) required to mark a Decision as confident; below it the
// Decision is flagged ambiguous.
func ClassifyFeature(rules *RuleSet, files []models.FeatureFile, currentTrack string, threshold float64) Decision {
	d := Decision{
		CurrentTrack: currentTrack,
		FileCount:    len(files),
	}
	if len(files) > 0 {
		d.FeatureID = files[0].FeatureID
	}

	if len(files) == 0 {
		d.Reason = "no-attribution"
		return d
	}

	counts := map[string]int{}
	matched := 0
	for _, f := range files {
		if track := bestTrackForPath(rules, f.FilePath); track != "" {
			counts[track]++
			matched++
		}
	}

	if matched == 0 {
		d.Reason = "no-match"
		return d
	}

	dominantTrack, dominantCount := topTrack(counts)
	share := float64(dominantCount) / float64(len(files))
	d.ProposedTrack = dominantTrack
	d.DominantShare = share

	// Ambiguity check runs first — a low-confidence dominance shouldn't be
	// reported as settled just because the feature happens to already live on
	// the dominant track. A 30% share is ambiguous regardless of where the
	// feature is currently attributed.
	if share < threshold {
		d.Ambiguous = true
		d.Reason = "ambiguous"
		return d
	}
	if currentTrack == dominantTrack {
		d.Reason = "no-change"
		return d
	}
	d.Reason = "confident"
	return d
}

// ClassifyAll iterates every feature in the project, loads its feature_files
// rows from the read index, and classifies each.
func ClassifyAll(database *sql.DB, projectDir string, rules *RuleSet, threshold float64, types []string) ([]Decision, error) {
	wantFeatures := false
	wantBugs := false
	for _, t := range types {
		switch t {
		case "features":
			wantFeatures = true
		case "bugs":
			wantBugs = true
		default:
			return nil, fmt.Errorf("unknown type %q (use features or bugs)", t)
		}
	}
	if !wantFeatures && !wantBugs {
		return nil, fmt.Errorf("--types must include at least one of: features, bugs")
	}

	var decisions []Decision
	if wantFeatures {
		ds, err := classifyByType(database, "feature", rules, threshold)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, ds...)
	}
	if wantBugs {
		ds, err := classifyByType(database, "bug", rules, threshold)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, ds...)
	}
	return decisions, nil
}

// classifyByType loads (id, track_id) for every work item of the given type
// and runs the classifier against each one's feature_files rows.
func classifyByType(database *sql.DB, typeName string, rules *RuleSet, threshold float64) ([]Decision, error) {
	rows, err := database.QueryContext(context.Background(),
		`SELECT id, COALESCE(track_id, '') FROM features WHERE type = ?`, typeName)
	if err != nil {
		return nil, fmt.Errorf("list %ss: %w", typeName, err)
	}
	defer rows.Close()

	var ids []string
	tracks := map[string]string{}
	for rows.Next() {
		var id, trackID string
		if err := rows.Scan(&id, &trackID); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		tracks[id] = trackID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Decision, 0, len(ids))
	for _, id := range ids {
		files, err := db.ListFilesByFeature(database, id)
		if err != nil {
			return nil, fmt.Errorf("load files for %s: %w", id, err)
		}
		d := ClassifyFeature(rules, files, tracks[id], threshold)
		// ClassifyFeature pulls FeatureID from files[0] when present; ensure
		// we record the id even when files is empty.
		d.FeatureID = id
		d.ItemType = typeName
		out = append(out, d)
	}
	return out, nil
}

// SortDecisions reorders Decisions for human-readable output: ambiguous
// first (most likely to need review), then by feature ID alphabetically.
func SortDecisions(ds []Decision) {
	sort.SliceStable(ds, func(i, j int) bool {
		ai, aj := ds[i].Ambiguous, ds[j].Ambiguous
		if ai != aj {
			return ai && !aj
		}
		return ds[i].FeatureID < ds[j].FeatureID
	})
}

// Summary aggregates Decisions into headline counts.
type Summary struct {
	Total         int
	Confident     int
	Ambiguous     int
	NoChange      int
	NoAttribution int
	NoMatch       int
}

// Summarize counts Decisions by Reason.
func Summarize(ds []Decision) Summary {
	s := Summary{Total: len(ds)}
	for _, d := range ds {
		switch d.Reason {
		case "confident":
			s.Confident++
		case "ambiguous":
			s.Ambiguous++
		case "no-change":
			s.NoChange++
		case "no-attribution":
			s.NoAttribution++
		case "no-match":
			s.NoMatch++
		}
	}
	return s
}

// bestTrackForPath returns the track ID of the highest-priority rule that
// matches path, or "" if no rule matches. Tries the original path first,
// then a normalized (repo-relative) variant for paths captured as absolute
// (e.g. from worktree-aware tool calls).
func bestTrackForPath(rules *RuleSet, p string) string {
	candidates := []string{p}
	if norm := normalizePath(p); norm != p {
		candidates = append(candidates, norm)
	}
	bestPriority := -1 << 30
	bestTrack := ""
	for _, r := range rules.Rules {
		matched := false
		for _, cand := range candidates {
			if matchGlob(r.Glob, cand) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if r.Priority > bestPriority {
			bestPriority = r.Priority
			bestTrack = r.TrackID
		}
	}
	return bestTrack
}

// normalizePath strips the absolute prefix from a path captured by tool
// hooks running inside a worktree, returning a repo-relative path so glob
// rules written against repo-relative globs (cmd/wipnote/blame.go) still
// match. Paths already relative are returned unchanged.
//
// Strategy:
//  1. If the path contains "/.claude/worktrees/<name>/", strip up to and
//     including that segment — this is the canonical worktree prefix layout.
//  2. Otherwise, find the first occurrence of any top-level repo directory
//     anchor (cmd/, internal/, plugin/, etc.) and strip everything before it.
//  3. If neither matches, return the path unchanged.
func normalizePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return p
	}
	if rest, ok := stripWorktreePrefix(p); ok {
		p = rest
		if !strings.HasPrefix(p, "/") {
			return p
		}
	}
	for _, anchor := range repoAnchors {
		needle := "/" + anchor
		if idx := strings.Index(p, needle); idx >= 0 {
			return p[idx+1:]
		}
	}
	return p
}

// stripWorktreePrefix strips a leading ".../.claude/worktrees/<name>/"
// segment from p, returning the remainder and true on success. Returns the
// original path and false when the marker is absent.
func stripWorktreePrefix(p string) (string, bool) {
	const marker = "/.claude/worktrees/"
	idx := strings.Index(p, marker)
	if idx < 0 {
		return p, false
	}
	tail := p[idx+len(marker):]
	slash := strings.Index(tail, "/")
	if slash < 0 {
		return p, false
	}
	return tail[slash+1:], true
}

// repoAnchors are the top-level directory names that mark the start of a
// repo-relative path. Order matters only when paths contain multiple — the
// first match wins, so list more-specific anchors before broader ones.
var repoAnchors = []string{
	"cmd/",
	"internal/",
	"plugin/",
	"packages/",
	"docs/",
	"scripts/",
	"web/",
	".githooks/",
}

// topTrack returns the track with the highest count. Ties broken by track ID
// (deterministic).
func topTrack(counts map[string]int) (string, int) {
	bestCount := -1
	bestTrack := ""
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if counts[k] > bestCount {
			bestCount = counts[k]
			bestTrack = k
		}
	}
	return bestTrack, bestCount
}

// matchGlob implements a small subset of glob syntax used by the rule catalog:
//
//   - — matches any sequence of non-/ characters within a single segment
//     **  — matches any sequence of characters including / boundaries
//
// Other characters are matched literally. Use this rather than path.Match,
// which doesn't support **.
func matchGlob(pattern, p string) bool {
	// Fast path: no wildcards.
	if !strings.ContainsAny(pattern, "*?") {
		return pattern == p
	}
	// Handle ** by splitting on it.
	if strings.Contains(pattern, "**") {
		parts := strings.SplitN(pattern, "**", 2)
		prefix, suffix := parts[0], parts[1]
		if !strings.HasPrefix(p, prefix) {
			return false
		}
		rest := p[len(prefix):]
		// suffix may itself contain * patterns — recurse, but only against
		// the tail of `rest`. Try every possible split point.
		if suffix == "" {
			return true
		}
		// Walk all suffixes of rest.
		for i := 0; i <= len(rest); i++ {
			if matchGlob(suffix, rest[i:]) {
				return true
			}
		}
		return false
	}
	// No **: defer to path.Match which handles * (single segment only).
	ok, err := path.Match(pattern, p)
	if err != nil {
		return false
	}
	return ok
}
