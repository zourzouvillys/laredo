package pg

import "testing"

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
