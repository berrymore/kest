package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type validator struct {
	src  *source
	errs Errors
}

func (v *validator) errorf(path, format string, args ...any) {
	v.errs = append(v.errs, v.src.errorAt(path, fmt.Sprintf(format, args...)))
}

func (c *Config) validate() error {
	v := &validator{src: c.src}

	switch c.Version {
	case SchemaVersion:
	case "":
		v.errorf("version", "required; set it to %q", SchemaVersion)
	default:
		v.errorf("version", "unsupported version %q; supported: %q", c.Version, SchemaVersion)
	}

	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.Log.Level) {
		v.errorf("log.level", "unknown level %q; use debug, info, warn or error", c.Log.Level)
	}
	if !slices.Contains([]string{"text", "json"}, c.Log.Format) {
		v.errorf("log.format", "unknown format %q; use text or json", c.Log.Format)
	}
	if err := checkAddress(c.Admin.Address); err != nil {
		v.errorf("admin.address", "%v", err)
	}
	if c.Server.DrainTimeout.Std() < 0 {
		v.errorf("server.drain_timeout", "must not be negative")
	}
	if c.Server.DrainDelay < 0 {
		v.errorf("server.drain_delay", "must not be negative")
	}

	clusters := v.clusters(c.Clusters)
	v.listeners(c.Listeners, c.Admin.Address, clusters)

	return v.errs.Err()
}

// clusters validates cluster declarations and returns the set of their names.
func (v *validator) clusters(list []Cluster) map[string]bool {
	names := make(map[string]bool, len(list))
	for i := range list {
		c := &list[i]
		path := indexPath("clusters", i)
		c.bind(v.src, path)

		v.name(path, c.Name, names, "cluster")
		if c.Type == "" {
			v.errorf(path+".type", "required")
		}
	}
	return names
}

func (v *validator) listeners(list []Listener, adminAddress string, clusters map[string]bool) {
	if len(list) == 0 {
		v.errorf("listeners", "at least one listener is required")
		return
	}

	names := make(map[string]bool, len(list))
	addresses := map[string]string{adminAddress: "admin.address"}
	for i := range list {
		l := &list[i]
		path := indexPath("listeners", i)

		v.name(path, l.Name, names, "listener")
		if err := checkAddress(l.Address); err != nil {
			v.errorf(path+".address", "%v", err)
		} else if other, dup := addresses[l.Address]; dup && !isEphemeral(l.Address) {
			v.errorf(path+".address", "address %s is already used by %s", l.Address, other)
		} else {
			addresses[l.Address] = path + ".address"
		}

		if l.HTTP == nil {
			v.errorf(path+".http", "required: http is the only supported protocol")
			continue
		}
		v.http(path+".http", l.HTTP, clusters)
	}
}

// Bounds of http.max_header_bytes.
const (
	minHeaderBytes = ByteSize(1 << 10)
	maxHeaderBytes = ByteSize(64 << 20)
)

func (v *validator) http(path string, h *HTTP, clusters map[string]bool) {
	for name, d := range map[string]*Duration{"read_header_timeout": h.ReadHeaderTimeout, "read_timeout": h.ReadTimeout,
		"write_timeout": h.WriteTimeout, "idle_timeout": h.IdleTimeout} {
		if d.Std() < 0 {
			v.errorf(path+"."+name, "must not be negative")
		}
	}
	if h.MaxHeaderBytes < minHeaderBytes || h.MaxHeaderBytes > maxHeaderBytes {
		v.errorf(path+".max_header_bytes", "must be between %s and %s, got %s", minHeaderBytes, maxHeaderBytes, h.MaxHeaderBytes)
	}

	for i := range h.Filters {
		f := &h.Filters[i]
		fpath := indexPath(path+".filters", i)
		f.bind(v.src, fpath)
		if f.Type == "" {
			v.errorf(fpath+".type", "required")
		}
	}

	if len(h.VirtualHosts) == 0 {
		v.errorf(path+".virtual_hosts", "at least one virtual host is required")
		return
	}

	names := make(map[string]bool, len(h.VirtualHosts))
	domains := make(map[string]string)
	for i := range h.VirtualHosts {
		vh := &h.VirtualHosts[i]
		vpath := indexPath(path+".virtual_hosts", i)

		v.name(vpath, vh.Name, names, "virtual host")

		if len(vh.Domains) == 0 {
			v.errorf(vpath+".domains", `at least one domain is required; use "*" to match any host`)
		}
		for j, d := range vh.Domains {
			dpath := indexPath(vpath+".domains", j)
			if err := checkDomain(d); err != nil {
				v.errorf(dpath, "%v", err)
			} else if other, dup := domains[d]; dup {
				v.errorf(dpath, "domain %q is already used by %s", d, other)
			} else {
				domains[d] = dpath
			}
		}

		if len(vh.Routes) == 0 {
			v.errorf(vpath+".routes", "at least one route is required")
		}
		for j := range vh.Routes {
			v.route(indexPath(vpath+".routes", j), &vh.Routes[j], clusters)
		}
	}
}

