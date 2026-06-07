package planyaml

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// planWriteMu serialises in-process Save calls for the same plan path.
// Cross-process safety still requires an advisory file lock at higher layers.
var planWriteMu sync.Map

// planAtomicSeq makes temp filenames unique across goroutines in the same PID.
var planAtomicSeq atomic.Int64

// NewPlan creates a PlanYAML with sensible defaults: status "draft",
// empty design/slices/questions, nil critique, and CreatedAt set to today.
func NewPlan(id, title, description string) *PlanYAML {
	return &PlanYAML{
		Meta: PlanMeta{
			ID:            id,
			Title:         title,
			Description:   description,
			CreatedAt:     time.Now().UTC().Format("2006-01-02"),
			Status:        "draft",
			Priority:      "medium",
			SchemaVersion: "v3",
			Version:       1,
		},
		Design:    PlanDesign{},
		Slices:    []PlanSlice{},
		Questions: []PlanQuestion{},
		Critique:  nil,
	}
}

// Load reads a YAML plan file from disk and unmarshals it.
func Load(path string) (*PlanYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan YAML: %w", err)
	}
	return LoadBytes(data)
}

// LoadBytes unmarshals a PlanYAML from raw YAML bytes.
func LoadBytes(data []byte) (*PlanYAML, error) {
	var plan PlanYAML
	if err := yaml.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("unmarshal plan YAML: %w", err)
	}
	return &plan, nil
}

// Save marshals the plan to YAML and writes it to the given path atomically.
// The write goes through a per-path mutex (so concurrent goroutines don't
// race) and then a temp-file + rename (so a crash mid-write can't leave a
// half-written file). Plan.Meta.Version auto-increments every save so every
// mutation is tracked as a distinct revision.
//
// In-process locking only — separate `wipnote` CLI processes editing the
// same plan still need an advisory file lock at the call site (see
// LockPlanForWrite).
func Save(path string, plan *PlanYAML) error {
	defer LockPlanForWrite(path)()
	return saveLocked(path, plan)
}

// SaveLocked performs the marshal + atomic write WITHOUT acquiring the
// per-plan mutex. The caller MUST already hold LockPlanForWrite(path) (or
// otherwise guarantee single-writer semantics). Use this when extending the
// load → modify → save window so the lock isn't released between steps.
func SaveLocked(path string, plan *PlanYAML) error {
	return saveLocked(path, plan)
}

// saveLocked performs the marshal + atomic write. The caller MUST hold
// LockPlanForWrite(path) (or otherwise guarantee single-writer semantics).
func saveLocked(path string, plan *PlanYAML) error {
	plan.Meta.Version++

	data, err := yaml.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan YAML: %w", err)
	}

	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create plan dir: %w", err)
		}
	}

	seq := planAtomicSeq.Add(1)
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), seq)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp plan: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp plan: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync temp plan: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp plan: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename plan: %w", err)
	}
	return nil
}

// LockPlanForWrite acquires a per-plan in-process mutex so a load → modify →
// save window is atomic against other goroutines doing the same. Callers
// MUST defer the returned release function.
//
// Cross-process safety is NOT provided here; layer an advisory file lock on
// top when multiple `wipnote` invocations may edit the same plan.
func LockPlanForWrite(path string) (release func()) {
	muVal, _ := planWriteMu.LoadOrStore(path, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
