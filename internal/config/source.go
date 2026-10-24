package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
)

type position struct {
	line, column int
}

// source remembers where each YAML path is located in the file, so that
// errors found after decoding can still point at a line.
type source struct {
	file       string
	byPath     map[string]position // path -> position of its value
	byPos      map[position]string // position of a key or value -> path
	quotedRefs map[position]bool   // quoted scalars that contained ${ENV} references
}

func newSource(file string) *source {
	return &source{
		file:       file,
		byPath:     make(map[string]position),
		byPos:      make(map[position]string),
		quotedRefs: make(map[position]bool),
	}
}

// record registers the value node n at path.
func (s *source) record(n *yaml.Node, path string) {
	p := position{n.Line, n.Column}
	if _, ok := s.byPath[path]; !ok {
		s.byPath[path] = p
	}
	s.byPos[p] = path
}

// recordKey registers a mapping key node. Keys take priority over the
// containers that start at the same position, so that "unknown field"
// errors name the field.
func (s *source) recordKey(k *yaml.Node, path string) {
	s.byPos[position{k.Line, k.Column}] = path
}

// errorAt builds an error for path, located at the closest known ancestor.
func (s *source) errorAt(path, msg string) *Error {
	e := &Error{File: s.file, Path: path, Msg: msg}
	for p := path; ; p = parentPath(p) {
		if pos, ok := s.byPath[p]; ok {
			e.Line, e.Column = pos.line, pos.column
			break
		}
		if p == "" {
			break
		}
	}
	return e
}

var unknownFieldRE = regexp.MustCompile(`^field (\S+) not found in type `)

// convert turns an error from the YAML library into located config errors.
func (s *source) convert(err error) Errors {
	var many *yaml.LoadErrors
	var one *yaml.LoadError
	switch {
	case errors.As(err, &many):
		out := make(Errors, 0, len(many.Errors))
		for _, le := range many.Errors {
			out = append(out, s.fromLoadError(le))
		}
		return out
	case errors.As(err, &one):
		return Errors{s.fromLoadError(one)}
	default:
		return Errors{{File: s.file, Msg: err.Error()}}
	}
}

func (s *source) fromLoadError(le *yaml.LoadError) *Error {
	e := &Error{
		File:   s.file,
		Line:   le.Mark.Line,
		Column: le.Mark.Column,
		Msg:    strings.TrimPrefix(le.Message, "yaml: "),
	}
	if e.Line > 0 {
		e.Path = s.byPos[position{e.Line, e.Column}]
	}
	if m := unknownFieldRE.FindStringSubmatch(e.Msg); m != nil {
		if e.Path != "" {
			e.Msg = "unknown field"
		} else {
			e.Msg = fmt.Sprintf("unknown field %q", m[1])
		}
	}
	if s.quotedRefs[position{e.Line, e.Column}] && strings.HasPrefix(e.Msg, "cannot construct !!str") {
		e.Msg += " (a quoted ${...} reference stays a string; leave it unquoted, in block style, to get a number or a boolean)"
	}
	return e
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func indexPath(parent string, i int) string {
	return fmt.Sprintf("%s[%d]", parent, i)
}

func parentPath(p string) string {
	i := strings.LastIndexAny(p, ".[")
	if i < 0 {
		return ""
	}
	return p[:i]
}
