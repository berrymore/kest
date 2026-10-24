//go:build integration

package phpworkers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/logging"
	"github.com/berrymore/kest/internal/metrics"
	"github.com/berrymore/kest/internal/metrics/metricstest"
	"github.com/berrymore/kest/internal/registry"
)

// Integration tests against an unmodified roadrunner/http PSR7Worker
// (testdata/php). `make test-integration` installs its composer packages.

var codecs = []string{CodecJSON, CodecProto}

// phpServer starts a php_workers cluster on testdata/php behind an HTTP server.
func phpServer(t *testing.T, codec string, mutate func(*Config)) *httptest.Server {
	t.Helper()
	return phpServerMetrics(t, codec, mutate, metrics.NewRegistry())
}

// phpServerMetrics is phpServer with the cluster's metrics in reg.
func phpServerMetrics(t *testing.T, codec string, mutate func(*Config), reg *metrics.Registry) *httptest.Server {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	dir, err := filepath.Abs(filepath.Join("testdata", "php"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "autoload.php")); err != nil {
		t.Skip("testdata/php has no vendor/: run `make test-integration`, which runs composer install")
	}

	cfg := DefaultConfig()
	cfg.Command = []string{php, "worker.php"}
	cfg.Codec = codec
	cfg.Pool.Workers = 2
	cfg.Pool.StartTimeout = config.Duration(20 * time.Second)
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := New(&cfg, registry.BuildContext{Name: "app", Logger: logging.Discard(), ConfigDir: dir, Metrics: reg},
		append(os.Environ(), "KEST_TEST=inherited"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := httptest.NewServer(c)
	t.Cleanup(func() {
		srv.Close()
		_ = c.Stop(t.Context())
	})
	return srv
}

type reply struct {
	status int
	header http.Header
	body   string
}

func send(t *testing.T, method, url, contentType string, body io.Reader, header http.Header) reply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, string(b)}
}

// echo is what /echo answers: the request as PHP sees it.
type echo struct {
	Method     string              `json:"method"`
	URI        string              `json:"uri"`
	Protocol   string              `json:"protocol"`
	Headers    map[string][]string `json:"headers"`
	Cookies    map[string]string   `json:"cookies"`
	Query      map[string]any      `json:"query"`
	Parsed     any                 `json:"parsed"`
	Body       string              `json:"body"`
	Attributes map[string]any      `json:"attributes"`
	Files      map[string]struct {
		Name    string  `json:"name"`
		Type    string  `json:"type"`
		Size    int     `json:"size"`
		Error   int     `json:"error"`
		Content *string `json:"content"`
	} `json:"files"`
	Server map[string]string `json:"server"`
}

func sendEcho(t *testing.T, method, url, contentType string, body io.Reader, header http.Header) echo {
	t.Helper()
	r := send(t, method, url, contentType, body, header)
	if r.status != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, url, r.status, r.body)
	}
	var e echo
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("decode echo: %v\n%s", err, r.body)
	}
	return e
}

func TestPHPRequestHead(t *testing.T) {
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			srv := phpServer(t, codec, nil)
			e := sendEcho(t, http.MethodGet, srv.URL+"/echo?a=1&b[]=2&b[]=3", "", http.NoBody, http.Header{
				"X-Multi": {"one", "two"},
				"Cookie":  {"session=abc%20def; theme=dark"},
			})
			host := strings.TrimPrefix(srv.URL, "http://")
			if e.Method != http.MethodGet || e.URI != srv.URL+"/echo?a=1&b%5B%5D=2&b%5B%5D=3" || e.Protocol != "1.1" {
				t.Errorf("method %q uri %q protocol %q", e.Method, e.URI, e.Protocol)
			}
			if want := map[string]any{"a": "1", "b": []any{"2", "3"}}; !reflect.DeepEqual(e.Query, want) {
				t.Errorf("query %v", e.Query)
			}
			if want := map[string]string{"session": "abc def", "theme": "dark"}; !reflect.DeepEqual(e.Cookies, want) {
				t.Errorf("cookies %v", e.Cookies)
			}
			if got := e.Headers["X-Multi"]; !reflect.DeepEqual(got, []string{"one", "two"}) {
				t.Errorf("X-Multi %q", got)
			}
			if got := e.Headers["Host"]; !reflect.DeepEqual(got, []string{host}) {
				t.Errorf("Host %q, want %q", got, host)
			}
			wantServer := map[string]string{
				"REMOTE_ADDR":    "127.0.0.1",
				"REQUEST_METHOD": "GET",
				"REQUEST_URI":    srv.URL + "/echo?a=1&b[]=2&b[]=3",
				"HTTP_HOST":      host,
				"RR_MODE":        "http",
				"RR_RELAY":       "pipes",
				"KEST_TEST":      "inherited",
			}
			if !reflect.DeepEqual(e.Server, wantServer) {
				t.Errorf("$_SERVER:\n got %v\nwant %v", e.Server, wantServer)
			}
			if e.Parsed != nil || e.Attributes["rr_parsed_body"] != false {
				t.Errorf("parsed %v attributes %v", e.Parsed, e.Attributes)
			}
		})
	}
}

