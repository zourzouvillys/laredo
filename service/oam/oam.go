// Package oam implements the OAM (operations, administration, monitoring)
// gRPC service for the laredo engine.
package oam

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/deadletter"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
)

// Service implements the LaredoOAMService. Unimplemented RPCs return
// CodeUnimplemented; replay RPCs are wired to the engine's SnapshotReplay.
type Service struct {
	laredov1connect.UnimplementedLaredoOAMServiceHandler

	engine          laredo.Engine
	snapshotStore   laredo.SnapshotStore
	deadLetterStore deadletter.Store

	mu       sync.Mutex
	replays  map[string]*replayState
	watchers []*watcher
}

// watcher is a registered WatchStatus subscriber.
type watcher struct {
	ch          chan *v1.WatchStatusResponse
	tables      map[string]bool // empty = all tables
	pipelineIDs map[string]bool // empty = all pipelines
}

type replayState struct {
	id        string
	cancel    context.CancelFunc
	startedAt time.Time
	result    *laredo.ReplayResult
	err       error
	done      chan struct{}
}

// Option configures the OAM service.
type Option func(*Service)

// WithSnapshotStore sets the snapshot store used for replay operations.
func WithSnapshotStore(store laredo.SnapshotStore) Option {
	return func(s *Service) {
		s.snapshotStore = store
	}
}

// WithDeadLetterStore sets the dead letter store for list/replay/purge operations.
func WithDeadLetterStore(store deadletter.Store) Option {
	return func(s *Service) {
		s.deadLetterStore = store
	}
}

