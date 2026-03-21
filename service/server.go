// Package service provides the Connect-RPC server hosting OAM, Query, and
// Replication services.
package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
)

// Server hosts OAM and Query services over Connect-RPC (HTTP/2 + HTTP/1.1).
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
	addr       string

	mu       sync.Mutex
	listener net.Listener
}

// Option configures the server.
type Option func(*serverConfig)

type serverConfig struct {
	addr         string
	oamHandler   laredov1connect.LaredoOAMServiceHandler
	queryHandler laredov1connect.LaredoQueryServiceHandler
}

// WithAddress sets the listen address (default ":4001").
func WithAddress(addr string) Option {
	return func(c *serverConfig) {
		c.addr = addr
	}
}

// EnableOAM registers the OAM service handler.
func EnableOAM(handler laredov1connect.LaredoOAMServiceHandler) Option {
	return func(c *serverConfig) {
		c.oamHandler = handler
	}
}

// EnableQuery registers the Query service handler.
func EnableQuery(handler laredov1connect.LaredoQueryServiceHandler) Option {
	return func(c *serverConfig) {
		c.queryHandler = handler
	}
}

// New creates a new server with the given options.
func New(opts ...Option) *Server {
	cfg := &serverConfig{
		addr: ":4001",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	mux := http.NewServeMux()

	if cfg.oamHandler != nil {
		path, handler := laredov1connect.NewLaredoOAMServiceHandler(cfg.oamHandler)
		mux.Handle(path, handler)
	}

	if cfg.queryHandler != nil {
		path, handler := laredov1connect.NewLaredoQueryServiceHandler(cfg.queryHandler)
		mux.Handle(path, handler)
	}

	return &Server{
		mux:  mux,
		addr: cfg.addr,
		httpServer: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}
}

// Start begins listening and serving. It blocks until the server is stopped
// or an error occurs during listen.
func (s *Server) Start() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	err = s.httpServer.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Addr returns the listener address. Only valid after Start has been called
// and before Stop. Returns empty string if not listening.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Stop performs a graceful shutdown: stops accepting new connections and
// waits for in-flight requests to complete (up to the context deadline).
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
