package snapshotter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNopEventSink(t *testing.T) {
	var s NopEventSink
	if s.Name() != "nop" {
		t.Fatalf("name = %q", s.Name())
	}
	if err := s.Publish(context.Background(), ArtifactEvent{}); err != nil {
		t.Fatalf("nop publish: %v", err)
	}
}

func TestArtifactEvent_JSONShape(t *testing.T) {
	from := "0/100"
	ev := ArtifactEvent{
		Table:        "public.users",
		Kind:         KindDiff,
		Epoch:        2,
		FromPosition: &from,
		ToPosition:   "0/200",
		HeadPosition: "0/200",
		ManifestURI:  "users/manifest.json",
		Formats:      map[string]string{"jsonl": "s3://b/k.jsonl"},
		CreatedAt:    time.Unix(0, 0).UTC(),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ArtifactEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Table != ev.Table || got.Kind != KindDiff || got.FromPosition == nil || *got.FromPosition != "0/100" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// A base snapshot has a null from_position.
	snap, _ := json.Marshal(ArtifactEvent{Kind: KindSnapshot, ToPosition: "0/10"})
	if !strings.Contains(string(snap), `"from_position":null`) {
		t.Fatalf("snapshot event should have null from_position: %s", snap)
	}
}