func TestPHPRequestBody(t *testing.T) {
	ctype, multipartBody := func() (string, string) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		_ = w.WriteField("name", "kest")
		_ = w.WriteField("tags[]", "a")
		_ = w.WriteField("tags[]", "b")
		_ = w.Close()
		return w.FormDataContentType(), b.String()
	}()
	tests := []struct {
		name        string
		method      string
		contentType string
		body        string
		parsed      any
		rawBody     string
	}{
		{"json", http.MethodPost, "application/json", `{"a":1}`, nil, `{"a":1}`},
		{"form", http.MethodPost, "application/x-www-form-urlencoded", "name=kest&tags[]=a&tags[]=b&user[email]=x%40example.com",
			map[string]any{"name": "kest", "tags": []any{"a", "b"}, "user": map[string]any{"email": "x@example.com"}},
			`{"name":"kest","tags":["a","b"],"user":{"email":"x@example.com"}}`},
		{"empty form", http.MethodPost, "application/x-www-form-urlencoded", "", []any{}, "{}"},
		{"multipart", http.MethodPut, ctype, multipartBody, map[string]any{"name": "kest", "tags": []any{"a", "b"}},
			`{"name":"kest","tags":["a","b"]}`},
		{"form on patch", http.MethodPatch, "application/x-www-form-urlencoded", "a=1", map[string]any{"a": "1"}, `{"a":"1"}`},
	}
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			srv := phpServer(t, codec, nil)
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					e := sendEcho(t, tt.method, srv.URL+"/echo", tt.contentType, strings.NewReader(tt.body), nil)
					if !reflect.DeepEqual(e.Parsed, tt.parsed) || e.Body != tt.rawBody {
						t.Errorf("parsed %#v body %q, want %#v %q", e.Parsed, e.Body, tt.parsed, tt.rawBody)
					}
					if e.Server["CONTENT_TYPE"] != tt.contentType {
						t.Errorf("CONTENT_TYPE %q", e.Server["CONTENT_TYPE"])
					}
				})
			}
		})
	}
}

func TestPHPRawBodyOption(t *testing.T) {
	srv := phpServer(t, CodecJSON, func(c *Config) { c.RawBody = true })
	e := sendEcho(t, http.MethodPost, srv.URL+"/echo", "application/x-www-form-urlencoded", strings.NewReader("a=1"), nil)
	if e.Parsed != nil || e.Body != "a=1" {
		t.Errorf("parsed %v body %q", e.Parsed, e.Body)
	}
}

func TestPHPResponses(t *testing.T) {
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			srv := phpServer(t, codec, nil)

			if r := send(t, http.MethodGet, srv.URL+"/status/404", "", http.NoBody, nil); r.status != 404 || r.body != "" {
				t.Errorf("/status/404: %d %q", r.status, r.body)
			}
			if r := send(t, http.MethodGet, srv.URL+"/nope", "", http.NoBody, nil); r.status != 404 || r.body != "no route" {
				t.Errorf("/nope: %d %q", r.status, r.body)
			}
			r := send(t, http.MethodGet, srv.URL+"/headers", "", http.NoBody, nil)
			if r.status != 200 || r.body != "headers" || r.header.Get("Content-Length") != "7" {
				t.Errorf("/headers: %d %q %v", r.status, r.body, r.header)
			}
			if got := r.header.Values("Set-Cookie"); !reflect.DeepEqual(got, []string{"a=1; Path=/", "b=2; Path=/; HttpOnly"}) {
				t.Errorf("Set-Cookie %q", got)
			}
			if got := r.header.Values("X-Multi"); !reflect.DeepEqual(got, []string{"one", "two"}) {
				t.Errorf("X-Multi %q", got)
			}
			if r := send(t, http.MethodGet, srv.URL+"/big/300000", "", http.NoBody, nil); r.status != 200 || len(r.body) != 300000 {
				t.Errorf("/big: %d, %d bytes", r.status, len(r.body))
			}
			if r := send(t, http.MethodHead, srv.URL+"/echo", "", http.NoBody, nil); r.status != 200 || r.body != "" {
				t.Errorf("HEAD /echo: %d %q", r.status, r.body)
			}
		})
	}
}

