package snapshotter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zourzouvillys/laredo"
)

// WriteBaseSnapshot writes a one-shot base-snapshot archive: a single snapshot
// artifact (encoded in each format, to every destination) plus a fresh manifest
// at epoch 1 that records the table schema. It is the producer behind
// `laredo archive export` — a point-in-time export that any consumer
// (source/archive, cold-tier replay) can replay or reconstruct from.
//
// It is intentionally not the Writer: the Writer maintains a live archive with
// incremental diffs, re-basing, and compare-and-swap commits, whereas this writes
// a self-contained snapshot in one shot with no prior manifest to reconcile.
// Re-running it overwrites the manifest, which a follower reads as a wholesale
// replacement (a fresh base its old position no longer continues) and re-baselines.
//
// now is supplied by the caller so the manifest timestamp is controllable (and
// tests deterministic).
func WriteBaseSnapshot(
	ctx context.Context,
	dests []Destination,
	keyPrefix string,
	formats []Format,
	table string,
	position string,
	rows []laredo.Row,
	columns []laredo.ColumnDefinition,
	now time.Time,
) error {
	if len(dests) == 0 {
		return fmt.Errorf("snapshotter: WriteBaseSnapshot requires at least one destination")
	}
	if len(formats) == 0 {
		return fmt.Errorf("snapshotter: WriteBaseSnapshot requires at least one format")
	}

	art := Artifact{
		Kind:       KindSnapshot,
		Epoch:      1,
		ToPosition: position,
		CreatedAt:  now,
		RowCount:   int64(len(rows)),
		Formats:    make(map[string]FormatRef, len(formats)),
	}
	for _, f := range formats {
		var buf bytes.Buffer
		if err := f.WriteSnapshot(&buf, rows); err != nil {
			return fmt.Errorf("snapshotter: encode snapshot (%s): %w", f.FormatID(), err)
		}
		payload := buf.Bytes()
		key := ArtifactObjectKey(keyPrefix, art, f.Extension())
		var ref FormatRef
		for _, dest := range dests {
			uri, size, err := dest.Put(ctx, key, bytes.NewReader(payload))
			if err != nil {
				return fmt.Errorf("snapshotter: put snapshot %s: %w", key, err)
			}
			ref = FormatRef{URI: uri, SizeBytes: size}
		}
		art.Formats[f.FormatID()] = ref
	}

	m := Manifest{
		ManifestVersion: ManifestVersion,
		Table:           table,
		Epoch:           1,
		UpdatedAt:       now,
		HeadPosition:    position,
		Artifacts:       []Artifact{art},
		Columns:         columns,
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("snapshotter: marshal manifest: %w", err)
	}
	for _, dest := range dests {
		if _, _, err := dest.Put(ctx, ManifestObjectKey(keyPrefix), bytes.NewReader(data)); err != nil {
			return fmt.Errorf("snapshotter: put manifest: %w", err)
		}
	}
	return nil
}
