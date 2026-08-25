// Package rowpb converts a laredo.Row into the protobuf Struct the wire
// protocols carry.
//
// It exists because structpb.NewStruct accepts a narrow set of Go types — nil,
// bool, the numeric kinds, string, []byte, map[string]any and []any — and the
// rows laredo produces routinely contain values outside it. A row read through
// pgx carries time.Time for a timestamptz, [16]byte for a uuid,
// pgtype.Numeric for a numeric and netip.Prefix for an inet. Handed straight
// to structpb.NewStruct, every one of those returns an error.
//
// That error used to be discarded at every call site in service/replication
// and service/query, which turned a type problem into silent, permanent data
// loss: the encode failed, a nil Struct went on the wire, the receiving client
// skipped the row because its Row field was nil, and the journal sequence
// advanced anyway — so the replica was missing rows and believed it was fully
// caught up. Any table with a timestamptz, uuid or numeric column was
// affected, with nothing in any log to say so.
//
// So this package does two things: it maps the types laredo actually produces
// onto something structpb accepts, and it returns an error for anything it
// cannot represent rather than letting a caller drop it.
package rowpb

import (
	"database/sql/driver"
	"encoding"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zourzouvillys/laredo"
)

// maxSafeInteger is the largest integer a float64 represents exactly.
// structpb has no integer type — every number is a double — so an integer
// beyond this cannot survive the round trip.
const maxSafeInteger = 1<<53 - 1

// RowToStruct converts a row for the wire, returning an error rather than a
// partial or nil result. Callers must propagate it: an unencodable row has to
// fail its stream, because the alternative is a consumer that silently holds
// different data from its source.
func RowToStruct(row laredo.Row) (*structpb.Struct, error) {
	if row == nil {
		return nil, nil
	}
	fields := make(map[string]*structpb.Value, len(row))
	for k, v := range row {
		pv, err := ToValue(v)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", k, err)
		}
		fields[k] = pv
	}
	return &structpb.Struct{Fields: fields}, nil
}

// ToValue converts a single column value.
//
// The explicit cases cover what pgx hands back for the common column types.
// Everything else falls through to three generic escapes — TextMarshaler,
// driver.Valuer and Stringer — which between them catch most of pgtype's
// remaining types without this package importing pgx.
func ToValue(v any) (*structpb.Value, error) {
	switch t := v.(type) {
	case nil:
		return structpb.NewNullValue(), nil

	case bool:
		return structpb.NewBoolValue(t), nil

	case string:
		return structpb.NewStringValue(t), nil

	case []byte:
		// structpb encodes bytes as base64, which round-trips.
		return structpb.NewValue(t)

	case time.Time:
		// RFC 3339 with nanoseconds: sorts lexically, parses everywhere, and
		// keeps the offset a timestamptz carries.
		return structpb.NewStringValue(t.Format(time.RFC3339Nano)), nil

	case [16]byte:
		// pgx decodes uuid to a raw array; render it canonically rather than
		// as a base64 blob nobody can read in a log.
		return structpb.NewStringValue(formatUUID(t)), nil

	case int:
		return safeInt(int64(t))
	case int8:
		return structpb.NewNumberValue(float64(t)), nil
	case int16:
		return structpb.NewNumberValue(float64(t)), nil
	case int32:
		return structpb.NewNumberValue(float64(t)), nil
	case int64:
		return safeInt(t)
	case uint:
		return safeUint(uint64(t))
	case uint8:
		return structpb.NewNumberValue(float64(t)), nil
	case uint16:
		return structpb.NewNumberValue(float64(t)), nil
	case uint32:
		return structpb.NewNumberValue(float64(t)), nil
	case uint64:
		return safeUint(t)

	case float32:
		return structpb.NewNumberValue(float64(t)), nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			// JSON, and therefore structpb, has no way to say either.
			return nil, fmt.Errorf("cannot represent %v", t)
		}
		return structpb.NewNumberValue(t), nil

	case map[string]any:
		return nestedValue(t)
	case []any:
		return nestedValue(t)
	}

	// Generic escapes, in order of how faithful they are.
	if tm, ok := v.(encoding.TextMarshaler); ok {
		b, err := tm.MarshalText()
		if err != nil {
			return nil, fmt.Errorf("marshal %T: %w", v, err)
		}
		return structpb.NewStringValue(string(b)), nil
	}

	if val, ok := v.(driver.Valuer); ok {
		dv, err := val.Value()
		if err != nil {
			return nil, fmt.Errorf("value %T: %w", v, err)
		}
		// pgtype.Numeric lands here and yields a string, which is what keeps
		// its precision — a numeric turned into a double would not round-trip.
		if _, again := dv.(driver.Valuer); again {
			return nil, fmt.Errorf("unsupported recursive Valuer %T", v)
		}
		return ToValue(dv)
	}

	if s, ok := v.(fmt.Stringer); ok {
		return structpb.NewStringValue(s.String()), nil
	}

	return nil, fmt.Errorf("unsupported type %T", v)
}

// nestedValue handles jsonb, which pgx decodes into plain Go maps and slices.
//
// Nested numbers are the one place precision is still lost silently: they ride
// structpb's double like any other number, and this package cannot tell a JSON
// integer from a JSON float once encoding/json has produced a float64. Callers
// that need exact large integers inside a document must carry them as strings.
func nestedValue(v any) (*structpb.Value, error) {
	pv, err := structpb.NewValue(v)
	if err != nil {
		return nil, fmt.Errorf("encode nested value: %w", err)
	}
	return pv, nil
}

func safeInt(v int64) (*structpb.Value, error) {
	if v > maxSafeInteger || v < -maxSafeInteger {
		return nil, fmt.Errorf(
			"integer %d exceeds the range a JSON number represents exactly (±%d); "+
				"carry it as text or numeric instead", v, int64(maxSafeInteger))
	}
	return structpb.NewNumberValue(float64(v)), nil
}

func safeUint(v uint64) (*structpb.Value, error) {
	if v > maxSafeInteger {
		return nil, fmt.Errorf(
			"integer %d exceeds the range a JSON number represents exactly (%d); "+
				"carry it as text or numeric instead", v, uint64(maxSafeInteger))
	}
	return structpb.NewNumberValue(float64(v)), nil
}

func formatUUID(b [16]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	i := 0
	for n, c := range b {
		if n == 4 || n == 6 || n == 8 || n == 10 {
			out[i] = '-'
			i++
		}
		out[i] = hex[c>>4]
		out[i+1] = hex[c&0x0f]
		i += 2
	}
	return string(out)
}
