// Package config implements HOCON configuration loading for laredo.
// It parses a HOCON config file and maps it to engine options.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gurkankaymak/hocon"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/filter"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/httpsync"
	"github.com/zourzouvillys/laredo/target/memory"
	"github.com/zourzouvillys/laredo/transform"
)

// Config holds the parsed configuration for a laredo server instance.
type Config struct {
	Sources  map[string]SourceConfig
	Tables   []TableConfig
	Snapshot *SnapshotConfig
	GRPC     *GRPCConfig
}

// SourceConfig is the configuration for a data source.
type SourceConfig struct {
	Type       string
	Connection string
	SlotMode   string
	SlotName   string
}

// TableConfig is the configuration for a table pipeline.
type TableConfig struct {
	Source  string
	Schema  string
	Table   string
	Targets []TargetConfig
	TTL     *TTLConfig
}

// TargetConfig is the configuration for a pipeline target.
type TargetConfig struct {
	Type         string
	LookupFields []string
	BaseURL      string
	BatchSize    int
	Timeout      time.Duration
	RetryCount   int
	AuthHeader   string
	Headers      map[string]string
	BufferSize   int
	BufferPolicy string
	ErrorPolicy  string
	MaxRetries   int
	Filters      []FilterConfig
	Transforms   []TransformConfig
}

// FilterConfig is the configuration for a pipeline filter.
type FilterConfig struct {
	Type   string
	Field  string
	Value  string
	Prefix string
}

// TransformConfig is the configuration for a pipeline transform.
type TransformConfig struct {
	Type   string
	Fields []string
	Field  string
}

// TTLConfig is the configuration for row expiry.
type TTLConfig struct {
	Mode          string
	Field         string
	CheckInterval time.Duration
}

// SnapshotConfig is the configuration for snapshots.
type SnapshotConfig struct {
	Enabled    bool
	Store      string
	Schedule   time.Duration
	OnShutdown bool
	Retention  int
}

// GRPCConfig is the configuration for the gRPC server.
type GRPCConfig struct {
	Port int
}

// Load parses a HOCON configuration file and returns a Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // config file path is from CLI flag, not user input
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(string(data))
}

// Parse parses a HOCON string and returns a Config.
func Parse(input string) (*Config, error) {
	hc, err := hocon.ParseString(input)
	if err != nil {
		return nil, fmt.Errorf("parse HOCON: %w", err)
	}

	cfg := &Config{Sources: make(map[string]SourceConfig)}

	// Parse sources.
	sourcesObj := safeGetObject(hc, "sources")
	for key := range sourcesObj {
		prefix := "sources." + key
		cfg.Sources[key] = SourceConfig{
			Type:       safeStr(hc, prefix+".type"),
			Connection: safeStr(hc, prefix+".connection"),
			SlotMode:   safeStr(hc, prefix+".slot_mode"),
			SlotName:   safeStr(hc, prefix+".slot_name"),
		}
	}

	// Parse tables — need to work with array elements as hocon.Object maps.
	tablesArr := safeGetArray(hc, "tables")
	for _, tableVal := range tablesArr {
		tableObj, ok := tableVal.(hocon.Object)
		if !ok {
			continue
		}
		tc := TableConfig{
			Source: objStr(tableObj, "source"),
			Schema: objStr(tableObj, "schema"),
			Table:  objStr(tableObj, "table"),
		}

		// Parse targets within this table.
		if targetsVal, ok := tableObj["targets"]; ok {
			if targetsArr, ok := targetsVal.(hocon.Array); ok {
				for _, tgtVal := range targetsArr {
					if tgtObj, ok := tgtVal.(hocon.Object); ok {
						tc.Targets = append(tc.Targets, parseTargetObj(tgtObj))
					}
				}
			}
		}

		// TTL.
		if ttlVal, ok := tableObj["ttl"]; ok {
			if ttlObj, ok := ttlVal.(hocon.Object); ok {
				field := objStr(ttlObj, "field")
				if field != "" {
					tc.TTL = &TTLConfig{
						Mode:          objStr(ttlObj, "mode"),
						Field:         field,
						CheckInterval: objDuration(ttlObj, "check_interval"),
					}
				}
			}
		}

		cfg.Tables = append(cfg.Tables, tc)
	}

	// Snapshot.
	snapStore := safeStr(hc, "snapshot.store")
	if snapStore != "" {
		cfg.Snapshot = &SnapshotConfig{
			Enabled:    safeBool(hc, "snapshot.enabled"),
			Store:      snapStore,
			Schedule:   safeDuration(hc, "snapshot.schedule"),
			OnShutdown: safeBool(hc, "snapshot.on_shutdown"),
			Retention:  safeInt(hc, "snapshot.retention.keep_count"),
		}
	}

	// gRPC.
	grpcPort := safeInt(hc, "grpc.port")
	if grpcPort > 0 {
		cfg.GRPC = &GRPCConfig{Port: grpcPort}
	}

	return cfg, nil
}

