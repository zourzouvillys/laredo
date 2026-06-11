package fanout_test

import (
	"context"
	"testing"

	"github.com/zourzouvillys/laredo"
	srcfanout "github.com/zourzouvillys/laredo/source/fanout"
)

// TestSource_Surface exercises the SyncSource methods that need no upstream
// connection, including the default (PostgreSQL-LSN) position comparator.
func TestSource_Surface(t *testing.T) {
	s := srcfanout.New(srcfanout.Table("public", "events"))
	ctx := context.Background()

	if errs := s.ValidateTables(ctx, []laredo.TableIdentifier{laredo.Table("public", "events")}); len(errs) != 0 {
		t.Fatalf("validate matching = %v, want none", errs)
	}
	if errs := s.ValidateTables(ctx, []laredo.TableIdentifier{laredo.Table("public", "other")}); len(errs) != 1 {
		t.Fatalf("validate mismatched = %v, want 1 error", errs)
	}

	if s.PositionToString("0/10") != "0/10" {
		t.Fatal("PositionToString identity failed")
	}
	p, err := s.PositionFromString("0/10")
	if err != nil || p.(string) != "0/10" {
		t.Fatalf("PositionFromString = %v, %v", p, err)
	}
	if s.ComparePositions("0/10", "0/20") != -1 {
		t.Fatal("ComparePositions: 0/10 should be < 0/20 (default LSN order)")
	}

	if got, _ := s.LastAckedPosition(ctx); got != nil {
		t.Fatalf("LastAckedPosition initially = %v, want nil", got)
	}
	if err := s.Ack(ctx, "0/30"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if got, _ := s.LastAckedPosition(ctx); got == nil || got.(string) != "0/30" {
		t.Fatalf("LastAckedPosition = %v, want 0/30", got)
	}

	if !s.SupportsResume() {
		t.Fatal("SupportsResume should be true")
	}
	if s.OrderingGuarantee() != laredo.TotalOrder {
		t.Fatalf("OrderingGuarantee = %v, want TotalOrder", s.OrderingGuarantee())
	}
	if s.State() != laredo.SourceConnecting {
		t.Fatalf("initial State = %v, want SourceConnecting", s.State())
	}
	_ = s.Pause(ctx)
	if s.State() != laredo.SourcePaused {
		t.Fatal("Pause should set SourcePaused")
	}
	_ = s.Resume(ctx)
	if s.State() != laredo.SourceStreaming {
		t.Fatal("Resume should set SourceStreaming")
	}
	_ = s.GetLag()
	if err := s.Close(ctx); err != nil { // client is nil before Init — must be safe
		t.Fatalf("Close: %v", err)
	}
	if s.State() != laredo.SourceClosed {
		t.Fatal("Close should set SourceClosed")
	}
}
