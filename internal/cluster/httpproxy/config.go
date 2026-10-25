package httpproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
)

// Load balancing policies.
const (
	RoundRobin   = "round_robin"
	LeastRequest = "least_request"
	Random       = "random"
)

// Retry conditions.
const (
	RetryConnectFailure = "connect_failure" // the connection couldn't be made: nothing was sent
	RetryGatewayError   = "gateway_error"   // 502, 503 or 504 from the endpoint
	RetryReset          = "reset"           // the connection broke before a response
)

// Config is the config block of an http_proxy cluster.
type Config struct {
	// Endpoints are the upstream servers: scheme://host[:port].
	Endpoints []string `yaml:"endpoints"`
	// LoadBalancing picks an endpoint per request: RoundRobin, LeastRequest
	// or Random.
	LoadBalancing string `yaml:"load_balancing"`
	// Host is the Host header sent upstream; empty keeps the client's.
	Host string `yaml:"host,omitempty"`
	// ConnectTimeout bounds making a connection, the TLS handshake included.
	ConnectTimeout config.Duration `yaml:"connect_timeout"`
	// MaxConnections bounds the connections to each endpoint; requests
	// beyond it wait for one.
	MaxConnections int `yaml:"max_connections"`
	// MaxIdleConnections bounds the idle connections kept per endpoint.
	MaxIdleConnections int `yaml:"max_idle_connections"`
	// IdleTimeout closes an idle connection after this long.
	IdleTimeout config.Duration `yaml:"idle_timeout"`
	// HTTP2 speaks HTTP/2 to the endpoints: h2 over TLS, h2c to http ones.
	HTTP2 bool `yaml:"http2"`

	TLS         TLS         `yaml:"tls"`
	HealthCheck HealthCheck `yaml:"health_check"`
	Retries     Retries     `yaml:"retries"`
}

// TLS configures connections to https endpoints.
type TLS struct {
	// CAFile holds the PEM certificates trusted for the endpoints; empty
	// trusts the system's.
	CAFile string `yaml:"ca_file,omitempty"`
	// ServerName is checked against the endpoints' certificates; empty
	// means each endpoint's host.
	ServerName string `yaml:"server_name,omitempty"`
	// InsecureSkipVerify accepts any certificate: for development only.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`
}

// HealthCheck probes every endpoint with a GET; off unless Path is set.
type HealthCheck struct {
	Path               string          `yaml:"path,omitempty"`
	Interval           config.Duration `yaml:"interval"`
	Timeout            config.Duration `yaml:"timeout"`
	HealthyThreshold   int             `yaml:"healthy_threshold"`
	UnhealthyThreshold int             `yaml:"unhealthy_threshold"`
}

// Retries sends a failed request to another endpoint.
type Retries struct {
	// Count is how many times a request is retried; 0 turns retries off.
	Count int `yaml:"count"`
	// On lists the conditions that are retried.
	On []string `yaml:"on"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		LoadBalancing:      RoundRobin,
		ConnectTimeout:     config.Duration(5 * time.Second),
		MaxConnections:     1024,
		MaxIdleConnections: 64,
		IdleTimeout:        config.Duration(90 * time.Second),
		HealthCheck: HealthCheck{
			Interval:           config.Duration(10 * time.Second),
			Timeout:            config.Duration(2 * time.Second),
			HealthyThreshold:   2,
			UnhealthyThreshold: 3,
		},
		Retries: Retries{Count: 1, On: []string{RetryConnectFailure}},
	}
}

// Validate reports every invalid field as a *config.FieldError.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	if len(c.Endpoints) == 0 {
		add("endpoints", "at least one endpoint is required, such as http://10.0.0.1:8080")
	}
	for i, e := range c.Endpoints {
		if _, err := parseEndpoint(e); err != nil {
			add(fmt.Sprintf("endpoints[%d]", i), "%v", err)
		} else if slices.Index(c.Endpoints, e) < i {
			add(fmt.Sprintf("endpoints[%d]", i), "duplicate endpoint %q", e)
		}
	}
	if !slices.Contains([]string{RoundRobin, LeastRequest, Random}, c.LoadBalancing) {
		add("load_balancing", "unknown policy %q; use %s, %s or %s", c.LoadBalancing, RoundRobin, LeastRequest, Random)
	}
	if c.Host != "" && strings.ContainsAny(c.Host, " /\t\r\n") {
		add("host", "invalid host %q", c.Host)
	}
	positive := func(field string, d config.Duration) {
		if d.Std() <= 0 {
			add(field, "must be positive, got %s", d)
		}
	}
	positive("connect_timeout", c.ConnectTimeout)
	positive("idle_timeout", c.IdleTimeout)
	if c.MaxConnections < 1 {
		add("max_connections", "must be positive, got %d", c.MaxConnections)
	}
	if c.MaxIdleConnections < 0 || c.MaxIdleConnections > c.MaxConnections {
		add("max_idle_connections", "must be between 0 and max_connections (%d), got %d", c.MaxConnections, c.MaxIdleConnections)
	}
	if hc := c.HealthCheck; hc.Path != "" {
		if !strings.HasPrefix(hc.Path, "/") {
			add("health_check.path", "must start with /")
		}
		positive("health_check.interval", hc.Interval)
		positive("health_check.timeout", hc.Timeout)
		if hc.Timeout > hc.Interval {
			add("health_check.timeout", "must not be longer than interval (%s)", hc.Interval)
		}
		if hc.HealthyThreshold < 1 {
			add("health_check.healthy_threshold", "must be positive, got %d", hc.HealthyThreshold)
		}
		if hc.UnhealthyThreshold < 1 {
			add("health_check.unhealthy_threshold", "must be positive, got %d", hc.UnhealthyThreshold)
		}
	}
	if c.Retries.Count < 0 || c.Retries.Count > 10 {
		add("retries.count", "must be between 0 and 10, got %d", c.Retries.Count)
	}
	for i, on := range c.Retries.On {
		if !slices.Contains([]string{RetryConnectFailure, RetryGatewayError, RetryReset}, on) {
			add(fmt.Sprintf("retries.on[%d]", i), "unknown condition %q; use %s, %s or %s", on, RetryConnectFailure,
				RetryGatewayError, RetryReset)
		}
	}
	return errors.Join(errs...)
}

// parseEndpoint reads scheme://host[:port], without a path, query or user.
func parseEndpoint(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return nil, fmt.Errorf("invalid endpoint %q", s)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("invalid endpoint %q: want http:// or https://", s)
	case u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("invalid endpoint %q: want scheme://host[:port], without a path", s)
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil && port == "" {
		return nil, fmt.Errorf("invalid endpoint %q: empty port", s)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}
