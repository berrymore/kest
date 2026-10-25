// Package static implements the static_files cluster type: it serves the
// files of a directory, for sites and assets that need no application.
package static

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/berrymore/kest/internal/cluster"
	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/registry"
	"github.com/berrymore/kest/internal/staticfiles"
)

// Type is the cluster type name in configuration.
const Type = "static_files"

func init() {
	cluster.Types.Register(registry.Factory[cluster.Cluster]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(bc registry.BuildContext, cfg any) (cluster.Cluster, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("static_files: unexpected config type")
			}
			return New(c, bc.ConfigDir), nil
		},
	})
}

// Config is the config block of a static_files cluster.
type Config struct {
	// Dir is served, relative to the config file.
	Dir string `yaml:"dir"`
	// Index is served for a directory's path; "" serves no directories.
	Index string `yaml:"index"`
	// Forbid lists extensions never served.
	Forbid []string `yaml:"forbid,omitempty"`
	// Headers are added to every file served, such as Cache-Control.
	Headers map[string]string `yaml:"headers,omitempty"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{Index: "index.html"}
}

// Validate reports a missing dir and invalid fields.
func (c *Config) Validate() error {
	var errs []error
	if c.Dir == "" {
		errs = append(errs, &config.FieldError{Field: "dir", Msg: "required"})
	}
	if c.Index != "" && (c.Index[0] == '.' || strings.ContainsAny(c.Index, "/\\\x00")) {
		errs = append(errs, &config.FieldError{Field: "index", Msg: "must be a file name, like index.html"})
	}
	if err := staticfiles.ValidateFields("", c.Forbid, c.Headers); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Cluster serves a directory.
type Cluster struct {
	files *staticfiles.Files
}

// New builds a cluster from a validated config; configDir resolves a
// relative dir.
func New(cfg *Config, configDir string) *Cluster {
	dir := cfg.Dir
	if configDir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(configDir, dir)
	}
	return &Cluster{files: staticfiles.New(staticfiles.Options{Dir: dir, Index: cfg.Index, Forbid: cfg.Forbid,
		Headers: cfg.Headers})}
}

// Start checks that the directory exists.
func (c *Cluster) Start(context.Context) error {
	return c.files.Check()
}

// Stop does nothing: requests in flight finish on their own.
func (c *Cluster) Stop(context.Context) error {
	return nil
}

// ServeHTTP serves the file for the request's path: 404 if there is none,
// 405 for methods other than GET and HEAD.
func (c *Cluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if !c.files.Serve(w, r) {
		http.NotFound(w, r)
	}
}
