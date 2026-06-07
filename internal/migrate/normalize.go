// Package migrate provides one-shot data migrations.
//
// normalize.go implements the path-normalization migration that rewrites
// absolute host paths to repo-relative form across .wipnote/ HTML and the
// derived SQLite read index. It is the companion of the runtime
// paths.NormalizeToRepoRelative path so that artefacts captured before the
// runtime normalizer was wired up can be repaired without re-ingest.
//
// The migration is idempotent — re-running on already-relative records is a
// no-op. Host paths are recognised via paths.HostPathPattern; anything that
// pattern misses (relatives, exotic absolutes like /opt/...) is left alone.
package migrate

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/paths"
)

// NormalizeOptions configures a normalize-paths run.
type NormalizeOptions struct {
	// RepoRoot is the absolute filesystem path of the wipnote project root
	// (parent of .wipnote/). It is used as the canonical relativization
	// anchor for every path under the project, bypassing the per-directory
	// git lookup.
	RepoRoot string

	// DryRun proposes changes without writing. Default false.
	DryRun bool

	// AllowDirty bypasses the clean-working-tree precondition. The summary
	// records the override.
	AllowDirty bool

	// NoMergeCollisions makes feature_files collisions abort the run
	// instead of merging duplicate rows. Default false (merge).
	NoMergeCollisions bool

	// Backup, when true (default), copies every touched HTML file under
	// .wipnote/.backup-<timestamp>/ before rewriting it.
	Backup bool

	// BackupTimestamp is the suffix used to name the backup directory. It
	// is captured up-front so dry-runs and real runs share the same value
	// and the test harness can fix it.
	BackupTimestamp string
}

// NormalizeSummary captures the per-target counts of a normalize run.
type NormalizeSummary struct {
	HTMLFilesRewritten     int
	DBValuesNormalized     int
	AlreadyRelativeSkipped int
	MarkedUnresolved       int
	CollisionsMerged       int
	CollisionsAborted      int
	DryRun                 bool
	AllowDirtyOverride     bool
	BackupDir              string
	Proposals              []Proposal
	Collisions             []FeatureFileCollision
}

// Proposal describes one record that the migration would rewrite (in dry-run)
// or has rewritten. The shape is shared between dry-run output and the audit
// log so tests can assert on either.
type Proposal struct {
	Target string // e.g. "agent_events.tool_input", "html.data-project-dir"
	ID     string // primary key / file path for the affected record
	Before string
	After  string
}

// FeatureFileCollision describes two feature_files rows for the same feature
// that normalize to the same repo-relative path.
type FeatureFileCollision struct {
	FeatureID  string
	BeforeA    string
	BeforeB    string
	After      string
	RowIDA     string // earliest first_seen wins
	RowIDB     string
	FirstSeenA time.Time
	FirstSeenB time.Time
}

// Format returns a one-line human-readable summary, used by the CLI.
func (s NormalizeSummary) Format() string {
	var b strings.Builder
	b.WriteString("Migration summary:\n")
	fmt.Fprintf(&b, "  HTML files rewritten:        %d\n", s.HTMLFilesRewritten)
	fmt.Fprintf(&b, "  DB values normalized:        %d\n", s.DBValuesNormalized)
	fmt.Fprintf(&b, "  Already-relative skipped:    %d\n", s.AlreadyRelativeSkipped)
	fmt.Fprintf(&b, "  Marked unresolved:           %d\n", s.MarkedUnresolved)
	fmt.Fprintf(&b, "  Collisions merged:           %d\n", s.CollisionsMerged)
	fmt.Fprintf(&b, "  Collisions aborted (--no-merge): %d\n", s.CollisionsAborted)
	if s.AllowDirtyOverride {
		b.WriteString("  AUDIT: --allow-dirty bypass applied (working tree was dirty)\n")
	}
	if s.BackupDir != "" {
		fmt.Fprintf(&b, "  Backup directory:            %s\n", s.BackupDir)
	}
	return b.String()
}

// normalizeOnePath delegates to paths.NormalizeToRepoRelative with the
// project's repoRoot pre-supplied so we never hit the per-directory git
// lookup during a bulk migration. It returns the new value, a flag for
// whether it changed, and whether the new value carries the "unresolved:"
// prefix.
func normalizeOnePath(value, repoRoot string) (newVal string, changed bool, unresolved bool) {
	if value == "" {
		return value, false, false
	}
	// Don't touch values already prefixed — they carry the runtime
	// normaliser's verdict (outside-repo) and re-running must be a no-op.
	if strings.HasPrefix(value, "unresolved:") {
		return value, false, true
	}
	if !filepath.IsAbs(value) {
		return value, false, false
	}
	out, _ := paths.NormalizeToRepoRelative(value, repoRoot)
	if out == value {
		return value, false, false
	}
	return out, true, strings.HasPrefix(out, "unresolved:")
}

