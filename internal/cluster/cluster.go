// Package cluster defines upstream clusters: named targets that routes send
// requests to, such as a pool of PHP workers.
package cluster

import (
	"context"
	"errors"
	"net/http"

	"github.com/berrymore/kest/internal/registry"
)

// Cluster serves requests routed to it.
//
// ServeHTTP must honor the request context: when a route timeout expires,
// the cluster stops waiting and answers 504 Gateway Timeout if nothing has
// been written yet.
type Cluster interface {
	http.Handler
	// Start prepares the cluster (for example, spawns workers) and returns
	// once it can serve requests.
	Start(ctx context.Context) error
	// Stop shuts the cluster down gracefully within the ctx deadline.
	Stop(ctx context.Context) error
}

// Prober is implemented by clusters that can tell whether they serve at all,
// such as a pool without a live worker. While one of them can't, the admin
// /ready probe fails, so that load balancers send traffic elsewhere.
type Prober interface {
	// Serving reports whether the cluster can serve requests now.
	Serving() bool
}

// Reporter is implemented by clusters that describe their state for the
// admin /clusters endpoint.
type Reporter interface {
	// Report returns the cluster's state; it is encoded as JSON.
	Report() any
}

// Resetter is implemented by clusters that can replace their workers without
// failing requests: admin POST /clusters/{name}/reset and SIGHUP.
type Resetter interface {
	// Reset replaces the workers and returns once done, or with the reason
	// it stopped; ErrResetRunning while another reset runs.
	Reset(ctx context.Context) error
}

// ErrResetRunning is returned by Reset while another reset runs.
var ErrResetRunning = errors.New("a reset is already running")

// Types is the registry of cluster implementations, keyed by config type.
var Types = registry.NewCategory[Cluster]("cluster")
