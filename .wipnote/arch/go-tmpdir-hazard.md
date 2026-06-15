---
name: go-tmpdir-hazard
kind: hazard
paths:
    - '**/*.go'
    - go.mod
    - go.sum
verified_at: eababf24b1e0a4f249ce63762026d0e9f372a6f2
links: []
created_by: claude-code/feat-c96e069b
created_at: 2026-06-11T09:29:23.098018105Z
updated_at: 2026-06-12T23:49:15.256928792Z
---

Before any `go build`, `go vet`, or `go test` in this repo, run: `export TMPDIR=$HOME/.gotest-tmp GOTMPDIR=$HOME/.gotest-tmp/wipnote-build && mkdir -p "$TMPDIR" "$GOTMPDIR"`. Lease tests (TestLease_AcquireAndHold, TestSingleOwnerLeaseRace, TestStaleSocketUnlinkRefusedWhenDifferentOwnerAlive) require a temp path containing "wipnote" outside a git tree; default /tmp false-fails them. Re-export in every shell call — state does not persist between calls.
