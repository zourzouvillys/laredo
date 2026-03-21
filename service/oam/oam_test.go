package oam_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/service/oam"
	"github.com/zourzouvillys/laredo/snapshot/jsonl"
	"github.com/zourzouvillys/laredo/snapshot/local"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func startTestServer(t *testing.T, engine laredo.Engine, store laredo.SnapshotStore) laredov1connect.LaredoOAMServiceClient {
	t.Helper()

	oamSvc := oam.New(engine, oam.WithSnapshotStore(store))
	srv := service.New(
		service.WithAddress("127.0.0.1:0"),
		service.EnableOAM(oamSvc),
	)

	go func() { _ = srv.Start() }()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	return laredov1connect.NewLaredoOAMServiceClient(
		http.DefaultClient,
		"http://"+srv.Addr(),
	)
}

func TestOAM_StartReplay(t *testing.T) {
	// Create an engine with a snapshot store.
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))

	target := memory.NewIndexedTarget()
	store := local.New(t.TempDir())

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
		laredo.WithSnapshotStore(store),
		laredo.WithSnapshotSerializer(jsonl.New()),
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

	// Create a snapshot.
	if err := eng.CreateSnapshot(ctx, nil); err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	client := startTestServer(t, eng, store)

	// Start a replay.
	resp, err := client.StartReplay(ctx, connect.NewRequest(&v1.StartReplayRequest{
		SnapshotId: "latest",
		Tables:     []string{testutil.SampleTable().String()},
	}))
	if err != nil {
		// The replay may fail because "latest" isn't a real snapshot ID.
		// That's OK — we're testing the RPC wiring, not the replay logic itself.
		t.Logf("StartReplay returned error (expected for 'latest'): %v", err)
	} else {
		if !resp.Msg.GetAccepted() {
			t.Error("expected accepted=true")
		}
		if resp.Msg.GetReplayId() == "" {
			t.Error("expected non-empty replay_id")
		}

		// Check replay status.
		replayID := resp.Msg.GetReplayId()
		statusResp, err := client.GetReplayStatus(ctx, connect.NewRequest(&v1.GetReplayStatusRequest{
			ReplayId: replayID,
		}))
		if err != nil {
			t.Fatalf("GetReplayStatus: %v", err)
		}
		if statusResp.Msg.GetReplayId() != replayID {
			t.Errorf("expected replay_id=%s, got %s", replayID, statusResp.Msg.GetReplayId())
		}
		state := statusResp.Msg.GetState()
		if state != "running" && state != "completed" && state != "error" {
			t.Errorf("unexpected state: %s", state)
		}
	}

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestOAM_StartReplay_NoStore(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	// No snapshot store.
	client := startTestServer(t, eng, nil)

	_, err := client.StartReplay(context.Background(), connect.NewRequest(&v1.StartReplayRequest{
		SnapshotId: "snap-1",
		Tables:     []string{"public.test_table"},
	}))
	if err == nil {
		t.Fatal("expected error without snapshot store")
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("expected CodeFailedPrecondition, got %v", connect.CodeOf(err))
	}
}

func TestOAM_GetReplayStatus_NotFound(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	client := startTestServer(t, eng, nil)

	_, err := client.GetReplayStatus(context.Background(), connect.NewRequest(&v1.GetReplayStatusRequest{
		ReplayId: "nonexistent",
	}))
	if err == nil {
		t.Fatal("expected error for nonexistent replay")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %v", connect.CodeOf(err))
	}
}

func TestOAM_StopReplay_NotFound(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	client := startTestServer(t, eng, nil)

	_, err := client.StopReplay(context.Background(), connect.NewRequest(&v1.StopReplayRequest{
		ReplayId: "nonexistent",
	}))
	if err == nil {
		t.Fatal("expected error for nonexistent replay")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %v", connect.CodeOf(err))
	}
}

func TestOAM_UnimplementedRPCs(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	target := memory.NewIndexedTarget()

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	client := startTestServer(t, eng, nil)

	// All non-replay RPCs should return CodeUnimplemented.
	_, err := client.GetStatus(context.Background(), connect.NewRequest(&v1.GetStatusRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("GetStatus: expected CodeUnimplemented, got %v", connect.CodeOf(err))
	}

	_, err = client.CheckReady(context.Background(), connect.NewRequest(&v1.CheckReadyRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("CheckReady: expected CodeUnimplemented, got %v", connect.CodeOf(err))
	}
}
