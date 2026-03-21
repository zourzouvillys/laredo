package httpsync_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/target/httpsync"
	"github.com/zourzouvillys/laredo/test/testutil"
)

type requestLog struct {
	mu       sync.Mutex
	requests []loggedRequest
}

type loggedRequest struct {
	Method string
	Path   string
	Body   json.RawMessage
}

func (l *requestLog) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l.mu.Lock()
		l.requests = append(l.requests, loggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   body,
		})
		l.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (l *requestLog) byPath(path string) []loggedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []loggedRequest
	for _, r := range l.requests {
		if r.Path == path {
			result = append(result, r)
		}
	}
	return result
}

func TestTarget_BaselineFlow(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(log.handler())
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(2),
	)

	ctx := context.Background()
	table := testutil.SampleTable()
	cols := testutil.SampleColumns()

	// Init should POST baseline/start.
	if err := target.OnInit(ctx, table, cols); err != nil {
		t.Fatalf("OnInit: %v", err)
	}
	starts := log.byPath("/baseline/start")
	if len(starts) != 1 {
		t.Fatalf("expected 1 baseline/start, got %d", len(starts))
	}

	// Add 3 rows with batch_size=2: should flush at 2, then 1 on complete.
	for i := range 3 {
		if err := target.OnBaselineRow(ctx, table, testutil.SampleRow(i+1, "row")); err != nil {
			t.Fatalf("OnBaselineRow: %v", err)
		}
	}

	batches := log.byPath("/baseline/batch")
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch flush (2 rows), got %d", len(batches))
	}

	// Complete should flush remaining 1 row + post complete.
	if err := target.OnBaselineComplete(ctx, table); err != nil {
		t.Fatalf("OnBaselineComplete: %v", err)
	}

	batches = log.byPath("/baseline/batch")
	if len(batches) != 2 {
		t.Fatalf("expected 2 batch flushes after complete, got %d", len(batches))
	}

	completes := log.byPath("/baseline/complete")
	if len(completes) != 1 {
		t.Fatalf("expected 1 baseline/complete, got %d", len(completes))
	}

	if !target.IsDurable() {
		t.Error("expected IsDurable=true after successful baseline")
	}
}

func TestTarget_ChangesBatching(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(log.handler())
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(3),
	)

	ctx := context.Background()
	table := testutil.SampleTable()

	// Emit 5 changes: should flush at 3, then 2 remain buffered.
	for i := range 5 {
		if err := target.OnInsert(ctx, table, testutil.SampleRow(i+1, "row")); err != nil {
			t.Fatalf("OnInsert: %v", err)
		}
	}

	changes := log.byPath("/changes")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change flush (3 items), got %d", len(changes))
	}

	// Verify the batch contains 3 entries.
	var batch []json.RawMessage
	if err := json.Unmarshal(changes[0].Body, &batch); err != nil {
		t.Fatalf("unmarshal change batch: %v", err)
	}
	if len(batch) != 3 {
		t.Errorf("expected 3 entries in batch, got %d", len(batch))
	}

	// Close should flush remaining 2.
	if err := target.OnClose(ctx, table); err != nil {
		t.Fatalf("OnClose: %v", err)
	}

	changes = log.byPath("/changes")
	if len(changes) != 2 {
		t.Fatalf("expected 2 change flushes after close, got %d", len(changes))
	}
}

func TestTarget_ChangeTypes(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(log.handler())
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(10),
	)

	ctx := context.Background()
	table := testutil.SampleTable()

	// Insert, update, delete.
	_ = target.OnInsert(ctx, table, laredo.Row{"id": 1, "name": "alice"})
	_ = target.OnUpdate(ctx, table, laredo.Row{"id": 1, "name": "alice2"}, laredo.Row{"id": 1})
	_ = target.OnDelete(ctx, table, laredo.Row{"id": 1})

	// Truncate flushes buffer first then sends truncate.
	_ = target.OnTruncate(ctx, table)

	changes := log.byPath("/changes")
	if len(changes) != 2 {
		t.Fatalf("expected 2 change requests (buffer flush + truncate), got %d", len(changes))
	}

	// First batch should have 3 entries (insert, update, delete).
	var batch1 []map[string]any
	if err := json.Unmarshal(changes[0].Body, &batch1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(batch1) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(batch1))
	}
	if batch1[0]["action"] != "INSERT" {
		t.Errorf("expected INSERT, got %v", batch1[0]["action"])
	}
	if batch1[1]["action"] != "UPDATE" {
		t.Errorf("expected UPDATE, got %v", batch1[1]["action"])
	}
	if batch1[2]["action"] != "DELETE" {
		t.Errorf("expected DELETE, got %v", batch1[2]["action"])
	}

	// Second should be truncate.
	var batch2 []map[string]any
	if err := json.Unmarshal(changes[1].Body, &batch2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(batch2) != 1 || batch2[0]["action"] != "TRUNCATE" {
		t.Errorf("expected TRUNCATE entry, got %v", batch2)
	}
}

func TestTarget_RetryOnFailure(t *testing.T) {
	attempts := 0
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()

		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(1),
		httpsync.RetryCount(3),
	)

	ctx := context.Background()
	table := testutil.SampleTable()

	// Should succeed after 2 failures + 1 success.
	if err := target.OnInsert(ctx, table, testutil.SampleRow(1, "alice")); err != nil {
		t.Fatalf("OnInsert with retry: %v", err)
	}

	mu.Lock()
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	mu.Unlock()

	if !target.IsDurable() {
		t.Error("expected IsDurable=true after successful retry")
	}
}

func TestTarget_RetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.BatchSize(1),
		httpsync.RetryCount(1),
	)

	ctx := context.Background()
	table := testutil.SampleTable()

	err := target.OnInsert(ctx, table, testutil.SampleRow(1, "alice"))
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}

	if target.IsDurable() {
		t.Error("expected IsDurable=false after failure")
	}
}

func TestTarget_AuthAndHeaders(t *testing.T) {
	var receivedAuth string
	var receivedCustom string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.AuthHeader("Bearer secret-token"),
		httpsync.Headers(map[string]string{"X-Custom": "value123"}),
	)

	ctx := context.Background()
	table := testutil.SampleTable()
	cols := testutil.SampleColumns()

	if err := target.OnInit(ctx, table, cols); err != nil {
		t.Fatalf("OnInit: %v", err)
	}

	if receivedAuth != "Bearer secret-token" {
		t.Errorf("expected auth header, got %q", receivedAuth)
	}
	if receivedCustom != "value123" {
		t.Errorf("expected custom header, got %q", receivedCustom)
	}
}

func TestTarget_Snapshot(t *testing.T) {
	target := httpsync.New(httpsync.BaseURL("http://unused"))

	// Stateless target — export returns empty.
	entries, err := target.ExportSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty snapshot, got %d entries", len(entries))
	}

	// Restore is a no-op.
	if err := target.RestoreSnapshot(context.Background(), laredo.TableSnapshotInfo{}, nil); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
}

func TestTarget_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	target := httpsync.New(
		httpsync.BaseURL(srv.URL),
		httpsync.Timeout(50*time.Millisecond),
		httpsync.RetryCount(0),
	)

	ctx := context.Background()
	table := testutil.SampleTable()
	cols := testutil.SampleColumns()

	err := target.OnInit(ctx, table, cols)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
