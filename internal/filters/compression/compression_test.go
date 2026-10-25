package compression

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var page = strings.Repeat("<p>hello, compressible world</p>\n", 100)

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}

func run(t *testing.T, cfg Config, h http.HandlerFunc, method, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, "/", http.NoBody)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	New(cfg)(h).ServeHTTP(rec, req)
	return rec
}

func TestCompression(t *testing.T) {
	html := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "3300")
		w.Header().Set("Etag", `"v1"`)
		_, _ = io.WriteString(w, page)
	}
	for _, tt := range []struct {
		name       string
		handler    http.HandlerFunc
		method     string
		accept     string
		compressed bool
		vary       bool
	}{
		{"gzip", html, http.MethodGet, "gzip, deflate, br", true, true},
		{"q-values", html, http.MethodGet, "br;q=1.0, gzip;q=0.5", true, true},
		{"gzip refused", html, http.MethodGet, "gzip;q=0, *", false, true},
		{"any coding", html, http.MethodGet, "*", true, true},
		{"no Accept-Encoding", html, http.MethodGet, "", false, true},
		{"HEAD", html, http.MethodHead, "gzip", false, true},
		{"sniffed type", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>"+page) },
			http.MethodGet, "gzip", true, true},
		{"small", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "5")
			_, _ = io.WriteString(w, "small")
		}, http.MethodGet, "gzip", false, false},
		{"not compressible", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, page)
		}, http.MethodGet, "gzip", false, false},
		{"encoded already", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, page)
		}, http.MethodGet, "gzip", false, false},
		{"no-transform", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Cache-Control", "public, no-transform")
			_, _ = io.WriteString(w, page)
		}, http.MethodGet, "gzip", false, false},
		{"partial content", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, page)
		}, http.MethodGet, "gzip", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := run(t, DefaultConfig(), tt.handler, tt.method, tt.accept)
			h := rec.Header()
			if got := h.Get("Content-Encoding") == "gzip"; got != tt.compressed {
				t.Fatalf("compressed = %v, want %v (headers %v)", got, tt.compressed, h)
			}
			if got := h.Get("Vary") == "Accept-Encoding"; got != tt.vary {
				t.Errorf("Vary: Accept-Encoding = %v, want %v", got, tt.vary)
			}
			if !tt.compressed {
				return
			}
			if got := gunzip(t, rec.Body.Bytes()); !strings.HasSuffix(got, page) {
				t.Errorf("body after gunzip: %d bytes, want the page", len(got))
			}
			if h.Get("Content-Length") != "" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
				t.Errorf("headers %v: want no Content-Length and the plain Content-Type", h)
			}
			if etag := h.Get("Etag"); etag != "" && etag != `W/"v1"` {
				t.Errorf("Etag = %s, want it weak", etag)
			}
		})
	}
}

// A flushed chunk reaches the client before the handler returns.
func TestCompressionFlush(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ContentTypes = []string{"text/event-stream"}
	srv := httptest.NewServer(New(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	})))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := gz.Read(buf)
	if err != nil || string(buf[:n]) != "data: one\n\n" {
		t.Errorf("first event = %q, %v", buf[:n], err)
	}
}

func TestValidate(t *testing.T) {
	c := Config{Level: 10, MinLength: -1, ContentTypes: []string{"text/*", "*/*", "json"}}
	want := []string{
		"level: must be between 1 and 9, got 10",
		"min_length: must not be negative",
		`content_types[1]: invalid media type "*/*": want type/subtype or type/*`,
		`content_types[2]: invalid media type "json": want type/subtype or type/*`,
	}
	if err := c.Validate(); err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Validate() =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}

func BenchmarkCompression(b *testing.B) {
	h := New(DefaultConfig())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, page)
	}))
	req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/", http.NoBody)
	req.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}
