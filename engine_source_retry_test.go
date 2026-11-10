package laredo_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// fastRetry keeps the retry tests quick. The defaults are seconds.
var fastRetry = laredo.WithSourceRetry(laredo.SourceRetry{
	InitialBackoff: 10 * time.Millisecond,
	MaxBackoff:     40 * time.Millisecond,
})

func startRetryEngine(t *testing.T, src *testsource.Source, target laredo.SyncTarget, obs *testutil.TestObserver, opts ...laredo.Option) laredo.Engine {
	t.Helper()
	all := append([]laredo.Option{
		laredo.WithSource("src", src),
		laredo.WithPipeline("src", testutil.SampleTable(), target),
		laredo.WithObserver(obs),
		fastRetry,
	}, opts...)
	e, errs := laredo.NewEngine(all...)
	if len(errs) > 0 {
		t.Fatalf("new engine: %v", errs)
	}
	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(ctx) })
	return e
}

// disconnectedWith reports whether any SourceDisconnected reason contains all
// of parts.
func disconnectedWith(obs *testutil.TestObserver, parts ...string) bool {
	for _, ev := range obs.EventsByType("SourceDisconnected") {
		r, _ := ev.Data["reason"].(string)
		ok := true
		for _, p := range parts {
			if !strings.Contains(r, p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// assertStreams proves the pipeline is live, not merely re-baselined: a change
// emitted now has to arrive through the stream.
func assertStreams(t *testing.T, e laredo.Engine, src *testsource.Source, target *memory.IndexedTarget, id int, name string) {
	t.Helper()
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return e.IsSourceReady("src")
	}, "source did not return to streaming")
	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(id, name))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		_, ok := target.Get(id)
		return ok
	}, "a change emitted after recovery did not reach the target")
}

// TestEngine_SourceRetry_baselineFailsAfterReBaseline: the
// stream is lost, the source asks for a re-baseline, and the re-baseline fails
// because the database is still restarting. The engine used to end the source
// there and leave the pipeline frozen for good. It must keep the rows it held,
// retry, and stream again once the database is back.
func TestEngine_SourceRetry_baselineFailsAfterReBaseline(t *testing.T) {
	src := configuredSource()
	src.SetNoResume(true) // like a PostgreSQL source on an ephemeral slot
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}
	e := startRetryEngine(t, src, target, obs)

	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	src.SetBaselineError(errors.New("begin transaction: conn closed"))
	src.FailStream(laredo.ErrReBaselineRequired)

	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return src.InitCount() >= 3
	}, "engine stopped retrying while the baseline kept failing")
	if !disconnectedWith(obs, "baseline failed", "conn closed", "retrying in") {
		t.Errorf("expected a disconnect naming the baseline failure and the retry, got %v", obs.EventsByType("SourceDisconnected"))
	}

	// Stale, not empty: the target kept what it had through the outage.
	if _, ok := target.Get(1); !ok || target.Count() != 1 {
		t.Fatalf("target lost its rows while the source was failing: count=%d", target.Count())
	}

	src.SetBaselineError(nil)
	assertStreams(t, e, src, target, 2, "bob")
}

// TestEngine_SourceRetry_streamError covers a stream that fails without asking
// for a re-baseline, such as a stateful source that ran out of reconnects.
func TestEngine_SourceRetry_streamError(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}
	e := startRetryEngine(t, src, target, obs)

	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}
	src.FailStream(errors.New("exhausted 10 reconnect attempts"))

	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return src.InitCount() >= 2
	}, "engine did not re-init the source after a stream error")
	if !disconnectedWith(obs, "stream error", "exhausted 10 reconnect attempts", "retrying in") {
		t.Errorf("expected a stream-error disconnect with a retry, got %v", obs.EventsByType("SourceDisconnected"))
	}
	assertStreams(t, e, src, target, 2, "bob")

	// Targets are initialised once; a retry must not run OnInit again.
	if n := obs.EventCount("SourceConnected"); n < 2 {
		t.Errorf("expected a SourceConnected per successful Init, got %d", n)
	}
}

// TestEngine_SourceRetry_initFailsAtBoot keeps retrying an Init that fails, and
// does not report ready until a baseline has actually completed — a host that
// gates boot on AwaitReady still fails its boot.
func TestEngine_SourceRetry_initFailsAtBoot(t *testing.T) {
	src := configuredSource()
	src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	src.SetInitError(errors.New("connection refused"))
	target := memory.NewIndexedTarget()
	obs := &testutil.TestObserver{}
	e := startRetryEngine(t, src, target, obs)

	if e.AwaitReady(200 * time.Millisecond) {
		t.Fatal("ready while Init was failing")
	}
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return src.InitCount() >= 3
	}, "engine stopped retrying a failing Init")

	src.SetInitError(nil)
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("not ready after Init recovered")
	}
	if _, ok := target.Get(1); !ok {
		t.Fatal("baseline row missing after Init recovered")
	}
	assertStreams(t, e, src, target, 2, "bob")
}

// rejectingTarget rejects every insert, to drive a pipeline's error policy.
type rejectingTarget struct {
	*memory.IndexedTarget
}

func (rejectingTarget) OnInsert(context.Context, laredo.TableIdentifier, laredo.Row) error {
	return errors.New("target rejects inserts")
}

