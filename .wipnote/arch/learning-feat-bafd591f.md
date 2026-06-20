---
name: learning-feat-bafd591f
kind: decision
paths:
    - .wipnote/features/feat-bafd591f.html
verified_at: 51e20054c5a019c3c840ae0ce154e2d76b48a8a9
links:
    - feat-bafd591f
created_by: wipnote-completion
created_at: 2026-06-18T08:56:48.589469462Z
updated_at: 2026-06-18T08:56:48.589469462Z
---

Wireframe block renderer lives in the SHARED plan/blocks leaf package (wireframe.go) so plan slice blocks and recap before/after panels render through ONE code path; it rejects raw hex/rgb/hsl (regex identical to plan/planyaml validate.go) and exposes a scoped --wf-* token set aliasing dashboard :root tokens. Plan blocks-zone stamps every block element with a slice-<num>-block-<type>-<idx> anchor (per-type 1-based index) to satisfy the slice-8 annotation-dropdown contract slice-\d+-block-[a-z0-9-]+-\d+.
