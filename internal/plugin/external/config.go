package external

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/berrymore/kest/internal/config"
)

// Defaults.
const (
	DefaultMaxConcurrentCalls = 64
	DefaultMaxFrameSize       = config.ByteSize(16 << 20)
	DefaultStartTimeout       = 10 * time.Second
	DefaultStopTimeout        = 10 * time.Second
)

// Bounds of max_frame_size, as for rpc.max_frame_size.
const (
	minFrameSize = config.ByteSize(1 << 10)
	maxFrameSize = config.ByteSize(1 << 30)
)

// Config is the external plugin type's config.
type Config struct {
	// Command runs the plugin: the executable and its arguments. A relative
	// executable is relative to Dir.
	Command []string `yaml:"command"`
	// Dir is the plugin's working directory, relative to the config file;
	// empty means the config file's directory.
	Dir string `yaml:"dir,omitempty"`
	// Env is added to the plugin's environment.
	Env map[string]string `yaml:"env,omitempty"`
	// InheritEnv passes kest's own environment to the plugin.
	InheritEnv bool `yaml:"inherit_env"`
	// PluginConfig is the plugin's own config, handed to it as JSON.
	PluginConfig any `yaml:"plugin_config,omitempty"`
	// MaxConcurrentCalls bounds the calls in progress; more wait for a place
	// within their timeout.
	MaxConcurrentCalls int `yaml:"max_concurrent_calls"`
	// MaxFrameSize bounds a call or an answer on the plugin's socket; keep it
	// at least rpc.max_frame_size.
	MaxFrameSize config.ByteSize `yaml:"max_frame_size"`
	// StartTimeout bounds the start, the plugin's own setup included.
	StartTimeout config.Duration `yaml:"start_timeout"`
	// StopTimeout bounds the stop; then the plugin is killed.
	StopTimeout config.Duration `yaml:"stop_timeout"`
}

func defaultConfig() *Config {
	return &Config{
		InheritEnv:         true,
		MaxConcurrentCalls: DefaultMaxConcurrentCalls,
		MaxFrameSize:       DefaultMaxFrameSize,
		StartTimeout:       config.Duration(DefaultStartTimeout),
		StopTimeout:        config.Duration(DefaultStopTimeout),
	}
}

// Validate reports every invalid field as a *config.FieldError.
func (c *Config) Validate() error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, &config.FieldError{Field: field, Msg: fmt.Sprintf(format, args...)})
	}
	if len(c.Command) == 0 || c.Command[0] == "" {
		add("command", "required: the plugin's executable and its arguments")
	}
	for i, arg := range c.Command {
		if strings.IndexByte(arg, 0) >= 0 {
			add(fmt.Sprintf("command[%d]", i), "must not contain NUL bytes")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		switch {
		case k == "" || strings.ContainsAny(k, "=\x00"):
			add("env", "invalid variable name %q", k)
		case strings.IndexByte(c.Env[k], 0) >= 0:
			add("env."+k, "must not contain NUL bytes")
		}
	}
	if _, err := c.pluginConfigJSON(); err != nil {
		add("plugin_config", "%v", err)
	}
	if c.MaxConcurrentCalls <= 0 {
		add("max_concurrent_calls", "must be positive, got %d", c.MaxConcurrentCalls)
	}
	if c.MaxFrameSize < minFrameSize || c.MaxFrameSize > maxFrameSize {
		add("max_frame_size", "must be between %s and %s, got %s", minFrameSize, maxFrameSize, c.MaxFrameSize)
	}
	if c.StartTimeout <= 0 {
		add("start_timeout", "must be positive")
	}
	if c.StopTimeout <= 0 {
		add("stop_timeout", "must be positive")
	}
	return errors.Join(errs...)
}

// Redacted hides what may be secret: env values and plugin_config.
func (c *Config) Redacted() any {
	out := *c
	if len(c.Env) > 0 {
		out.Env = make(map[string]string, len(c.Env))
		for k := range c.Env {
			out.Env[k] = config.Redacted
		}
	}
	if c.PluginConfig != nil {
		out.PluginConfig = config.Redacted
	}
	return &out
}

// pluginConfigJSON returns plugin_config as JSON, or nil without one.
func (c *Config) pluginConfigJSON() (json.RawMessage, error) {
	if c.PluginConfig == nil {
		return nil, nil
	}
	v, err := jsonable(c.PluginConfig)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// jsonable turns what YAML decodes into something JSON encodes: mappings
// with keys that aren't strings are refused, since JSON has none.
func jsonable(v any) (any, error) {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[k] = j
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			s, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("key %v is not a string: the plugin gets its config as JSON", k)
			}
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[s] = j
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[i] = j
		}
		return out, nil
	}
	return v, nil
}
