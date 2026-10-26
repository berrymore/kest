// Package admin implements the admin HTTP API, modeled on Envoy's admin
// interface: readiness, server information, the effective config, cluster
// state and resets, and metrics.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/buildinfo"
	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/metrics"
)

// State is the lifecycle state of the server.
type State string

// Server states.
const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDraining State = "draining"
)

// Source provides the server facts that the admin API exposes.
type Source interface {
	State() State
	StartedAt() time.Time
	ConfigFile() string
	// NotServing names the clusters that can't serve requests now
	// (cluster.Prober), and the plugins, as "plugin <name>" (plugin.Prober).
	NotServing() []string
	// Clusters describes every cluster, in config order.
	Clusters() []ClusterInfo
	// ResetCluster replaces a cluster's workers (cluster.Resetter): nil once
	// done, ErrUnknownCluster, ErrCantReset, ErrNotReady,
	// cluster.ErrResetRunning, or why the reset stopped.
	ResetCluster(ctx context.Context, name string) error
	// ConfigDump is the effective configuration as YAML, secrets redacted.
	ConfigDump() ([]byte, error)
	Metrics() *metrics.Registry
}

// Errors of Source.ResetCluster.
var (
	ErrUnknownCluster = errors.New("no such cluster")
	ErrCantReset      = errors.New("this type of cluster can't reset its workers")
	ErrNotReady       = errors.New("the server is not ready")
)

// ClusterInfo describes a cluster for /clusters.
type ClusterInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Serving *bool  `json:"serving,omitempty"` // nil if the cluster can't tell (no cluster.Prober)
	Status  any    `json:"status,omitempty"`  // the cluster's cluster.Reporter output
}

var endpoints = []struct {
	pattern, help string
}{
	{"GET /ready", "200 when serving traffic; 503 while starting, draining, or a cluster has no live worker"},
	{"GET /server_info", "version, state, uptime and config file (JSON)"},
	{"GET /config_dump", "the effective configuration with defaults, env values redacted (YAML)"},
	{"GET /clusters", "every cluster with its state and workers (JSON)"},
	{"POST /clusters/{name}/reset", "replace a cluster's workers one by one; returns when done"},
	{"GET /metrics", "metrics in the Prometheus text format"},
}

// NewHandler returns the admin API handler.
func NewHandler(src Source) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "kest admin")
		for _, e := range endpoints {
			fmt.Fprintf(w, "  %-18s %s\n", e.pattern, e.help)
		}
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		state := src.State()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if state != StateReady {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, state)
			return
		}
		if down := src.NotServing(); len(down) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "not serving:", strings.Join(down, ", "))
			return
		}
		fmt.Fprintln(w, state)
	})
	mux.HandleFunc("GET /config_dump", func(w http.ResponseWriter, _ *http.Request) {
		out, err := src.ConfigDump()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(out)
	})
	mux.HandleFunc("GET /clusters", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, struct {
			Clusters []ClusterInfo `json:"clusters"`
		}{src.Clusters()})
	})
	mux.HandleFunc("POST /clusters/{name}/reset", func(w http.ResponseWriter, r *http.Request) {
		// A client that gives up doesn't stop the reset half-way.
		err := src.ResetCluster(context.WithoutCancel(r.Context()), r.PathValue("name"))
		status := http.StatusOK
		switch {
		case err == nil:
		case errors.Is(err, ErrUnknownCluster):
			status = http.StatusNotFound
		case errors.Is(err, ErrCantReset):
			status = http.StatusNotImplemented
		case errors.Is(err, cluster.ErrResetRunning):
			status = http.StatusConflict
		case errors.Is(err, ErrNotReady):
			status = http.StatusServiceUnavailable
		default:
			status = http.StatusInternalServerError
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		if err != nil {
			fmt.Fprintln(w, err)
			return
		}
		fmt.Fprintln(w, "reset done")
	})
	mux.Handle("GET /metrics", src.Metrics().Handler())
	mux.HandleFunc("GET /server_info", func(w http.ResponseWriter, _ *http.Request) {
		bi := buildinfo.Get()
		writeJSON(w, serverInfo{
			Version:       bi.Version,
			Commit:        bi.Commit,
			GoVersion:     bi.GoVersion,
			State:         src.State(),
			StartedAt:     src.StartedAt().UTC().Format(time.RFC3339),
			UptimeSeconds: int64(time.Since(src.StartedAt()).Seconds()),
			ConfigFile:    src.ConfigFile(),
		})
	})
	return mux
}

type serverInfo struct {
	Version       string `json:"version"`
	Commit        string `json:"commit,omitempty"`
	GoVersion     string `json:"go_version"`
	State         State  `json:"state"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	ConfigFile    string `json:"config_file"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
