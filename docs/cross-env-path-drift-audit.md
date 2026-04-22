# Cross-Environment Path Drift Audit

## Context

HtmlGraph is developed across two hosts: macOS (`/Users/shakes/DevProjects/htmlgraph`)
and a Linux Codespaces devcontainer (`/workspaces/htmlgraph`). Several committed artifacts
carry host-local absolute paths that break or mislead agents on the other host. Two
specific instances were fixed (bug-f4760452, bug-e1c968fe) and a pre-commit guardrail
landed (bug-4b6d8369). This document classifies every on-disk artifact class that can
carry such paths, assigns remediation status, and lists remaining open items.

---

## Audit Matrix

| Artifact | Classification | Remediation | Status |
|---|---|---|---|
| `.htmlgraph/plans/*.yaml` | relative-rewriteable | Redact to placeholders; pre-commit guardrail | Done (bug-f4760452, bug-4b6d8369) |
| `.htmlgraph/bugs/*.html` body text | relative-rewriteable | Redact; pre-commit guardrail | Done (bug-f4760452, bug-4b6d8369) |
| `.htmlgraph/sessions/*.html` `data-project-dir` attr | ephemeral | Written once at session creation from live `$CLAUDE_PROJECT_DIR`; no repair needed | OK — by design |
| `.htmlgraph/sessions/*.html` tool-call list | ephemeral | Replay log of actual agent actions; paths are historical record, not config | OK — by design |
| `htmlgraph.db` `sessions.project_dir` column | ephemeral | Populated from `CLAUDE_PROJECT_DIR` at `SessionStart`; value reflects host at time of session | OK — by design; SQLite is not synced cross-host |
| `htmlgraph.db` `sessions.transcript_path` column | ephemeral | Absolute path to Claude project JSONL on writing host; correct for that host | OK — by design |
| `.claude/settings.local.json` | must-stay-absolute | Per-machine file, gitignored; stale paths silently override project config | Open — see §1 |
| `.claude/worktrees/*/.git` gitdir line | must-stay-absolute | Rewrite on `SessionStart` when mismatch detected | Done (bug-e1c968fe) |
| `.claude/agent-memory/**/*.md` | relative-rewriteable | Agent-authored; can contain observed host paths in prose | Open — see §2 |
| `~/.claude/projects/<slug>/memory/*.md` | relative-rewriteable | User-level memory; can store examples with host paths | Open — see §3 |
| `plugin/hooks/hooks.json` | relative-rewriteable | All hook commands use bare `htmlgraph hook …` — no absolute paths | OK — clean |
| `plugin/hooks/bin/htmlgraph` (binary) | ephemeral | Compiled per-host; not committed (gitignored) | OK — no issue |
| `.env` / `.env.local` | N/A | Neither file exists in this repo | N/A |
| `.claude/skills/**` prose referencing host paths | relative-rewriteable | Auto-synced from `plugin/skills/`; one stale path found in deployment skill reference | Open — see §4 |

---

## Deep-Dive Per Artifact

### `.htmlgraph/plans/*.yaml` and `.htmlgraph/bugs/*.html` body text

**What paths:** Free-form text fields (precondition notes, bug descriptions) copied verbatim
from agent prose. Examples found before remediation:
- `plan-c248b73f.yaml:178` — `/home/vscode/.copilot/session-state/…`
- `bug-71fc095f.html:43` — `/Users/shakes/DevProjects/shakestzd/.htmlgraph/htmlgraph.db`

**Who writes:** `htmlgraph plan create`, `htmlgraph bug create`, agent-generated updates.

**Remediation:** bug-f4760452 redacted the two live instances. bug-4b6d8369 added
`scripts/check-host-paths.sh` (invoked from `.githooks/pre-commit --staged`) to block
future commits. Check script excludes `bug-4b6d8369.html` itself (cites paths as examples).

**Current violations** (from `scripts/check-host-paths.sh --all`): `plan-251676c5.yaml`
and its `.html` mirror contain `/home/vscode/` in legitimate technical spec text describing
expected devcontainer paths — these are not stale drift but intentional documentation.
They are currently caught by the scanner without an allowlist entry.

**Remaining gap:** No allowlist mechanism for intentional `/home/vscode/` references in
spec prose. The scanner produces false positives for these. See Open §5.

---

### `.htmlgraph/sessions/*.html`

**What paths:** Every session HTML carries `data-project-dir="/abs/path/to/repo"` on the
root element, and the tool-call `<li>` log includes every absolute path the agent accessed
during the session.

