// Package registry holds compiled-in extensions, grouped by extension point
// (ADR-0004).
//
// Each extension point owns a Category, and extensions register a Factory
// in it from an init function. When the server graph is built, Build looks
// up the factory named by a config block's type, decodes the block strictly
// into the factory's config struct and builds the component.
package registry

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/metrics"
)

// Factory creates components of one extension type.
type Factory[T any] struct {
	// Type is the name used in configuration: {type: <Type>, config: {...}}.
	Type string
	// NewConfig returns a pointer to a config struct pre-filled with
	// defaults; the config block is decoded on top of it. If the config
	// implements Validate() error, it is called after decoding; return
	// *config.FieldError values (possibly joined) to point at fields.
	// A nil NewConfig means the extension takes no config.
	NewConfig func() any
	// Build creates the component from the decoded config. It must not
	// start goroutines or do I/O: that happens when the component starts.
	Build func(bc BuildContext, cfg any) (T, error)
}

// BuildContext carries what factories need, without globals.
type BuildContext struct {
	// Name is the component's configured name, for named components such as clusters.
	Name   string
	Logger *slog.Logger
	// ConfigDir is the absolute directory of the config file. Extensions
	// resolve relative paths against it. Empty when there is no file.
	ConfigDir string
	// Metrics is the server's metrics registry; use MetricsRegistry.
	Metrics *metrics.Registry
	// RPCAddress is where workers reach the RPC server, as RR_RPC carries it
	// ("unix:///path/to/rpc.sock"); empty when rpc is off.
	RPCAddress string
}

// MetricsRegistry returns the registry components register their metrics
// in: bc.Metrics, or a new one nobody reads when it is nil (in tests).
func (bc BuildContext) MetricsRegistry() *metrics.Registry {
	if bc.Metrics != nil {
		return bc.Metrics
	}
	return metrics.NewRegistry()
}

// Category is the set of factories for one extension point.
type Category[T any] struct {
	name string

	mu        sync.RWMutex
	factories map[string]Factory[T]
}

// NewCategory creates an empty category; name is used in error messages.
func NewCategory[T any](name string) *Category[T] {
	return &Category[T]{name: name, factories: make(map[string]Factory[T])}
}

// Name returns the category name.
func (c *Category[T]) Name() string {
	return c.name
}

// Register adds a factory. It panics on an empty or duplicate type or a
// missing Build function: these are programming errors caught at init.
func (c *Category[T]) Register(f Factory[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case f.Type == "":
		panic(fmt.Sprintf("registry: %s factory with an empty type", c.name))
	case f.Build == nil:
		panic(fmt.Sprintf("registry: %s factory %q has no Build function", c.name, f.Type))
	}
	if _, dup := c.factories[f.Type]; dup {
		panic(fmt.Sprintf("registry: duplicate %s type %q", c.name, f.Type))
	}
	c.factories[f.Type] = f
}

// Lookup returns the factory registered for typ.
func (c *Category[T]) Lookup(typ string) (Factory[T], bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.factories[typ]
	return f, ok
}

// Types returns the registered type names, sorted.
func (c *Category[T]) Types() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	types := make([]string, 0, len(c.factories))
	for t := range c.factories {
		types = append(types, t)
	}
	slices.Sort(types)
	return types
}

// Build creates the component that ext describes. Configuration problems are
// returned as located config errors.
func Build[T any](c *Category[T], ext *config.Extension, bc BuildContext) (T, error) {
	var zero T

	f, ok := c.Lookup(ext.Type)
	if !ok {
		known := "none are registered"
		if types := c.Types(); len(types) > 0 {
			known = "known types: " + strings.Join(types, ", ")
		}
		return zero, ext.Errorf("type", "unknown %s type %q (%s)", c.name, ext.Type, known)
	}

	var cfg any
	switch {
	case f.NewConfig != nil:
		cfg = f.NewConfig()
		if err := ext.DecodeConfig(cfg); err != nil {
			return zero, err
		}
		if v, ok := cfg.(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return zero, fieldErrors(ext, err)
			}
		}
		ext.SetDecoded(cfg)
	case ext.HasConfig():
		return zero, ext.Errorf("config", "%s type %q takes no config", c.name, ext.Type)
	}

	component, err := f.Build(bc, cfg)
	if fe, ok := errors.AsType[*config.FieldError](err); ok {
		// A problem Build found in a field, such as a file it couldn't read.
		return zero, ext.Errorf(joinField("config", fe.Field), "%s", fe.Msg)
	}
	if err != nil {
		return zero, ext.Errorf("", "%v", err)
	}
	return component, nil
}

// fieldErrors locates the errors returned by an extension config's Validate.
func fieldErrors(ext *config.Extension, err error) error {
	var errs []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	} else {
		errs = []error{err}
	}

	var out config.Errors
	for _, e := range errs {
		if fe, ok := errors.AsType[*config.FieldError](e); ok {
			out.Append(ext.Errorf(joinField("config", fe.Field), "%s", fe.Msg))
		} else {
			out.Append(ext.Errorf("config", "%v", e))
		}
	}
	return out.Err()
}

func joinField(parent, field string) string {
	if field == "" {
		return parent
	}
	if strings.HasPrefix(field, "[") {
		return parent + field
	}
	return parent + "." + field
}
