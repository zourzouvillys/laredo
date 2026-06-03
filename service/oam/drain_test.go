package oam_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/service/oam"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestOAM_DrainReplication(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	ft := fanout.New()
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), ft),
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

	svc := oam.New(eng)

	t.Run("drains all fan-out targets", func(t *testing.T) {
		resp, err := svc.DrainReplication(ctx, connect.NewRequest(&v1.DrainReplicationRequest{
			DrainDeadlineSeconds: 30,
		}))
		if err != nil {
			t.Fatalf("DrainReplication: %v", err)
		}
		if !resp.Msg.GetAccepted() || resp.Msg.GetDrainedTargets() != 1 {
			t.Fatalf("response = %+v, want accepted with 1 target", resp.Msg)
		}
		if !ft.IsDraining() {
			t.Fatal("target should be draining after DrainReplication")
		}
	})

	t.Run("rejects mismatched schema/table", func(t *testing.T) {
		_, err := svc.DrainReplication(ctx, connect.NewRequest(&v1.DrainReplicationRequest{
			Schema: "public",
		}))
		if err == nil {
			t.Fatal("expected error for schema set without table")
		}
	})
}

func TestOAM_DrainReplication_NoTargets(t *testing.T) {
	src := testsource.New()
	src.SetSchema(testutil.SampleTable(), testutil.SampleColumns())
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

	// A non-fan-out target: drain should report nothing to do.
	eng, errs := laredo.NewEngine(
		laredo.WithSource("pg", src),
		laredo.WithPipeline("pg", testutil.SampleTable(), &countingTarget{}),
	)
	if len(errs) > 0 {
		t.Fatalf("engine errors: %v", errs)
	}
	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.AwaitReady(5 * time.Second)
	t.Cleanup(func() { _ = eng.Stop(ctx) })

	resp, err := oam.New(eng).DrainReplication(ctx, connect.NewRequest(&v1.DrainReplicationRequest{}))
	if err != nil {
		t.Fatalf("DrainReplication: %v", err)
	}
	if resp.Msg.GetAccepted() {
		t.Fatal("expected accepted=false when there are no fan-out targets")
	}
}

// countingTarget is a minimal non-fan-out SyncTarget for the no-targets case.
type countingTarget struct{ n int }

func (c *countingTarget) OnInit(context.Context, laredo.TableIdentifier, []laredo.ColumnDefinition) error {
	return nil
}

func (c *countingTarget) OnBaselineRow(context.Context, laredo.TableIdentifier, laredo.Row) error {
	c.n++
	return nil
}

func (c *countingTarget) OnBaselineComplete(context.Context, laredo.TableIdentifier) error {
	return nil
}

func (c *countingTarget) OnInsert(context.Context, laredo.TableIdentifier, laredo.Row) error {
	return nil
}

func (c *countingTarget) OnUpdate(context.Context, laredo.TableIdentifier, laredo.Row, laredo.Row) error {
	return nil
}

func (c *countingTarget) OnDelete(context.Context, laredo.TableIdentifier, laredo.Row) error {
	return nil
}
func (c *countingTarget) OnTruncate(context.Context, laredo.TableIdentifier) error { return nil }
func (c *countingTarget) IsDurable() bool                                          { return true }
func (c *countingTarget) OnSchemaChange(context.Context, laredo.TableIdentifier, []laredo.ColumnDefinition, []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

func (c *countingTarget) ExportSnapshot(context.Context) ([]laredo.SnapshotEntry, error) {
	return nil, nil
}

func (c *countingTarget) RestoreSnapshot(context.Context, laredo.TableSnapshotInfo, []laredo.SnapshotEntry) error {
	return nil
}
func (c *countingTarget) SupportsConsistentSnapshot() bool                      { return false }
func (c *countingTarget) OnClose(context.Context, laredo.TableIdentifier) error { return nil }
