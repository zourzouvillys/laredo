package config

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
)

const (
	storeLocal   = "local"
	eventsPrefix = "public.events/"
)

const archiveConfig = `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}
tables = [
  {
    source = pg
    schema = public
    table = events
    targets = [
      {
        type = replication-fanout
        archive {
          store = local
          store_config { path = "/var/lib/laredo/archive/events" }
          format = jsonl
          key_prefix = "public.events/"
        }
      }
    ]
  }
]
`

func TestParse_FanoutArchive(t *testing.T) {
	cfg, err := Parse(archiveConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ac := cfg.Tables[0].Targets[0].Fanout.Archive
	if ac == nil {
		t.Fatalf("expected archive config to be populated")
	}
	if ac.Store != storeLocal {
		t.Errorf("store: got %q, want local", ac.Store)
	}
	if ac.Path != "/var/lib/laredo/archive/events" {
		t.Errorf("path: got %q", ac.Path)
	}
	if ac.KeyPrefix != eventsPrefix {
		t.Errorf("key_prefix: got %q", ac.KeyPrefix)
	}
	if len(ac.Formats) != 1 || ac.Formats[0] != "jsonl" {
		t.Errorf("formats: got %v, want [jsonl]", ac.Formats)
	}
}

// TestBuildArchiveReader_LocalEndToEnd writes a real archive (manifest +
// snapshot) under a non-empty key prefix, then builds a reader from an
// equivalent ArchiveConfig and reads it back — proving the destination, prefix,
// and format are all wired correctly.
func TestBuildArchiveReader_LocalEndToEnd(t *testing.T) {
	dir := t.TempDir()
	const prefix = eventsPrefix
	ctx := context.Background()

	dest := local.New(dir)
	f := jsonl.New()
	snapArt := snapshotter.Artifact{
		Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "1", RowCount: 1,
		Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	}
	var sb bytes.Buffer
	if err := f.WriteSnapshot(&sb, []laredo.Row{{"id": 1, "name": "alice"}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, _, err := dest.Put(ctx, snapshotter.ArtifactObjectKey(prefix, snapArt, f.Extension()), bytes.NewReader(sb.Bytes())); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}
	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion,
		Table:           "public.events",
		Epoch:           1,
		HeadPosition:    "1",
		Artifacts:       []snapshotter.Artifact{snapArt},
	}
	data, _ := json.Marshal(m)
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(prefix), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}

	// Build the reader the way laredo-server would, from config.
	reader, err := BuildArchiveReader(&ArchiveConfig{
		Store:     storeLocal,
		Path:      dir,
		KeyPrefix: prefix,
		// Formats omitted on purpose: must default to jsonl.
	})
	if err != nil {
		t.Fatalf("BuildArchiveReader: %v", err)
	}
	got, err := reader.LoadManifest(ctx)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got.HeadPosition != "1" {
		t.Errorf("head position: got %q, want 1", got.HeadPosition)
	}
	rows, err := reader.ReadSnapshot(ctx, snapArt)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "alice" {
		t.Errorf("snapshot rows: got %v", rows)
	}
}

func TestBuildArchiveReader_Errors(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *ArchiveConfig
		wantSub string
	}{
		{"unknown store", &ArchiveConfig{Store: "gcs"}, "unknown destination type"},
		{"empty store", &ArchiveConfig{Store: ""}, "type is required"},
		{"local without path", &ArchiveConfig{Store: storeLocal}, "requires a path"},
		{"s3 without bucket", &ArchiveConfig{Store: "s3", Region: "us-east-1"}, "requires a bucket"},
		{"credentials unsupported", &ArchiveConfig{Store: "s3", Bucket: "b", Credentials: "prod"}, "credentials is not supported"},
		{"unknown format", &ArchiveConfig{Store: storeLocal, Path: "/tmp/x", Formats: []string{"xml"}}, "unknown format"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildArchiveReader(c.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantSub)
			}
		})
	}
}

// TestBuildArchiveReader_S3 verifies an s3 archive builds a reader. Construction
// is network-free (AWS credentials resolve lazily), so no live S3 is needed.
func TestBuildArchiveReader_S3(t *testing.T) {
	r, err := BuildArchiveReader(&ArchiveConfig{
		Store:     "s3",
		Bucket:    "laredo-archive",
		Prefix:    "laredo/",
		Region:    "us-east-1",
		KeyPrefix: eventsPrefix,
	})
	if err != nil {
		t.Fatalf("BuildArchiveReader(s3): %v", err)
	}
	if r == nil {
		t.Fatal("expected a non-nil s3 reader")
	}
}

