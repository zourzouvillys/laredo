// Package config implements HOCON configuration loading for laredo.
// It parses a HOCON config file and maps it to engine options.
package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gurkankaymak/hocon"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/filter"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/destwire"
	"github.com/zourzouvillys/laredo/source/pg"
	"github.com/zourzouvillys/laredo/target/fanout"
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
	Fanout   *FanoutServerConfig
}

// SourceConfig is the configuration for a data source.
type SourceConfig struct {
	Type           string
	Connection     string
	SlotMode       string
	SlotName       string
	AlwaysBaseline bool
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
	// Fanout holds replication-fanout-specific settings. It is populated only
	// when Type == "replication-fanout".
	Fanout *FanoutConfig
}

// FanoutConfig configures a replication-fanout target. The fields map directly
// onto target/fanout.New options. The replication protocol is served by the
// engine-global fan-out listener (see FanoutServerConfig), not per target.
type FanoutConfig struct {
	JournalMaxEntries  int
	JournalMaxAge      time.Duration
	SnapshotInterval   time.Duration
	SnapshotKeepCount  int
	SnapshotMaxAge     time.Duration
	MaxClients         int
	ClientBufferSize   int
	ClientBufferPolicy string
	HeartbeatInterval  time.Duration
	// Archive optionally registers a read-only cold-tier archive (written by
	// laredo-snapshotter) for this target's table, enabling SYNC_MODE_REPLAY_ARCHIVE
	// for clients that fall behind the in-memory journal. See EDR-0005.
	Archive *ArchiveConfig
}

// ArchiveConfig describes the cold-tier archive a replication-fanout target
// reads from. It mirrors the snapshotter's destination shape. The archive is
// read-only here; the snapshotter remains the sole writer.
type ArchiveConfig struct {
	// Store is the destination backend: "local" (wired) or "s3" (not yet
	// supported in laredo-server — see EDR-0005).
	Store string
	// Path is the local filesystem root, used when Store == "local".
	Path string
	// Bucket/Prefix/Region/Credentials configure an S3 destination (Store == "s3").
	// Prefix is the storage-level object prefix, distinct from KeyPrefix below.
	Bucket      string
	Prefix      string
	Region      string
	Credentials string
	// Formats are the artifact codecs to try, by id ("jsonl", "protobuf").
	// Empty defaults to ["jsonl"]. The reader tries them in order.
	Formats []string
	// KeyPrefix is the per-table object-key prefix. It MUST match the prefix the
	// snapshotter Writer used for this table, or the reader finds no manifest and
	// cold replay declines to the live snapshot path.
	KeyPrefix string
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

// FanoutServerConfig configures the engine-global fan-out replication listener.
// A single LaredoReplicationService serves every replication-fanout target in
// the engine, routing by table; it listens on its own port, distinct from the
// OAM/Query gRPC port (GRPCConfig). See ADR-007.
type FanoutServerConfig struct {
	// GRPCPort is the listen port for the replication protocol (default 4002).
	GRPCPort int
}

// LoadOptions controls how configuration is loaded.
type LoadOptions struct {
	// InitDir is an optional directory of *.conf files to merge before ConfDir.
	// Supports the Docker /docker-entrypoint-init.d/ pattern. Files are merged
	// in alphabetical order. The directory is silently skipped if it doesn't exist.
	InitDir string

	// ConfDir is an optional directory of *.conf files to merge (alphabetical order).
	// Later files override earlier files. Conf.d files override the main config.
	ConfDir string

	// Overrides are key=value pairs applied after all files are loaded.
	// Keys use HOCON dot notation: "sources.pg.connection=postgres://..."
	Overrides []string
}

// Load parses a HOCON configuration file, applies environment variable
// overrides, and returns a Config.
func Load(path string) (*Config, error) {
	return LoadWithOptions(path, LoadOptions{})
}

// LoadWithOptions parses the main config file, merges conf.d files,
// applies --set overrides, applies env var overrides, and returns a Config.
func LoadWithOptions(path string, opts LoadOptions) (*Config, error) {
	// 1. Read main config file.
	data, err := os.ReadFile(path) //nolint:gosec // config file path is from CLI flag
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	combined := string(data)

	// 2. Merge init-dir directory (if specified, silently skipped if missing).
	if opts.InitDir != "" {
		extras, err := loadConfDir(opts.InitDir)
		if err == nil {
			combined += "\n" + extras
		}
		// Silently ignore missing init-dir — it's optional (Docker pattern).
	}

	// 3. Merge conf.d directory (if specified).
	if opts.ConfDir != "" {
		extras, err := loadConfDir(opts.ConfDir)
		if err != nil {
			return nil, fmt.Errorf("load conf.d: %w", err)
		}
		combined += "\n" + extras
	}

	// 4. Apply --set key=value overrides as HOCON.
	for _, override := range opts.Overrides {
		key, value, ok := strings.Cut(override, "=")
		if !ok {
			return nil, fmt.Errorf("invalid override %q: expected key=value", override)
		}
		// Convert dotted key to nested HOCON: "a.b.c=v" → "a { b { c = v } }"
		combined += "\n" + dotKeyToHOCON(strings.TrimSpace(key), strings.TrimSpace(value))
	}

	cfg, err := Parse(combined)
	if err != nil {
		return nil, err
	}

	// 5. Apply environment variable overrides.
	cfg.ApplyEnvOverrides()
	return cfg, nil
}

// loadConfDir reads all *.conf files from the given directory in alphabetical
// order and concatenates their contents.
func loadConfDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // directory doesn't exist — not an error
		}
		return "", fmt.Errorf("read directory %s: %w", dir, err)
	}

	var parts []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}
		data, err := os.ReadFile(dir + "/" + entry.Name()) //nolint:gosec // controlled directory path
		if err != nil {
			return "", fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		parts = append(parts, string(data))
	}

	return strings.Join(parts, "\n"), nil
}

