// Package static implements the static HTTP filter: it answers GET and HEAD
// requests for the files of a directory, such as an application's public/,
// and passes every other request on to the rest of the chain, as nginx's
// try_files $uri @app does.
package static

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "static"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(bc registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("static: unexpected config type")
			}
			return New(c, bc.ConfigDir)
		},
	})
}

// Config configures the filter.
type Config struct {
	// Dir is served, relative to the config file.
	Dir string `yaml:"dir"`
	// Forbid lists extensions never served, so that the source of index.php
	// doesn't leak; requests for them are passed on.
	Forbid []string `yaml:"forbid"`
	// Headers are added to every file served, such as Cache-Control.
	Headers map[string]string `yaml:"headers,omitempty"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{Forbid: []string{".php"}}
}

// Validate reports a missing dir, invalid extensions and invalid headers.
func (c *Config) Validate() error {
	var errs []error
	if c.Dir == "" {
		errs = append(errs, &config.FieldError{Field: "dir", Msg: "required"})
	}
	for i, ext := range c.Forbid {
		if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext[1:], "./\\\x00") {
			errs = append(errs, &config.FieldError{Field: fmt.Sprintf("forbid[%d]", i),
				Msg: fmt.Sprintf("invalid extension %q: want a dot and a name, like .php", ext)})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Headers)) {
		switch {
		case !httpx.IsToken(name):
			errs = append(errs, &config.FieldError{Field: "headers." + name, Msg: fmt.Sprintf("invalid header name %q", name)})
		case !httpx.IsHeaderValue(c.Headers[name]):
			errs = append(errs, &config.FieldError{Field: "headers." + name, Msg: "the value has control characters"})
		}
	}
	return errors.Join(errs...)
}

// New returns the filter for a validated config; configDir resolves a
// relative dir. A dir that isn't a directory is a *config.FieldError, so
// that kest validate reports it.
func New(cfg *Config, configDir string) (httpx.Filter, error) {
	dir := cfg.Dir
	if configDir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(configDir, dir)
	}
	fi, err := os.Stat(dir)
	if err == nil && !fi.IsDir() {
		err = fmt.Errorf("%s is not a directory", dir)
	}
	if err != nil {
		return nil, &config.FieldError{Field: "dir", Msg: err.Error()}
	}
	f := newFiles(dir, cfg.Forbid, cfg.Headers)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !f.serve(w, r) {
				next.ServeHTTP(w, r)
			}
		})
	}, nil
}
