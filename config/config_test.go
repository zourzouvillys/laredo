package config

import (
	"testing"
)

const basicConfig = `
sources {
  pg_main {
    type = postgresql
    connection = "postgresql://localhost:5432/mydb?user=testuser"
    slot_mode = stateful
    slot_name = laredo_slot_01
  }
}

tables = [
  {
    source = pg_main
    schema = public
    table = users

    targets = [
      {
        type = indexed-memory
        lookup_fields = [email]

        buffer { max_size = 5000, policy = block }

        error_handling {
          max_retries = 3
          on_persistent_failure = isolate
        }
      }
    ]

    ttl {
      mode = field
      field = expires_at
      check_interval = 30s
    }
  }
]

grpc {
  port = 4001
}
`

func TestParse_BasicConfig(t *testing.T) {
	cfg, err := Parse(basicConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Sources.
	if len(cfg.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(cfg.Sources))
	}
	src := cfg.Sources["pg_main"]
	if src.Type != "postgresql" {
		t.Errorf("expected type=postgresql, got %q", src.Type)
	}
	if src.SlotMode != "stateful" {
		t.Errorf("expected slot_mode=stateful, got %q", src.SlotMode)
	}
	if src.SlotName != "laredo_slot_01" {
		t.Errorf("expected slot_name=laredo_slot_01, got %q", src.SlotName)
	}

	// Tables.
	if len(cfg.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(cfg.Tables))
	}
	tc := cfg.Tables[0]
	if tc.Source != "pg_main" {
		t.Errorf("expected source=pg_main, got %q", tc.Source)
	}
	if tc.Schema != "public" {
		t.Errorf("expected schema=public, got %q", tc.Schema)
	}
	if tc.Table != "users" {
		t.Errorf("expected table=users, got %q", tc.Table)
	}

	// Target.
	if len(tc.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(tc.Targets))
	}
	tgt := tc.Targets[0]
	if tgt.Type != "indexed-memory" {
		t.Errorf("expected type=indexed-memory, got %q", tgt.Type)
	}
	if len(tgt.LookupFields) != 1 || tgt.LookupFields[0] != "email" {
		t.Errorf("expected lookup_fields=[email], got %v", tgt.LookupFields)
	}
	if tgt.BufferSize != 5000 {
		t.Errorf("expected buffer.max_size=5000, got %d", tgt.BufferSize)
	}
	if tgt.BufferPolicy != "block" {
		t.Errorf("expected buffer.policy=block, got %q", tgt.BufferPolicy)
	}
	if tgt.MaxRetries != 3 {
		t.Errorf("expected max_retries=3, got %d", tgt.MaxRetries)
	}
	if tgt.ErrorPolicy != "isolate" {
		t.Errorf("expected error_policy=isolate, got %q", tgt.ErrorPolicy)
	}

	// TTL.
	if tc.TTL == nil {
		t.Fatal("expected TTL config")
	}
	if tc.TTL.Field != "expires_at" {
		t.Errorf("expected ttl.field=expires_at, got %q", tc.TTL.Field)
	}

	// gRPC.
	if cfg.GRPC == nil || cfg.GRPC.Port != 4001 {
		t.Errorf("expected grpc.port=4001, got %v", cfg.GRPC)
	}
}

func TestParse_HTTPSyncTarget(t *testing.T) {
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
        type = http-sync
        base_url = "https://api.example.com/sync"
        batch_size = 500
        timeout_ms = 5000
        retry_count = 3

        filters = [
          { type = field-equals, field = status, value = active }
        ]
        transforms = [
          { type = drop-fields, fields = [secret, internal] }
        ]
      }
    ]
  }
]
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tgt := cfg.Tables[0].Targets[0]
	if tgt.Type != "http-sync" {
		t.Errorf("expected http-sync, got %q", tgt.Type)
	}
	if tgt.BaseURL != "https://api.example.com/sync" {
		t.Errorf("expected base_url, got %q", tgt.BaseURL)
	}
	if tgt.BatchSize != 500 {
		t.Errorf("expected batch_size=500, got %d", tgt.BatchSize)
	}
	if tgt.RetryCount != 3 {
		t.Errorf("expected retry_count=3, got %d", tgt.RetryCount)
	}

	// Filter.
	if len(tgt.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(tgt.Filters))
	}
	if tgt.Filters[0].Type != "field-equals" || tgt.Filters[0].Field != "status" || tgt.Filters[0].Value != "active" {
		t.Errorf("unexpected filter: %+v", tgt.Filters[0])
	}

	// Transform.
	if len(tgt.Transforms) != 1 {
		t.Fatalf("expected 1 transform, got %d", len(tgt.Transforms))
	}
	if tgt.Transforms[0].Type != "drop-fields" {
		t.Errorf("expected drop-fields, got %q", tgt.Transforms[0].Type)
	}
	if len(tgt.Transforms[0].Fields) != 2 {
		t.Errorf("expected 2 fields, got %v", tgt.Transforms[0].Fields)
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg, err := Parse(basicConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Errorf("unexpected validation errors: %v", errs)
	}
}

func TestValidate_NoSources(t *testing.T) {
	cfg := &Config{Tables: []TableConfig{{Source: "x", Schema: "s", Table: "t", Targets: []TargetConfig{{Type: "indexed-memory"}}}}}
	errs := cfg.Validate()
	found := false
	for _, err := range errs {
		if err.Error() == "at least one source is required" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'at least one source' error")
	}
}

func TestValidate_UnknownSource(t *testing.T) {
	cfg := &Config{
		Sources: map[string]SourceConfig{"pg": {Type: "postgresql"}},
		Tables:  []TableConfig{{Source: "unknown", Schema: "s", Table: "t", Targets: []TargetConfig{{Type: "x"}}}},
	}
	errs := cfg.Validate()
	found := false
	for _, err := range errs {
		if contains(err.Error(), "unknown source") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'unknown source' error, got %v", errs)
	}
}

func TestToEngineOptions_Basic(t *testing.T) {
	cfg, err := Parse(basicConfig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	opts, err := cfg.ToEngineOptions()
	if err != nil {
		t.Fatalf("ToEngineOptions: %v", err)
	}

	// Should have at least 2 options: WithSource + WithPipeline.
	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestToEngineOptions_UnknownSourceType(t *testing.T) {
	cfg := &Config{
		Sources: map[string]SourceConfig{"x": {Type: "nosql"}},
		Tables:  []TableConfig{{Source: "x", Schema: "s", Table: "t", Targets: []TargetConfig{{Type: "indexed-memory"}}}},
	}
	_, err := cfg.ToEngineOptions()
	if err == nil {
		t.Fatal("expected error for unknown source type")
	}
}

func TestToEngineOptions_UnknownTargetType(t *testing.T) {
	cfg := &Config{
		Sources: map[string]SourceConfig{"pg": {Type: "postgresql"}},
		Tables:  []TableConfig{{Source: "pg", Schema: "s", Table: "t", Targets: []TargetConfig{{Type: "exotic-target"}}}},
	}
	_, err := cfg.ToEngineOptions()
	if err == nil {
		t.Fatal("expected error for unknown target type")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
