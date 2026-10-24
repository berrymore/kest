package rrhttp

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/relay"
)

func newHTTPRequest(t *testing.T, method, target, contentType, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func mustRequest(t *testing.T, r *http.Request, opts Options) *Request {
	t.Helper()
	req, err := NewRequest(r, opts)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

var jsonOpts = Options{Codec: relay.CodecJSON}

func TestNewRequestHead(t *testing.T) {
	r := newHTTPRequest(t, http.MethodGet, "http://example.com:8080/a%20b/c?x=1&y[]=2", "", "")
	r.RemoteAddr = "192.0.2.10:54321"
	r.Header.Add("X-Multi", "one")
	r.Header.Add("X-Multi", "two")
	r.Header.Set("Cookie", "a=1; b=x%20y+z; bad=%zz; a=2")

	req := mustRequest(t, r, jsonOpts)
	if req.RemoteAddr != "192.0.2.10" || req.Protocol != "HTTP/1.1" || req.Method != http.MethodGet ||
		req.URI != "http://example.com:8080/a%20b/c?x=1&y[]=2" || req.RawQuery != "x=1&y[]=2" {
		t.Errorf("request head: %+v", req)
	}
	if !reflect.DeepEqual(req.Header, r.Header) || req.Attributes != nil {
		t.Errorf("header %v attributes %v", req.Header, req.Attributes)
	}
	if want := map[string]string{"a": "2", "b": "x y z"}; !reflect.DeepEqual(req.Cookies, want) {
		t.Errorf("cookies %v, want %v", req.Cookies, want)
	}
	if req.Parsed || req.Body != nil || req.Uploads != nil {
		t.Errorf("parsed=%v body=%q uploads=%q", req.Parsed, req.Body, req.Uploads)
	}
}

func TestRequestURI(t *testing.T) {
	tests := []struct {
		name   string
		target string
		host   string
		tls    bool
		want   string
	}{
		{"origin form", "/p?q=1", "example.com", false, "http://example.com/p?q=1"},
		{"host with port", "/", "example.com:8080", false, "http://example.com:8080/"},
		{"tls", "/p", "example.com", true, "https://example.com/p"},
		{"absolute form", "http://other.example/p?q", "example.com", false, "http://other.example/p?q"},
		{"escaped path", "/a%2Fb", "h", false, "http://h/a%2Fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newHTTPRequest(t, http.MethodGet, tt.target, "", "")
			r.Host = tt.host
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			} else {
				r.TLS = nil
			}
			if got := requestURI(r); got != tt.want {
				t.Errorf("requestURI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoteIP(t *testing.T) {
	for addr, want := range map[string]string{
		"192.0.2.1:80":      "192.0.2.1",
		"[2001:db8::1]:443": "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
		"192.0.2.1":         "192.0.2.1",
		"@":                 "@",
		"":                  "",
		"not:an:address":    "",
	} {
		if got := remoteIP(addr); got != want {
			t.Errorf("remoteIP(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestStripNewlines(t *testing.T) {
	if got := stripNewlines("a\r\nb\nc"); got != "abc" {
		t.Errorf("stripNewlines = %q", got)
	}
}

func TestNewRequestBody(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		rawBody     bool
		wantParsed  bool
		wantBody    string
	}{
		{"raw json", http.MethodPost, "application/json", `{"a":1}`, false, false, `{"a":1}`},
		{"no content type", http.MethodPut, "", "plain", false, false, "plain"},
		{"empty raw", http.MethodPost, "text/plain", "", false, false, ""},
		{"head is not read", http.MethodHead, "text/plain", "ignored", false, false, ""},
		{"options is not read", http.MethodOptions, "application/x-www-form-urlencoded", "a=1", false, false, ""},
		{"form", http.MethodPost, "application/x-www-form-urlencoded", "a=1&b[]=2&b[]=3&c[d]=4", false, true,
			`{"a":"1","b":["2","3"],"c":{"d":"4"}}`},
		{"form with charset", http.MethodPost, "application/x-www-form-urlencoded; charset=utf-8", "a=%C3%A9+x", false, true,
			`{"a":"é x"}`},
		{"form on delete", http.MethodDelete, "application/x-www-form-urlencoded", "id=7", false, true, `{"id":"7"}`},
		{"empty form", http.MethodPost, "application/x-www-form-urlencoded", "", false, true, "{}"},
		{"form on get without body", http.MethodGet, "application/x-www-form-urlencoded", "", false, true, "{}"},
		{"raw form", http.MethodPost, "application/x-www-form-urlencoded", "a=1", true, false, "a=1"},
		{"html escaping", http.MethodPost, "application/x-www-form-urlencoded", "a=%3Cb%3E", false, true, `{"a":"\u003cb\u003e"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newHTTPRequest(t, tt.method, "/", tt.contentType, tt.body)
			req := mustRequest(t, r, Options{Codec: relay.CodecJSON, RawBody: tt.rawBody})
			if req.Parsed != tt.wantParsed || string(req.Body) != tt.wantBody {
				t.Errorf("parsed=%v body=%q, want parsed=%v body=%q", req.Parsed, req.Body, tt.wantParsed, tt.wantBody)
			}
		})
	}
}

func multipartBody(t *testing.T, write func(w *multipart.Writer)) (contentType, body string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	write(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w.FormDataContentType(), b.String()
}

func TestNewRequestMultipartValues(t *testing.T) {
	ct, body := multipartBody(t, func(w *multipart.Writer) {
		_ = w.WriteField("name", "kest")
		_ = w.WriteField("tags[]", "a")
		_ = w.WriteField("tags[]", "b")
		_ = w.WriteField("user[email]", "x@example.com")
		fw, _ := w.CreateFormFile("doc", "a.txt")
		_, _ = fw.Write([]byte("file content"))
	})
	req := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body), Options{Codec: relay.CodecJSON, Uploads: Uploads{Dir: t.TempDir()}})
	defer func() { _ = req.Close() }()
	want := `{"name":"kest","tags":["a","b"],"user":{"email":"x@example.com"}}`
	if !req.Parsed || string(req.Body) != want {
		t.Errorf("parsed=%v body=%s, want %s", req.Parsed, req.Body, want)
	}

	raw := mustRequest(t, newHTTPRequest(t, http.MethodPost, "/", ct, body), Options{Codec: relay.CodecJSON, RawBody: true})
	if raw.Parsed || string(raw.Body) != body {
		t.Errorf("raw body: parsed=%v, %d bytes, want the original %d bytes", raw.Parsed, len(raw.Body), len(body))
	}
}

func TestNewRequestErrors(t *testing.T) {
	tooManyFields := strings.Repeat("a=1&", maxFormFields)
	tooManyParts, partsBody := multipartBody(t, func(w *multipart.Writer) {
		for range maxParts + 1 {
			_ = w.WriteField("a", "1")
		}
	})
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{"bad escape", "application/x-www-form-urlencoded", "a=%zz", "invalid URL escape"},
		{"semicolon", "application/x-www-form-urlencoded", "a=1;b=2", "invalid semicolon separator"},
		{"too many fields", "application/x-www-form-urlencoded", tooManyFields, "too many form fields"},
		{"conflict", "application/x-www-form-urlencoded", "a=1&a[b]=2", `invalid multiple values to key "a"`},
		{"no boundary", "multipart/form-data", "x", "no boundary"},
		{"bad media type", "multipart/form-data; boundary", "x", "mime: invalid media parameter"},
		{"truncated multipart", "multipart/form-data; boundary=xyz", "--xyz\r\nContent-Disposition: form-data; name=a\r\n\r\nvalue", "unexpected EOF"},
		{"too many parts", tooManyParts, partsBody, "too many parts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRequest(newHTTPRequest(t, http.MethodPost, "/", tt.contentType, tt.body), jsonOpts)
			var re *RequestError
			if !errors.As(err, &re) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want a *RequestError containing %q", err, tt.want)
			}
		})
	}
}

func TestNewRequestBodyLimit(t *testing.T) {
	for _, ct := range []string{"application/json", "application/x-www-form-urlencoded"} {
		r := newHTTPRequest(t, http.MethodPost, "/", ct, strings.Repeat("a", 100))
		r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, 10)
		_, err := NewRequest(r, jsonOpts)
		if _, ok := errors.AsType[*http.MaxBytesError](err); !ok {
			t.Errorf("%s: err = %v, want an *http.MaxBytesError", ct, err)
		}
	}
}

func TestPayloadJSON(t *testing.T) {
	r := newHTTPRequest(t, http.MethodPost, "http://example.com/p?q=1", "application/x-www-form-urlencoded", "a=1")
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Cookie", "c=v")
	p, err := mustRequest(t, r, jsonOpts).Payload(relay.CodecJSON)
	if err != nil {
		t.Fatal(err)
	}
	if p.Codec != relay.CodecJSON || string(p.Body) != `{"a":"1"}` {
		t.Errorf("codec %#x body %q", p.Codec, p.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(p.Context, &got); err != nil {
		t.Fatalf("context is not JSON: %v\n%s", err, p.Context)
	}
	want := map[string]any{
		"remoteAddr": "192.0.2.1",
		"protocol":   "HTTP/1.1",
		"method":     "POST",
		"uri":        "http://example.com/p?q=1",
		"headers": map[string]any{
			"Content-Type": []any{"application/x-www-form-urlencoded"},
			"Cookie":       []any{"c=v"},
		},
		"cookies":    map[string]any{"c": "v"},
		"rawQuery":   "q=1",
		"parsed":     true,
		"uploads":    nil,
		"attributes": map[string]any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("context:\n got %v\nwant %v", got, want)
	}

	// Nil maps are sent as {}.
	p, _ = (&Request{Method: "GET"}).Payload(relay.CodecJSON)
	if !bytes.Contains(p.Context, []byte(`"headers":{},"cookies":{}`)) {
		t.Errorf("empty maps: %s", p.Context)
	}
}

func TestPayloadProtoGolden(t *testing.T) {
	// The same messages as testdata/gen/main.go.
	tests := map[string]*Request{
		"request_full.pb": {
			RemoteAddr: "192.0.2.10",
			Protocol:   "HTTP/1.1",
			Method:     "POST",
			URI:        "http://example.com/path?a=1&b[]=2",
			Header: http.Header{
				"Accept":       {"text/html"},
				"Content-Type": {"application/x-www-form-urlencoded"},
				"X-Empty":      {},
				"X-Long":       {strings.Repeat("v", 300)},
				"X-Multi":      {"one", "two", ""},
			},
			Cookies:    map[string]string{"session": "abc def", "theme": ""},
			RawQuery:   "a=1&b[]=2",
			Parsed:     true,
			Attributes: map[string][]string{"route": {"home"}},
			Body:       []byte("not in the context"),
		},
		"request_minimal.pb": {Method: "GET", URI: "http://localhost/"},
	}
	for file, req := range tests {
		t.Run(file, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatal(err)
			}
			p, err := req.Payload(relay.CodecProto)
			if err != nil {
				t.Fatal(err)
			}
			if p.Codec != relay.CodecProto || !bytes.Equal(p.Context, want) {
				t.Errorf("context differs from %s:\n got %x\nwant %x", file, p.Context, want)
			}
			if !bytes.Equal(p.Body, req.Body) {
				t.Errorf("body = %q", p.Body)
			}
		})
	}
}

func TestPayloadProtoInvalidUTF8(t *testing.T) {
	p, err := (&Request{Method: "GET", URI: "http://h/?q=\xff"}).Payload(relay.CodecProto)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(p.Context, []byte("q=\uFFFD")) {
		t.Errorf("invalid UTF-8 not replaced: %q", p.Context)
	}
}

func TestPayloadUnknownCodec(t *testing.T) {
	if _, err := (&Request{}).Payload(relay.CodecRaw); err == nil {
		t.Error("CodecRaw accepted")
	}
	if err := (&Options{Codec: relay.CodecRaw}).Validate(); err == nil {
		t.Error("Validate accepted CodecRaw")
	}
}

func BenchmarkPayload(b *testing.B) {
	r := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "http://example.com/products/42?ref=home", http.NoBody)
	for k, v := range map[string]string{
		"Accept":          "text/html,application/xhtml+xml",
		"Accept-Encoding": "gzip, deflate, br",
		"Accept-Language": "en-US,en;q=0.9",
		"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
		"Cookie":          "session=abc123; theme=dark",
	} {
		r.Header.Set(k, v)
	}
	for _, codec := range []relay.Flags{relay.CodecJSON, relay.CodecProto} {
		b.Run(url.PathEscape(codecName(codec)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				req, err := NewRequest(r, Options{Codec: codec})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := req.Payload(codec); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func codecName(c relay.Flags) string {
	if c == relay.CodecProto {
		return "proto"
	}
	return "json"
}
