---
name: learning-bug-17a49c83
kind: decision
paths:
    - core/workitem/plan_patch_test.go
    - core/hooks/session_html_test.go
    - core/workitem/plans.go
    - core/hooks/session_html.go
verified_at: cb7a1a5e2dfd76c6ab46355cefde9c7a257276a6
links:
    - bug-17a49c83
created_by: wipnote-completion
created_at: 2026-06-15T21:33:53.585915913Z
updated_at: 2026-06-15T21:33:53.585915913Z
---

TestConcurrentStatusMutationsKeepSQLiteDerivedStatusInOrder in core/workitem deadlocks when wipnote daemon is not running — it calls daemon.WriterClient.SubmitOrSpawn which blocks indefinitely. Pre-existing issue unrelated to plan/session guard changes. Skip with -run flag or ensure daemon is active before running full workitem suite.
