package config

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/dest/local"
	"github.com/zourzouvillys/laredo/snapshotter/format/jsonl"
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
	if ac.Store != "local" {
		t.Errorf("store: got %q, want local", ac.Store)
	}
	if ac.Path != "/var/lib/laredo/archive/events" {
		t.Errorf("path: got %q", ac.Path)
	}
	if ac.KeyPrefix != "public.events/" {
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
	const prefix = "public.events/"
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
		Store:     "local",
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
		{"s3 rejected", &ArchiveConfig{Store: "s3", Bucket: "b"}, "not yet supported"},
		{"unknown store", &ArchiveConfig{Store: "gcs"}, "unknown store"},
		{"empty store", &ArchiveConfig{Store: ""}, "store is required"},
		{"local without path", &ArchiveConfig{Store: "local"}, "requires store_config.path"},
		{"unknown format", &ArchiveConfig{Store: "local", Path: "/tmp/x", Formats: []string{"xml"}}, "unknown format"},
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

func TestBuildArchiveReader_Nil(t *testing.T) {
	r, err := BuildArchiveReader(nil)
	if err != nil || r != nil {
		t.Errorf("nil archive: got (%v, %v), want (nil, nil)", r, err)
	}
}

// TestValidate_ArchiveS3Rejected verifies an s3 archive is caught at validation
// time, not only at server start.
func TestValidate_ArchiveS3Rejected(t *testing.T) {
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
        archive { store = s3, store_config { bucket = b } }
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
		if strings.Contains(e.Error(), "not yet supported") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected validation to reject s3 archive, got %v", cfg.Validate())
	}
}
