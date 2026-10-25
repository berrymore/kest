package rrhttp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker"
)

// encodeResponseProto encodes an http.v1.Response, for tests.
func encodeResponseProto(status int64, header map[string][]string) []byte {
	var b []byte
	if status != 0 {
		b = binary.AppendUvarint(b, respStatus<<3|wireVarint)
		b = binary.AppendUvarint(b, uint64(status))
	}
	return appendValuesMap(b, respHeaders, header)
}

func TestDecodeResponseProtoGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "response_full.pb"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Context: data, Body: []byte("body")})
	if err != nil {
		t.Fatal(err)
	}
	want := &Response{
		Status: 201,
		Header: map[string][]string{
			"Content-Type": {"application/json"},
			"Set-Cookie":   {"a=1; Path=/", "b=2; Path=/"},
			"X-Empty":      nil,
			"X-Long":       {strings.Repeat("v", 300)},
		},
		Body: []byte("body"),
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("response:\n got %+v\nwant %+v", resp, want)
	}
	// The test encoder agrees with the official one.
	if enc := encodeResponseProto(201, want.Header); !bytes.Equal(enc, data) {
		t.Errorf("appendResponseProto differs from the golden file:\n got %x\nwant %x", enc, data)
	}

	negative, err := os.ReadFile(filepath.Join("testdata", "response_negative_status.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Context: negative}); !isType[*ResponseError](err) {
		t.Errorf("status -1: err = %v, want a *ResponseError", err)
	}
}

func TestDecodeResponseProto(t *testing.T) {
	field := func(num uint64, wire int, payload ...byte) []byte {
		return append(binary.AppendUvarint(nil, num<<3|uint64(wire)), payload...)
	}
	bytesField := func(num uint64, data []byte) []byte {
		return append(binary.AppendUvarint(field(num, wireBytes), uint64(len(data))), data...)
	}
	entry := func(name string, values ...[]byte) []byte {
		var v []byte
		for _, x := range values {
			v = append(v, x...)
		}
		return bytesField(respHeaders, append(bytesField(1, []byte(name)), bytesField(2, v)...))
	}
	value := func(s string) []byte { return bytesField(1, []byte(s)) }
	join := slices.Concat[[]byte]

	tests := []struct {
		name    string
		data    []byte
		status  int
		headers map[string][]string
		wantErr string
	}{
		{"status only", encodeResponseProto(404, nil), 404, nil, ""},
		{"unknown fields are skipped", join(
			field(3, wireVarint, 0x96, 0x01),
			field(4, wireI64, 1, 2, 3, 4, 5, 6, 7, 8),
			field(5, wireI32, 1, 2, 3, 4),
			bytesField(6, []byte("x")),
			encodeResponseProto(200, nil),
		), 200, nil, ""},
		{"the last status wins", join(encodeResponseProto(500, nil), encodeResponseProto(202, nil)), 202, nil, ""},
		{"the last entry for a key wins", join(encodeResponseProto(200, nil), entry("X", value("a")), entry("X", value("b"))),
			200, map[string][]string{"X": {"b"}}, ""},
		{"a split value merges", join(encodeResponseProto(200, nil),
			bytesField(respHeaders, join(bytesField(1, []byte("X")), bytesField(2, value("a")), bytesField(2, value("b"))))),
			200, map[string][]string{"X": {"a", "b"}}, ""},
		{"no status", entry("X", value("a")), 0, nil, "invalid status 0"},
		{"1xx", encodeResponseProto(103, nil), 0, nil, "invalid status 103"},
		{"600", encodeResponseProto(600, nil), 0, nil, "invalid status 600"},
		{"huge status", encodeResponseProto(1<<40, nil), 0, nil, "invalid status -1"},
		{"truncated tag", []byte{0x80}, 0, nil, "truncated"},
		{"truncated length", join(field(2, wireBytes), []byte{5, 1}), 0, nil, "truncated"},
		{"truncated fixed64", field(4, wireI64, 1, 2), 0, nil, "truncated"},
		{"truncated varint", field(1, wireVarint, 0x80), 0, nil, "truncated"},
		{"group", field(3, 3), 0, nil, "unsupported wire type 3"},
		{"field zero", []byte{0x00, 0x01}, 0, nil, "invalid field number 0"},
		{"bad entry", bytesField(respHeaders, []byte{0x0a, 0x05}), 0, nil, "headers: truncated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Context: tt.data})
			if tt.wantErr != "" {
				if !isType[*ResponseError](err) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want a *ResponseError containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tt.status || !reflect.DeepEqual(resp.Header, tt.headers) {
				t.Errorf("got %d %v, want %d %v", resp.Status, resp.Header, tt.status, tt.headers)
			}
		})
	}
}

