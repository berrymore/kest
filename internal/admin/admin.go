// Package admin implements the admin HTTP API, modeled on Envoy's admin
// interface: readiness, server information, metrics and, later, config dumps
// and cluster state.
package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/buildinfo"
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
	// (cluster.Prober).
	NotServing() []string
	Metrics() *metrics.Registry
}

var endpoints = []struct {
	pattern, help string
}{
	{"GET /ready", "200 when serving traffic; 503 while starting, draining, or a cluster has no live worker"},
	{"GET /server_info", "version, state, uptime and config file (JSON)"},
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
