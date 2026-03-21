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

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
)

// Service implements the LaredoOAMService. Unimplemented RPCs return
// CodeUnimplemented; replay RPCs are wired to the engine's SnapshotReplay.
type Service struct {
	laredov1connect.UnimplementedLaredoOAMServiceHandler

	engine        laredo.Engine
	snapshotStore laredo.SnapshotStore

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
