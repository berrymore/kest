package recovery

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func run(t *testing.T, h http.HandlerFunc) (rec *httptest.ResponseRecorder, logs string, panicked any) {
	t.Helper()
	var buf bytes.Buffer
	rec = httptest.NewRecorder()
	func() {
		defer func() { panicked = recover() }()
		New(slog.New(slog.NewTextHandler(&buf, nil)))(h).ServeHTTP(rec,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", http.NoBody))
	}()
	return rec, buf.String(), panicked
}

func TestRecovery(t *testing.T) {
	rec, logs, p := run(t, func(http.ResponseWriter, *http.Request) { panic("boom") })
	if p != nil || rec.Code != http.StatusInternalServerError || rec.Body.String() != "Internal Server Error\n" {
		t.Errorf("panic before writing: %d %q, panicked %v; want a 500", rec.Code, rec.Body.String(), p)
	}
	if !strings.Contains(logs, `msg="panic while serving a request" method=GET path=/x`) ||
		!strings.Contains(logs, "panic=boom") || !strings.Contains(logs, "recovery_test.go") {
		t.Errorf("log %q, want the panic and its stack", logs)
	}
}

func TestRecoveryAfterWriting(t *testing.T) {
	rec, _, p := run(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "half")
		panic("boom")
	})
	if p != http.ErrAbortHandler || rec.Body.String() != "half" { //nolint:errorlint // a panic value
		t.Errorf("panic after writing: body %q, panicked %v; want the connection aborted", rec.Body.String(), p)
	}
}

func TestRecoveryPassesAbort(t *testing.T) {
	_, logs, p := run(t, func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
	if p != http.ErrAbortHandler || logs != "" { //nolint:errorlint // a panic value
		t.Errorf("ErrAbortHandler: panicked %v, log %q; want it to pass unlogged", p, logs)
	}
}

func TestRecoveryNoPanic(t *testing.T) {
	rec, logs, p := run(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	if p != nil || logs != "" || rec.Code != http.StatusTeapot {
		t.Errorf("no panic: %d, log %q, panicked %v", rec.Code, logs, p)
	}
}