// Validate checks the config for errors.
func (c *Config) Validate() []error {
	var errs []error

	if len(c.Sources) == 0 {
		errs = append(errs, fmt.Errorf("at least one source is required"))
	}
	if len(c.Tables) == 0 {
		errs = append(errs, fmt.Errorf("at least one table is required"))
	}

	for i, tc := range c.Tables {
		prefix := fmt.Sprintf("tables[%d]", i)
		if tc.Source == "" {
			errs = append(errs, fmt.Errorf("%s: source is required", prefix))
		} else if _, ok := c.Sources[tc.Source]; !ok {
			errs = append(errs, fmt.Errorf("%s: references unknown source %q", prefix, tc.Source))
		}
		if tc.Schema == "" {
			errs = append(errs, fmt.Errorf("%s: schema is required", prefix))
		}
		if tc.Table == "" {
			errs = append(errs, fmt.Errorf("%s: table is required", prefix))
		}
		if len(tc.Targets) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one target is required", prefix))
		}
		for j, tgt := range tc.Targets {
			if tgt.Type == "" {
				errs = append(errs, fmt.Errorf("%s.targets[%d]: type is required", prefix, j))
			}
		}
	}

	return errs
}

// ToEngineOptions converts the parsed config into laredo engine options.
func (c *Config) ToEngineOptions() ([]laredo.Option, error) {
	var opts []laredo.Option

	for id, src := range c.Sources {
		source, err := createSource(src)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", id, err)
		}
		opts = append(opts, laredo.WithSource(id, source))
	}

	for _, tc := range c.Tables {
		table := laredo.Table(tc.Schema, tc.Table)
		for _, tgt := range tc.Targets {
			target, err := createTarget(tgt)
			if err != nil {
				return nil, fmt.Errorf("table %s.%s target %s: %w", tc.Schema, tc.Table, tgt.Type, err)
			}
			opts = append(opts, laredo.WithPipeline(tc.Source, table, target, buildPipelineOpts(tgt, tc.TTL)...))
		}
	}

	if c.Snapshot != nil {
		if c.Snapshot.Schedule > 0 {
			opts = append(opts, laredo.WithSnapshotSchedule(c.Snapshot.Schedule))
		}
		if c.Snapshot.OnShutdown {
			opts = append(opts, laredo.WithSnapshotOnShutdown(true))
		}
		if c.Snapshot.Retention > 0 {
			opts = append(opts, laredo.WithSnapshotRetention(c.Snapshot.Retention))
		}
	}

	return opts, nil
}

