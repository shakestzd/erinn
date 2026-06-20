---
name: learning-feat-63676de5
kind: decision
paths:
    - .wipnote/features/feat-63676de5.html
verified_at: 0782e358e283292b8ca2105cd347b5a0355cf9c7
links:
    - feat-63676de5
created_by: wipnote-completion
created_at: 2026-06-18T09:39:35.598899799Z
updated_at: 2026-06-18T09:39:35.598899799Z
---

Recap render endpoint reads the committed .wipnote/recaps/<id>.html artifact directly (unlike plans which rebuild from YAML), then scopes its CSS via the existing scopePlanCSS helper reused verbatim — no new scoping logic needed.