**Who writes:** `internal/hooks/session_html.go` writes `data-project-dir` from the live
`CLAUDE_PROJECT_DIR` env var at session creation. Tool-call entries are appended by
`PostToolUse` hooks.

**Classification:** **Ephemeral.** Session HTMLs are observability records — they document
what happened on a specific host at a specific time. The `data-project-dir` attribute is
correct for the originating host. These files are committed to the repo as historical
records, so they will contain cross-host paths forever. This is expected and not a bug.

**No remediation needed.** The pre-commit scanner's scope intentionally covers `.htmlgraph/`
but session HTMLs should arguably be in an allowlist given their historical nature.

---

### `htmlgraph.db` (SQLite)

**What paths:** `sessions.project_dir` — absolute path of the project on the writing host.
`sessions.transcript_path` — absolute path to the Claude JSONL transcript file on the writing
host (e.g. `/home/vscode/.claude/projects/-workspaces-htmlgraph/abc123.jsonl` or
`/Users/shakes/.claude/projects/-Users-shakes-…/abc123.jsonl`).

**Who writes:** `internal/hooks/session_start.go` (`SessionStart` handler) reads
`CLAUDE_PROJECT_DIR` from the env and the Claude hook payload.

**Classification:** **Ephemeral.** SQLite is not committed to the repo (it is `.gitignore`d
or rebuilt via `htmlgraph reindex`). Each host maintains its own DB. Cross-host path
drift in SQLite is a non-issue because the DB is never synced.

**No remediation needed.**

---

### `.claude/settings.local.json`

**What paths:** Permission allow-list entries with absolute paths to plugin binaries and
scripts, `statusLine.command` pointing to an absolute wrapper script. Confirmed live
examples:
- `"Bash(CLAUDE_PLUGIN_ROOT=/Users/shakes/.claude/plugins/cache/…)"` permission rules
- `"statusLine": {"command": "/Users/shakes/.claude/omp-claude-wrapper.sh"}`
- Multiple `"Bash(/Users/shakes/DevProjects/htmlgraph/…:*)"` permission entries

**Who writes:** Claude Code writes this file whenever the user approves a new permission
pattern or changes Claude settings. It is gitignored (per-machine by design) but the
file is committed in this repo (`.claude/settings.local.json` appears in git history and
is tracked).

**Classification:** **Must-stay-absolute** — the paths are intentional, referencing
real host-local binaries. However the file should not be committed; it should be
gitignored.

**Remediation:** Verify `.gitignore` excludes `.claude/settings.local.json`. If it is
tracked, add it to `.gitignore`. When the file contains paths from the other host, agents
see silently-failing `statusLine` or hook commands with no error. Detection: `grep -n
"/Users\|/home/vscode" .claude/settings.local.json`. See Open §1.

---

### `.claude/worktrees/*/.git`

**What paths:** The `.git` file in a linked worktree is a text file containing
`gitdir: /abs/path/to/.git/worktrees/<id>`. When the worktree was created on macOS and
the file is read on Linux, the path does not exist.

**Who writes:** `git worktree add` writes the `.git` file at creation time using the
current host's absolute repo root. `internal/worktree/worktree.go` calls `git worktree add`.

**Classification:** **Must-stay-absolute** — git requires an absolute `gitdir` line in
linked worktree `.git` files (relative paths are not supported by all git versions).

**Remediation:** bug-e1c968fe tracked this. The worktree `.git` file for
`trk-787f57d3` and `trk-cd61bbae` currently show correct `/workspaces/htmlgraph/…` paths.
However, no code in `internal/hooks/session_start.go` or `internal/worktree/` currently
detects and repairs a stale `gitdir` — the fix was manual rewrite. A `session-start`
hook repair step was proposed in bug-e1c968fe but not implemented. See Open §6.

---

### `.claude/agent-memory/**/*.md`

**What paths:** Agent-written reference documents that record observed environment facts.
Confirmed: `agent-memory/htmlgraph-researcher/reference_external_cli_integration.md`
contains `/Users/shakes/.nvm/versions/node/v22.20.0/bin/` and
`/Users/shakes/.codex/skills/htmlgraph-tracker`.

**Who writes:** Agents writing memory files during sessions on macOS.

**Classification:** **Relative-rewriteable.** These are factual snapshots at time of
writing. On Linux the paths do not exist and the information misleads agents about
installed locations.

