//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/config"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/service/oam"
	"github.com/zourzouvillys/laredo/service/query"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestFullServerStartup(t *testing.T) {
	// Start PG with test data.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 5)

	// Write HOCON config to temp file.
	hoconConfig := `
sources {
  pg {
    type = postgresql
    connection = "` + pgc.ConnStr + `"
    slot_mode = ephemeral
    slot_name = server_test_slot
  }
}

tables = [
  {
    source = pg
    schema = public
    table = test_users
    targets = [
      {
        type = indexed-memory
        lookup_fields = [name]
      }
    ]
  }
]
`
	confFile, err := os.CreateTemp(t.TempDir(), "laredo-*.conf")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	if _, err := confFile.WriteString(hoconConfig); err != nil {
		t.Fatalf("write config: %v", err)
	}
	confFile.Close()

	// Load config and start engine.
	cfg, err := config.Load(confFile.Name())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}

	opts, err := cfg.ToEngineOptions()
	if err != nil {
		t.Fatalf("engine options: %v", err)
	}

	eng, errs := laredo.NewEngine(opts...)
	if len(errs) > 0 {
		t.Fatalf("engine: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine not ready")
	}

	// Start gRPC server with OAM + Query.
	oamSvc := oam.New(eng)
	querySvc := query.New(eng)

	oamClient, queryClient := testutil.TestGRPCServer(t,
		service.EnableOAM(oamSvc),
		service.EnableQuery(querySvc),
	)

	// Test OAM GetStatus.
	statusResp, err := oamClient.GetStatus(ctx, connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(statusResp.Msg.GetPipelines()) != 1 {
		t.Errorf("expected 1 pipeline, got %d", len(statusResp.Msg.GetPipelines()))
	}

	// Test Query CountRows.
	countResp, err := queryClient.CountRows(ctx, connect.NewRequest(&v1.CountRowsRequest{
		Schema: "public",
		Table:  "test_users",
	}))
	if err != nil {
		t.Fatalf("CountRows: %v", err)
	}
	if countResp.Msg.GetCount() != 5 {
		t.Errorf("expected 5 rows, got %d", countResp.Msg.GetCount())
	}

	// Test CheckReady.
	readyResp, err := oamClient.CheckReady(ctx, connect.NewRequest(&v1.CheckReadyRequest{}))
	if err != nil {
		t.Fatalf("CheckReady: %v", err)
	}
	if !readyResp.Msg.GetReady() {
		t.Error("expected ready=true")
	}

	t.Log("full server startup test passed")
}
