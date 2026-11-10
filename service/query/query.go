// Package query implements the Query gRPC service for looking up rows in
// in-memory targets.
package query

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/internal/rowpb"
	"github.com/zourzouvillys/laredo/target/memory"
)

// Page bounds for ListRows.
const (
	defaultListRowsPageSize = 100
	maxListRowsPageSize     = 10000
)

// Service implements the LaredoQueryService.
type Service struct {
	laredov1connect.UnimplementedLaredoQueryServiceHandler
	engine laredo.Engine
}

// New creates a new Query service backed by the given engine.
func New(engine laredo.Engine) *Service {
	return &Service{engine: engine}
}

// Lookup performs a single-row lookup on the configured lookup index.
func (s *Service) Lookup(_ context.Context, req *connect.Request[v1.LookupRequest]) (*connect.Response[v1.LookupResponse], error) {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return nil, err
	}

	keyValues := protoValuesToLaredo(req.Msg.GetKeyValues())

	indexName := req.Msg.GetIndexName()
	if indexName != "" {
		// Use named index — LookupAll returns at most one for unique indexes.
		rows := target.LookupAll(indexName, keyValues...)
		if len(rows) == 0 {
			return connect.NewResponse(&v1.LookupResponse{Found: false}), nil
		}
		row, err := rowToStruct(rows[0])
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&v1.LookupResponse{Found: true, Row: row}), nil
	}

	// Default: use the primary lookup index.
	row, ok := target.Lookup(keyValues...)
	if !ok {
		return connect.NewResponse(&v1.LookupResponse{Found: false}), nil
	}
	s2, err := rowToStruct(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.LookupResponse{Found: true, Row: s2}), nil
}

// LookupAll performs a multi-row lookup on a named secondary index.
func (s *Service) LookupAll(_ context.Context, req *connect.Request[v1.LookupAllRequest]) (*connect.Response[v1.LookupAllResponse], error) {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return nil, err
	}

	indexName := req.Msg.GetIndexName()
	if indexName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("index_name is required"))
	}

	keyValues := protoValuesToLaredo(req.Msg.GetKeyValues())
	rows := target.LookupAll(indexName, keyValues...)

	protoRows := make([]*structpb.Struct, 0, len(rows))
	for _, row := range rows {
		s2, err := rowToStruct(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		protoRows = append(protoRows, s2)
	}

	return connect.NewResponse(&v1.LookupAllResponse{Rows: protoRows}), nil
}

// GetRow retrieves a row by primary key.
func (s *Service) GetRow(_ context.Context, req *connect.Request[v1.GetRowRequest]) (*connect.Response[v1.GetRowResponse], error) {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return nil, err
	}

	row, ok := target.Get(req.Msg.GetPrimaryKey())
	if !ok {
		return connect.NewResponse(&v1.GetRowResponse{Found: false}), nil
	}
	s2, err := rowToStruct(row)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.GetRowResponse{Found: true, Row: s2}), nil
}

// ListRows returns a paginated list of all rows.
func (s *Service) ListRows(_ context.Context, req *connect.Request[v1.ListRowsRequest]) (*connect.Response[v1.ListRowsResponse], error) {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return nil, err
	}

	// Cap the page. There is no page token, so an unbounded page_size was a
	// way to materialize the whole table into one response.
	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = defaultListRowsPageSize
	}
	if pageSize > maxListRowsPageSize {
		pageSize = maxListRowsPageSize
	}

	var rows []*structpb.Struct
	for _, row := range target.All() {
		s2, err := rowToStruct(row)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		rows = append(rows, s2)
		if len(rows) >= pageSize {
			break
		}
	}

	return connect.NewResponse(&v1.ListRowsResponse{
		Rows:       rows,
		TotalCount: int64(target.Count()),
	}), nil
}

// CountRows returns the number of rows in the target.
func (s *Service) CountRows(_ context.Context, req *connect.Request[v1.CountRowsRequest]) (*connect.Response[v1.CountRowsResponse], error) {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.CountRowsResponse{
		Count: int64(target.Count()),
	}), nil
}

// Subscribe streams change events for a table. If replay_existing is true,
// all current rows are sent as INSERT events before live changes.
func (s *Service) Subscribe(ctx context.Context, req *connect.Request[v1.SubscribeRequest], stream *connect.ServerStream[v1.SubscribeResponse]) error {
	target, err := s.findIndexedTarget(req.Msg.GetSchema(), req.Msg.GetTable())
	if err != nil {
		return err
	}

	ch := make(chan *v1.SubscribeResponse, 256)

	// A row that will not encode ends the subscription. Dropping the event
	// instead would leave the subscriber believing it had seen every change,
	// which is exactly the silent divergence this conversion path exists to
	// prevent — unlike the slow-consumer drop below, which the protocol's
	// buffering contract already allows for.
	encodeErr := make(chan error, 1)
	failSubscription := func(err error) {
		select {
		case encodeErr <- err:
		default:
		}
	}

	// Register listener. The callback runs under the target's write lock, so
	// it must not block — use a non-blocking channel send.
	unsub := target.Listen(func(old, new laredo.Row) {
		var action string
		switch {
		case old == nil && new != nil:
			action = "INSERT"
		case old != nil && new != nil:
			action = "UPDATE"
		case old != nil && new == nil:
			action = "DELETE"
		default:
			action = "TRUNCATE"
		}

		resp := &v1.SubscribeResponse{
			Action:    action,
			Timestamp: timestamppb.Now(),
		}
		if new != nil {
			v, err := rowToStruct(new)
			if err != nil {
				failSubscription(fmt.Errorf("encode new values: %w", err))
				return
			}
			resp.NewValues = v
		}
		if old != nil {
			v, err := rowToStruct(old)
			if err != nil {
				failSubscription(fmt.Errorf("encode old values: %w", err))
				return
			}
			resp.OldValues = v
		}

		select {
		case ch <- resp:
		default:
			// Slow consumer — drop event.
		}
	})
	defer unsub()

	// Replay existing rows if requested.
	if req.Msg.GetReplayExisting() {
		for _, row := range target.All() {
			rowStruct, err := rowToStruct(row)
			if err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			if err := stream.Send(&v1.SubscribeResponse{
				Action:    "INSERT",
				Timestamp: timestamppb.Now(),
				NewValues: rowStruct,
			}); err != nil {
				return err
			}
		}
	}

	// Stream live changes.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-encodeErr:
			return connect.NewError(connect.CodeInternal, err)
		case msg := <-ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// findIndexedTarget looks up an IndexedTarget for the given schema.table.
func (s *Service) findIndexedTarget(schema, table string) (*memory.IndexedTarget, error) {
	if schema == "" || table == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema and table are required"))
	}

	tid := laredo.Table(schema, table)
	targets := s.engine.Targets("", tid)

	for _, t := range targets {
		if indexed, ok := t.(*memory.IndexedTarget); ok {
			return indexed, nil
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no indexed target found for %s", tid))
}

// protoValuesToLaredo converts proto Values to laredo Values.
func protoValuesToLaredo(values []*structpb.Value) []laredo.Value {
	result := make([]laredo.Value, len(values))
	for i, v := range values {
		result[i] = v.AsInterface()
	}
	return result
}

// rowToStruct converts a laredo.Row to a protobuf Struct. The conversion
// lives in internal/rowpb because the same row types cross the replication
// wire, and a plain structpb.NewStruct rejects the time.Time, uuid and
// numeric values pgx routinely produces.
func rowToStruct(row laredo.Row) (*structpb.Struct, error) {
	return rowpb.RowToStruct(row)
}
