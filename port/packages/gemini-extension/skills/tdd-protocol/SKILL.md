---
name: wipnote:tdd-protocol
description: TDD protocol for sub-agent implementation tasks — write failing tests first, run quality gates, commit format, attribution rules
---

# TDD Protocol for Sub-Agent Implementation

Use this skill for any implementation task dispatched by `/wipnote:execute`. It defines the mandatory workflow: failing tests first, quality gates, commit format, and work-item attribution.

**Trigger keywords:** write tests first, TDD, test-driven, quality gate, commit format, attribution

---

## Step 1: Attribution — Before Any Code

```bash
wipnote feature start {feature_id}
```

Run this as the FIRST command, before reading files, writing tests, or any implementation.

---

## Step 2: Write Failing Tests FIRST (mandatory)

Create test file(s) before writing any implementation:

1. Write tests that express the acceptance criteria.
2. Verify tests **compile** (no syntax errors).
3. Verify tests **fail** — the implementation doesn't exist yet.

```bash
go test ./...   # Expect failures here, not compilation errors
```

Do not write implementation code until tests exist and fail for the right reason.

---

## Step 3: Implement Until Tests Pass

Write the minimum implementation to make all tests pass. Do not add code that is not covered by a test.

---

## Step 4: Quality Gates (mandatory before commit)

```bash
go build ./... && go vet ./... && go test ./...
```

All three must pass. Fix any errors before committing — even pre-existing ones in files you touched.

---

## Step 5: Commit Format

Use conventional commits with the feature ID in the message:

```
git commit -m "feat({scope}): {description} ({feature_id})"
```

Include attribution in every commit:

```
Co-Authored-By: Claude Sonnet <noreply@anthropic.com>
```

---

## Step 6: Complete Attribution

```bash
wipnote feature complete {feature_id}
```

Run this after quality gates pass and the commit is made.

---

## Report

After completion, report:
- Files changed and lines added
- Test names and count
- Quality gate results
