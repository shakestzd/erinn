package migrate

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/core/paths"
)

// pathsNormalizeProjectDir wraps paths.NormalizeProjectDir so tests can stub
// it via the package-level var below.
var pathsNormalizeProjectDir = paths.NormalizeProjectDir

// NormalizePaths is the top-level entry point. It walks every rewrite target
// and either proposes (DryRun=true) or applies the rewrites in place.
//
// Database mutations are wrapped in a single transaction so a mid-run
// failure leaves the read index untouched. HTML rewrites happen after the
// commit so a corrupt HTML write does not leave the DB and HTML out of sync.
func NormalizePaths(db *sql.DB, opts NormalizeOptions) (NormalizeSummary, error) {
	summary := NormalizeSummary{DryRun: opts.DryRun}

	// Feature-files collision detection runs first so we can abort cleanly
	// (or stage the merge plan) before any other mutation.
	collisions, err := scanFeatureFileCollisions(db, opts.RepoRoot)
	if err != nil {
		return summary, fmt.Errorf("scan feature_files collisions: %w", err)
	}
	summary.Collisions = collisions
	if len(collisions) > 0 && opts.NoMergeCollisions {
		summary.CollisionsAborted = len(collisions)
		return summary, fmt.Errorf(
			"feature_files: %d collision(s) would merge; pass without --no-merge-collisions to proceed",
			len(collisions),
		)
	}

	// Stage 1: SQL rewrites — wrapped in a transaction so a fault rolls back.
	if !opts.DryRun {
		tx, err := db.Begin()
		if err != nil {
			return summary, fmt.Errorf("begin tx: %w", err)
		}
		ok := false
		defer func() {
			if !ok {
				_ = tx.Rollback()
			}
		}()
		if err := applyDBRewrites(tx, opts, &summary); err != nil {
			return summary, err
		}
		if err := applyFeatureFileMerges(tx, collisions, &summary); err != nil {
			return summary, err
		}
		if err := tx.Commit(); err != nil {
			return summary, fmt.Errorf("commit tx: %w", err)
		}
		ok = true
	} else {
		// Dry-run: still compute the proposals so the operator sees them.
		if err := proposeDBRewrites(db, opts, &summary); err != nil {
			return summary, err
		}
		// Collisions are already in the summary; nothing to merge in
		// dry-run mode.
		summary.CollisionsMerged = len(collisions)
	}

	// Stage 2: HTML rewrites. The walker handles both .wipnote/sessions/
	// data-project-dir attributes and free-text host-path embeds.
	if err := applyHTMLRewrites(opts, &summary); err != nil {
		return summary, err
	}

	return summary, nil
}

// applyDBRewrites mutates the four SQL targets in-place. The transaction
// caller will commit or roll back.
func applyDBRewrites(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary) error {
	if err := rewriteAgentEventsToolInput(tx, opts, summary, false); err != nil {
		return fmt.Errorf("agent_events.tool_input: %w", err)
	}
	if err := rewriteAgentEventsInputSummary(tx, opts, summary, false); err != nil {
		return fmt.Errorf("agent_events.input_summary: %w", err)
	}
	if err := rewriteFeatureFilesPath(tx, opts, summary, false); err != nil {
		return fmt.Errorf("feature_files.file_path: %w", err)
	}
	if err := rewritePendingSubagentCWD(tx, opts, summary, false); err != nil {
		return fmt.Errorf("pending_subagent_starts.cwd: %w", err)
	}
	if err := rewriteSessionsProjectDir(tx, opts, summary, false); err != nil {
		return fmt.Errorf("sessions.project_dir: %w", err)
	}
	return nil
}

// proposeDBRewrites mirrors applyDBRewrites but rolls back its transaction
// at the end so the read index is untouched. Used by dry-run mode.
func proposeDBRewrites(db *sql.DB, opts NormalizeOptions, summary *NormalizeSummary) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin dry-run tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := rewriteAgentEventsToolInput(tx, opts, summary, true); err != nil {
		return fmt.Errorf("agent_events.tool_input (dry-run): %w", err)
	}
	if err := rewriteAgentEventsInputSummary(tx, opts, summary, true); err != nil {
		return fmt.Errorf("agent_events.input_summary (dry-run): %w", err)
	}
	if err := rewriteFeatureFilesPath(tx, opts, summary, true); err != nil {
		return fmt.Errorf("feature_files.file_path (dry-run): %w", err)
	}
	if err := rewritePendingSubagentCWD(tx, opts, summary, true); err != nil {
		return fmt.Errorf("pending_subagent_starts.cwd (dry-run): %w", err)
	}
	if err := rewriteSessionsProjectDir(tx, opts, summary, true); err != nil {
		return fmt.Errorf("sessions.project_dir (dry-run): %w", err)
	}
	return nil
}

