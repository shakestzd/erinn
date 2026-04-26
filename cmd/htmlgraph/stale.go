package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/spf13/cobra"
)

const defaultStaleThreshold = 72 * time.Hour

// sweepCommitPattern matches commit subjects that are metadata sweeps —
// state-hygiene commits that touch many work-item HTML files at once
// without representing real development on any single item. These are
// excluded from "real activity" so an item's mtime cannot mask staleness.
var sweepCommitPattern = regexp.MustCompile(
	`(?i)(^|\s)(chore[:(]|catch up work-item|roborev metadata|metadata fix|metadata sweep)`,
)

// StaleItem describes an in-progress work item that has no recent
// real (non-sweep) commit referencing its ID.
type StaleItem struct {
	ItemID      string        `json:"item_id"`
	Type        string        `json:"type"`
	Title       string        `json:"title"`
	LastCommit  time.Time     `json:"last_commit,omitempty"`
	LastSubject string        `json:"last_subject,omitempty"`
	AgeSeconds  float64       `json:"age_seconds"`
	Reason      string        `json:"reason"`
	Age         time.Duration `json:"-"`
}

func staleCmd() *cobra.Command {
	var (
		threshold     time.Duration
		format        string
		includeSweeps bool
	)
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "Detect in-progress items with no recent commit activity",
		Long: `Cross-references in-progress work items against git commit messages
to identify items that haven't seen real development activity within the
threshold (default 72h).

Unlike the analytics that uses HTML file mtime — which gets refreshed by
metadata-sweep commits ("chore: catch up work-item state") — this scans
for commits whose subject references the work-item ID and ignores known
sweep patterns. An item is stale when no real commit references its ID
within the threshold (or no commit references it at all).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runStale(threshold, format, includeSweeps)
		},
	}
	cmd.Flags().DurationVar(&threshold, "threshold", defaultStaleThreshold, "Age threshold (e.g. 72h, 168h)")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table or json")
	cmd.Flags().BoolVar(&includeSweeps, "include-sweeps", false, "Treat metadata-sweep commits as real activity (debug)")
	return cmd
}

func runStale(threshold time.Duration, format string, includeSweeps bool) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}
	items, err := scanInProgress(dir)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(dir)

	stale := computeStaleItems(projectDir, items, threshold, includeSweeps, time.Now().UTC())

	switch strings.ToLower(format) {
	case "json":
		return writeStaleJSON(os.Stdout, stale)
	case "table", "":
		return writeStaleTable(os.Stdout, stale, threshold, len(items))
	default:
		return fmt.Errorf("unknown format %q (use table or json)", format)
	}
}

// computeStaleItems is the testable core: returns in-progress items that
// have no real commit referencing their ID within threshold.
func computeStaleItems(
	projectDir string,
	items []*models.Node,
	threshold time.Duration,
	includeSweeps bool,
	now time.Time,
) []StaleItem {
	var out []StaleItem
	for _, n := range items {
		ts, subject := lastRealCommitForItem(projectDir, n.ID, includeSweeps)
		var age time.Duration
		var reason string
		if ts.IsZero() {
			age = now.Sub(n.UpdatedAt)
			reason = "no commit references this item ID"
		} else {
			age = now.Sub(ts)
			if age < threshold {
				continue
			}
			reason = fmt.Sprintf("last real commit %s ago: %q", formatAge(age), truncate(subject, 60))
		}
		out = append(out, StaleItem{
			ItemID:      n.ID,
			Type:        n.Type,
			Title:       n.Title,
			LastCommit:  ts,
			LastSubject: subject,
			AgeSeconds:  age.Seconds(),
			Age:         age,
			Reason:      reason,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Age > out[j].Age
	})
	return out
}

// lastRealCommitForItem returns the timestamp + subject of the most recent
// commit referencing itemID that is not a metadata sweep. Returns zero time
// when no such commit exists.
func lastRealCommitForItem(projectDir, itemID string, includeSweeps bool) (time.Time, string) {
	commits, err := gitCommitsReferencing(projectDir, itemID)
	if err != nil || len(commits) == 0 {
		return time.Time{}, ""
	}
	for _, c := range commits {
		if !includeSweeps && isSweepCommit(c.Subject) {
			continue
		}
		return c.Timestamp, c.Subject
	}
	return time.Time{}, ""
}

func isSweepCommit(subject string) bool {
	return sweepCommitPattern.MatchString(subject)
}

func writeStaleTable(w io.Writer, items []StaleItem, threshold time.Duration, totalInProgress int) error {
	fmt.Fprintf(w, "Stale in-progress items (threshold: %s, scanned: %d)\n\n", threshold, totalInProgress)
	if len(items) == 0 {
		fmt.Fprintln(w, "No stale items found.")
		return nil
	}
	fmt.Fprintf(w, "%-22s  %-8s  %-6s  %s\n", "ID", "TYPE", "AGE", "TITLE")
	fmt.Fprintln(w, strings.Repeat("-", 90))
	for _, it := range items {
		fmt.Fprintf(w, "%-22s  %-8s  %-6s  %s\n",
			it.ItemID, it.Type, formatAge(it.Age), truncate(it.Title, 50))
		fmt.Fprintf(w, "%-22s  %-8s  %-6s    %s\n", "", "", "", it.Reason)
	}
	return nil
}

func writeStaleJSON(w io.Writer, items []StaleItem) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func formatAge(d time.Duration) string {
	h := int(d.Hours())
	if h >= 24 {
		return fmt.Sprintf("%dd", h/24)
	}
	if h < 0 {
		return "0h"
	}
	return fmt.Sprintf("%dh", h)
}
