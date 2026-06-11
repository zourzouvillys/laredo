package snapshotter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// manifestKey is the object key of the manifest within each destination.
func (w *Writer) manifestKey() string { return ManifestObjectKey(w.cfg.KeyPrefix) }

// artifactKey builds the object key for one encoded artifact.
func (w *Writer) artifactKey(art Artifact, ext string) string {
	return ArtifactObjectKey(w.cfg.KeyPrefix, art, ext)
}

// putArtifact writes one encoded format to every destination and returns the
// reference recorded in the manifest (the primary destination's URI + size). An
// artifact is durable only once written to all destinations.
func (w *Writer) putArtifact(ctx context.Context, art Artifact, formatID, ext string, data []byte) (FormatRef, error) {
	key := w.artifactKey(art, ext)
	var primary FormatRef
	for i, d := range w.cfg.Destinations {
		uri, size, err := d.Put(ctx, key, bytes.NewReader(data))
		if err != nil {
			return FormatRef{}, fmt.Errorf("put %s to %s: %w", key, d.Name(), err)
		}
		if i == 0 {
			primary = FormatRef{URI: uri, SizeBytes: size}
		}
	}
	_ = formatID
	return primary, nil
}

// loadManifest reads any existing manifest from the destinations so a restart
// resumes the chain (epoch, head position) and records each destination's
// current revision for compare-and-swap.
func (w *Writer) loadManifest(ctx context.Context) error {
	key := w.manifestKey()
	for _, d := range w.cfg.Destinations {
		data, rev, found, err := d.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read manifest from %s: %w", d.Name(), err)
		}
		w.revs[d.Name()] = rev
		if found && d.Name() == w.cfg.Destinations[0].Name() {
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				return fmt.Errorf("parse manifest from %s: %w", d.Name(), err)
			}
			w.mu.Lock()
			w.manifest = &m
			w.epoch = m.Epoch
			w.fromPos = m.HeadPosition
			w.headPos = m.HeadPosition
			w.mu.Unlock()
		}
	}
	return nil
}

// commit appends the artifact to the manifest and writes the new manifest to
// every destination with compare-and-swap. This is the artifact's commit point.
func (w *Writer) commit(ctx context.Context, art Artifact) error {
	w.mu.Lock()
	m := w.manifest
	m.ManifestVersion = ManifestVersion
	m.Table = w.cfg.Table
	if art.Kind == KindSnapshot {
		m.Epoch = art.Epoch
	}
	m.UpdatedAt = w.now().UTC()
	m.HeadPosition = art.ToPosition
	m.Artifacts = append(m.Artifacts, art)
	m.prune(w.cfg.KeepEpochs)
	data, err := json.MarshalIndent(m, "", "  ")
	w.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	key := w.manifestKey()
	for _, d := range w.cfg.Destinations {
		newRev, err := d.PutCAS(ctx, key, data, w.revs[d.Name()])
		if err != nil {
			return fmt.Errorf("commit manifest to %s: %w", d.Name(), err)
		}
		w.revs[d.Name()] = newRev
	}
	return nil
}

// emit publishes an advisory event to each sink. Best-effort: failures are
// logged but never roll back the committed artifact.
func (w *Writer) emit(ctx context.Context, art Artifact) {
	if len(w.cfg.Sinks) == 0 {
		return
	}
	formats := make(map[string]string, len(art.Formats))
	for id, ref := range art.Formats {
		formats[id] = ref.URI
	}
	ev := ArtifactEvent{
		Table:        w.cfg.Table,
		Kind:         art.Kind,
		Epoch:        art.Epoch,
		FromPosition: art.FromPosition,
		ToPosition:   art.ToPosition,
		HeadPosition: art.ToPosition,
		ManifestURI:  w.manifestKey(),
		Formats:      formats,
		CreatedAt:    art.CreatedAt,
	}
	for _, s := range w.cfg.Sinks {
		if err := s.Publish(ctx, ev); err != nil {
			w.log.Warn("event publish failed", "sink", s.Name(), "error", err)
		}
	}
}
