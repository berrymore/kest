package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Load reads and validates the configuration file at path, expanding
// ${ENV} references from the process environment.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator chooses the config file
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, path, os.LookupEnv)
}

// Parse decodes and validates configuration from data. file names the source
// in error messages; lookupEnv resolves ${ENV} references.
//
// Validation errors are returned as Errors, sorted by location.
func Parse(data []byte, file string, lookupEnv func(string) (string, bool)) (*Config, error) {
	src := newSource(file)

	var doc yaml.Node
	if err := yaml.Load(data, &doc); err != nil {
		if isNoDocument(err) {
			return nil, Errors{{File: file, Msg: "configuration is empty"}}
		}
		return nil, src.convert(err).Err()
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, Errors{{File: file, Line: doc.Line, Column: doc.Column, Msg: "configuration must be a YAML mapping"}}
	}

	var errs Errors
	walk(&doc, "", src, lookupEnv, &errs)
	if len(errs) > 0 {
		return nil, errs.Err()
	}

	cfg := &Config{src: src}
	if err := doc.Load(cfg, yaml.WithKnownFields()); err != nil {
		return nil, src.convert(err).Err()
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// walk indexes node positions by YAML path and expands ${ENV} references in
// scalar values. Mapping keys are never expanded.
func walk(n *yaml.Node, path string, src *source, lookupEnv func(string) (string, bool), errs *Errors) {
	src.record(n, path)
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, path, src, lookupEnv, errs)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := joinPath(path, k.Value)
			src.recordKey(k, child)
			walk(v, child, src, lookupEnv, errs)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walk(c, indexPath(path, i), src, lookupEnv, errs)
		}
	case yaml.ScalarNode:
		expanded, err := expandScalar(n, lookupEnv)
		if err != nil {
			*errs = append(*errs, src.errorAt(path, err.Error()))
		}
		if expanded && n.Style&quotedStyles != 0 {
			src.quotedRefs[position{n.Line, n.Column}] = true
		}
	default:
		// Alias nodes: the anchored node is walked where it is defined.
	}
}

const quotedStyles = yaml.SingleQuotedStyle | yaml.DoubleQuotedStyle | yaml.LiteralStyle | yaml.FoldedStyle

// expandScalar expands ${ENV} references in n and reports whether it changed.
// A plain scalar is re-typed after expansion, so ${PORT} becomes an int.
// A quoted one stays a string, as YAML says.
func expandScalar(n *yaml.Node, lookupEnv func(string) (string, bool)) (bool, error) {
	v, err := expandEnv(n.Value, lookupEnv)
	if err != nil || v == n.Value {
		return false, err
	}
	n.Value = v
	if n.Style&quotedStyles == 0 {
		n.Tag = ""
	}
	return true, nil
}

func isNoDocument(err error) bool {
	var many *yaml.LoadErrors
	if !errors.As(err, &many) || len(many.Errors) != 1 {
		return false
	}
	return strings.Contains(many.Errors[0].Message, "no documents in stream")
}
