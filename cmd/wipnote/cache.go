package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and prune the SQLite read-index cache",
		Long: `The SQLite read-index lives in the OS user cache directory, keyed by
project-path hash. Cache files are derived state — losing them is harmless
(the indexer rebuilds). These subcommands report and reclaim disk usage.`,
	}
	cmd.AddCommand(cachePruneCmd())
	cmd.AddCommand(cacheStatsCmd())
	return cmd
}

func cachePruneCmd() *cobra.Command {
	var (
		dryRun  bool
		maxAge  time.Duration
		maxSize int64
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Evict cache subdirs older than --max-age or beyond --max-size",
		Long: `Removes per-project cache subdirs from the user cache directory.
Eviction runs in two passes: first by age (anything older than --max-age),
then by LRU until the surviving total fits in --max-size.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := storage.CacheRoot()
			if err != nil {
				return err
			}
			opts := storage.EvictOptions{
				MaxAge:    maxAge,
				MaxSize:   maxSize,
				DryRun:    dryRun,
				Protected: protectedForCacheCmd(),
			}
			res, err := storage.Evict(root, opts)
			if err != nil {
				return err
			}
			return printPruneResult(cmd.OutOrStdout(), root, res)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed without deleting")
	cmd.Flags().DurationVar(&maxAge, "max-age", storage.DefaultMaxAge, "evict cache dirs older than this duration")
	cmd.Flags().Int64Var(&maxSize, "max-size", storage.DefaultMaxSize, "evict LRU dirs until total size fits in this many bytes")
	return cmd
}

func cacheStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "List per-project cache size and last-modified time",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := storage.CacheRoot()
			if err != nil {
				return err
			}
			entries, err := storage.CacheStats(root)
			if err != nil {
				return err
			}
			return printCacheStats(cmd.OutOrStdout(), root, entries)
		},
	}
}

func printPruneResult(w io.Writer, root string, res storage.EvictResult) error {
	verb := "Removed"
	if res.DryRun {
		verb = "Would remove"
	}
	fmt.Fprintf(w, "Cache root: %s\n", root)
	fmt.Fprintf(w, "%s %d cache dir(s), freed %s — %d kept (%s)\n",
		verb, len(res.Removed), humanBytes(res.BytesFreed),
		res.RemainingDirs, humanBytes(res.RemainingBytes))
	for _, p := range res.Removed {
		fmt.Fprintf(w, "  %s\n", p)
	}
	return nil
}

func printCacheStats(w io.Writer, root string, entries []storage.CacheEntry) error {
	fmt.Fprintf(w, "Cache root: %s\n", root)
	if len(entries) == 0 {
		fmt.Fprintln(w, "  (empty)")
		return nil
	}
	var total int64
	for _, e := range entries {
		total += e.Size
	}
	fmt.Fprintf(w, "  %d project(s), %s total\n\n", len(entries), humanBytes(total))
	fmt.Fprintf(w, "  %-16s  %10s  %s\n", "HASH", "SIZE", "LAST USE")
	for _, e := range entries {
		age := time.Since(e.ModTime).Round(time.Second)
		fmt.Fprintf(w, "  %s  %10s  %s ago\n", e.Hash, humanBytes(e.Size), age)
	}
	return nil
}

// protectedForCacheCmd returns the active project's cache dir as a one-element
// slice for EvictOptions.Protected, so an explicit `wipnote cache prune`
// can never delete the read-index of the very project the operator is in.
// Returns nil when the project root or its cache path can't be resolved.
func protectedForCacheCmd() []string {
	hgDir, err := findWipnoteDir()
	if err != nil {
		return nil
	}
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(hgDir))
	if err != nil {
		return nil
	}
	return []string{filepath.Dir(dbPath)}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	sym := "KMGTPE"[exp]
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), sym)
}
