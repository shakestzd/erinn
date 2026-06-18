---
name: learning-feat-96b98270
kind: invariant
paths:
    - .wipnote/features/feat-96b98270.html
verified_at: cb823f2291b261522103ef9a21ead8ae602da66e
links:
    - feat-96b98270
created_by: wipnote-completion
created_at: 2026-06-18T08:06:37.832774345Z
updated_at: 2026-06-18T08:06:37.832774345Z
---

PlanSlice.Blocks is additive-optional (yaml:blocks,omitempty): no schema_version bump needed because planyaml.Validate only enumerates meta enums, never the slice field set. Block shapes validate ONLY when present, against planyaml.BlockCatalog() — the single source of truth shared by validate.go and 'wipnote plan blocks'. Do NOT hardcode block tags in renderers; read BlockCatalog. Vocabulary: data-model(rows name/type), api-endpoint(method/path), file-tree(entries), wireframe(html, raw hex/rgb/hsl rejected — design tokens only).