// dotKeyToHOCON converts "a.b.c" = "value" to nested HOCON: "a { b { c = value } }"
func dotKeyToHOCON(key, value string) string {
	segments := strings.Split(key, ".")
	// Build nested braces: a { b { c = value } }
	var sb strings.Builder
	for i, seg := range segments {
		if i > 0 {
			sb.WriteString(" { ")
		}
		sb.WriteString(seg)
	}
	sb.WriteString(" = ")
	// Quote the value if it contains special characters.
	if strings.ContainsAny(value, " {},[]=#") {
		sb.WriteString(`"` + value + `"`)
	} else {
		sb.WriteString(value)
	}
	for range len(segments) - 1 {
		sb.WriteString(" }")
	}
	return sb.String()
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
			Type:           safeStr(hc, prefix+".type"),
			Connection:     safeStr(hc, prefix+".connection"),
			SlotMode:       safeStr(hc, prefix+".slot_mode"),
			SlotName:       safeStr(hc, prefix+".slot_name"),
			AlwaysBaseline: safeBool(hc, prefix+".always_baseline"),
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

	// Fan-out replication listener (engine-global). When a replication-fanout
	// target exists, ensure a listener config with a default port, so callers
	// can treat a non-nil cfg.Fanout as "serve the replication protocol".
	cfg.Fanout = parseFanoutServerConfig(hc)
	if configHasFanoutTarget(cfg) {
		if cfg.Fanout == nil {
			cfg.Fanout = &FanoutServerConfig{}
		}
		if cfg.Fanout.GRPCPort == 0 {
			cfg.Fanout.GRPCPort = DefaultFanoutPort
		}
	}

	return cfg, nil
}

// DefaultFanoutPort is the default listen port for the fan-out replication
// protocol when a replication-fanout target is configured (see ADR-007).
const DefaultFanoutPort = 4002

// targetTypeReplicationFanout is the config `type` value for a fan-out target.
const targetTypeReplicationFanout = "replication-fanout"

// configHasFanoutTarget reports whether any table declares a replication-fanout
// target.
func configHasFanoutTarget(cfg *Config) bool {
	for _, t := range cfg.Tables {
		for _, tg := range t.Targets {
			if tg.Type == targetTypeReplicationFanout {
				return true
			}
		}
	}
	return false
}

