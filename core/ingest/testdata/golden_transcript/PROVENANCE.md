# Golden transcript capture (bug-d7741c8f)

## What this is

A real Claude Code session-transcript JSONL file — not hand-built JSONL that
happens to match what `core/ingest/parser.go` already expects. It exists to
catch the same class of drift the golden OTLP capture
(`observe/otel/otlp/testdata/golden_capture/`, bug-735806ff) catches for the
telemetry pipeline: `parser_test.go`'s existing tests all construct their
input JSONL by hand, which cannot notice when a real transcript stops
matching those hand-built assumptions, even though this parser feeds the
same cost and token attribution the OTLP pipeline does.

`golden_transcript_test.go` (in the parent directory) runs this file through
`ParseFile` — the same entry point production sync/discovery code calls — and
asserts on specific fields by name.

## Provenance

| | |
|---|---|
| Harness | Claude Code |
| `version` (transcript field) | `2.1.224` — same underlying session as the golden OTLP capture (bug-735806ff), captured the same day |
| Captured | 2026-08-08 |
| Capture method | See "How this was captured" below |
| Content | One user prompt, 3 `attachment` lines, 1 `ai-title` line, 2 `queue-operation` lines, 4 assistant turns (text → Read tool_use → Edit tool_use → text), 2 tool-result `user` lines, 1 `last-prompt` line |

## How this was captured

This transcript is the *same session* used for the golden OTLP capture: an
isolated `claude -p "Read hello.txt and then use the Edit tool to append a
fourth line that says 'line four'." --permission-mode bypassPermissions
--output-format json` run against a throwaway scratch directory. Claude Code
persists session transcripts under `~/.claude/projects/<sanitized-cwd>/` keyed
by session id, independent of the working directory's own filesystem
contents, so the transcript was still available after the scratch directory
was cleaned up.

Re-running this capture from scratch is simple — session transcripts are a
side effect of any `claude -p` run — but note this only reused an existing
session opportunistically. There was nothing special about `ParseFile` that
required correlating this transcript with the earlier OTLP capture; a fresh,
unrelated real session would serve the test's purpose equally well.

## Redaction — why this fixture's redaction problem differs from the OTLP one

The OTLP golden capture happened to contain **no** prompt text, tool inputs,
tool outputs, or file paths at all, because none of `OTEL_LOG_USER_PROMPTS` /
`OTEL_LOG_TOOL_DETAILS` / `OTEL_LOG_RAW_API_BODIES` were set during that
capture. A transcript file has no such escape hatch: `message.content`,
tool `input`, tool `tool_result` content, and `cwd` are load-bearing fields
on *every* line, always populated, by design — this is what
`core/ingest/parser.go` exists to extract.

This fixture sidesteps the "real prompt/response text is sensitive" half of
that problem **by construction**, not by redacting it away: the underlying
session's prompt ("Read hello.txt and then use the Edit tool to append a
fourth line that says 'line four'.") was authored by the person capturing it
specifically to be a harmless, throwaway task against a scratch file with
placeholder content ("line one" / "line two" / "line three"). There is no
real user prompt, assistant response, or file content to protect here — all
of it is fixture-appropriate text already. **This is not a general-case
redaction technique.** A future re-capture from an actual working session
would carry real prompt/response text and real file contents that this
approach does not address; see the warning at the bottom of this file.

What *did* need redaction — real identifiers and real absolute host paths —
was handled the same way as the OTLP capture: hand-enumerated exact literal
string replacement (never regex), each real value mapped to a stable
zero-padded/placeholder equivalent so cross-record correlation (the
parent/child `uuid` chain, `tool_use_id` reuse across the tool_use block and
its `tool_result`) still works after redaction:

- Both real absolute paths (the scratch working directory and
  `hello.txt` within it) → `/home/dev/sample-project` /
  `/home/dev/sample-project/hello.txt`
- `sessionId` (constant across every line) → `00000000-0000-0000-0000-000000000000`
- `promptId` → `00000000-0000-0000-0000-000000000001`
- 11 distinct `uuid` / `parentUuid` / `sourceToolAssistantUUID` / `leafUuid`
  values → sequential `00000000-0000-0000-0000-000000000002` through
  `...00000000000c`, preserving the real parent-child order
