package main

import (
	"context"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

func TestDrainFanoutTargets(t *testing.T) {
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

	n := drainFanoutTargets(eng, time.Now().Add(time.Minute))
	if n != 1 {
		t.Fatalf("drained %d targets, want 1", n)
	}
	if !ft.IsDraining() {
		t.Fatal("fan-out target should be draining")
	}
}