// parseFanoutServerConfig reads the top-level `fanout { grpc { port } }` block.
// It returns nil when the block is absent, so callers can default the port only
// when a replication-fanout target actually exists.
func parseFanoutServerConfig(hc *hocon.Config) *FanoutServerConfig {
	obj := safeGetObject(hc, "fanout")
	if obj == nil {
		return nil
	}
	fc := &FanoutServerConfig{}
	if grpc, ok := obj["grpc"].(hocon.Object); ok {
		fc.GRPCPort = objInt(grpc, "port")
	}
	return fc
}

// ApplyEnvOverrides applies environment variable overrides to the parsed config.
// HOCON paths map to env vars with dots replaced by underscores and uppercased.
// Both bare names and LAREDO_ prefix are checked (LAREDO_ takes precedence).
// E.g. sources.pg_main.connection → SOURCES_PG_MAIN_CONNECTION or LAREDO_SOURCES_PG_MAIN_CONNECTION
func (c *Config) ApplyEnvOverrides() {
	// Override source fields.
	for id, src := range c.Sources {
		prefix := "sources." + id
		src.Connection = envOverride(prefix+".connection", src.Connection)
		src.Type = envOverride(prefix+".type", src.Type)
		src.SlotMode = envOverride(prefix+".slot_mode", src.SlotMode)
		src.SlotName = envOverride(prefix+".slot_name", src.SlotName)
		src.AlwaysBaseline = envOverrideBool(prefix+".always_baseline", src.AlwaysBaseline)
		c.Sources[id] = src
	}

	// Override target fields within each table.
	for i, tc := range c.Tables {
		for j, tgt := range tc.Targets {
			prefix := fmt.Sprintf("tables.%d.targets.%d", i, j)
			tgt.BaseURL = envOverride(prefix+".base_url", tgt.BaseURL)
			tgt.AuthHeader = envOverride(prefix+".auth_header", tgt.AuthHeader)
			c.Tables[i].Targets[j] = tgt
		}
	}

	// Override gRPC port.
	if c.GRPC != nil {
		if v := envLookup("grpc.port"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				c.GRPC.Port = n
			}
		}
	}
}

// envOverride returns the env var value for the given HOCON path if set,
// otherwise returns the current value.
func envOverride(hoconPath, current string) string {
	if v := envLookup(hoconPath); v != "" {
		return v
	}
	return current
}

// envOverrideBool returns the env var value (parsed as a bool) for the given
// HOCON path if set, otherwise returns the current value. Accepts "true"/"1"
// as true and "false"/"0" as false; any other value leaves current unchanged.
func envOverrideBool(hoconPath string, current bool) bool {
	switch envLookup(hoconPath) {
	case "true", "1":
		return true
	case "false", "0":
		return false
	default:
		return current
	}
}

// envLookup checks for an env var matching the HOCON path.
// Checks LAREDO_ prefix first, then bare name.
func envLookup(hoconPath string) string {
	envKey := strings.ToUpper(strings.ReplaceAll(hoconPath, ".", "_"))

	// Check LAREDO_ prefix first.
	if v, ok := os.LookupEnv("LAREDO_" + envKey); ok {
		return v
	}
	// Check bare name.
	if v, ok := os.LookupEnv(envKey); ok {
		return v
	}
	return ""
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
			// A configured archive must build; surface s3/unknown-store/format
			// errors here rather than only at server start.
			if tgt.Fanout != nil && tgt.Fanout.Archive != nil {
				if _, err := BuildArchiveReader(tgt.Fanout.Archive); err != nil {
					errs = append(errs, fmt.Errorf("%s.targets[%d]: %w", prefix, j, err))
				}
			}
		}
	}

	// The fan-out replication listener must not collide with the OAM/Query port;
	// they are separate servers and would race to bind the same address.
	if c.Fanout != nil && c.GRPC != nil && c.Fanout.GRPCPort == c.GRPC.Port {
		errs = append(errs, fmt.Errorf("fanout.grpc.port (%d) must differ from grpc.port", c.Fanout.GRPCPort))
	}

	return errs
}

