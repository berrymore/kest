// Package recovery implements the recovery HTTP filter: a panic below it is
// logged with its stack and answered with 500, instead of net/http dropping
// the connection.
package recovery

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "recovery"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		Build: func(bc registry.BuildContext, _ any) (httpx.Filter, error) {
			return New(bc.Logger), nil
		},
	})
}

// New returns the filter. If the response had started when the panic came,
// the connection is aborted (http.ErrAbortHandler), so that the client sees
// a cut-off response rather than a complete-looking one. A panic with
// http.ErrAbortHandler itself passes through.
func New(logger *slog.Logger) httpx.Filter {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := httpx.NewResponseWriter(w)
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // a panic value, compared as net/http does
					panic(v)
				}
				logger.Error("panic while serving a request", "method", r.Method, "path", r.URL.Path,
					"request_id", httpx.Info(r).RequestID, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rw.Written() {
					panic(http.ErrAbortHandler)
				}
				http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}()
			next.ServeHTTP(rw, r)
		})
	}
}
