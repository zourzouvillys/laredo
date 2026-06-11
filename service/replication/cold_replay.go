package replication

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/target/fanout"
)

// coldReplay is a fully-materialized cold-tier replay: the base snapshot rows
// (when the plan starts from one) and the decoded diffs, all read and decoded
// up front so a mid-stream object-storage read can never force a fallback after
// the handshake. EDR-0002 requires falling back to a live snapshot *before* any
// data is sent; materializing here is what makes that guarantee hold.
type coldReplay struct {
	hasSnapshot  bool
	snapshotID   string
	snapshotRows []laredo.Row
	diffs        []coldDiff
	resumeSeq    int64 // hot-journal sequence to resume after, once this is applied
}

type coldDiff struct {
	position string // the diff's to_position; stamped on each change as its watermark
	changes  []snapshotter.Change
}

// planColdReplay attempts to materialize a cold-tier replay for a client at
// fromPos. It returns (replay, true) only when an archive is configured for the
// table, its manifest is readable, a plan exists, the plan's head bridges to the
// hot journal (so cold→hot is gapless), and every artifact decodes. Any failure
// logs and returns false so the caller falls back to a live snapshot before
// sending the handshake.
func (s *Service) planColdReplay(ctx context.Context, tid laredo.TableIdentifier, ft *fanout.Target, src laredo.SyncSource, fromPos string) (*coldReplay, bool) {
	r := s.archives[tid]
	if r == nil {
		return nil, false
	}
	m, err := r.LoadManifest(ctx)
	if err != nil {
		s.log.Warn("cold-tier replay unavailable: manifest", "table", tid.String(), "error", err)
		return nil, false
	}
	cmp := func(a, b string) int {
		pa, ea := src.PositionFromString(a)
		pb, eb := src.PositionFromString(b)
		if ea != nil || eb != nil {
			return 0
		}
		return src.ComparePositions(pa, pb)
	}
	plan, err := r.Plan(m, fromPos, cmp)
	if err != nil {
		s.log.Warn("cold-tier replay unavailable: plan", "table", tid.String(), "error", err)
		return nil, false
	}
	if plan == nil {
		return nil, false
	}
	headPos, err := src.PositionFromString(plan.HeadPosition)
	if err != nil {
		return nil, false
	}
	// The plan must bridge to the hot journal: its head must still be covered, or
	// there is a gap cold replay cannot fill (the snapshotter fell behind and the
	// journal pruned past the archive head). seq is the hot sequence to resume
	// after once the cold plan is applied.
	seq, covered := ft.ResumeSequenceForPosition(headPos, src.ComparePositions)
	if !covered {
		s.log.Warn("cold-tier replay declined: archive head predates the journal (gap)",
			"table", tid.String(), "archive_head", plan.HeadPosition)
		return nil, false
	}

	cr := &coldReplay{resumeSeq: seq}
	if plan.Snapshot != nil {
		rows, err := r.ReadSnapshot(ctx, *plan.Snapshot)
		if err != nil {
			s.log.Warn("cold-tier replay unavailable: read snapshot", "table", tid.String(), "error", err)
			return nil, false
		}
		cr.hasSnapshot = true
		cr.snapshotID = fmt.Sprintf("archive-%d", plan.Snapshot.Epoch)
		cr.snapshotRows = rows
	}
	for _, d := range plan.Diffs {
		changes, err := r.ReadDiff(ctx, d)
		if err != nil {
			s.log.Warn("cold-tier replay unavailable: read diff", "table", tid.String(), "error", err)
			return nil, false
		}
		cr.diffs = append(cr.diffs, coldDiff{position: d.ToPosition, changes: changes})
	}
	return cr, true
}

// streamColdReplay sends the materialized base snapshot (if any) and diffs to the
// client, applying the subscription filter and stamping the cold resume sequence
// plus each diff's to_position. After it returns, the caller continues with the
// ordinary hot-journal catch-up from resumeSeq and then the live loop.
func streamColdReplay(stream *connect.ServerStream[v1.SyncResponse], cr *coldReplay, filter *subscriptionFilter) error {
	if cr.hasSnapshot {
		rows := cr.snapshotRows
		if filter != nil {
			rows = filterRows(rows, filter)
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotBegin{
			SnapshotBegin: &v1.SnapshotBegin{SnapshotId: cr.snapshotID, Sequence: cr.resumeSeq, RowCount: int64(len(rows))},
		}}); err != nil {
			return err
		}
		for _, row := range rows {
			rowStruct, _ := structpb.NewStruct(map[string]any(row))
			if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotRow{
				SnapshotRow: &v1.SnapshotRow{Row: rowStruct},
			}}); err != nil {
				return err
			}
		}
		if err := stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_SnapshotEnd{
			SnapshotEnd: &v1.SnapshotEnd{Sequence: cr.resumeSeq, RowsSent: int64(len(rows))},
		}}); err != nil {
			return err
		}
	}
	for _, d := range cr.diffs {
		for _, ch := range d.changes {
			if !changeMatchesFilter(ch, filter) {
				continue
			}
			if err := sendArchiveChange(stream, ch, d.position, cr.resumeSeq); err != nil {
				return err
			}
		}
	}
	return nil
}

// filterRows returns the rows that satisfy the (non-nil) subscription filter.
func filterRows(rows []laredo.Row, filter *subscriptionFilter) []laredo.Row {
	out := make([]laredo.Row, 0, len(rows))
	for _, row := range rows {
		if filter.matchRow(row) {
			out = append(out, row)
		}
	}
	return out
}

// changeMatchesFilter applies the same rule as subscriptionFilter.matchEntry to a
// decoded archive change: the post-change row for inserts/updates, the pre-change
// row for deletes, truncate always. A nil filter admits everything.
func changeMatchesFilter(ch snapshotter.Change, filter *subscriptionFilter) bool {
	if filter == nil {
		return true
	}
	switch ch.Action {
	case laredo.ActionDelete:
		return filter.matchRow(ch.Old)
	case laredo.ActionTruncate:
		return true
	default:
		return filter.matchRow(ch.New)
	}
}

// sendArchiveChange streams one archive change as a journal entry, stamped with
// the diff's to_position (the watermark the client resumes from) and the cold
// resume sequence.
func sendArchiveChange(stream *connect.ServerStream[v1.SyncResponse], ch snapshotter.Change, position string, seq int64) error {
	entry := &v1.ReplicationJournalEntry{
		Sequence:       seq,
		SourcePosition: position,
		Action:         ch.Action.String(),
		Timestamp:      timestamppb.Now(),
	}
	if ch.New != nil {
		entry.NewValues, _ = structpb.NewStruct(map[string]any(ch.New))
	}
	if ch.Old != nil {
		entry.OldValues, _ = structpb.NewStruct(map[string]any(ch.Old))
	}
	return stream.Send(&v1.SyncResponse{Message: &v1.SyncResponse_JournalEntry{JournalEntry: entry}})
}
