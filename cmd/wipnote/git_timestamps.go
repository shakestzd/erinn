package main

import (
	"bytes"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// gitFileTimestamps returns (created, updated) for a file from git history.
//
//   - created = timestamp of the first commit that added the file
//     (via git log --diff-filter=A --follow, oldest entry wins).
//   - updated = timestamp of the most recent commit touching the file
//     (via git log -1).
//
// Falls back to (zero, zero) when git is unavailable or the file is untracked.
// Callers should use the HTML-attribute timestamps as a fallback when both
// returned values are zero.
func gitFileTimestamps(projectDir, filePath string) (created, updated time.Time, err error) {
	updated, err = gitLastModified(projectDir, filePath)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	created, err = gitFirstAdded(projectDir, filePath)
	if err != nil {
		// If we can't get the creation time, still return updated.
		return time.Time{}, updated, err
	}

	// If file has only one commit, created == updated, which is correct.
	if created.IsZero() {
		created = updated
	}

	return created, updated, nil
}

// gitLastModified returns the author timestamp of the most recent commit
// that touched filePath. Returns zero time when the file is untracked.
func gitLastModified(projectDir, filePath string) (time.Time, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "-1", "--format=%aI", "--", filePath,
	).Output()
	if err != nil {
		return time.Time{}, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return time.Time{}, nil // untracked
	}
	return parseGitTimestamp(line)
}

// gitFirstAdded returns the author timestamp of the oldest commit that
// introduced filePath (following renames via --follow --diff-filter=A).
// Returns zero time when the file is untracked.
func gitFirstAdded(projectDir, filePath string) (time.Time, error) {
	out, err := exec.Command(
		"git", "-C", projectDir,
		"log", "--diff-filter=A", "--follow", "--format=%aI", "--", filePath,
	).Output()
	if err != nil {
		return time.Time{}, err
	}

	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return time.Time{}, nil // untracked
	}

	// git log outputs newest-first; we want the oldest (last line).
	lines := bytes.Split([]byte(raw), []byte("\n"))
	last := strings.TrimSpace(string(lines[len(lines)-1]))
	if last == "" {
		last = strings.TrimSpace(string(lines[0]))
	}
	return parseGitTimestamp(last)
}

// parseGitTimestamp parses an ISO 8601 timestamp produced by git --format=%aI.
func parseGitTimestamp(s string) (time.Time, error) {
	// git %aI produces RFC3339 with timezone offset e.g. "2024-01-15T10:30:00+05:30"
	return time.Parse(time.RFC3339, s)
}

// applyGitTimestamps overrides node timestamps with git history when available.
// If git has no record of the file (untracked/not committed), the provided
// htmlCreated and htmlUpdated values are returned unchanged.
//
// This is the primary integration point between git history and reindex.
func applyGitTimestamps(
	projectDir, filePath string,
	htmlCreated, htmlUpdated time.Time,
) (created, updated time.Time) {
	gitCreated, gitUpdated, err := gitFileTimestamps(projectDir, filePath)
	if err != nil {
		// git unavailable — use HTML attributes as-is.
		return htmlCreated, htmlUpdated
	}
	return resolveTimestamps(gitCreated, gitUpdated, htmlCreated, htmlUpdated)
}

// resolveTimestamps applies the html-attribute-fallback rule to a
// (gitCreated, gitUpdated) pair, however that pair was obtained. Both
// applyGitTimestamps (one file, its own `git log` calls) and the batch
// lookup path (batchGitFileTimestamps, one `git log` walk for many files)
// route through this single function so the two stay behaviorally
// identical by construction rather than by keeping two copies of the same
// zero-handling rules in sync by hand.
func resolveTimestamps(gitCreated, gitUpdated, htmlCreated, htmlUpdated time.Time) (created, updated time.Time) {
	if gitCreated.IsZero() && gitUpdated.IsZero() {
		// git has no record of the file (untracked/not committed) — use
		// HTML attributes as-is.
		return htmlCreated, htmlUpdated
	}

	created = gitCreated
	updated = gitUpdated

	// Sanity: if git only returned updated (no --diff-filter=A hit), fall back
	// to HTML created or updated as the creation timestamp.
	if created.IsZero() {
		if !htmlCreated.IsZero() {
			created = htmlCreated
		} else {
			created = updated
		}
	}

	return created, updated
}