// New creates a new OAM service backed by the given engine.
func New(engine laredo.Engine, opts ...Option) *Service {
	s := &Service{
		engine:  engine,
		replays: make(map[string]*replayState),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SetEngine sets the engine after construction. Useful when the observer must
// be registered before the engine starts.
func (s *Service) SetEngine(engine laredo.Engine) {
	s.engine = engine
}

// --- Status & Monitoring ---

// GetStatus returns the aggregate engine state with source and pipeline statuses.
func (s *Service) GetStatus(_ context.Context, _ *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	pipelines := s.engine.Pipelines()

	// Determine aggregate service state.
	state := v1.ServiceState_SERVICE_STATE_STREAMING
	if !s.engine.IsReady() {
		state = v1.ServiceState_SERVICE_STATE_BASELINING
	}
	for _, p := range pipelines {
		if p.State == laredo.PipelineError {
			state = v1.ServiceState_SERVICE_STATE_ERROR
			break
		}
		if p.State == laredo.PipelinePaused {
			state = v1.ServiceState_SERVICE_STATE_PAUSED
		}
	}

	// Build pipeline statuses.
	pipelineStatuses := make([]*v1.PipelineStatus, 0, len(pipelines))
	for _, p := range pipelines {
		pipelineStatuses = append(pipelineStatuses, pipelineInfoToProto(p))
	}

	// Build source statuses.
	sourceIDs := s.engine.SourceIDs()
	sourceStatuses := make([]*v1.SourceStatus, 0, len(sourceIDs))
	for _, sid := range sourceIDs {
		sourceStatuses = append(sourceStatuses, &v1.SourceStatus{
			SourceId: sid,
		})
	}

	return connect.NewResponse(&v1.GetStatusResponse{
		State:     state,
		Pipelines: pipelineStatuses,
		Sources:   sourceStatuses,
	}), nil
}

// GetTableStatus returns pipelines for a specific table.
func (s *Service) GetTableStatus(_ context.Context, req *connect.Request[v1.GetTableStatusRequest]) (*connect.Response[v1.GetTableStatusResponse], error) {
	schema := req.Msg.GetSchema()
	table := req.Msg.GetTable()
	if schema == "" || table == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	tid := laredo.Table(schema, table)
	var statuses []*v1.PipelineStatus
	for _, p := range s.engine.Pipelines() {
		if p.Table == tid {
			statuses = append(statuses, pipelineInfoToProto(p))
		}
	}

	return connect.NewResponse(&v1.GetTableStatusResponse{
		Pipelines: statuses,
	}), nil
}

// GetPipelineStatus returns the status of a single pipeline.
func (s *Service) GetPipelineStatus(_ context.Context, req *connect.Request[v1.GetPipelineStatusRequest]) (*connect.Response[v1.GetPipelineStatusResponse], error) {
	pipelineID := req.Msg.GetPipelineId()
	if pipelineID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pipeline_id is required"))
	}

	for _, p := range s.engine.Pipelines() {
		if p.ID == pipelineID {
			return connect.NewResponse(&v1.GetPipelineStatusResponse{
				Status: pipelineInfoToProto(p),
			}), nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pipeline %s not found", pipelineID))
}

// WatchStatus streams engine events to the client in real time.
func (s *Service) WatchStatus(ctx context.Context, req *connect.Request[v1.WatchStatusRequest], stream *connect.ServerStream[v1.WatchStatusResponse]) error {
	w := &watcher{
		ch:          make(chan *v1.WatchStatusResponse, 256),
		tables:      make(map[string]bool),
		pipelineIDs: make(map[string]bool),
	}
	for _, t := range req.Msg.GetTables() {
		w.tables[t] = true
	}
	for _, pid := range req.Msg.GetPipelineIds() {
		w.pipelineIDs[pid] = true
	}

	s.mu.Lock()
	s.watchers = append(s.watchers, w)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		for i, ww := range s.watchers {
			if ww == w {
				s.watchers = append(s.watchers[:i], s.watchers[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-w.ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// Observer returns an EngineObserver that feeds events to connected WatchStatus clients.
func (s *Service) Observer() laredo.EngineObserver {
	return &serviceObserver{svc: s}
}

// serviceObserver implements EngineObserver to broadcast events to WatchStatus watchers.
type serviceObserver struct {
	laredo.NullObserver
	svc *Service
}

//nolint:revive // EngineObserver implementation.
func (o *serviceObserver) OnPipelineStateChanged(pipelineID string, oldState, newState laredo.PipelineState) {
	o.svc.broadcast(pipelineID, "", &v1.WatchStatusResponse{
		Timestamp: timestamppb.Now(),
		Event: &v1.WatchStatusResponse_PipelineStateChange{
			PipelineStateChange: &v1.PipelineStateChangeEvent{
				PipelineId: pipelineID,
				OldState:   pipelineStateToProto(oldState),
				NewState:   pipelineStateToProto(newState),
			},
		},
	})
}

//nolint:revive // EngineObserver implementation.
func (o *serviceObserver) OnSourceConnected(sourceID, sourceType string) {
	o.svc.broadcast("", "", &v1.WatchStatusResponse{
		Timestamp: timestamppb.Now(),
		Event: &v1.WatchStatusResponse_SourceStateChange{
			SourceStateChange: &v1.SourceStateChange{
				SourceId:  sourceID,
				EventType: "connected",
				Message:   fmt.Sprintf("source connected (type=%s)", sourceType),
			},
		},
	})
}

//nolint:revive // EngineObserver implementation.
func (o *serviceObserver) OnSourceDisconnected(sourceID, reason string) {
	o.svc.broadcast("", "", &v1.WatchStatusResponse{
		Timestamp: timestamppb.Now(),
		Event: &v1.WatchStatusResponse_SourceStateChange{
			SourceStateChange: &v1.SourceStateChange{
				SourceId:  sourceID,
				EventType: "disconnected",
				Message:   reason,
			},
		},
	})
}

//nolint:revive // EngineObserver implementation.
func (o *serviceObserver) OnChangeApplied(pipelineID string, table laredo.TableIdentifier, action laredo.ChangeAction, _ time.Duration) {
	o.svc.broadcast(pipelineID, table.Schema+"."+table.Table, &v1.WatchStatusResponse{
		Timestamp: timestamppb.Now(),
		Event: &v1.WatchStatusResponse_RowChange{
			RowChange: &v1.RowChange{
				PipelineId: pipelineID,
				Schema:     table.Schema,
				Table:      table.Table,
				Action:     action.String(),
			},
		},
	})
}

// broadcast sends an event to all matching watchers. Non-blocking: drops events
// if a watcher's channel is full.
func (s *Service) broadcast(pipelineID, tableKey string, msg *v1.WatchStatusResponse) {
	s.mu.Lock()
	watchers := make([]*watcher, len(s.watchers))
	copy(watchers, s.watchers)
	s.mu.Unlock()

	for _, w := range watchers {
		if !w.matches(pipelineID, tableKey) {
			continue
		}
		select {
		case w.ch <- msg:
		default:
			// Watcher is slow — drop event to avoid blocking engine goroutine.
		}
	}
}

func (w *watcher) matches(pipelineID, tableKey string) bool {
	if len(w.pipelineIDs) > 0 && pipelineID != "" && !w.pipelineIDs[pipelineID] {
		return false
	}
	if len(w.tables) > 0 && tableKey != "" && !w.tables[tableKey] {
		return false
	}
	return true
}

func pipelineInfoToProto(p laredo.PipelineInfo) *v1.PipelineStatus {
	return &v1.PipelineStatus{
		PipelineId: p.ID,
		SourceId:   p.SourceID,
		Schema:     p.Table.Schema,
		Table:      p.Table.Table,
		TargetType: p.TargetType,
		State:      pipelineStateToProto(p.State),
		RowCount:   p.RowCount,
	}
}

func pipelineStateToProto(s laredo.PipelineState) v1.PipelineState {
	switch s {
	case laredo.PipelineInitializing:
		return v1.PipelineState_PIPELINE_STATE_INITIALIZING
	case laredo.PipelineBaselining:
		return v1.PipelineState_PIPELINE_STATE_BASELINING
	case laredo.PipelineStreaming:
		return v1.PipelineState_PIPELINE_STATE_STREAMING
	case laredo.PipelinePaused:
		return v1.PipelineState_PIPELINE_STATE_PAUSED
	case laredo.PipelineError:
		return v1.PipelineState_PIPELINE_STATE_ERROR
	case laredo.PipelineStopped:
		return v1.PipelineState_PIPELINE_STATE_STOPPED
	default:
		return v1.PipelineState_PIPELINE_STATE_UNSPECIFIED
	}
}

// CheckReady reports whether the engine (or a specific source/pipeline) is ready.
func (s *Service) CheckReady(_ context.Context, req *connect.Request[v1.CheckReadyRequest]) (*connect.Response[v1.CheckReadyResponse], error) {
	var ready bool
	var reasons []string

	switch {
	case req.Msg.GetSource() != "":
		ready = s.engine.IsSourceReady(req.Msg.GetSource())
		if !ready {
			reasons = append(reasons, fmt.Sprintf("source %s is not ready", req.Msg.GetSource()))
		}
	default:
		ready = s.engine.IsReady()
		if !ready {
			reasons = append(reasons, "not all pipelines are ready")
		}
	}

	return connect.NewResponse(&v1.CheckReadyResponse{
		Ready:           ready,
		NotReadyReasons: reasons,
	}), nil
}

// ReloadTable triggers a re-baseline for a specific table on a source.
func (s *Service) ReloadTable(ctx context.Context, req *connect.Request[v1.ReloadTableRequest]) (*connect.Response[v1.ReloadTableResponse], error) {
	sourceID := req.Msg.GetSourceId()
	if sourceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source_id is required"))
	}

	tableStr := req.Msg.GetSchema() + "." + req.Msg.GetTable()
	var table laredo.TableIdentifier
	if err := table.UnmarshalText([]byte(tableStr)); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid table: %w", err))
	}

	if err := s.engine.Reload(ctx, sourceID, table); err != nil {
		return connect.NewResponse(&v1.ReloadTableResponse{
			Accepted: false,
			Message:  err.Error(),
		}), nil
	}

	return connect.NewResponse(&v1.ReloadTableResponse{
		Accepted: true,
		Message:  fmt.Sprintf("reload started for %s on source %s", table, sourceID),
	}), nil
}

// PauseSync pauses change streaming for a source.
func (s *Service) PauseSync(ctx context.Context, req *connect.Request[v1.PauseSyncRequest]) (*connect.Response[v1.PauseSyncResponse], error) {
	sourceID := req.Msg.GetSourceId()
	if sourceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source_id is required"))
	}

	if err := s.engine.Pause(ctx, sourceID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.PauseSyncResponse{
		State: v1.ServiceState_SERVICE_STATE_PAUSED,
	}), nil
}

// ResumeSync resumes change streaming for a source.
func (s *Service) ResumeSync(ctx context.Context, req *connect.Request[v1.ResumeSyncRequest]) (*connect.Response[v1.ResumeSyncResponse], error) {
	sourceID := req.Msg.GetSourceId()
	if sourceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source_id is required"))
	}

	if err := s.engine.Resume(ctx, sourceID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&v1.ResumeSyncResponse{
		State: v1.ServiceState_SERVICE_STATE_STREAMING,
	}), nil
}

// GetSourceInfo returns details about sources.
func (s *Service) GetSourceInfo(_ context.Context, req *connect.Request[v1.GetSourceInfoRequest]) (*connect.Response[v1.GetSourceInfoResponse], error) {
	var sources []*v1.SourceInfo

	if id := req.Msg.GetSourceId(); id != "" {
		// Specific source.
		info, ok := s.engine.SourceInfo(id)
		if !ok {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("source %s not found", id))
		}
		sources = append(sources, sourceRunInfoToProto(info))
	} else {
		// All sources.
		for _, id := range s.engine.SourceIDs() {
			if info, ok := s.engine.SourceInfo(id); ok {
				sources = append(sources, sourceRunInfoToProto(info))
			}
		}
	}

	return connect.NewResponse(&v1.GetSourceInfoResponse{Sources: sources}), nil
}

func sourceRunInfoToProto(info laredo.SourceRunInfo) *v1.SourceInfo {
	si := &v1.SourceInfo{
		SourceId:          info.ID,
		SourceType:        info.SourceType,
		SupportsResume:    info.SupportsResume,
		OrderingGuarantee: orderingGuaranteeStr(info.OrderingGuarantee),
		LagBytes:          info.Lag.LagBytes,
	}
	if info.Lag.LagTime != nil {
		si.LagTime = durationpb.New(*info.Lag.LagTime)
	}
	return si
}

func orderingGuaranteeStr(g laredo.OrderingGuarantee) string {
	switch g {
	case laredo.TotalOrder:
		return "total"
	case laredo.PerPartitionOrder:
		return "per-partition"
	case laredo.BestEffort:
		return "best-effort"
	default:
		return "unknown"
	}
}

// GetTableSchema returns column definitions for a table.
func (s *Service) GetTableSchema(_ context.Context, req *connect.Request[v1.GetTableSchemaRequest]) (*connect.Response[v1.GetTableSchemaResponse], error) {
	schema := req.Msg.GetSchema()
	table := req.Msg.GetTable()
	if schema == "" || table == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	cols := s.engine.TableSchema(laredo.Table(schema, table))
	if cols == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no schema for %s.%s", schema, table))
	}

	var protoCols []*v1.ColumnDefinition
	for _, c := range cols {
		protoCols = append(protoCols, &v1.ColumnDefinition{
			OrdinalPosition:   int32(c.OrdinalPosition), //nolint:gosec // won't overflow
			ColumnName:        c.Name,
			DataType:          c.Type,
			NotNull:           !c.Nullable,
			IsPrimaryKey:      c.PrimaryKey,
			PrimaryKeyOrdinal: int32(c.PrimaryKeyOrdinal), //nolint:gosec // won't overflow
		})
	}

	return connect.NewResponse(&v1.GetTableSchemaResponse{Columns: protoCols}), nil
}

// ResetSource drops and recreates a source's replication slot.
func (s *Service) ResetSource(ctx context.Context, req *connect.Request[v1.ResetSourceRequest]) (*connect.Response[v1.ResetSourceResponse], error) {
	sourceID := req.Msg.GetSourceId()
	if sourceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("source_id is required"))
	}
	if !req.Msg.GetConfirm() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("confirm=true is required for destructive operation"))
	}

	if err := s.engine.ResetSource(ctx, sourceID, req.Msg.GetDropPublication()); err != nil {
		return connect.NewResponse(&v1.ResetSourceResponse{
			Accepted: false,
			Message:  err.Error(),
		}), nil
	}

	return connect.NewResponse(&v1.ResetSourceResponse{
		Accepted: true,
		Message:  fmt.Sprintf("source %s reset successfully", sourceID),
	}), nil
}

