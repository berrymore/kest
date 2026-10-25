// Package httpx implements the HTTP connection manager of a listener: the
// chain of HTTP filters followed by the router.
package httpx

import (
	"net/http"
	"slices"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/registry"
)

// Filter is one step of the HTTP chain. It wraps the rest of the chain.
type Filter func(next http.Handler) http.Handler

// Filters is the registry of HTTP filter implementations, keyed by config type.
var Filters = registry.NewCategory[Filter]("http filter")

// ClusterLookup resolves a cluster name to the handler that serves it.
type ClusterLookup func(name string) (http.Handler, bool)

// NewHandler builds the handler for a listener's http block: the configured
// filters in order, then the router. bc.Name is the listener's name, which
// labels its metrics. Configuration problems are returned as config.Errors.
func NewHandler(cfg *config.HTTP, clusters ClusterLookup, bc registry.BuildContext) (http.Handler, error) {
	var errs config.Errors
	filters := make([]Filter, 0, len(cfg.Filters))
	for i := range cfg.Filters {
		f, err := registry.Build(Filters, &cfg.Filters[i], bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		filters = append(filters, f)
	}
	if err := errs.Err(); err != nil {
		return nil, err
	}
	m := newListenerMetrics(bc.MetricsRegistry(), bc.Name)
	h := Chain(newRouter(cfg.VirtualHosts, clusters, m), filters...)
	return m.entry(normalizer(h, isTrue(cfg.NormalizePath), isTrue(cfg.MergeSlashes))), nil
}

// isTrue reads an optional boolean whose default config.Parse has filled in.
func isTrue(b *bool) bool {
	return b != nil && *b
}

// Chain wraps h with filters so that filters[0] sees the request first.
func Chain(h http.Handler, filters ...Filter) http.Handler {
	for _, f := range slices.Backward(filters) {
		h = f(h)
	}
	return h
}
