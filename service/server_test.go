package service_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
	"github.com/zourzouvillys/laredo/service"
)

func TestServer_StartStop(t *testing.T) {
	srv := service.New(service.WithAddress("127.0.0.1:0"))

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// Wait for server to be listening.
	time.Sleep(50 * time.Millisecond)

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("expected non-empty address after Start")
	}

	// Stop gracefully.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestServer_WithOAMService(t *testing.T) {
	// Register a minimal OAM handler (unimplemented) and verify it responds.
	handler := laredov1connect.UnimplementedLaredoOAMServiceHandler{}

	srv := service.New(
		service.WithAddress("127.0.0.1:0"),
		service.EnableOAM(handler),
	)

	go func() { _ = srv.Start() }()
	time.Sleep(50 * time.Millisecond)
	defer func() { _ = srv.Stop(context.Background()) }()

	// Create a client and call an unimplemented RPC.
	client := laredov1connect.NewLaredoOAMServiceClient(
		http.DefaultClient,
		"http://"+srv.Addr(),
	)

	_, err := client.GetStatus(context.Background(), connect.NewRequest(&v1.GetStatusRequest{}))
	if err == nil {
		t.Fatal("expected error from unimplemented RPC")
	}
	// Should be CodeUnimplemented.
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("expected CodeUnimplemented, got %v", connect.CodeOf(err))
	}
}
