package engine

import "testing"

func intCompare(a, b any) int {
	ai, bi := a.(int), b.(int)
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

func TestAckTracker_SinglePipeline(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)

	// No confirmed position yet.
	pos, advanced := at.AckPosition("src1")
	if pos != nil || advanced {
		t.Error("expected no position before any confirm")
	}

	at.Confirm("p1", 5)
	pos, advanced = at.AckPosition("src1")
	if pos != 5 || !advanced {
		t.Errorf("expected position=5, advanced=true, got %v, %v", pos, advanced)
	}

	// Same position — no advance.
	_, advanced = at.AckPosition("src1")
	if advanced {
		t.Error("expected no advance on same position")
	}

	// New position.
	at.Confirm("p1", 10)
	pos, advanced = at.AckPosition("src1")
	if pos != 10 || !advanced {
		t.Errorf("expected position=10, advanced=true, got %v, %v", pos, advanced)
	}
}

func TestAckTracker_MultiplePipelines(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)
	at.RegisterPipeline("p2", "src1", intCompare)

	// Only p1 confirmed — can't advance (p2 hasn't confirmed).
	at.Confirm("p1", 5)
	pos, advanced := at.AckPosition("src1")
	if pos != nil || advanced {
		t.Error("expected no position when not all pipelines confirmed")
	}

	// Both confirmed — minimum is used.
	at.Confirm("p2", 3)
	pos, advanced = at.AckPosition("src1")
	if pos != 3 || !advanced {
		t.Errorf("expected position=3 (minimum), got %v, %v", pos, advanced)
	}

	// p2 advances.
	at.Confirm("p2", 7)
	pos, advanced = at.AckPosition("src1")
	if pos != 5 || !advanced {
		t.Errorf("expected position=5 (p1 is the minimum), got %v, %v", pos, advanced)
	}
}

func TestAckTracker_SkippedPipeline(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)
	at.RegisterPipeline("p2", "src1", intCompare)

	at.Confirm("p1", 5)
	// p2 hasn't confirmed — normally blocks.
	_, advanced := at.AckPosition("src1")
	if advanced {
		t.Error("expected no advance with unconfirmed pipeline")
	}

	// Skip p2 (e.g., ERROR state).
	at.Skip("p2")
	pos, advanced := at.AckPosition("src1")
	if pos != 5 || !advanced {
		t.Errorf("expected position=5 after skipping p2, got %v, %v", pos, advanced)
	}
}

func TestAckTracker_MultipleSources(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)
	at.RegisterPipeline("p2", "src2", intCompare)

	at.Confirm("p1", 10)
	at.Confirm("p2", 20)

	pos1, adv1 := at.AckPosition("src1")
	pos2, adv2 := at.AckPosition("src2")

	if pos1 != 10 || !adv1 {
		t.Errorf("src1: expected 10/true, got %v/%v", pos1, adv1)
	}
	if pos2 != 20 || !adv2 {
		t.Errorf("src2: expected 20/true, got %v/%v", pos2, adv2)
	}
}

func TestAckTracker_UnknownSource(t *testing.T) {
	at := NewAckTracker()
	pos, advanced := at.AckPosition("nonexistent")
	if pos != nil || advanced {
		t.Error("expected nil for unknown source")
	}
}

func TestAckTracker_ConfirmAll_FillsUnconfirmedPipelines(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)
	at.RegisterPipeline("p2", "src1", intCompare)

	// Heartbeat ahead of any event — both pipelines jump to 42.
	at.ConfirmAll("src1", 42)

	pos, advanced := at.AckPosition("src1")
	if pos != 42 || !advanced {
		t.Errorf("expected position=42 after heartbeat, got %v, %v", pos, advanced)
	}
}

func TestAckTracker_ConfirmAll_AdvancesOnlyWhenAhead(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)

	at.Confirm("p1", 100)
	// Regressing heartbeat — should not pull the pipeline backwards.
	at.ConfirmAll("src1", 50)

	pos, advanced := at.AckPosition("src1")
	if pos != 100 || !advanced {
		t.Errorf("expected position to stay at 100, got %v, %v", pos, advanced)
	}
}

func TestAckTracker_ConfirmAll_IgnoresOtherSources(t *testing.T) {
	at := NewAckTracker()
	at.RegisterPipeline("p1", "src1", intCompare)
	at.RegisterPipeline("p2", "src2", intCompare)

	// Heartbeat on src1 must not touch src2's pipeline.
	at.ConfirmAll("src1", 200)

	if _, advanced := at.AckPosition("src2"); advanced {
		t.Error("src2 should not advance on a src1 heartbeat")
	}

	pos, advanced := at.AckPosition("src1")
	if pos != 200 || !advanced {
		t.Errorf("src1: expected position=200, got %v, %v", pos, advanced)
	}
}
