package config

import (
	"strings"
	"testing"
	"time"

	"github.com/zourzouvillys/laredo/target/fanout"
)

// fanoutConfig is a full replication-fanout config exercising every settable
// field plus the top-level fan-out listener block.
const fanoutConfig = `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}

fanout {
  grpc { port = 4100 }
}

tables = [
  {
    source = pg
    schema = public
    table = config_document
    targets = [
      {
        type = replication-fanout
        journal { max_entries = 1000000, max_age = 24h }
        snapshot {
          interval = 5m
          retention { keep_count = 5, max_age = 1h }
        }
        max_clients = 500
        client_buffer { max_size = 50000, policy = drop_disconnect }
        heartbeat_interval = 10s
      }
    ]
  }
]
`

func TestParse_ReplicationFanoutTarget(t *testing.T) {
	cfg, err := Parse(fanoutConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Top-level listener.
	if cfg.Fanout == nil {
		t.Fatalf("expected cfg.Fanout to be populated")
	}
	if cfg.Fanout.GRPCPort != 4100 {
		t.Errorf("expected fanout port 4100, got %d", cfg.Fanout.GRPCPort)
	}

	// Target.
	tgt := cfg.Tables[0].Targets[0]
	if tgt.Type != targetTypeReplicationFanout {
		t.Fatalf("expected replication-fanout, got %q", tgt.Type)
	}
	fc := tgt.Fanout
	if fc == nil {
		t.Fatalf("expected tgt.Fanout to be populated")
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"journal max_entries", fc.JournalMaxEntries, 1000000},
		{"journal max_age", fc.JournalMaxAge, 24 * time.Hour},
		{"snapshot interval", fc.SnapshotInterval, 5 * time.Minute},
		{"snapshot keep_count", fc.SnapshotKeepCount, 5},
		{"snapshot max_age", fc.SnapshotMaxAge, time.Hour},
		{"max_clients", fc.MaxClients, 500},
		{"client_buffer max_size", fc.ClientBufferSize, 50000},
		{"client_buffer policy", fc.ClientBufferPolicy, "drop_disconnect"},
		{"heartbeat_interval", fc.HeartbeatInterval, 10 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}

	// The factory must produce a *fanout.Target.
	target, err := createTarget(tgt)
	if err != nil {
		t.Fatalf("createTarget: %v", err)
	}
	if _, ok := target.(*fanout.Target); !ok {
		t.Errorf("expected *fanout.Target, got %T", target)
	}
}

// TestParse_ReplicationFanoutDefaultPort verifies that a replication-fanout
// target with no explicit `fanout {}` block still gets a listener config with
// the default port, so laredo-server knows to serve the protocol.
func TestParse_ReplicationFanoutDefaultPort(t *testing.T) {
	input := `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}
tables = [
  {
    source = pg
    schema = public
    table = events
    targets = [ { type = replication-fanout } ]
  }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Fanout == nil {
		t.Fatalf("expected cfg.Fanout to default when a fan-out target exists")
	}
	if cfg.Fanout.GRPCPort != DefaultFanoutPort {
		t.Errorf("expected default port %d, got %d", DefaultFanoutPort, cfg.Fanout.GRPCPort)
	}

	// A bare target must still build, keeping fanout.New's own defaults.
	if _, err := createTarget(cfg.Tables[0].Targets[0]); err != nil {
		t.Fatalf("createTarget(bare): %v", err)
	}
}

// TestValidate_FanoutPortCollision verifies that a fan-out listener sharing the
// OAM/Query port is rejected, rather than failing to bind at runtime.
func TestValidate_FanoutPortCollision(t *testing.T) {
	input := `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}
grpc { port = 4002 }
tables = [
  {
    source = pg
    schema = public
    table = events
    targets = [ { type = replication-fanout } ]
  }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Fan-out defaults to 4002, which collides with grpc.port above.
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "must differ from grpc.port") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a port-collision validation error, got %v", errs)
	}
}

// TestParse_NoFanoutNoListener verifies that configs without a replication-fanout
// target do not get a fan-out listener (so laredo-server starts no extra port).
func TestParse_NoFanoutNoListener(t *testing.T) {
	input := `
sources {
  pg { type = postgresql, connection = "postgres://localhost/db" }
}
tables = [
  {
    source = pg
    schema = public
    table = events
    targets = [ { type = compiled-memory } ]
  }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Fanout != nil {
		t.Errorf("expected no fan-out listener, got %+v", cfg.Fanout)
	}
}