func buildPipelineOpts(tgt TargetConfig, ttl *TTLConfig) []laredo.PipelineOption {
	var opts []laredo.PipelineOption
	if tgt.BufferSize > 0 {
		opts = append(opts, laredo.BufferSize(tgt.BufferSize))
	}
	if tgt.BufferPolicy != "" {
		opts = append(opts, laredo.BufferPolicyOpt(parseBufferPolicy(tgt.BufferPolicy)))
	}
	if tgt.ErrorPolicy != "" {
		opts = append(opts, laredo.ErrorPolicyOpt(parseErrorPolicy(tgt.ErrorPolicy)))
	}
	if tgt.MaxRetries > 0 {
		opts = append(opts, laredo.MaxRetries(tgt.MaxRetries))
	}
	for _, fc := range tgt.Filters {
		if f, err := createFilter(fc); err == nil {
			opts = append(opts, laredo.PipelineFilterOpt(f))
		}
	}
	for _, tc := range tgt.Transforms {
		if t, err := createTransform(tc); err == nil {
			opts = append(opts, laredo.PipelineTransformOpt(t))
		}
	}
	if ttl != nil && ttl.Field != "" {
		opts = append(opts, laredo.WithTTLField(ttl.Field))
		if ttl.CheckInterval > 0 {
			opts = append(opts, laredo.WithTTLScanInterval(ttl.CheckInterval))
		}
	}
	return opts
}

// --- factories ---

func createSource(cfg SourceConfig) (laredo.SyncSource, error) {
	switch cfg.Type {
	case "postgresql", "pg":
		var opts []pg.Option
		if cfg.Connection != "" {
			opts = append(opts, pg.Connection(cfg.Connection))
		}
		if cfg.SlotMode == "stateful" {
			opts = append(opts, pg.SlotModeOpt(pg.SlotStateful))
		}
		if cfg.SlotName != "" {
			opts = append(opts, pg.SlotName(cfg.SlotName))
		}
		return pg.New(opts...), nil
	default:
		return nil, fmt.Errorf("unknown source type %q", cfg.Type)
	}
}

func createTarget(cfg TargetConfig) (laredo.SyncTarget, error) {
	switch cfg.Type {
	case "indexed-memory":
		var opts []memory.IndexedTargetOption
		if len(cfg.LookupFields) > 0 {
			opts = append(opts, memory.LookupFields(cfg.LookupFields...))
		}
		return memory.NewIndexedTarget(opts...), nil
	case "compiled-memory":
		return memory.NewCompiledTarget(), nil
	case "http-sync":
		var opts []httpsync.Option
		if cfg.BaseURL != "" {
			opts = append(opts, httpsync.BaseURL(cfg.BaseURL))
		}
		if cfg.BatchSize > 0 {
			opts = append(opts, httpsync.BatchSize(cfg.BatchSize))
		}
		if cfg.Timeout > 0 {
			opts = append(opts, httpsync.Timeout(cfg.Timeout))
		}
		if cfg.RetryCount > 0 {
			opts = append(opts, httpsync.RetryCount(cfg.RetryCount))
		}
		if cfg.AuthHeader != "" {
			opts = append(opts, httpsync.AuthHeader(cfg.AuthHeader))
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, httpsync.Headers(cfg.Headers))
		}
		return httpsync.New(opts...), nil
	default:
		return nil, fmt.Errorf("unknown target type %q", cfg.Type)
	}
}

func createFilter(cfg FilterConfig) (laredo.PipelineFilter, error) {
	switch cfg.Type {
	case "field-equals":
		return &filter.FieldEquals{Field: cfg.Field, Value: cfg.Value}, nil
	case "field-prefix":
		return &filter.FieldPrefix{Field: cfg.Field, Prefix: cfg.Prefix}, nil
	default:
		return nil, fmt.Errorf("unknown filter type %q", cfg.Type)
	}
}

func createTransform(cfg TransformConfig) (laredo.PipelineTransform, error) {
	switch cfg.Type {
	case "drop-fields":
		return &transform.DropFields{Fields: cfg.Fields}, nil
	case "add-timestamp":
		return &transform.AddTimestamp{Field: cfg.Field}, nil
	default:
		return nil, fmt.Errorf("unknown transform type %q", cfg.Type)
	}
}

func parseBufferPolicy(s string) laredo.BufferPolicy {
	switch s {
	case "drop_oldest":
		return laredo.BufferDropOldest
	case "error":
		return laredo.BufferError
	default:
		return laredo.BufferBlock
	}
}

