package rrhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker"
)

// Response is a worker's answer to a request.
type Response struct {
	Status int
	// Header holds the headers as the worker sent them: names are not
	// canonicalized or checked yet; WriteResponse does that.
	Header map[string][]string
	Body   []byte
}

// ResponseError means a worker's answer can't be turned into an HTTP
// response. It answers 502.
type ResponseError struct {
	Err error
}

func (e *ResponseError) Error() string {
	return "invalid response from worker: " + e.Err.Error()
}

func (e *ResponseError) Unwrap() error {
	return e.Err
}

// DecodeResponse decodes a worker's payload. The context is protobuf if the
// frame says so and JSON otherwise (roadrunner/http before 3.5 sets no codec
// flag); an empty context means status 200 without headers, as in RoadRunner.
// Errors are *ResponseError values.
func DecodeResponse(p worker.Payload) (*Response, error) {
	resp := &Response{Status: http.StatusOK, Body: p.Body}
	if len(p.Context) > 0 {
		resp.Status = 0 // a context must have a status
		var err error
		if p.Codec == relay.CodecProto {
			err = decodeResponseProto(p.Context, resp)
		} else {
			err = decodeResponseJSON(p.Context, resp)
		}
		if err != nil {
			return nil, &ResponseError{Err: err}
		}
	}
	// 1xx are only meaningful before a final response, in a stream.
	if resp.Status < 200 || resp.Status > 599 {
		return nil, &ResponseError{Err: fmt.Errorf("invalid status %d", resp.Status)}
	}
	return resp, nil
}

// jsonResponse is the JSON response context: {"status": 200, "headers":
// {"Name": ["value"]}}. Headers may be [] (an empty PHP array) and a value
// may be a single string.
type jsonResponse struct {
	Status  int64           `json:"status"`
	Headers json.RawMessage `json:"headers"`
}

func decodeResponseJSON(data []byte, resp *Response) error {
	var jr jsonResponse
	if err := json.Unmarshal(data, &jr); err != nil {
		return fmt.Errorf("decode JSON context: %w", err)
	}
	resp.Status = clampStatus(jr.Status)

	raw := bytes.TrimSpace(jr.Headers)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("[]")) {
		return nil
	}
	var headers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &headers); err != nil {
		return fmt.Errorf("decode JSON headers: %w", err)
	}
	resp.Header = make(map[string][]string, len(headers))
	for name, v := range headers {
		var values []string
		if err := json.Unmarshal(v, &values); err != nil {
			var one string
			if json.Unmarshal(v, &one) != nil {
				return fmt.Errorf("decode JSON header %q: want a string or a list of strings, got %s", name, clip(v))
			}
			values = []string{one}
		}
		resp.Header[name] = values
	}
	return nil
}

// clampStatus keeps an out-of-range int64 out of range as an int.
func clampStatus(s int64) int {
	if s < 0 || s > 999 {
		return -1
	}
	return int(s)
}

func clip(b []byte) []byte {
	if len(b) > 64 {
		return b[:64]
	}
	return b
}

// WriteResponse writes resp as the answer to r. kest frames the body itself:
// the worker's Content-Length and Transfer-Encoding are replaced (a HEAD
// response keeps a valid Content-Length), there is no body for HEAD, 204 and
// 304, and headers named in "Trailer" are sent as trailers. Header names
// that aren't HTTP tokens and values with control characters are dropped;
// WriteResponse returns how many. The error is from writing the body.
func WriteResponse(w http.ResponseWriter, r *http.Request, resp *Response) (dropped int, err error) {
	h := w.Header()
	trailers := trailerNames(resp.Header)
	noBodyStatus := resp.Status == http.StatusNoContent || resp.Status == http.StatusNotModified
	bodyAllowed := r.Method != http.MethodHead && !noBodyStatus

	var trailerValues map[string][]string
	for name, values := range resp.Header {
		if !validHeaderName(name) {
			dropped += len(values)
			continue
		}
		key := http.CanonicalHeaderKey(name)
		switch key {
		case "Transfer-Encoding", "Trailer":
			continue
		case "Content-Length":
			if r.Method != http.MethodHead || noBodyStatus || len(values) != 1 || !validLength(values[0]) {
				continue
			}
		}
		dst := h
		if slices.Contains(trailers, key) {
			if trailerValues == nil {
				trailerValues = make(map[string][]string, len(trailers))
			}
			dst = trailerValues
		}
		for _, v := range values {
			if !validHeaderValue(v) {
				dropped++
				continue
			}
			dst[key] = append(dst[key], v)
		}
	}

	switch {
	case len(trailers) > 0:
		// Trailers need a chunked body: no Content-Length.
		h.Set("Trailer", strings.Join(trailers, ", "))
	case bodyAllowed:
		h.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	w.WriteHeader(resp.Status)
	if bodyAllowed && len(resp.Body) > 0 {
		_, err = w.Write(resp.Body)
	}
	maps.Copy(h, trailerValues)
	return dropped, err
}

// trailerNames returns the canonical names listed in the "Trailer" header
// that the response has values for, sorted.
func trailerNames(header map[string][]string) []string {
	var names []string
	for k, values := range header {
		if !strings.EqualFold(k, "Trailer") {
			continue
		}
		for _, v := range values {
			for n := range strings.SplitSeq(v, ",") {
				n = http.CanonicalHeaderKey(strings.Trim(n, " \t"))
				if validHeaderName(n) && hasHeader(header, n) && !slices.Contains(names, n) && !framingHeader(n) {
					names = append(names, n)
				}
			}
		}
	}
	slices.Sort(names)
	return names
}

// framingHeader reports the headers that kest sets itself and that can't be trailers.
func framingHeader(name string) bool {
	return name == "Content-Length" || name == "Transfer-Encoding" || name == "Trailer"
}

func hasHeader(header map[string][]string, name string) bool {
	for k := range header {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// validHeaderName reports whether s is an HTTP token (RFC 9110 § 5.6.2).
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isTokenChar(s[i]) {
			return false
		}
	}
	return true
}

func isTokenChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	default:
		return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
	}
}

// validHeaderValue rejects control characters other than tab: CR and LF
// would split the header.
func validHeaderValue(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func validLength(s string) bool {
	n, err := strconv.ParseUint(s, 10, 63)
	return err == nil && n < 1<<62
}
