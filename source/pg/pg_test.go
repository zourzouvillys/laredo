package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zourzouvillys/laredo"
)

func TestNew_Defaults(t *testing.T) {
	src := New()

	if src.cfg.slotMode != SlotEphemeral {
		t.Errorf("expected default SlotEphemeral, got %d", src.cfg.slotMode)
	}
	if src.cfg.slotName != "laredo_slot" {
		t.Errorf("expected default slot name 'laredo_slot', got %q", src.cfg.slotName)
	}
	if !src.cfg.publication.Create {
		t.Error("expected default publication.Create = true")
	}
	if src.SupportsResume() {
		t.Error("expected SupportsResume() = false for ephemeral mode")
	}
}

func TestNew_WithOptions(t *testing.T) {
	src := New(
		Connection("postgres://localhost/mydb"),
		SlotModeOpt(SlotStateful),
		SlotName("custom_slot"),
		Publication(PublicationConfig{Name: "my_pub", Create: false}),
	)

	if src.cfg.connString != "postgres://localhost/mydb" {
		t.Errorf("expected connection string, got %q", src.cfg.connString)
	}
	if src.cfg.slotMode != SlotStateful {
		t.Errorf("expected SlotStateful, got %d", src.cfg.slotMode)
	}
	if src.cfg.slotName != "custom_slot" {
		t.Errorf("expected 'custom_slot', got %q", src.cfg.slotName)
	}
	if src.cfg.publication.Name != "my_pub" {
		t.Errorf("expected publication name 'my_pub', got %q", src.cfg.publication.Name)
	}
	if src.cfg.publication.Create {
		t.Error("expected publication.Create = false")
	}
	if !src.SupportsResume() {
		t.Error("expected SupportsResume() = true for stateful mode")
	}
}

func TestBeforeConnect_SetsHookAndMutatesConfig(t *testing.T) {
	var seen *pgconn.Config
	hook := func(ctx context.Context, cfg *pgconn.Config) error {
		seen = cfg
		cfg.Password = "resolved-token"
		return nil
	}
	src := New(
		Connection("postgres://u@localhost/db"),
		BeforeConnect(hook),
	)
	if src.cfg.beforeConnect == nil {
		t.Fatal("expected beforeConnect hook to be registered")
	}
	cfg := &pgconn.Config{}
	if err := src.cfg.beforeConnect(context.Background(), cfg); err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if seen == nil {
		t.Fatal("hook did not receive config")
	}
	if cfg.Password != "resolved-token" {
		t.Errorf("hook mutation not visible: password=%q", cfg.Password)
	}
}

func TestBeforeConnect_ErrorIsReturned(t *testing.T) {
	want := errors.New("token generation failed")
	src := New(BeforeConnect(func(ctx context.Context, cfg *pgconn.Config) error {
		return want
	}))
	got := src.cfg.beforeConnect(context.Background(), &pgconn.Config{})
	if !errors.Is(got, want) {
		t.Errorf("expected error %v, got %v", want, got)
	}
}

func TestSource_PositionRoundTrip(t *testing.T) {
	src := New()

	pos, err := src.PositionFromString("1A/2B3C4D5E")
	if err != nil {
		t.Fatalf("PositionFromString: %v", err)
	}

	str := src.PositionToString(pos)
	if str != "1A/2B3C4D5E" {
		t.Errorf("expected '1A/2B3C4D5E', got %q", str)
	}
}

func TestSource_ComparePositions(t *testing.T) {
	src := New()

	if src.ComparePositions(LSN(1), LSN(2)) >= 0 {
		t.Error("expected 1 < 2")
	}
	if src.ComparePositions(LSN(2), LSN(1)) <= 0 {
		t.Error("expected 2 > 1")
	}
	if src.ComparePositions(LSN(5), LSN(5)) != 0 {
		t.Error("expected 5 == 5")
	}
}

func TestSource_Init_NoConnectionString(t *testing.T) {
	src := New() // no Connection() option

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{laredo.Table("public", "test")},
	})
	if err == nil {
		t.Fatal("expected error without connection string")
	}
	if src.State() != laredo.SourceError {
		t.Errorf("expected SourceError state, got %v", src.State())
	}
}

func TestSource_Init_InvalidConnectionString(t *testing.T) {
	// pgx.ParseConfig should fail on a completely invalid connection string.
	src := New(Connection("not a valid connection string"))

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{laredo.Table("public", "test")},
	})
	if err == nil {
		t.Fatal("expected error with invalid connection string")
	}
	if src.State() != laredo.SourceError {
		t.Errorf("expected SourceError state, got %v", src.State())
	}
}

func TestSource_Init_ConnectionRefused(t *testing.T) {
	// Valid connection string but nothing listening.
	src := New(Connection("postgres://localhost:59999/nonexistent"))

	_, err := src.Init(context.Background(), laredo.SourceConfig{
		Tables: []laredo.TableIdentifier{laredo.Table("public", "test")},
	})
	if err == nil {
		t.Fatal("expected error connecting to non-existent server")
	}
	if src.State() != laredo.SourceError {
		t.Errorf("expected SourceError state, got %v", src.State())
	}
}

func TestSource_StateTransitions(t *testing.T) {
	src := New()

	if src.State() != laredo.SourceClosed {
		t.Errorf("expected initial state SourceClosed, got %v", src.State())
	}

	// Close should be safe to call on an uninitialized source.
	if err := src.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if src.State() != laredo.SourceClosed {
		t.Errorf("expected SourceClosed after Close, got %v", src.State())
	}
}

func TestFormatTableSpec(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]TablePublicationConfig
		want string
	}{
		{
			name: "no options",
			opts: nil,
			want: `"public"."users"`,
		},
		{
			name: "row filter",
			opts: map[string]TablePublicationConfig{
				"public.users": {RowFilter: "active = true"},
			},
			want: `"public"."users" WHERE (active = true)`,
		},
		{
			name: "column list",
			opts: map[string]TablePublicationConfig{
				"public.users": {Columns: []string{"id", "name"}},
			},
			want: `"public"."users" ("id", "name")`,
		},
		{
			name: "both",
			opts: map[string]TablePublicationConfig{
				"public.users": {
					Columns:   []string{"id", "name"},
					RowFilter: "id > 0",
				},
			},
			want: `"public"."users" ("id", "name") WHERE (id > 0)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := New(Publication(PublicationConfig{
				Create:       true,
				TableOptions: tt.opts,
			}))
			got := src.formatTableSpec(laredo.Table("public", "users"))
			if got != tt.want {
				t.Errorf("formatTableSpec = %q, want %q", got, tt.want)
			}
		})
	}
}
