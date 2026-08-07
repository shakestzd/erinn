# Launcher Isolation Runbook

This runbook documents the staged rollout of wipnote launcher isolation — the
transition from warn-only (host-production default) to worktree-enforced
isolation — and the gate conditions that must pass before advancing.

---

## Background

wipnote launchers (claude, codex, antigravity, yolo) can run either in-place (in
the main worktree) or inside a managed git worktree isolated per work item.
Slice-8 (plan-1670cacd) defined four deployment profiles:

| Profile | Default Rollout |
|---------|----------------|
| `host-production` | warn-only — never enforced by default |
| `devcontainer-dev` | config-gated (opt-in) |
| `ci-test` | config-gated (opt-in) |
| `local-plugin-dev` | warn-only |

**The host-production profile NEVER defaults to enforcement.** This is a
deliberate safety constraint: flipping a host to mandatory isolation has
user-visible consequences (every launch without a work-item ID would be
refused or degraded). The gate exists to prevent accidental breakage.

---

## Rollout Gate (slice-9)

Before the host profile can advance from warn-only to config-gated or
worktree-by-default, ALL of the following must hold:

1. **Doctor checks pass** — `wipnote launcher doctor` reports no stale
   worktrees, no canonical-root mismatch, and clean git state.

2. **Orphan sessions cleaned** — `wipnote cleanup orphan-sessions` reports
   zero orphans (or the user has acknowledged and dismissed them).

3. **Session reconciliation clean** — `wipnote reconcile` reports no
   ambiguous generator drift (runs clean or with `--strict` exit 0).

4. **Legacy sessions are visible** — Old sessions without `session_family_id`
   must remain visible in the dashboard labeled "legacy" (not hidden or
   broken). Verify with `wipnote session list`.

5. **Operator opt-in** — Set `WIPNOTE_ENFORCE_ISOLATION=true` in the
   environment before running any launcher. This is NOT set automatically.

---

## Step-by-Step: Advance Host to Enforced Mode

```bash
# Step 1: Run the doctor and read its output carefully.
wipnote launcher doctor

# Step 2: Clean up orphan sessions (non-destructive list first).
wipnote cleanup orphan-sessions --dry-run
# When satisfied, remove them:
wipnote cleanup orphan-sessions

# Step 3: Reconcile session artifacts.
wipnote reconcile --strict

# Step 4: Prune stale worktree admin entries if doctor flagged any.
# The doctor prints the exact command — e.g.:
git -C /path/to/repo worktree prune

# Step 5: Opt in on the host.
export WIPNOTE_ENFORCE_ISOLATION=true

# Step 6: Launch as normal — managed worktree will be required.
wipnote claude --work-item feat-xxxx
```

---

## Reverting Enforcement

Simply unset the environment variable:

```bash
unset WIPNOTE_ENFORCE_ISOLATION
```

The launcher falls back to warn-only immediately. No state is persisted.

---

## Legacy Session Compatibility

Sessions created before slice-4 (session_family_id introduced) have no
family metadata. These sessions:

- Remain **fully visible** in the dashboard (not hidden or broken).
- Are labeled **"legacy"** in `wipnote launcher doctor` output.
- Do NOT need migration; they age out naturally as new sessions replace them.

No action is required for legacy sessions. The doctor reports their count
for informational purposes only.

---

## What the Doctor Checks

`wipnote launcher doctor` reports (non-destructively):

| Check | What it detects |
|-------|----------------|
| Git state | Dirty main branch, ahead/behind origin |
| Managed worktrees | Stale entries (directory removed but admin record exists) |
| Session family | Legacy sessions count, canonical-root divergence |
| Rollout gate | Whether `WIPNOTE_ENFORCE_ISOLATION` is active |

The doctor **never auto-mutates** anything. It prints `git worktree prune`
and `wipnote cleanup orphan-sessions` as guidance but does not run them.

---

## Delegated Commands

The doctor explicitly delegates to two external commands:

- **`wipnote cleanup orphan-sessions`** — plan-ae0c37b2 slice-11 (feat-2c631aa9).
  Handles orphan NDJSON session directories with no DB row.
