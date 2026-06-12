---
name: test-extract-archive-flake
kind: hazard
paths:
    - cmd/wipnote/**
    - internal/**
verified_at: f0f34febadd9ebbf29ab8105679df87d7dc40110
links: []
created_by: claude-code/feat-c96e069b
created_at: 2026-06-11T09:29:34.431372902Z
updated_at: 2026-06-11T09:29:34.431372902Z
---

TestExtractArchive_BinaryAndPlugin is a known flaky test. If it fails in a full test run, re-run it in isolation (`go test ./... -run TestExtractArchive_BinaryAndPlugin`) before treating it as a real failure. Do not block a commit solely on this test failing in a bulk run.
