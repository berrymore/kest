package rrhttp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// A hand-written protobuf codec for the four messages of RoadRunner's
// http.v1 package (api-go/v6 http/v1/http.proto), so that kest needs no
// protobuf dependency:
//
//	message Request {
//	  string remote_addr = 1; string protocol = 2; string method = 3; string uri = 4;
//	  map<string, HeaderValue> header = 5; map<string, HeaderValue> cookies = 6;
//	  string raw_query = 7; bool parsed = 8; bytes uploads = 9;
//	  map<string, HeaderValue> attributes = 10;
//	}
//	message Response { int64 status = 1; map<string, HeaderValue> headers = 2; }
//	message HeaderValue { repeated bytes value = 1; }
//
// The encoder writes what google.golang.org/protobuf writes with
// Deterministic: true (map entries sorted by key, empty proto3 fields
// omitted, map keys and values always present); the tests compare with
// golden bytes from it.

// Protobuf wire types.
const (
	wireVarint = 0
	wireI64    = 1
	wireBytes  = 2
	wireI32    = 5
)

// Field numbers of http.v1.Request.
const (
	reqRemoteAddr = 1
	reqProtocol   = 2
	reqMethod     = 3
	reqURI        = 4
	reqHeader     = 5
	reqCookies    = 6
	reqRawQuery   = 7
	reqParsed     = 8
	reqAttributes = 10
)

// Field numbers of http.v1.Response.
const (
	respStatus  = 1
	respHeaders = 2
)

// appendRequestProto appends req's context as an http.v1.Request.
func appendRequestProto(b []byte, req *Request) []byte {
	b = appendString(b, reqRemoteAddr, req.RemoteAddr)
	b = appendString(b, reqProtocol, req.Protocol)
	b = appendString(b, reqMethod, req.Method)
	b = appendString(b, reqURI, req.URI)
	b = appendValuesMap(b, reqHeader, req.Header)
	for _, k := range slices.Sorted(maps.Keys(req.Cookies)) {
		v := req.Cookies[k]
		b = appendMapEntry(b, reqCookies, k, func(b []byte) []byte {
			return appendBytes(b, 1, []byte(v), true)
		}, size(1, len(v)))
	}
	b = appendString(b, reqRawQuery, req.RawQuery)
	if req.Parsed {
		b = binary.AppendUvarint(b, reqParsed<<3|wireVarint)
		b = append(b, 1)
	}
	b = appendValuesMap(b, reqAttributes, req.Attributes)
	return b
}

// appendValuesMap appends a map<string, HeaderValue> field.
func appendValuesMap[M ~map[string][]string](b []byte, field uint64, m M) []byte {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		values := m[k]
		n := 0
		for _, v := range values {
			n += size(1, len(v))
		}
		b = appendMapEntry(b, field, k, func(b []byte) []byte {
			for _, v := range values {
				b = appendBytes(b, 1, []byte(v), true)
			}
			return b
		}, n)
	}
	return b
}

// appendMapEntry appends one entry of a map<string, message> field: the key
// as field 1 and the message, of valueSize bytes written by value, as field 2.
func appendMapEntry(b []byte, field uint64, key string, value func([]byte) []byte, valueSize int) []byte {
	key = validUTF8(key)
	b = binary.AppendUvarint(b, field<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(size(1, len(key))+size(2, valueSize))) //nolint:gosec // sizes are positive
	b = appendBytes(b, 1, []byte(key), true)
	b = binary.AppendUvarint(b, 2<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(valueSize)) //nolint:gosec // sizes are positive
	return value(b)
}

// appendString appends a proto3 string field, omitted when empty.
func appendString(b []byte, field uint64, s string) []byte {
	return appendBytes(b, field, []byte(validUTF8(s)), false)
}

func appendBytes(b []byte, field uint64, v []byte, always bool) []byte {
	if len(v) == 0 && !always {
		return b
	}
	b = binary.AppendUvarint(b, field<<3|wireBytes)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// size is the encoded size of a length-delimited field with n bytes of data.
func size(field uint64, n int) int {
	return uvarintLen(field<<3) + uvarintLen(uint64(n)) + n //nolint:gosec // n is a length
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// decodeResponseProto decodes an http.v1.Response. As protobuf says, the
// last value of a repeated scalar field and the last entry for a map key
// win, and unknown fields are skipped.
func decodeResponseProto(b []byte, resp *Response) error {
	for len(b) > 0 {
		field, wire, data, rest, err := nextField(b)
		if err != nil {
			return err
		}
		b = rest
		switch {
		case field == respStatus && wire == wireVarint:
			v, _ := binary.Uvarint(data)
			resp.Status = clampStatus(int64(v)) //nolint:gosec // int64 fields are encoded as two's complement
		case field == respHeaders && wire == wireBytes:
			name, values, err := decodeHeaderEntry(data)
			if err != nil {
				return fmt.Errorf("headers: %w", err)
			}
			if resp.Header == nil {
				resp.Header = make(map[string][]string)
			}
			resp.Header[name] = values
		}
	}
	return nil
}

// decodeHeaderEntry decodes a map<string, HeaderValue> entry.
func decodeHeaderEntry(b []byte) (string, []string, error) {
	var (
		name   string
		values []string
	)
	for len(b) > 0 {
		field, wire, data, rest, err := nextField(b)
		if err != nil {
			return "", nil, err
		}
		b = rest
		switch {
		case field == 1 && wire == wireBytes:
			name = string(data)
		case field == 2 && wire == wireBytes:
			// A map entry's message value: a repeated field merges.
			if values, err = decodeHeaderValue(data, values); err != nil {
				return "", nil, err
			}
		}
	}
	return name, values, nil
}

// decodeHeaderValue appends the values of a HeaderValue message to values.
func decodeHeaderValue(b []byte, values []string) ([]string, error) {
	for len(b) > 0 {
		field, wire, data, rest, err := nextField(b)
		if err != nil {
			return nil, err
		}
		b = rest
		if field == 1 && wire == wireBytes {
			values = append(values, string(data))
		}
	}
	return values, nil
}

var errTruncated = errors.New("truncated protobuf message")

// nextField splits off the first field of b. data is the varint for
// wireVarint, the contents for wireBytes, and the raw bytes for wireI64 and
// wireI32.
func nextField(b []byte) (field uint64, wire int, data, rest []byte, err error) {
	tag, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, 0, nil, nil, errTruncated
	}
	field, wire, b = tag>>3, int(tag&7), b[n:]
	if field == 0 {
		return 0, 0, nil, nil, errors.New("invalid field number 0")
	}
	switch wire {
	case wireVarint:
		_, n = binary.Uvarint(b)
		if n <= 0 {
			return 0, 0, nil, nil, errTruncated
		}
		return field, wire, b[:n], b[n:], nil
	case wireI64, wireI32:
		size := 8
		if wire == wireI32 {
			size = 4
		}
		if len(b) < size {
			return 0, 0, nil, nil, errTruncated
		}
		return field, wire, b[:size], b[size:], nil
	case wireBytes:
		l, n := binary.Uvarint(b)
		if n <= 0 || l > uint64(len(b)-n) { //nolint:gosec // n <= len(b)
			return 0, 0, nil, nil, errTruncated
		}
		b = b[n:]
		return field, wire, b[:l], b[l:], nil
	default:
		return 0, 0, nil, nil, fmt.Errorf("unsupported wire type %d", wire)
	}
}
