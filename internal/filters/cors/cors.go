// Package cors implements the cors HTTP filter: it answers CORS preflight
// requests and adds the CORS headers to the responses for allowed origins,
// so that browsers let pages from those origins call the application.
package cors

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "cors"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(_ registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("cors: unexpected config type")
			}
			return New(*c), nil
		},
	})
}

// Config configures the filter.
type Config struct {
	// AllowOrigins are exact origins (https://app.example.com), origins with
	// a subdomain wildcard (https://*.example.com) or "*" for any origin.
	AllowOrigins []string `yaml:"allow_origins"`
	// AllowMethods may be used in cross-origin requests.
	AllowMethods []string `yaml:"allow_methods"`
	// AllowHeaders may be sent in cross-origin requests; "*" allows any.
	AllowHeaders []string `yaml:"allow_headers"`
	// ExposeHeaders are response headers that scripts may read, beyond the
	// safelisted ones.
	ExposeHeaders []string `yaml:"expose_headers"`
	// AllowCredentials lets requests carry cookies and HTTP authentication.
	AllowCredentials bool `yaml:"allow_credentials"`
	// MaxAge is how long browsers may cache a preflight answer; 0s: not sent.
	MaxAge config.Duration `yaml:"max_age"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		AllowMethods: []string{http.MethodGet, http.MethodHead, http.MethodPost},
		MaxAge:       config.Duration(5 * time.Minute),
	}
}

// Validate reports invalid origins, methods and header names.
func (c *Config) Validate() error {
	var errs []error
	fail := func(field, format string, args ...any) {
		errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	if len(c.AllowOrigins) == 0 {
		fail("allow_origins", `at least one origin is required, such as https://app.example.com, or "*"`)
	}
	for i, o := range c.AllowOrigins {
		field := fmt.Sprintf("allow_origins[%d]", i)
		switch {
		case o == "*" && c.AllowCredentials:
			fail(field, `"*" can't be used with allow_credentials: list the origins`)
		case o == "*":
		default:
			if _, err := parseOrigin(o); err != nil {
				fail(field, "%v", err)
			}
		}
	}
	for i, m := range c.AllowMethods {
		if !httpx.IsToken(m) {
			fail(fmt.Sprintf("allow_methods[%d]", i), "invalid method %q", m)
		}
	}
	for _, list := range []struct {
		field    string
		names    []string
		wildcard bool
	}{{"allow_headers", c.AllowHeaders, true}, {"expose_headers", c.ExposeHeaders, false}} {
		for i, h := range list.names {
			if !httpx.IsToken(h) || (h == "*" && !list.wildcard) {
				fail(fmt.Sprintf("%s[%d]", list.field, i), "invalid header name %q", h)
			}
		}
	}
	if c.MaxAge < 0 {
		fail("max_age", "must not be negative")
	}
	return errors.Join(errs...)
}

// origin is an allowed origin: an exact one, or a suffix after a wildcard.
type origin struct {
	exact  string // "https://app.example.com"
	scheme string // for a wildcard: "https"
	suffix string // for a wildcard: ".example.com", with the port if any
}

// parseOrigin reads scheme://host[:port], where host may start with "*.".
func parseOrigin(s string) (origin, error) {
	scheme, host, ok := strings.Cut(strings.ToLower(s), "://")
	if !ok || scheme == "" || host == "" || strings.ContainsAny(host, "/?#@ ") {
		return origin{}, fmt.Errorf("invalid origin %q: want scheme://host[:port], such as https://app.example.com", s)
	}
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		if rest == "" || strings.Contains(rest, "*") {
			return origin{}, fmt.Errorf("invalid origin %q", s)
		}
		return origin{scheme: scheme, suffix: "." + rest}, nil
	}
	if strings.Contains(host, "*") {
		return origin{}, fmt.Errorf(`invalid origin %q: a wildcard is only allowed as "*." at the start of the host`, s)
	}
	if _, err := url.Parse(scheme + "://" + host); err != nil {
		return origin{}, fmt.Errorf("invalid origin %q", s)
	}
	return origin{exact: scheme + "://" + host}, nil
}

