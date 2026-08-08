package hooks

import "strings"

// This file holds the Claude-specific MECHANICS for bug-190950e0: `wipnote
// (feature|bug|spike) (start|complete|reopen)`, run as a Bash command inside
// a dispatched subagent, always wrote its claim under the __root__ sentinel
// instead of the subagent's real identity, because that CLI invocation is a
// plain shell command with no CloudEvent of its own to read an agent_id
// from — it falls back to os.Getenv("WIPNOTE_AGENT_ID"), which the
// undocumented, empirically-unreliable CLAUDE_ENV_FILE mechanism was
// supposed to seed and does not (see feat-7ee73444 research).
//
// The fix is Claude Code's own documented, reliable pairing instead: every
// PreToolUse hook fired inside a subagent carries a real, distinguishing
// agent_id on the CloudEvent (code.claude.com/docs/en/hooks, "Common input
// fields" — "agent_id: Unique identifier for the subagent. Present only when
// the hook fires inside a subagent call."), and PreToolUse can rewrite a
// tool's arguments before it runs via hookSpecificOutput.updatedInput (same
// page, PreToolUse section). This file rewrites a matching Bash command to
// prefix it with WIPNOTE_AGENT_ID=<agentID>.
//
// This file is intentionally Claude-only and knows it: the harness-neutral
// decision of WHETHER to call into this rewrite at all lives in
// agent_identity.go's AgentIdentityAdapter seam, and
// agent_identity_claude.go is the only caller of
// rewriteWipnoteClaimCommandsForAgent below. Nothing here should be called
// directly from the shared PreToolUse path.
//
// This is a high-blast-radius rewrite path — it can alter arbitrary user
// shell commands — so every helper below is deliberately conservative:
// it only ever touches a command it can anchor with high confidence, and
// leaves everything else byte-for-byte untouched.

// wipnoteClaimSubjects and wipnoteClaimVerbs anchor the exact wipnote
// subcommand shape that mutates a claim. Anything else (list, show, etc.)
// is left alone — this rewrite exists only to fix claim attribution.
var wipnoteClaimSubjects = map[string]bool{"feature": true, "bug": true, "spike": true}
var wipnoteClaimVerbs = map[string]bool{"start": true, "complete": true, "reopen": true}

// rewriteWipnoteClaimCommandsForAgent scans cmd for top-level shell segments
// (split on the same separators PreToolUse's existing wipnote-write guard
// uses: \n, ;, |, &&) that invoke `wipnote (feature|bug|spike)
// (start|complete|reopen)`, and prefixes each match with
// "WIPNOTE_AGENT_ID=<agentID> " so the CLI process resolves the correct
// per-subagent claim owner instead of falling back to __root__.
//
// Returns the (possibly unmodified) command and whether any rewrite was
// applied. Deliberately conservative:
//
//   - No-op when agentID or cmd is empty. The root session legitimately has
//     no distinct agent id and must keep resolving to __root__ — this must
//     never emit "WIPNOTE_AGENT_ID=" with an empty value.
//   - No-op for the ENTIRE command when a heredoc redirection ("<<") appears
//     anywhere in it. The segment splitter below has no concept of a heredoc
//     body, so a wipnote-shaped line inside one would otherwise be
//     misidentified as a top-level command. Refusing the whole command is a
//     small, deliberate loss of coverage in exchange for ruling out an
//     entire class of corruption.
//   - Matching is anchored on the first token of each top-level segment
//     (after skipping leading VAR=value assignments, mirroring
//     segmentStartsWithWipnoteCLI's tokenisation) plus the next two tokens
//     matching a claim subject and verb exactly. Trigger words appearing in
//     a comment (first token "#"), inside a quoted argument to an unrelated
//     command (first token is that command, not "wipnote"), or as an
//     argument VALUE to a genuine wipnote command (harmless — it's a true
//     positive) are all handled correctly by this anchor alone.
//   - Only the matched segment is rewritten, via a targeted insertion at the
//     exact byte offset immediately before its "wipnote" token — every other
//     segment, separator, and byte of whitespace in the original string is
//     preserved exactly. This is why the implementation locates each
//     already-known segment back inside the original string (via a
//     forward-only cursor) rather than reassembling the command from parts.
func rewriteWipnoteClaimCommandsForAgent(cmd, agentID string) (string, bool) {
	if agentID == "" || strings.TrimSpace(cmd) == "" {
		return cmd, false
	}
	if strings.Contains(cmd, "<<") {
		return cmd, false // heredoc present anywhere — refuse to touch the command at all
	}

	segments := splitShellCommandSegments(cmd)
	if len(segments) == 0 {
		return cmd, false
	}

	prefix := "WIPNOTE_AGENT_ID=" + agentID + " "
	result := cmd
	cursor := 0
	rewrote := false
	for _, seg := range segments {
		idx := strings.Index(result[cursor:], seg)
		if idx < 0 {
			// Should not happen — splitShellCommandSegments only returns
			// trimmed substrings of cmd in order. Be safe and stop rather
			// than risk misplacing a rewrite.
			break
		}
		absStart := cursor + idx
		cursor = absStart + len(seg)

		insertAt, ok := wipnoteClaimCommandInsertOffset(seg)
		if !ok {
			continue
		}
		abs := absStart + insertAt
		result = result[:abs] + prefix + result[abs:]
		cursor += len(prefix)
		rewrote = true
	}
	if !rewrote {
		return cmd, false
	}
	return result, true
}

// wipnoteClaimCommandInsertOffset returns the byte offset (relative to seg,
// the raw untrimmed segment text as returned by splitShellCommandSegments)
// immediately before the "wipnote" token, when seg's leading tokens — after
// skipping any VAR=value assignment tokens — are exactly
// "wipnote (feature|bug|spike) (start|complete|reopen)". The wipnote token
// itself may be a path ending in "/wipnote", matching
// segmentStartsWithWipnoteCLI's own definition of "is this wipnote".
func wipnoteClaimCommandInsertOffset(seg string) (int, bool) {
	pos := 0
	for {
		for pos < len(seg) && isShellFieldSpace(seg[pos]) {
			pos++
		}
		tokenStart := pos
		for pos < len(seg) && !isShellFieldSpace(seg[pos]) {
			pos++
		}
		token := seg[tokenStart:pos]
		if token == "" {
			return 0, false
		}
		if strings.Contains(token, "=") && !strings.HasPrefix(token, "-") {
			continue // leading env-var assignment (e.g. FOO=bar) — keep scanning
		}
		if token != "wipnote" && !strings.HasSuffix(token, "/wipnote") {
			return 0, false
		}
		fields := strings.Fields(seg[pos:])
		if len(fields) < 2 {
			return 0, false
		}
		if !wipnoteClaimSubjects[fields[0]] || !wipnoteClaimVerbs[fields[1]] {
			return 0, false
		}
		return tokenStart, true
	}
}

// isShellFieldSpace matches the whitespace strings.Fields itself splits on
// for the ASCII range this scanner cares about (shell command text). Kept
// narrow and local to this file rather than reusing unicode.IsSpace so the
// tokenisation here stays byte-offset-exact and trivially auditable.
func isShellFieldSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}
