// Package local implements the snapshotter Destination interface against the
// local filesystem. Artifact writes are atomic (temp file + rename); the
// manifest uses a content-hash revision for best-effort compare-and-swap.
package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zourzouvillys/laredo/snapshotter"
)

// Destination writes artifacts and the manifest under a base directory.
type Destination struct {
	base string
}

// New returns a local-filesystem destination rooted at basePath.
func New(basePath string) *Destination {
	return &Destination{base: basePath}
}

var _ snapshotter.Destination = (*Destination)(nil)

// Name implements snapshotter.Destination.
func (d *Destination) Name() string { return "local:" + d.base }

func (d *Destination) path(key string) string {
	return filepath.Join(d.base, filepath.FromSlash(key))
}

// Put implements snapshotter.Destination with an atomic temp-file + rename.
func (d *Destination) Put(_ context.Context, key string, body io.Reader) (string, int64, error) {
	full := d.path(key)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return "", 0, fmt.Errorf("local: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("local: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	n, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		return "", 0, fmt.Errorf("local: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("local: close temp: %w", err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return "", 0, fmt.Errorf("local: rename: %w", err)
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		abs = full
	}
	return "file://" + abs, n, nil
}

// Get implements snapshotter.Destination.
func (d *Destination) Get(_ context.Context, key string) ([]byte, string, bool, error) {
	data, err := os.ReadFile(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("local: read %s: %w", key, err)
	}
	return data, revOf(data), true, nil
}

// PutCAS implements best-effort compare-and-swap: it re-reads the current
// object, checks its content hash against expectedRev, then writes atomically.
// On a single host with one writer per key this is sufficient to detect an
// accidental second writer; it does not provide cross-process locking.
func (d *Destination) PutCAS(ctx context.Context, key string, body []byte, expectedRev string) (string, error) {
	_, curRev, found, err := d.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if !found {
		curRev = ""
	}
	if curRev != expectedRev {
		return "", snapshotter.ErrCASConflict
	}
	if _, _, err := d.Put(ctx, key, bytes.NewReader(body)); err != nil {
		return "", err
	}
	return revOf(body), nil
}

func revOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
