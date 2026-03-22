// Package replication implements the fan-out Replication gRPC service.
package replication

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

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
			return connect.NewResponse(&v1.GetReplicationStatusResponse{
				CurrentSequence:       ft.JournalSequence(),
				JournalOldestSequence: ft.JournalOldestSequence(),
				JournalEntryCount:     int64(ft.JournalLen()),
				RowCount:              int64(ft.Count()),
			}), nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no fan-out target for %s.%s", schema, table))
}
