package snapshotter

import (
	"io"

	"github.com/zourzouvillys/laredo"
)

// Format encodes and decodes artifacts. Snapshots and diffs may use different
// formats, and a writer may emit several formats of the same artifact. Every
// implementation ships with a conformance test (see formattest) so formats stay
// interchangeable.
type Format interface {
	// FormatID is the stable identifier recorded in the manifest (e.g. "jsonl").
	FormatID() string
	// Extension is the file extension including the dot (e.g. ".jsonl").
	Extension() string

	// WriteSnapshot encodes a full snapshot of rows to w.
	WriteSnapshot(w io.Writer, rows []laredo.Row) error
	// ReadSnapshot decodes a snapshot previously written by WriteSnapshot.
	ReadSnapshot(r io.Reader) ([]laredo.Row, error)

	// WriteDiff encodes a diff (a sequence of changes) to w.
	WriteDiff(w io.Writer, changes []Change) error
	// ReadDiff decodes a diff previously written by WriteDiff.
	ReadDiff(r io.Reader) ([]Change, error)
}
