// Package service provides the Connect-RPC server hosting OAM, Query, and
// Replication services.
package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/zourzouvillys/laredo/gen/laredo/replication/v1/replicationv1connect"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
)

// Server hosts OAM and Query services over Connect-RPC (HTTP/2 + HTTP/1.1).
type Server struct {
	// tlsErr records a certificate that failed to load, surfaced by Start.
	tlsErr error

	httpServer *http.Server
	mux        *http.ServeMux
	addr       string
	tlsConfig  *tls.Config

	mu       sync.Mutex
	listener net.Listener
}

// maxRequestBytes bounds a single request body.
const maxRequestBytes = 4 << 20

// Option configures the server.
type Option func(*serverConfig)

type serverConfig struct {
	addr               string
	oamHandler         laredov1connect.LaredoOAMServiceHandler
	queryHandler       laredov1connect.LaredoQueryServiceHandler
	replicationHandler replicationv1connect.LaredoReplicationServiceHandler
	tlsCertFile        string
	tlsKeyFile         string
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

// EnableReplication registers the fan-out replication service handler. The
// handler is engine-global: it serves every fan-out target in the engine,
// routing by the table identified in each request.
func EnableReplication(handler replicationv1connect.LaredoReplicationServiceHandler) Option {
	return func(c *serverConfig) {
		c.replicationHandler = handler
	}
}

// WithTLS enables TLS with the given certificate and key files.
func WithTLS(certFile, keyFile string) Option {
	return func(c *serverConfig) {
		c.tlsCertFile = certFile
		c.tlsKeyFile = keyFile
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

	// Connect imposes no read limit of its own, and the replication service
	// accepts a client-supplied filter list, so an unbounded request body was
	// reachable before any handler ran.
	handlerOpts := []connect.HandlerOption{
		connect.WithReadMaxBytes(maxRequestBytes),
	}

	if cfg.oamHandler != nil {
		path, handler := laredov1connect.NewLaredoOAMServiceHandler(cfg.oamHandler, handlerOpts...)
		mux.Handle(path, handler)
	}

	if cfg.queryHandler != nil {
		path, handler := laredov1connect.NewLaredoQueryServiceHandler(cfg.queryHandler, handlerOpts...)
		mux.Handle(path, handler)
	}

	if cfg.replicationHandler != nil {
		path, handler := replicationv1connect.NewLaredoReplicationServiceHandler(cfg.replicationHandler, handlerOpts...)
		mux.Handle(path, handler)
	}

	srv := &Server{
		mux:  mux,
		addr: cfg.addr,
		httpServer: &http.Server{
			// h2c so plaintext deployments speak HTTP/2. Connect's
			// bidirectional streaming — which the replication Sync call needs
			// in order to carry client acknowledgements — is HTTP/2 only, and
			// a plain mux over a plaintext listener negotiates HTTP/1.1.
			Handler:           h2c.NewHandler(mux, &http2.Server{}),
			ReadHeaderTimeout: 10 * time.Second,
			// No write or idle timeout: the replication and Query streams are
			// long-lived by design and either would cut them. ReadTimeout is
			// likewise unset because a bidirectional stream reads for as long
			// as it runs; the body size cap above is what bounds a request.
			MaxHeaderBytes: 1 << 20,
		},
	}

	// Configure TLS if cert and key are provided. A load failure is recorded
	// and surfaced by Start: swallowing it meant a typo in a certificate path
	// silently started the server in plaintext, which is the one failure mode
	// a TLS option must never have.
	if cfg.tlsCertFile != "" && cfg.tlsKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.tlsCertFile, cfg.tlsKeyFile)
		if err != nil {
			srv.tlsErr = fmt.Errorf("load TLS key pair (%s, %s): %w", cfg.tlsCertFile, cfg.tlsKeyFile, err)
		} else {
			srv.tlsConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
			srv.httpServer.TLSConfig = srv.tlsConfig
		}
	}

	return srv
}

// Start begins listening and serving. It blocks until the server is stopped
// or an error occurs during listen.
func (s *Server) Start() error {
	if s.tlsErr != nil {
		return s.tlsErr
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}

	// Wrap listener with TLS if configured.
	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
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

// IsTLS reports whether TLS is configured.
func (s *Server) IsTLS() bool {
	return s.tlsConfig != nil
}

// Stop performs a graceful shutdown: stops accepting new connections and
// waits for in-flight requests to complete (up to the context deadline).
func (s *Server) Stop(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