- **`wipnote reconcile`** — plan-83f909bc slice-5 (feat-f93fe770).
  Auto-commits done-but-uncommitted work-item artifacts and reports generator drift.

Do NOT reimplement their logic inside the doctor. Always delegate.

---

## Release Guidance

Recommended release order for flipping host default:

1. Ship this runbook + `wipnote launcher doctor` (slice-9, done).
2. Communicate the gate to users in release notes.
3. Allow a soak period (≥1 release cycle) in warn-only mode.
4. Re-run doctor against real user installations.
5. When all gate conditions hold: update `profile.go` to set
   `EnforceIsolation: true` for the `host-production` profile, gated on
   a new config key (e.g. `WIPNOTE_ISOLATION_PROFILE=enforced`).
6. Do NOT flip the default unconditionally — always require explicit opt-in.

---

## Production Acceptance Harness (slice-10, plan-1670cacd)

A two-tier acceptance harness validates the full parallel-launcher model
before each release. Both tiers build on the plan-ae0c37b2 contention
fixture (feat-156e0a1a) and add git-isolation / lineage / parallel-launcher
assertions on top.

### Tier 1: CI-safe (always runs in CI)

These tests use fake DB rows and existing API handlers — no real harness
binaries, no network, no exec-heavy subprocesses. They run in every CI job
via the `.github/workflows/ci.yml` "Acceptance harness" step:

```bash
# Runs automatically in CI (go test -short flag, fake binaries only).
go test -short -run 'TestParallelHarnessLineage|TestParallelHarness_MainStaysClean|TestMultiHarnessIngestion' ./cmd/wipnote/
```

**What they assert:**
- `TestMultiHarnessIngestion` — Claude + Codex + Gemini OTLP signals route
  correctly through the adapter registry into the ndjson sink.
- `TestParallelHarnessLineage` — three harnesses under one canonical project,
  subagent lineage chain (root → subagent at depth=1), claim collision
  surfaced at project level, session_family_id preserved for resumed sessions.
- `TestParallelHarness_MainStaysClean` — main and worktree roots appear as
  SEPARATE project groups in `/api/sessions/parallel` (main isolation derived
  from session metadata without inspecting git state).

### Tier 2: Full stress harness (opt-in, not in CI by default)

The heavy stress harness runs 20 producers for 30 seconds and validates
zero first-party SQLITE_BUSY events across 3 consecutive runs. It requires
a WAL-safe filesystem and a generous time budget:

```bash
# Opt-in: run before a release, not in routine CI.
export TMPDIR=/home/vscode/.gotest-tmp
go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/
```

This is the same fixture shipped in plan-ae0c37b2 slice-10 (feat-156e0a1a).
Slice-10 of plan-1670cacd (feat-7835351a) EXTENDS it — it does NOT duplicate
the contention workload.

### Exec-capable TMPDIR requirement (slice-8 profile)

Any test that spawns child binaries (childproc package, stress harness) needs
an exec-capable filesystem at TMPDIR. The devcontainer mounts `/tmp` as noexec.

```bash
# Always set this before running exec-capable tests in the devcontainer:
export TMPDIR=/home/vscode/.gotest-tmp
go build ./... && go vet ./... && go test ./...
```

Tests that require an exec-capable TMPDIR skip cleanly (never fail) when
the constraint is not met, so CI without a privileged exec root still passes.

### Release checklist

Before a release, run in order:

1. CI Tier 1 passes (automatic in CI pipeline).
2. `wipnote launcher doctor` — all gate conditions green.
3. Tier 2 stress harness:
   `go test -run TestSQLiteContentionStress -count=3 ./cmd/wipnote/`
4. Full suite with exec-capable TMPDIR:
   `export TMPDIR=/home/vscode/.gotest-tmp && go build ./... && go vet ./... && go test ./...`
5. Verify dashboard: `GET /api/sessions/parallel` shows correct canonical
   grouping, collision flags, and exec_root fields for all active sessions.
