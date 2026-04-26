package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func traceCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "trace <commit-sha | file-path | feat-id | bug-id | spk-id>",
		Short: "Trace a commit, file, or feature to its related work items",
		Long: `Takes a commit SHA, file path, or work item ID and returns attribution:

  trace <commit-sha>  — session, feature, and track for a commit
  trace <file-path>   — all features that touched the file, with tracks
  trace <feat-id>     — commits, sessions, and files for a feature (forward)
  trace <bug-id>      — commits, sessions, and files for a bug (forward)
  trace <spk-id>      — commits, sessions, and files for a spike (forward)

Examples:
  htmlgraph trace abc1234
  htmlgraph trace internal/db/schema.go
  htmlgraph trace feat-046e2e03
  htmlgraph trace feat-046e2e03 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTrace(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON output")
	return cmd
}

// commitSHARe matches valid commit SHA hashes (7-40 hex characters).
var commitSHARe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// workItemIDRe matches feature/bug/spike IDs: feat-, bug-, or spk- followed
// by exactly 8 hex chars. The 8-hex suffix is produced by the ID generators
// in internal/models (see GenerateFeatureID / GenerateBugID / GenerateSpikeID).
// If that generation scheme ever changes, update this regex in lockstep or
// work-item IDs will silently fall through to the file-path branch.
var workItemIDRe = regexp.MustCompile(`^(feat|bug|spk)-[0-9a-f]{8}$`)

// looksLikeFilePath returns true when the argument looks like a file path
// rather than a commit SHA. File paths contain "/" or "." (except lone hex).
func looksLikeFilePath(s string) bool {
	return !commitSHARe.MatchString(s)
}

// looksLikeWorkItemID returns true when the argument is a work item ID prefix.
func looksLikeWorkItemID(s string) bool {
	return workItemIDRe.MatchString(s)
}

func runTrace(arg string, jsonOut bool) error {
	if looksLikeWorkItemID(arg) {
		dir, err := findHtmlgraphDir()
		if err != nil {
			return err
		}
		dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
		if err != nil {
			return fmt.Errorf("resolve db path: %w", err)
		}
		database, err := dbpkg.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()
		if jsonOut {
			return runTraceFeatureJSON(os.Stdout, database, arg)
		}
		return runTraceFeature(os.Stdout, database, arg)
	}
	if looksLikeFilePath(arg) {
		return runTraceFile(arg, jsonOut)
	}
	return runTraceCommit(arg, jsonOut)
}

// traceCommitJSON is the structured output schema for commit tracing.
type traceCommitJSON struct {
	Query   string            `json:"query"`
	Results []traceCommitHit  `json:"results"`
}

type traceCommitHit struct {
	Commit   string `json:"commit"`
	Message  string `json:"message,omitempty"`
	Session  string `json:"session,omitempty"`
	Feature  string `json:"feature,omitempty"`
	Track    string `json:"track,omitempty"`
}

func runTraceCommit(sha string, jsonOut bool) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	commits, err := dbpkg.TraceCommit(database, sha)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("commit %s not found in git_commits table\nRun 'htmlgraph ingest commits' to import git history", sha)
	}

	if jsonOut {
		out := traceCommitJSON{Query: sha, Results: make([]traceCommitHit, 0, len(commits))}
		for _, c := range commits {
			out.Results = append(out.Results, traceCommitHit{
				Commit:  c.CommitHash,
				Message: c.Message,
				Session: c.SessionID,
				Feature: c.FeatureID,
				Track:   c.TrackID,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Trace: %s\n", truncate(sha, 10))
	fmt.Println(sep)

	for _, c := range commits {
		fmt.Printf("  Commit    %s\n", truncate(c.CommitHash, 10))
		if c.Message != "" {
			fmt.Printf("  Message   %s\n", truncate(c.Message, 55))
		}
		fmt.Printf("  Session   %s\n", c.SessionID)
		if c.FeatureID != "" {
			fmt.Printf("  Feature   %s\n", c.FeatureID)
		}
		if c.TrackID != "" {
			fmt.Printf("  Track     %s\n", c.TrackID)
		}
		if len(commits) > 1 {
			fmt.Println()
		}
	}
	return nil
}

// traceFileJSON is the structured output schema for file tracing.
type traceFileJSON struct {
	Query    string         `json:"query"`
	Features []traceFileHit `json:"features"`
	Tracks   []string       `json:"tracks,omitempty"`
	Owner    string         `json:"owner,omitempty"`
}

type traceFileHit struct {
	FeatureID string `json:"feature_id"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	TrackID   string `json:"track_id,omitempty"`
	Operation string `json:"operation,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

func runTraceFile(filePath string, jsonOut bool) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	dbPath, err := storage.CanonicalDBPath(filepath.Dir(dir))
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	results, err := dbpkg.TraceFile(database, filePath)
	if err != nil {
		return err
	}

	if jsonOut {
		out := traceFileJSON{Query: filePath, Features: make([]traceFileHit, 0, len(results))}
		trackSet := make(map[string]bool)
		for _, r := range results {
			out.Features = append(out.Features, traceFileHit{
				FeatureID: r.FeatureID,
				Title:     r.Title,
				Status:    r.Status,
				TrackID:   r.TrackID,
				Operation: r.Operation,
				LastSeen:  r.LastSeen,
			})
			if r.TrackID != "" {
				trackSet[r.TrackID] = true
			}
		}
		for t := range trackSet {
			out.Tracks = append(out.Tracks, t)
		}
		// Deterministic order so the JSON payload is snapshot-stable across runs.
		sort.Strings(out.Tracks)
		if owner := dbpkg.ResolveFileOwner(database, filePath); owner != nil {
			out.Owner = owner.FeatureID
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	fmt.Printf("  Trace: %s\n", filePath)
	fmt.Println(sep)

	if len(results) == 0 {
		fmt.Println("  No features found for this file.")
		fmt.Println("  Run 'htmlgraph reindex' to rebuild file attribution.")
		return nil
	}

	// Collect unique tracks.
	tracks := make(map[string]bool)
	for _, r := range results {
		if r.TrackID != "" {
			tracks[r.TrackID] = true
		}
	}

	fmt.Printf("\n  Features (%d):\n", len(results))
	for _, r := range results {
		status := r.Status
		if status == "" {
			status = "unknown"
		}
		fmt.Printf("    %s  [%s]  %s\n", r.FeatureID, status, truncate(r.Title, 40))
		if r.TrackID != "" {
			fmt.Printf("      Track: %s\n", r.TrackID)
		}
		fmt.Printf("      Op: %s  Last seen: %s\n", r.Operation, truncate(r.LastSeen, 19))
	}

	if len(tracks) > 0 {
		fmt.Printf("\n  Tracks (%d):\n", len(tracks))
		for trackID := range tracks {
			fmt.Printf("    %s\n", trackID)
		}
	}

	// Show the most likely owner.
	if owner := dbpkg.ResolveFileOwner(database, filePath); owner != nil {
		fmt.Printf("\n  Owner: %s", owner.FeatureID)
		if owner.Title != "" {
			fmt.Printf("  %s", truncate(owner.Title, 40))
		}
		fmt.Println()
	}

	return nil
}

// traceFeatureJSON is the structured JSON output schema for feature trace.
type traceFeatureJSON struct {
	Feature  string   `json:"feature"`
	Commits  []string `json:"commits"`
	Sessions []string `json:"sessions"`
	Files    []string `json:"files"`
}

// uniqueSessions returns the union of session IDs drawn from both git_commits
// and feature_files, preserving insertion order and dropping empties. Sessions
// that touched a feature through files without producing a commit are included.
func uniqueSessions(commits []models.GitCommit, files []models.FeatureFile) []string {
	seen := make(map[string]bool)
	var out []string
	for _, c := range commits {
		if c.SessionID != "" && !seen[c.SessionID] {
			seen[c.SessionID] = true
			out = append(out, c.SessionID)
		}
	}
	for _, f := range files {
		if f.SessionID != "" && !seen[f.SessionID] {
			seen[f.SessionID] = true
			out = append(out, f.SessionID)
		}
	}
	return out
}

// runTraceFeature writes a human-readable text tree for a feature's forward trace.
func runTraceFeature(w io.Writer, database *sql.DB, featureID string) error {
	commits, err := dbpkg.GetCommitsByFeature(database, featureID)
	if err != nil {
		return fmt.Errorf("get commits: %w", err)
	}

	files, err := dbpkg.ListFilesByFeature(database, featureID)
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	sessions := uniqueSessions(commits, files)

	sep := strings.Repeat("─", 60)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Trace: %s\n", featureID)
	fmt.Fprintln(w, sep)

	fmt.Fprintf(w, "\n  Commits (%d):\n", len(commits))
	for _, c := range commits {
		fmt.Fprintf(w, "    %s", truncate(c.CommitHash, 10))
		if c.Message != "" {
			fmt.Fprintf(w, "  %s", truncate(c.Message, 48))
		}
		fmt.Fprintln(w)
		if c.SessionID != "" {
			fmt.Fprintf(w, "      Session: %s\n", c.SessionID)
		}
	}

	fmt.Fprintf(w, "\n  Sessions (%d):\n", len(sessions))
	for _, sid := range sessions {
		fmt.Fprintf(w, "    %s\n", sid)
	}

	fmt.Fprintf(w, "\n  Files (%d):\n", len(files))
	for _, f := range files {
		fmt.Fprintf(w, "    %s  [%s]\n", f.FilePath, f.Operation)
	}

	return nil
}

// runTraceFeatureJSON writes structured JSON for a feature's forward trace.
func runTraceFeatureJSON(w io.Writer, database *sql.DB, featureID string) error {
	commits, err := dbpkg.GetCommitsByFeature(database, featureID)
	if err != nil {
		return fmt.Errorf("get commits: %w", err)
	}

	files, err := dbpkg.ListFilesByFeature(database, featureID)
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	sessions := uniqueSessions(commits, files)

	commitHashes := make([]string, 0, len(commits))
	for _, c := range commits {
		commitHashes = append(commitHashes, c.CommitHash)
	}

	filePaths := make([]string, 0, len(files))
	for _, f := range files {
		filePaths = append(filePaths, f.FilePath)
	}

	out := traceFeatureJSON{
		Feature:  featureID,
		Commits:  commitHashes,
		Sessions: sessions,
		Files:    filePaths,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
