---
name: serve-writequeue-hazard
kind: hazard
paths:
    - cmd/wipnote/serve_child.go
    - cmd/wipnote/serve_global.go
verified_at: e56c49a13549da970df6ea78c4dd4453b1ece6e9
links: []
created_by: feat-359312ab
created_at: 2026-06-10T22:08:06.35492617Z
updated_at: 2026-06-10T22:08:06.35492617Z
---

Dashboard read pool is capped (dashboardReadPoolMaxConns) to prevent connection bursts from holding too many SHARED locks that starve the single SQLite writer under DELETE journal mode (bug-74a7bda7). Never remove the pool cap or allow unbounded concurrent readers. The writer uses a writequeue.Queue to serialize writes; bypassing it causes SQLITE_BUSY at runtime.
