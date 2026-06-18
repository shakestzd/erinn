package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"
)

// Ranking weight constants — tunable.
const (
	weightFileMention  = 2.0 // each ripgrep hit inside HTML content
	weightCommitItem   = 3.0 // commit attributed to this item via wipnote-item trailer
	weightStatusActive = 1.5 // bonus for in-progress items
	weightStatusDone   = 0.2 // small weight for done items (still relevant as context)
)

// queryType classifies the user's query.
type queryType int

const (
	queryTypeKeyword queryType = iota
	queryTypeFile
	queryTypeSHA
)

// shaPattern matches 7–40 lowercase hex characters (git SHA prefix or full).
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// knownExtensions is used to classify path-like queries without a slash.
var knownExtensions = map[string]bool{
	".go": true, ".md": true, ".html": true, ".yaml": true, ".yml": true,
	".json": true, ".sh": true, ".ts": true, ".tsx": true, ".js": true,
	".py": true, ".toml": true, ".txt": true,
}

// citation is a source reference supporting a match.
type citation struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Date    string `json:"date,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// relevantResult is one ranked work-item match.
type relevantResult struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Score     float64    `json:"score"`
	Citations []citation `json:"citations"`
}

func relevantCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "relevant <query>",
		Short: "Find work items related to a file path, git SHA, or keyword",
		Long: `Retrieval-first search: one call returns ranked, cited work-item matches.

Query auto-detection:
  file path  — contains '/' or ends with a known extension, or the path exists
  git SHA    — 7–40 lowercase hex characters
  keyword    — anything else