func TestPHPErrors(t *testing.T) {
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			srv := phpServer(t, codec, func(c *Config) {
				c.Supervisor.ExecTTL = config.Duration(500 * time.Millisecond)
				c.MaxResponseSize = 1 << 20
				c.MaxRequestBody = 1 << 10
			})
			tests := []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, "/error", "", 500},
				{http.MethodGet, "/exit", "", 502},
				{http.MethodGet, "/fatal", "", 502},
				{http.MethodGet, "/sleep/3000", "", 504},
				{http.MethodGet, "/big/2000000", "", 502},
				{http.MethodPost, "/echo", strings.Repeat("x", 2000), 413},
			}
			for _, tt := range tests {
				r := send(t, tt.method, srv.URL+tt.path, "text/plain", strings.NewReader(tt.body), nil)
				if want := http.StatusText(tt.status) + "\n"; r.status != tt.status || r.body != want {
					t.Errorf("%s %s: %d %q, want %d %q", tt.method, tt.path, r.status, r.body, tt.status, want)
				}
				// The pool recovers: the next request works.
				if r := send(t, http.MethodGet, srv.URL+"/status/200", "", http.NoBody, nil); r.status != 200 {
					t.Errorf("after %s: %d %q", tt.path, r.status, r.body)
				}
			}
		})
	}
}

// A PHP script that is not a worker, like a Symfony app without the
// RoadRunner runtime: it prints a page to stdout and exits.
func TestPHPNotAWorker(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	cfg := DefaultConfig()
	cfg.Command = []string{php, "-r", `echo "<!DOCTYPE html>\n<p>Hello</p>\n";`}
	cfg.Pool.Workers = 1
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err := New(&cfg, registry.BuildContext{Logger: logging.Discard(), ConfigDir: t.TempDir()}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	err = c.Start(t.Context())
	if err == nil {
		_ = c.Stop(t.Context())
		t.Fatal("Start succeeded")
	}
	for _, want := range []string{`but the worker printed "<!DOCTYPE html>\n<p>Hello</p>\n"`, "; " + stdoutHint} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Start = %v\nwant it to contain %s", err, want)
		}
	}
}

func TestPHPMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	srv := phpServerMetrics(t, CodecJSON, func(c *Config) { c.Pool.MaxJobs = 2 }, reg)
	for range 5 {
		if r := send(t, http.MethodGet, srv.URL+"/status/200", "", http.NoBody, nil); r.status != 200 {
			t.Fatalf("status %d", r.status)
		}
	}
	if got := metricstest.Value(t, reg, "kest_pool_wait_seconds", "cluster", "app"); got != 5 {
		t.Errorf("waits = %v, want 5", got)
	}
	// Each process serves 2 jobs (no jitter at 2), and the live ones at most 1
	// each, so 5 jobs always make 2 replacements. A worker answers before it
	// is replaced: wait for the last replacement to finish.
	deadline := time.Now().Add(10 * time.Second)
	for {
		restarts := metricstest.Value(t, reg, "kest_pool_worker_restarts_total", "cluster", "app", "reason", "max_jobs")
		ready := metricstest.Value(t, reg, "kest_pool_workers", "cluster", "app", "state", "ready")
		if restarts == 2 && ready == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("max_jobs restarts = %v and ready workers = %v, want 2 and 2", restarts, ready)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPHPConcurrent(t *testing.T) {
	srv := phpServer(t, CodecJSON, func(c *Config) { c.Pool.Workers = 4 })
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Go(func() {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/echo?i=%d", srv.URL, i), http.NoBody)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), fmt.Sprintf(`"query":{"i":"%d"}`, i)) {
				errs <- fmt.Errorf("request %d: %d %s", i, resp.StatusCode, b)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPHPUploads(t *testing.T) {
	for _, codec := range codecs {
		t.Run(codec, func(t *testing.T) {
			dir := t.TempDir()
			srv := phpServer(t, codec, func(c *Config) {
				c.Uploads.Dir = dir
				c.Uploads.Forbid = []string{".php"}
			})

			var b bytes.Buffer
			w := multipart.NewWriter(&b)
			_ = w.WriteField("title", "files")
			for _, f := range []struct{ field, name, content string }{
				{"doc", "a.txt", "first file"},
				{"photos[]", "1.jpg", "jpeg one"},
				{"photos[]", "2.jpg", "jpeg two"},
				{"shell", "x.php", "<?php evil();"},
			} {
				fw, err := w.CreateFormFile(f.field, f.name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = fw.Write([]byte(f.content))
			}
			_ = w.Close()

			e := sendEcho(t, http.MethodPost, srv.URL+"/echo", w.FormDataContentType(), &b, nil)
			if !reflect.DeepEqual(e.Parsed, map[string]any{"title": "files"}) {
				t.Errorf("parsed %v", e.Parsed)
			}
			for field, want := range map[string]string{"doc": "first file", "photos[0]": "jpeg one", "photos[1]": "jpeg two"} {
				f := e.Files[field]
				if f.Error != 0 || f.Content == nil || *f.Content != want || f.Size != len(want) || f.Type != "application/octet-stream" {
					t.Errorf("%s: %+v, want %q", field, f, want)
				}
			}
			if f := e.Files["shell"]; f.Error != 8 || f.Content != nil || f.Name != "x.php" {
				t.Errorf("forbidden file: %+v", f)
			}

			// The temp files are removed once the response is written.
			deadline := time.Now().Add(5 * time.Second)
			for {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("temp files left: %v", entries)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