// MaskSensitive returns a copy of the config with sensitive values masked.
// Connection strings, auth headers, and other credentials are replaced with "***".
func MaskSensitive(c *Config) *Config {
	masked := *c

	// Deep copy and mask sources.
	masked.Sources = make(map[string]SourceConfig, len(c.Sources))
	for id, src := range c.Sources {
		src.Connection = maskValue(src.Connection)
		masked.Sources[id] = src
	}

	// Deep copy and mask targets.
	masked.Tables = make([]TableConfig, len(c.Tables))
	for i, tc := range c.Tables {
		maskedTargets := make([]TargetConfig, len(tc.Targets))
		for j, tgt := range tc.Targets {
			tgt.AuthHeader = maskValue(tgt.AuthHeader)
			maskedTargets[j] = tgt
		}
		tc.Targets = maskedTargets
		masked.Tables[i] = tc
	}

	return &masked
}

// maskValue replaces a non-empty value with "***".
func maskValue(s string) string {
	if s == "" {
		return ""
	}
	return "***"
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
		if cfg.AlwaysBaseline {
			opts = append(opts, pg.AlwaysBaseline(true))
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
	case targetTypeReplicationFanout:
		return fanout.New(fanoutOptions(cfg.Fanout)...), nil
	default:
		return nil, fmt.Errorf("unknown target type %q", cfg.Type)
	}
}

// fanoutOptions maps a FanoutConfig onto target/fanout.New options. A nil
// config (or zero fields) yields an empty option set, so the fan-out target
// keeps its built-in defaults.
func fanoutOptions(fc *FanoutConfig) []fanout.Option {
	if fc == nil {
		return nil
	}
	var opts []fanout.Option
	if fc.JournalMaxEntries > 0 {
		opts = append(opts, fanout.JournalMaxEntries(fc.JournalMaxEntries))
	}
	if fc.JournalMaxAge > 0 {
		opts = append(opts, fanout.JournalMaxAge(fc.JournalMaxAge))
	}
	if fc.SnapshotInterval > 0 {
		opts = append(opts, fanout.SnapshotInterval(fc.SnapshotInterval))
	}
	if fc.SnapshotKeepCount > 0 {
		opts = append(opts, fanout.SnapshotKeepCount(fc.SnapshotKeepCount))
	}
	if fc.SnapshotMaxAge > 0 {
		opts = append(opts, fanout.SnapshotMaxAge(fc.SnapshotMaxAge))
	}
	if fc.MaxClients > 0 {
		opts = append(opts, fanout.MaxClients(fc.MaxClients))
	}
	if fc.ClientBufferSize > 0 {
		opts = append(opts, fanout.ClientBufferSize(fc.ClientBufferSize))
	}
	if fc.ClientBufferPolicy != "" {
		opts = append(opts, fanout.ClientBufferPolicy(fc.ClientBufferPolicy))
	}
	if fc.HeartbeatInterval > 0 {
		opts = append(opts, fanout.HeartbeatInterval(fc.HeartbeatInterval))
	}
	return opts
}

// BuildArchiveReader constructs a read-only cold-tier archive reader from an
// ArchiveConfig, for registration via replication.WithArchive. Both local and
// s3 stores are supported, built through snapshotter/destwire so laredo-server
// and laredo-snapshotter wire destinations the same way (EDR-0005). S3 uses the
// ambient AWS credential chain; named profiles / assume-role are not wired for
// the archive. The reader does not touch storage at construction time, so an
// archive whose objects do not yet exist builds fine and simply declines cold
// replay until they do.
func BuildArchiveReader(ac *ArchiveConfig) (*snapshotter.Reader, error) {
	if ac == nil {
		return nil, nil
	}
	if ac.Credentials != "" {
		return nil, fmt.Errorf("archive: store_config.credentials is not supported here; laredo-server reads the archive with ambient AWS credentials")
	}
	formats, err := destwire.BuildFormats(archiveFormatIDs(ac.Formats))
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	dest, err := destwire.BuildDestination(context.Background(), destwire.DestinationSpec{
		Type:   ac.Store,
		Path:   ac.Path,
		Bucket: ac.Bucket,
		Prefix: ac.Prefix,
		Region: ac.Region,
	}, destwire.AmbientAWSConfig)
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	return snapshotter.NewReader(dest, ac.KeyPrefix, formats...)
}