// fileTimestamps holds the git-derived (created, updated) pair for one file,
// as produced by batchGitFileTimestamps.
type fileTimestamps struct {
	created time.Time
	updated time.Time
}

// batchGitFileTimestamps resolves raw (created, updated) git timestamps for
// every path in filePaths using one bulk `git log` walk for "updated" plus
// one `git log --follow` subprocess per file for "created" — down from two
// subprocesses per file (bug-4e5816f4: reindexTracks and reindexFeatureDir
// calling applyGitTimestamps once per file dominated a full `wipnote
// reindex` at several minutes for roughly a thousand
// tracked/feature/bug/spike files).
//
// filePaths must be absolute paths (or relative to the current working
// directory) the same way callers already pass them to applyGitTimestamps.
// projectDir is the git repo root.
//
// WHY ONLY "UPDATED" IS BATCHED: "last modified" for a literal path is
// unambiguous — the newest commit that touched that exact path, which is
// exactly what one whole-repo `git log --name-only` walk (newest-first,
// first sighting per path wins) computes for every path at once. "created"
// is a different problem: it must trace back through renames AND copies the
// same way `git log --follow` does, and this repo's own history has real
// examples of both, including a work-item file whose true origin requires
// following a git-detected low-similarity COPY (not a rename) to a
// completely different historical filename. Git only detects that class of
// copy with `--find-copies-harder` (`-C -C`), which is an O(n^2) whole-tree
// scan — verified experimentally to not finish in 3 minutes across this
// repo's ~7800-commit history, where the equivalent per-file `--follow`
// call costs about a quarter of a second. A cheaper bulk heuristic (chase
// only `-M`-detected renames, or add plain `-C`) was tried and caught by a
// real-corpus equivalence test: it produced silently wrong "created" values
// for every file whose true history requires copy-detection that the
// cheaper flags miss, indistinguishable from a genuine "no earlier history"
// case using only the bulk output. Rather than ship a heuristic proven to
// have real false positives on this repo's own data, "created" keeps using
// the exact original gitFirstAdded (--follow) call, unchanged — this
// function only removes the "updated" half of the per-file cost.
//
// Returns raw (created, updated) pairs, not yet reconciled against HTML
// attribute fallbacks — callers pass the result through resolveTimestamps
// (see timestampsFromBatch) exactly as applyGitTimestamps does for the
// single-file path.
func batchGitFileTimestamps(projectDir string, filePaths []string) map[string]fileTimestamps {
	if len(filePaths) == 0 {
		return nil
	}

	relToAbs := make(map[string]string, len(filePaths))
	for _, fp := range filePaths {
		rel, err := filepath.Rel(projectDir, fp)
		if err != nil {
			continue
		}
		relToAbs[filepath.ToSlash(rel)] = fp
	}
	if len(relToAbs) == 0 {
		return nil
	}

	updated := bulkWalks(projectDir, relToAbs).lastModified

	// "created" is resolved per file and CANNOT be batched — see gitFirstAdded.
	// What it can be is concurrent: each call is an independent, read-only `git
	// log` subprocess, so running them across the available cores gives an
	// identical answer for every path by construction. That matters because
	// this loop is the whole cost of building a projection: 1,027 work items at
	// ~195ms each measured 213.9s serially on this repo.
	type firstAddedResult struct {
		rel, abs string
		created  time.Time
		err      error
	}

	jobs := make(chan string, len(relToAbs))
	for rel := range relToAbs {
		jobs <- rel
	}
	close(jobs)

	workers := runtime.NumCPU()
	if workers > len(relToAbs) {
		workers = len(relToAbs)
	}
	if workers < 1 {
		workers = 1
	}

	results := make(chan firstAddedResult, len(relToAbs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				abs := relToAbs[rel]
				created, err := gitFirstAdded(projectDir, abs)
				results <- firstAddedResult{rel: rel, abs: abs, created: created, err: err}
			}
		}()
	}
	wg.Wait()
	close(results)

	result := make(map[string]fileTimestamps, len(relToAbs))
	for r := range results {
		if r.err != nil {
			// gitFileTimestamps treats a gitFirstAdded error as fatal to the
			// whole pair (it returns created=zero, discarding the otherwise-
			// valid updated value it already computed), and applyGitTimestamps
			// then discards that too, falling back to HTML attributes
			// entirely. Omitting this path from the batch result makes
			// timestampsFromBatch report ok=false, so the caller falls back
			// to applyGitTimestamps and reproduces that exact behavior
			// instead of us trying to duplicate it here.
			continue
		}
		u := updated[r.rel] // zero value if untracked, matching gitLastModified
		created := r.created
		if created.IsZero() {
			created = u
		}
		result[r.abs] = fileTimestamps{created: created, updated: u}
	}
	return result
}