func rewriteAgentEventsToolInput(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary, dryRun bool) error {
	rows, err := tx.Query(`SELECT event_id, COALESCE(tool_input, '') FROM agent_events WHERE tool_input IS NOT NULL AND tool_input != ''`)
	if err != nil {
		return err
	}
	type update struct {
		EventID   string
		NewValue  string
		OldValue  string
		Changed   int
		Unresolv  int
	}
	var updates []update
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		newVal, c, u, rerr := rewriteToolInputColumn(raw, opts.RepoRoot)
		if rerr != nil {
			rows.Close()
			return rerr
		}
		if c > 0 {
			updates = append(updates, update{EventID: id, NewValue: newVal, OldValue: raw, Changed: c, Unresolv: u})
		} else {
			summary.AlreadyRelativeSkipped++
		}
	}
	rows.Close()
	for _, up := range updates {
		summary.DBValuesNormalized++
		summary.MarkedUnresolved += up.Unresolv
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "agent_events.tool_input",
			ID:     up.EventID,
			Before: truncateForLog(up.OldValue, 80),
			After:  truncateForLog(up.NewValue, 80),
		})
		if dryRun {
			continue
		}
		if _, err := tx.Exec(`UPDATE agent_events SET tool_input = ? WHERE event_id = ?`, up.NewValue, up.EventID); err != nil {
			return err
		}
	}
	return nil
}

func rewriteAgentEventsInputSummary(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary, dryRun bool) error {
	rows, err := tx.Query(`SELECT event_id, COALESCE(input_summary, '') FROM agent_events WHERE input_summary IS NOT NULL AND input_summary != ''`)
	if err != nil {
		return err
	}
	type update struct {
		EventID  string
		NewValue string
		OldValue string
		Unresolv int
	}
	var updates []update
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		newVal, c, u := rewriteEmbeds(raw, opts.RepoRoot)
		if c > 0 {
			updates = append(updates, update{EventID: id, NewValue: newVal, OldValue: raw, Unresolv: u})
		}
	}
	rows.Close()
	for _, up := range updates {
		summary.DBValuesNormalized++
		summary.MarkedUnresolved += up.Unresolv
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "agent_events.input_summary",
			ID:     up.EventID,
			Before: truncateForLog(up.OldValue, 80),
			After:  truncateForLog(up.NewValue, 80),
		})
		if dryRun {
			continue
		}
		if _, err := tx.Exec(`UPDATE agent_events SET input_summary = ? WHERE event_id = ?`, up.NewValue, up.EventID); err != nil {
			return err
		}
	}
	return nil
}

func rewriteFeatureFilesPath(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary, dryRun bool) error {
	// Build the set of rows that lose a merge so we skip them here — the
	// merger handles deletion. Rows that ARE the merge winner still need
	// their file_path normalised in place.
	losers := map[string]bool{}
	for _, c := range summary.Collisions {
		losers[c.RowIDB] = true
	}
	rows, err := tx.Query(`SELECT id, file_path FROM feature_files`)
	if err != nil {
		return err
	}
	type update struct {
		ID       string
		NewValue string
		OldValue string
		Unresolv bool
	}
	var updates []update
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		if losers[id] {
			continue
		}
		newVal, did, isUnresolved := normalizeOnePath(p, opts.RepoRoot)
		if !did {
			continue
		}
		updates = append(updates, update{ID: id, NewValue: newVal, OldValue: p, Unresolv: isUnresolved})
	}
	rows.Close()
	for _, up := range updates {
		summary.DBValuesNormalized++
		if up.Unresolv {
			summary.MarkedUnresolved++
		}
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "feature_files.file_path",
			ID:     up.ID,
			Before: up.OldValue,
			After:  up.NewValue,
		})
		if dryRun {
			continue
		}
		if _, err := tx.Exec(`UPDATE feature_files SET file_path = ? WHERE id = ?`, up.NewValue, up.ID); err != nil {
			return err
		}
	}
	return nil
}

func rewritePendingSubagentCWD(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary, dryRun bool) error {
	rows, err := tx.Query(`SELECT agent_id, COALESCE(cwd, '') FROM pending_subagent_starts WHERE cwd IS NOT NULL AND cwd != ''`)
	if err != nil {
		return err
	}
	type update struct {
		AgentID  string
		NewValue string
		OldValue string
		Unresolv bool
	}
	var updates []update
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		newVal, did, isUnresolved := normalizeOnePath(p, opts.RepoRoot)
		if !did {
			continue
		}
		updates = append(updates, update{AgentID: id, NewValue: newVal, OldValue: p, Unresolv: isUnresolved})
	}
	rows.Close()
	for _, up := range updates {
		summary.DBValuesNormalized++
		if up.Unresolv {
			summary.MarkedUnresolved++
		}
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "pending_subagent_starts.cwd",
			ID:     up.AgentID,
			Before: up.OldValue,
			After:  up.NewValue,
		})
		if dryRun {
			continue
		}
		if _, err := tx.Exec(`UPDATE pending_subagent_starts SET cwd = ? WHERE agent_id = ?`, up.NewValue, up.AgentID); err != nil {
			return err
		}
	}
	return nil
}

