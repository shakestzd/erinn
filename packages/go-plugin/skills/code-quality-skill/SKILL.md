---
name: code-quality
description: Code hygiene, quality gates, and pre-commit workflows. Use for linting, type checking, testing, and fixing errors.
---

# Code Quality Skill

Use this skill for code hygiene, quality gates, and pre-commit workflows.

**Trigger keywords:** code quality, lint, go vet, type checking, go test, pre-commit, build, fix errors

## Work Item Attribution

Quality gate runs should be attributed. Before fixing errors:
1. Ensure a feature or bug is active: `htmlgraph status`
2. If fixing a bug: `htmlgraph bug create "Fix: description"` then `htmlgraph bug start <id>`
3. Run `htmlgraph help` for available commands

---

## Quick Workflow

```bash
# Before EVERY commit:
(cd packages/go && go build ./...)           # Type checking + compile
(cd packages/go && go vet ./...)             # Linting
(cd packages/go && go test ./...)            # Run tests

# Only commit when ALL checks pass
git commit -m "..."
```

## Research First

**Before implementing anything new:**

- Search Go ecosystem (pkg.go.dev, etc.) for existing libraries before writing custom implementations
- Check `go.mod` for what is already available as a dependency
- Check `packages/go/internal/` for shared utilities before duplicating logic
- Prefer well-maintained packages over one-off custom code

## Philosophy

**CRITICAL: Fix ALL errors with every commit, regardless of when introduced.**

- Errors compound over time
- Pre-existing errors are YOUR responsibility when touching related code
- Clean as you go - leave code better than you found it
- Every commit should reduce technical debt, not accumulate it

## Quality Gates

The deployment script (`deploy-all.sh`) blocks on:
- Go build errors (type checking + compilation)
- Go vet warnings (linting)
- Test failures

This is intentional - maintain quality gates.

## Tools Reference

### Go Build (Type Checking + Compilation)

```bash
# Build all packages
(cd packages/go && go build ./...)

# Build specific package
(cd packages/go && go build ./cmd/htmlgraph)

# Verbose output
(cd packages/go && go build -v ./...)
```

### Go Vet (Linting)

```bash
# Check all packages
(cd packages/go && go vet ./...)

# Check specific package
(cd packages/go && go vet ./cmd/htmlgraph)

# Verbose output
(cd packages/go && go vet -v ./...)
```

### Go Test (Testing)

```bash
# Run all tests
(cd packages/go && go test ./...)

# Verbose output
(cd packages/go && go test -v ./...)

# Run specific test
(cd packages/go && go test -run TestName ./...)

# Run with coverage
(cd packages/go && go test -cover ./...)
```

## Common Fix Patterns

### Type Errors (Build Errors)

```go
// Before (type error)
func GetUser(id interface{}) *User {
    return db.Query(id)
}

// After (typed)
func GetUser(id string) *User {
    return db.Query(id)
}
```

### Lint Errors (Vet Warnings)

```go
// Before (unused import)
import (
    "os"
    "sys"
)
var x = 1

// After (clean)
var x = 1
```

### Format Issues

```bash
# Go automatically formats with gofmt
# Most editors auto-format on save
(cd packages/go && gofmt -w .)
```

## Integration with HtmlGraph

Track quality improvements:

```bash
# Create a spike to document fixes
htmlgraph spike create "Fix go vet errors in models.go"
# Then add findings via htmlgraph spike edit <id>
```

---

**Remember:** Fixing errors immediately is faster than letting them accumulate.
