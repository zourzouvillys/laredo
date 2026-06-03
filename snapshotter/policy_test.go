package snapshotter

import (
	"testing"
	"time"
)

func TestPolicy_ShouldSnapshot(t *testing.T) {
	p := Policy{
		DiffInterval:     30 * time.Second,
		MinInterval:      5 * time.Minute,
		MaxInterval:      6 * time.Hour,
		MaxDiffBytes:     1000,
		MaxDiffFraction:  0.25,
		MaxChurnRecords:  100,
		MaxChurnFraction: 0.5,
	}

	tests := []struct {
		name    string
		st      triggerState
		want    bool
		trigger string
	}{
		{
			name: "within min interval never snapshots",
			st:   triggerState{sinceSnapshot: time.Minute, diffBytes: 1 << 20, churnRecords: 1e6, datasetSize: 10, lastSnapshotBytes: 10},
			want: false,
		},
		{
			name:    "max interval",
			st:      triggerState{sinceSnapshot: 7 * time.Hour, diffBytes: 1, churnRecords: 1},
			want:    true,
			trigger: "max_interval",
		},
		{
			name:    "diff bytes absolute",
			st:      triggerState{sinceSnapshot: 10 * time.Minute, diffBytes: 1000, churnRecords: 1},
			want:    true,
			trigger: "max_diff_bytes",
		},
		{
			name:    "diff fraction of last snapshot",
			st:      triggerState{sinceSnapshot: 10 * time.Minute, diffBytes: 300, lastSnapshotBytes: 1000},
			want:    true,
			trigger: "max_diff_fraction",
		},
		{
			name:    "churn records",
			st:      triggerState{sinceSnapshot: 10 * time.Minute, diffBytes: 10, churnRecords: 100},
			want:    true,
			trigger: "max_churn_records",
		},
		{
			name:    "churn fraction of dataset",
			st:      triggerState{sinceSnapshot: 10 * time.Minute, diffBytes: 10, churnRecords: 60, datasetSize: 100},
			want:    true,
			trigger: "max_churn_fraction",
		},
		{
			name: "quiet table writes a diff",
			st:   triggerState{sinceSnapshot: 10 * time.Minute, diffBytes: 10, churnRecords: 1, datasetSize: 1000, lastSnapshotBytes: 1 << 20},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, trigger := p.shouldSnapshot(tc.st)
			if got != tc.want {
				t.Fatalf("shouldSnapshot = %v, want %v (trigger %q)", got, tc.want, trigger)
			}
			if got && trigger != tc.trigger {
				t.Fatalf("trigger = %q, want %q", trigger, tc.trigger)
			}
		})
	}
}

func TestPolicy_DisabledTriggers(t *testing.T) {
	// All thresholds zero = only ever write diffs.
	var p Policy
	got, _ := p.shouldSnapshot(triggerState{sinceSnapshot: 100 * time.Hour, diffBytes: 1 << 30, churnRecords: 1 << 30, datasetSize: 1})
	if got {
		t.Fatal("with all triggers disabled, should never snapshot")
	}
}