// ListTables returns the configured tables derived from pipeline information.
func (s *Service) ListTables(_ context.Context, _ *connect.Request[v1.ListTablesRequest]) (*connect.Response[v1.ListTablesResponse], error) {
	pipelines := s.engine.Pipelines()

	// Deduplicate tables (same table may have multiple targets).
	type tableKey struct{ schema, table string }
	seen := make(map[tableKey]bool)
	var tables []*v1.TableConfig

	for _, p := range pipelines {
		key := tableKey{p.Table.Schema, p.Table.Table}
		if seen[key] {
			continue
		}
		seen[key] = true
		tables = append(tables, &v1.TableConfig{
			Schema:     p.Table.Schema,
			Table:      p.Table.Table,
			SourceId:   p.SourceID,
			TargetType: p.TargetType,
		})
	}

	return connect.NewResponse(&v1.ListTablesResponse{Tables: tables}), nil
}

// CreateSnapshot triggers an on-demand snapshot.
func (s *Service) CreateSnapshot(ctx context.Context, req *connect.Request[v1.CreateSnapshotRequest]) (*connect.Response[v1.CreateSnapshotResponse], error) {
	// Convert proto Struct to map[string]Value for user metadata.
	var userMeta map[string]laredo.Value
	if req.Msg.GetUserMeta() != nil {
		userMeta = make(map[string]laredo.Value, len(req.Msg.GetUserMeta().GetFields()))
		for k, v := range req.Msg.GetUserMeta().GetFields() {
			userMeta[k] = v.AsInterface()
		}
	}

	if err := s.engine.CreateSnapshot(ctx, userMeta); err != nil {
		return connect.NewResponse(&v1.CreateSnapshotResponse{
			Accepted: false,
			Message:  err.Error(),
		}), nil
	}

	return connect.NewResponse(&v1.CreateSnapshotResponse{
		Accepted: true,
		Message:  "snapshot creation started",
	}), nil
}

