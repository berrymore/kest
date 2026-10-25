package accesslog

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/berrymore/kest/internal/httpx"
)

func logged(t *testing.T, h http.Handler, req *http.Request) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	New(logger)(h).ServeHTTP(httptest.NewRecorder(), req)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line %q: %v", buf.String(), err)
	}
	return line
}

func TestAccessLog(t *testing.T) {
	req := httpx.WithRequestInfo(httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://example.com/orders?secret=1", http.NoBody))
	req.RemoteAddr = "192.0.2.7:51234"
	req.Header.Set("User-Agent", "curl/8")
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Later filters and the router fill in RequestInfo.
		info := httpx.Info(r)
		info.RequestID, info.VirtualHost, info.Cluster = "abc", "shop", "php"
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "created")
	})
	line := logged(t, h, req)
	for k, want := range map[string]any{
		"msg": "request", "method": "POST", "path": "/orders", "proto": "HTTP/1.1", "status": 201.0, "bytes": 7.0,
		"remote": "192.0.2.7", "request_id": "abc", "virtual_host": "shop", "cluster": "php", "user_agent": "curl/8",
	} {
		if line[k] != want {
			t.Errorf("%s = %v, want %v", k, line[k], want)
		}
	}
	if _, ok := line["duration"].(float64); !ok {
		t.Errorf("duration = %v, want a number", line["duration"])
	}
}

// A panic below is logged as a 500 and goes on up.
func TestAccessLogPanic(t *testing.T) {
	var buf bytes.Buffer
	h := New(slog.New(slog.NewJSONHandler(&buf, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic didn't go on")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	}()
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil || line["status"] != 500.0 {
		t.Errorf("log line %q (%v), want status 500", buf.String(), err)
	}
}
