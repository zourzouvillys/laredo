package snapshotter

import (
	"fmt"
	"strings"

	"github.com/zourzouvillys/laredo"
)

// ManifestObjectKey is the object key of a table's manifest within a destination
// for a given key prefix (e.g. "public.events/" → "public.events/manifest.json").
// Writer and Reader share this so the two never drift on object layout.
func ManifestObjectKey(keyPrefix string) string {
	return keyPrefix + "manifest.json"
}

// ArtifactObjectKey builds the object key for one encoded artifact (one format).
// ext is the format's file extension including the dot (e.g. ".jsonl"). Positions
// (WAL LSNs like "0/1A2B3C") are sanitized for use in a path. Writer and Reader
// share this so the two never drift on object layout.
func ArtifactObjectKey(keyPrefix string, art Artifact, ext string) string {
	to := sanitizePosition(art.ToPosition)
	var name string
	if art.Kind == KindSnapshot {
		name = fmt.Sprintf("snapshot-%s%s", to, ext)
	} else {
		from := "start"
		if art.FromPosition != nil {
			from = sanitizePosition(*art.FromPosition)
		}
		name = fmt.Sprintf("diff-%s-%s%s", from, to, ext)
	}
	return fmt.Sprintf("%sepoch=%d/%s", keyPrefix, art.Epoch, name)
}

func sanitizePosition(p string) string {
	if p == "" {
		return "0"
	}
	return strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(p)
}

// RowKey derives the key used to address a row in the diff buffer and in a
// reconstructed table state, from its primary-key field values. Writer and
// Reader share it so a row keyed in a snapshot matches the same row keyed in a
// diff. keyFields defaults to ["id"] when empty.
func RowKey(row laredo.Row, keyFields []string) string {
	if len(keyFields) == 0 {
		keyFields = []string{"id"}
	}
	if len(keyFields) == 1 {
		return fmt.Sprintf("%v", row[keyFields[0]])
	}
	parts := make([]any, len(keyFields))
	for i, f := range keyFields {
		parts[i] = row[f]
	}
	return fmt.Sprintf("%v", parts)
}