**Remediation:** Files should use relative references or omit host-specific install paths.
The pre-commit scanner currently excludes `.claude/worktrees/` but covers `.claude/`
broadly — meaning these files are scanned. They are not currently flagged because the
scanner uses `.claude/` scope but the `agent-memory/` path may be outside scope.
Confirm scanner covers `agent-memory/` and add a fix-forward policy. See Open §2.

---

### `~/.claude/projects/<slug>/memory/*.md`

**What paths:** User-level MEMORY.md references the `settings_local_cross_machine_drift.md`
file by name (no host path). The `settings_local_cross_machine_drift.md` file itself
contains `/Users/shakes/…` and `/home/vscode/…` as illustrative examples in prose.

**Who writes:** Claude Code memory tool, via agent sessions.

**Classification:** **Relative-rewriteable.** Illustrative examples are acceptable in
prose but should be clearly labelled as examples, not config.

**Remediation:** Low priority. The memory files are outside the repo and not committed.
No action required unless agents start treating memory examples as live config. See Open §3.

---

### `.claude/skills/**` (auto-synced from `plugin/skills/`)

**What paths:** `plugin/skills/deployment-automation-skill/reference.md` contains
`cd /Users/shakes/DevProjects/htmlgraph` in an example command block.

**Who writes:** Hand-edited skill source files under `plugin/`.

**Classification:** **Relative-rewriteable.** Example commands in skill docs should use
`$PROJECT_ROOT` or a generic placeholder rather than a host-specific path.

**Remediation:** Update `plugin/skills/deployment-automation-skill/reference.md` to
replace the absolute path with a placeholder. Regenerate ports with `htmlgraph plugin
build-ports`. Add skill files to the pre-commit scanner scope. See Open §4.

---

## Open Follow-ups

1. **Verify `.claude/settings.local.json` gitignore status.** If the file is committed, add
   it to `.gitignore`. If it must be committed for any reason, strip the absolute-path
   permission entries and rely on project-level `settings.json` instead. (Medium priority.)

2. **Extend pre-commit scanner to `.claude/agent-memory/`.** Currently the scanner covers
   `.claude/**` but the wording in `check-host-paths.sh` excludes `worktrees/` —
   confirm `agent-memory/` is in scope, and update the `STATIC_ALLOWLIST` or add a
   `--fix` mode to redact stale paths. (Low priority — no runtime impact.)

3. **Add user-memory path guidance.** Document in the project MEMORY.md that illustrative
   host paths in memory prose should be labelled `(example)` or use `<host-path>` placeholders.
   (Low priority — informational only.)

4. **Fix absolute path in `plugin/skills/deployment-automation-skill/reference.md`.**
   Replace `/Users/shakes/DevProjects/htmlgraph` with `<project-root>` or `$(git rev-parse
   --show-toplevel)`. Regenerate all plugin ports and commit. (Low priority — doc-only.)

5. **Add allowlist for intentional `/home/vscode/` in spec prose.** `plan-251676c5` contains
   legitimate technical spec text describing expected devcontainer install paths. The
   pre-commit scanner currently flags it as a false positive. Either add these files to
   `scripts/host-paths-allowlist.txt` or tighten the scanner to distinguish config
   references from descriptive prose. (Medium priority — reduces scanner noise.)

6. **Implement worktree `.git` auto-repair at session start.** bug-e1c968fe identified the
   problem and was closed after manual repair, but no code was written to detect and rewrite
   a stale `gitdir` line on `SessionStart`. Add detection in `internal/hooks/session_start.go`
   or `internal/worktree/worktree.go`: if `CWD` is inside `.claude/worktrees/` and the `.git`
   file's `gitdir` prefix does not match the current repo root, rewrite it before any git
   command runs. (High priority — prevents git failure in cross-environment worktree sessions.)

---

## Classification Summary

- **Ephemeral (5):** session HTMLs, SQLite `project_dir`, SQLite `transcript_path`, compiled
  binary, `.env` files (absent)
- **Relative-rewriteable (4):** plan/bug body text (done), agent-memory files, user memory
  prose, skill reference docs
- **Must-stay-absolute (2):** `.claude/settings.local.json`, worktree `.git` gitdir line

**Remediated:** plan/bug body text (bug-f4760452), worktree `.git` manual repair (bug-e1c968fe),
pre-commit guardrail (bug-4b6d8369).

**Open:** Items 1 (settings.local.json gitignore), 4 (skill reference), 5 (scanner false
positives), 6 (auto-repair worktree gitdir at session start).
