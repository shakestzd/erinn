package hooks

import (
	"database/sql"
	"regexp"
	"strings"

	"github.com/shakestzd/wipnote/core/paths"
)

// researchWaiverTrailerRe matches a trailer-shaped `RESEARCH-WAIVER: <reason>`
// entry at the start of a (commit-message) line, requiring a non-empty reason
// after the colon. Mirrors the --research-waiver completion flag (feat-d1bcbf10).
var researchWaiverTrailerRe = regexp.MustCompile(`(?im)^\s*RESEARCH-WAIVER:\s*\S`)

// commitMessageHasResearchWaiver reports whether a git commit command carries an
// explicit, audited RESEARCH-WAIVER trailer inside one of its `-m`/`--message`
// argument VALUES. The waiver must be recorded in the commit message itself (so
// it is durable and reviewable), not merely present somewhere in the shell text.
//
// Parsing is argv-based: the git-commit segment is tokenized honoring quotes and
// `#` comments, then only actual `-m`/`--message` argument values are inspected.
// This defeats both a chained `&& echo -m "RESEARCH-WAIVER: …"` (different
// segment, roborev #572) and a commented `# -m "RESEARCH-WAIVER: …"` (not a real
// argument, roborev #581).
func commitMessageHasResearchWaiver(cmd string) bool {
	segment := gitCommitSegment(cmd)
	if segment == "" {
		return false
	}
	for _, msg := range commitMessageArgs(shellWords(segment)) {
		if researchWaiverTrailerRe.MatchString(msg) {
			return true
		}
	}
	return false
}

// shellWords splits a single shell command segment into argv-style words,
// honoring single/double quotes and treating an unquoted `#` at a word boundary
// as the start of a comment (the rest of the segment is ignored). It is NOT a
// full shell parser (no expansion or backslash escapes) but is sufficient to
// isolate real `-m`/`--message` argument values from comments and stray text.
func shellWords(segment string) []string {
	var words []string
	var cur []byte
	var inSingle, inDouble, started bool
	flush := func() {
		if started {
			words = append(words, string(cur))
			cur = cur[:0]
			started = false
		}
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur = append(cur, c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else {
				cur = append(cur, c)
			}
		case c == '\'':
			inSingle, started = true, true
		case c == '"':
			inDouble, started = true, true
		case c == '#' && !started:
			// Unquoted '#' at a word boundary begins a comment.
			return words
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur = append(cur, c)
			started = true
		}
	}
	flush()
	return words
}

// commitMessageArgs returns the values of all `-m`/`--message` arguments in an
// argv slice, covering the separate (`-m val`), attached (`-mval`), and
// `--message=val` forms. Parsing stops at Git's `--` option terminator: anything
// after it is a pathspec, not an option, so a post-`--` `-m` is not a message
// (roborev #582).
func commitMessageArgs(words []string) []string {
	var msgs []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "--" {
			break
		}
		switch {
		case w == "-m" || w == "--message":
			if i+1 < len(words) {
				msgs = append(msgs, words[i+1])
				i++
			}
		case strings.HasPrefix(w, "--message="):
			msgs = append(msgs, strings.TrimPrefix(w, "--message="))
		case strings.HasPrefix(w, "-m") && len(w) > 2:
			msgs = append(msgs, w[2:])
		}
	}
	return msgs
}

// gitCommitSegment returns the first top-level shell segment that is a
// `git commit` invocation, or "" if none. This scopes message-flag parsing to
// the commit command itself rather than the whole pipeline.
func gitCommitSegment(cmd string) string {
	for _, seg := range splitTopLevelShellSegments(cmd) {
		if gitCommitPattern.MatchString(seg) {
			return seg
		}
	}
	return ""
}

