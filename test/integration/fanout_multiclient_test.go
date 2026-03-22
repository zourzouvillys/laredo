//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestFanOut_MultiClient(t *testing.T) {
	// Tests that multiple fan-out clients receive consistent state and live updates.
	pgc := testutil.NewTestPostgres(t)
	pgc.CreateTestTable(t)
	pgc.InsertTestRows(t, 3)

	ctx := context.Background()

	fanoutTarget := fanout.New(
		fanout.JournalMaxEntries(10000),
		fanout.SnapshotKeepCount(5),
		fanout.HeartbeatInterval(500*time.Millisecond),
		fanout.MaxClients(10),
	)

	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", pg.New(
			pg.Connection(pgc.ConnStr),
			pg.SlotName("fanout_multi_slot"),
			pg.Publication(pg.PublicationConfig{Name: "fanout_multi_pub", Create: true}),
		)),
		laredo.WithPipeline("pg", laredo.Table("public", "test_users"), fanoutTarget),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}

	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	if !eng.AwaitReady(30 * time.Second) {
		t.Fatal("engine not ready")
	}

	// Verify baseline data.
	if fanoutTarget.Count() != 3 {
		t.Fatalf("expected 3 rows, got %d", fanoutTarget.Count())
	}

	// Register multiple clients and verify they all see the same state.
	const numClients = 5
	for i := range numClients {
		clientID := clientName(i)
		if !fanoutTarget.RegisterClient(clientID) {
			t.Fatalf("failed to register client %s", clientID)
		}
	}
	t.Cleanup(func() {
		for i := range numClients {
			fanoutTarget.UnregisterClient(clientName(i))
		}
	})

	if fanoutTarget.ConnectedClients() != numClients {
		t.Errorf("expected %d connected clients, got %d", numClients, fanoutTarget.ConnectedClients())
	}

	// Take a snapshot — all clients should see the same data.
	snap := fanoutTarget.TakeSnapshot()
	if snap.RowCount != 3 {
		t.Errorf("snapshot: expected 3 rows, got %d", snap.RowCount)
	}

	// Stream a change via PG.
	conn, err := pgx.Connect(ctx, pgc.ConnStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)", "multi-user", "multi@test.com")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Wait for the change to arrive.
	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return fanoutTarget.Count() == 4
	}, "expected 4 rows after insert")

	// All clients should see the new journal entry.
	entries := fanoutTarget.JournalEntriesSince(snap.Sequence)
	if len(entries) < 1 {
		t.Fatalf("expected at least 1 journal entry, got %d", len(entries))
	}

	// Simulate clients catching up by updating their sequences.
	latestSeq := fanoutTarget.JournalSequence()
	var wg sync.WaitGroup
	for i := range numClients {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cid := clientName(idx)
			fanoutTarget.UpdateClientSequence(cid, latestSeq)
			fanoutTarget.SetClientState(cid, "live")
		}(i)
	}
	wg.Wait()

	// Verify all clients are at the same sequence.
	for _, ci := range fanoutTarget.ClientList() {
		if ci.CurrentSequence != latestSeq {
			t.Errorf("client %s at seq %d, expected %d", ci.ID, ci.CurrentSequence, latestSeq)
		}
		if ci.State != "live" {
			t.Errorf("client %s state=%s, expected live", ci.ID, ci.State)
		}
	}

	// Send more changes and verify journal grows.
	for i := range 5 {
		name := fmt.Sprintf("batch-user-%d", i)
		_, err = conn.Exec(ctx, "INSERT INTO test_users (name, email) VALUES ($1, $2)",
			name, "batch@test.com")
		if err != nil {
			t.Fatalf("batch insert %d: %v", i, err)
		}
	}

	testutil.AssertEventually(t, 15*time.Second, func() bool {
		return fanoutTarget.Count() == 9
	}, "expected 9 rows after batch insert")

	// Verify journal has entries for all changes.
	newEntries := fanoutTarget.JournalEntriesSince(latestSeq)
	if len(newEntries) < 5 {
		t.Errorf("expected at least 5 journal entries, got %d", len(newEntries))
	}

	// Unregister half the clients.
	for i := range numClients / 2 {
		fanoutTarget.UnregisterClient(clientName(i))
	}
	if fanoutTarget.ConnectedClients() != numClients-numClients/2 {
		t.Errorf("expected %d clients after unregister, got %d",
			numClients-numClients/2, fanoutTarget.ConnectedClients())
	}

	t.Logf("multi-client test passed: %d rows, %d journal entries, %d/%d clients remaining",
		fanoutTarget.Count(), fanoutTarget.JournalLen(),
		fanoutTarget.ConnectedClients(), numClients)
}

func clientName(i int) string {
	return "test-client-" + string(rune('A'+i))
}
