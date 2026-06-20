---
name: learning-bug-1af3c3d9
kind: decision
paths:
    - cmd/wipnote/plugin_ensure_test.go
    - observe/otel/collector/lifecycle.go
    - cmd/wipnote/claude.go
verified_at: 6058e47a211b06a2ea82d4dc3db081ae3bf19ddd
links:
    - bug-1af3c3d9
created_by: wipnote-completion
created_at: 2026-06-20T18:15:53.335062339Z
updated_at: 2026-06-20T18:15:53.335062339Z
---

wipnote claude --dev launch latency: removeMarketplaceWipnote ran ~8 'claude plugin' subprocess calls (~4s) every launch even when nothing installed — gate it behind marketplaceWipnotePresent(); collector spawn retries default to maxAttempts to bound launch wait (3->1); the post-banner gap had zero feedback. Cross-module gotcha: changing observe/otel/collector/lifecycle.go RetrySpawn count also breaks lifecycle_test.go in the observe module, which root go test ./... does not cover.
