---
name: learning-feat-4accdafe
kind: decision
paths:
    - cmd/wipnote/sqlite_write_boundary_test.go
    - plugin/skills/plan/SKILL.md
    - plan/planyaml/validate_test.go
    - cmd/wipnote/plan_yaml_extras.go
    - cmd/wipnote/plan_validate.go
    - plan/planyaml/validate.go
verified_at: 94a5863df1ebc54ae54f041757c7300cc6d4a03c
links:
    - feat-4accdafe
created_by: wipnote-completion
created_at: 2026-06-20T21:39:21.624062078Z
updated_at: 2026-06-20T21:39:21.624062078Z
---

ValidateBlockAdvisories mirrors ValidateResearchAdvisories ([]string return, same two callers: validatePlanFromYAML + runValidateYAML); gates on effectiveComplexity(s) excluding trivial. Inserting code into plan_yaml_extras.go shifts dbpkg.Open call sites and breaks TestWritableDBOpenBoundary's pinned Line: numbers in sqlite_write_boundary_test.go approvedWriteSites — update those Line fields when editing that file.