func TestBuildArchiveReader_Nil(t *testing.T) {
	r, err := BuildArchiveReader(nil)
	if err != nil || r != nil {
		t.Errorf("nil archive: got (%v, %v), want (nil, nil)", r, err)
	}
}

// writeSeedArchive writes a one-row base snapshot (alice) plus a manifest at
// head "1" under prefix — the minimal archive a source can replay.
func writeSeedArchive(t *testing.T, dir, prefix string) {
	t.Helper()
	ctx := context.Background()
	dest := local.New(dir)
	f := jsonl.New()
	snapArt := snapshotter.Artifact{
		Kind: snapshotter.KindSnapshot, Epoch: 1, ToPosition: "1", RowCount: 1,
		Formats: map[string]snapshotter.FormatRef{"jsonl": {}},
	}
	var sb bytes.Buffer
	if err := f.WriteSnapshot(&sb, []laredo.Row{{"id": 1, "name": "alice"}}); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, _, err := dest.Put(ctx, snapshotter.ArtifactObjectKey(prefix, snapArt, f.Extension()), bytes.NewReader(sb.Bytes())); err != nil {
		t.Fatalf("put snapshot: %v", err)
	}
	m := snapshotter.Manifest{
		ManifestVersion: snapshotter.ManifestVersion, Table: "public.events",
		Epoch: 1, HeadPosition: "1", Artifacts: []snapshotter.Artifact{snapArt},
	}
	data, _ := json.Marshal(m)
	if _, _, err := dest.Put(ctx, snapshotter.ManifestObjectKey(prefix), bytes.NewReader(data)); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
}

const archiveSourceConfig = `
sources {
  seed {
    type = archive
    store = local
    store_config { path = "/var/lib/laredo/archive/events" }
    format = jsonl
    key_prefix = "public.events/"
    follow = true
    poll_interval = 2s
    state_path = "/var/lib/laredo/archive/events.ack"
    key_fields = [id]
  }
}
tables = [
  { source = seed, schema = public, table = events, targets = [ { type = indexed-memory } ] }
]
`

