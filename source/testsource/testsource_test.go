package testsource_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestSource_SetSchemaAndInit(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	cols := testutil.SampleColumns()
	src.SetSchema(table, cols)

	schemas, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	got, ok := schemas[table]
	if !ok {
		t.Fatalf("Init did not return schema for %s", table)
	}
	if len(got) != len(cols) {
		t.Fatalf("expected %d columns, got %d", len(cols), len(got))
	}
	for i, c := range got {
		if c.Name != cols[i].Name {
			t.Errorf("column %d: expected name %q, got %q", i, cols[i].Name, c.Name)
		}
		if c.Type != cols[i].Type {
			t.Errorf("column %d: expected type %q, got %q", i, cols[i].Type, c.Type)
		}
		if c.PrimaryKey != cols[i].PrimaryKey {
			t.Errorf("column %d: expected PrimaryKey=%v, got %v", i, cols[i].PrimaryKey, c.PrimaryKey)
		}
	}
}

func TestSource_InitUnconfiguredTable(t *testing.T) {
	src := testsource.New()
	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{
			laredo.Table("public", "nonexistent"),
		},
	})
	if err == nil {
		t.Fatal("expected error for unconfigured table, got nil")
	}
}

func TestSource_ValidateTables(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	unknown := laredo.Table("public", "missing")

	t.Run("configured table passes", func(t *testing.T) {
		errs := src.ValidateTables(context.Background(), []laredo.TableIdentifier{table})
		if len(errs) != 0 {
			t.Errorf("expected no validation errors for configured table, got %d", len(errs))
		}
	})

	t.Run("unconfigured table fails", func(t *testing.T) {
		errs := src.ValidateTables(context.Background(), []laredo.TableIdentifier{unknown})
		if len(errs) != 1 {
			t.Fatalf("expected 1 validation error, got %d", len(errs))
		}
		if errs[0].Code != "TABLE_NOT_FOUND" {
			t.Errorf("expected code TABLE_NOT_FOUND, got %q", errs[0].Code)
		}
		if errs[0].Table == nil || *errs[0].Table != unknown {
			t.Error("expected validation error to reference the unknown table")
		}
	})

	t.Run("mixed tables", func(t *testing.T) {
		errs := src.ValidateTables(context.Background(), []laredo.TableIdentifier{table, unknown})
		if len(errs) != 1 {
			t.Fatalf("expected 1 validation error, got %d", len(errs))
		}
	})
}

func TestSource_Baseline(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())
	src.AddRow(table, testutil.SampleRow(1, "alice"))
	src.AddRow(table, testutil.SampleRow(2, "bob"))

	var received []laredo.Row
	pos, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, row laredo.Row) {
		received = append(received, row)
	})
	if err != nil {
		t.Fatalf("Baseline returned error: %v", err)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(received))
	}
	if received[0].GetString("name") != "alice" {
		t.Errorf("row 0: expected name=alice, got %q", received[0].GetString("name"))
	}
	if received[1].GetString("name") != "bob" {
		t.Errorf("row 1: expected name=bob, got %q", received[1].GetString("name"))
	}

	posVal, ok := pos.(uint64)
	if !ok {
		t.Fatalf("expected uint64 position, got %T", pos)
	}
	if posVal == 0 {
		t.Error("expected non-zero baseline position")
	}
}

func TestSource_BaselineMultipleTables(t *testing.T) {
	src := testsource.New()
	table1 := laredo.Table("public", "users")
	table2 := laredo.Table("public", "orders")

	src.SetSchema(table1, testutil.SampleColumns())
	src.SetSchema(table2, testutil.SampleColumns())

	src.AddRow(table1, testutil.SampleRow(1, "alice"))
	src.AddRow(table2, testutil.SampleRow(10, "order-a"))
	src.AddRow(table2, testutil.SampleRow(11, "order-b"))

	rowsByTable := make(map[laredo.TableIdentifier][]laredo.Row)
	pos, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table1, table2}, func(table laredo.TableIdentifier, row laredo.Row) {
		rowsByTable[table] = append(rowsByTable[table], row)
	})
	if err != nil {
		t.Fatalf("Baseline returned error: %v", err)
	}

	if len(rowsByTable[table1]) != 1 {
		t.Errorf("expected 1 row for table1, got %d", len(rowsByTable[table1]))
	}
	if len(rowsByTable[table2]) != 2 {
		t.Errorf("expected 2 rows for table2, got %d", len(rowsByTable[table2]))
	}

	posVal, ok := pos.(uint64)
	if !ok {
		t.Fatalf("expected uint64 position, got %T", pos)
	}
	if posVal == 0 {
		t.Error("expected non-zero baseline position")
	}
}