// bulkWalkResult holds the git-derived timestamp map for one (repo, HEAD,
// scope) triple, keyed by repo-relative path. Only "lastModified" is here;
// "created" is not batchable (see the note above bulkLastModified).
type bulkWalkResult struct {
	lastModified map[string]time.Time
}

type bulkWalkKey struct {
	projectDir string
	head       string
	scope      string
}

var (
	bulkWalkMu    sync.Mutex
	bulkWalkCache = map[bulkWalkKey]*bulkWalkResult{}
)

// bulkWalks returns both timestamp maps for the paths in relToAbs, running at
// most one `git log` walk per (repo, HEAD, scope) per process.
//
// Hydration calls batchGitFileTimestamps once per work-item directory
// (tracks, features, bugs, spikes, ...). Scoping each walk to its own directory
// would repeat a near-identical whole-history walk four times; widening the
// scope to the enclosing .wipnote directory lets all of them share one result.
// Measured on this repo (6,854 commits): a .wipnote-scoped first-added walk is
// ~500ms and a last-modified walk ~490ms, so hydration pays ~1s once instead of
// ~2.8s spread over four directories.
//
// The cache is keyed by HEAD so a commit invalidates it; `git rev-parse HEAD`
// costs ~32ms, which is the price of not serving stale timestamps to a
// long-running process (serve_child re-hydrates on rebuild).
func bulkWalks(projectDir string, relToAbs map[string]string) *bulkWalkResult {
	scope := bulkWalkScope(relToAbs)
	key := bulkWalkKey{projectDir: projectDir, head: gitHead(projectDir), scope: scope}

	bulkWalkMu.Lock()
	defer bulkWalkMu.Unlock()
	if cached, ok := bulkWalkCache[key]; ok {
		return cached
	}
	res := &bulkWalkResult{lastModified: bulkLastModified(projectDir, scope)}
	bulkWalkCache[key] = res
	return res
}

// bulkWalkScope picks the pathspec to restrict the walks to. It returns the
// enclosing `.wipnote` directory when every path lives under one (the hydration
// case, which is what makes a single shared walk possible), otherwise the
// longest common directory prefix, otherwise "" for an unrestricted walk.
func bulkWalkScope(relToAbs map[string]string) string {
	var common []string
	first := true
	for rel := range relToAbs {
		parts := strings.Split(path.Dir(filepath.ToSlash(rel)), "/")
		if first {
			common = append([]string(nil), parts...)
			first = false
			continue
		}
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
		if len(common) == 0 {
			break
		}
	}
	// Widen to the .wipnote root so every work-item directory shares one walk.
	for i, seg := range common {
		if seg == ".wipnote" {
			return strings.Join(common[:i+1], "/")
		}
	}
	if len(common) == 0 || (len(common) == 1 && (common[0] == "." || common[0] == "")) {
		return ""
	}
	return strings.Join(common, "/")
}

