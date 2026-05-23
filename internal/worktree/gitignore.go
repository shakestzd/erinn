package worktree

import (
	"os"
	"path/filepath"
)

// wipnoteGitignoreContent is the managed policy block written to
// .wipnote/.gitignore. Patterns are relative to .wipnote/ (nested gitignore).
//
// Design: create-if-missing only — if the file already exists (user may have
// edited it), we leave it alone. This is the simplest acceptable idempotence
// contract: one write, zero surprise mutations. If the file is missing it is
// written fresh; if present, it is never touched. Rationale: the file is
// machine-generated from a stable policy list; adopter projects that upgraded
// from an older wipnote version that never wrote it will get it on first
// session-start after upgrading. Projects that already have the file (either
// user-managed or from a future wipnote version that changes the policy) are
// left intact.
//
// TRACKED canonical content is NOT ignored here: features/, bugs/, tracks/,
// plans/, *.html work items, and project config/metadata must remain visible
// to git. sessions/ being ignored already covers session HTML, events.ndjson,
// state.json, .index-offset, .collector-pid under sessions/<id>/ — the **/
// patterns are belt-and-suspenders for any such markers outside sessions/.
const wipnoteGitignoreContent = `# Managed by wipnote — do not edit this block manually.
# Patterns are relative to .wipnote/ (this is a nested .gitignore).
# See: https://git-scm.com/docs/gitignore

# Runtime/session directories
sessions/
events/
logs/

# Database and index artifacts
*.db
*.db-*
*.sqlite*
*.bloom
archive-index/

# Obsolete analytics artifacts
cigs/

# Runtime telemetry files
**/*.jsonl
**/*.log
**/*.lock
.active-session
.launch-mode
.otel-notice-shown
.session-warning-state.json
.session-families.lock
session-families.json
parent-activity.json
.error-spikes.json
active-auto-spikes.json
drift-queue.json
.otlp-port
.serve.lock

# Migration markers
migrations/*.done

# Process/collector pid and offset markers
**/*.pid
**/.index-offset
**/.collector-pid
**/state.json
`

// EnsureWipnoteGitignore ensures that .wipnote/.gitignore exists with the
// wipnote runtime-artifact policy. It is idempotent: if the file already
// exists (for any reason — user-managed or prior run), it is not modified.
// If writing fails, the error is silently swallowed — a missing .gitignore
// is undesirable but must never block a session launch.
//
// Wire this at any .wipnote bootstrap seam: session-start hooks and the
// family-lock path both call it so all harnesses (Claude, Codex, Gemini)
// receive the file on their first session after upgrading.
func EnsureWipnoteGitignore(projectDir string) {
	if projectDir == "" {
		return
	}
	dir := filepath.Join(projectDir, ".wipnote")
	// Ensure the directory exists (best-effort; callers may have already done this).
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	target := filepath.Join(dir, ".gitignore")
	// Create-if-missing only. O_CREATE|O_EXCL fails if the file exists, which
	// is exactly the idempotence contract we want — no silent overwrites.
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// File already exists (err from O_EXCL) or dir is unwritable — either
		// way, this is not an error the caller needs to handle.
		return
	}
	defer f.Close()
	_, _ = f.WriteString(wipnoteGitignoreContent)
}