// rewriteEmbeds scans free-text for HostPathPattern matches and rewrites each
// one in place by extending the match to the next whitespace boundary and
// normalising the resulting path. Used for agent_events.input_summary, which
// embeds absolute paths inside human-readable summaries rather than holding a
// bare path.
func rewriteEmbeds(text, repoRoot string) (string, int, int) {
	if text == "" {
		return text, 0, 0
	}
	// Match a host-path PREFIX and then keep consuming up to the next
	// whitespace so we capture the entire path token.
	// HostPathPattern only matches the leading anchor (e.g. "/workspaces/foo/").
	// We extend it forward through non-whitespace, non-quote characters so
	// the full path is rewritten in one shot.
	re := regexp.MustCompile(
		`(/Users/[^/\s]+/[^\s"',\)\]]*` +
			`|/home/[^/\s]+/[^\s"',\)\]]*` +
			`|/workspaces/[^/\s]+/[^\s"',\)\]]*` +
			`|/private/var/folders/[^\s"',\)\]]*)`,
	)
	changed, unresolved := 0, 0
	out := re.ReplaceAllStringFunc(text, func(match string) string {
		newVal, did, isUnresolved := normalizeOnePath(match, repoRoot)
		if did {
			changed++
		}
		if isUnresolved {
			unresolved++
		}
		return newVal
	})
	return out, changed, unresolved
}

// rewriteToolInputJSON walks a parsed agent_events.tool_input JSON value and
// normalises any string field whose key strongly suggests a filesystem path
// (file_path, path, cwd, command, etc.). The DB column is SINGLE-encoded
// JSON, so callers must json.Unmarshal once before passing the value here.
//
// The walker recurses through maps and slices; strings that don't look like
// host paths are left alone so we never mangle unrelated free text.
func rewriteToolInputJSON(v interface{}, repoRoot string) (changed, unresolved int) {
	switch tv := v.(type) {
	case map[string]interface{}:
		for k, child := range tv {
			if s, ok := child.(string); ok && looksLikePathField(k) {
				newVal, did, wasUnresolved := normalizeOnePath(s, repoRoot)
				if did {
					tv[k] = newVal
					changed++
				}
				if wasUnresolved {
					unresolved++
				}
				continue
			}
			// "command" / "description" carry embedded paths inside
			// shell text — rewrite via the embed scanner.
			if s, ok := child.(string); ok && looksLikeEmbedField(k) {
				newVal, c, u := rewriteEmbeds(s, repoRoot)
				if c > 0 {
					tv[k] = newVal
					changed += c
				}
				unresolved += u
				continue
			}
			c, u := rewriteToolInputJSON(child, repoRoot)
			changed += c
			unresolved += u
		}
	case []interface{}:
		for _, child := range tv {
			c, u := rewriteToolInputJSON(child, repoRoot)
			changed += c
			unresolved += u
		}
	}
	return changed, unresolved
}

// looksLikePathField reports whether a JSON key in tool_input typically
// holds a single absolute path. Keep this list conservative — the wrong
// match would normalise free text.
func looksLikePathField(key string) bool {
	switch key {
	case "file_path", "path", "cwd", "notebook_path", "abs_path",
		"directory", "dir", "filepath", "filePath", "Filepath",
		"target_dir", "target_directory", "source", "destination",
		"src", "dst", "input_path", "output_path":
		return true
	}
	return false
}

// looksLikeEmbedField reports whether a JSON key in tool_input holds free
// text that may embed absolute paths (e.g. a shell command, search pattern).
func looksLikeEmbedField(key string) bool {
	switch key {
	case "command", "description", "old_string", "new_string",
		"content", "pattern", "prompt", "query":
		return true
	}
	return false
}

// rewriteToolInputColumn applies rewriteToolInputJSON to a raw tool_input
// column value. The column is SINGLE-encoded JSON (per the DDL), so the
// caller passes the raw bytes and the function returns the rewritten bytes
// plus counts; an empty/null input returns no change.
func rewriteToolInputColumn(raw string, repoRoot string) (string, int, int, error) {
	if raw == "" || raw == "null" {
		return raw, 0, 0, nil
	}
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		// Non-JSON content — historically some hooks wrote raw strings.
		// Treat as plain text via the embed scanner so we still catch
		// host paths.
		out, c, u := rewriteEmbeds(raw, repoRoot)
		return out, c, u, nil
	}
	changed, unresolved := rewriteToolInputJSON(v, repoRoot)
	if changed == 0 {
		return raw, 0, unresolved, nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return raw, 0, 0, fmt.Errorf("re-marshal tool_input: %w", err)
	}
	return string(encoded), changed, unresolved, nil
}

// IsDirty reports whether `git status --porcelain .wipnote/` returns any
// output. Used as the safety precondition for normalize-paths.
//
// gitRunner is split out so tests can stub the shell-out.
type gitRunner func(repoRoot string, args ...string) (string, error)

