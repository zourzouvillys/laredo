package laredo

import (
	"context"
	"fmt"
	"time"
)

// ReplaySpeed controls the rate at which snapshot rows are replayed.
type ReplaySpeed int

// Replay speed options.
const (
	ReplayFullSpeed ReplaySpeed = iota // Replay as fast as possible.
	ReplayRealTime                     // Replay at approximately real-time pace.
)

// SnapshotReplay replays a stored snapshot into a set of targets. This is useful
// for testing, debugging, populating new targets from historical data, or
// validating pipeline behavior against known data.
type SnapshotReplay struct {
	store   SnapshotStore
	targets map[TableIdentifier][]SyncTarget
	speed   ReplaySpeed
}

// NewSnapshotReplay creates a replay builder.
func NewSnapshotReplay(store SnapshotStore) *SnapshotReplay {
	return &SnapshotReplay{
		store:   store,
		targets: make(map[TableIdentifier][]SyncTarget),
		speed:   ReplayFullSpeed,
	}
}

// Target adds a target for a specific table. Multiple targets per table are supported.
func (r *SnapshotReplay) Target(table TableIdentifier, target SyncTarget) *SnapshotReplay {
	r.targets[table] = append(r.targets[table], target)
	return r
}

// Speed sets the replay speed.
func (r *SnapshotReplay) Speed(speed ReplaySpeed) *SnapshotReplay {
	r.speed = speed
	return r
}

// ReplayResult contains statistics from a completed replay.
type ReplayResult struct {
	SnapshotID string
	Tables     int
	TotalRows  int64
	Duration   time.Duration
}

// Run loads the specified snapshot and replays it into the configured targets.
// Blocks until the replay is complete or the context is cancelled.
func (r *SnapshotReplay) Run(ctx context.Context, snapshotID string) (ReplayResult, error) {
	start := time.Now()

	metadata, entries, err := r.store.Load(ctx, snapshotID)
	if err != nil {
		return ReplayResult{}, fmt.Errorf("replay: load snapshot %s: %w", snapshotID, err)
	}

	// Build column schema map from metadata.
	columnsByTable := make(map[TableIdentifier][]ColumnDefinition, len(metadata.Tables))
	for _, info := range metadata.Tables {
		columnsByTable[info.Table] = info.Columns
	}

	var totalRows int64
	tablesReplayed := 0

	for table, tableEntries := range entries {
		targets := r.targets[table]
		if len(targets) == 0 {
			continue
		}

		columns := columnsByTable[table]
		tablesReplayed++

		// Init targets.
		for _, target := range targets {
			if err := target.OnInit(ctx, table, columns); err != nil {
				return ReplayResult{}, fmt.Errorf("replay: init target for %s: %w", table, err)
			}
		}

		// Replay rows.
		for _, entry := range tableEntries {
			if ctx.Err() != nil {
				return ReplayResult{}, ctx.Err()
			}

			for _, target := range targets {
				if err := target.OnBaselineRow(ctx, table, entry.Row); err != nil {
					return ReplayResult{}, fmt.Errorf("replay: row for %s: %w", table, err)
				}
			}
			totalRows++
		}

		// Complete baseline.
		for _, target := range targets {
			if err := target.OnBaselineComplete(ctx, table); err != nil {
				return ReplayResult{}, fmt.Errorf("replay: complete for %s: %w", table, err)
			}
		}
	}

	return ReplayResult{
		SnapshotID: snapshotID,
		Tables:     tablesReplayed,
		TotalRows:  totalRows,
		Duration:   time.Since(start),
	}, nil
}

// Start runs the replay asynchronously. Returns a channel that receives
// the result when the replay completes.
func (r *SnapshotReplay) Start(ctx context.Context, snapshotID string) <-chan ReplayResult {
	ch := make(chan ReplayResult, 1)
	go func() {
		result, err := r.Run(ctx, snapshotID)
		if err != nil {
			// On error, return a zero result with just the snapshot ID.
			ch <- ReplayResult{SnapshotID: snapshotID}
		} else {
			ch <- result
		}
		close(ch)
	}()
	return ch
}
