// Package protobuf implements the snapshotter Format interface as a stream of
// length-delimited protobuf messages: one SnapshotRow (snapshot) or DiffChange
// (diff) per record.
package protobuf

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
	snapshotterv1 "github.com/zourzouvillys/laredo/gen/laredo/snapshotter/v1"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Format encodes snapshots and diffs as length-delimited protobuf.
type Format struct{}

// New returns a protobuf format.
func New() *Format { return &Format{} }

var _ snapshotter.Format = (*Format)(nil)

// FormatID implements snapshotter.Format.
func (*Format) FormatID() string { return "protobuf" }

// Extension implements snapshotter.Format.
func (*Format) Extension() string { return ".pb" }

// WriteSnapshot implements snapshotter.Format.
func (*Format) WriteSnapshot(w io.Writer, rows []laredo.Row) error {
	bw := bufio.NewWriter(w)
	for _, row := range rows {
		s, err := structpb.NewStruct(map[string]any(row))
		if err != nil {
			return fmt.Errorf("protobuf: encode snapshot row: %w", err)
		}
		if _, err := protodelim.MarshalTo(bw, &snapshotterv1.SnapshotRow{Row: s}); err != nil {
			return fmt.Errorf("protobuf: write snapshot row: %w", err)
		}
	}
	return bw.Flush()
}

// ReadSnapshot implements snapshotter.Format.
func (*Format) ReadSnapshot(r io.Reader) ([]laredo.Row, error) {
	br := bufio.NewReader(r)
	var rows []laredo.Row
	for {
		var msg snapshotterv1.SnapshotRow
		if err := protodelim.UnmarshalFrom(br, &msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("protobuf: read snapshot row: %w", err)
		}
		rows = append(rows, laredo.Row(msg.GetRow().AsMap()))
	}
	return rows, nil
}

// WriteDiff implements snapshotter.Format.
func (*Format) WriteDiff(w io.Writer, changes []snapshotter.Change) error {
	bw := bufio.NewWriter(w)
	for _, c := range changes {
		msg := &snapshotterv1.DiffChange{Action: c.Action.String(), Key: c.Key}
		if c.Old != nil {
			s, err := structpb.NewStruct(map[string]any(c.Old))
			if err != nil {
				return fmt.Errorf("protobuf: encode diff old: %w", err)
			}
			msg.Old = s
		}
		if c.New != nil {
			s, err := structpb.NewStruct(map[string]any(c.New))
			if err != nil {
				return fmt.Errorf("protobuf: encode diff new: %w", err)
			}
			msg.New = s
		}
		if _, err := protodelim.MarshalTo(bw, msg); err != nil {
			return fmt.Errorf("protobuf: write diff change: %w", err)
		}
	}
	return bw.Flush()
}

// ReadDiff implements snapshotter.Format.
func (*Format) ReadDiff(r io.Reader) ([]snapshotter.Change, error) {
	br := bufio.NewReader(r)
	var changes []snapshotter.Change
	for {
		var msg snapshotterv1.DiffChange
		if err := protodelim.UnmarshalFrom(br, &msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("protobuf: read diff change: %w", err)
		}
		action, err := parseAction(msg.GetAction())
		if err != nil {
			return nil, err
		}
		c := snapshotter.Change{Action: action, Key: msg.GetKey()}
		if msg.GetOld() != nil {
			c.Old = laredo.Row(msg.GetOld().AsMap())
		}
		if msg.GetNew() != nil {
			c.New = laredo.Row(msg.GetNew().AsMap())
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func parseAction(s string) (laredo.ChangeAction, error) {
	switch s {
	case laredo.ActionInsert.String():
		return laredo.ActionInsert, nil
	case laredo.ActionUpdate.String():
		return laredo.ActionUpdate, nil
	case laredo.ActionDelete.String():
		return laredo.ActionDelete, nil
	case laredo.ActionTruncate.String():
		return laredo.ActionTruncate, nil
	default:
		return 0, fmt.Errorf("protobuf: unknown action %q", s)
	}
}
