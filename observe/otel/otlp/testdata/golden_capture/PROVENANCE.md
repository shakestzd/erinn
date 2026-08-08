# Golden OTLP capture (bug-735806ff)

## What this is

A real, raw OTLP/HTTP JSON export captured directly from a live Claude Code
session — not a Go-constructed synthetic fixture. It exists to catch drift
between what the harness actually emits and what `observe/otel/adapter`
assumes it emits: the adapter's own test suite (`claude_test.go`) builds its
inputs by hand, which cannot notice when a real capture stops matching those
assumptions (see bug-5652a5ba: `tool_use_id` sat in every real capture's
`attrs_json` for the column's entire lifetime while every synthetic test
fixture happened to never need to set it).

`golden_capture_test.go` (in the parent directory) decodes these three files
through wipnote's own `otlp.Decode*` + `adapter.ClaudeAdapter` pipeline — the
same code path production traffic goes through — and asserts on specific
fields. If a future harness release renames or drops an attribute this test
depends on, the assertion names the missing field directly.

## Provenance

| | |
|---|---|
| Harness | Claude Code |
| `service.version` | `2.1.224` |
| Captured | 2026-08-08 |
| Capture method | See "How to re-capture" below |
| Content | One `Read` tool call (Read → tool_decision → tool_result → llm_request → api_request → assistant_response), covering `claude_code.tool`, `claude_code.tool.execution`, `claude_code.tool.blocked_on_user`, `claude_code.llm_request` (traces), `tool_decision`, `tool_result`, `api_request`, `assistant_response` (logs), and `cost.usage`/`token.usage` (metrics) |

## Redaction

Every identifying value has been replaced with an obviously-synthetic
placeholder of the same shape (hex IDs stay the same length, UUIDs stay
UUID-shaped, etc.) so the fixture still exercises real cross-record joins —
in particular `tool_use_id`, which is shared across the `claude_code.tool`
span, the `claude_code.tool.execution` span, and the `claude_code.tool_result`
log record in this exact capture, and `trace_id`/`span_id`/`parent_span_id`,
which preserve the real parent-child span structure.

Redacted: `user.email`, `user.id`, `session.id`, `organization.id`,
`user.account_uuid`, `user.account_id`, `request_id` / `gen_ai.response.id`,
`client_request_id`, `prompt.id`, `message.uuid`, `tool_use_id` /
`gen_ai.tool.call.id`, `trace_id`, `span_id`, `parent_span_id`.

Not present in this capture (so nothing to redact): prompt text, tool inputs,
tool outputs, file paths, and assistant response text — Claude Code redacts
`assistant_response.response` to the literal string `<REDACTED>` by default,
and none of `OTEL_LOG_USER_PROMPTS` / `OTEL_LOG_TOOL_DETAILS` /
`OTEL_LOG_RAW_API_BODIES` were set during capture, so no content-bearing
attribute was ever emitted in the first place. The full redacted output was
read end-to-end by hand before being committed, not just checked by script.

## How to re-capture

Point a throwaway Claude Code session's OTLP export at a local dump server
instead of a real collector, run one or two small representative turns, and
redact before touching git:

```bash
# 1. A minimal HTTP server that writes each raw POST body to a file and
#    responds with a valid empty OTLP/HTTP JSON success body ({}). Listen on
#    a port nothing else uses; bind to 127.0.0.1 only.

# 2. Run a small, real session against it — one or two tool calls is enough,
#    per the design intent of this fixture (small, diff-reviewable, not a
#    whole session):
CLAUDE_CODE_ENABLE_TELEMETRY=1 \
CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 \
OTEL_TRACES_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json \
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port> \
OTEL_METRIC_EXPORT_INTERVAL=1000 OTEL_LOGS_EXPORT_INTERVAL=1000 OTEL_TRACES_EXPORT_INTERVAL=1000 \
claude -p "<a small, real task>" --permission-mode bypassPermissions --output-format json

# 3. From the captured files, pick ONE representative traces/logs/metrics
#    triplet covering the span and log kinds you need (grep each file's
#    resourceSpans[].scopeSpans[].spans[].name / resourceLogs[]...body to
#    find a good one).

# 4. Redact every value listed above with a literal, hand-enumerated
#    find-and-replace (not a regex sweep — a pattern that doesn't match lets
#    a real value through silently). Keep the same real value mapped to the
#    same fake value across all three files so cross-file correlation still
#    works.

# 5. Grep the redacted output for every original real value, the real email,
#    and "/Users/" / "/home/" / "/private/" before going any further.

# 6. Read the entire redacted output yourself, end to end. Do not rely on
#    step 5's grep as sufficient on its own — it only catches what you
#    thought to check for.

# 7. Update this PROVENANCE.md: new capture date, new service.version, and
#    re-describe the content if the turns you captured differ from the
#    table above. Run `go test ./observe/otel/otlp/...` and update the test's
#    field assertions if the new capture's shape legitimately differs.
```

If OTEL_LOG_TOOL_DETAILS, OTEL_LOG_USER_PROMPTS, or OTEL_LOG_RAW_API_BODIES
end up set (accidentally inherited from the ambient environment, for
example) the capture will contain real file paths, prompt text, or full API
bodies — check for and unset all three explicitly before capturing, and
re-read step 5's redaction list against whatever new attributes those flags
add before trusting a re-capture done with them on.
