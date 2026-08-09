// Register in main.go: rootCmd.AddCommand(wipCmd())
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/sessionledger"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// wipPerSessionSoftLimit is the per-owner-session advisory threshold.
// A session owning this many or more in-progress items is flagged [SOFT LIMIT].
const wipPerSessionSoftLimit = 3

// wipGlobalAdvisoryLimit is the global advisory threshold across all sessions.
// When total in-progress items reach this value the display shows [ADVISORY].
const wipGlobalAdvisoryLimit = 10

func wipCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wip",
		Short: "Manage WIP (work-in-progress) limits",
	}
	cmd.AddCommand(wipShowCmd())
	cmd.AddCommand(wipResetCmd())
	return cmd
}

// wipShowCmd displays in-progress items against the WIP limit.
func wipShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current WIP count and in-progress items",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runWipShow()
		},
	}
}

func runWipShow() error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	items, err := scanInProgress(dir)
	if err != nil {
		return err
	}

	// Resolve live session IDs from the authoritative session ledger.
	liveSessions := wipLiveSessions(dir)

	// Group items by owner session (the implemented_in edge target).
	bySession := wipGroupBySession(items)

	globalStatus := "OK"
	if len(items) >= wipGlobalAdvisoryLimit {
		globalStatus = fmt.Sprintf("ADVISORY — global advisory limit %d", wipGlobalAdvisoryLimit)
	}
	fmt.Printf("WIP: %d total  [%s]\n", len(items), globalStatus)

	if len(items) == 0 {
		fmt.Println("\nNo in-progress work items.")
		return nil
	}

	// Print per-session summary table.
	fmt.Println()
	fmt.Printf("%-24s  %-5s  %s\n", "SESSION", "ITEMS", "STATUS")
	fmt.Println(strings.Repeat("-", 50))

	// Stable ordering: sessions sorted, "unknown" always last.
	sessionKeys := wipSortedSessionKeys(bySession)
	for _, sess := range sessionKeys {
		sessItems := bySession[sess]
		sessStatus := "OK"
		if sess != "unknown" {
			if len(sessItems) >= wipPerSessionSoftLimit {
				sessStatus = "SOFT LIMIT"
			}
			if !liveSessions[sess] {
				if sessStatus == "SOFT LIMIT" {
					sessStatus = "SOFT LIMIT  SESSION DEAD?"
				} else {
					sessStatus = "SESSION DEAD?"
				}
			}
		}
		display := truncate(sess, 22)
		fmt.Printf("%-24s  %-5d  %s\n", display, len(sessItems), sessStatus)
	}

	// Print per-item detail table with SESSION column.
	fmt.Println()
	fmt.Printf("%-22s  %-8s  %-16s  %s\n", "ID", "TYPE", "SESSION", "TITLE")
	fmt.Println(strings.Repeat("-", 80))
	for _, sess := range sessionKeys {
		for _, n := range bySession[sess] {
			sessDisplay := truncate(sess, 16)
			if sess != "unknown" && !liveSessions[sess] {
				sessDisplay = truncate(sess, 8) + " [dead?]"
			}
			fmt.Printf("%-22s  %-8s  %-16s  %s\n", n.ID, n.Type, sessDisplay, truncate(n.Title, 36))
		}
	}
	return nil
}

// wipLiveSessions returns the set of currently-active session IDs from the
// canonical session ledger.
// Returns an empty map (not nil) on any error so liveness checks degrade gracefully.
func wipLiveSessions(wipnoteDir string) map[string]bool {
	live := make(map[string]bool)
	sessions, err := sessionledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return live
	}
	for _, s := range sessions {
		if s.IsOpen() {
			live[s.SessionID] = true
		}
	}
	return live
}

