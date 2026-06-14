---
name: learning-feat-fdcc46d8
kind: decision
paths:
    - AGENTS.md
    - core/storage/dbpath.go
    - core/storage/dbpath_test.go
verified_at: 5a692078055aeae2412742baf89cb28df1759bea
links:
    - feat-fdcc46d8
created_by: wipnote-completion
created_at: 2026-06-13T03:00:46.735710402Z
updated_at: 2026-06-13T03:00:46.735710402Z
---

core/ is a SEPARATE Go module (core/go.mod: github.com/shakestzd/wipnote/core). Root-level ./... does not include it — run go build/vet/test in /workspaces/wipnote/core separately for a complete quality gate.