// gitHead returns HEAD's sha, or "" when it cannot be resolved (which simply
// makes the cache key weaker, never wrong for a single-shot CLI process).
func gitHead(projectDir string) string {
	out, err := exec.Command("git", "-C", projectDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WHY "created" IS NOT BATCHED, AND MUST NOT BE.
//
// It is tempting to resolve every path's first-add from one oldest-first
// `git log --diff-filter=A --name-only --reverse` walk, the way bulkLastModified
// resolves "updated". That was tried and is wrong: measured against the per-file
// --follow reference over this repo's 1,075 work-item files, a bulk walk
// disagreed on 566 of them (53%).
//
// The reason is not a missing rename marker that could be special-cased. Example:
// feat-075c110d.html has exactly ONE "A" event under its current path, at
// 2026-06-03T11:43:34, and --follow reports creation at 11:42:27 — the file was
// created under another name a minute earlier and git does not record the move
// as a rename (the content changed too much for rename detection). So the bulk
// walk sees a perfectly ordinary "A" and has no signal at all that it is really
// a move target. Filtering on R/C events does not help, and neither does
// checking whether the path was "missing" from the walk — it is not missing.
//
// Resolving this correctly needs --find-copies-harder, an O(n^2) whole-tree
// scan, which is far more expensive than the per-file calls it would replace.
// So the per-file --follow call stays, and the cost is addressed by running the
// calls concurrently instead (see batchGitFileTimestamps).
//
// TestRealCorpusTimestampEquivalence-style checking against real history is the
// only thing that catches a regression here; the synthetic fixtures in
// git_timestamps_batch_test.go all pass against the broken bulk version.

// parseNameOnlyWalk parses `git log --name-only --pretty=format:@@%aI` output
// into path -> timestamp, keeping the FIRST sighting of each path.
//
// First-sighting-wins is correct for both callers because each orders the walk
// so that the sighting it wants comes first: bulkLastModified walks newest-first
// (first sighting = most recent commit touching the path) and bulkFirstAdded
// walks oldest-first via --reverse (first sighting = the commit that added it).
func parseNameOnlyWalk(out string) map[string]time.Time {
	result := make(map[string]time.Time)
	var curTS time.Time
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if ts, perr := parseGitTimestamp(strings.TrimPrefix(line, "@@")); perr == nil {
				curTS = ts
			}
			continue
		}
		if _, seen := result[line]; !seen {
			result[line] = curTS
		}
	}
	return result
}

// bulkLastModified resolves the newest-commit timestamp for every path under
// scope using a single `git log --name-only` walk (newest-first; first sighting
// of a path wins) instead of one `git log -1 -- path` subprocess per path.
// Matches gitLastModified's semantics exactly -- like that function, no
// rename-following is attempted, since "last modified" is inherently about the
// literal path as it exists today.
//
// scope restricts the walk to a pathspec (see bulkWalkScope); "" walks the whole
// repo. Returns a map keyed by repo-relative path; a path with no entry is
// untracked (zero time), exactly like gitLastModified returning a zero time
// with a nil error.
func bulkLastModified(projectDir, scope string) map[string]time.Time {
	// The @@ prefix can never collide with a name-only path line.
	args := []string{
		"-C", projectDir,
		"log", "--name-only", "--pretty=format:@@%aI",
	}
	if scope != "" {
		args = append(args, "--", scope)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil
	}
	return parseNameOnlyWalk(string(out))
}

// timestampsFromBatch looks up filePath in a map produced by
// batchGitFileTimestamps and applies the same html-attribute-fallback rule
// applyGitTimestamps applies to a single-file lookup. ok is false when
// filePath has no entry in batch (including when batch is nil), signaling
// the caller to fall back to applyGitTimestamps for that file.
func timestampsFromBatch(batch map[string]fileTimestamps, filePath string, htmlCreated, htmlUpdated time.Time) (created, updated time.Time, ok bool) {
	ts, found := batch[filePath]
	if !found {
		return time.Time{}, time.Time{}, false
	}
	c, u := resolveTimestamps(ts.created, ts.updated, htmlCreated, htmlUpdated)
	return c, u, true
}
