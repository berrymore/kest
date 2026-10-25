// Package httpx implements the HTTP connection manager of a listener: the
// chain of HTTP filters followed by the router, which runs the filters of the
// virtual host and of the route it chooses before the route's action.
package httpx

import (
	"net/http"
	"slices"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/registry"
)

// Filter is one step of the HTTP chain. It wraps the rest of the chain.
type Filter func(next http.Handler) http.Handler

// Filters is the registry of HTTP filter implementations, keyed by config type.
var Filters = registry.NewCategory[Filter]("http filter")

// ClusterLookup returns the handler of a route's cluster. lane is nil unless
// the route asks for a lane of its own; then the cluster gives the route a
// handler of its own, or an error (a *config.FieldError for a field of the
// route action). A nil handler without an error means there is no such
// cluster: it failed to build, which is reported already.
type ClusterLookup func(cluster string, lane *Lane) (http.Handler, error)

// Lane is what a route asks of its cluster's pool: a queue of its own, with a
// limit on the workers it takes, a priority and a queue timeout.
type Lane struct {
	Name         string        // the route's name
	MaxWorkers   int           // 0: no limit
	Priority     string        // config.PriorityHigh, PriorityNormal (or empty) or PriorityLow
	QueueTimeout time.Duration // 0: the cluster's own
}

// NewHandler builds the handler for a listener's http block: the configured
// filters in order, then the router. bc.Name is the listener's name, which
// labels its metrics. Configuration problems are returned as config.Errors.
func NewHandler(cfg *config.HTTP, clusters ClusterLookup, bc registry.BuildContext) (http.Handler, error) {
	var errs config.Errors
	filters := buildFilters(cfg.Filters, bc, &errs)
	m := newListenerMetrics(bc.MetricsRegistry(), bc.Name)
	rt := newRouter(cfg.VirtualHosts, clusters, m, bc, &errs)
	if err := errs.Err(); err != nil {
		return nil, err
	}
	h := Chain(rt, filters...)
	return m.entry(normalizer(h, isTrue(cfg.NormalizePath), isTrue(cfg.MergeSlashes)), len(filters) > 0), nil
}

// buildFilters builds the filters that exts configure, adding their
// configuration problems to errs.
func buildFilters(exts []config.Extension, bc registry.BuildContext, errs *config.Errors) []Filter {
	filters := make([]Filter, 0, len(exts))
	for i := range exts {
		f, err := registry.Build(Filters, &exts[i], bc)
		if err != nil {
			errs.Append(err)
			continue
		}
		filters = append(filters, f)
	}
	return filters
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
