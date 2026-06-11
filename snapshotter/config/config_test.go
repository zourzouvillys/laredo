package config

import (
	"testing"
	"time"
)

func TestParse_InlineSingleTable(t *testing.T) {
	cfg, err := Parse(`
snapshotter {
  source { server = "laredo:4001", schema = public, table = users, client_id = c1 }
  diff { interval = 10s }
  snapshot { min_interval = 1m, max_interval = 2h, max_diff_bytes = 1048576, max_churn_fraction = 0.5 }
  destinations = [ { type = local, path = "/tmp/a" } ]
  formats { snapshot = [ jsonl, protobuf ], diff = [ protobuf ] }
  http { port = 9090 }
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.HTTPPort != 9090 {
		t.Fatalf("http port = %d", cfg.HTTPPort)
	}
	if len(cfg.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(cfg.Tables))
	}
	tbl := cfg.Tables[0]
	if tbl.Source.Server != "laredo:4001" || tbl.Source.Table != "users" {
		t.Fatalf("source = %+v", tbl.Source)
	}
	if tbl.DiffInterval != 10*time.Second {
		t.Fatalf("diff interval = %v", tbl.DiffInterval)
	}
	if tbl.Snapshot.MinInterval != time.Minute || tbl.Snapshot.MaxInterval != 2*time.Hour {
		t.Fatalf("snapshot policy = %+v", tbl.Snapshot)
	}
	if tbl.Snapshot.MaxDiffBytes != 1048576 || tbl.Snapshot.MaxChurnFraction != 0.5 {
		t.Fatalf("thresholds = %+v", tbl.Snapshot)
	}
	if tbl.KeyPrefix != "users/" {
		t.Fatalf("key prefix = %q", tbl.KeyPrefix)
	}
	if len(tbl.SnapshotFormats) != 2 || tbl.SnapshotFormats[1] != "protobuf" || len(tbl.DiffFormats) != 1 {
		t.Fatalf("formats = %v / %v", tbl.SnapshotFormats, tbl.DiffFormats)
	}
	if len(tbl.Destinations) != 1 || tbl.Destinations[0].Type != "local" || tbl.Destinations[0].Path != "/tmp/a" {
		t.Fatalf("destinations = %+v", tbl.Destinations)
	}
}

func TestParse_TablesArrayWithCredentialsAndEvents(t *testing.T) {
	cfg, err := Parse(`
snapshotter {
  credentials {
    s3w  { type = ambient }
    pub  { type = assume_role, role_arn = "arn:aws:iam::1:role/x", external_id = "e" }
  }
  tables = [
    {
      source { server = "laredo:4001", schema = public, table = docs }
      destinations = [ { type = s3, bucket = b, prefix = "docs/", region = us-east-1, credentials = s3w } ]
      events = [ { type = kinesis, stream = laredo-docs, credentials = pub } ]
      retention { keep_epochs = 3 }
    }
  ]
}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Credentials["pub"].Type != "assume_role" || cfg.Credentials["pub"].RoleARN == "" {
		t.Fatalf("credentials = %+v", cfg.Credentials)
	}
	tbl := cfg.Tables[0]
	if tbl.Destinations[0].Type != "s3" || tbl.Destinations[0].Credentials != "s3w" {
		t.Fatalf("dest = %+v", tbl.Destinations[0])
	}
	if len(tbl.Events) != 1 || tbl.Events[0].Type != "kinesis" || tbl.Events[0].Stream != "laredo-docs" {
		t.Fatalf("events = %+v", tbl.Events)
	}
	if tbl.KeepEpochs != 3 {
		t.Fatalf("keep_epochs = %d", tbl.KeepEpochs)
	}
}

func TestParse_Errors(t *testing.T) {
	if _, err := Parse(`foo {}`); err == nil {
		t.Fatal("missing snapshotter block should error")
	}
	if _, err := Parse(`snapshotter { source { server = s } }`); err == nil {
		t.Fatal("missing schema/table should error")
	}
	if _, err := Parse(`snapshotter { source { server = s, schema = p, table = t } }`); err == nil {
		t.Fatal("missing destinations should error")
	}
}

func TestParse_ShippedExamples(t *testing.T) {
	for _, f := range []string{
		"../../examples/snapshotter/snapshotter.conf",
		"../../examples/snapshotter/snapshotter-local-dev.conf",
	} {
		if _, err := Load(f); err != nil {
			t.Errorf("example %s failed to parse: %v", f, err)
		}
	}
}
