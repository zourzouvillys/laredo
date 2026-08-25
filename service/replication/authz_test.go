package replication

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/fanout"
	"github.com/zourzouvillys/laredo/test/testutil"
)

// startAuthorizedService is startReplService with an Authorizer installed.
func startAuthorizedService(t *testing.T, a service.Authorizer) replicationv1connect.LaredoReplicationServiceClient {
	t.Helper()

	src := testsource.New()
	tbl := testutil.SampleTable()
	src.SetSchema(tbl, testutil.SampleColumns())
	src.AddRow(tbl, testutil.SampleRow(1, "alice"))
	src.AddRow(tbl, testutil.SampleRow(2, "bob"))

	ft := fanout.New()
	eng, errs := laredo.NewEngine(laredo.WithSource("pg", src), laredo.WithPipeline("pg", tbl, ft))
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

	opts := []connect.HandlerOption{}
	if a != nil {
		opts = append(opts, connect.WithInterceptors(newTestAuthInterceptor(a)))
	}
	path, handler := replicationv1connect.NewLaredoReplicationServiceHandler(New(eng), opts...)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: mux, Protocols: testProtocols(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return replicationv1connect.NewLaredoReplicationServiceClient(
		testH2CClient(), "http://"+listener.Addr().String(), connect.WithGRPC())
}

// newTestAuthInterceptor mirrors what service.WithAuthorizer installs. It
// lives here because the service package's own interceptor is unexported and
// this package cannot import it without a cycle.
func newTestAuthInterceptor(a service.Authorizer) connect.Interceptor {
	return &testAuthInterceptor{a: a}
}

type testAuthInterceptor struct{ a service.Authorizer }

func (i *testAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		d, err := i.a.Authorize(ctx, service.AuthRequest{
			Procedure: req.Spec().Procedure, Header: req.Header(),
		})
		if err != nil {
			return nil, err
		}
		return next(service.WithAuthorizerValue(service.WithDecision(ctx, d), i.a), req)
	}
}

func (i *testAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *testAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		d, err := i.a.Authorize(ctx, service.AuthRequest{
			Procedure: conn.Spec().Procedure, Header: conn.RequestHeader(),
		})
		if err != nil {
			return err
		}
		return next(service.WithAuthorizerValue(service.WithDecision(ctx, d), i.a), conn)
	}
}

// TestSync_DeniedWithoutAuthorization confirms an Authorizer can refuse.
func TestSync_DeniedWithoutAuthorization(t *testing.T) {
	deny := service.AuthorizerFunc(func(_ context.Context, _ service.AuthRequest) (service.AuthDecision, error) {
		return service.AuthDecision{}, connect.NewError(connect.CodePermissionDenied, errors.New("nope"))
	})
	client := startAuthorizedService(t, deny)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.Sync(ctx)
	_ = stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Start{
		Start: &v1.SyncStart{Schema: tbl.Schema, Table: tbl.Table, ClientId: "x"},
	}})
	if _, err := stream.Receive(); err == nil {
		t.Fatal("an unauthorized Sync returned data")
	}
}

// TestSync_ImposedPredicatesCannotBeWidened is the important one. A client's
// own filters only ever subtract, so a caller that sends none is asking for
// the whole table. An Authorizer therefore has to be able to add predicates,
// not merely approve the ones it was given — otherwise scoping a subscriber to
// its own partition is impossible.
func TestSync_ImposedPredicatesCannotBeWidened(t *testing.T) {
	scoped := service.AuthorizerFunc(func(_ context.Context, req service.AuthRequest) (service.AuthDecision, error) {
		if req.Schema == "" {
			// Stream-open check: allow, scope is decided on the second call.
			return service.AuthDecision{Subject: "tenant-a"}, nil
		}
		return service.AuthDecision{
			Subject: "tenant-a",
			Require: []*v1.FieldPredicate{{
				Field: "name",
				Match: &v1.FieldPredicate_Equals{Equals: structpb.NewStringValue("alice")},
			}},
		}, nil
	})
	client := startAuthorizedService(t, scoped)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The client deliberately sends NO filters — asking for everything.
	stream := client.Sync(ctx)
	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Start{
		Start: &v1.SyncStart{Schema: tbl.Schema, Table: tbl.Table, ClientId: "greedy"},
	}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var names []string
	for {
		msg, err := stream.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if row := msg.GetSnapshotRow(); row != nil {
			names = append(names, row.GetRow().GetFields()["name"].GetStringValue())
		}
		if msg.GetSnapshotEnd() != nil {
			break
		}
	}

	if len(names) != 1 || names[0] != "alice" {
		t.Errorf("received %v; the imposed predicate did not constrain a caller that sent no filters", names)
	}
}

// TestSync_ClientIDBoundToSubject covers the status-view impersonation: the
// client id is a free-form string the caller chooses, and the status view is
// keyed on it.
func TestSync_ClientIDBoundToSubject(t *testing.T) {
	auth := service.AuthorizerFunc(func(_ context.Context, _ service.AuthRequest) (service.AuthDecision, error) {
		return service.AuthDecision{Subject: "verified-subject"}, nil
	})
	client := startAuthorizedService(t, auth)
	tbl := testutil.SampleTable()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := client.Sync(ctx)
	if err := stream.Send(&v1.SyncClientMessage{Message: &v1.SyncClientMessage_Start{
		Start: &v1.SyncStart{Schema: tbl.Schema, Table: tbl.Table, ClientId: "i-am-warden"},
	}}); err != nil {
		t.Fatalf("send: %v", err)
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

	if got := clientStatus(t, client, tbl, "i-am-warden"); got != nil {
		t.Error("the client-supplied id was used; a caller can impersonate another subscriber in the status view")
	}
	if got := clientStatus(t, client, tbl, "verified-subject"); got == nil {
		t.Error("the authenticated subject was not used as the client id")
	}
}
