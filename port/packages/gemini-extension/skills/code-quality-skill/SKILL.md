---
name: code-quality
description: Code hygiene, quality gates, and pre-commit workflows. Use for linting, type checking, testing, and fixing errors. Works for Go, JavaScript/TypeScript, Python, Rust, and any other language.
---

# Code Quality Skill

Use this skill for code hygiene, quality gates, and pre-commit workflows.

**Trigger keywords:** code quality, lint, type checking, pre-commit, build, fix errors, quality gate

## Work Item Attribution

Quality gate runs should be attributed. Before fixing errors:
1. Ensure a feature or bug is active: `wipnote status`
2. If fixing a bug: `wipnote bug create "Fix: description" --track <trk-id>` then `wipnote bug start <id>`
3. Run `wipnote help` for available commands

---

## Quality Gate Pattern: BUILD → LINT → TEST

Every project enforces the same three-phase pattern. Only commit when all three pass.

### Step 1: Detect Project Type

Check for a project manifest in the repository root:

| File | Language/Runtime |
|------|-----------------|
| `go.mod` | Go |
| `package.json` | JavaScript / TypeScript (Node) |
| `pyproject.toml` or `requirements.txt` | Python |
| `Cargo.toml` | Rust |
| `pom.xml` or `build.gradle` | Java / JVM |

Multiple manifests may coexist (e.g., a Go backend with a `package.json` frontend) — run quality gates for each.

### Step 2: Run the Three Phases

#### Go (`go.mod`)

```bash
go build ./...           # BUILD — type checking + compilation
go vet ./...             # LINT  — static analysis
go test ./...            # TEST  — run test suite
```

#### JavaScript / TypeScript (`package.json`)

```bash
npm run build            # BUILD — or tsc --noEmit for type-check only
npm run lint             # LINT  — eslint, biome, or equivalent
npm test                 # TEST  — jest, vitest, mocha, etc.
```

#### Python (`pyproject.toml` / `requirements.txt`)

```bash
uv run python -m py_compile **/*.py   # BUILD — syntax check
uv run ruff check .                   # LINT  — or flake8, pylint
uv run pytest                         # TEST
```

#### Rust (`Cargo.toml`)

```bash
cargo build              # BUILD
cargo clippy             # LINT
cargo test               # TEST
```

#### Java — Maven (`pom.xml`)

```bash
mvn compile              # BUILD
mvn checkstyle:check     # LINT
mvn test                 # TEST
```

---

## Research First

**Before implementing anything new:**

- Search the ecosystem (pkg.go.dev, npmjs.com, pypi.org, crates.io) for existing libraries
- Check the project manifest (`go.mod`, `package.json`, `pyproject.toml`, etc.) for already-available dependencies
- Check shared utility directories (`internal/`, `lib/`, `src/utils/`) before duplicating logic
- Prefer well-maintained packages over one-off custom code

## Philosophy

**CRITICAL: Fix ALL errors with every commit, regardless of when introduced.**

- Errors compound over time
- Pre-existing errors are YOUR responsibility when touching related code
- Clean as you go — leave code better than you found it
- Every commit should reduce technical debt, not accumulate it

## Quality Gates and Deployment

Deployment scripts and CI pipelines block on failing quality gates. This is intentional — maintain quality gates regardless of time pressure.

Typical blockers:
- Build errors (type checking + compilation failures)
- Lint warnings (static analysis findings)
- Test failures

## Common Fix Patterns

### Type / Compile Errors

Narrow the type, remove the ambiguity:

```go
// Go: tighten interface{} to concrete type
func GetUser(id string) *User { ... }
```

```typescript
// TypeScript: add explicit type annotation
function getUser(id: string): User { ... }
```

### Lint / Static Analysis Warnings

Remove unused imports, fix shadowed variables, resolve flagged patterns. Most linters print the file and line — fix each location directly.

```bash
# Go: gofmt fixes formatting automatically
gofmt -w .

# JS/TS: many linters have --fix
eslint --fix .

# Python: ruff can auto-fix
uv run ruff check --fix .
```

### Test Failures

1. Read the failure output — identify which assertion failed and why
2. Determine whether the code or the test expectation is wrong
3. Fix the root cause; do not delete or skip the failing test

## Integration with wipnote

Track quality improvements in active work items (features, bugs) using `wipnote feature edit <id>` or `wipnote bug edit <id>`.

---

**Remember:** Fixing errors immediately is faster than letting them accumulate.
