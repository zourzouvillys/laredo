// Command laredo-snapshotter materializes one or more laredo fan-out tables into
// durable base snapshots + diffs on pluggable destinations, indexed by a
// manifest, with optional change events. See docs/edr/0001-snapshot-writer.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/config"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to HOCON config file (or set LAREDO_SNAPSHOTTER_CONFIG)")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	setupLogging(*logLevel)

	path := *configPath
	if path == "" {
		path = os.Getenv("LAREDO_SNAPSHOTTER_CONFIG")
	}
	if path == "" {
		return fmt.Errorf("config file required: use --config or LAREDO_SNAPSHOTTER_CONFIG")
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writers, err := buildWriters(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build writers: %w", err)
	}
	slog.Info("starting laredo-snapshotter", "tables", len(writers), "http_port", cfg.HTTPPort)

	// Supervisor: run each per-table writer concurrently.
	var wg sync.WaitGroup
	for _, w := range writers {
		wg.Add(1)
		go func(w *snapshotter.Writer) {
			defer wg.Done()
			if err := w.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("writer stopped", "table", w.Status().Table, "error", err)
			}
		}(w)
	}

	httpSrv := newHTTPServer(cfg.HTTPPort, writers)
	go func() {
		slog.Info("http server listening", "port", cfg.HTTPPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "error", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())

	cancel() // writers flush a final diff and stop

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "error", err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		slog.Warn("writers did not stop within the shutdown timeout")
	}
	slog.Info("shutdown complete")
	return nil
}

func setupLogging(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}
