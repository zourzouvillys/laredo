// Command laredo-server is the pre-built service binary that wires together
// all laredo modules with HOCON config, gRPC, metrics, and signal handling.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/config"
	prom "github.com/zourzouvillys/laredo/metrics/prometheus"
	"github.com/zourzouvillys/laredo/service"
	"github.com/zourzouvillys/laredo/service/oam"
	"github.com/zourzouvillys/laredo/service/query"
)

func main() {
	// Subcommand dispatch.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "validate":
			validateCmd()
			return
		case "config":
			configDumpCmd()
			return
		case "version":
			fmt.Printf("laredo-server %s\n", laredo.Version)
			return
		case "healthcheck":
			healthcheckCmd()
			return
		}
	}

	// Main server flow.
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// setFlags is a flag.Value that collects multiple --set key=value pairs.
type setFlags []string

func (s *setFlags) String() string { return strings.Join(*s, ", ") }
func (s *setFlags) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func run() error {
	configPath := flag.String("config", "", "path to HOCON config file (or set LAREDO_CONFIG)")
	confDir := flag.String("conf-dir", "/etc/laredo/conf.d", "directory of *.conf files to merge")
	initDir := flag.String("init-dir", "/docker-entrypoint-init.d", "directory of *.conf files to merge before conf-dir (Docker init pattern)")
	healthPort := flag.Int("health-port", 8080, "HTTP port for health and metrics endpoints")
	logLevel := flag.String("log-level", "info", "log level (debug, info, warn, error)")
	var sets setFlags
	flag.Var(&sets, "set", "config override as key=value (repeatable)")
	flag.Parse()

	// Configure logging.
	setupLogging(*logLevel)

	// Resolve config path.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("LAREDO_CONFIG")
	}
	if cfgPath == "" {
		return fmt.Errorf("config file required: use --config flag or LAREDO_CONFIG env var")
	}

	slog.Info("starting laredo-server", "version", laredo.Version, "config", cfgPath) //nolint:gosec // structured logging, not string interpolation

	// Load and validate config.
	cfg, err := config.LoadWithOptions(cfgPath, config.LoadOptions{
		InitDir:   *initDir,
		ConfDir:   *confDir,
		Overrides: sets,
	})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			slog.Error("config validation", "error", e)
		}
		return fmt.Errorf("config validation failed (%d errors)", len(errs))
	}

	// Set up Prometheus metrics.
	promReg := prometheus.NewRegistry()
	promReg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	promReg.MustRegister(collectors.NewGoCollector())
	observer := prom.New(promReg)

	// Create OAM service early so its observer can capture engine events.
	oamSvc := oam.New(nil)

	// Build engine options from config.
	opts, err := cfg.ToEngineOptions()
	if err != nil {
		return fmt.Errorf("build engine options: %w", err)
	}
	opts = append(opts, laredo.WithObserver(laredo.NewCompositeObserver(observer, oamSvc.Observer())))

	// Create engine.
	eng, errs := laredo.NewEngine(opts...)
	if len(errs) > 0 {
		for _, e := range errs {
			slog.Error("engine creation", "error", e)
		}
		return fmt.Errorf("engine creation failed (%d errors)", len(errs))
	}
	oamSvc.SetEngine(eng)

	// Start engine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	slog.Info("engine started")

	// Start health + metrics HTTP server.
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	healthMux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		ready := eng.IsReady()
		status := http.StatusOK
		if !ready {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		resp := map[string]any{"ready": ready}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck // best effort
	})
	healthMux.HandleFunc("/health/startup", func(w http.ResponseWriter, _ *http.Request) {
		// Startup probe: returns OK once the engine has been started.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"started"}`)
	})
	healthMux.Handle("/metrics", observer.Handler())

	healthServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", *healthPort),
		Handler:           healthMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("health server listening", "port", *healthPort)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("health server", "error", err)
		}
	}()

	// Start gRPC server.
	var grpcSrv *service.Server
	if cfg.GRPC != nil && cfg.GRPC.Port > 0 {
		querySvc := query.New(eng)

		grpcAddr := fmt.Sprintf(":%d", cfg.GRPC.Port)
		grpcSrv = service.New(
			service.WithAddress(grpcAddr),
			service.EnableOAM(oamSvc),
			service.EnableQuery(querySvc),
		)

		go func() {
			slog.Info("gRPC server listening", "port", cfg.GRPC.Port) //nolint:gosec // structured logging
			if err := grpcSrv.Start(); err != nil {
				slog.Error("gRPC server", "error", err)
			}
		}()
	}

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig)

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop gRPC server first.
	if grpcSrv != nil {
		if err := grpcSrv.Stop(shutdownCtx); err != nil {
			slog.Warn("gRPC shutdown", "error", err)
		}
	}

	// Stop health server.
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("health server shutdown", "error", err)
	}

	// Stop engine (drain buffers, snapshot, close).
	if err := eng.Stop(shutdownCtx); err != nil {
		slog.Warn("engine shutdown", "error", err)
	}

	slog.Info("shutdown complete")
	return nil
}

func validateCmd() {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	configPath := fs.String("config", "", "path to HOCON config file")
	fs.Parse(os.Args[2:]) //nolint:errcheck // ExitOnError handles errors

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("LAREDO_CONFIG")
	}
	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "config file required: use --config flag or LAREDO_CONFIG env var")
		os.Exit(1)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "validation error: %v\n", e)
		}
		os.Exit(1)
	}

	fmt.Println("config is valid")
}

func configDumpCmd() {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", "", "path to HOCON config file")
	confDir := fs.String("conf-dir", "/etc/laredo/conf.d", "directory of *.conf files to merge")
	fs.Parse(os.Args[2:]) //nolint:errcheck // ExitOnError handles errors

	// Handle "config --dump" or just "config" as dump.
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("LAREDO_CONFIG")
	}
	if cfgPath == "" {
		fmt.Fprintln(os.Stderr, "config file required: use --config flag or LAREDO_CONFIG env var")
		os.Exit(1)
	}

	cfg, err := config.LoadWithOptions(cfgPath, config.LoadOptions{ConfDir: *confDir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Mask sensitive values and dump.
	masked := config.MaskSensitive(cfg)
	data, err := json.MarshalIndent(masked, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error marshaling config: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func healthcheckCmd() {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	port := fs.Int("port", 8080, "health endpoint port")
	fs.Parse(os.Args[2:]) //nolint:errcheck // ExitOnError handles errors

	url := fmt.Sprintf("http://localhost:%d/health/ready", *port)
	resp, err := http.Get(url) //nolint:gosec,noctx // local health check
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		fmt.Println("ready")
	} else {
		fmt.Printf("not ready (status %d)\n", resp.StatusCode)
		os.Exit(1)
	}
}

func setupLogging(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
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
