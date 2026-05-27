# Guard Profiles

A guard profile is a hand-maintained YAML file committed at `.wipnote/guard-profile.yaml`.
It gives wipnote an explicit, version-controlled list of commands to run as quality,
completion, and yolo gates — replacing manifest autodetection with something you own,
diff, and review in pull requests.

---

## Schema

```yaml
guards:
  quality:          # run by `wipnote check --gate`
    - name: <string>               # short identifier shown in gate output (required)
      cmd: <shell command>         # command to run (required, non-empty)
      cwd: <relative path>         # repo-root-relative working dir (optional)
      applies_when:                # narrow when guard is active (optional)
        paths:
          - "internal/**/*.go"     # ** matches zero or more path segments
          - "cmd/**/*.go"
  completion:       # work-item completion gate
    - name: <string>
      cmd: <shell command>
      cwd: <relative path>
      applies_when:
        paths: ["**/*.go"]
  yolo:             # per-commit gate in yolo sessions
    - name: <string>
      cmd: <shell command>
approved:
  signature: "sha256:..."   # content hash; written by wipnote, not by hand
  by: "<git user.name>"     # approver identity
  at: "<RFC3339>"           # UTC approval timestamp
```

### Field reference

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Short label shown in gate output |
| `cmd` | yes | Shell command; empty string is a validation error |
| `cwd` | no | Repo-root-relative working directory; defaults to repo root |
| `applies_when.paths` | no | Forward-slash globs; guard skipped when no repo file matches. Omit to run unconditionally. |
| `approved.signature` | set by wipnote | Do not edit — any change invalidates approval |
| `approved.by` / `at` | set by wipnote | Git `user.name` and RFC3339 UTC timestamp of approval |

### Phases

| Phase key | When it runs |
|-----------|-------------|
| `quality` | `wipnote check --gate` |
| `completion` | `wipnote feature complete`, `wipnote bug complete`, etc. |
| `yolo` | Per-commit PostToolUse gate in yolo sessions |

All three phases are optional. Omit a phase key to inherit autodetection for that phase.

### Glob semantics

`applies_when.paths` uses forward-slash globs with doublestar semantics:

- `**` matches zero or more path segments (`internal/**/*.go` matches both
  `internal/foo.go` and `internal/pkg/sub/foo.go`).
- `*` does not cross `/` (standard `path.Match` semantics).
- Paths are always repo-root-relative; no leading `./` or absolute paths.
- A guard whose paths match no file in the repo is silently skipped — safe for
  speculative globs.

---

## Approval model and trust boundary

wipnote only honors a profile when `approved.signature` equals the sha256 of the
canonical guard content. The signature covers only the `guards:` block — the `approved:`
block itself is excluded.

**Falls back to autodetection without error when:**
- No profile file present.
- `approved.signature` is empty.
- Signature no longer matches content (edited after approval).
- Validation failure: unknown phase key or empty `cmd`.

In every fallback case wipnote emits a hint to run `wipnote guard init`.

**Order-independence:** the signature is order-independent. Guards are sorted by full
canonical tuple; phases iterate in fixed order (`quality → completion → yolo`);
`applies_when.paths` are sorted. Reformatting YAML or reordering guards does not change
the signature. Adding, removing, or modifying any guard field does.

**Staleness:** passing gate records store the profile signature. A completion validated
against a since-changed profile is reported as stale.

---

## Setting up a guard profile

### Launch-time proposal (automatic)

Every interactive `wipnote claude`, `wipnote yolo`, `wipnote dev`, `wipnote codex`, and
`wipnote gemini` launch calls `ensureGuardProfile`:

1. Approved profile exists → no-op.
2. Non-interactive (no TTY) → skip silently, never block.
3. Interactive, no approved profile → inspect manifests, propose a phase-grouped profile,
   print it for review, prompt `[y/N]`.
   - `y`: sign, write `.wipnote/guard-profile.yaml`, commit.
   - `N` (or anything else): defer — re-offered on the next interactive launch.

### Explicit setup and drift re-approval

```bash
wipnote guard init    # propose, review, approve, commit (re-runnable)
```

`wipnote guard init` is always re-runnable. Use it:

- To set up a profile for the first time (interactive or non-interactive context).
- After any edit to the `guards:` section (**drift re-approval**): run it to record a
  fresh `approved.signature` and commit.

The proposal is **prune-not-invent**: it surfaces detected signals with provenance
(e.g. `go.mod`, `Makefile:test`) and flags low-confidence entries. Remove guards you do
not want; do not invent new ones from scratch during approval.

---

## Polyglot / monorepo example

```yaml
guards:
  quality:
    - name: go-build
      cmd: go build ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
    - name: go-vet
      cmd: go vet ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
    - name: go-test
      cmd: go test ./...
      cwd: backend
      applies_when:
        paths: ["backend/**/*.go"]
    - name: npm-build
      cmd: npm run build
      cwd: frontend
      applies_when:
        paths: ["frontend/**"]
    - name: npm-test
      cmd: npm test
      cwd: frontend
      applies_when:
        paths: ["frontend/**"]
    - name: py-lint
      cmd: uv run ruff check .
      cwd: dataservice
      applies_when:
        paths: ["dataservice/**/*.py"]
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

Notes:

- `cwd` routes each toolchain to its own subdirectory (relative to repo root).
- `applies_when.paths` prevents the Go gate from firing on a purely Python commit.
- A guard with paths matching no repo file is silently skipped — safe to include
  even if the directory does not exist yet.
- The `yolo` phase uses a lightweight vet guard to keep per-commit latency low.

---

## Validation rules

- Valid phase keys: `quality`, `completion`, `yolo`. Unknown keys are rejected.
- `cmd` must be non-empty. Validation failures are treated as absent profiles — the
  gate falls back to autodetection rather than erroring out.

---

## File ownership

`.wipnote/guard-profile.yaml` is committed to the repository. Changes belong in pull
requests with the same review process you apply to CI config changes.

Do not hand-edit `approved.signature` — it is computed by `wipnote guard init`. Any
manual change to the `guards:` section (or to `approved:`) invalidates the signature
and triggers the fallback until you re-approve.

---

## CLI reference

| Command | Description |
|---------|-------------|
| `wipnote guard init` | Propose, review, approve, and commit the guard profile (re-runnable) |
| `wipnote check --gate` | Run `quality`-phase guards (autodetects if no approved profile) |
| `wipnote feature complete <id>` | Run `completion`-phase guards at work-item completion |