func TestDecodeResponseJSON(t *testing.T) {
	tests := []struct {
		name    string
		codec   relay.Flags
		context string
		status  int
		headers map[string][]string
		wantErr string
	}{
		{"headers", relay.CodecJSON, `{"status":201,"headers":{"Content-Type":["text/plain"],"Set-Cookie":["a=1","b=2"]}}`,
			201, map[string][]string{"Content-Type": {"text/plain"}, "Set-Cookie": {"a=1", "b=2"}}, ""},
		{"no codec flag", 0, `{"status":200,"headers":{}}`, 200, map[string][]string{}, ""},
		{"empty PHP array", relay.CodecJSON, `{"status":204,"headers":[]}`, 204, nil, ""},
		{"null headers", relay.CodecJSON, `{"status":200,"headers":null}`, 200, nil, ""},
		{"no headers", relay.CodecJSON, `{"status":200}`, 200, nil, ""},
		{"single string", relay.CodecJSON, `{"status":200,"headers":{"X":"one"}}`, 200, map[string][]string{"X": {"one"}}, ""},
		{"escaped slashes", relay.CodecJSON, `{"status":200,"headers":{"Location":["http:\/\/x\/"]}}`, 200,
			map[string][]string{"Location": {"http://x/"}}, ""},
		{"number value", relay.CodecJSON, `{"status":200,"headers":{"X":[1]}}`, 0, nil, `header "X"`},
		{"list headers", relay.CodecJSON, `{"status":200,"headers":["x"]}`, 0, nil, "decode JSON headers"},
		{"not JSON", relay.CodecJSON, `status: 200`, 0, nil, "decode JSON context"},
		{"status 99", relay.CodecJSON, `{"status":99}`, 0, nil, "invalid status 99"},
		{"status string", relay.CodecJSON, `{"status":"200"}`, 0, nil, "decode JSON context"},
		{"huge status", relay.CodecJSON, `{"status":10000000000}`, 0, nil, "invalid status -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := DecodeResponse(worker.Payload{Codec: tt.codec, Context: []byte(tt.context)})
			if tt.wantErr != "" {
				if !isType[*ResponseError](err) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want a *ResponseError containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != tt.status || !reflect.DeepEqual(resp.Header, tt.headers) {
				t.Errorf("got %d %v, want %d %v", resp.Status, resp.Header, tt.status, tt.headers)
			}
		})
	}
}

func TestDecodeResponseEmptyContext(t *testing.T) {
	resp, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Body: []byte("hi")})
	if err != nil || resp.Status != http.StatusOK || resp.Header != nil || string(resp.Body) != "hi" {
		t.Errorf("got %+v, %v", resp, err)
	}
}

// serve answers every request with resp over a real HTTP server, so that
// framing and trailers are what a client sees.
func serve(t *testing.T, resp *Response) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := WriteResponse(w, r, resp); err != nil {
			t.Errorf("WriteResponse: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// result is what a client got; the body is read and closed.
type result struct {
	*http.Response
	body string
}

func do(t *testing.T, method, url string) result {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{resp, string(body)}
}

func TestWriteResponse(t *testing.T) {
	srv := serve(t, &Response{
		Status: http.StatusCreated,
		Header: map[string][]string{
			"content-type":      {"text/plain"},
			"Set-Cookie":        {"a=1; Path=/", "b=2; Path=/"},
			"Content-Length":    {"999"},
			"Transfer-Encoding": {"chunked"},
			"Bad Name":          {"x"},
			"":                  {"x"},
			"X-Injected":        {"ok", "a\r\nX-Evil: 1", "nul\x00"},
		},
		Body: []byte("hello"),
	})
	resp := do(t, http.MethodGet, srv.URL)
	if resp.StatusCode != http.StatusCreated || resp.body != "hello" {
		t.Fatalf("got %d %q", resp.StatusCode, resp.body)
	}
	if resp.ContentLength != 5 || len(resp.TransferEncoding) != 0 {
		t.Errorf("Content-Length %d, Transfer-Encoding %v; want 5 and none", resp.ContentLength, resp.TransferEncoding)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Values("Set-Cookie"); !reflect.DeepEqual(got, []string{"a=1; Path=/", "b=2; Path=/"}) {
		t.Errorf("Set-Cookie = %q", got)
	}
	if got := resp.Header.Values("X-Injected"); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Errorf("X-Injected = %q", got)
	}
	if resp.Header.Get("X-Evil") != "" || resp.Header.Get("Bad Name") != "" {
		t.Errorf("invalid headers got through: %v", resp.Header)
	}
}

func TestWriteResponseDropped(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	dropped, err := WriteResponse(rec, r, &Response{Status: 200, Header: map[string][]string{
		"Bad Name": {"a", "b"},
		"X":        {"ok", "bad\n"},
	}})
	if err != nil || dropped != 3 {
		t.Errorf("dropped %d, %v; want 3", dropped, err)
	}
}

func TestWriteResponseNoBody(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		status        int
		contentLength string
		wantLength    int64 // -1: unknown
	}{
		{"head keeps the worker's length", http.MethodHead, 200, "1234", 1234},
		{"head drops an invalid length", http.MethodHead, 200, "-1", -1},
		{"head without length", http.MethodHead, 200, "", -1},
		{"204", http.MethodGet, http.StatusNoContent, "5", 0},
		{"304", http.MethodGet, http.StatusNotModified, "5", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := map[string][]string{}
			if tt.contentLength != "" {
				header["Content-Length"] = []string{tt.contentLength}
			}
			srv := serve(t, &Response{Status: tt.status, Header: header, Body: []byte("hello")})
			resp := do(t, tt.method, srv.URL)
			if resp.StatusCode != tt.status || resp.body != "" {
				t.Errorf("got %d %q, want %d and no body", resp.StatusCode, resp.body, tt.status)
			}
			if tt.method == http.MethodHead && resp.ContentLength != tt.wantLength {
				t.Errorf("Content-Length = %d, want %d", resp.ContentLength, tt.wantLength)
			}
		})
	}
}