- 2 `tool_use_id` values (the Read and Edit calls) →
  `toolu_000000000000000000000001` / `...002`
- 3 `requestId` values → `req_000000000000000000000001` / `002` / `003`
- 3 `message.id` values → `msg_000000000000000000000001` / `002` / `003`

Not present in this capture (so nothing to redact beyond the above): email
addresses, account identifiers, API keys, and tokens — this transcript
format (unlike the OTel resource attributes) carries no such fields on
these line types.

The full redacted transcript was read end to end by hand before being
committed, not just checked by script.

## A deliberate size/fidelity trade: eliding unparsed `attachment` payloads

3 of the 15 lines in the real transcript are `type: "attachment"` lines
(`deferred_tools_delta`, `agent_listing_delta`, `skill_listing`) carrying
large tool/agent/skill listing payloads. `parser.go`'s `parse()` switch has
no case for `type: "attachment"` — these lines are already dead weight from
the parser's point of view, always falling through unhandled.

Rather than commit their full (bulky, mostly boilerplate) content, this
fixture keeps each attachment line's real envelope (`type`, `uuid`,
`parentUuid`, `timestamp`, `cwd`, `sessionId` — needed to preserve the real
parent-child `uuid` chain across the whole file) and replaces only the
`attachment` payload itself with a short placeholder note identifying why it
was elided. This more than halved the fixture's size (23,568 → 11,075 bytes)
with zero loss of anything `parser.go` actually reads. If a future parser
change adds a case for `type: "attachment"`, this fixture will need a
re-capture with that payload intact — `golden_transcript_test.go` asserting
`len(result.Messages) == 5` (not more) is this fixture's tripwire for that:
it will fail, naming the count, the day an attachment line starts producing
a message.

## The contract this fixture pins

`message.model`, `message.stop_reason`, `message.usage.{input_tokens,
output_tokens,cache_read_input_tokens}`, `message.content[].type` (`text` /
`tool_use`), and each `tool_use` block's `id` / `name` / `input` are the
actual values this fixture exists to protect — these are exactly the fields
bug-d7741c8f named as feeding cost and token attribution. Do not "clean up"
or restructure them during a refresh without first checking whether
`core/ingest/parser.go` and `golden_transcript_test.go` still agree on what
they mean.

## How to re-capture

```bash
# 1. Run a small, real, throwaway session — one or two tool calls, against a
#    scratch directory with placeholder file contents, so the resulting
#    prompt/response/file-content text is fixture-appropriate by
#    construction rather than something that needs redacting:
claude -p "<a small, harmless task against scratch files>" \
  --permission-mode bypassPermissions --output-format json

# 2. Find the transcript Claude Code wrote for that session:
#    ~/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
#    (sanitized-cwd replaces "/" with "-" in the scratch directory's path)

# 3. If your capture used a REAL working session instead of a fresh
#    throwaway one, STOP: this fixture's approach of skipping prompt/response
#    redaction only works because the content was authored to be harmless.
#    A real session's prompt text, assistant text, tool inputs/outputs, and
#    file contents are exactly the sensitive payload this parser exists to
#    extract, and none of it can be dropped without gutting what the fixture
#    proves. Either author a fresh throwaway session (step 1) or redact
#    prompt/response/file-content text field-by-field and judge honestly
#    whether what remains still exercises real parser behavior.

# 4. Hand-enumerate every real identifier in the transcript (sessionId,
#    promptId, every uuid/parentUuid/sourceToolAssistantUUID/leafUuid, every
#    tool_use_id, every requestId, every message.id) and every real absolute
#    path, and do an exact literal find-and-replace (not a regex sweep) —
#    map each real value to the SAME fake value everywhere it appears so the
#    parent-child uuid chain and tool_use_id-to-tool_result correlation still
#    work after redaction.

# 5. Grep the redacted output for every original real value, the real email,
#    and "/Users/" / "/home/<real-user>" / "/private/" before going further.

# 6. Read the entire redacted transcript yourself, end to end. Do not rely
#    on step 5's grep as sufficient on its own.

# 7. Update this PROVENANCE.md (new capture date, new version, re-describe
#    content if it differs) and golden_transcript_test.go's field assertions
#    if the new capture's shape legitimately differs. Run
#    `go test ./core/ingest/...`.
```