func TestSource_Stream(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []laredo.ChangeEvent

	handler := laredo.ChangeHandlerFunc(func(event laredo.ChangeEvent) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	})

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- src.Stream(ctx, nil, handler)
	}()

	// Give Stream time to start its select loop.
	time.Sleep(10 * time.Millisecond)

	// Emit changes from a separate goroutine.
	go func() {
		src.EmitInsert(table, testutil.SampleRow(1, "alice"))
		src.EmitUpdate(table, testutil.SampleRow(1, "alice-updated"), testutil.SampleRow(1, "alice"))
		src.EmitDelete(table, laredo.Row{"id": 1})
		src.EmitTruncate(table)
	}()

	testutil.AssertEventually(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 4
	}, "expected 4 events")

	cancel()
	<-streamDone

	mu.Lock()
	defer mu.Unlock()

	if events[0].Action != laredo.ActionInsert {
		t.Errorf("event 0: expected INSERT, got %s", events[0].Action)
	}
	if events[1].Action != laredo.ActionUpdate {
		t.Errorf("event 1: expected UPDATE, got %s", events[1].Action)
	}
	if events[2].Action != laredo.ActionDelete {
		t.Errorf("event 2: expected DELETE, got %s", events[2].Action)
	}
	if events[3].Action != laredo.ActionTruncate {
		t.Errorf("event 3: expected TRUNCATE, got %s", events[3].Action)
	}

	// Verify monotonically increasing positions.
	for i := 1; i < len(events); i++ {
		prev := events[i-1].Position.(uint64)
		curr := events[i].Position.(uint64)
		if curr <= prev {
			t.Errorf("positions not monotonic: event %d pos=%d, event %d pos=%d", i-1, prev, i, curr)
		}
	}
}

func TestSource_StreamContextCancel(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	ctx, cancel := context.WithCancel(context.Background())

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- src.Stream(ctx, nil, laredo.ChangeHandlerFunc(func(_ laredo.ChangeEvent) error {
			return nil
		}))
	}()

	// Cancel and verify Stream returns context error.
	cancel()

	select {
	case err := <-streamDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after context cancellation")
	}
}

func TestSource_AckAndResume(t *testing.T) {
	src := testsource.New()

	// Before any ACK, SupportsResume is false and LastAckedPosition is nil.
	if src.SupportsResume() {
		t.Error("expected SupportsResume=false before any ACK")
	}

	pos, err := src.LastAckedPosition(context.Background())
	if err != nil {
		t.Fatalf("LastAckedPosition: %v", err)
	}
	if pos != nil {
		t.Errorf("expected nil position before ACK, got %v", pos)
	}

	// ACK a position.
	if err := src.Ack(context.Background(), uint64(5)); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	if !src.SupportsResume() {
		t.Error("expected SupportsResume=true after ACK")
	}

	pos, err = src.LastAckedPosition(context.Background())
	if err != nil {
		t.Fatalf("LastAckedPosition: %v", err)
	}

	posVal, ok := pos.(uint64)
	if !ok {
		t.Fatalf("expected uint64 position, got %T", pos)
	}
	if posVal != 5 {
		t.Errorf("expected last ACKed position=5, got %d", posVal)
	}

	// ACK a higher position.
	if err := src.Ack(context.Background(), uint64(10)); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	pos, _ = src.LastAckedPosition(context.Background())
	if pos.(uint64) != 10 {
		t.Errorf("expected last ACKed position=10, got %v", pos)
	}
}

