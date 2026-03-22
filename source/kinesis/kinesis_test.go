package kinesis

import (
	"encoding/json"
	"testing"

	"github.com/zourzouvillys/laredo"
)

func TestManifest_Parse(t *testing.T) {
	data := `{
		"tables": {
			"public.users": {
				"columns": [
					{"name": "id", "type": "integer", "nullable": false, "primary_key": true},
					{"name": "name", "type": "text", "nullable": false},
					{"name": "email", "type": "text", "nullable": true}
				]
			},
			"public.orders": {
				"columns": [
					{"name": "order_id", "type": "uuid", "nullable": false, "primary_key": true},
					{"name": "user_id", "type": "integer", "nullable": false},
					{"name": "total", "type": "numeric", "nullable": false}
				]
			}
		}
	}`

	var m Manifest
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(m.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(m.Tables))
	}

	users := m.Tables["public.users"]
	if len(users.Columns) != 3 {
		t.Fatalf("expected 3 columns for users, got %d", len(users.Columns))
	}
	if users.Columns[0].Name != "id" || !users.Columns[0].PrimaryKey {
		t.Error("expected first column to be id primary key")
	}
	if users.Columns[2].Name != "email" || !users.Columns[2].Nullable {
		t.Error("expected email to be nullable")
	}
}

func TestManifest_Empty(t *testing.T) {
	data := `{"tables": {}}`
	var m Manifest
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Tables) != 0 {
		t.Errorf("expected 0 tables, got %d", len(m.Tables))
	}
}

func TestPosition_Serialization(t *testing.T) {
	pos := &Position{
		S3Version:      "v1",
		ShardSequences: map[string]string{"shard-001": "12345", "shard-002": "67890"},
	}

	src := New()
	str := src.PositionToString(pos)
	if str == "" {
		t.Fatal("expected non-empty position string")
	}

	restored, err := src.PositionFromString(str)
	if err != nil {
		t.Fatalf("PositionFromString: %v", err)
	}

	restoredPos, ok := restored.(*Position)
	if !ok {
		t.Fatal("expected *Position type")
	}
	if restoredPos.S3Version != "v1" {
		t.Errorf("expected S3Version=v1, got %s", restoredPos.S3Version)
	}
	if restoredPos.ShardSequences["shard-001"] != "12345" {
		t.Errorf("shard-001 sequence mismatch")
	}
	if restoredPos.ShardSequences["shard-002"] != "67890" {
		t.Errorf("shard-002 sequence mismatch")
	}
}

func TestInit_NoManifest(t *testing.T) {
	// Without manifest key, Init should return empty schemas.
	src := New()
	schemas, err := src.Init(t.Context(), laredo.SourceConfig{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(schemas) != 0 {
		t.Errorf("expected 0 schemas without manifest, got %d", len(schemas))
	}
}

func TestOrderingGuarantee(t *testing.T) {
	src := New()
	if src.OrderingGuarantee() != laredo.PerPartitionOrder {
		t.Error("expected PerPartitionOrder")
	}
}

func TestState_Lifecycle(t *testing.T) {
	src := New()
	if src.State() != laredo.SourceClosed {
		t.Errorf("expected initial state=Closed, got %v", src.State())
	}

	_, _ = src.Init(t.Context(), laredo.SourceConfig{})
	if src.State() != laredo.SourceConnected {
		t.Errorf("expected state=Connected after Init, got %v", src.State())
	}

	_ = src.Pause(t.Context())
	if src.State() != laredo.SourcePaused {
		t.Errorf("expected state=Paused, got %v", src.State())
	}

	_ = src.Resume(t.Context())
	if src.State() != laredo.SourceStreaming {
		t.Errorf("expected state=Streaming, got %v", src.State())
	}

	_ = src.Close(t.Context())
	if src.State() != laredo.SourceClosed {
		t.Errorf("expected state=Closed, got %v", src.State())
	}
}

func TestSupportsResume(t *testing.T) {
	src := New()
	if src.SupportsResume() {
		t.Error("expected SupportsResume=false")
	}
}