// ListSnapshots returns available snapshots.
func (s *Service) ListSnapshots(ctx context.Context, req *connect.Request[v1.ListSnapshotsRequest]) (*connect.Response[v1.ListSnapshotsResponse], error) {
	if s.snapshotStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no snapshot store configured"))
	}

	var filter *laredo.SnapshotFilter
	if t := req.Msg.GetTable(); t != "" {
		var table laredo.TableIdentifier
		if err := table.UnmarshalText([]byte(t)); err == nil {
			filter = &laredo.SnapshotFilter{Table: &table}
		}
	}

	descriptors, err := s.snapshotStore.List(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list snapshots: %w", err))
	}

	infos := make([]*v1.SnapshotInfo, 0, len(descriptors))
	for _, d := range descriptors {
		infos = append(infos, descriptorToProto(d))
	}

	return connect.NewResponse(&v1.ListSnapshotsResponse{Snapshots: infos}), nil
}

// InspectSnapshot returns detailed information about a specific snapshot.
func (s *Service) InspectSnapshot(ctx context.Context, req *connect.Request[v1.InspectSnapshotRequest]) (*connect.Response[v1.InspectSnapshotResponse], error) {
	if s.snapshotStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no snapshot store configured"))
	}

	id := req.Msg.GetSnapshotId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("snapshot_id is required"))
	}

	desc, err := s.snapshotStore.Describe(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("describe snapshot: %w", err))
	}

	return connect.NewResponse(&v1.InspectSnapshotResponse{Info: descriptorToProto(desc)}), nil
}

