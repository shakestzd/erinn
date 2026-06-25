package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

// maybeAutoGeneratePlanRollupRecap checks whether the just-completed feature
// is the last remaining feature of a plan. If so, it auto-generates (or
// refreshes) the plan-rollup recap by calling RunPlanRollupRecap.
//
// Bug fix (roborev #544): plan finalize already writes recap-pln-<id>.html
// before any features are implemented. The old file-existence check wrongly
// treated that pre-implementation recap as current and skipped regeneration,
// leaving the final recap stale. We now compare the recap's last-committed HEAD
// SHA against the repository's current HEAD SHA. When they differ, HEAD has
// advanced (features were built), so we refresh the recap. When they match, a
// concurrent agent just generated it — skip to avoid a duplicate round-trip.
//
// Before generating the recap we flush the commit outbox so the completing
// feature's artifact lands in HEAD; RunPlanRollupRecap then ranges through a
// HEAD that includes the just-committed feature state (roborev #544 fix 2).
//
// This is best-effort and non-fatal: any error is printed to stderr and the
// feature completion is unaffected.
//
// Parameters:
//   - wipnoteDir: absolute path to the .wipnote directory
//   - featNode:   the completed feature node (edges already populated)
//   - p:          open Project — used to fetch plan and feature status data
func maybeAutoGeneratePlanRollupRecap(wipnoteDir string, featNode *models.Node, p *workitem.Project) {
	planID := owningPlanID(featNode)
	if planID == "" {
		return // feature not linked to any plan
	}

	featureIDs, err := planFeatureIDs(wipnoteDir, planID)
	if err != nil || len(featureIDs) == 0 {
		return // no promoted features — nothing to check
	}

	if !allFeaturesDone(p, featureIDs) {
		return // some features still in progress
	}

	repoRoot := filepath.Dir(wipnoteDir)

	// Idempotency: distinguish a stale pre-implementation recap (written by
	// plan finalize before any features were built) from a current post-impl
	// recap (written by a concurrent agent that beat us here). We compare the
	// HEAD SHA embedded in the recap's last git commit against current HEAD:
	//
	//   - recap absent              → generate (normal first-time path)
	//   - recap committed at HEAD   → skip (concurrent agent finished first)
	//   - recap committed behind HEAD → refresh (pre-implementation recap; HEAD
	//     has advanced with feature implementation commits since finalize wrote it)
	//   - recap exists but never committed (no git history) → skip (another
	//     process is generating it right now; file is already being written)
	//
	// recapAbsPath is the canonical on-disk location derived from wipnoteDir.
	// For git log we also need the absolute path; repoRoot is used as the -C arg.
	recapAbsPath := filepath.Join(wipnoteDir, "recaps", "recap-pln-"+planID+".html")

	if _, statErr := os.Stat(recapAbsPath); statErr == nil {
		// File exists. Check whether it has been committed.
		recapCommitSHA := recapLastCommitSHA(repoRoot, recapAbsPath)
		if recapCommitSHA == "" {
			// Uncommitted — another concurrent process is writing it right now.
			return
		}
		currentHEAD := repoHEAD(repoRoot)
		if currentHEAD != "" && recapCommitSHA == currentHEAD {
			// Recap was committed at the current HEAD — already current.
			return
		}
		// recapCommitSHA != currentHEAD: the recap was committed before HEAD
		// advanced (pre-implementation recap from plan finalize). Fall through
		// to refresh it with the current implementation range.
	}

	// Flush the commit outbox before generating the recap so the just-completed
	// feature's artifact commit lands in HEAD. RunPlanRollupRecap builds its
	// git range through HEAD; without this flush the deferred artifact commit
	// is queued but not yet in HEAD, and the recap would omit the feature's
	// committed state (roborev #544 fix 2).
	if flushErr := flushCommitQueueForRecap(repoRoot); flushErr != nil {
		fmt.Fprintf(os.Stderr, "  plan-recap (non-fatal): flush commit queue: %v\n", flushErr)
		// Non-fatal: proceed anyway; the recap may omit the deferred artifact
		// commit but is still better than no recap.
	}

	// All plan features are done and the existing recap (if any) is stale —
	// auto-generate (or refresh) the post-implementation rollup recap.
	fmt.Fprintf(os.Stderr,
		"  plan %s: all features done — auto-generating plan rollup recap\n", planID)
	if recapErr := RunPlanRollupRecap(wipnoteDir, planID); recapErr != nil {
		fmt.Fprintf(os.Stderr, "  plan-recap (non-fatal): %v\n", recapErr)
	}
}

// recapLastCommitSHA returns the full SHA of the most recent git commit that
// touched recapAbsPath, or "" when the file has no commit history (untracked
// or never staged). The empty string means the file exists on disk but has not
// landed in the commit graph — treat as "concurrent in-progress generation."
func recapLastCommitSHA(repoRoot, recapAbsPath string) string {
	out, err := exec.Command(
		"git", "-C", repoRoot, "log", "-1", "--format=%H", "--", recapAbsPath,
	).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// repoHEAD returns the full SHA of the repository's current HEAD commit, or
// "" on error (e.g. empty repo, non-git directory). Used to detect whether the
// recap has already been generated against the current commit graph.
func repoHEAD(repoRoot string) string {
	out, err := exec.Command(
		"git", "-C", repoRoot, "rev-parse", "HEAD",
	).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// flushCommitQueueForRecap drains the commit outbox so that any queued
// work-item artifact commits land in HEAD before RunPlanRollupRecap builds its
// git range. This is a best-effort step: a non-fatal error here means the recap
// may miss the just-deferred artifact commit, but the caller already handles
// that gracefully.
func flushCommitQueueForRecap(repoRoot string) error {
	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		return fmt.Errorf("open commit outbox: %w", err)
	}
	_, err = ob.Flush(outboxCommitter, 0 /* use default max-attempts */)
	return err
}

// owningPlanID returns the plan-* ID linked to the feature via a planned_in
// edge, or "" if none is found. Both planned_in and part_of edges are checked
// for compatibility with older feature creation paths.
func owningPlanID(featNode *models.Node) string {
	for _, rel := range []models.RelationshipType{models.RelPlannedIn, models.RelPartOf} {
		for _, edge := range featNode.Edges[string(rel)] {
			if strings.HasPrefix(edge.TargetID, "plan-") {
				return edge.TargetID
			}
		}
	}
	return ""
}

// planFeatureIDs returns all feature IDs promoted from the plan. It reads the
// plan YAML (authoritative post-finalize source) and collects every slice's
// FeatureID. Returns nil when the YAML is missing or has no slice feature IDs.
func planFeatureIDs(wipnoteDir, planID string) ([]string, error) {
	yamlPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	plan, err := planyaml.Load(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("load plan YAML for %s: %w", planID, err)
	}
	var ids []string
	for _, s := range plan.Slices {
		if s.FeatureID != "" {
			ids = append(ids, s.FeatureID)
		}
	}
	return ids, nil
}

// allFeaturesDone returns true when every feature ID in ids has status "done".
// A feature that cannot be loaded is treated as not done (safe default).
func allFeaturesDone(p *workitem.Project, ids []string) bool {
	for _, id := range ids {
		node, err := p.Features.Get(id)
		if err != nil {
			return false // can't confirm done — assume not done
		}
		if node.Status != models.StatusDone {
			return false
		}
	}
	return true
}
