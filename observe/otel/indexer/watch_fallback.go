// Package indexer — polling watcher (all platforms).
//
// This file provides the polling-based file-change detection for all platforms.
// No build tags are used — polling is the sole strategy (no fsnotify dependency).
// A 500ms poll interval provides sub-second latency for dashboard updates without
// requiring a new dependency (fsnotify) in go.mod.
package indexer

// No additional symbols needed — polling logic lives in indexer.go (Start/runOnce).
// This file exists to document the deliberate polling-only decision and serve as
// the extension point if fsnotify is added in a future iteration.