// IsWorkingTreeDirty reports whether .wipnote/ inside repoRoot has any
// uncommitted changes. The check delegates to `git -C <repoRoot> status
// --porcelain .wipnote/` so it works the same in tests and in production.
func IsWorkingTreeDirty(repoRoot string, run gitRunner) (bool, error) {
	out, err := run(repoRoot, "status", "--porcelain", ".wipnote/")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// PrepareBackupDir creates the backup directory under repoRoot/.wipnote/ if
// it does not already exist. Returns the absolute path. A timestamp suffix
// is included so multiple migrations on the same day produce distinct dirs.
func PrepareBackupDir(repoRoot, timestamp string) (string, error) {
	dir := filepath.Join(repoRoot, ".wipnote", ".backup-"+timestamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	return dir, nil
}

// CopyFileForBackup copies srcPath to dstDir mirroring its repo-relative
// position so restore-paths can reverse the operation cleanly. Returns the
// destination path.
func CopyFileForBackup(srcPath, repoRoot, backupDir string) (string, error) {
	rel, err := filepath.Rel(repoRoot, srcPath)
	if err != nil {
		return "", fmt.Errorf("rel %s: %w", srcPath, err)
	}
	// Strip the .wipnote/ prefix so the backup root acts as the new wipnote
	// dir and restore can mirror straight back.
	rel = strings.TrimPrefix(rel, ".wipnote"+string(filepath.Separator))
	dst := filepath.Join(backupDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir backup parent: %w", err)
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", srcPath, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	return dst, nil
}

// rewriteHTMLFile reads a single HTML file, rewrites every host-path embed,
// and (if changed) writes the result back atomically. The rewrite re-uses
// rewriteEmbeds so the same boundary rules apply.
//
// Returns (changed, unresolved, error). The function NEVER writes when
// dryRun=true even if changed > 0; callers must reflect that in the
// summary.
func rewriteHTMLFile(absPath, repoRoot string, dryRun bool) (changed, unresolved int, before string, after string, err error) {
	data, readErr := os.ReadFile(absPath)
	if readErr != nil {
		return 0, 0, "", "", readErr
	}
	original := string(data)
	rewritten, c, u := rewriteEmbeds(original, repoRoot)
	if c == 0 {
		return 0, u, "", "", nil
	}
	if dryRun {
		return c, u, original, rewritten, nil
	}
	if err := os.WriteFile(absPath, []byte(rewritten), 0o644); err != nil {
		return 0, 0, "", "", fmt.Errorf("write %s: %w", absPath, err)
	}
	return c, u, original, rewritten, nil
}

// scanFeatureFileCollisions returns every feature_files collision pair that
// would result from normalising file_path. A collision is two distinct rows
// in the same feature whose normalised file_path is identical.
func scanFeatureFileCollisions(db *sql.DB, repoRoot string) ([]FeatureFileCollision, error) {
	rows, err := db.Query(`
		SELECT id, feature_id, file_path, first_seen
		FROM feature_files
		ORDER BY feature_id, first_seen ASC`)
	if err != nil {
		return nil, fmt.Errorf("query feature_files: %w", err)
	}
	defer rows.Close()

	type row struct {
		ID         string
		FeatureID  string
		FilePath   string
		Normalized string
		FirstSeen  time.Time
	}
	// Bucket by (feature_id, normalised path).
	buckets := map[string][]row{}
	for rows.Next() {
		var r row
		var ts string
		if err := rows.Scan(&r.ID, &r.FeatureID, &r.FilePath, &ts); err != nil {
			return nil, err
		}
		r.FirstSeen = parseDBTime(ts)
		r.Normalized, _, _ = normalizeOnePath(r.FilePath, repoRoot)
		if r.Normalized == "" {
			r.Normalized = r.FilePath
		}
		key := r.FeatureID + "\x00" + r.Normalized
		buckets[key] = append(buckets[key], r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var collisions []FeatureFileCollision
	for _, k := range keys {
		bucket := buckets[k]
		if len(bucket) < 2 {
			continue
		}
		// Sort by first_seen ASC so position 0 is the keeper.
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].FirstSeen.Before(bucket[j].FirstSeen)
		})
		// One collision per (winner, loser) pair so callers can report
		// every duplicate independently.
		winner := bucket[0]
		for _, loser := range bucket[1:] {
			collisions = append(collisions, FeatureFileCollision{
				FeatureID:  winner.FeatureID,
				BeforeA:    winner.FilePath,
				BeforeB:    loser.FilePath,
				After:      winner.Normalized,
				RowIDA:     winner.ID,
				RowIDB:     loser.ID,
				FirstSeenA: winner.FirstSeen,
				FirstSeenB: loser.FirstSeen,
			})
		}
	}
	return collisions, nil
}

// parseDBTime parses a SQLite DATETIME value across the three formats the
// schema actually emits. Returns the zero time on parse failure so callers
// fall through to the row ordering as a tiebreaker.
func parseDBTime(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