func parseErrorPolicy(s string) laredo.ErrorPolicyKind {
	switch s {
	case "stop_source":
		return laredo.ErrorStopSource
	case "stop_all":
		return laredo.ErrorStopAll
	default:
		return laredo.ErrorIsolate
	}
}

// --- hocon.Object helpers ---

func parseTargetObj(obj hocon.Object) TargetConfig {
	tc := TargetConfig{
		Type:       objStr(obj, "type"),
		BaseURL:    objStr(obj, "base_url"),
		BatchSize:  objInt(obj, "batch_size"),
		Timeout:    time.Duration(objInt(obj, "timeout_ms")) * time.Millisecond,
		RetryCount: objInt(obj, "retry_count"),
		AuthHeader: objStr(obj, "auth_header"),
	}

	tc.LookupFields = objStrSlice(obj, "lookup_fields")

	if buf, ok := obj["buffer"].(hocon.Object); ok {
		tc.BufferSize = objInt(buf, "max_size")
		tc.BufferPolicy = objStr(buf, "policy")
	}

	if eh, ok := obj["error_handling"].(hocon.Object); ok {
		tc.MaxRetries = objInt(eh, "max_retries")
		tc.ErrorPolicy = objStr(eh, "on_persistent_failure")
	}

	if filtersVal, ok := obj["filters"]; ok {
		if arr, ok := filtersVal.(hocon.Array); ok {
			for _, fv := range arr {
				if fo, ok := fv.(hocon.Object); ok {
					tc.Filters = append(tc.Filters, FilterConfig{
						Type:   objStr(fo, "type"),
						Field:  objStr(fo, "field"),
						Value:  objStr(fo, "value"),
						Prefix: objStr(fo, "prefix"),
					})
				}
			}
		}
	}

	if transformsVal, ok := obj["transforms"]; ok {
		if arr, ok := transformsVal.(hocon.Array); ok {
			for _, tv := range arr {
				if to, ok := tv.(hocon.Object); ok {
					tc.Transforms = append(tc.Transforms, TransformConfig{
						Type:   objStr(to, "type"),
						Field:  objStr(to, "field"),
						Fields: objStrSlice(to, "fields"),
					})
				}
			}
		}
	}

	return tc
}

// objStr extracts a string value from a hocon.Object, stripping quotes.
func objStr(obj hocon.Object, key string) string {
	v, ok := obj[key]
	if !ok {
		return ""
	}
	s := fmt.Sprintf("%v", v)
	s = strings.Trim(s, "\"")
	return s
}

// objInt extracts an integer value from a hocon.Object.
func objInt(obj hocon.Object, key string) int {
	v, ok := obj[key]
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(fmt.Sprintf("%v", v))
	return n
}

// objDuration extracts a duration value from a hocon.Object.
func objDuration(obj hocon.Object, key string) time.Duration {
	v, ok := obj[key]
	if !ok {
		return 0
	}
	s := fmt.Sprintf("%v", v)
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// objStrSlice extracts a string slice from a hocon.Object.
func objStrSlice(obj hocon.Object, key string) []string {
	v, ok := obj[key]
	if !ok {
		return nil
	}
	arr, ok := v.(hocon.Array)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range arr {
		s := strings.Trim(fmt.Sprintf("%v", item), "\"")
		result = append(result, s)
	}
	return result
}

// --- hocon.Config safe accessors ---

func safeStr(hc *hocon.Config, path string) string {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	s := hc.GetString(path)
	return strings.Trim(s, "\"")
}

func safeInt(hc *hocon.Config, path string) int {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	return hc.GetInt(path)
}

func safeBool(hc *hocon.Config, path string) bool {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	return hc.GetBoolean(path)
}

func safeDuration(hc *hocon.Config, path string) time.Duration {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	return hc.GetDuration(path)
}

func safeGetObject(hc *hocon.Config, path string) hocon.Object {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	return hc.GetObject(path)
}

func safeGetArray(hc *hocon.Config, path string) hocon.Array {
	defer func() { recover() }() //nolint:errcheck // recover from panic
	return hc.GetArray(path)
}
