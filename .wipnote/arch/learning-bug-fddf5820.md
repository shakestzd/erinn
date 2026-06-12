---
name: learning-bug-fddf5820
kind: decision
paths:
    - cmd/wipnote/check_gate_test.go
    - cmd/wipnote/plan_rewrite_track_test.go
    - cmd/wipnote/plan_yaml_extras.go
    - cmd/wipnote/sync_test.go
    - cmd/wipnote/arch_cmds.go
    - cmd/wipnote/sync.go
    - cmd/wipnote/serve_child_plan_write_test.go
    - cmd/wipnote/ingest_events_test.go
    - plan/plantmpl/templates/plan_page.gohtml
    - core/arch/card_test.go
    - core/arch/card.go
    - core/daemon/lifecycle_test.go
    - core/daemon/daemon_test.go
    - core/daemon/lease_linux.go
    - cmd/wipnote/reindex.go
    - core/db/plan_feedback_test.go
    - core/db/plan_feedback.go
    - core/db/feature_repo.go
    - cmd/wipnote/check_gate_support.go
    - core/workitem/project.go
    - core/workitem/htmlwriter_edgehref_test.go
    - core/workitem/htmlwriter.go
    - core/workitem/templates/node.gohtml
verified_at: 45f92ef73a060a2ed050c7bbe9abb05146b70691
links:
    - bug-fddf5820
created_by: wipnote-completion
created_at: 2026-06-11T13:09:47.053533596Z
updated_at: 2026-06-11T13:09:47.053533596Z
---

Renderer fix is the deliverable for ~24 broken artifacts; do NOT hand-repair existing .wipnote HTML. Corrected hrefs/escaping apply on next WriteNodeHTML (any mutation) or via reindex re-parse; no mass rewrite was run per spec.
