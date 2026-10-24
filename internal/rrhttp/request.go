// Package rrhttp maps HTTP requests and responses to and from the payloads
// of RoadRunner's HTTP workers (roadrunner/http, PSR7Worker), as verified in
// .ai/research/roadrunner.md.
//
// A request becomes a context (the request head, encoded as JSON or
// protobuf) and a body: the raw body, or a JSON tree of form fields with
// PHP's bracket syntax. A response is a context (status and headers) and a
// body.
package rrhttp

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker"
)

// Options control how requests are passed to workers.
type Options struct {
	// Codec encodes the request context: relay.CodecJSON or relay.CodecProto.
	Codec relay.Flags
	// RawBody sends form and multipart bodies as they are instead of parsing them.
	RawBody bool
	// Uploads controls where the files of multipart requests are stored.
	Uploads Uploads
}

// Validate reports an unsupported codec.
func (o *Options) Validate() error {
	if o.Codec != relay.CodecJSON && o.Codec != relay.CodecProto {
		return fmt.Errorf("unsupported codec %#x", uint8(o.Codec))
	}
	return nil
}

// Request is an HTTP request as the PHP side sees it.
type Request struct {
	RemoteAddr string // the client IP, without the port
	Protocol   string // "HTTP/1.1"
	Method     string
	URI        string // absolute: scheme, host, path and query
	Header     http.Header
	Cookies    map[string]string
	RawQuery   string
	Attributes map[string][]string // PSR-7 request attributes
	// Parsed means Body is a JSON tree of form fields, not the raw body.
	Parsed bool
	Body   []byte
	// Uploads is the JSON tree of uploaded files, for parsed multipart
	// requests; Close removes the files.
	Uploads []byte

	files *files
}

// Close removes the temp files of uploads. Call it once the worker has
// answered.
func (req *Request) Close() error {
	if req.files == nil {
		return nil
	}
	return req.files.remove()
}

// UploadErrors returns the problems storing uploads; the files concerned
// reach PHP with an upload error code.
func (req *Request) UploadErrors() []error {
	if req.files == nil {
		return nil
	}
	return req.files.errs
}

// RequestError means a request can't be passed to a worker: its body
// couldn't be read or parsed. It answers 413 if it wraps an
// *http.MaxBytesError, 400 otherwise.
type RequestError struct {
	Err error
}

func (e *RequestError) Error() string {
	return "invalid request: " + e.Err.Error()
}

func (e *RequestError) Unwrap() error {
	return e.Err
}

// Kinds of request bodies, chosen by method and Content-Type as RoadRunner does.
type bodyKind int

const (
	bodyNone       bodyKind = iota // HEAD and OPTIONS: the body isn't read
	bodyRaw                        // sent as it is
	bodyURLEncoded                 // application/x-www-form-urlencoded
	bodyMultipart                  // multipart/form-data
)

func kindOf(r *http.Request) bodyKind {
	if r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return bodyNone
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "application/x-www-form-urlencoded"):
		return bodyURLEncoded
	case strings.Contains(ct, "multipart/form-data"):
		return bodyMultipart
	default:
		return bodyRaw
	}
}

// NewRequest reads r, including its body, into a Request. Bound the body
// first (http.MaxBytesReader): it is read into memory. Errors are
// *RequestError values.
func NewRequest(r *http.Request, opts Options) (*Request, error) {
	req := &Request{
		RemoteAddr: remoteIP(r.RemoteAddr),
		Protocol:   r.Proto,
		Method:     r.Method,
		URI:        requestURI(r),
		Header:     r.Header,
		Cookies:    cookies(r),
		RawQuery:   stripNewlines(r.URL.RawQuery),
	}

	var err error
	switch kind := kindOf(r); {
	case kind == bodyNone:
	case kind == bodyRaw, opts.RawBody:
		req.Body, err = readAll(r.Body)
	case kind == bodyURLEncoded:
		err = req.readURLEncoded(r.Body)
	case kind == bodyMultipart:
		err = req.readMultipart(r, &opts.Uploads)
	}
	if err != nil {
		return nil, &RequestError{Err: err}
	}
	return req, nil
}

// readAll reads a body; an empty one is nil.
func readAll(body io.Reader) ([]byte, error) {
	b, err := io.ReadAll(body)
	if len(b) == 0 {
		b = nil
	}
	return b, err
}

func (req *Request) readURLEncoded(body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f, err := parseURLEncoded(string(b))
	if err != nil {
		return fmt.Errorf("parse form: %w", err)
	}
	return req.setForm(f)
}