// archiveFormatIDs defaults to jsonl when no formats are configured.
func archiveFormatIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{"jsonl"}
	}
	return ids
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

	if tc.Type == targetTypeReplicationFanout {
		tc.Fanout = parseFanoutConfig(obj)
	}

	return tc
}

// parseFanoutConfig reads the replication-fanout target settings:
//
//	journal       { max_entries, max_age }
//	snapshot      { interval, retention { keep_count, max_age } }
//	max_clients
//	client_buffer { max_size, policy }
//	heartbeat_interval
//	archive       { store, store_config { ... }, format, key_prefix }
//
// Zero/absent fields leave the corresponding target/fanout.New default in place.
func parseFanoutConfig(obj hocon.Object) *FanoutConfig {
	fc := &FanoutConfig{
		MaxClients:        objInt(obj, "max_clients"),
		HeartbeatInterval: objDuration(obj, "heartbeat_interval"),
	}
	if j, ok := obj["journal"].(hocon.Object); ok {
		fc.JournalMaxEntries = objInt(j, "max_entries")
		fc.JournalMaxAge = objDuration(j, "max_age")
	}
	if s, ok := obj["snapshot"].(hocon.Object); ok {
		fc.SnapshotInterval = objDuration(s, "interval")
		if r, ok := s["retention"].(hocon.Object); ok {
			fc.SnapshotKeepCount = objInt(r, "keep_count")
			fc.SnapshotMaxAge = objDuration(r, "max_age")
		}
	}
	if cb, ok := obj["client_buffer"].(hocon.Object); ok {
		fc.ClientBufferSize = objInt(cb, "max_size")
		fc.ClientBufferPolicy = objStr(cb, "policy")
	}
	if a, ok := obj["archive"].(hocon.Object); ok {
		fc.Archive = parseArchiveConfig(a)
	}
	return fc
}

// parseArchiveConfig reads a replication-fanout target's `archive` block:
//
//	archive {
//	  store = local | s3
//	  store_config { path = ... | bucket = ..., prefix = ..., region = ..., credentials = ... }
//	  format = jsonl | protobuf | [jsonl, protobuf]
//	  key_prefix = "public.events/"
//	}
func parseArchiveConfig(obj hocon.Object) *ArchiveConfig {
	ac := &ArchiveConfig{
		Store:     objStr(obj, "store"),
		KeyPrefix: objStr(obj, "key_prefix"),
		Formats:   objStrOrStrSlice(obj, "format"),
	}
	if sc, ok := obj["store_config"].(hocon.Object); ok {
		ac.Path = objStr(sc, "path")
		ac.Bucket = objStr(sc, "bucket")
		ac.Prefix = objStr(sc, "prefix")
		ac.Region = objStr(sc, "region")
		ac.Credentials = objStr(sc, "credentials")
	}
	return ac
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

// objDuration extracts a duration value from a hocon.Object, accepting either a
// bare (24h) or quoted ("24h") Go duration literal.
func objDuration(obj hocon.Object, key string) time.Duration {
	v, ok := obj[key]
	if !ok {
		return 0
	}
	s := strings.Trim(fmt.Sprintf("%v", v), "\"")
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

// objStrOrStrSlice accepts either a single scalar (format = jsonl) or an array
// (format = [jsonl, protobuf]) and returns it as a slice. Returns nil when absent.
func objStrOrStrSlice(obj hocon.Object, key string) []string {
	if _, ok := obj[key]; !ok {
		return nil
	}
	if s := objStrSlice(obj, key); s != nil {
		return s
	}
	if s := objStr(obj, key); s != "" {
		return []string{s}
	}
	return nil
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