func rewriteSessionsProjectDir(tx *sql.Tx, opts NormalizeOptions, summary *NormalizeSummary, dryRun bool) error {
	rows, err := tx.Query(`SELECT session_id, COALESCE(project_dir, '') FROM sessions WHERE project_dir IS NOT NULL AND project_dir != ''`)
	if err != nil {
		return err
	}
	type update struct {
		SessionID string
		NewValue  string
		OldValue  string
		Unresolv  bool
	}
	var updates []update
	for rows.Next() {
		var id, dir string
		if err := rows.Scan(&id, &dir); err != nil {
			rows.Close()
			return err
		}
		newVal := projectDirNormalize(dir, opts.RepoRoot)
		if newVal == dir {
			continue
		}
		updates = append(updates, update{
			SessionID: id,
			NewValue:  newVal,
			OldValue:  dir,
			Unresolv:  strings.HasPrefix(newVal, "unresolved:"),
		})
	}
	rows.Close()
	for _, up := range updates {
		summary.DBValuesNormalized++
		if up.Unresolv {
			summary.MarkedUnresolved++
		}
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "sessions.project_dir",
			ID:     up.SessionID,
			Before: up.OldValue,
			After:  up.NewValue,
		})
		if dryRun {
			continue
		}
		if _, err := tx.Exec(`UPDATE sessions SET project_dir = ? WHERE session_id = ?`, up.NewValue, up.SessionID); err != nil {
			return err
		}
	}
	return nil
}

// projectDirNormalize is a thin wrapper around paths.NormalizeProjectDir that
// honours the project's RepoRoot when the input matches the repo root exactly
// (avoiding the per-directory git lookup).
func projectDirNormalize(dir, repoRoot string) string {
	if dir == "" || !filepath.IsAbs(dir) {
		return dir
	}
	if strings.HasPrefix(dir, "unresolved:") {
		return dir
	}
	if repoRoot != "" {
		if dir == repoRoot {
			return "."
		}
		if rel, err := filepath.Rel(repoRoot, dir); err == nil {
			if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return filepath.ToSlash(rel)
			}
		}
	}
	// Fall back to the shared resolver — handles foreign-machine sessions.
	return pathsNormalizeProjectDir(dir)
}

// applyFeatureFileMerges deletes loser rows and bumps the winner's session
// count after the file_path of the winner has been normalised. In dry-run
// mode (handled by callers) this function is never called.
func applyFeatureFileMerges(tx *sql.Tx, collisions []FeatureFileCollision, summary *NormalizeSummary) error {
	merged := 0
	for _, c := range collisions {
		// Normalise the winner's file_path BEFORE we delete the loser so
		// the UNIQUE(feature_id, file_path) constraint never fires on a
		// transient state.
		if _, err := tx.Exec(`UPDATE feature_files SET file_path = ? WHERE id = ?`, c.After, c.RowIDA); err != nil {
			return fmt.Errorf("normalize winner %s: %w", c.RowIDA, err)
		}
		if _, err := tx.Exec(`DELETE FROM feature_files WHERE id = ?`, c.RowIDB); err != nil {
			return fmt.Errorf("delete loser %s: %w", c.RowIDB, err)
		}
		merged++
	}
	summary.CollisionsMerged = merged
	return nil
}

// applyHTMLRewrites walks .wipnote/sessions/*.html, .wipnote/features/*.html,
// .wipnote/bugs/*.html, .wipnote/spikes/*.html and rewrites any host-path
// embed found (data-project-dir attribute, affected_files property string,
// inline `<a href>` paths, etc.). Files are backed up before being touched.
func applyHTMLRewrites(opts NormalizeOptions, summary *NormalizeSummary) error {
	roots := []string{"sessions", "features", "bugs", "spikes", "tracks", "plans", "specs"}
	var touched []string
	for _, sub := range roots {
		dir := filepath.Join(opts.RepoRoot, ".wipnote", sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*.html"))
		touched = append(touched, matches...)
	}

	var backupDir string
	if opts.Backup && !opts.DryRun {
		var err error
		backupDir, err = PrepareBackupDir(opts.RepoRoot, opts.BackupTimestamp)
		if err != nil {
			return err
		}
	}

	for _, abs := range touched {
		// First, scan in dry-run mode so we know whether to back up
		// before rewriting. This keeps the backup directory minimal —
		// only files that actually changed end up in it.
		changed, _, _, _, err := rewriteHTMLFile(abs, opts.RepoRoot, true)
		if err != nil {
			return err
		}
		if changed == 0 {
			continue
		}
		if opts.Backup && backupDir != "" {
			if _, err := CopyFileForBackup(abs, opts.RepoRoot, backupDir); err != nil {
				return err
			}
		}
		// Now perform the real rewrite (dry-run if the caller asked).
		_, unresolved, before, after, err := rewriteHTMLFile(abs, opts.RepoRoot, opts.DryRun)
		if err != nil {
			return err
		}
		summary.HTMLFilesRewritten++
		summary.MarkedUnresolved += unresolved
		summary.Proposals = append(summary.Proposals, Proposal{
			Target: "html",
			ID:     abs,
			Before: truncateForLog(before, 120),
			After:  truncateForLog(after, 120),
		})
	}
	if backupDir != "" {
		summary.BackupDir = backupDir
	}
	return nil
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
