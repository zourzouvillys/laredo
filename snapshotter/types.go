// Package snapshotter materializes a laredo fan-out table into durable base
// snapshots plus a stream of diffs, indexed by a manifest, on pluggable
// destinations in pluggable formats. See docs/edr/0001-snapshot-writer.md.
package snapshotter

import (
	"time"

	"github.com/zourzouvillys/laredo"
)

// ArtifactKind distinguishes a full base snapshot from an incremental diff.
type ArtifactKind string

// Artifact kinds.
const (
	KindSnapshot ArtifactKind = "snapshot"
	KindDiff     ArtifactKind = "diff"
)

// Change is a single row-level change recorded in a diff. It is keyed by the
// row's primary key so repeated edits to the same row within a flush window
// collapse to the net change. A delete carries Old (the identity); an insert
// carries New; an update carries both.
type Change struct {
	Action laredo.ChangeAction `json:"action"`
	Key    string              `json:"key"`
	Old    laredo.Row          `json:"old,omitempty"`
	New    laredo.Row          `json:"new,omitempty"`
}

// FormatRef points at one encoded copy of an artifact (one format → one object).
type FormatRef struct {
	URI       string `json:"uri"`
	SizeBytes int64  `json:"size_bytes"`
}

// Artifact is one durable snapshot or diff recorded in the manifest. Each
// artifact may exist in several formats (JSONL, protobuf, …), one object each.
type Artifact struct {
	Kind         ArtifactKind         `json:"kind"`
	Epoch        int64                `json:"epoch"`
	FromPosition *string              `json:"from_position"` // nil for a base snapshot
	ToPosition   string               `json:"to_position"`
	CreatedAt    time.Time            `json:"created_at"`
	RowCount     int64                `json:"row_count,omitempty"`    // snapshots
	ChangeCount  int64                `json:"change_count,omitempty"` // diffs
	Formats      map[string]FormatRef `json:"formats"`
}

// Manifest is the per-table index that consumers read to discover the latest
// state and the chain of artifacts needed to reconstruct it. It is the commit
// point and the compatibility contract; writes are compare-and-swap.
type Manifest struct {
	ManifestVersion int        `json:"manifest_version"`
	Table           string     `json:"table"`
	Epoch           int64      `json:"epoch"`
	UpdatedAt       time.Time  `json:"updated_at"`
	HeadPosition    string     `json:"head_position"`
	Artifacts       []Artifact `json:"artifacts"`
}

// ManifestVersion is the current manifest schema version.
const ManifestVersion = 1

// ArtifactEvent is the advisory, at-least-once notification published to event
// sinks after the manifest head advances.
type ArtifactEvent struct {
	Table        string            `json:"table"`
	Kind         ArtifactKind      `json:"kind"`
	Epoch        int64             `json:"epoch"`
	FromPosition *string           `json:"from_position"`
	ToPosition   string            `json:"to_position"`
	HeadPosition string            `json:"head_position"`
	ManifestURI  string            `json:"manifest_uri,omitempty"`
	Formats      map[string]string `json:"formats"` // format id → uri
	CreatedAt    time.Time         `json:"created_at"`
}

// latestSnapshot returns the newest snapshot artifact (highest epoch) in the
// manifest, or nil if there is none.
func (m *Manifest) latestSnapshot() *Artifact {
	var latest *Artifact
	for i := range m.Artifacts {
		a := &m.Artifacts[i]
		if a.Kind != KindSnapshot {
			continue
		}
		if latest == nil || a.Epoch > latest.Epoch {
			latest = a
		}
	}
	return latest
}

// prune drops artifacts from epochs older than keepEpochs generations before the
// current epoch. A value of 0 disables pruning. Artifacts in the current and the
// most recent keepEpochs-1 epochs are retained.
func (m *Manifest) prune(keepEpochs int) {
	if keepEpochs <= 0 {
		return
	}
	minEpoch := m.Epoch - int64(keepEpochs) + 1
	kept := m.Artifacts[:0]
	for _, a := range m.Artifacts {
		if a.Epoch >= minEpoch {
			kept = append(kept, a)
		}
	}
	m.Artifacts = kept
}
