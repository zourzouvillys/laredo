package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// newHTTPServer builds the operational HTTP server: health, status, on-demand
// snapshot, and Prometheus metrics derived from each writer's status.
func newHTTPServer(port int, writers []*snapshotter.Writer) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		ready := true
		for _, wr := range writers {
			if wr.Status().Epoch < 1 { // not yet wrote its initial base snapshot
				ready = false
				break
			}
		}
		code := http.StatusOK
		if !ready {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]bool{"ready": ready})
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		statuses := make([]snapshotter.Status, 0, len(writers))
		for _, wr := range writers {
			statuses = append(statuses, wr.Status())
		}
		writeJSON(w, http.StatusOK, statuses)
	})

	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use POST"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		results := map[string]string{}
		for _, wr := range writers {
			table := wr.Status().Table
			if err := wr.Snapshot(ctx); err != nil {
				results[table] = "error: " + err.Error()
			} else {
				results[table] = "ok"
			}
		}
		writeJSON(w, http.StatusOK, results)
	})

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(&writerCollector{writers: writers})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	return &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writerCollector exposes per-writer status as Prometheus gauges.
type writerCollector struct {
	writers []*snapshotter.Writer
}

var (
	descEpoch       = prometheus.NewDesc("snapshotter_epoch", "Current artifact epoch (bumped each snapshot).", []string{"table"}, nil)
	descBufferDepth = prometheus.NewDesc("snapshotter_buffer_depth", "Number of changes buffered for the next diff.", []string{"table"}, nil)
	descChurn       = prometheus.NewDesc("snapshotter_churn_records", "Change events since the last snapshot.", []string{"table"}, nil)
	descSnapAge     = prometheus.NewDesc("snapshotter_snapshot_age_seconds", "Seconds since the last base snapshot.", []string{"table"}, nil)
)

func (c *writerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descEpoch
	ch <- descBufferDepth
	ch <- descChurn
	ch <- descSnapAge
}

func (c *writerCollector) Collect(ch chan<- prometheus.Metric) {
	for _, w := range c.writers {
		s := w.Status()
		ch <- prometheus.MustNewConstMetric(descEpoch, prometheus.GaugeValue, float64(s.Epoch), s.Table)
		ch <- prometheus.MustNewConstMetric(descBufferDepth, prometheus.GaugeValue, float64(s.BufferDepth), s.Table)
		ch <- prometheus.MustNewConstMetric(descChurn, prometheus.GaugeValue, float64(s.ChurnRecords), s.Table)
		age := 0.0
		if !s.LastSnapshotAt.IsZero() {
			age = time.Since(s.LastSnapshotAt).Seconds()
		}
		ch <- prometheus.MustNewConstMetric(descSnapAge, prometheus.GaugeValue, age, s.Table)
	}
}