// TestParse_ArchiveSource verifies the archive source block maps onto SourceConfig.
func TestParse_ArchiveSource(t *testing.T) {
	cfg, err := Parse(archiveSourceConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sc, ok := cfg.Sources["seed"]
	if !ok {
		t.Fatal("expected seed source")
	}
	if sc.Type != "archive" {
		t.Errorf("type: got %q", sc.Type)
	}
	if sc.Archive == nil || sc.Archive.Store != storeLocal || sc.Archive.Path != "/var/lib/laredo/archive/events" {
		t.Fatalf("archive store config: %+v", sc.Archive)
	}
	if sc.Archive.KeyPrefix != eventsPrefix {
		t.Errorf("key_prefix: got %q", sc.Archive.KeyPrefix)
	}
	if !sc.Follow {
		t.Error("follow should be true")
	}
	if sc.PollInterval != 2*time.Second {
		t.Errorf("poll_interval: got %v, want 2s", sc.PollInterval)
	}
	if sc.StatePath != "/var/lib/laredo/archive/events.ack" {
		t.Errorf("state_path: got %q", sc.StatePath)
	}
	if len(sc.KeyFields) != 1 || sc.KeyFields[0] != "id" {
		t.Errorf("key_fields: got %v", sc.KeyFields)
	}
}

// TestCreateSource_ArchiveEndToEnd proves the config → source → read path: build
// an archive source from config and replay a real on-disk archive with no
// database involved.
func TestCreateSource_ArchiveEndToEnd(t *testing.T) {
	dir := t.TempDir()
	const prefix = eventsPrefix
	writeSeedArchive(t, dir, prefix)

	src, err := createSource(SourceConfig{
		Type:      "archive",
		Archive:   &ArchiveConfig{Store: storeLocal, Path: dir, KeyPrefix: prefix},
		KeyFields: []string{"id"},
	})
	if err != nil {
		t.Fatalf("createSource: %v", err)
	}
	ctx := context.Background()
	tbl := laredo.Table("public", "events")
	if _, err := src.Init(ctx, laredo.SourceConfig{Tables: []laredo.TableIdentifier{tbl}}); err != nil {
		t.Fatalf("init: %v", err)
	}
	var rows []laredo.Row
	pos, err := src.Baseline(ctx, nil, func(_ laredo.TableIdentifier, r laredo.Row) { rows = append(rows, r) })
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "alice" {
		t.Fatalf("baseline rows: got %v", rows)
	}
	if pos != "1" {
		t.Errorf("position: got %v, want 1", pos)
	}
	_ = src.Close(ctx)
}

// TestCreateSource_ArchiveMissingStore verifies a store-less archive source fails
// to build with a clear error.
func TestCreateSource_ArchiveMissingStore(t *testing.T) {
	if _, err := createSource(SourceConfig{Type: "file"}); err == nil {
		t.Fatal("expected an error when no store is configured")
	}
}

// TestValidate_ArchiveSourceMissingStore verifies a store-less archive source is
// rejected at validation time.
func TestValidate_ArchiveSourceMissingStore(t *testing.T) {
	input := `
sources { seed { type = archive } }
tables = [ { source = seed, schema = public, table = events, targets = [ { type = indexed-memory } ] } ]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, e := range cfg.Validate() {
		if strings.Contains(e.Error(), "archive source requires a store") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected validation to reject store-less archive source, got %v", cfg.Validate())
	}
}

// TestArchiveSourceForTable verifies the group clone derives a per-table
// key_prefix and state file without mutating the original config.
func TestArchiveSourceForTable(t *testing.T) {
	src := SourceConfig{
		Type: "archive", Group: true, StatePath: "/var/state",
		Archive:   &ArchiveConfig{Store: storeLocal, Path: "/arch"},
		KeyFields: []string{"id"},
	}
	got := archiveSourceForTable(src, "public", "events")
	if got.Archive.KeyPrefix != eventsPrefix {
		t.Errorf("derived key_prefix: got %q, want public.events/", got.Archive.KeyPrefix)
	}
	if want := filepath.Join("/var/state", "public.events.pos"); got.StatePath != want {
		t.Errorf("derived state_path: got %q, want %q", got.StatePath, want)
	}
	if got.Archive.Store != storeLocal || got.Archive.Path != "/arch" {
		t.Errorf("store config not carried: %+v", got.Archive)
	}
	// The original must be untouched (clone copies ArchiveConfig by value).
	if src.Archive.KeyPrefix != "" {
		t.Errorf("original ArchiveConfig mutated: %q", src.Archive.KeyPrefix)
	}
	if src.StatePath != "/var/state" {
		t.Errorf("original StatePath mutated: %q", src.StatePath)
	}
}

// TestToEngineOptions_ArchiveGroup verifies a group block expands to one archive
// source per referencing table, keyed by a synthesized per-table id.
func TestToEngineOptions_ArchiveGroup(t *testing.T) {
	input := `
sources {
  seed {
    type = archive
    group = true
    store = local
    store_config { path = "/tmp/laredo-archive" }
    format = jsonl
    follow = true
  }
}
tables = [
  { source = seed, schema = public, table = events, targets = [ { type = indexed-memory } ] },
  { source = seed, schema = public, table = users,  targets = [ { type = indexed-memory } ] }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errs := cfg.Validate(); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	opts, err := cfg.ToEngineOptions()
	if err != nil {
		t.Fatalf("to engine options: %v", err)
	}
	eng, errs := laredo.NewEngine(opts...)
	if len(errs) != 0 {
		t.Fatalf("new engine: %v", errs)
	}
	ids := map[string]bool{}
	for _, id := range eng.SourceIDs() {
		ids[id] = true
	}
	for _, want := range []string{"seed/public.events", "seed/public.users"} {
		if !ids[want] {
			t.Errorf("missing synthesized source %q; got %v", want, eng.SourceIDs())
		}
	}
	if ids["seed"] {
		t.Errorf("group id should not be registered directly; got %v", eng.SourceIDs())
	}
}

// TestValidate_ArchiveBadStore verifies a malformed archive (here, an unknown
// store) is caught at validation time, not only at server start.
func TestValidate_ArchiveBadStore(t *testing.T) {
	input := `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}
tables = [
  {
    source = pg
    schema = public
    table = events
    targets = [
      {
        type = replication-fanout
        archive { store = gcs }
      }
    ]
  }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	found := false
	for _, e := range cfg.Validate() {
		if strings.Contains(e.Error(), "unknown destination type") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected validation to reject unknown archive store, got %v", cfg.Validate())
	}
}