func TestSource_ComparePositions(t *testing.T) {
	src := testsource.New()

	tests := []struct {
		name string
		a, b uint64
		want int
	}{
		{"equal", 5, 5, 0},
		{"less", 3, 7, -1},
		{"greater", 10, 2, 1},
		{"zero vs nonzero", 0, 1, -1},
		{"both zero", 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := src.ComparePositions(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("ComparePositions(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSource_PositionRoundTrip(t *testing.T) {
	src := testsource.New()

	tests := []uint64{0, 1, 42, 18446744073709551615}
	for _, pos := range tests {
		str := src.PositionToString(pos)
		got, err := src.PositionFromString(str)
		if err != nil {
			t.Fatalf("PositionFromString(%q): %v", str, err)
		}
		if got.(uint64) != pos {
			t.Errorf("round-trip failed: started with %d, got %d", pos, got.(uint64))
		}
	}
}

func TestSource_PositionFromStringInvalid(t *testing.T) {
	src := testsource.New()
	_, err := src.PositionFromString("not-a-number")
	if err == nil {
		t.Error("expected error for invalid position string, got nil")
	}
}

func TestSource_StateTransitions(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	// Initial state: SourceClosed.
	if got := src.State(); got != laredo.SourceClosed {
		t.Errorf("initial state: expected SourceClosed, got %d", got)
	}

	// After Init: SourceConnected.
	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := src.State(); got != laredo.SourceConnected {
		t.Errorf("after Init: expected SourceConnected, got %d", got)
	}

	// During Stream: SourceStreaming.
	ctx, cancel := context.WithCancel(context.Background())
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- src.Stream(ctx, nil, laredo.ChangeHandlerFunc(func(_ laredo.ChangeEvent) error {
			return nil
		}))
	}()

	testutil.AssertEventually(t, time.Second, func() bool {
		return src.State() == laredo.SourceStreaming
	}, "expected SourceStreaming during Stream")

	// After cancel: SourceClosed.
	cancel()
	<-streamDone

	if got := src.State(); got != laredo.SourceClosed {
		t.Errorf("after cancel: expected SourceClosed, got %d", got)
	}
}

func TestSource_ErrorInjection(t *testing.T) {
	t.Run("init error", func(t *testing.T) {
		src := testsource.New()
		table := testutil.SampleTable()
		src.SetSchema(table, testutil.SampleColumns())

		injected := errors.New("connection refused")
		src.SetInitError(injected)

		_, err := src.Init(context.Background(), laredo.SourceConfig{
			Tables: []laredo.TableIdentifier{table},
		})
		if !errors.Is(err, injected) {
			t.Errorf("expected injected error, got %v", err)
		}
	})

	t.Run("baseline error", func(t *testing.T) {
		src := testsource.New()
		table := testutil.SampleTable()
		src.SetSchema(table, testutil.SampleColumns())

		injected := errors.New("snapshot failed")
		src.SetBaselineError(injected)

		_, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, _ laredo.Row) {
			t.Error("callback should not be called when baseline error is injected")
		})
		if !errors.Is(err, injected) {
			t.Errorf("expected injected error, got %v", err)
		}
	})
}

func TestSource_PauseResume(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- src.Stream(ctx, nil, laredo.ChangeHandlerFunc(func(_ laredo.ChangeEvent) error {
			return nil
		}))
	}()

	testutil.AssertEventually(t, time.Second, func() bool {
		return src.State() == laredo.SourceStreaming
	}, "expected SourceStreaming")

	// Pause.
	if err := src.Pause(context.Background()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got := src.State(); got != laredo.SourcePaused {
		t.Errorf("after Pause: expected SourcePaused, got %d", got)
	}

	// Resume.
	if err := src.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := src.State(); got != laredo.SourceStreaming {
		t.Errorf("after Resume: expected SourceStreaming, got %d", got)
	}

	cancel()
	<-streamDone
}

func TestSource_Close(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{table},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- src.Stream(context.Background(), nil, laredo.ChangeHandlerFunc(func(_ laredo.ChangeEvent) error {
			return nil
		}))
	}()

	testutil.AssertEventually(t, time.Second, func() bool {
		return src.State() == laredo.SourceStreaming
	}, "expected SourceStreaming")

	// Close should unblock Stream.
	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-streamDone:
		if err != nil {
			t.Errorf("expected nil error from Stream after Close, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after Close")
	}

	if got := src.State(); got != laredo.SourceClosed {
		t.Errorf("after Close: expected SourceClosed, got %d", got)
	}

	// Double close should not panic.
	if err := src.Close(context.Background()); err != nil {
		t.Errorf("double Close: expected no error, got %v", err)
	}
}

func TestSource_OrderingGuarantee(t *testing.T) {
	src := testsource.New()
	if got := src.OrderingGuarantee(); got != laredo.TotalOrder {
		t.Errorf("expected TotalOrder, got %d", got)
	}
}

func TestSource_GetLag(t *testing.T) {
	src := testsource.New()
	lag := src.GetLag()
	if lag.LagBytes != 0 {
		t.Errorf("expected LagBytes=0, got %d", lag.LagBytes)
	}
	if lag.LagTime != nil {
		t.Errorf("expected LagTime=nil, got %v", lag.LagTime)
	}
}

func TestSource_BaselinePosition(t *testing.T) {
	src := testsource.New()
	table := testutil.SampleTable()
	src.SetSchema(table, testutil.SampleColumns())

	// First baseline returns position 1.
	pos1, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, _ laredo.Row) {})
	if err != nil {
		t.Fatalf("Baseline 1: %v", err)
	}

	// Second baseline returns a higher position.
	pos2, err := src.Baseline(context.Background(), []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, _ laredo.Row) {})
	if err != nil {
		t.Fatalf("Baseline 2: %v", err)
	}

	if pos1.(uint64) >= pos2.(uint64) {
		t.Errorf("expected second baseline position (%d) > first (%d)", pos2.(uint64), pos1.(uint64))
	}
}

func TestSource_AckInvalidType(t *testing.T) {
	src := testsource.New()
	err := src.Ack(context.Background(), "not-a-uint64")
	if err == nil {
		t.Error("expected error for invalid position type, got nil")
	}
}

func TestSource_PositionToStringInvalidType(t *testing.T) {
	src := testsource.New()
	got := src.PositionToString("not-a-uint64")
	if got != "" {
		t.Errorf("expected empty string for invalid position type, got %q", got)
	}
}