// DeleteSnapshot removes a snapshot.
func (s *Service) DeleteSnapshot(ctx context.Context, req *connect.Request[v1.DeleteSnapshotRequest]) (*connect.Response[v1.DeleteSnapshotResponse], error) {
	if s.snapshotStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no snapshot store configured"))
	}

	id := req.Msg.GetSnapshotId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("snapshot_id is required"))
	}

	if err := s.snapshotStore.Delete(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete snapshot: %w", err))
	}

	return connect.NewResponse(&v1.DeleteSnapshotResponse{Deleted: true}), nil
}

// PruneSnapshots deletes all but the N most recent snapshots.
func (s *Service) PruneSnapshots(ctx context.Context, req *connect.Request[v1.PruneSnapshotsRequest]) (*connect.Response[v1.PruneSnapshotsResponse], error) {
	if s.snapshotStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no snapshot store configured"))
	}

	keep := int(req.Msg.GetKeep())
	if keep <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("keep must be positive"))
	}

	// Count before prune.
	before, _ := s.snapshotStore.List(ctx, nil)

	var tableFilter *laredo.TableIdentifier
	if t := req.Msg.GetTable(); t != "" {
		var table laredo.TableIdentifier
		if err := table.UnmarshalText([]byte(t)); err == nil {
			tableFilter = &table
		}
	}

	if err := s.snapshotStore.Prune(ctx, keep, tableFilter); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("prune snapshots: %w", err))
	}

	after, _ := s.snapshotStore.List(ctx, nil)
	deleted := len(before) - len(after)
	if deleted < 0 {
		deleted = 0
	}

	return connect.NewResponse(&v1.PruneSnapshotsResponse{
		DeletedCount: int32(deleted), //nolint:gosec // count won't overflow
	}), nil
}