func (o origin) matches(requested string) bool {
	if o.exact != "" {
		return requested == o.exact
	}
	scheme, host, ok := strings.Cut(requested, "://")
	return ok && scheme == o.scheme && len(host) > len(o.suffix) && strings.HasSuffix(host, o.suffix)
}

// policy is a validated config, ready for requests.
type policy struct {
	anyOrigin    bool
	origins      []origin
	methods      []string
	methodsValue string
	anyHeader    bool
	headers      []string // lower case
	exposeValue  string
	credentials  bool
	maxAge       string // seconds; "" when not sent
}

// New returns the filter for a validated config.
func New(cfg Config) httpx.Filter {
	p := &policy{
		methods:      cfg.AllowMethods,
		methodsValue: strings.Join(cfg.AllowMethods, ", "),
		exposeValue:  strings.Join(cfg.ExposeHeaders, ", "),
		credentials:  cfg.AllowCredentials,
	}
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			p.anyOrigin = true
			continue
		}
		if parsed, err := parseOrigin(o); err == nil {
			p.origins = append(p.origins, parsed)
		}
	}
	for _, h := range cfg.AllowHeaders {
		if h == "*" {
			p.anyHeader = true
		}
		p.headers = append(p.headers, strings.ToLower(h))
	}
	if s := int64(cfg.MaxAge.Std().Seconds()); s > 0 {
		p.maxAge = strconv.FormatInt(s, 10)
	}
	return p.filter
}

func (p *policy) filter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if r.Method == http.MethodOptions && o != "" && r.Header.Get("Access-Control-Request-Method") != "" {
			p.preflight(w, r, o)
			return
		}
		w = httpx.BeforeHeader(w, func(h http.Header, _ int) {
			if p.varies() {
				h.Add("Vary", "Origin")
			}
			if o == "" || !p.allows(o) {
				return
			}
			h.Set("Access-Control-Allow-Origin", p.allowOrigin(o))
			if p.credentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}
			if p.exposeValue != "" {
				h.Set("Access-Control-Expose-Headers", p.exposeValue)
			}
		})
		next.ServeHTTP(w, r)
	})
}

// varies reports whether responses depend on the Origin: unless any origin
// gets "*", caches must keep them apart.
func (p *policy) varies() bool {
	return !p.anyOrigin || p.credentials
}

func (p *policy) allows(o string) bool {
	if p.anyOrigin {
		return true
	}
	o = strings.ToLower(o)
	for _, allowed := range p.origins {
		if allowed.matches(o) {
			return true
		}
	}
	return false
}

func (p *policy) allowOrigin(o string) string {
	if p.anyOrigin && !p.credentials {
		return "*"
	}
	return o
}

// preflight answers a preflight request itself: 204, with the CORS headers
// only if the origin, the method and every header are allowed.
func (p *policy) preflight(w http.ResponseWriter, r *http.Request, o string) {
	h := w.Header()
	h.Add("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	method := r.Header.Get("Access-Control-Request-Method")
	requested := requestedHeaders(r)
	if p.allows(o) && slices.Contains(p.methods, method) && p.allowsHeaders(requested) {
		h.Set("Access-Control-Allow-Origin", p.allowOrigin(o))
		h.Set("Access-Control-Allow-Methods", p.methodsValue)
		if len(requested) > 0 {
			h.Set("Access-Control-Allow-Headers", strings.Join(requested, ", "))
		}
		if p.credentials {
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if p.maxAge != "" {
			h.Set("Access-Control-Max-Age", p.maxAge)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestedHeaders returns the header names a preflight asks for, lower case.
func requestedHeaders(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("Access-Control-Request-Headers") {
		for name := range strings.SplitSeq(v, ",") {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

func (p *policy) allowsHeaders(requested []string) bool {
	if p.anyHeader {
		return true
	}
	for _, h := range requested {
		if !slices.Contains(p.headers, h) {
			return false
		}
	}
	return true
}
