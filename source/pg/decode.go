package pg

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// decodeTextValue turns a pgoutput text-format column value into the same Go
// type the baseline path produces for that column.
//
// This exists because the two paths into a laredo.Row disagreed about types,
// and every consumer downstream inherited the disagreement:
//
//   - BASELINE (initial COPY) goes through pgx's rows.Values(), which decodes
//     each column with its registered codec. An int8 arrives as int64, a
//     timestamptz as time.Time, a uuid as [16]byte, a numeric as
//     pgtype.Numeric.
//   - STREAMING (pgoutput) delivers every column as the text encoding on the
//     wire, so the same three columns arrive as "42", "2026-08-24 10:00:00+00"
//     and "a1b2...".
//
// So a row's type depended on whether the process learned about it from the
// snapshot or from a later change — and a consumer that type-asserted worked
// until the row was first updated, then broke. Subscription filters had the
// same bug in a quieter form: a numeric predicate compiled from JSON compares
// numerically against the baseline's int64 and fails against the streaming
// path's string, so a filter could match a row during catch-up and stop
// matching it live.
//
// Decoding here, with the type OID pgoutput already gives us in the RELATION
// message, makes the streaming path agree with the baseline path rather than
// the other way round. Agreeing on the *text* form would have been less code,
// but it would have pushed parsing onto every consumer and made every numeric
// filter a string comparison.
//
// An OID with no registered codec falls back to the raw text, which is what
// the caller used to get for everything — an unknown type is no worse off than
// before, and a custom enum or domain still arrives as its label.
func decodeTextValue(m *pgtype.Map, oid uint32, data []byte) (any, error) {
	dt, ok := m.TypeForOID(oid)
	if !ok {
		return string(data), nil
	}

	v, err := dt.Codec.DecodeValue(m, oid, pgtype.TextFormatCode, data)
	if err != nil {
		return nil, fmt.Errorf("decode oid %d as %s: %w", oid, dt.Name, err)
	}
	return v, nil
}