func descriptorToProto(d laredo.SnapshotDescriptor) *v1.SnapshotInfo {
	info := &v1.SnapshotInfo{
		SnapshotId: d.SnapshotID,
		CreatedAt:  timestamppb.New(d.CreatedAt),
		Format:     d.Format,
		SizeBytes:  d.SizeBytes,
	}
	for _, t := range d.Tables {
		info.Tables = append(info.Tables, &v1.TableSnapshotSummary{
			Schema:     t.Table.Schema,
			Table:      t.Table.Table,
			RowCount:   t.RowCount,
			TargetType: t.TargetType,
		})
	}
	return info
}

// StartReplay begins replaying a snapshot into targets.
func (s *Service) StartReplay(_ context.Context, req *connect.Request[v1.StartReplayRequest]) (*connect.Response[v1.StartReplayResponse], error) {
	if s.snapshotStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no snapshot store configured"))
	}

	snapshotID := req.Msg.GetSnapshotId()
	if snapshotID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("snapshot_id is required"))
	}

	// Build the replay.
	replay := laredo.NewSnapshotReplay(s.snapshotStore)

	// Map speed string to ReplaySpeed.
	switch req.Msg.GetSpeed() {
	case "realtime":
		replay.Speed(laredo.ReplayRealTime)
	default:
		replay.Speed(laredo.ReplayFullSpeed)
	}

	// Find targets from engine for the requested tables.
	for _, tableStr := range req.Msg.GetTables() {
		var table laredo.TableIdentifier
		if err := table.UnmarshalText([]byte(tableStr)); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid table %q: %w", tableStr, err))
		}
		// Search all sources for targets matching this table.
		targets := s.engine.Targets("", table)
		for _, t := range targets {
			replay.Target(table, t)
		}
	}

	// Generate replay ID.
	pipelineID := req.Msg.GetTargetPipelineId()
	replayID := fmt.Sprintf("replay-%s-%d", snapshotID, time.Now().UnixMilli())
	if pipelineID != "" {
		replayID = fmt.Sprintf("replay-%s-%s-%d", pipelineID, snapshotID, time.Now().UnixMilli())
	}

	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored in replayState and called from StopReplay

	state := &replayState{
		id:        replayID,
		cancel:    cancel,
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}

	s.mu.Lock()
	s.replays[replayID] = state
	s.mu.Unlock()

	// Run replay in background.
	go func() {
		defer close(state.done)
		result, err := replay.Run(ctx, snapshotID)
		s.mu.Lock()
		state.result = &result
		state.err = err
		s.mu.Unlock()
	}()

	return connect.NewResponse(&v1.StartReplayResponse{
		Accepted: true,
		ReplayId: replayID,
		Message:  fmt.Sprintf("replay started for snapshot %s", snapshotID),
	}), nil
}

