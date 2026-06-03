// Package jsonl implements the snapshotter Format interface as newline-delimited
// JSON: one row (snapshot) or one change (diff) per line.
package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/snapshotter"
)

// Format encodes snapshots and diffs as newline-delimited JSON.
type Format struct{}

// New returns a JSONL format.
func New() *Format { return &Format{} }

var _ snapshotter.Format = (*Format)(nil)

// FormatID implements snapshotter.Format.
func (*Format) FormatID() string { return "jsonl" }

// Extension implements snapshotter.Format.
func (*Format) Extension() string { return ".jsonl" }

// WriteSnapshot writes one JSON object per row.
func (*Format) WriteSnapshot(w io.Writer, rows []laredo.Row) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("jsonl: encode snapshot row: %w", err)
		}
	}
	return bw.Flush()
}

// ReadSnapshot reads a snapshot written by WriteSnapshot.
func (*Format) ReadSnapshot(r io.Reader) ([]laredo.Row, error) {
	var rows []laredo.Row
	sc := newScanner(r)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row laredo.Row
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("jsonl: decode snapshot row: %w", err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jsonl: read snapshot: %w", err)
	}
	return rows, nil
}

// jsonlChange is the wire form of a Change with a human-readable action string.
type jsonlChange struct {
	Action string     `json:"action"`
	Key    string     `json:"key"`
	Old    laredo.Row `json:"old,omitempty"`
	New    laredo.Row `json:"new,omitempty"`
}

// WriteDiff writes one JSON object per change.
func (*Format) WriteDiff(w io.Writer, changes []snapshotter.Change) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for _, c := range changes {
		if err := enc.Encode(jsonlChange{
			Action: c.Action.String(),
			Key:    c.Key,
			Old:    c.Old,
			New:    c.New,
		}); err != nil {
			return fmt.Errorf("jsonl: encode diff change: %w", err)
		}
	}
	return bw.Flush()
}

// ReadDiff reads a diff written by WriteDiff.
func (*Format) ReadDiff(r io.Reader) ([]snapshotter.Change, error) {
	var changes []snapshotter.Change
	sc := newScanner(r)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var jc jsonlChange
		if err := json.Unmarshal(line, &jc); err != nil {
			return nil, fmt.Errorf("jsonl: decode diff change: %w", err)
		}
		action, err := parseAction(jc.Action)
		if err != nil {
			return nil, err
		}
		changes = append(changes, snapshotter.Change{
			Action: action,
			Key:    jc.Key,
			Old:    jc.Old,
			New:    jc.New,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("jsonl: read diff: %w", err)
	}
	return changes, nil
}

// newScanner returns a line scanner with a large buffer for wide rows.
func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return sc
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
		return 0, fmt.Errorf("jsonl: unknown action %q", s)
	}
}
