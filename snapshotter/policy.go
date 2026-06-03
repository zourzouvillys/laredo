package snapshotter

import "time"

// Policy configures when the writer flushes a diff versus re-basing with a fresh
// snapshot. Every threshold is independent; set a value to zero to disable that
// trigger. See docs/design/snapshot-writer.md for the decision diagram.
type Policy struct {
	// DiffInterval is how often buffered changes are flushed.
	DiffInterval time.Duration

	// MinInterval is the floor between snapshots: a re-base that would fire
	// sooner is deferred and a diff is written instead. Zero disables the floor.
	MinInterval time.Duration
	// MaxInterval forces a re-base once this long has passed since the last
	// snapshot, even on a quiet table. Zero disables the ceiling.
	MaxInterval time.Duration

	// MaxDiffBytes re-bases when the serialized diff would be at least this big.
	MaxDiffBytes int64
	// MaxDiffFraction re-bases when the serialized diff would be at least this
	// fraction of the last snapshot's byte size (e.g. 0.25 = 25%).
	MaxDiffFraction float64

	// MaxChurnRecords re-bases when this many change events have accumulated
	// since the last snapshot.
	MaxChurnRecords int64
	// MaxChurnFraction re-bases when changes since the last snapshot reach this
	// fraction of the current dataset row count (e.g. 0.5 = 50%).
	MaxChurnFraction float64
}

// triggerState is the live input to the snapshot-vs-diff decision.
type triggerState struct {
	sinceSnapshot     time.Duration
	diffBytes         int64
	churnRecords      int64
	datasetSize       int64
	lastSnapshotBytes int64
}

// shouldSnapshot reports whether to re-base (write a snapshot) instead of a diff,
// and names the trigger that fired. The MinInterval floor takes precedence over
// every other trigger except — by construction — nothing: while inside the
// floor, a diff is always written.
func (p Policy) shouldSnapshot(st triggerState) (bool, string) {
	if p.MinInterval > 0 && st.sinceSnapshot < p.MinInterval {
		return false, ""
	}
	switch {
	case p.MaxInterval > 0 && st.sinceSnapshot >= p.MaxInterval:
		return true, "max_interval"
	case p.MaxDiffBytes > 0 && st.diffBytes >= p.MaxDiffBytes:
		return true, "max_diff_bytes"
	case p.MaxDiffFraction > 0 && st.lastSnapshotBytes > 0 &&
		float64(st.diffBytes) >= p.MaxDiffFraction*float64(st.lastSnapshotBytes):
		return true, "max_diff_fraction"
	case p.MaxChurnRecords > 0 && st.churnRecords >= p.MaxChurnRecords:
		return true, "max_churn_records"
	case p.MaxChurnFraction > 0 && st.datasetSize > 0 &&
		float64(st.churnRecords) >= p.MaxChurnFraction*float64(st.datasetSize):
		return true, "max_churn_fraction"
	default:
		return false, ""
	}
}
