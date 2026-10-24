package metrics

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHandler(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("c_total", "A counter.").Add(5)
	h := r.Handler()

	tests := []struct {
		name, method, acceptEncoding string
		gzipped, body                bool
	}{
		{name: "plain", method: http.MethodGet, body: true},
		{name: "gzip", method: http.MethodGet, acceptEncoding: "gzip", gzipped: true, body: true},
		{name: "head", method: http.MethodHead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), tt.method, "/metrics", http.NoBody)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if ct := rec.Header().Get("Content-Type"); ct != ContentType {
				t.Errorf("Content-Type %q", ct)
			}
			if gz := rec.Header().Get("Content-Encoding") == "gzip"; gz != tt.gzipped {
				t.Errorf("gzipped %v, want %v", gz, tt.gzipped)
			}
			var body io.Reader = rec.Body
			if tt.gzipped {
				zr, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatal(err)
				}
				body = zr
			}
			b, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if !tt.body {
				if len(b) != 0 {
					t.Errorf("HEAD wrote a body: %q", b)
				}
				return
			}
			if got := parse(t, b)["c_total"].Metric[0].Counter.GetValue(); got != 5 {
				t.Errorf("counter = %v", got)
			}
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                         false,
		"gzip":                     true,
		"GZIP":                     true,
		"identity, gzip;q=0.5":     true,
		"gzip;q=0":                 false,
		"gzip; q=0.0":              false,
		"*":                        true,
		"*;q=0":                    false,
		"*;q=0, gzip":              true,
		"gzip;q=0, *":              false,
		"deflate, br":              false,
		"x-gzip":                   false,
		"gzip;q=1.0, identity;q=0": true,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestRegisterRuntime(t *testing.T) {
	r := NewRegistry()
	RegisterRuntime(r)
	fams := parse(t, write(t, r))

	positive := []string{"go_goroutines", "go_threads", "go_sched_gomaxprocs_threads", "go_memstats_alloc_bytes",
		"go_memstats_sys_bytes", "go_memstats_next_gc_bytes", "go_gc_gomemlimit_bytes", "process_open_fds",
		"process_max_fds"}
	if runtime.GOOS == "linux" {
		positive = append(positive, "process_resident_memory_bytes", "process_virtual_memory_bytes")
	}
	for _, name := range positive {
		f, ok := fams[name]
		if !ok || len(f.Metric) != 1 || f.Metric[0].Gauge.GetValue() <= 0 {
			t.Errorf("%s: %v, want one positive gauge", name, f)
		}
	}
	if _, ok := fams["process_cpu_seconds_total"]; !ok || len(fams["process_cpu_seconds_total"].Metric) != 1 {
		t.Error("process_cpu_seconds_total missing")
	}
	start := time.Unix(int64(fams["process_start_time_seconds"].Metric[0].Gauge.GetValue()), 0)
	if since := time.Since(start); since < 0 || since > time.Hour {
		t.Errorf("process_start_time_seconds is %v ago", since)
	}
	if v := fams["go_info"].Metric[0].Label[0].GetValue(); !strings.HasPrefix(v, "go") {
		t.Errorf("go_info version %q", v)
	}
}
