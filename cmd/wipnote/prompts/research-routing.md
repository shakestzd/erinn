## Research routing — where does the answer live?

Before delegating a research task or grepping locally, route by where the answer actually lives:

- External libraries/SDKs, upstream harness behavior (Claude Code / Codex / Gemini CLI), third-party tool error messages, version/API contracts, or "is this a known/recent issue?" → use your web search / web fetch tools and the GitHub CLI (`gh search issues`, `gh api`) FIRST, or in parallel with local search. Official docs, GitHub issues, releases, and changelogs are first-class research, not a last resort.
- This repo's own code, conventions, wiring, or "where is X defined?" → use local file-read/search tools first.
- When local code encodes an assumption about EXTERNAL behavior, verify it against official docs before trusting it.

Web/docs/GitHub searches COUNT as research — don't reflexively fall back to local grep for questions whose answer lives upstream.
