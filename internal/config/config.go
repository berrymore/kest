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
	"slices"
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

	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = ByteSize(1 << 20)
	DefaultRouteTimeout      = 30 * time.Second
	DefaultStatus            = 200
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

// ErrorAt returns an error about the value at a YAML path, such as
// "listeners[0].tls.certificates[1]", located in the file when the path is
// known. Components that check the config while the server graph is built
// use it for problems that validation can't see, like unreadable files.
func (c *Config) ErrorAt(path, msg string) *Error {
	if c.src == nil {
		return &Error{Path: path, Msg: msg}
	}
	return c.src.errorAt(path, msg)
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

// Redactor is implemented by extension configs that may hold secrets, such
// as environment variables: Redacted returns a copy to show instead.
type Redactor interface {
	Redacted() any
}

// Redacted is what DumpRedacted shows in place of a secret.
const Redacted = "[redacted]"

// DumpRedacted is Dump with every decoded extension config that implements
// Redactor replaced by its Redacted copy, for the admin API. Call it after
// the server graph is built: a config block that wasn't decoded is shown as
// Redacted, since nothing knows what it holds.
func (c *Config) DumpRedacted() ([]byte, error) {
	out := *c
	out.Clusters = slices.Clone(c.Clusters)
	for i := range out.Clusters {
		out.Clusters[i].Extension = out.Clusters[i].redacted()
	}
	out.Listeners = slices.Clone(c.Listeners)
	for i := range out.Listeners {
		h := out.Listeners[i].HTTP
		if h == nil {
			continue
		}
		copied := *h
		copied.Filters = redactedFilters(h.Filters)
		copied.VirtualHosts = slices.Clone(h.VirtualHosts)
		for j := range copied.VirtualHosts {
			vh := &copied.VirtualHosts[j]
			vh.Filters = redactedFilters(vh.Filters)
			vh.Routes = slices.Clone(vh.Routes)
			for k := range vh.Routes {
				vh.Routes[k].Filters = redactedFilters(vh.Routes[k].Filters)
			}
		}
		out.Listeners[i].HTTP = &copied
	}
	return yaml.Dump(&out, yaml.WithIndent(2))
}

func redactedFilters(filters []Extension) []Extension {
	out := slices.Clone(filters)
	for i := range out {
		out[i] = out[i].redacted()
	}
	return out
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
	// DrainDelay is how long the listeners keep serving after SIGTERM, with
	// /ready answering 503, so that load balancers stop sending traffic
	// before connections close. "0s" starts draining at once.
	DrainDelay Duration `yaml:"drain_delay"`
	// DrainTimeout is how long in-flight requests may take to finish after
	// shutdown begins. "0s" closes connections immediately.
	DrainTimeout *Duration `yaml:"drain_timeout"`
}

// Listener is a named network endpoint.
type Listener struct {
	Name    string `yaml:"name"`
	Address string `yaml:"address"`
	// TLS, if set, makes the listener serve HTTPS.
	TLS  *TLS  `yaml:"tls,omitempty"`
	HTTP *HTTP `yaml:"http"`
}

// TLS holds a listener's certificates.
type TLS struct {
	// Certificates are chosen by the server name the client asks for (SNI);
	// the first one serves clients that ask for none of them.
	Certificates []Certificate `yaml:"certificates"`
	// MinVersion is the oldest TLS version accepted: "1.2" or "1.3".
	MinVersion string `yaml:"min_version"`
}

// Certificate is a certificate chain and its private key, in PEM files.
// Relative paths resolve against the config file's directory.
type Certificate struct {
	CertFile string `yaml:"cert_file"` // the leaf first, then the intermediates
	KeyFile  string `yaml:"key_file"`
}

// Values of HTTP.Protocols.
const (
	ProtocolHTTP1 = "http1"
	ProtocolHTTP2 = "http2"
)

// TLS versions of TLS.MinVersion.
const (
	TLS12 = "1.2"
	TLS13 = "1.3"
)

// HTTP is the protocol block of an HTTP listener.
type HTTP struct {
	// Filters run in order before the router, which is always last. Virtual
	// hosts and routes have filters of their own, which run after the router
	// has chosen them.
	Filters []Extension `yaml:"filters,omitempty"`

	// Protocols are the HTTP versions served: ProtocolHTTP1, ProtocolHTTP2.
	// HTTP/2 without TLS is h2c with prior knowledge.
	Protocols []string `yaml:"protocols"`

	// NormalizePath removes "." and ".." segments from request paths, and
	// MergeSlashes collapses repeated slashes, before the filters and the
	// router see the request. Both default to true.
	NormalizePath *bool `yaml:"normalize_path"`
	MergeSlashes  *bool `yaml:"merge_slashes"`

	// Server limits. 0s turns a timeout off.
	ReadHeaderTimeout *Duration `yaml:"read_header_timeout"` // reading a request's headers
	ReadTimeout       *Duration `yaml:"read_timeout"`        // reading a whole request, body included
	WriteTimeout      *Duration `yaml:"write_timeout"`       // from the end of the headers to the end of the response
	IdleTimeout       *Duration `yaml:"idle_timeout"`        // a keep-alive connection waiting for its next request
	MaxHeaderBytes    ByteSize  `yaml:"max_header_bytes"`    // request line and headers

	VirtualHosts []VirtualHost `yaml:"virtual_hosts"`
}

// VirtualHost is a set of routes selected by the request's Host.
type VirtualHost struct {
	Name string `yaml:"name"`
	// Domains are exact hosts ("example.com"), suffix wildcards
	// ("*.example.com") or "*" for any host. Ports are ignored when matching.
	Domains []string `yaml:"domains"`
	// Filters run for every request to the virtual host, before the router
	// chooses a route.
	Filters []Extension `yaml:"filters,omitempty"`
	Routes  []Route     `yaml:"routes"`
}

// Route is a match rule and exactly one action.
type Route struct {
	// Name is optional and unique in the config; it labels the route's
	// metrics and access log lines.
	Name  string     `yaml:"name,omitempty"`
	Match RouteMatch `yaml:"match"`
	// Filters run after the virtual host's, before the action.
	Filters        []Extension     `yaml:"filters,omitempty"`
	Route          *RouteAction    `yaml:"route,omitempty"`
	DirectResponse *DirectResponse `yaml:"direct_response,omitempty"`
	Redirect       *Redirect       `yaml:"redirect,omitempty"`
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

// Redirect answers with a redirect to the request's URL with some parts
// replaced. A new scheme or host drops the request's port unless Port is set.
type Redirect struct {
	Scheme string `yaml:"scheme,omitempty"` // "http" or "https"
	Host   string `yaml:"host,omitempty"`
	Port   int    `yaml:"port,omitempty"`
	// Path replaces the whole path; PrefixRewrite replaces the part that
	// match.prefix matched.
	Path          string `yaml:"path,omitempty"`
	PrefixRewrite string `yaml:"prefix_rewrite,omitempty"`
	StripQuery    bool   `yaml:"strip_query,omitempty"`
	Status        int    `yaml:"status"` // 301, 302, 303, 307 or 308
}

// DefaultRedirectStatus is the status of a redirect that sets none.
const DefaultRedirectStatus = 301

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
		if t := c.Listeners[i].TLS; t != nil && t.MinVersion == "" {
			t.MinVersion = TLS12
		}
		h := c.Listeners[i].HTTP
		if h == nil {
			continue
		}
		for _, b := range []**bool{&h.NormalizePath, &h.MergeSlashes} {
			if *b == nil {
				on := true
				*b = &on
			}
		}
		if h.Protocols == nil {
			h.Protocols = []string{ProtocolHTTP1}
			if c.Listeners[i].TLS != nil {
				h.Protocols = append(h.Protocols, ProtocolHTTP2)
			}
		}
		for _, d := range []struct {
			field **Duration
			def   time.Duration
		}{
			{&h.ReadHeaderTimeout, DefaultReadHeaderTimeout},
			{&h.ReadTimeout, 0},
			{&h.WriteTimeout, 0},
			{&h.IdleTimeout, DefaultIdleTimeout},
		} {
			if *d.field == nil {
				v := Duration(d.def)
				*d.field = &v
			}
		}
		if h.MaxHeaderBytes == 0 {
			h.MaxHeaderBytes = DefaultMaxHeaderBytes
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
				if r.Redirect != nil && r.Redirect.Status == 0 {
					r.Redirect.Status = DefaultRedirectStatus
				}
			}
		}
	}
}
