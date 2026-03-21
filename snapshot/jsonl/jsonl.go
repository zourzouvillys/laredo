// Package jsonl implements a SnapshotSerializer using JSONL format.
package jsonl

import (
	"io"

	"github.com/zourzouvillys/laredo"
)

// Serializer implements laredo.SnapshotSerializer using newline-delimited JSON.
type Serializer struct{}

// New creates a new JSONL snapshot serializer.
func New() *Serializer {
	return &Serializer{}
}

var _ laredo.SnapshotSerializer = (*Serializer)(nil)

//nolint:revive // implements SnapshotSerializer.
func (s *Serializer) FormatID() string { return "jsonl" }

//nolint:revive // implements SnapshotSerializer.
func (s *Serializer) Write(info laredo.TableSnapshotInfo, rows []laredo.Row, w io.Writer) error {
	panic("not implemented")
}

//nolint:revive // implements SnapshotSerializer.
func (s *Serializer) Read(r io.Reader) (laredo.TableSnapshotInfo, []laredo.Row, error) {
	panic("not implemented")
}
