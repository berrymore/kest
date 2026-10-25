package httpx_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/berrymore/kest/internal/httpx"
)

func TestBeforeHeader(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler func(w http.ResponseWriter)
		status  int // seen by edit
	}{
		{"WriteHeader", func(w http.ResponseWriter) { w.WriteHeader(http.StatusCreated) }, http.StatusCreated},
		{"Write", func(w http.ResponseWriter) { _, _ = io.WriteString(w, "x") }, http.StatusOK},
		{"Flush", func(w http.ResponseWriter) { _ = http.NewResponseController(w).Flush() }, http.StatusOK},
		{"after an informational response", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "x")
		}, http.StatusAccepted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			var calls, status int
			w := httpx.BeforeHeader(rec, func(h http.Header, code int) {
				calls++
				status = code
				h.Set("X-Edited", "yes")
			})
			tt.handler(w)
			_, _ = io.WriteString(w, "more")
			if calls != 1 || status != tt.status || rec.Header().Get("X-Edited") != "yes" {
				t.Errorf("edit ran %d times with status %d (header %q); want once with %d", calls, status,
					rec.Header().Get("X-Edited"), tt.status)
			}
		})
	}
}