func (v *validator) route(path string, r *Route, clusters map[string]bool) {
	m := r.Match
	switch {
	case m.Prefix == "" && m.Path == "":
		v.errorf(path+".match", "required: set prefix or path")
	case m.Prefix != "" && m.Path != "":
		v.errorf(path+".match", "set only one of prefix or path")
	case m.Prefix != "" && !strings.HasPrefix(m.Prefix, "/"):
		v.errorf(path+".match.prefix", "must start with /")
	case m.Path != "" && !strings.HasPrefix(m.Path, "/"):
		v.errorf(path+".match.path", "must start with /")
	}

	switch {
	case r.Route == nil && r.DirectResponse == nil:
		v.errorf(path, "an action is required: set route or direct_response")
	case r.Route != nil && r.DirectResponse != nil:
		v.errorf(path, "set only one action: route or direct_response")
	case r.Route != nil:
		switch {
		case r.Route.Cluster == "":
			v.errorf(path+".route.cluster", "required")
		case !clusters[r.Route.Cluster]:
			v.errorf(path+".route.cluster", "unknown cluster %q", r.Route.Cluster)
		}
		if r.Route.Timeout.Std() < 0 {
			v.errorf(path+".route.timeout", "must not be negative")
		}
	case r.DirectResponse != nil:
		if s := r.DirectResponse.Status; s < 100 || s > 599 {
			v.errorf(path+".direct_response.status", "invalid HTTP status %d", s)
		}
	}
}

// name checks a component name and its uniqueness within seen.
func (v *validator) name(path, name string, seen map[string]bool, kind string) {
	switch {
	case name == "":
		v.errorf(path+".name", "required")
	case !nameRE.MatchString(name):
		v.errorf(path+".name", "invalid name %q: use up to 64 letters, digits, '_', '.' or '-'", name)
	case seen[name]:
		v.errorf(path+".name", "duplicate %s name %q", kind, name)
	default:
		seen[name] = true
	}
}

func checkAddress(addr string) error {
	if addr == "" {
		return errors.New("required")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: want host:port, like 127.0.0.1:8080 or :8080", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid port %q in address %q", port, addr)
	}
	return nil
}

// isEphemeral reports whether addr asks for any free port (port 0), which
// never conflicts with another address.
func isEphemeral(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	return err == nil && port == "0"
}

func checkDomain(d string) error {
	switch {
	case d == "":
		return errors.New("must not be empty")
	case d == "*":
		return nil
	case strings.HasPrefix(d, "*."):
		if rest := d[2:]; rest == "" || strings.Contains(rest, "*") {
			return fmt.Errorf("invalid wildcard domain %q", d)
		}
		d = d[2:]
	case strings.Contains(d, "*"):
		return fmt.Errorf(`invalid domain %q: a wildcard is only allowed as "*" or a "*." prefix`, d)
	}

	switch {
	case strings.HasPrefix(d, "["):
		if !strings.HasSuffix(d, "]") {
			return fmt.Errorf("invalid IPv6 domain %q", d)
		}
	case strings.Contains(d, ":"):
		return fmt.Errorf("invalid domain %q: domains must not include a port", d)
	case strings.ContainsAny(d, " \t/"):
		return fmt.Errorf("invalid domain %q", d)
	}
	return nil
}

// NormalizeHost lowercases a host and drops a trailing dot, so that
// configured domains compare equal to normalized request hosts.
func NormalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
