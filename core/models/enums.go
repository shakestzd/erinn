// Package models defines the core data structures for wipnote.
//
// These types mirror the Python Pydantic models in models.py and event_log.py,
// ensuring JSON-compatible serialization so both runtimes can read/write the
// same .wipnote/ files and SQLite databases.
package models

import "strings"

// RelationshipType enumerates typed relationships between graph nodes.
type RelationshipType string

const (
	RelBlocks        RelationshipType = "blocks"
	RelBlockedBy     RelationshipType = "blocked_by"
	RelRelatesTo     RelationshipType = "relates_to"
	RelImplements    RelationshipType = "implements"
	RelCausedBy      RelationshipType = "caused_by"
	RelSpawnedFrom   RelationshipType = "spawned_from"
	RelImplementedIn RelationshipType = "implemented_in"
	RelPartOf        RelationshipType = "part_of"
	RelContains      RelationshipType = "contains"
	RelPlannedIn     RelationshipType = "planned_in"
)

// ValidRelationshipTypes lists all known relationship types.
var ValidRelationshipTypes = []RelationshipType{
	RelBlocks,
	RelBlockedBy,
	RelRelatesTo,
	RelImplements,
	RelCausedBy,
	RelSpawnedFrom,
	RelImplementedIn,
	RelPartOf,
	RelContains,
	RelPlannedIn,
}

// relationshipAliases maps convenience short-forms to canonical relationship types.
// This allows users to type e.g. "child" instead of "contains" in CLI commands.
var relationshipAliases = map[string]RelationshipType{
	"child":   RelContains,
	"parent":  RelPartOf,
	"dep":     RelBlockedBy,
	"depends": RelBlockedBy,
}

// NormalizeRelationship converts a raw relationship string to its canonical
// underscore form, replacing hyphens with underscores and lowercasing.
// It also resolves convenience aliases (e.g. "child" → "contains",
// "parent" → "part_of").
func NormalizeRelationship(s string) RelationshipType {
	normalized := RelationshipType(strings.ToLower(strings.ReplaceAll(s, "-", "_")))
	if alias, ok := relationshipAliases[string(normalized)]; ok {
		return alias
	}
	return normalized
}

// IsValidRelationship reports whether r is a known RelationshipType.
func IsValidRelationship(r RelationshipType) bool {
	for _, v := range ValidRelationshipTypes {
		if r == v {
			return true
		}
	}
	return false
}

// WorkType classifies work/activity type for events and sessions.
type WorkType string

const (
	WorkFeature       WorkType = "feature-implementation"
	WorkSpike         WorkType = "spike-investigation"
	WorkBugFix        WorkType = "bug-fix"
	WorkMaintenance   WorkType = "maintenance"
	WorkDocumentation WorkType = "documentation"
	WorkPlanning      WorkType = "planning"
	WorkReview        WorkType = "review"
	WorkAdmin         WorkType = "admin"
)

// SpikeType categorises spike investigations.
type SpikeType string

const (
	SpikeTechnical     SpikeType = "technical"
	SpikeArchitectural SpikeType = "architectural"
	SpikeRisk          SpikeType = "risk"
	SpikeGeneral       SpikeType = "general"
)

// MaintenanceType categorises software maintenance per IEEE standards.
type MaintenanceType string

const (
	MaintCorrective MaintenanceType = "corrective"
	MaintAdaptive   MaintenanceType = "adaptive"
	MaintPerfective MaintenanceType = "perfective"
	MaintPreventive MaintenanceType = "preventive"
)

// NodeStatus represents the lifecycle state of a work item.
type NodeStatus string

const (
	StatusTodo       NodeStatus = "todo"
	StatusInProgress NodeStatus = "in-progress"
	StatusBlocked    NodeStatus = "blocked"
	StatusDone       NodeStatus = "done"
	StatusActive     NodeStatus = "active"
	StatusEnded      NodeStatus = "ended"
	StatusStale      NodeStatus = "stale"
)

// Priority represents the priority level of a work item.
type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

// EventType enumerates agent event types stored in SQLite.
type EventType string

const (
	EventToolCall       EventType = "tool_call"
	EventToolResult     EventType = "tool_result"
	EventError          EventType = "error"
	EventDelegation     EventType = "delegation"
	EventCompletion     EventType = "completion"
	EventStart          EventType = "start"
	EventEnd            EventType = "end"
	EventCheckPoint     EventType = "check_point"
	EventTaskDelegation EventType = "task_delegation"
	EventTeammateIdle   EventType = "teammate_idle"
	EventTaskCreated    EventType = "task_created"
	EventTaskCompleted  EventType = "task_completed"
	EventQualityGate    EventType = "quality_gate"
)

// Claim lifecycle event types.
const (
	EventClaimProposed  EventType = "claim.proposed"
	EventClaimClaimed   EventType = "claim.claimed"
	EventClaimHeartbeat EventType = "claim.heartbeat"
	EventClaimBlocked   EventType = "claim.blocked"
	EventClaimCompleted EventType = "claim.completed"
	EventClaimAbandoned EventType = "claim.abandoned"
	EventClaimExpired   EventType = "claim.expired"
	EventClaimHandoff   EventType = "claim.handoff"
)
