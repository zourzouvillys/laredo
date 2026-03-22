// Package replication implements the fan-out Replication gRPC service.
package replication

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/target/fanout"
)

// Service implements the LaredoReplicationService for fan-out targets.
type Service struct {
	replicationv1connect.UnimplementedLaredoReplicationServiceHandler
	engine laredo.Engine
}

// New creates a new Replication service.
func New(engine laredo.Engine) *Service {
	return &Service{engine: engine}
}

// GetReplicationStatus returns current replication status for a fan-out target.
func (s *Service) GetReplicationStatus(_ context.Context, req *connect.Request[v1.GetReplicationStatusRequest]) (*connect.Response[v1.GetReplicationStatusResponse], error) {
	schema := req.Msg.GetSchema()
	table := req.Msg.GetTable()
	if schema == "" || table == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	tid := laredo.Table(schema, table)
	targets := s.engine.Targets("", tid)

	for _, t := range targets {
		if ft, ok := t.(*fanout.Target); ok {
			resp := &v1.GetReplicationStatusResponse{
				CurrentSequence:       ft.JournalSequence(),
				JournalOldestSequence: ft.JournalOldestSequence(),
				JournalEntryCount:     int64(ft.JournalLen()),
				RowCount:              int64(ft.Count()),
				ConnectedClients:      int32(ft.ConnectedClients()), //nolint:gosec // won't overflow
			}

			// Add per-client state.
			for _, ci := range ft.ClientList() {
				resp.Clients = append(resp.Clients, &v1.ConnectedClient{
					ClientId:        ci.ID,
					CurrentSequence: ci.CurrentSequence,
					State:           ci.State,
					BufferDepth:     int32(ci.BufferDepth), //nolint:gosec // won't overflow
				})
			}

			// Latest snapshot info.
			if snap := ft.LatestSnapshot(); snap != nil {
				resp.LatestSnapshot = &v1.ReplicationSnapshotInfo{
					SnapshotId: snap.ID,
					Sequence:   snap.Sequence,
					RowCount:   snap.RowCount,
				}
			}

			return connect.NewResponse(resp), nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no fan-out target for %s.%s", schema, table))
}

// ListSnapshots returns available fan-out snapshots for client bootstrapping.
func (s *Service) ListSnapshots(_ context.Context, req *connect.Request[v1.ListSnapshotsRequest]) (*connect.Response[v1.ListSnapshotsResponse], error) {
	schema := req.Msg.GetSchema()
	table := req.Msg.GetTable()
	if schema == "" || table == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	tid := laredo.Table(schema, table)
	targets := s.engine.Targets("", tid)

	for _, t := range targets {
		if ft, ok := t.(*fanout.Target); ok {
			snaps := ft.ListSnapshots()
			var result []*v1.ReplicationSnapshotInfo
			for _, snap := range snaps {
				result = append(result, &v1.ReplicationSnapshotInfo{
					SnapshotId: snap.ID,
					Sequence:   snap.Sequence,
					RowCount:   snap.RowCount,
				})
			}
			return connect.NewResponse(&v1.ListSnapshotsResponse{Snapshots: result}), nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no fan-out target for %s.%s", schema, table))
}

// FetchSnapshot streams a specific snapshot's data to the client.
func (s *Service) FetchSnapshot(_ context.Context, req *connect.Request[v1.FetchSnapshotRequest], stream *connect.ServerStream[v1.FetchSnapshotResponse]) error {
	snapshotID := req.Msg.GetSnapshotId()
	if snapshotID == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("snapshot_id is required"))
	}

	// Find the snapshot across all fan-out targets.
	for _, sid := range s.engine.SourceIDs() {
		for _, t := range s.engine.Targets(sid, laredo.TableIdentifier{}) {
			ft, ok := t.(*fanout.Target)
			if !ok {
				continue
			}
			for _, snap := range ft.ListSnapshots() {
				if snap.ID != snapshotID {
					continue
				}

				// Found — stream it.
				if err := stream.Send(&v1.FetchSnapshotResponse{
					Chunk: &v1.FetchSnapshotResponse_Begin{
						Begin: &v1.SnapshotBegin{
							SnapshotId: snap.ID,
							Sequence:   snap.Sequence,
							RowCount:   snap.RowCount,
						},
					},
				}); err != nil {
					return err
				}

				for _, row := range snap.Rows {
					rowStruct, _ := structpb.NewStruct(map[string]any(row))
					if err := stream.Send(&v1.FetchSnapshotResponse{
						Chunk: &v1.FetchSnapshotResponse_Row{
							Row: &v1.SnapshotRow{Row: rowStruct},
						},
					}); err != nil {
						return err
					}
				}

				return stream.Send(&v1.FetchSnapshotResponse{
					Chunk: &v1.FetchSnapshotResponse_End{
						End: &v1.SnapshotEnd{
							Sequence: snap.Sequence,
							RowsSent: snap.RowCount,
						},
					},
				})
			}
		}
	}

	return connect.NewError(connect.CodeNotFound, fmt.Errorf("snapshot %s not found", snapshotID))
}

// Sync implements the primary server-streaming replication call.
// Protocol: handshake → snapshot (if needed) → journal catch-up → live streaming.
func (s *Service) Sync(ctx context.Context, req *connect.Request[v1.SyncRequest], stream *connect.ServerStream[v1.SyncResponse]) error {
	schema := req.Msg.GetSchema()
	table := req.Msg.GetTable()
	clientID := req.Msg.GetClientId()
	lastSeq := req.Msg.GetLastKnownSequence()

	if schema == "" || table == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	ft := s.findFanoutTarget(schema, table)
	if ft == nil {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("no fan-out target for %s.%s", schema, table))
	}

	if clientID == "" {
		clientID = fmt.Sprintf("anon-%d", time.Now().UnixMilli())
	}
	if !ft.RegisterClient(clientID) {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("max clients reached"))
	}
	defer ft.UnregisterClient(clientID)

	// Determine sync mode.
	oldestSeq := ft.JournalOldestSequence()
	currentSeq := ft.JournalSequence()
	snapshotID := req.Msg.GetLastSnapshotId()

	var mode v1.SyncMode
	var resumeSeq int64
	var handshakeSnapshotID string

	switch {
	case lastSeq > 0 && lastSeq >= oldestSeq:
		// Client has a recent sequence — send journal delta.
		mode = v1.SyncMode_SYNC_MODE_DELTA
		resumeSeq = lastSeq

	case snapshotID != "":
		// Client has a local snapshot — check if we can send delta from its sequence.
		for _, snap := range ft.ListSnapshots() {
			if snap.ID == snapshotID && snap.Sequence >= oldestSeq {
				mode = v1.SyncMode_SYNC_MODE_DELTA_FROM_SNAPSHOT
				resumeSeq = snap.Sequence
				handshakeSnapshotID = snap.ID
				break
			}
		}
		if mode == v1.SyncMode_SYNC_MODE_UNSPECIFIED {
			// Snapshot not found or too old — fall back to full snapshot.
			mode = v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT
		}

	default:
		mode = v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT
	}

	// Handshake.
	if err := stream.Send(&v1.SyncResponse{
		Message: &v1.SyncResponse_Handshake{
			Handshake: &v1.SyncHandshake{
				Mode:                  mode,
				ServerCurrentSequence: currentSeq,
				JournalOldestSequence: oldestSeq,
				ResumeFromSequence:    resumeSeq,
				SnapshotId:            handshakeSnapshotID,
			},
		},
	}); err != nil {
		return err
	}

	ft.SetClientState(clientID, "catching_up")

	// Full snapshot if needed.
	if mode == v1.SyncMode_SYNC_MODE_FULL_SNAPSHOT {
		snap := ft.TakeSnapshot()
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotBegin{
				SnapshotBegin: &v1.SnapshotBegin{
					SnapshotId: snap.ID, Sequence: snap.Sequence, RowCount: snap.RowCount,
				},
			},
		}); err != nil {
			return err
		}
		for _, row := range snap.Rows {
			rowStruct, _ := structpb.NewStruct(map[string]any(row))
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_SnapshotRow{SnapshotRow: &v1.SnapshotRow{Row: rowStruct}},
			}); err != nil {
				return err
			}
		}
		if err := stream.Send(&v1.SyncResponse{
			Message: &v1.SyncResponse_SnapshotEnd{
				SnapshotEnd: &v1.SnapshotEnd{Sequence: snap.Sequence, RowsSent: snap.RowCount},
			},
		}); err != nil {
			return err
		}
		resumeSeq = snap.Sequence
	}

	// Journal catch-up.
	for _, e := range ft.JournalEntriesSince(resumeSeq) {
		if err := sendJournalEntry(stream, e); err != nil {
			return err
		}
		ft.UpdateClientSequence(clientID, e.Sequence)
	}

	ft.SetClientState(clientID, "live")
	lastSeqSent := ft.JournalSequence()
	lastSent := time.Now()

	// Live streaming loop.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		newEntries := ft.JournalEntriesSince(lastSeqSent)
		if len(newEntries) > 0 {
			for _, e := range newEntries {
				if err := sendJournalEntry(stream, e); err != nil {
					return err
				}
				ft.UpdateClientSequence(clientID, e.Sequence)
				lastSeqSent = e.Sequence
			}
			lastSent = time.Now()
		} else if time.Since(lastSent) >= ft.HeartbeatInterval() {
			if err := stream.Send(&v1.SyncResponse{
				Message: &v1.SyncResponse_Heartbeat{
					Heartbeat: &v1.Heartbeat{
						CurrentSequence: ft.JournalSequence(),
						ServerTime:      timestamppb.Now(),
					},
				},
			}); err != nil {
				return err
			}
			lastSent = time.Now()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *Service) findFanoutTarget(schema, table string) *fanout.Target {
	tid := laredo.Table(schema, table)
	for _, t := range s.engine.Targets("", tid) {
		if ft, ok := t.(*fanout.Target); ok {
			return ft
		}
	}
	return nil
}

func sendJournalEntry(stream *connect.ServerStream[v1.SyncResponse], e fanout.JournalEntry) error {
	entry := &v1.ReplicationJournalEntry{
		Sequence:  e.Sequence,
		Timestamp: timestamppb.New(e.Timestamp),
		Action:    e.Action.String(),
	}
	if e.NewValues != nil {
		entry.NewValues, _ = structpb.NewStruct(map[string]any(e.NewValues))
	}
	if e.OldValues != nil {
		entry.OldValues, _ = structpb.NewStruct(map[string]any(e.OldValues))
	}
	return stream.Send(&v1.SyncResponse{
		Message: &v1.SyncResponse_JournalEntry{JournalEntry: entry},
	})
}