func TestWriteResponseTrailers(t *testing.T) {
	srv := serve(t, &Response{
		Status: http.StatusOK,
		Header: map[string][]string{
			"Trailer":        {"x-checksum, X-Missing", "Content-Length"},
			"X-Checksum":     {"abc"},
			"Content-Length": {"5"},
			"Content-Type":   {"text/plain"},
		},
		Body: []byte("hello"),
	})
	resp := do(t, http.MethodGet, srv.URL)
	if resp.body != "hello" {
		t.Fatalf("body = %q", resp.body)
	}
	if got := resp.Trailer.Get("X-Checksum"); got != "abc" {
		t.Errorf("trailer X-Checksum = %q; trailers %v, headers %v", got, resp.Trailer, resp.Header)
	}
	if resp.Header.Get("X-Checksum") != "" {
		t.Error("X-Checksum also sent as a header")
	}
	if !slices.Equal(slices.Sorted(maps.Keys(resp.Trailer)), []string{"X-Checksum"}) {
		t.Errorf("trailers = %v", resp.Trailer)
	}
}

func TestErrorStatus(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&RequestError{Err: &http.MaxBytesError{Limit: 1}}, 413},
		{&RequestError{Err: errors.New("bad form")}, 400},
		{&RequestError{Err: &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}}, 408},
		{worker.ErrQueueFull, 503},
		{worker.ErrNoWorker, 503},
		{worker.ErrStopped, 503},
		{&worker.ProcessError{Op: "exec", Err: worker.ErrExecTimeout}, 504},
		{fmt.Errorf("exec: %w", context.DeadlineExceeded), 504},
		{&worker.JobError{Message: "boom"}, 500},
		{&worker.ProcessError{Op: "exec", Err: io.EOF}, 502},
		{worker.ErrStopRequested, 502},
		{&ResponseError{Err: errors.New("bad")}, 502},
		{errors.New("other"), 502},
	}
	for _, tt := range tests {
		if got := ErrorStatus(tt.err); got != tt.want {
			t.Errorf("ErrorStatus(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}

	rec := httptest.NewRecorder()
	if status := WriteError(rec, worker.ErrNoWorker); status != 503 || rec.Code != 503 ||
		strings.TrimSpace(rec.Body.String()) != "Service Unavailable" {
		t.Errorf("WriteError: %d %d %q", status, rec.Code, rec.Body.String())
	}
}

func FuzzDecodeResponseProto(f *testing.F) {
	golden, _ := os.ReadFile(filepath.Join("testdata", "response_full.pb"))
	f.Add(golden)
	f.Add(encodeResponseProto(200, map[string][]string{"X": {"a", "b"}}))
	f.Add([]byte{0x08, 0xc8, 0x01, 0x12, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		resp, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Context: data})
		if err != nil {
			if !isType[*ResponseError](err) {
				t.Fatalf("err %v is not a *ResponseError", err)
			}
			return
		}
		if resp.Status < 200 || resp.Status > 599 {
			t.Fatalf("status %d accepted", resp.Status)
		}
		// Re-encoding and decoding gives the same response. The encoder
		// replaces invalid UTF-8 in names, which can merge two of them.
		for k := range resp.Header {
			if !utf8.ValidString(k) {
				return
			}
		}
		again, err := DecodeResponse(worker.Payload{Codec: relay.CodecProto, Context: encodeResponseProto(int64(resp.Status), resp.Header)})
		if err != nil || again.Status != resp.Status || !equalHeaders(again.Header, resp.Header) {
			t.Fatalf("round trip: %+v, %v; want %+v", again, err, resp)
		}
	})
}

func equalHeaders(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, slices.Equal)
}

func FuzzDecodeResponseJSON(f *testing.F) {
	for _, s := range []string{`{"status":200,"headers":{"X":["a"]}}`, `{"status":204,"headers":[]}`, `{"status":200,"headers":{"X":"a"}}`, `{}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		resp, err := DecodeResponse(worker.Payload{Codec: relay.CodecJSON, Context: data})
		if err != nil {
			if !isType[*ResponseError](err) {
				t.Fatalf("err %v is not a *ResponseError", err)
			}
			return
		}
		if resp.Status < 200 || resp.Status > 599 {
			t.Fatalf("status %d accepted", resp.Status)
		}
		// Whatever the worker sent, the response can be written.
		rec := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		if _, err := WriteResponse(rec, r, resp); err != nil {
			t.Fatal(err)
		}
	})
}