func (req *Request) readMultipart(r *http.Request, opts *Uploads) error {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("parse multipart form: %w", err)
	}
	f, fs, err := readMultipart(r.Body, params["boundary"], opts)
	if err != nil {
		return fmt.Errorf("parse multipart form: %w", err)
	}
	req.files = fs
	err = req.setForm(f)
	if err == nil {
		err = req.setUploads(fs)
	}
	if err != nil {
		_ = fs.remove()
		req.files = nil
	}
	return err
}

// setUploads records the upload tree; RoadRunner sends one, maybe empty, for
// every parsed multipart request.
func (req *Request) setUploads(fs *files) error {
	t, err := fs.tree()
	if err != nil {
		return err
	}
	req.Uploads, err = json.Marshal(t)
	return err
}

// setForm makes the body the form's JSON tree. A form without fields is
// sent as "{}": PHP decodes a parsed body with JSON_THROW_ON_ERROR.
func (req *Request) setForm(f *form) error {
	t, err := f.tree()
	if err != nil {
		return err
	}
	req.Parsed = true
	if len(t) == 0 {
		req.Body = []byte("{}")
		return nil
	}
	req.Body, err = json.Marshal(t)
	return err
}

// Payload encodes the request with codec, relay.CodecJSON or relay.CodecProto.
func (req *Request) Payload(codec relay.Flags) (worker.Payload, error) {
	var (
		ctx []byte
		err error
	)
	switch codec {
	case relay.CodecProto:
		ctx = appendRequestProto(nil, req)
	case relay.CodecJSON:
		ctx, err = req.marshalJSON()
	default:
		err = fmt.Errorf("unsupported codec %#x", uint8(codec))
	}
	if err != nil {
		return worker.Payload{}, err
	}
	return worker.Payload{Codec: codec, Context: ctx, Body: req.Body}, nil
}

// jsonRequest is the JSON request context, the format of roadrunner/http
// before 3.5 that later versions still accept.
type jsonRequest struct {
	RemoteAddr string              `json:"remoteAddr"`
	Protocol   string              `json:"protocol"`
	Method     string              `json:"method"`
	URI        string              `json:"uri"`
	Headers    http.Header         `json:"headers"`
	Cookies    map[string]string   `json:"cookies"`
	RawQuery   string              `json:"rawQuery"`
	Parsed     bool                `json:"parsed"`
	Uploads    json.RawMessage     `json:"uploads"`
	Attributes map[string][]string `json:"attributes"`
}

func (req *Request) marshalJSON() ([]byte, error) {
	return json.Marshal(&jsonRequest{
		RemoteAddr: req.RemoteAddr,
		Protocol:   req.Protocol,
		Method:     req.Method,
		URI:        req.URI,
		Headers:    nonNil(req.Header),
		Cookies:    nonNil(req.Cookies),
		RawQuery:   req.RawQuery,
		Parsed:     req.Parsed,
		Uploads:    uploadsJSON(req.Uploads),
		Attributes: nonNil(req.Attributes),
	})
}

func uploadsJSON(tree []byte) json.RawMessage {
	if len(tree) == 0 {
		return json.RawMessage("null")
	}
	return tree
}

// nonNil makes JSON say {} rather than null.
func nonNil[M ~map[K]V, K comparable, V any](m M) M {
	if m == nil {
		return M{}
	}
	return m
}

// remoteIP returns the IP of a "host:port" remote address.
func remoteIP(addr string) string {
	if !strings.Contains(addr, ":") {
		return addr
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return ""
}

// requestURI returns the absolute request URI, as RoadRunner builds it.
func requestURI(r *http.Request) string {
	uri := stripNewlines(r.URL.String())
	switch {
	case r.URL.Host != "":
		return uri
	case r.TLS != nil:
		return "https://" + r.Host + uri
	default:
		return "http://" + r.Host + uri
	}
}

// cookies decodes the request cookies. As in RoadRunner, values are
// URL-unescaped, undecodable ones are dropped, and of two cookies with the
// same name the later one wins.
func cookies(r *http.Request) map[string]string {
	list := r.Cookies()
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]string, len(list))
	for _, c := range list {
		if v, err := url.QueryUnescape(c.Value); err == nil {
			m[c.Name] = v
		}
	}
	return m
}

func stripNewlines(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// validUTF8 replaces invalid UTF-8, as encoding/json does: protobuf strings
// must be valid UTF-8, and both codecs should deliver the same text.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