// wipLatestImplementedInSession returns the TargetID of the most recent
// implemented_in edge by Edge.Since timestamp, or "unknown" when none exist.
// Items accumulate edges on restart/handoff; the latest edge reflects current
// ownership, not the first session that ever touched the item.
func wipLatestImplementedInSession(n *models.Node) string {
	edges, ok := n.Edges[string(models.RelImplementedIn)]
	if !ok || len(edges) == 0 {
		return "unknown"
	}
	latest := edges[0]
	for _, e := range edges[1:] {
		if e.Since.After(latest.Since) {
			latest = e
		}
	}
	return latest.TargetID
}

// wipGroupBySession groups in-progress nodes by the most-recent implemented_in
// session edge owner. Items with no such edge land in the "unknown" bucket.
func wipGroupBySession(items []*models.Node) map[string][]*models.Node {
	bySession := make(map[string][]*models.Node)
	for _, n := range items {
		sess := wipLatestImplementedInSession(n)
		bySession[sess] = append(bySession[sess], n)
	}
	return bySession
}

// wipSortedSessionKeys returns the session keys in stable alphabetical order,
// with "unknown" always last.
func wipSortedSessionKeys(bySession map[string][]*models.Node) []string {
	keys := make([]string, 0, len(bySession))
	for k := range bySession {
		if k != "unknown" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if _, ok := bySession["unknown"]; ok {
		keys = append(keys, "unknown")
	}
	return keys
}

// wipResetCmd marks all in-progress items as todo.
func wipResetCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset all in-progress items to todo (cleans stale WIP)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runWipReset(force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Required: confirm destructive reset")
	return cmd
}

func runWipReset(force bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	items, err := scanInProgress(dir)
	if err != nil {
		return err
	}

	if !force {
		count := len(items)
		return fmt.Errorf("%d items are in-progress. This will reset all to todo.\nRun 'wipnote wip reset --force' to confirm, or 'wipnote wip show' to review first.", count)
	}

	if len(items) == 0 {
		fmt.Println("No in-progress items found.")
		return nil
	}

	p, err := workitem.Open(dir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	for _, n := range items {
		if err := resetNodeToTodo(p, n); err != nil {
			fmt.Fprintf(os.Stderr, "warning: reset %s: %v\n", n.ID, err)
			continue
		}
		fmt.Printf("Reset: %s  %s\n", n.ID, truncate(n.Title, 50))
	}
	fmt.Printf("\n%d item(s) reset to todo\n", len(items))
	return nil
}

// resetNodeToTodo writes the node back with status=todo and cleared agent.
func resetNodeToTodo(p *workitem.Project, n *models.Node) error {
	n.Status = models.StatusTodo
	n.AgentAssigned = ""
	n.UpdatedAt = time.Now().UTC()
	dir := collectionDir(p, n.Type)
	_, err := workitem.WriteNodeHTML(dir, n)
	return err
}

// collectionDir maps a node type to its collection directory.
func collectionDir(p *workitem.Project, nodeType string) string {
	switch nodeType {
	case "bug":
		return p.BugsDir()
	case "spike":
		return p.SpikesDir()
	default: // "feature" and anything else
		return p.FeaturesDir()
	}
}

// scanInProgress collects all in-progress nodes across features, bugs, spikes.
func scanInProgress(wipnoteDir string) ([]*models.Node, error) {
	dirs := []struct {
		path     string
		nodeType string
	}{
		{filepath.Join(wipnoteDir, "features"), "feature"},
		{filepath.Join(wipnoteDir, "bugs"), "bug"},
		{filepath.Join(wipnoteDir, "spikes"), "spike"},
	}

	var items []*models.Node
	for _, d := range dirs {
		found, err := loadInProgressFromDir(d.path)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", d.nodeType, err)
		}
		items = append(items, found...)
	}
	return items, nil
}

// loadInProgressFromDir scans one directory for in-progress nodes.
func loadInProgressFromDir(dir string) ([]*models.Node, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []*models.Node
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		node, err := htmlparse.ParseFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // skip unparseable files
		}
		if node.Status == models.StatusInProgress {
			out = append(out, node)
		}
	}
	return out, nil
}
