package laredo

import "testing"

func TestPipelineFilterFunc(t *testing.T) {
	f := PipelineFilterFunc(func(table TableIdentifier, row Row) bool {
		return row.GetString("status") == "active"
	})

	table := Table("public", "test")

	if !f.Include(table, Row{"status": "active"}) {
		t.Error("expected Include to return true for active row")
	}
	if f.Include(table, Row{"status": "inactive"}) {
		t.Error("expected Include to return false for inactive row")
	}
	if f.Include(table, Row{}) {
		t.Error("expected Include to return false for empty row")
	}
}

func TestPipelineTransformFunc(t *testing.T) {
	f := PipelineTransformFunc(func(table TableIdentifier, row Row) Row {
		return row.Without("secret")
	})

	table := Table("public", "test")
	got := f.Transform(table, Row{"name": "alice", "secret": "s3cr3t"})

	if got["name"] != "alice" {
		t.Errorf("expected name=alice, got %v", got["name"])
	}
	if _, ok := got["secret"]; ok {
		t.Error("expected secret field to be removed")
	}
}

func TestPipelineTransformFunc_Nil(t *testing.T) {
	f := PipelineTransformFunc(func(table TableIdentifier, row Row) Row {
		return nil // drop the row
	})

	table := Table("public", "test")
	got := f.Transform(table, Row{"name": "alice"})

	if got != nil {
		t.Errorf("expected nil (dropped row), got %v", got)
	}
}

func TestPipelineState_String(t *testing.T) {
	tests := []struct {
		state PipelineState
		want  string
	}{
		{PipelineInitializing, "INITIALIZING"},
		{PipelineBaselining, "BASELINING"},
		{PipelineStreaming, "STREAMING"},
		{PipelinePaused, "PAUSED"},
		{PipelineError, "ERROR"},
		{PipelineStopped, "STOPPED"},
		{PipelineState(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("PipelineState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
