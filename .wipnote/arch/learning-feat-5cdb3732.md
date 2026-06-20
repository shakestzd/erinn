---
name: learning-feat-5cdb3732
kind: subsystem-map
paths:
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/cmd/wipnote/lineage_busy_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/recap/shared_walk_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/lineage/walk_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/recap/collect_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/recap/collect.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/recap/git.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/recap/types.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/cmd/wipnote/plan_blocks_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/cmd/wipnote/plan_cmds.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/cmd/wipnote/plan_blocks.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/cmd/wipnote/lineage.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/plan/planyaml/blocks_test.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a94bcb784e1480b01/internal/lineage/walk.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/plan/planyaml/validate.go
    - .claude/worktrees/visual-planning-recap-native-trk-a951e3c0/.claude/worktrees/agent-a787967cab311572c/plan/planyaml/schema.go
verified_at: 75427c366980eb731de2cc894ee756c1ce304058
links:
    - feat-5cdb3732
created_by: wipnote-completion
created_at: 2026-06-18T08:25:58.727671779Z
updated_at: 2026-06-18T08:25:58.727671779Z
---

The bidirectional lineage BFS walk lives ONLY in internal/lineage (BFSWalk/ForwardWalk/BackwardWalk/AnnotateTimestamps, takes only *sql.DB). cmd/wipnote/lineage.go aliases lineageNode = lineage.Node and delegates; internal/recap.Collect consumes it. Do not reintroduce a private BFS copy — TestLineageWalkIsShared in internal/recap guards this.
