// Package config parses the laredo-snapshotter HOCON configuration into plain
// data structs. Wiring config to concrete destinations/formats/sinks (which pull
// in the AWS SDK) lives in cmd/laredo-snapshotter, keeping this package light and
// unit-testable.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gurkankaymak/hocon"
)

// Config is the parsed laredo-snapshotter configuration.
type Config struct {
	Tables      []Table
	Credentials map[string]Credential
	HTTPPort    int
}

// Table configures one materialized table (one Writer).
type Table struct {
	Source          Source
	DiffInterval    time.Duration
	Snapshot        SnapshotPolicy
	Destinations    []Destination
	SnapshotFormats []string
	DiffFormats     []string
	Events          []Event
	KeyFields       []string
	KeepEpochs      int
	KeyPrefix       string
}

// Source is the fan-out subscription.
type Source struct {
	Server            string
	Schema            string
	Table             string
	ClientID          string
	LocalSnapshotPath string
}

// SnapshotPolicy is the re-base threshold configuration.
type SnapshotPolicy struct {
	MinInterval      time.Duration
	MaxInterval      time.Duration
	MaxDiffBytes     int64
	MaxDiffFraction  float64
	MaxChurnRecords  int64
	MaxChurnFraction float64
}

// Destination is one artifact/manifest sink (local or s3).
type Destination struct {
	Type        string // "local" | "s3"
	Path        string // local
	Bucket      string // s3
	Prefix      string // s3
	Region      string // s3
	Credentials string // s3: credentials profile name
}

// Event is one change-event sink (sns, sqs, kinesis).
type Event struct {
	Type        string // "sns" | "sqs" | "kinesis"
	TopicARN    string // sns
	QueueURL    string // sqs
	Stream      string // kinesis
	Region      string
	Credentials string
}

// Credential is a named AWS credential profile.
type Credential struct {
	Type        string // "ambient" | "assume_role"
	Region      string
	RoleARN     string
	ExternalID  string
	SessionName string
}

// Load reads and parses a HOCON config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // config file path is from CLI flag
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(string(data))
}

// Parse parses a HOCON config string.
func Parse(input string) (*Config, error) {
	hc, err := hocon.ParseString(input)
	if err != nil {
		return nil, fmt.Errorf("config: parse HOCON: %w", err)
	}
	root := safeGetObject(hc, "snapshotter")
	if root == nil {
		return nil, fmt.Errorf("config: missing top-level \"snapshotter\" object")
	}

	cfg := &Config{Credentials: map[string]Credential{}}

	if http, ok := root["http"].(hocon.Object); ok {
		cfg.HTTPPort = objInt(http, "port")
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8080
	}

	if creds, ok := root["credentials"].(hocon.Object); ok {
		for name, v := range creds {
			if o, ok := v.(hocon.Object); ok {
				cfg.Credentials[name] = Credential{
					Type:        objStr(o, "type"),
					Region:      objStr(o, "region"),
					RoleARN:     objStr(o, "role_arn"),
					ExternalID:  objStr(o, "external_id"),
					SessionName: objStr(o, "session_name"),
				}
			}
		}
	}

	// A table may be given inline (single table) or as a "tables" array.
	if arr, ok := root["tables"].(hocon.Array); ok {
		for _, tv := range arr {
			if to, ok := tv.(hocon.Object); ok {
				cfg.Tables = append(cfg.Tables, parseTable(to))
			}
		}
	} else if _, ok := root["source"].(hocon.Object); ok {
		cfg.Tables = append(cfg.Tables, parseTable(root))
	}

	if len(cfg.Tables) == 0 {
		return nil, fmt.Errorf("config: no tables configured (set snapshotter.tables or snapshotter.source)")
	}
	for i, tbl := range cfg.Tables {
		if tbl.Source.Server == "" || tbl.Source.Schema == "" || tbl.Source.Table == "" {
			return nil, fmt.Errorf("config: tables[%d]: source.server, schema and table are required", i)
		}
		if len(tbl.Destinations) == 0 {
			return nil, fmt.Errorf("config: tables[%d]: at least one destination is required", i)
		}
	}
	return cfg, nil
}

