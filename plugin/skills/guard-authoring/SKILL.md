---
name: guard-authoring
description: Author, approve, and maintain project guard profiles (.wipnote/guard-profile.yaml). Use for custom quality/completion/yolo gates, polyglot monorepo per-directory guards, re-approving after drift, and understanding the trust boundary and autodetection fallback.
---

# Guard Authoring Skill

**Trigger keywords:** guard profile, guard init, quality gate, completion gate, yolo gate,
applies_when, guard authoring, re-approve guard, guard drift

---

## What a guard profile is

`.wipnote/guard-profile.yaml` is a hand-maintained YAML file committed to the repository.
It replaces wipnote's manifest autodetection with an explicit, version-controlled list of
commands. Treat it like any other config file — diff it, review it in PRs, edit it when
your toolchain changes. See `docs/guard-profiles.md` for the full reference.

---

## Schema

```yaml
guards:
  quality:          # run by `wipnote check --gate`
    - name: go-build               # short identifier (required)
      cmd: go build ./...          # shell command (required, non-empty)
      cwd: backend                 # repo-root-relative working dir (optional)
      applies_when:                # narrow when guard is active (optional)
        paths: ["backend/**/*.go"] # ** matches zero or more path segments
  completion:       # work-item completion gate
    - name: go-test
      cmd: go test ./...
  yolo:             # per-commit gate in yolo sessions
    - name: go-vet
      cmd: go vet ./...
approved:
  signature: "sha256:..."   # content hash; written by wipnote, never by hand
  by: "Alice"
  at: "2026-01-15T10:30:00Z"
```

Fields: `name` (required), `cmd` (required, non-empty), `cwd` (optional, repo-root-relative),
`applies_when.paths` (optional forward-slash globs — `**` crosses `/`, `*` does not).
Do not hand-edit `approved.signature`.

---

## Polyglot / monorepo example

```yaml
guards:
  quality:
    - name: go-test
      cmd: go test ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
    - name: npm-test
      cmd: npm test
      cwd: frontend
      applies_when:
        paths: ["frontend/**"]
    - name: py-test
      cmd: uv run pytest
      cwd: dataservice
      applies_when:
        paths: ["dataservice/**/*.py"]
  completion:
    - name: go-test
      cmd: go test ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
  yolo:
    - name: go-vet
      cmd: go vet ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
approved:
  signature: "sha256:..."
  by: "Alice"
  at: "2026-01-15T10:30:00Z"
```

`cwd` routes each toolchain to its subdirectory. `applies_when.paths` skips a guard when
no matching file exists in the repo — so the Go gate does not fire on a Python-only commit.

---

## Approval model and trust boundary

wipnote only honors a profile when `approved.signature` matches the sha256 of the guard
content. Falls back to autodetection (no error) when: profile absent, signature empty,
signature mismatch (edited after approval), or validation failure (unknown phase key or
empty `cmd`). The signature is order-independent — reformatting YAML does not break it,
but changing any guard field does.

---

## Launch-time setup and `wipnote guard init`

Every interactive launch (`wipnote claude/yolo/dev/codex/gemini`) auto-runs setup:
if no approved profile exists and the launch is interactive, it proposes guards from
manifests, prompts `[y/N]`, and on `y` signs + commits the profile. Non-interactive
launches skip silently.

```bash
wipnote guard init    # explicit: propose, review, approve, commit (re-runnable)
```

Run `wipnote guard init` after any edit to re-approve and record a fresh signature.

---

## Safe default

No approved profile → autodetection from `go.mod`, `package.json`, `Makefile`, etc.,
with a hint to run `wipnote guard init`. Use a committed profile for custom commands,
subdirectory routing, or `applies_when` filtering.
