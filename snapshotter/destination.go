package snapshotter

import (
	"context"
	"errors"
	"io"
)

// ErrCASConflict is returned by Destination.PutCAS when the stored object's
// revision no longer matches the expected revision — i.e. another writer
// advanced it. The caller should re-read and retry.
var ErrCASConflict = errors.New("snapshotter: compare-and-swap conflict")

// Destination is where artifact bytes land and where the manifest lives. A
// writer may have several destinations; an artifact is durable only once
// written to all of them. The manifest is read with Get and committed with
// PutCAS so concurrent writers cannot clobber each other.
type Destination interface {
	// Name identifies the destination in logs and metrics.
	Name() string

	// Put writes (creating or overwriting) the object at key and returns its
	// canonical URI and byte size. Artifact keys are unique, so Put needs no
	// concurrency control.
	Put(ctx context.Context, key string, body io.Reader) (uri string, size int64, err error)

	// Get reads the object at key. found is false (with nil error) when the
	// object does not exist. rev is an opaque revision token for compare-and-swap.
	Get(ctx context.Context, key string) (data []byte, rev string, found bool, err error)

	// PutCAS writes body to key only if the stored object's revision equals
	// expectedRev (use "" to require that the object does not yet exist). It
	// returns the new revision, or ErrCASConflict if the precondition failed.
	PutCAS(ctx context.Context, key string, body []byte, expectedRev string) (newRev string, err error)
}
