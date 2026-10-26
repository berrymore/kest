// Package clientip implements the client_ip HTTP filter: behind a load
// balancer or a proxy, the client's IP and scheme come from forwarding
// headers, but only when the peer is a proxy the config trusts.
package clientip

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "client_ip"

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
				return nil, errors.New("client_ip: unexpected config type")
			}
			return New(*c)
		},
	})
}

// Config configures the filter.
type Config struct {
	// TrustedProxies are the addresses (192.0.2.10) and networks
	// (10.0.0.0/8) of the peers allowed to tell the client's IP; "unix"
	// trusts peers on a unix socket listener.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// Header carries the client's IP: X-Forwarded-For, Forwarded, or a
	// header with one address, such as X-Real-Ip or CF-Connecting-IP.
	Header string `yaml:"header"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{Header: xff}
}

// Headers with their own syntax.
const (
	xff       = "X-Forwarded-For"
	forwarded = "Forwarded"
	xfp       = "X-Forwarded-Proto"
)

// Validate reports missing or invalid trusted proxies and an invalid header name.
func (c *Config) Validate() error {
	var errs []error
	if len(c.TrustedProxies) == 0 {
		errs = append(errs, &config.FieldError{Field: "trusted_proxies",
			Msg: "at least one address or network is required, such as 10.0.0.0/8"})
	}
	for i, p := range c.TrustedProxies {
		if p == unixPeers {
			continue
		}
		if _, err := parsePrefix(p); err != nil {
			errs = append(errs, &config.FieldError{Field: fmt.Sprintf("trusted_proxies[%d]", i), Msg: err.Error()})
		}
	}
	if !httpx.IsToken(c.Header) {
		errs = append(errs, &config.FieldError{Field: "header", Msg: fmt.Sprintf("must be a valid HTTP header name, got %q", c.Header)})
	}
	return errors.Join(errs...)
}

// parsePrefix reads a network, or an address as a network of one.
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid network %q", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid address %q: want an IP address, a network such as 10.0.0.0/8, or %s", s, unixPeers)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// unixPeers in TrustedProxies trusts peers on a unix socket listener.
const unixPeers = "unix"

// filter holds a validated config.
type filter struct {
	trusted   []netip.Prefix
	trustUnix bool   // peers on a unix socket listener
	header    string // canonical
	parse     func(values []string) []string
}

// New returns the filter for a validated config.
func New(cfg Config) (httpx.Filter, error) {
	f := &filter{header: http.CanonicalHeaderKey(cfg.Header)}
	for _, p := range cfg.TrustedProxies {
		if p == unixPeers {
			f.trustUnix = true
			continue
		}
		prefix, err := parsePrefix(p)
		if err != nil {
			return nil, err
		}
		f.trusted = append(f.trusted, prefix)
	}
	switch f.header {
	case xff:
		f.parse = splitList
	case forwarded:
		f.parse = forwardedFor
	default:
		f.parse = lastValue
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.resolve(r)
			next.ServeHTTP(w, r)
		})
	}, nil
}

// resolve records the client's IP and scheme in the request's
// httpx.RequestInfo if the peer is trusted and says something valid.
func (f *filter) resolve(r *http.Request) {
	if !f.peerTrusted(r) {
		return
	}
	info := httpx.Info(r)
	// From the nearest hop backwards: the first address that isn't a
	// trusted proxy is the client, so a client can't choose its IP by
	// sending the header itself. If every hop is trusted, the farthest one
	// is the client. An entry that isn't an address ends the walk.
	for _, entry := range slices.Backward(f.parse(r.Header.Values(f.header))) {
		a, ok := parseAddr(entry)
		if !ok {
			break
		}
		info.ClientIP = a.String()
		if !f.isTrusted(a) {
			break
		}
	}
	if v := r.Header.Values(xfp); len(v) == 1 {
		switch p := strings.ToLower(strings.TrimSpace(v[0])); p {
		case "http", "https":
			info.Scheme = p
		}
	}
}

// peerTrusted reports whether the request's peer is a trusted proxy. A peer
// on a unix socket listener has no address: "unix" in TrustedProxies trusts
// it.
func (f *filter) peerTrusted(r *http.Request) bool {
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && local.Network() == "unix" {
		return f.trustUnix
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && f.isTrusted(peer.Addr().Unmap())
}

func (f *filter) isTrusted(a netip.Addr) bool {
	for _, p := range f.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// splitList returns the entries of a comma-separated header, over all its lines.
func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for e := range strings.SplitSeq(v, ",") {
			out = append(out, strings.TrimSpace(e))
		}
	}
	return out
}

// forwardedFor returns the for= parameter of each element of a Forwarded
// header (RFC 7239); an element without one gives "".
func forwardedFor(values []string) []string {
	elements := splitList(values)
	out := make([]string, len(elements))
	for i, e := range elements {
		for pair := range strings.SplitSeq(e, ";") {
			name, value, _ := strings.Cut(strings.TrimSpace(pair), "=")
			if strings.EqualFold(name, "for") {
				out[i] = strings.Trim(value, `"`)
			}
		}
	}
	return out
}

// lastValue reads a header with one address: its last line, as the nearest
// proxy sets it.
func lastValue(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return []string{strings.TrimSpace(values[len(values)-1])}
}

// parseAddr reads an address with or without a port: 192.0.2.1,
// 192.0.2.1:4711, 2001:db8::1, [2001:db8::1]:4711.
func parseAddr(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil && ap.Addr().Zone() == "" {
		return ap.Addr().Unmap(), true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return parseAddr(s[1 : len(s)-1])
	}
	return netip.Addr{}, false
}
