// Package headers implements the headers HTTP filter: it sets, adds and
// removes request headers on the way to the cluster and response headers on
// the way to the client.
package headers

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "headers"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type:      Type,
		NewConfig: func() any { return &Config{} },
		Build: func(_ registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("headers: unexpected config type")
			}
			return New(*c), nil
		},
	})
}

// Config configures the filter.
type Config struct {
	Request  Rules `yaml:"request"`  // applied to the request the cluster gets
	Response Rules `yaml:"response"` // applied to every response, from clusters and from kest
}

// Rules change a set of headers, in this order: Remove, Set, Add, SetIfAbsent.
type Rules struct {
	Remove      []string          `yaml:"remove,omitempty"`
	Set         map[string]string `yaml:"set,omitempty"`           // replaces any value
	Add         map[string]string `yaml:"add,omitempty"`           // adds a value to those there are
	SetIfAbsent map[string]string `yaml:"set_if_absent,omitempty"` // only when there is no value
}

// protected are headers that kest or net/http manage: changing them would
// break the framing or the connection.
var protected = []string{"Host", "Content-Length", "Transfer-Encoding", "Connection", "Keep-Alive",
	"Proxy-Connection", "Te", "Trailer", "Upgrade"}

// Validate reports invalid or protected header names and invalid values.
func (c *Config) Validate() error {
	return errors.Join(c.Request.validate("request"), c.Response.validate("response"))
}

func (r *Rules) validate(path string) error {
	var errs []error
	check := func(field, name string) {
		switch {
		case !httpx.IsToken(name):
			errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf("invalid header name %q", name)})
		case slices.Contains(protected, http.CanonicalHeaderKey(name)):
			errs = append(errs, &config.FieldError{Field: field, Msg: http.CanonicalHeaderKey(name) + " can't be changed"})
		}
	}
	for i, name := range r.Remove {
		check(fmt.Sprintf("%s.remove[%d]", path, i), name)
	}
	for _, op := range []struct {
		name   string
		values map[string]string
	}{{"set", r.Set}, {"add", r.Add}, {"set_if_absent", r.SetIfAbsent}} {
		for _, name := range slices.Sorted(maps.Keys(op.values)) {
			field := path + "." + op.name + "." + name
			check(field, name)
			if !httpx.IsHeaderValue(op.values[name]) {
				errs = append(errs, &config.FieldError{Field: field, Msg: "the value has control characters"})
			}
		}
	}
	return errors.Join(errs...)
}

// rules is Rules with canonical names, in a fixed order.
type rules struct {
	remove                []string
	set, add, setIfAbsent [][2]string
}

func compile(r Rules) rules {
	pairs := func(m map[string]string) [][2]string {
		out := make([][2]string, 0, len(m))
		for _, name := range slices.Sorted(maps.Keys(m)) {
			out = append(out, [2]string{http.CanonicalHeaderKey(name), m[name]})
		}
		return out
	}
	out := rules{set: pairs(r.Set), add: pairs(r.Add), setIfAbsent: pairs(r.SetIfAbsent)}
	for _, name := range r.Remove {
		out.remove = append(out.remove, http.CanonicalHeaderKey(name))
	}
	return out
}

func (r *rules) empty() bool {
	return len(r.remove)+len(r.set)+len(r.add)+len(r.setIfAbsent) == 0
}

func (r *rules) apply(h http.Header) {
	for _, name := range r.remove {
		if name == "Date" {
			h[name] = nil // net/http adds a Date unless the key holds nil
		} else {
			delete(h, name)
		}
	}
	for _, kv := range r.set {
		h[kv[0]] = []string{kv[1]}
	}
	for _, kv := range r.add {
		h[kv[0]] = append(h[kv[0]], kv[1])
	}
	for _, kv := range r.setIfAbsent {
		if len(h[kv[0]]) == 0 {
			h[kv[0]] = []string{kv[1]}
		}
	}
}

// New returns the filter for a validated config.
func New(cfg Config) httpx.Filter {
	req, resp := compile(cfg.Request), compile(cfg.Response)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !req.empty() {
				// Handlers must not change the request they get: change a copy.
				out := *r
				out.Header = r.Header.Clone()
				req.apply(out.Header)
				r = &out
			}
			if !resp.empty() {
				w = httpx.BeforeHeader(w, func(h http.Header, _ int) { resp.apply(h) })
			}
			next.ServeHTTP(w, r)
		})
	}
}
