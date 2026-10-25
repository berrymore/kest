// Package accesslog implements the access_log HTTP filter: one line per
// request in kest's log, with component=access.
package accesslog

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "access_log"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		Build: func(bc registry.BuildContext, _ any) (httpx.Filter, error) {
			return New(bc.Logger.With("component", "access")), nil
		},
	})
}

// New returns the filter. It logs once the response is done, also when the
// handler panics (then as a 500 unless another status was sent), with what
// the router and the request_id filter recorded in httpx.RequestInfo.
func New(logger *slog.Logger) httpx.Filter {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := httpx.NewResponseWriter(w)
			panicked := true
			defer func() {
				status := rw.Status()
				if panicked && !rw.Written() {
					status = http.StatusInternalServerError
				}
				info := httpx.Info(r)
				logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("proto", r.Proto),
					slog.Int("status", status),
					slog.Int64("bytes", rw.BytesWritten()),
					slog.Duration("duration", time.Since(start)),
					slog.String("remote", httpx.ClientIP(r)),
					slog.String("request_id", info.RequestID),
					slog.String("virtual_host", info.VirtualHost),
					slog.String("route", info.Route),
					slog.String("cluster", info.Cluster),
					slog.String("user_agent", r.UserAgent()),
				)
			}()
			next.ServeHTTP(rw, r)
			panicked = false
		})
	}
}
