//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/httpsync"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestPGToHTTPSync_EndToEnd(t *testing.T) {
	// Full pipeline: PG source → engine → HTTP sync target.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 3)

	// Set up HTTP sync receiver.
	var mu sync.Mutex
	var baselineRows []json.RawMessage
	var changePayloads []json.RawMessage
	baselineStarted := false
	baselineCompleted := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()

		switch r.URL.Path {
		case "/baseline/start":
			baselineStarted = true
		case "/baseline/batch":
			var batch struct {
				Rows []json.RawMessage `json:"rows"`
			}
			_ = json.Unmarshal(body, &batch)
			baselineRows = append(baselineRows, batch.Rows...)
		case "/baseline/complete":
			baselineCompleted = true
		case "/changes":
			changePayloads = append(changePayloads, body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(1), // flush each change immediately
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotName("pg_http_test"),
			pg.Publication(pg.PublicationConfig{Name: "pg_http_pub", Create: true}),
		)),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), target),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine not ready")
	}

	// Verify baseline was sent to HTTP target.
	mu.Lock()
	if !baselineStarted {
		t.Error("expected baseline/start")
	}
	if !baselineCompleted {
		t.Error("expected baseline/complete")
	}
	if len(baselineRows) != 3 {
		t.Errorf("expected 3 baseline rows, got %d", len(baselineRows))
	}
	mu.Unlock()

	// Now stream a change via PG.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "http-user", "http@test.com")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Wait for the change to arrive at the HTTP target.
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(changePayloads) > 0
	}, "expected at least 1 change payload at HTTP target")

	mu.Lock()
	t.Logf("baseline rows: %d, change payloads: %d", len(baselineRows), len(changePayloads))
	mu.Unlock()

	if err := eng.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