// GetReplayStatus returns the current status of a replay.
func (s *Service) GetReplayStatus(_ context.Context, req *connect.Request[v1.GetReplayStatusRequest]) (*connect.Response[v1.GetReplayStatusResponse], error) {
	replayID := req.Msg.GetReplayId()

	s.mu.Lock()
	state, ok := s.replays[replayID]
	s.mu.Unlock()

	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("replay %s not found", replayID))
	}

	resp := &v1.GetReplayStatusResponse{
		ReplayId: replayID,
	}

	select {
	case <-state.done:
		// Replay finished.
		s.mu.Lock()
		result := state.result
		err := state.err
		s.mu.Unlock()

		if err != nil {
			resp.State = "error"
		} else {
			resp.State = "completed"
			if result != nil {
				resp.RowsReplayed = result.TotalRows
				resp.Elapsed = durationpb.New(result.Duration)
			}
		}
	default:
		// Still running.
		resp.State = "running"
		resp.Elapsed = durationpb.New(time.Since(state.startedAt))
	}

	return connect.NewResponse(resp), nil
}

// StopReplay cancels a running replay.
func (s *Service) StopReplay(_ context.Context, req *connect.Request[v1.StopReplayRequest]) (*connect.Response[v1.StopReplayResponse], error) {
	replayID := req.Msg.GetReplayId()

	s.mu.Lock()
	state, ok := s.replays[replayID]
	s.mu.Unlock()

	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("replay %s not found", replayID))
	}

	state.cancel()

	return connect.NewResponse(&v1.StopReplayResponse{
		Stopped: true,
	}), nil
}

// --- Dead Letter Management ---

// ListDeadLetters returns dead letter entries for a pipeline.
func (s *Service) ListDeadLetters(_ context.Context, req *connect.Request[v1.ListDeadLettersRequest]) (*connect.Response[v1.ListDeadLettersResponse], error) {
	if s.deadLetterStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no dead letter store configured"))
	}

	pipelineID := req.Msg.GetPipelineId()
	if pipelineID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pipeline_id is required"))
	}

	entries, err := s.deadLetterStore.Read(pipelineID, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("read dead letters: %w", err))
	}

	protoEntries := make([]*v1.DeadLetterEntry, 0, len(entries))
	for _, e := range entries {
		dle := &v1.DeadLetterEntry{
			Timestamp:    timestamppb.New(e.Change.Timestamp),
			Action:       e.Change.Action.String(),
			ErrorMessage: e.Error.Message,
		}
		if e.Change.Position != nil {
			dle.Position = fmt.Sprintf("%v", e.Change.Position)
		}
		changeData, err := structpb.NewStruct(map[string]any{
			"new_values": map[string]any(e.Change.NewValues),
			"old_values": map[string]any(e.Change.OldValues),
		})
		if err == nil {
			dle.ChangeData = changeData
		}
		protoEntries = append(protoEntries, dle)
	}

	return connect.NewResponse(&v1.ListDeadLettersResponse{
		Entries:    protoEntries,
		TotalCount: int32(len(entries)), //nolint:gosec // entry count won't overflow int32
	}), nil
}

// PurgeDeadLetters removes all dead letter entries for a pipeline.
func (s *Service) PurgeDeadLetters(_ context.Context, req *connect.Request[v1.PurgeDeadLettersRequest]) (*connect.Response[v1.PurgeDeadLettersResponse], error) {
	if s.deadLetterStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no dead letter store configured"))
	}

	pipelineID := req.Msg.GetPipelineId()
	if pipelineID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pipeline_id is required"))
	}

	// Read count before purge for the response.
	entries, _ := s.deadLetterStore.Read(pipelineID, 0)
	count := int32(len(entries)) //nolint:gosec // entry count won't overflow int32

	if err := s.deadLetterStore.Purge(pipelineID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("purge dead letters: %w", err))
	}

	return connect.NewResponse(&v1.PurgeDeadLettersResponse{
		Purged: count,
	}), nil
}
