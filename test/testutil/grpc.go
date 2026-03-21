package testutil

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/service"
)

// TestGRPCServer starts an in-process Connect-RPC server and returns
// OAM and Query clients. Callers must register handlers via service options.
// The server is automatically stopped when the test finishes.
func TestGRPCServer(t *testing.T, opts ...service.Option) (laredov1connect.LaredoOAMServiceClient, laredov1connect.LaredoQueryServiceClient) {
	t.Helper()

	allOpts := make([]service.Option, 0, 1+len(opts))
	allOpts = append(allOpts, service.WithAddress("127.0.0.1:0"))
	allOpts = append(allOpts, opts...)

	srv := service.New(allOpts...)

	go func() { _ = srv.Start() }()
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	addr := "http://" + srv.Addr()
	oamClient := laredov1connect.NewLaredoOAMServiceClient(http.DefaultClient, addr)
	queryClient := laredov1connect.NewLaredoQueryServiceClient(http.DefaultClient, addr)

	return oamClient, queryClient
}
