package snapshotter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zourzouvillys/laredo"
)

// ErrManifestNotFound is returned by Reader.LoadManifest when no manifest exists
// at the configured prefix.
var ErrManifestNotFound = errors.New("snapshotter: manifest not found")

// ErrUnsupportedManifestVersion is returned when the manifest is a newer schema
// than this build understands. Callers treat it as "cannot read" and fall back —
// never guess at a future format.
var ErrUnsupportedManifestVersion = errors.New("snapshotter: unsupported manifest version")

// Reader is the read-side inverse of Writer: it loads a table's manifest and
// decodes artifacts from a destination, and plans the chain of artifacts needed
// to bring a consumer from a given source position up to the archive head. It
// holds no mutable state and is safe for concurrent use.
//
// EDR-0001 makes reconstruction the consumer's job; Reader is the reusable tool
// for it. laredo's replication service uses it for cold-tier replay (EDR-0002),
// and offline consumers can use it to rebuild a table from object storage.
type Reader struct {
	dest    Destination
	prefix  string
	formats []Format // preference order; the first whose FormatID an artifact carries wins
}

// NewReader returns a Reader over dest, reading artifacts under keyPrefix and
// decoding them with the given formats, tried in order. At least one format is
// required.
func NewReader(dest Destination, keyPrefix string, formats ...Format) (*Reader, error) {
	if dest == nil {
		return nil, errors.New("snapshotter: Reader requires a destination")
	}
	if len(formats) == 0 {
		return nil, errors.New("snapshotter: Reader requires at least one format")
	}
	return &Reader{dest: dest, prefix: keyPrefix, formats: formats}, nil
}

// LoadManifest reads and parses the table manifest. It returns ErrManifestNotFound
// when none exists and ErrUnsupportedManifestVersion when the manifest is a newer
// schema than this build supports.
func (r *Reader) LoadManifest(ctx context.Context) (*Manifest, error) {
	data, _, found, err := r.dest.Get(ctx, ManifestObjectKey(r.prefix))
	if err != nil {
		return nil, fmt.Errorf("snapshotter: read manifest: %w", err)
	}
	if !found {
		return nil, ErrManifestNotFound
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("snapshotter: parse manifest: %w", err)
	}
	if m.ManifestVersion > ManifestVersion {
		return nil, fmt.Errorf("%w: manifest is v%d, this build supports up to v%d",
			ErrUnsupportedManifestVersion, m.ManifestVersion, ManifestVersion)
	}
	return &m, nil
}

// ReadSnapshot fetches and decodes a snapshot artifact's rows.
func (r *Reader) ReadSnapshot(ctx context.Context, art Artifact) ([]laredo.Row, error) {
	f := r.formatFor(art)
	if f == nil {
		return nil, fmt.Errorf("snapshotter: no configured format can decode snapshot at epoch %d", art.Epoch)
	}
	data, err := r.artifactBytes(ctx, art, f)
	if err != nil {
		return nil, err
	}
	return f.ReadSnapshot(bytes.NewReader(data))
}

// ReadDiff fetches and decodes a diff artifact's changes.
func (r *Reader) ReadDiff(ctx context.Context, art Artifact) ([]Change, error) {
	f := r.formatFor(art)
	if f == nil {
		return nil, fmt.Errorf("snapshotter: no configured format can decode diff at epoch %d", art.Epoch)
	}
	data, err := r.artifactBytes(ctx, art, f)
	if err != nil {
		return nil, err
	}
	return f.ReadDiff(bytes.NewReader(data))
}

// formatFor returns the first configured format whose FormatID the artifact
// carries, or nil if none of the artifact's formats can be decoded.
func (r *Reader) formatFor(art Artifact) Format {
	for _, f := range r.formats {
		if _, ok := art.Formats[f.FormatID()]; ok {
			return f
		}
	}
	return nil
}

func (r *Reader) artifactBytes(ctx context.Context, art Artifact, f Format) ([]byte, error) {
	key := ArtifactObjectKey(r.prefix, art, f.Extension())
	data, _, found, err := r.dest.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("snapshotter: read artifact %s: %w", key, err)
	}
	if !found {
		return nil, fmt.Errorf("snapshotter: artifact object missing: %s", key)
	}
	return data, nil
}

// ReplayPlan is the ordered chain of artifacts that brings a consumer from a
// requested source position up to HeadPosition. When Snapshot is non-nil it is a
// full base that must be applied first, then Diffs in order. When Snapshot is nil
// the consumer already holds the state at the requested position and only Diffs
// are needed.
type ReplayPlan struct {
	Snapshot     *Artifact // base to apply first; nil for a diff-only plan
	Diffs        []Artifact
	HeadPosition string // the position the plan brings the consumer to
}

// Plan selects the chain to bring a consumer at fromPosition up to the archive
// head. cmp compares two opaque position strings (negative if a<b, zero if equal,
// positive if a>b); the caller supplies it because positions are source-defined
// (e.g. PostgreSQL WAL LSNs).
//
// It returns (nil, nil) — "the archive cannot serve this consumer" — when the
// manifest is empty or holds no base snapshot. The caller falls back to a live
// snapshot.
//
// Two plan shapes:
//   - Diff-only: the manifest holds a contiguous diff chain starting exactly at
//     fromPosition and reaching the head; only those diffs are returned (the
//     consumer already has the state at fromPosition).
//   - Snapshot-base: otherwise, the newest base snapshot plus the contiguous diff
//     chain after it.
func (r *Reader) Plan(m *Manifest, fromPosition string, cmp func(a, b string) int) (*ReplayPlan, error) {
	if m == nil || len(m.Artifacts) == 0 {
		return nil, nil
	}
	var diffs []Artifact
	for _, a := range m.Artifacts {
		if a.Kind == KindDiff && a.FromPosition != nil {
			diffs = append(diffs, a)
		}
	}

	// Diff-only: an unbroken chain from exactly fromPosition to the head.
	if chain, head, ok := contiguousDiffChain(diffs, fromPosition); ok && cmp(head, m.HeadPosition) >= 0 {
		return &ReplayPlan{Diffs: chain, HeadPosition: head}, nil
	}

	// Snapshot-base: newest snapshot + the diffs after it.
	snap := m.latestSnapshot()
	if snap == nil {
		return nil, nil
	}
	chain, head, _ := contiguousDiffChain(diffs, snap.ToPosition)
	if head == "" {
		head = snap.ToPosition
	}
	return &ReplayPlan{Snapshot: snap, Diffs: chain, HeadPosition: head}, nil
}

// contiguousDiffChain returns the diffs that form an unbroken chain starting at
// startPos — a diff whose FromPosition == startPos, then each next diff whose
// FromPosition == the previous diff's ToPosition — the head position the chain
// reaches, and whether any diff started at startPos. The writer sets each diff's
// FromPosition to the previous artifact's ToPosition, so boundaries match
// exactly and string equality is the correct link test.
func contiguousDiffChain(diffs []Artifact, startPos string) (chain []Artifact, head string, ok bool) {
	byFrom := make(map[string]Artifact, len(diffs))
	for _, d := range diffs {
		byFrom[*d.FromPosition] = d
	}
	for cur := startPos; ; {
		d, found := byFrom[cur]
		if !found {
			break
		}
		chain = append(chain, d)
		head = d.ToPosition
		ok = true
		cur = d.ToPosition
	}
	return chain, head, ok
}
