# wipnote

Local-first observability and coordination platform for AI-assisted development.

## Architecture

| Layer | Role |
|-------|------|
| `.wipnote/*.html` | Canonical store — single source of truth |
| SQLite (WAL-safety-driven path: `~/.cache/wipnote/<hash>/` if WAL-safe, else `~/.local/share/wipnote/<hash>/` if WAL-safe, else ephemeral tmp; derived, not committed) | Per-user read index for queries and dashboard |
| Go binary (`wipnote`) | CLI + hook handler |

## For AI Agents

All CLI usage, safety rules, and best practices are delivered by the wipnote plugin.
Run `wipnote help --compact` for the CLI reference.

## Supported Harnesses

wipnote currently ships the same plugin to three AI coding harnesses:

- **Claude Code** — plugin tree at `plugin/`
- **Codex CLI** — marketplace tree at `port/packages/codex-marketplace/`
- **Gemini CLI** — extension tree at `port/packages/gemini-extension/`

All three trees are **generated** from the same source of truth at
`packages/plugin-core/manifest.json` by `wipnote plugin build-ports`. Shared
markdown assets (commands, agents, skills, templates) live in `plugin/…/`.
Codex agent markdown is translated into custom-agent TOML during generation, then
`wipnote codex` mirrors those TOML files into Codex's documented `.codex/agents`
lookup locations when launching. See `packages/plugin-core/README.md` for
details.

**Codex Agent Persistence:** `wipnote codex` copies `wipnote-*.toml` files into `~/.codex/agents/` on every launch but intentionally never deletes them on exit. This allows users running bare `codex` (outside the wipnote launcher) to see the wipnote agents in `/agent` listings and invoke them from any session. Stale `wipnote-*.toml` files from old versions are cleaned up on the next `wipnote codex` launch—the function `ensureCodexCustomAgentsInstalled` removes any `wipnote-*.toml` no longer present in the source plugin tree. Non-`wipnote-*` files at the target are never touched.

Agent role names are capability-based across harnesses. Use names like
`patch-coder`, `feature-coder`, and `architect-coder`; model choices such as
Claude `haiku`/`sonnet`/`opus`, Codex `gpt-*`, or Gemini `flash`/`pro` belong in
the per-harness model configuration, not in the role name.

## Dogfooding

This project uses wipnote to develop itself. `.wipnote/` contains real work items — not demos.

## Dashboard Port Convention

Production/local host installs use the default dashboard port:

```bash
wipnote serve
# http://127.0.0.1:8080
```

Inside the VS Code devcontainer, run the dashboard on container port 8088 and
bind all interfaces so the host can reach the forwarded port:

```bash
wipnote serve --bind 0.0.0.0 --port 8088
# host: http://127.0.0.1:8088
```

Do not use the production default port 8080 for devcontainer dashboard sessions.

## Temporal Awareness

A `UserPromptSubmit` hook injects the current local timestamp (with timezone) on every user prompt. Use it to reason about elapsed time between messages — detect stale context in long sessions, recognize when a session has been resumed after a gap, and avoid treating old references as fresh.