// TestEngine_SourceRetry_errorStopSourceIsNotRetried: a pipeline whose error
// policy stopped the source did so on purpose, and the engine must not undo it.
func TestEngine_SourceRetry_errorStopSourceIsNotRetried(t *testing.T) {
	src := configuredSource()
	target := rejectingTarget{memory.NewIndexedTarget()}
	obs := &testutil.TestObserver{}
	e, errs := laredo.NewEngine(
		laredo.WithSource("src", src),
		laredo.WithPipeline("src", testutil.SampleTable(), target,
			laredo.ErrorPolicyOpt(laredo.ErrorStopSource), laredo.MaxRetries(0)),
		laredo.WithObserver(obs),
		fastRetry,
	)
	if len(errs) > 0 {
		t.Fatalf("new engine: %v", errs)
	}
	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(ctx) })
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return obs.EventCount("SourceDisconnected") >= 1
	}, "source did not stop")

	time.Sleep(300 * time.Millisecond) // many retry intervals
	if n := src.InitCount(); n != 1 {
		t.Errorf("a source stopped by ErrorStopSource was re-initialised: %d Init calls", n)
	}
	if disconnectedWith(obs, "retrying in") {
		t.Error("a source stopped by ErrorStopSource announced a retry")
	}
}

// TestEngine_SourceRetry_isolatedPipelineStaysIsolated: when the source fails,
// only the pipelines it took down come back. One its own error policy isolated
// earlier stays in ERROR.
func TestEngine_SourceRetry_isolatedPipelineStaysIsolated(t *testing.T) {
	src := configuredSource()
	good := memory.NewIndexedTarget()
	bad := rejectingTarget{memory.NewIndexedTarget()}
	obs := &testutil.TestObserver{}
	e, errs := laredo.NewEngine(
		laredo.WithSource("src", src),
		laredo.WithPipeline("src", testutil.SampleTable(), good),
		laredo.WithPipeline("src", testutil.SampleTable(), bad,
			laredo.ErrorPolicyOpt(laredo.ErrorIsolate), laredo.MaxRetries(0)),
		laredo.WithObserver(obs),
		fastRetry,
	)
	if len(errs) > 0 {
		t.Fatalf("new engine: %v", errs)
	}
	ctx := context.Background()
	if err := e.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = e.Stop(ctx) })
	if !e.AwaitReady(5 * time.Second) {
		t.Fatal("not ready")
	}

	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		_, ok := good.Get(1)
		return ok
	}, "good pipeline did not apply the insert")

	var badID string
	for _, ev := range obs.EventsByType("PipelineStateChanged") {
		if ev.Data["newState"] == laredo.PipelineError {
			badID, _ = ev.Data["pipelineID"].(string)
		}
	}
	if badID == "" {
		t.Fatal("isolated pipeline never entered ERROR")
	}

	src.FailStream(errors.New("connection reset"))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		return src.InitCount() >= 2
	}, "source was not retried")

	src.EmitInsert(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
	testutil.AssertEventually(t, 5*time.Second, func() bool {
		_, ok := good.Get(2)
		return ok
	}, "good pipeline did not recover")

	for _, ev := range obs.EventsByType("PipelineStateChanged") {
		if ev.Data["pipelineID"] == badID && ev.Data["oldState"] == laredo.PipelineError {
			t.Errorf("isolated pipeline left ERROR: %v", ev.Data)
		}
	}
}

// TestEngine_SourceRetry_reBaselineRemovesRowsDeletedDuringOutage: rows deleted
// at the source while the pipeline was not streaming must be gone once it
// recovers. A re-baseline that only upserted would keep enforcing them.
func TestEngine_SourceRetry_reBaselineRemovesRowsDeletedDuringOutage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func() (laredo.SyncTarget, func(id int) bool)
	}{
		{"indexed", func() (laredo.SyncTarget, func(int) bool) {
			tg := memory.NewIndexedTarget()
			return tg, func(id int) bool { _, ok := tg.Get(id); return ok }
		}},
		{"compiled", func() (laredo.SyncTarget, func(int) bool) {
			tg := memory.NewCompiledTarget(
				memory.Compiler(func(r laredo.Row) (any, error) { return r["name"], nil }),
				memory.KeyFields("id"),
			)
			return tg, func(id int) bool { _, ok := tg.Get(id); return ok }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := configuredSource()
			src.SetNoResume(true)
			src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))
			src.AddRow(testutil.SampleTable(), testutil.SampleRow(2, "bob"))
			target, has := tc.target()
			obs := &testutil.TestObserver{}
			e := startRetryEngine(t, src, target, obs)
			if !e.AwaitReady(5 * time.Second) {
				t.Fatal("not ready")
			}
			if !has(1) || !has(2) {
				t.Fatal("baseline rows missing")
			}

			// The outage: the stream is lost and the database is down. While it
			// is, row 2 is deleted at the source.
			src.SetBaselineError(errors.New("conn closed"))
			src.FailStream(laredo.ErrReBaselineRequired)
			testutil.AssertEventually(t, 5*time.Second, func() bool {
				return src.InitCount() >= 2
			}, "no retry")
			src.ClearRows(testutil.SampleTable())
			src.AddRow(testutil.SampleTable(), testutil.SampleRow(1, "alice"))

			// Still stale while failing: the failed copy removed nothing.
			if !has(2) {
				t.Fatal("a failed baseline removed rows")
			}

			src.SetBaselineError(nil)
			testutil.AssertEventually(t, 5*time.Second, func() bool {
				return e.IsSourceReady("src") && !has(2)
			}, "row deleted during the outage survived recovery")
			if !has(1) {
				t.Fatal("recovery removed a row that still exists")
			}
		})
	}
}
