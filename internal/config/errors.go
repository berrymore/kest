package config

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Error is a problem in a configuration file, located by YAML path and, when
// known, by line and column.
type Error struct {
	File   string
	Line   int    // 1-based; 0 if unknown
	Column int    // 1-based; 0 if unknown
	Path   string // e.g. listeners[0].http.virtual_hosts[1].routes[2].route.cluster
	Msg    string
}

// Error formats the error as "file:line:col: path: message".
func (e *Error) Error() string {
	var b strings.Builder
	if e.File != "" {
		b.WriteString(e.File)
		b.WriteByte(':')
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, "%d:%d:", e.Line, e.Column)
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	if e.Path != "" {
		b.WriteString(e.Path)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Errors is a list of configuration errors that are reported together.
type Errors []*Error

// Error formats one error per line.
func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Append adds err to the list. Errors and *Error values are flattened;
// any other error becomes an Error without a location.
func (es *Errors) Append(err error) {
	if err == nil {
		return
	}
	var list Errors
	var one *Error
	switch {
	case errors.As(err, &list):
		*es = append(*es, list...)
	case errors.As(err, &one):
		*es = append(*es, one)
	default:
		*es = append(*es, &Error{Msg: err.Error()})
	}
}

// Err returns the list sorted by location, or nil if it is empty.
func (es Errors) Err() error {
	if len(es) == 0 {
		return nil
	}
	sorted := slices.Clone(es)
	slices.SortStableFunc(sorted, compareErrors)
	return sorted
}

// compareErrors orders located errors by position, then unlocated ones by path.
func compareErrors(a, b *Error) int {
	if (a.Line == 0) != (b.Line == 0) {
		if a.Line == 0 {
			return 1
		}
		return -1
	}
	return cmp.Or(
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Column, b.Column),
		cmp.Compare(a.Path, b.Path),
	)
}

// FieldError is a validation error for one field of an extension config,
// relative to the extension's config block. Extension configs return them
// from their Validate method; several can be combined with errors.Join.
type FieldError struct {
	Field string // e.g. "format" or "rules[2].pattern"
	Msg   string
}

// Error formats the error as "field: message".
func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}
