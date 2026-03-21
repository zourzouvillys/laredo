// Package jsonl implements a SnapshotSerializer using JSONL (newline-delimited JSON) format.
//
// Format: the first line is a JSON-encoded TableSnapshotInfo header. Each subsequent
// line is a JSON-encoded Row (map[string]any). All standard Value types are supported:
// nil, string, int/float, bool, time.Time (RFC 3339), []byte (base64), and nested JSON.
package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
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
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	// First line: header with table metadata.
	if err := enc.Encode(info); err != nil {
		return fmt.Errorf("jsonl: encode header: %w", err)
	}

	// Subsequent lines: one row per line.
	for _, row := range rows {
		if err := enc.Encode(map[string]any(row)); err != nil {
			return fmt.Errorf("jsonl: encode row: %w", err)
		}
	}

	return nil
}

//nolint:revive // implements SnapshotSerializer.
func (s *Serializer) Read(r io.Reader) (laredo.TableSnapshotInfo, []laredo.Row, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // up to 10MB per line

	// First line: header.
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return laredo.TableSnapshotInfo{}, nil, fmt.Errorf("jsonl: read header: %w", err)
		}
		return laredo.TableSnapshotInfo{}, nil, fmt.Errorf("jsonl: empty input")
	}

	var info laredo.TableSnapshotInfo
	if err := json.Unmarshal(scanner.Bytes(), &info); err != nil {
		return laredo.TableSnapshotInfo{}, nil, fmt.Errorf("jsonl: decode header: %w", err)
	}

	// Subsequent lines: rows.
	var rows []laredo.Row
	for scanner.Scan() {
		var row laredo.Row
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return info, nil, fmt.Errorf("jsonl: decode row: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return info, nil, fmt.Errorf("jsonl: scan: %w", err)
	}

	return info, rows, nil
}
