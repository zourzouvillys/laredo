// Package deadletter provides the dead letter store interface and implementations.
package deadletter

import "github.com/zourzouvillys/laredo"

// Entry is a dead letter record.
type Entry struct {
	PipelineID string
	Change     laredo.ChangeEvent
	Error      laredo.ErrorInfo
}

// Store persists failed changes for later inspection or replay.
type Store interface {
	Write(pipelineID string, change laredo.ChangeEvent, err laredo.ErrorInfo) error
	Read(pipelineID string, limit int) ([]Entry, error)
	Replay(pipelineID string, target laredo.SyncTarget) error
	Purge(pipelineID string) error
}
