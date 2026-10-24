package config

import (
	"fmt"

	"go.yaml.in/yaml/v4"
)

// Extension selects a compiled-in extension by type and carries its config:
//
//	type: access_log
//	config:
//	  format: json
//
// The config block stays raw until the registry decodes it into the
// extension's own config struct (see DecodeConfig), so it is validated as
// strictly as the rest of the file.
type Extension struct {
	Type   string    `yaml:"type"`
	Config RawConfig `yaml:"config,omitempty"`

	src  *source
	path string
}

// Path returns the YAML path of the extension, such as "clusters[0]".
func (e *Extension) Path() string {
	return e.path
}

// HasConfig reports whether a non-null config block was given.
func (e *Extension) HasConfig() bool {
	return e.Config.node != nil
}

// DecodeConfig strictly decodes the config block into out, which should
// point to a struct pre-filled with defaults. Without a config block, out is
// left unchanged.
func (e *Extension) DecodeConfig(out any) error {
	if e.Config.node == nil {
		return nil
	}
	if err := e.Config.node.Load(out, yaml.WithKnownFields()); err != nil {
		return e.source().convert(err).Err()
	}
	return nil
}

// Errorf returns a located error for field, relative to the extension:
// "type", "config", "config.format". An empty field means the extension itself.
func (e *Extension) Errorf(field, format string, args ...any) error {
	path := e.path
	if field != "" {
		path = joinPath(path, field)
	}
	return e.source().errorAt(path, fmt.Sprintf(format, args...))
}

// SetDecoded records the decoded config so that Dump shows effective values.
func (e *Extension) SetDecoded(v any) {
	e.Config.decoded = v
}

func (e *Extension) bind(src *source, path string) {
	e.src, e.path = src, path
}

func (e *Extension) source() *source {
	if e.src == nil {
		return newSource("")
	}
	return e.src
}

// RawConfig holds an extension's config block until the extension decodes it.
type RawConfig struct {
	node    *yaml.Node
	decoded any
}

// UnmarshalYAML implements yaml.Unmarshaler. A null block counts as absent.
func (r *RawConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	r.node = n
	return nil
}

// MarshalYAML implements yaml.Marshaler: the decoded config if known, else the raw block.
func (r RawConfig) MarshalYAML() (any, error) {
	if r.decoded != nil {
		return r.decoded, nil
	}
	return r.node, nil
}

// IsZero reports whether there is no config block; it makes omitempty work.
func (r RawConfig) IsZero() bool {
	return r.node == nil && r.decoded == nil
}
