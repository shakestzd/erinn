---
name: learning-bug-81ea68a6
kind: decision
paths:
    - plan/plantmpl/plantmpl_test.go
    - plan/plantmpl/templates/plan_page.gohtml
    - plan/plantmpl/slice_card_test.go
    - plan/plantmpl/plantmpl.go
    - plan/plantmpl/slice_card.go
verified_at: 2c50d26acad2d51a51961364da71a7da85388b72
links:
    - bug-81ea68a6
created_by: wipnote-completion
created_at: 2026-06-17T11:43:58.314469257Z
updated_at: 2026-06-17T11:43:58.314469257Z
---

Plan HTML renderer lives in plan/plantmpl (its OWN Go module — repo has 5 modules: root, core, port, plan, observe). Plan text fields must be HTML-escaped at render; issue badges count only DANGER/WARN (not SUCCESS/INFO); critique kinds need badge-warn/success/danger/info CSS.