Reads directly from .wipnote/*.html via ripgrep + git log.
SQLite is not consulted.

Examples:
  wipnote relevant cmd/wipnote/relevant.go
  wipnote relevant "retrieval"
  wipnote relevant abc1234`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRelevant(args[0], format)
		},
	}

	isTTY := isTerminal()
	defaultFmt := "json"
	if isTTY {
		defaultFmt = "text"
	}
	cmd.Flags().StringVar(&format, "format", defaultFmt, "Output format: json or text")
	return cmd
}

// isTerminal returns true when stdout is a TTY.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// detectQueryType classifies query as file path, git SHA, or keyword.
func detectQueryType(query string) queryType {
	// Explicit path separators.
	if strings.Contains(query, "/") {
		return queryTypeFile
	}
	// Known file extension suffix.
	ext := strings.ToLower(filepath.Ext(query))
	if ext != "" && knownExtensions[ext] {
		return queryTypeFile
	}
	// File exists on disk.
	if _, err := os.Stat(query); err == nil {
		return queryTypeFile
	}
	// Ends with slash (directory).
	if strings.HasSuffix(query, "/") {
		return queryTypeFile
	}
	// SHA pattern.
	if shaPattern.MatchString(strings.ToLower(query)) {
		return queryTypeSHA
	}
	return queryTypeKeyword
}

// runRelevant is the main entry point for the relevant command.
func runRelevant(query, format string) error {
	hgDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	qType := detectQueryType(query)
	results, err := runRelevantSearch(hgDir, query, qType)
	if err != nil {
		return err
	}

	results = rankResults(results)

	switch format {
	case "json":
		return printRelevantJSON(results)
	default:
		printRelevantText(results)
		return nil
	}
}

// runRelevantSearch performs the multi-source retrieval pipeline and returns
// unranked (or lightly scored) results. Exported for tests.
func runRelevantSearch(hgDir, query string, qType queryType) ([]relevantResult, error) {
	projectDir := filepath.Dir(hgDir)
	scores := make(map[string]*relevantResult)

	// Step 1: ripgrep search over HTML files.
	//
	// For keyword queries we tokenize on whitespace and call searchWithRipgrep
	// once per token, accumulating scores in the same map. A literal
	// multi-token phrase like "lineage review" matches nothing even when each
	// token individually matches several items, so we score tokens
	// independently. Items that match multiple tokens naturally rise to the
	// top via repeated weightFileMention additions.
	rgTokens := []string{query}
	switch qType {
	case queryTypeSHA:
		// SHAs are single tokens by definition.
		rgTokens = []string{query}
	case queryTypeFile:
		// File-path queries use the base name as the search token.
		rgTokens = []string{filepath.Base(query)}
	case queryTypeKeyword:
		rgTokens = tokenizeQuery(query)
	}

	for _, tok := range rgTokens {
		if tok == "" {
			continue
		}
		if err := searchWithRipgrep(hgDir, tok, scores); err != nil {
			if isCommandNotFound(err) {
				return nil, fmt.Errorf("ripgrep (rg) not found in PATH — install it: https://github.com/BurntSushi/ripgrep")
			}
			// Other errors are non-fatal: rg may have hit a transient issue. Continue with git-only attribution.
		}
	}

	// Step 2: git log attribution (commit trailer + file history).
	switch qType {
	case queryTypeFile:
		if err := searchViaGitFileHistory(projectDir, hgDir, query, scores); err != nil {
			// Non-fatal: git may not be available or file may be untracked.
			_ = err
		}
	case queryTypeSHA:
		if err := searchViaGitSHA(projectDir, hgDir, query, scores); err != nil {
			_ = err
		}
	}

	// Step 3: apply status bonuses to all collected items.
	for _, r := range scores {
		switch r.Status {
		case "in-progress":
			r.Score += weightStatusActive
		case "done":
			r.Score += weightStatusDone
		}
	}

	// Collect into slice.
	var out []relevantResult
	for _, r := range scores {
		out = append(out, *r)
	}
	return out, nil
}

// tokenizeQuery splits a free-form keyword query into whitespace-separated
// tokens, drops duplicates, and skips tokens shorter than 2 characters (which
// would otherwise generate noise from stop-word-length matches). Preserves
// original casing — ripgrep runs case-insensitive.
func tokenizeQuery(query string) []string {
	fields := strings.Fields(query)
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		key := strings.ToLower(f)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// searchWithRipgrep runs rg --json over .wipnote/*.html for the given query
// and adds scores/citations to the scores map.
func searchWithRipgrep(hgDir, query string, scores map[string]*relevantResult) error {
	args := []string{
		"--json",
		"--type", "html",
		"--ignore-case",
		"--max-count", "20",
		query,
		hgDir,
	}
	out, err := exec.Command("rg", args...).Output()
	if err != nil {
		// rg exits 1 when no matches — that's fine. Other errors pass through.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return err
	}

	// Parse NDJSON output from rg --json.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg rgMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Type != "match" {
			continue
		}
		if err := processRipgrepMatch(msg, scores); err != nil {
			continue
		}
	}
	return nil
}

// rgMessage is a minimal ripgrep --json message.
type rgMessage struct {
	Type string      `json:"type"`
	Data rgMatchData `json:"data"`
}

type rgMatchData struct {
	Path       rgText `json:"path"`
	Lines      rgText `json:"lines"`
	LineNumber int    `json:"line_number"`
}

type rgText struct {
	Text string `json:"text"`
}

// processRipgrepMatch extracts the work-item ID from the matched HTML file,
// filters out hits that land in HTML markup, and updates scores.
func processRipgrepMatch(msg rgMessage, scores map[string]*relevantResult) error {
	filePath := msg.Data.Path.Text
	lineText := strings.TrimSpace(msg.Data.Lines.Text)
	lineNum := msg.Data.LineNumber

	// Skip lines that look like pure HTML tag markup (data- attribute lines, tags only).
	// Accept lines with meaningful text content outside angle brackets.
	if isMarkupOnlyLine(lineText) {
		return nil
	}

	// Parse the file to get work-item metadata.
	node, err := parseHTMLWorkItem(filePath)
	if err != nil || node == nil {
		return err
	}

	r := getOrCreate(scores, node)
	r.Score += weightFileMention
	r.Citations = append(r.Citations, citation{
		File:    filePath,
		Line:    lineNum,
		Snippet: truncate(lineText, 80),
	})
	return nil
}

// htmlTagPattern matches an HTML tag — compiled once and reused on the
// per-ripgrep-line hot path inside isMarkupOnlyLine.
var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)

// isMarkupOnlyLine returns true for lines that consist entirely of HTML tags/attributes
// with no visible text content — these are low-signal ripgrep hits.
func isMarkupOnlyLine(line string) bool {
	if !strings.HasPrefix(line, "<") {
		return false
	}
	stripped := htmlTagPattern.ReplaceAllString(line, "")
	return strings.TrimSpace(stripped) == ""
}

// parseHTMLWorkItem opens an HTML file and extracts id, type, title, status.
func parseHTMLWorkItem(filePath string) (*relevantResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		return nil, err
	}

	article := doc.Find("article[id]").First()
	if article.Length() == 0 {
		// Recap artifacts have no <article id> — they carry data-recap-* on <body>.
		// Surface them so find/relevant can return committed recaps too.
		if body := doc.Find("body[data-recap-kind]").First(); body.Length() > 0 {
			return recapResultFromFile(filePath, body), nil
		}
		return nil, nil // not a work item file
	}

	id, _ := article.Attr("id")
	if id == "" {
		return nil, nil
	}

	title := strings.TrimSpace(doc.Find("header h1").First().Text())
	if title == "" {
		title = strings.TrimSpace(doc.Find("title").First().Text())
	}

	return &relevantResult{
		ID:     id,
		Type:   attrOrEmpty(article, "data-type"),
		Title:  title,
		Status: attrOrEmpty(article, "data-status"),
	}, nil
}

// recapResultFromFile builds a relevantResult for a recap artifact. The id is
// derived from the filename (recaps embed their id in the path, not the HTML);
// type is "recap" so callers can distinguish recaps from work items.
func recapResultFromFile(filePath string, body *goquery.Selection) *relevantResult {
	id := strings.TrimSuffix(filepath.Base(filePath), ".html")
	title := attrOrEmpty(body, "data-recap-title")
	if title == "" {
		title = attrOrEmpty(body, "data-recap-outcome")
	}
	return &relevantResult{
		ID:     id,
		Type:   "recap",
		Title:  title,
		Status: "",
	}
}

// attrOrEmpty returns an attribute value or empty string.
func attrOrEmpty(sel *goquery.Selection, name string) string {
	v, _ := sel.Attr(name)
	return v
}

// searchViaGitFileHistory uses git log --follow to find commits touching the
// given file, then parses wipnote-item trailers to attribute work items.
//
// Uses ASCII record separator (\x1e) between commits and unit separator (\x00)
// between fields so that multi-line commit bodies (where wipnote-item trailers
// usually live) parse correctly.
func searchViaGitFileHistory(projectDir, hgDir, filePath string, scores map[string]*relevantResult) error {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--follow", "--format=%x1e%H%x00%aI%x00%s%x00%b",
		"--", filePath,
	).Output()
	if err != nil {
		return nil
	}

	for _, block := range strings.Split(string(out), "\x1e") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		parts := strings.SplitN(block, "\x00", 4)
		if len(parts) < 4 {
			continue
		}
		sha, date, subject, body := parts[0], parts[1], parts[2], parts[3]

		// Extract wipnote-item trailers from commit body.
		for _, itemID := range extractHTMLGraphTrailers(body) {
			filePath := resolveItemHTMLPath(hgDir, itemID)
			var node *relevantResult
			if filePath != "" {
				node, _ = parseHTMLWorkItem(filePath)
			}
			if node == nil {
				node = &relevantResult{ID: itemID}
			}
			r := getOrCreate(scores, node)
			r.Score += weightCommitItem
			r.Citations = append(r.Citations, citation{
				SHA:     sha[:min7(sha)],
				Date:    date,
				Subject: truncate(subject, 72),
			})
		}
	}
	return nil
}

// searchViaGitSHA looks up a specific commit SHA and extracts wipnote-item
// trailers to attribute work items, then augments with ripgrep of item IDs.
//
// Uses \x00 field separators so that multi-line commit bodies parse correctly.
func searchViaGitSHA(projectDir, hgDir, sha string, scores map[string]*relevantResult) error {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "-1", "--format=%H%x00%aI%x00%s%x00%B",
		sha,
	).Output()
	if err != nil {
		return nil
	}

	block := strings.TrimRight(string(out), "\n")
	if block == "" {
		return nil
	}
	parts := strings.SplitN(block, "\x00", 4)
	if len(parts) < 4 {
		return nil
	}
	fullSHA, date, subject, body := parts[0], parts[1], parts[2], parts[3]

	for _, itemID := range extractHTMLGraphTrailers(body) {
		filePath := resolveItemHTMLPath(hgDir, itemID)
		var node *relevantResult
		if filePath != "" {
			node, _ = parseHTMLWorkItem(filePath)
		}
		if node == nil {
			node = &relevantResult{ID: itemID}
		}
		r := getOrCreate(scores, node)
		r.Score += weightCommitItem
		r.Citations = append(r.Citations, citation{
			SHA:     fullSHA[:min7(fullSHA)],
			Date:    date,
			Subject: truncate(subject, 72),
		})
	}

	// Also search the HTML files for the SHA as a keyword.
	_ = searchWithRipgrep(hgDir, sha, scores)
	return nil
}

// extractHTMLGraphTrailers parses "wipnote-item: <id>" trailers from a commit body.
func extractHTMLGraphTrailers(body string) []string {
	var ids []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		after, ok := strings.CutPrefix(strings.ToLower(line), "wipnote-item:")
		if !ok {
			continue
		}
		id := strings.TrimSpace(after)
		// Restore original case — re-cut from original line.
		orig := strings.TrimSpace(line[len("wipnote-item:"):])
		if orig != "" {
			ids = append(ids, orig)
		} else if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// resolveItemHTMLPath finds the HTML file for a work-item ID by checking known subdirs.
func resolveItemHTMLPath(hgDir, id string) string {
	subdirs := []string{"features", "bugs", "spikes", "tracks", "plans", "specs", "metrics", "recaps"}
	for _, sub := range subdirs {
		p := filepath.Join(hgDir, sub, id+".html")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// getOrCreate returns an existing result or inserts a new one in the map.
func getOrCreate(scores map[string]*relevantResult, node *relevantResult) *relevantResult {
	if existing, ok := scores[node.ID]; ok {
		// Update metadata if missing.
		if existing.Title == "" && node.Title != "" {
			existing.Title = node.Title
		}
		if existing.Type == "" && node.Type != "" {
			existing.Type = node.Type
		}
		if existing.Status == "" && node.Status != "" {
			existing.Status = node.Status
		}
		return existing
	}
	r := &relevantResult{
		ID:     node.ID,
		Type:   node.Type,
		Title:  node.Title,
		Status: node.Status,
	}
	scores[node.ID] = r
	return r
}

// rankResults sorts results by score descending.
func rankResults(results []relevantResult) []relevantResult {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// isCommandNotFound returns true when the error indicates a missing executable.
func isCommandNotFound(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if ok := isExecError(err, &execErr); ok {
		return execErr.Err == exec.ErrNotFound
	}
	return strings.Contains(err.Error(), "executable file not found")
}

func isExecError(err error, target **exec.Error) bool {
	if e, ok := err.(*exec.Error); ok {
		*target = e
		return true
	}
	return false
}

// min7 returns the min of 7 and len(s).
func min7(s string) int {
	if len(s) < 7 {
		return len(s)
	}
	return 7
}

// printRelevantJSON outputs results as a JSON array.
func printRelevantJSON(results []relevantResult) error {
	if results == nil {
		results = []relevantResult{}
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// printRelevantText outputs a human-readable ranked list.
func printRelevantText(results []relevantResult) {
	if len(results) == 0 {
		fmt.Println("No matching work items found.")
		return
	}
	fmt.Printf("%-22s  %-8s  %-11s  %5s  %s\n", "ID", "TYPE", "STATUS", "SCORE", "TITLE")
	fmt.Println(strings.Repeat("-", 80))
	for _, r := range results {
		fmt.Printf("%-22s  %-8s  %-11s  %5.1f  %s\n",
			r.ID, r.Type, r.Status, r.Score, truncate(r.Title, 36))
		for _, c := range r.Citations {
			if c.SHA != "" {
				fmt.Printf("    [%s] %s  %s\n", c.SHA, c.Date, truncate(c.Subject, 60))
			} else if c.File != "" {
				fmt.Printf("    %s:%d  %s\n", filepath.Base(c.File), c.Line, c.Snippet)
			}
		}
	}
	fmt.Printf("\n%d item(s)\n", len(results))
}
