package replication

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// TestGetReplicationStatus_DistinguishesSentFromApplied is the reason Sync is
// bidirectional. Server-side status could only ever report what had been sent
// to a client, and "sent" is not "applied" — the client still has to decode
// the row and install it. An operator asking whether a change has reached the
// fleet needs the second answer, and reporting the first as though it were the
// second is worse than reporting nothing at all.
func TestGetReplicationStatus_DistinguishesSentFromApplied(t *testing.T) {
	client, _, _ := startReplService(t)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := client.Sync(ctx)
	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Start{
		Start: &v1.SyncStart{
			Schema:   tbl.Schema,
			Table:    tbl.Table,
			ClientId: "applied-probe",
		},
	}}); err != nil {
		t.Fatalf("send start: %v", err)
	}

	// Read until the snapshot completes so the server has certainly sent us
	// something and registered the session.
	for {
		msg, err := stream.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if msg.GetSnapshotEnd() != nil {
			break
		}
	}

	// Before any ack, the client is connected and has been sent data, but has
	// reported applying nothing. That distinction is the whole feature.
	before := clientStatus(t, client, tbl, "applied-probe")
	if before == nil {
		t.Fatal("client not listed in GetReplicationStatus")
	}
	if before.GetAppliedSequence() != 0 || before.GetAppliedSourcePosition() != "" {
		t.Errorf("applied state = (%d, %q) before any ack, want zero",
			before.GetAppliedSequence(), before.GetAppliedSourcePosition())
	}
	if before.GetLastAckAt() != nil {
		t.Error("last_ack_at set before any ack")
	}

	// Now acknowledge.
	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Ack{
		Ack: &v1.ApplyAck{
			AppliedSequence:       7,
			AppliedSourcePosition: "0/CAFE",
			AppliedGeneration:     "sha256:abc",
		},
	}}); err != nil {
		t.Fatalf("send ack: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := clientStatus(t, client, tbl, "applied-probe")
		if got != nil && got.GetAppliedSequence() == 7 {
			if got.GetAppliedSourcePosition() != "0/CAFE" {
				t.Errorf("applied_source_position = %q, want 0/CAFE", got.GetAppliedSourcePosition())
			}
			if got.GetAppliedGeneration() != "sha256:abc" {
				t.Errorf("applied_generation = %q, want sha256:abc", got.GetAppliedGeneration())
			}
			if got.GetLastAckAt() == nil {
				t.Error("last_ack_at not set after an ack")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("applied state never reached the server's status view")
}

// TestApplyAck_CarriesApplyError covers the other half: a client that is
// connected and receiving but cannot apply what it gets must be visibly
// broken, not silently counted as healthy.
func TestApplyAck_CarriesApplyError(t *testing.T) {
	client, _, _ := startReplService(t)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := client.Sync(ctx)
	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Start{
		Start: &v1.SyncStart{Schema: tbl.Schema, Table: tbl.Table, ClientId: "broken"},
	}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	for {
		msg, err := stream.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if msg.GetSnapshotEnd() != nil {
			break
		}
	}

	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Ack{
		Ack: &v1.ApplyAck{ApplyError: "decode column \"created_at\": unsupported type"},
	}}); err != nil {
		t.Fatalf("send ack: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := clientStatus(t, client, tbl, "broken"); got != nil && got.GetApplyError() != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("apply_error never surfaced in GetReplicationStatus")
}

// clientStatus returns the ConnectedClient entry for id, or nil.
func clientStatus(t *testing.T, client replicationv1connect.LaredoReplicationServiceClient, tbl laredo.TableIdentifier, id string) *v1.ConnectedClient {
	t.Helper()
	resp, err := client.GetReplicationStatus(context.Background(),
		connect.NewRequest(&v1.GetReplicationStatusRequest{
			Schema: tbl.Schema, Table: tbl.Table,
		}))
	if err != nil {
		t.Fatalf("GetReplicationStatus: %v", err)
	}
	for _, c := range resp.Msg.GetClients() {
		if c.GetClientId() == id {
			return c
		}
	}
	return nil
}
