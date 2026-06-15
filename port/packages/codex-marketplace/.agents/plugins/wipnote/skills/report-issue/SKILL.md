---
name: wipnote:report-issue
description: Report a wipnote bug or issue to the development team from any supported harness. Use when the user asks to report an issue, file a bug, or says the report-issue slash command is unavailable.
---

# wipnote Report Issue

Use this skill to help the user file a concise, safe issue for `shakestzd/wipnote`.

## Instructions

1. Ask for a short title and description if the user has not already provided them.
2. Gather safe environment context when available:
   - `wipnote version`
   - `go version`
   - OS via `uname -s -r` or the platform equivalent
   - active harness version, such as `claude --version`, `codex --version`, `gemini --version`, or `agy --version`
3. Ask whether to include steps to reproduce, expected behavior, actual behavior, and any error text.
4. Scrub secrets, API keys, tokens, private file contents, and personal data before drafting or submitting.
5. If `gh` is authenticated and the user wants you to submit it, create the issue:

```bash
gh issue create --repo shakestzd/wipnote --title "Bug: <title>" --body-file <draft-file>
```

6. If GitHub submission is unavailable, provide a ready-to-paste issue body and link to https://github.com/shakestzd/wipnote/issues/new.

## Issue Body Template

```markdown
## Bug Report

**Description:** <user description>

### Environment
- wipnote version: <version>
- Go version: <version>
- OS: <os>
- Harness version: <version>

### Steps to Reproduce
<steps>

### Expected Behavior
<expected>

### Actual Behavior
<actual>

### Error Output
~~~text
<error messages>
~~~

---
Reported via wipnote issue reporter
```
