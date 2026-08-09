package main

import (
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shakestzd/wipnote/core/claimledger"
	"github.com/shakestzd/wipnote/core/models"
)

// Canonical provenance lookups for the completion gates (feat-fc3cc9e0).
//
// These replace core/db.GetCommitsByFeature / core/db.CodeBearingPaths, which
// read the per-project SQLite read-index that no longer exists. Both facts the
// gates need are recoverable from durable, committed state:
//
//   - LINKED COMMITS are Git-derived. wipnote's commit convention puts the work
//     item ID in the message — parenthesised "(feat-abc12345)" or an explicit
//     "Refs:"/"Fixes:" trailer — and `wipnote reindex` has always rebuilt
//     git_commits.feature_id by parsing exactly that (reindex_trailers.go). We
//     read the same source directly instead of the table derived from it, so
//     the gate sees identical linkage without an index.
//
//   - CODE-BEARING PATHS are the files those commits touched (git diff-tree,
//     via the batched commitFilesByHash helper reindexFeatureFiles already
//     uses), UNION — only when the item carries zero linked commits — the
//     uncommitted source in the working tree. The second half is what keeps the
//     gate non-vacuous: with commit-derived paths alone, "code-bearing" would
//     imply "has commits" and the gate could never fire. It is scoped to items
//     with canonical evidence that an agent actually implemented them (an
//     implemented_in session edge on the artifact, or a claim-ledger episode),
//     so a hand-written docs item completed in a dirty tree stays exempt.
//
// KNOWN NARROWING vs the old feature_files table: that table also carried
// per-tool-call file touches recorded live by the hooks. Those rows lived only
// in the derived index — the canonical record of them is the multi-hundred-MB
// per-session events NDJSON, which cannot be scanned synchronously on a
// completion path. An item whose implementation was committed WITHOUT its ID in
// the message, in an otherwise clean tree, therefore now reads as not
// code-bearing and is exempt. That is the honest answer: no durable artifact
// links it to any source change, so there is nothing for the gate to point at.

// canonicalLinkedCommits returns the commits that carry provenance for
// workItemID, from BOTH canonical sources:
//
//  1. Explicit committed_in edges on the item's own artifact, written by
//     `wipnote link-commit` (link_commit.go). These come first because they are
//     a deliberate human assertion of provenance.
//  2. Commits whose MESSAGE names the item under wipnote's convention.
//
// The union is not optional. Source 2 alone cannot see a manually linked
// commit — and a commit that does not name its item is the entire reason
// `link-commit` exists — so a message-only gate would keep refusing completion
// after the user had already supplied the provenance it was asking for.
//
// node may be nil (the artifact could not be read), in which case only the
// message-derived half contributes.
//
// `git log --all` is deliberate: provenance exists as soon as the work is
// committed somewhere reachable, even on a branch not yet merged to HEAD.
// --fixed-strings --grep narrows the walk to candidate messages cheaply; each
// candidate is then confirmed with parseTrailers, the SAME parser the reindex
// path uses, so a stray mention of the ID in unrelated prose does not count as
// linkage.
func canonicalLinkedCommits(repoRoot, workItemID string, node *models.Node) []string {
	var hashes []string
	seen := map[string]bool{}

	// Source 1: explicit committed_in edges. Included even when the object is
	// not present locally (shallow clone, unfetched branch) — the edge is the
	// durable record of provenance, and its absence from this checkout is a
	// property of the checkout, not of the work.
	if node != nil {
		for _, e := range node.Edges[string(RelCommittedIn)] {
			h := strings.TrimSpace(e.TargetID)
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			hashes = append(hashes, h)
		}
	}

	if repoRoot == "" || workItemID == "" || !isGitRepo(repoRoot) {
		return hashes
	}
	out, err := exec.Command(
		"git", "-C", repoRoot, "log", "--all",
		"--fixed-strings", "--grep="+workItemID,
		"--format=%H%x1f%s%x1f%b%x1e",
	).Output()
	if err != nil {
		return hashes
	}

	// Source 2: message-derived.
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) < 2 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		message := fields[1]
		if len(fields) > 2 {
			message += "\n" + fields[2]
		}
		if hash == "" || seen[hash] {
			continue
		}
		for _, linked := range parseTrailers(message) {
			if linked == workItemID {
				seen[hash] = true
				hashes = append(hashes, hash)
				break
			}
		}
	}
	return hashes
}

// canonicalCodeBearingPaths returns the distinct in-project source paths
// (outside .wipnote/) attributable to workItemID, sorted.
//
// node may be nil; when present its implemented_in edges are one of the two
// canonical signals that an agent implemented the item (the other is a
// claim-ledger episode). commits is the already-resolved output of
// canonicalLinkedCommits — passed in so the provenance gate does not run the
// git log walk twice.
func canonicalCodeBearingPaths(repoRoot, wipnoteDir, workItemID string, node *models.Node, commits []string) []string {
	if repoRoot == "" || !isGitRepo(repoRoot) {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || seen[p] || isWipnotePath(p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	for _, files := range commitFilesByHash(repoRoot, commits) {
		for _, f := range files {
			add(f)
		}
	}

	// Uncommitted evidence, considered ONLY when nothing is committed against
	// the item. When commits exist the provenance gate passes on them alone, so
	// widening the path set with working-tree noise would buy nothing and would
	// feed unrelated files to the dependency-research gate downstream.
	if len(out) == 0 && workItemImplemented(wipnoteDir, workItemID, node) {
		for _, p := range uncommittedSourcePaths(repoRoot) {
			add(p)
		}
	}

	sort.Strings(out)
	return out
}

// workItemImplemented reports whether canonical state records that an agent
// worked the item: an implemented_in session edge on the artifact, or a claim
// episode in the claim ledger. Either is enough; the claim ledger is only read
// when the artifact does not already answer the question.
func workItemImplemented(wipnoteDir, workItemID string, node *models.Node) bool {
	if node != nil {
		for _, e := range node.Edges[string(models.RelImplementedIn)] {
			if strings.TrimSpace(e.TargetID) != "" {
				return true
			}
		}
	}
	if wipnoteDir == "" || workItemID == "" {
		return false
	}
	episodes, err := claimledger.NewStore(wipnoteDir).ReadAll()
	if err != nil {
		return false
	}
	for _, e := range episodes {
		if e.WorkItemID == workItemID {
			return true
		}
	}
	return false
}

// uncommittedSourcePaths lists working-tree paths outside .wipnote/ that are
// not in HEAD: modified/staged tracked files AND untracked (non-ignored) ones.
//
// It deliberately includes untracked files, which dirtyTrackedSourceFiles
// (--untracked-files=no, the --allow-dirty gate) does not see at all: a brand
// new source file that was never added is uncommitted implementation with no
// provenance whatsoever, which is precisely what this gate exists to catch.
func uncommittedSourcePaths(repoRoot string) []string {
	out, err := exec.Command(
		"git", "-C", repoRoot, "status", "--porcelain=v1", "-z",
		// =all so a wholly-untracked new source directory is reported as its
		// individual files rather than collapsed to one "dir/" entry.
		"--untracked-files=all",
	).CombinedOutput()
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range parsePorcelainZ(out) {
		if entry.Path == ".claude/settings.local.json" || isWipnotePath(entry.Path) {
			continue
		}
		files = append(files, entry.Path)
	}
	return files
}
