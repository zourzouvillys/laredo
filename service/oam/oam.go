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

	mu      sync.Mutex
	replays map[string]*replayState
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
