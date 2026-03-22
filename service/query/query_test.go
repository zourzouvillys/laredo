package query_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/service/query"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func startQueryServer(t *testing.T, engine laredo.Engine) laredov1connect.LaredoQueryServiceClient {
	t.Helper()

	querySvc := query.New(engine)
	srv := service.New(
		service.WithAddress("127.0.0.1:0"),
		service.EnableQuery(querySvc),
	)

	go func() { _ = srv.Start() }()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	return laredov1connect.NewLaredoQueryServiceClient(
		http.DefaultClient,
		"http://"+srv.Addr(),
	)
}

func startedQueryEngine(t *testing.T) (laredo.Engine, *memory.IndexedTarget) {
	t.Helper()

	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	target := memory.NewIndexedTarget(
		memory.LookupFields("name"),
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(5 * time.Second) {
		t.Fatal("engine not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	return eng, target
}

func TestQuery_GetRow(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	t.Run("found", func(t *testing.T) {
		resp, err := client.GetRow(context.Background(), connect.NewRequest(&v1.GetRowRequest{
			Schema:     "public",
			Table:      "test_table",
			PrimaryKey: 1,
		}))
		if err != nil {
			t.Fatalf("GetRow: %v", err)
		}
		if !resp.Msg.GetFound() {
			t.Fatal("expected found=true")
		}
		name := resp.Msg.GetRow().GetFields()["name"].GetStringValue()
		if name != "alice" {
			t.Errorf("expected name=alice, got %s", name)
		}
	})

	t.Run("not found", func(t *testing.T) {
		resp, err := client.GetRow(context.Background(), connect.NewRequest(&v1.GetRowRequest{
			Schema:     "public",
			Table:      "test_table",
			PrimaryKey: 999,
		}))
		if err != nil {
			t.Fatalf("GetRow: %v", err)
		}
		if resp.Msg.GetFound() {
			t.Error("expected found=false")
		}
	})
}

func TestQuery_Lookup(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	t.Run("found", func(t *testing.T) {
		nameVal, _ := structpb.NewValue("alice")
		resp, err := client.Lookup(context.Background(), connect.NewRequest(&v1.LookupRequest{
			Schema:    "public",
			Table:     "test_table",
			KeyValues: []*structpb.Value{nameVal},
		}))
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if !resp.Msg.GetFound() {
			t.Fatal("expected found=true")
		}
		id := resp.Msg.GetRow().GetFields()["id"].GetNumberValue()
		if id != 1 {
			t.Errorf("expected id=1, got %v", id)
		}
	})

	t.Run("not found", func(t *testing.T) {
		nameVal, _ := structpb.NewValue("nonexistent")
		resp, err := client.Lookup(context.Background(), connect.NewRequest(&v1.LookupRequest{
			Schema:    "public",
			Table:     "test_table",
			KeyValues: []*structpb.Value{nameVal},
		}))
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if resp.Msg.GetFound() {
			t.Error("expected found=false")
		}
	})
}

func TestQuery_CountRows(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	resp, err := client.CountRows(context.Background(), connect.NewRequest(&v1.CountRowsRequest{
		Schema: "public",
		Table:  "test_table",
	}))
	if err != nil {
		t.Fatalf("CountRows: %v", err)
	}
	if resp.Msg.GetCount() != 3 {
		t.Errorf("expected count=3, got %d", resp.Msg.GetCount())
	}
}

func TestQuery_ListRows(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	t.Run("all rows", func(t *testing.T) {
		resp, err := client.ListRows(context.Background(), connect.NewRequest(&v1.ListRowsRequest{
			Schema:   "public",
			Table:    "test_table",
			PageSize: 100,
		}))
		if err != nil {
			t.Fatalf("ListRows: %v", err)
		}
		if len(resp.Msg.GetRows()) != 3 {
			t.Errorf("expected 3 rows, got %d", len(resp.Msg.GetRows()))
		}
		if resp.Msg.GetTotalCount() != 3 {
			t.Errorf("expected total_count=3, got %d", resp.Msg.GetTotalCount())
		}
	})

	t.Run("page size limit", func(t *testing.T) {
		resp, err := client.ListRows(context.Background(), connect.NewRequest(&v1.ListRowsRequest{
			Schema:   "public",
			Table:    "test_table",
			PageSize: 2,
		}))
		if err != nil {
			t.Fatalf("ListRows: %v", err)
		}
		if len(resp.Msg.GetRows()) != 2 {
			t.Errorf("expected 2 rows with page_size=2, got %d", len(resp.Msg.GetRows()))
		}
		// total_count should still be 3.
		if resp.Msg.GetTotalCount() != 3 {
			t.Errorf("expected total_count=3, got %d", resp.Msg.GetTotalCount())
		}
	})
}

func TestQuery_TableNotFound(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	_, err := client.CountRows(context.Background(), connect.NewRequest(&v1.CountRowsRequest{
		Schema: "public",
		Table:  "nonexistent",
	}))
	if err == nil {
		t.Fatal("expected error for nonexistent table")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %v", connect.CodeOf(err))
	}
}

func TestQuery_MissingTable(t *testing.T) {
	eng, _ := startedQueryEngine(t)
	client := startQueryServer(t, eng)

	_, err := client.CountRows(context.Background(), connect.NewRequest(&v1.CountRowsRequest{}))
	if err == nil {
		t.Fatal("expected error for missing schema/table")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %v", connect.CodeOf(err))
	}
}

func TestQuery_Subscribe_ReplayAndLive(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(5 * time.Second) {
		t.Fatal("engine not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	client := startQueryServer(t, eng)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *v1.SubscribeResponse, 20)
	streamErr := make(chan error, 1)
	go func() {
		stream, err := client.Subscribe(subCtx, connect.NewRequest(&v1.SubscribeRequest{
			Schema:         "public",
			Table:          "test_table",
			ReplayExisting: true,
		}))
		if err != nil {
			streamErr <- err
			return
		}
		for stream.Receive() {
			events <- stream.Msg()
		}
		streamErr <- stream.Err()
	}()

	// Wait for replay events (2 existing rows).
	timeout := time.After(5 * time.Second)
	var replayed int
	for replayed < 2 {
		select {
		case msg := <-events:
			if msg.GetAction() != "INSERT" {
				t.Errorf("replay: expected INSERT, got %s", msg.GetAction())
			}
			if msg.GetNewValues() == nil {
				t.Error("replay: expected new_values")
			}
			replayed++
		case err := <-streamErr:
			t.Fatalf("stream error during replay: %v", err)
		case <-timeout:
			t.Fatalf("timed out waiting for replay (got %d of 2)", replayed)
		}
	}

	// Emit a live change.
	time.Sleep(50 * time.Millisecond)
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(3, "charlie"))

	// Wait for live INSERT event.
	timeout = time.After(5 * time.Second)
	select {
	case msg := <-events:
		if msg.GetAction() != "INSERT" {
			t.Errorf("live: expected INSERT, got %s", msg.GetAction())
		}
		vals := msg.GetNewValues().AsMap()
		if vals["name"] != "charlie" {
			t.Errorf("live: expected name=charlie, got %v", vals["name"])
		}
	case err := <-streamErr:
		t.Fatalf("stream error waiting for live event: %v", err)
	case <-timeout:
		t.Fatal("timed out waiting for live event")
	}
}

func TestQuery_Subscribe_LiveOnly(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	target := memory.NewIndexedTarget()
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(5 * time.Second) {
		t.Fatal("engine not ready")
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	client := startQueryServer(t, eng)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan *v1.SubscribeResponse, 20)
	go func() {
		stream, err := client.Subscribe(subCtx, connect.NewRequest(&v1.SubscribeRequest{
			Schema:         "public",
			Table:          "test_table",
			ReplayExisting: false,
		}))
		if err != nil {
			return
		}
		for stream.Receive() {
			events <- stream.Msg()
		}
	}()

	// Give time for the subscription to be established.
	time.Sleep(100 * time.Millisecond)

	// Emit a live update.
	src.EmitUpdate(testutil.SampleTable(), testutil.SampleRow(1, "alice-updated"), testutil.SampleRow(1, "alice"))

	timeout := time.After(5 * time.Second)
	select {
	case msg := <-events:
		if msg.GetAction() != "UPDATE" {
			t.Errorf("expected UPDATE, got %s", msg.GetAction())
		}
	case <-timeout:
		t.Fatal("timed out waiting for live UPDATE event")
	}

	// No replay events should have been sent — just the live UPDATE.
	// Check there are no extra events.
	select {
	case msg := <-events:
		t.Errorf("unexpected extra event: %s", msg.GetAction())
	default:
		// Good — no extra events.
	}
}
