---
name: learning-bug-252ae19a
kind: decision
paths:
    - core/hooks/session_html_test.go
    - core/hooks/session_html.go
verified_at: ded2e2504bfc3e000a61f4150f27d4a20a6e2a54
links:
    - bug-252ae19a
created_by: wipnote-completion
created_at: 2026-06-16T04:08:53.572805443Z
updated_at: 2026-06-16T04:08:53.572805443Z
---

Session HTML population is signaled by activity <li> entries, NOT data-event-count: AppendEventToSessionHTML adds entries without bumping the attribute (only FinalizeSessionHTML updates it). Any guard checking whether a session is populated must count activity entries, not trust data-event-count.
