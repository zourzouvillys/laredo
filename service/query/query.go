// Package query implements the Query gRPC service for looking up rows in
// in-memory targets.
package query

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/target/memory"
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

	pageSize := int(req.Msg.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
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

// rowToStruct converts a laredo.Row to a protobuf Struct.
func rowToStruct(row laredo.Row) (*structpb.Struct, error) {
	fields := make(map[string]any, len(row))
	for k, v := range row {
		fields[k] = v
	}
	return structpb.NewStruct(fields)
}