// splitTopLevelShellSegments splits cmd at UNQUOTED `&&`, `||`, `;`, `|`, and
// newline operators. Unlike splitShellCommandSegments it tracks single/double
// quote state, so a separator inside a quoted argument (e.g. a multi-line
// `-m "subject\nRESEARCH-WAIVER: reason"` commit message) does not split the
// segment. This keeps the git-commit segment intact for message-flag parsing
// while still excluding a chained `&& echo …` (roborev #572).
func splitTopLevelShellSegments(cmd string) []string {
	var segs []string
	var inSingle, inDouble bool
	start := 0
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == ';' || c == '\n':
			segs = append(segs, cmd[start:i])
			start = i + 1
		case c == '|':
			segs = append(segs, cmd[start:i])
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				i++
			}
			start = i + 1
		case c == '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				segs = append(segs, cmd[start:i])
				i++
				start = i + 1
			}
		}
	}
	segs = append(segs, cmd[start:])
	return segs
}

// stagedDependencyManifests returns the staged paths that are dependency
// manifests (go.mod, package.json, …). It uses the shared
// paths.IsDependencyManifest set so the commit-time gate stays in lockstep with
// the pre-edit guard (feat-868c752b) and the completion gate (feat-d1bcbf10).
func stagedDependencyManifests(staged []string) []string {
	var hits []string
	for _, f := range staged {
		if paths.IsDependencyManifest(f) {
			hits = append(hits, f)
		}
	}
	return hits
}

// checkDependencyResearchCommitGuard BLOCKS a git commit whose staged diff
// changes a dependency manifest (go.mod/package.json/…) when no web/docs
// research (WebSearch/WebFetch or `gh`) appears in the session — the commit-time
// complement to the pre-edit checkExternalTechResearchGuard. A dependency can be
// introduced via Bash (`go get`, `npm install`) that never triggers the pre-edit
// guard, so this gate catches the change at commit time regardless of HOW the
// manifest was modified — answering the question from durable staged-diff state
// per the project's hook-state rule (feat-af4ae1c3 / spk-0a982f70).
//
// Always-on (not YOLO-gated), consistent with checkPortDriftCommitGuard. Fast
// path: one `git diff --cached --name-only` when the command is a git commit;
// returns "" immediately for any other command. Explicit overrides: a
// RESEARCH-WAIVER trailer in the commit message, or WIPNOTE_GUARDS_OFF=1.
func checkDependencyResearchCommitGuard(event *CloudEvent, database *sql.DB, ctx *toolUseContext) string {
	if !isShellTool(event.ToolName) {
		return ""
	}
	cmd := shellCommand(event.ToolInput)
	commitSeg := gitCommitSegment(cmd) // codex P2: detect git commit anywhere in a chained command
	if commitSeg == "" {
		return ""
	}
	dir := event.CWD
	if dir == "" {
		return ""
	}
	repoRoot := resolveGitRepoRoot(dir)
	if repoRoot == "" {
		return ""
	}
	staged, err := stagedFiles(repoRoot)
	if err != nil || len(staged) == 0 {
		return ""
	}
	manifests := stagedDependencyManifests(staged)
	if len(manifests) == 0 {
		return "" // fast pass: no dependency manifest staged
	}
	// Explicit, audited waiver recorded as a trailer in the commit message.
	if commitMessageHasResearchWaiver(commitSeg) {
		return ""
	}
	if ctx == nil {
		return ""
	}
	if hasRecentWebResearch(database, ctx.SessionID, ctx.AgentID, ctx.ProjectDir) {
		return ""
	}
	return "Commit blocked: staged dependency-manifest change(s) (" +
		strings.Join(manifests, ", ") + ") but no web research this session. " +
		"Verify the new/changed dependency against current official docs/changelogs " +
		"with WebSearch/WebFetch (or `gh search`) before committing — a local file " +
		"read does not satisfy this gate. To intentionally waive, add a " +
		"`RESEARCH-WAIVER: <reason>` trailer to the commit message; for an emergency " +
		"override set WIPNOTE_GUARDS_OFF=1."
}
