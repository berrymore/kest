// Package config loads, validates and describes the kest configuration file.
//
// The pipeline is: parse YAML into a node tree, expand ${ENV} references in
// scalar values, decode strictly (unknown fields are errors), apply
// defaults, then validate. Every error carries the YAML path of the offending
// value and, when known, its line and column.
//
// Extension configs ({type, config} blocks) are kept raw here and decoded by
// the extension registry when the server graph is built; see Extension.
package config

import (
	"time"

	"go.yaml.in/yaml/v4"
)

// SchemaVersion is the only supported value of the top-level "version" field.
const SchemaVersion = "v1"

// Defaults for optional settings.
const (
	DefaultLogLevel     = "info"
	DefaultLogFormat    = "text"
	DefaultAdminAddress = "127.0.0.1:9901"
	DefaultDrainTimeout = 15 * time.Second
	DefaultRouteTimeout = 30 * time.Second
	DefaultStatus       = 200
)

// Config is the whole configuration file.
type Config struct {
	Version   string     `yaml:"version"`
	Log       Log        `yaml:"log"`
	Admin     Admin      `yaml:"admin"`
	Server    Server     `yaml:"server"`
	Listeners []Listener `yaml:"listeners"`
	Clusters  []Cluster  `yaml:"clusters,omitempty"`

	src *source
}

// File returns the path the configuration was loaded from.
func (c *Config) File() string {
	if c.src == nil {
		return ""
	}
	return c.src.file
}

// Dump returns the effective configuration as YAML, with defaults applied.
// Extension configs show their decoded values once the server graph has been
// built from this Config, and their raw blocks before that.
func (c *Config) Dump() ([]byte, error) {
	return yaml.Dump(c, yaml.WithIndent(2))
}

// Log configures the process logger.
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// Admin configures the admin HTTP server.
type Admin struct {
	Address string `yaml:"address"`
}

// Server holds process-wide settings.
type Server struct {
	// DrainTimeout is how long in-flight requests may take to finish after
	// shutdown begins. "0s" closes connections immediately.
	DrainTimeout *Duration `yaml:"drain_timeout"`
}

// Listener is a named network endpoint.
type Listener struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
	HTTP    *HTTP  `yaml:"http"`
}

// HTTP is the protocol block of an HTTP listener.
type HTTP struct {
	// Filters run in order before the router, which is always last.
	Filters      []Extension   `yaml:"filters,omitempty"`
	VirtualHosts []VirtualHost `yaml:"virtual_hosts"`
}

// VirtualHost is a set of routes selected by the request's Host.
type VirtualHost struct {
	Name string `yaml:"name"`
	// Domains are exact hosts ("example.com"), suffix wildcards
	// ("*.example.com") or "*" for any host. Ports are ignored when matching.
	Domains []string `yaml:"domains"`
	Routes  []Route  `yaml:"routes"`
}

// Route is a match rule and exactly one action.
type Route struct {
	Match          RouteMatch      `yaml:"match"`
	Route          *RouteAction    `yaml:"route,omitempty"`
	DirectResponse *DirectResponse `yaml:"direct_response,omitempty"`
}

// RouteMatch selects requests by path. Exactly one field must be set.
type RouteMatch struct {
	Prefix string `yaml:"prefix,omitempty"`
	Path   string `yaml:"path,omitempty"`
}

// RouteAction forwards the request to a cluster.
type RouteAction struct {
	Cluster string `yaml:"cluster"`
	// Timeout bounds the whole request; "0s" disables it.
	Timeout *Duration `yaml:"timeout"`
}

// DirectResponse answers the request without an upstream.
type DirectResponse struct {
	Status int    `yaml:"status"`
	Body   string `yaml:"body,omitempty"`
}

// Cluster is a named upstream; its type selects the implementation.
type Cluster struct {
	Name      string `yaml:"name"`
	Extension `yaml:",inline"`
}

func (c *Config) applyDefaults() {
	if c.Log.Level == "" {
		c.Log.Level = DefaultLogLevel
	}
	if c.Log.Format == "" {
		c.Log.Format = DefaultLogFormat
	}
	if c.Admin.Address == "" {
		c.Admin.Address = DefaultAdminAddress
	}
	if c.Server.DrainTimeout == nil {
		d := Duration(DefaultDrainTimeout)
		c.Server.DrainTimeout = &d
	}
	for i := range c.Listeners {
		h := c.Listeners[i].HTTP
		if h == nil {
			continue
		}
		for j := range h.VirtualHosts {
			vh := &h.VirtualHosts[j]
			for k, d := range vh.Domains {
				vh.Domains[k] = NormalizeHost(d)
			}
			for k := range vh.Routes {
				r := &vh.Routes[k]
				if r.Route != nil && r.Route.Timeout == nil {
					d := Duration(DefaultRouteTimeout)
					r.Route.Timeout = &d
				}
				if r.DirectResponse != nil && r.DirectResponse.Status == 0 {
					r.DirectResponse.Status = DefaultStatus
				}
			}
		}
	}
}
