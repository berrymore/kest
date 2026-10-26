package kv

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The kv.v1 messages of RoadRunner's API (roadrunner/api/kv/v1/kv.proto),
// encoded by hand, as rrhttp does for the HTTP payload:
//
//	message Request  { string storage = 1; repeated Item items = 2; }
//	message Item     { string key = 1; bytes value = 2; string timeout = 3; }
//	message Response { repeated Item items = 1; }

// Wire types.
const (
	wireVarint = 0
	wireI64    = 1
	wireLen    = 2
	wireI32    = 5
)

var errTruncated = errors.New("truncated message")

// request is a decoded kv.v1.Request.
type request struct {
	storage string
	items   []entry
}

func (r *request) keys() []string {
	keys := make([]string, len(r.items))
	for i, it := range r.items {
		keys[i] = it.key
	}
	return keys
}

// decodeRequest decodes a kv.v1.Request; unknown fields are skipped, as
// protobuf does.
func decodeRequest(b []byte) (*request, error) {
	r := &request{}
	err := fields(b, func(num uint64, typ int, v []byte) error {
		switch {
		case num == 1 && typ == wireLen:
			r.storage = string(v)
		case num == 2 && typ == wireLen:
			it, err := decodeItem(v)
			if err != nil {
				return fmt.Errorf("item %d: %w", len(r.items), err)
			}
			r.items = append(r.items, it)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid kv.v1.Request: %w", err)
	}
	return r, nil
}

func decodeItem(b []byte) (entry, error) {
	var e entry
	err := fields(b, func(num uint64, typ int, v []byte) error {
		if typ != wireLen {
			return nil
		}
		switch num {
		case 1:
			e.key = string(v)
		case 2:
			e.value = v
		case 3:
			e.timeout = string(v)
		}
		return nil
	})
	return e, err
}

// fields calls f for each field of the message b. A length-delimited value
// is passed as its bytes, and the others as nil.
func fields(b []byte, f func(num uint64, typ int, v []byte) error) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return errTruncated
		}
		b = b[n:]
		num, typ := tag>>3, int(tag&7)
		if num == 0 {
			return errors.New("field number 0")
		}
		var v []byte
		switch typ {
		case wireVarint:
			if _, n = binary.Uvarint(b); n <= 0 {
				return errTruncated
			}
			b = b[n:]
		case wireI64, wireI32:
			size := 8
			if typ == wireI32 {
				size = 4
			}
			if len(b) < size {
				return errTruncated
			}
			b = b[size:]
		case wireLen:
			size, n := binary.Uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) { //nolint:gosec // len(b) >= n
				return errTruncated
			}
			v, b = b[n:n+int(size)], b[n+int(size):] //nolint:gosec // size is within len(b)
		default:
			return fmt.Errorf("unsupported wire type %d", typ)
		}
		if err := f(num, typ, v); err != nil {
			return err
		}
	}
	return nil
}

// encodeResponse encodes a kv.v1.Response with entries as its items, fields
// in number order and empty ones left out, as protobuf's encoders do.
func encodeResponse(entries []entry) []byte {
	var b []byte
	for _, e := range entries {
		b = binary.AppendUvarint(b, 1<<3|wireLen)
		b = binary.AppendUvarint(b, uint64(itemSize(e))) //nolint:gosec // a length
		b = appendString(b, 1, e.key)
		b = appendString(b, 2, string(e.value))
		b = appendString(b, 3, e.timeout)
	}
	return b
}

func itemSize(e entry) int {
	return fieldSize(len(e.key)) + fieldSize(len(e.value)) + fieldSize(len(e.timeout))
}

// fieldSize is the size of a length-delimited field with a one-byte tag, 0
// when empty.
func fieldSize(n int) int {
	if n == 0 {
		return 0
	}
	return 1 + uvarintSize(uint64(n)) + n //nolint:gosec // a length
}

func uvarintSize(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func appendString(b []byte, num uint64, s string) []byte {
	if s == "" {
		return b
	}
	b = binary.AppendUvarint(b, num<<3|wireLen)
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}