func parseTable(o hocon.Object) Table {
	t := Table{KeyFields: []string{"id"}}

	if src, ok := o["source"].(hocon.Object); ok {
		t.Source = Source{
			Server:            objStr(src, "server"),
			Schema:            objStr(src, "schema"),
			Table:             objStr(src, "table"),
			ClientID:          objStr(src, "client_id"),
			LocalSnapshotPath: objStr(src, "local_snapshot_path"),
		}
	}
	t.KeyPrefix = objStr(o, "key_prefix")
	if t.KeyPrefix == "" {
		t.KeyPrefix = t.Source.Table + "/"
	}
	if kf := objStrSlice(o, "key_fields"); len(kf) > 0 {
		t.KeyFields = kf
	}

	t.DiffInterval = 30 * time.Second
	if d, ok := o["diff"].(hocon.Object); ok {
		if v := objDuration(d, "interval"); v > 0 {
			t.DiffInterval = v
		}
	}

	if s, ok := o["snapshot"].(hocon.Object); ok {
		t.Snapshot = SnapshotPolicy{
			MinInterval:      objDuration(s, "min_interval"),
			MaxInterval:      objDuration(s, "max_interval"),
			MaxDiffBytes:     int64(objInt(s, "max_diff_bytes")),
			MaxDiffFraction:  objFloat(s, "max_diff_fraction"),
			MaxChurnRecords:  int64(objInt(s, "max_churn_records")),
			MaxChurnFraction: objFloat(s, "max_churn_fraction"),
		}
	}
	if r, ok := o["retention"].(hocon.Object); ok {
		t.KeepEpochs = objInt(r, "keep_epochs")
	}

	if dests, ok := o["destinations"].(hocon.Array); ok {
		for _, dv := range dests {
			if do, ok := dv.(hocon.Object); ok {
				t.Destinations = append(t.Destinations, Destination{
					Type:        objStr(do, "type"),
					Path:        objStr(do, "path"),
					Bucket:      objStr(do, "bucket"),
					Prefix:      objStr(do, "prefix"),
					Region:      objStr(do, "region"),
					Credentials: objStr(do, "credentials"),
				})
			}
		}
	}

	if f, ok := o["formats"].(hocon.Object); ok {
		t.SnapshotFormats = objStrSlice(f, "snapshot")
		t.DiffFormats = objStrSlice(f, "diff")
	}
	if len(t.SnapshotFormats) == 0 {
		t.SnapshotFormats = []string{"jsonl"}
	}
	if len(t.DiffFormats) == 0 {
		t.DiffFormats = []string{"jsonl"}
	}

	if events, ok := o["events"].(hocon.Array); ok {
		for _, ev := range events {
			if eo, ok := ev.(hocon.Object); ok {
				t.Events = append(t.Events, Event{
					Type:        objStr(eo, "type"),
					TopicARN:    objStr(eo, "topic_arn"),
					QueueURL:    objStr(eo, "queue_url"),
					Stream:      objStr(eo, "stream"),
					Region:      objStr(eo, "region"),
					Credentials: objStr(eo, "credentials"),
				})
			}
		}
	}
	return t
}

// --- hocon.Object helpers (mirrors config package conventions) ---

// safeGetObject returns the object at path, recovering from the hocon library's
// panic when the path is absent or not an object.
func safeGetObject(hc *hocon.Config, path string) (obj hocon.Object) {
	defer func() { _ = recover() }()
	return hc.GetObject(path)
}

func objStr(o hocon.Object, key string) string {
	v, ok := o[key]
	if !ok {
		return ""
	}
	return strings.Trim(fmt.Sprintf("%v", v), "\"")
}

func objInt(o hocon.Object, key string) int {
	v, ok := o[key]
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(fmt.Sprintf("%v", v))
	return n
}

func objFloat(o hocon.Object, key string) float64 {
	v, ok := o[key]
	if !ok {
		return 0
	}
	f, _ := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
	return f
}

func objDuration(o hocon.Object, key string) time.Duration {
	v, ok := o[key]
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(fmt.Sprintf("%v", v))
	if err != nil {
		return 0
	}
	return d
}

func objStrSlice(o hocon.Object, key string) []string {
	v, ok := o[key]
	if !ok {
		return nil
	}
	if arr, ok := v.(hocon.Array); ok {
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			out = append(out, strings.Trim(fmt.Sprintf("%v", e), "\""))
		}
		return out
	}
	return nil
}
