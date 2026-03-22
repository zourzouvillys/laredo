package bench

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/internal/engine"
	"github.com/zourzouvillys/laredo/snapshot/jsonl"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// --- IndexedTarget benchmarks ---

func BenchmarkIndexedTarget_Insert(b *testing.B) {
	for _, size := range []int{100, 10_000, 100_000} {
		b.Run(fmt.Sprintf("rows=%d", size), func(b *testing.B) {
			for range b.N {
				target := memory.NewIndexedTarget(memory.LookupFields("name"))
				_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
				for i := range size {
					_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
						laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
				}
			}
		})
	}
}

func BenchmarkIndexedTarget_Lookup(b *testing.B) {
	target := memory.NewIndexedTarget(memory.LookupFields("name"))
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	for i := range 10_000 {
		_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
			laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
	}
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	b.ResetTimer()
	for i := range b.N {
		target.Lookup(fmt.Sprintf("user-%d", i%10_000))
	}
}

func BenchmarkIndexedTarget_Get(b *testing.B) {
	target := memory.NewIndexedTarget()
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	for i := range 10_000 {
		_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
			laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
	}
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	b.ResetTimer()
	for i := range b.N {
		target.Get(i % 10_000)
	}
}

// --- CompiledTarget benchmarks ---

func BenchmarkCompiledTarget_Insert(b *testing.B) {
	for range b.N {
		target := memory.NewCompiledTarget(
			memory.Compiler(func(row laredo.Row) (any, error) { return row, nil }),
			memory.KeyFields("id"),
		)
		_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
		for i := range 1000 {
			_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
				laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
		}
	}
}

// --- ChangeBuffer benchmarks ---

func BenchmarkChangeBuffer_Block(b *testing.B) {
	buf := engine.NewChangeBuffer[int](1000)
	done := make(chan struct{})
	go func() {
		for range buf.Receive() {
		}
		close(done)
	}()
	b.ResetTimer()
	for i := range b.N {
		buf.Send(i)
	}
	buf.Close()
	<-done
}

func BenchmarkChangeBuffer_TrySend(b *testing.B) {
	buf := engine.NewChangeBuffer[int](1000)
	done := make(chan struct{})
	go func() {
		for range buf.Receive() {
		}
		close(done)
	}()
	b.ResetTimer()
	for i := range b.N {
		buf.TrySend(i)
	}
	buf.Close()
	<-done
}

// --- JSONL Snapshot Serializer benchmarks ---

func BenchmarkJSONL_Write(b *testing.B) {
	serializer := jsonl.New()
	info := laredo.TableSnapshotInfo{
		Table:    testutil.SampleTable(),
		RowCount: 1000,
	}
	rows := make([]laredo.Row, 1000)
	for i := range rows {
		rows[i] = laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i), "email": fmt.Sprintf("u%d@test.com", i)}
	}

	b.ResetTimer()
	for range b.N {
		var buf devNull
		_ = serializer.Write(info, rows, &buf)
	}
}

func BenchmarkJSONL_Read(b *testing.B) {
	serializer := jsonl.New()
	info := laredo.TableSnapshotInfo{
		Table:    testutil.SampleTable(),
		RowCount: 1000,
	}
	rows := make([]laredo.Row, 1000)
	for i := range rows {
		rows[i] = laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)}
	}

	// Serialize once.
	var buf bytesBuffer
	_ = serializer.Write(info, rows, &buf)
	data := buf.Bytes()

	b.ResetTimer()
	for range b.N {
		reader := bytesReader(data)
		_, _, _ = serializer.Read(&reader)
	}
}

// devNull discards writes.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }

// bytesBuffer is a simple bytes.Buffer wrapper.
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *bytesBuffer) Bytes() []byte { return b.data }

// bytesReader wraps a byte slice as an io.Reader.
type bytesReader []byte

func (b *bytesReader) Read(p []byte) (int, error) {
	if len(*b) == 0 {
		return 0, fmt.Errorf("EOF")
	}
	n := copy(p, *b)
	*b = (*b)[n:]
	return n, nil
}

// --- Fan-out target benchmarks ---

func BenchmarkFanOut_Insert(b *testing.B) {
	for _, clients := range []int{0, 1, 10, 100} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			target := fanout.New(
				fanout.JournalMaxEntries(b.N+1000),
				fanout.HeartbeatInterval(time.Hour), // Disable heartbeat.
			)
			_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())

			// Register clients.
			for i := range clients {
				target.RegisterClient(fmt.Sprintf("client-%d", i))
			}

			// Baseline a few rows so the target has state.
			for i := range 10 {
				_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
					laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
			}
			_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

			b.ResetTimer()
			for i := range b.N {
				_ = target.OnInsert(context.Background(), testutil.SampleTable(),
					laredo.Row{"id": 1000 + i, "name": fmt.Sprintf("bench-%d", i)})
			}

			// Cleanup clients.
			for i := range clients {
				target.UnregisterClient(fmt.Sprintf("client-%d", i))
			}
		})
	}
}

func BenchmarkFanOut_JournalRead(b *testing.B) {
	target := fanout.New(fanout.JournalMaxEntries(100000))
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	// Fill journal with entries.
	for i := range 10000 {
		_ = target.OnInsert(context.Background(), testutil.SampleTable(),
			laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
	}

	b.ResetTimer()
	for range b.N {
		// Read last 100 entries — simulates a client catching up.
		seq := target.JournalSequence() - 100
		entries := target.JournalEntriesSince(seq)
		_ = entries
	}
}

func BenchmarkFanOut_Snapshot(b *testing.B) {
	target := fanout.New(fanout.SnapshotKeepCount(10))
	_ = target.OnInit(context.Background(), testutil.SampleTable(), testutil.SampleColumns())
	for i := range 1000 {
		_ = target.OnBaselineRow(context.Background(), testutil.SampleTable(),
			laredo.Row{"id": i, "name": fmt.Sprintf("user-%d", i)})
	}
	_ = target.OnBaselineComplete(context.Background(), testutil.SampleTable())

	b.ResetTimer()
	for range b.N {
		_ = target.TakeSnapshot()
	}
}
