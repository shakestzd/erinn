package main

import (
	"fmt"
	"io"
	"time"

	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Report and reclaim leftover SQLite read-index caches",
		Long: `wipnote no longer keeps a per-project SQLite read-index on disk; queries
run against a process-local in-memory projection rebuilt from the canonical
.wipnote/ artifacts. Installs that predate that change may still have
per-project cache directories in the OS user cache dir, keyed by project-path
hash, holding a wipnote.db and its WAL/SHM sidecars.

Those leftovers are never read and are never deleted automatically. These
subcommands exist so an operator can see them and reclaim the disk on
purpose. Everything they can delete is derived state — the canonical store
is .wipnote/, which lives in the repo and is never touched here.`,
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
		Short: "Evict leftover cache subdirs older than --max-age or beyond --max-size",
		Long: `Removes leftover per-project cache subdirs from the user cache directory.
Eviction runs in two passes: first by age (anything older than --max-age),
then by LRU until the surviving total fits in --max-size.

This is the only command that deletes them. Nothing prunes them for you: a
leftover cache is ignored, not reclaimed, until you run this.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := storage.CacheRoot()
			if err != nil {
				return err
			}
			// Nothing is protected. The old carve-out shielded the running
			// project's live read-index from its own operator; no project
			// has a live read-index on disk any more, so every subdir here
			// is a leftover the operator explicitly asked to reclaim.
			opts := storage.EvictOptions{
				MaxAge:  maxAge,
				MaxSize: maxSize,
				DryRun:  dryRun,
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
		Short: "List leftover per-project cache size and last-modified time",
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
