# wipnote — OSS / Show-HN Positioning Draft

> Working draft for founder review. All copy is editable — the structure is the deliverable.

---

## 1. README First-Screen Block

### One-line value prop

**wipnote** — local-first causal-lineage and coordination layer for AI-assisted dev: know *why* every commit exists, across Claude Code, Codex, and Antigravity.

### Elevator (2-3 lines)

wipnote captures the full chain from work item to commit to agent session in a plain `.wipnote/*.html` store that lives in your repo. It composes under orchestrators like OpenAI Symphony and runs against whichever AI coding harness you use today. Nothing lives on a server; your provenance is always yours.

### Lineage chain — concrete snippet

```
$ wipnote lineage feat-5d2188b9

feat-5d2188b9  OSS launch readiness
  └─ session abc12  claude-sonnet-4-6  2026-05-19T14:03Z
       └─ spawn  feature-coder → patch-coder
       └─ commit 47fa13f  "docs(feat-5d2188b9): OSS Show-HN positioning draft"
  └─ session def99  codex/gpt-4o  2026-05-19T14:41Z  [experimental]
       └─ commit 3c9b2af  "fix(feat-5d2188b9): cold-clone aha path"
```

No server. No external service. One HTML file per work item, committed alongside your code.

---

## 2. Differentiation Table

| Job | wipnote | Symphony (upstream) | Kata (gannonh/kata fork) |
|-----|---------|--------------------|-----------------------|
| **Control plane** | Coordination layer — work items, sessions, agent spawns | Orchestration spec — routes tasks between agents | Third-party CLI wrapping upstream Symphony spec |
| **Causal lineage** | First-class: work item → commit → session → agent-spawn chain | None | None |
| **Local-first store** | `.wipnote/*.html` in your repo; no cloud dependency | Not scoped | Not scoped |
| **Cross-harness** | Claude Code + Codex + Antigravity session capture | Codex/app-server-protocol only | Extends Symphony; same lock-in |
| **Tracker** | Any (wipnote is tracker-agnostic) | Linear only (non-Linear is a documented upstream TODO) | Inherits Symphony's Linear lock |
| **Model-agnostic** | Yes — harness-level, not claim | No — upstream Symphony is Codex-locked | Yes — Kata adds this; it is NOT upstream Symphony |
| **Composes with Symphony** | Yes — wipnote is the provenance layer Symphony lacks | N/A | N/A |

**Honest overlap risk:** wipnote's `wipnote yolo` orchestration surface does overlap with Symphony's routing intent. The positioning bet is that Symphony's momentum becomes tailwind — wipnote provides what Symphony deliberately omits (causal lineage, local store, multi-harness). If OpenAI adds native lineage to Symphony the moat narrows.

---

## 3. Show-HN Submission Titles

**Top pick:**
> Show HN: wipnote – local-first causal lineage for AI-assisted dev (Claude/Codex/Antigravity)

**Candidate 2:**
> Show HN: wipnote – know why every commit exists across AI coding agents

**Candidate 3:**
> Show HN: wipnote – provenance + coordination layer for Claude Code, Codex, Antigravity

Constraints applied: all ≤80 chars, no "revolutionary"/"game-changing", HN Show-HN prefix, no trailing period.

---

## 4. Show-HN First Comment (founder framing)

> Hi HN. I built wipnote to solve a problem I kept hitting: after a long AI-assisted coding session I couldn't answer "why does this commit exist?" The agent had done real work but the decision trail was gone.
>
> wipnote captures the causal chain — work item → session → agent spawns → commits — in plain `.wipnote/*.html` files committed alongside your code. Nothing leaves your machine. The store is just HTML so it's readable without any tooling and survives forever.
>
> It ships a plugin to Claude Code, Codex CLI, and Antigravity. Honest scope note: the Claude Code integration is production-stable; Codex and Antigravity are experimental and I'd call them early-access. Cross-harness session capture works but the rough edges show.
>
> On Symphony: wipnote is not an orchestrator and is not trying to be. Symphony is an OpenAI spec for routing tasks between agents; wipnote is the provenance layer that sits beneath that. They compose. If you use Symphony today, wipnote gives you the lineage graph Symphony doesn't provide.
>
> What I'm looking for: does the "why does this commit exist" problem resonate? Is the local-first constraint a feature or a dealbreaker for your workflow? Harsh feedback welcome — especially from people who tried it and bounced.

Word count: ~210. Adjust the personal voice as needed.

---

## 5. Pre-Launch Must-Do Checklist

- [ ] **Positioning locked** — README first-screen matches the "provenance/coordination layer, not orchestrator" frame; remove or demote any language that positions wipnote as an orchestrator.
- [ ] **Cold-clone aha works** — `git clone <repo> && wipnote install && wipnote feature start test-001 && echo "done"` succeeds on a clean machine in under 2 minutes; the lineage output is non-empty and readable.
- [ ] **Honest scope labels** — Codex and Antigravity integrations are visibly marked experimental in README, CLI help, and any demo GIF; no screenshot implies production parity with Claude Code.
- [ ] **2-session concurrency sane** — run two `wipnote feature start` calls against the same item in parallel; verify no corrupt HTML, no silent data loss, and the second claim either wins cleanly or returns a clear error.
