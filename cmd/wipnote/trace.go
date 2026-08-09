package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/spf13/cobra"
)

func traceCmd() *cobra.Command {
	var jsonOut bool
	var testsOnly bool
	cmd := &cobra.Command{
		Use:   "trace <commit-sha | file-path | feat-id | bug-id | spk-id>",
		Short: "Trace a commit, file, or feature to its related work items",
		Long: `Takes a commit SHA, file path, or work item ID and returns attribution:

  trace <commit-sha>  — session, feature, and track for a commit
  trace <file-path>   — all features that touched the file, with tracks
  trace <feat-id>     — commits, sessions, and files for a feature (forward)
  trace <bug-id>      — commits, sessions, and files for a bug (forward)
  trace <spk-id>      — commits, sessions, and files for a spike (forward)

--tests narrows a work-item trace to the test files it touched. This is
touched-by, not owned-by: feature_files is a many-to-many table (UNIQUE on
feature_id+file_path, not on file_path alone), so it answers "which test
files did this item touch," never "which tests verify this item." To go
the other direction — given a test file, which work items touched it —
run 'wipnote trace <test-file-path>'; the existing file-path route already
covers test files, no flag needed.

Examples:
  wipnote trace abc1234
  wipnote trace internal/db/schema.go
  wipnote trace feat-046e2e03
  wipnote trace feat-046e2e03 --json
  wipnote trace feat-046e2e03 --tests`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runTrace(args[0], jsonOut, testsOnly)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON output")
	cmd.Flags().BoolVar(&testsOnly, "tests", false, "limit a work-item trace to its touched test files (touched-by, not owned-by)")
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

// testFilePathRe recognizes common test-file naming conventions across the
// languages present in this repo: Go's _test.go suffix, Python's test_*.py /
// *_test.py, and the *.test.js|ts / *.spec.js|ts convention used by JS/TS
// tooling. feature_files stores whatever path a session touched — there is
// no "is this a test" flag in the schema — so this is a path-naming
// heuristic, not a semantic oracle. It only decides whether a file *looks*
// like a test by name.
var testFilePathRe = regexp.MustCompile(`(?i)(_test\.go|(^|/)test_[^/]+\.py|_test\.py|\.(test|spec)\.[jt]sx?)$`)

// isTestFilePath returns true when path matches a recognized test-file naming
// convention.
func isTestFilePath(path string) bool {
	return testFilePathRe.MatchString(path)
}

func runTrace(arg string, jsonOut, testsOnly bool) error {
	if looksLikeWorkItemID(arg) {
		dir, err := findWipnoteDir()
		if err != nil {
			return err
		}
		database, err := openReadOnlyDB(dir)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()
		if testsOnly {
			if jsonOut {
				return runTraceFeatureTestsJSON(os.Stdout, database, arg)
			}
			return runTraceFeatureTests(os.Stdout, database, arg)
		}
		if jsonOut {
			return runTraceFeatureJSON(os.Stdout, database, arg)
		}
		return runTraceFeature(os.Stdout, database, arg)
	}
	if testsOnly {
		return fmt.Errorf("--tests only applies to a work-item ID (feat-/bug-/spk-), not %q", arg)
	}
	if looksLikeFilePath(arg) {
		return runTraceFile(arg, jsonOut)
	}
	return runTraceCommit(arg, jsonOut)
}

// traceCommitJSON is the structured output schema for commit tracing.
type traceCommitJSON struct {
	Query   string           `json:"query"`
	Results []traceCommitHit `json:"results"`
}

type traceCommitHit struct {
	Commit  string `json:"commit"`
	Message string `json:"message,omitempty"`
	Session string `json:"session,omitempty"`
	Feature string `json:"feature,omitempty"`
	Track   string `json:"track,omitempty"`
}

func runTraceCommit(sha string, jsonOut bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	database, err := openReadOnlyDB(dir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	// bug-7dbaf552: dbpkg.TraceCommit fully materialises its result slice and
	// closes its own *sql.Rows before returning, so there is no read lock to
	// leak across retries. Wrap the (idempotent, handle-free) call in
	// RetryOnBusy so a transient SQLITE_BUSY under contention doesn't fail
	// `wipnote trace <sha>`.
	var commits []dbpkg.TraceResult
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		c, derr := dbpkg.TraceCommit(database, sha)
		if derr != nil {
			return derr
		}
		commits = c
		return nil
	}); err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("commit %s not found in git_commits table\nRun 'wipnote ingest commits' to import git history", sha)
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
	IsTest   bool           `json:"is_test_file,omitempty"`
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
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	database, err := openReadOnlyDB(dir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	// bug-7dbaf552: same discipline as runTraceCommit — TraceFile returns a
	// materialised slice with no caller-held *sql.Rows, so RetryOnBusy can
	// safely re-run the whole call on a transient SQLITE_BUSY.
	var results []dbpkg.FileTraceResult
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		r, derr := dbpkg.TraceFile(database, filePath)
		if derr != nil {
			return derr
		}
		results = r
		return nil
	}); err != nil {
		return err
	}

	if jsonOut {
		out := traceFileJSON{Query: filePath, IsTest: isTestFilePath(filePath), Features: make([]traceFileHit, 0, len(results))}
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
	if isTestFilePath(filePath) {
		fmt.Println("  (test file)")
	}
	fmt.Println(sep)

	if len(results) == 0 {
		fmt.Println("  No features found for this file.")
		fmt.Println("  Run 'wipnote reindex' to rebuild file attribution.")
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

// filesByFeature loads a feature's feature_files rows under RetryOnBusy.
// bug-7dbaf552: ListFilesByFeature fully materialises its slice and closes
// its own *sql.Rows before returning, so re-running the whole call on a
// transient SQLITE_BUSY leaks no read lock. Shared by traceFeatureData and
// the --tests-only feature trace, so both paths see the same rows.
func filesByFeature(database *sql.DB, featureID string) ([]models.FeatureFile, error) {
	var files []models.FeatureFile
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		f, derr := dbpkg.ListFilesByFeature(database, featureID)
		if derr != nil {
			return derr
		}
		files = f
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	return files, nil
}

// traceFeatureData loads a feature's commits and files, each fetch wrapped in
// RetryOnBusy. bug-7dbaf552: GetCommitsByFeature fully materialises its slice
// and closes its own *sql.Rows before returning, so re-running it on a
// transient SQLITE_BUSY leaks no read lock. Shared by the text and JSON
// feature-trace renderers (DRY).
func traceFeatureData(database *sql.DB, featureID string) ([]models.GitCommit, []models.FeatureFile, error) {
	var commits []models.GitCommit
	if err := dbpkg.RetryOnBusy(dbpkg.DefaultBusyBackoff, func() error {
		c, derr := dbpkg.GetCommitsByFeature(database, featureID)
		if derr != nil {
			return derr
		}
		commits = c
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("get commits: %w", err)
	}

	files, err := filesByFeature(database, featureID)
	if err != nil {
		return nil, nil, err
	}
	return commits, files, nil
}

// filterTestFiles returns the subset of files whose path looks like a test
// file (see isTestFilePath). Order is preserved from the input.
func filterTestFiles(files []models.FeatureFile) []models.FeatureFile {
	out := make([]models.FeatureFile, 0, len(files))
	for _, f := range files {
		if isTestFilePath(f.FilePath) {
			out = append(out, f)
		}
	}
	return out
}

// testFilesSemanticsNote documents that feature_files — and therefore this
// view — is touched-by, not owned-by. UNIQUE(feature_id, file_path) makes
// the table many-to-many by design (cmd/wipnote/sqlite_write_boundary_test.go
// carries a single file under 10 distinct feature IDs), so the same test
// file can legitimately be linked to several work items. This answers
// "which test files did this item touch," never "which tests verify this
// item" — there is no per-test-function or pass/fail data in this table.
const testFilesSemanticsNote = "Note: touched-by, not owned-by. feature_files links a file to every work " +
	"item whose session/commit touched it (many-to-many), so this lists test files this item touched — " +
	"not tests that verify this item's behavior."

// runTraceFeature writes a human-readable text tree for a feature's forward trace.
func runTraceFeature(w io.Writer, database *sql.DB, featureID string) error {
	commits, files, err := traceFeatureData(database, featureID)
	if err != nil {
		return err
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
	commits, files, err := traceFeatureData(database, featureID)
	if err != nil {
		return err
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

// runTraceFeatureTests writes a human-readable list of the test files a work
// item touched — the same feature_files rows runTraceFeature lists under
// "Files", filtered to test-looking paths. See testFilesSemanticsNote for why
// this is touched-by, not owned-by.
func runTraceFeatureTests(w io.Writer, database *sql.DB, featureID string) error {
	files, err := filesByFeature(database, featureID)
	if err != nil {
		return err
	}
	testFiles := filterTestFiles(files)

	sep := strings.Repeat("─", 60)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Trace: %s (test files)\n", featureID)
	fmt.Fprintln(w, sep)

	fmt.Fprintf(w, "\n  Test files (%d):\n", len(testFiles))
	for _, f := range testFiles {
		fmt.Fprintf(w, "    %s  [%s]\n", f.FilePath, f.Operation)
	}

	fmt.Fprintf(w, "\n  %s\n", testFilesSemanticsNote)
	return nil
}

// traceFeatureTestsJSON is the structured JSON output schema for a
// work item's --tests trace.
type traceFeatureTestsJSON struct {
	Feature   string             `json:"feature"`
	TestFiles []traceTestFileHit `json:"test_files"`
	Semantics string             `json:"semantics"`
}

type traceTestFileHit struct {
	FilePath  string `json:"file_path"`
	Operation string `json:"operation"`
	SessionID string `json:"session_id,omitempty"`
}

// runTraceFeatureTestsJSON writes structured JSON for a work item's touched
// test files. The semantics field carries testFilesSemanticsNote so
// automation consuming --json can't miss the touched-by-not-owned-by caveat
// the way it could if the note were print-only.
func runTraceFeatureTestsJSON(w io.Writer, database *sql.DB, featureID string) error {
	files, err := filesByFeature(database, featureID)
	if err != nil {
		return err
	}
	testFiles := filterTestFiles(files)

	out := traceFeatureTestsJSON{
		Feature:   featureID,
		TestFiles: make([]traceTestFileHit, 0, len(testFiles)),
		Semantics: testFilesSemanticsNote,
	}
	for _, f := range testFiles {
		out.TestFiles = append(out.TestFiles, traceTestFileHit{
			FilePath:  f.FilePath,
			Operation: f.Operation,
			SessionID: f.SessionID,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
