package rrhttp

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Limits on forms, the same as Go's url.ParseQuery and multipart.Reader.ReadForm.
const (
	maxFormFields = 10000 // fields of a urlencoded body
	maxParts      = 1000  // parts of a multipart body
	maxTreeDepth  = 127   // deeper keys are ignored, as in RoadRunner
)

var errTooManyFields = errors.New("too many form fields")

// form is a list of fields in order of first appearance, each with all its
// values in order.
type form struct {
	keys   []string
	values map[string][]string
}

func newForm() *form {
	return &form{values: make(map[string][]string)}
}

func (f *form) add(key, value string) {
	if _, ok := f.values[key]; !ok {
		f.keys = append(f.keys, key)
	}
	f.values[key] = append(f.values[key], value)
}

// parseURLEncoded parses an application/x-www-form-urlencoded body like
// url.ParseQuery, but keeps the order of the fields. It returns the first
// error, as url.ParseQuery does.
func parseURLEncoded(body string) (*form, error) {
	if strings.Count(body, "&")+1 > maxFormFields {
		return nil, errTooManyFields
	}
	f := newForm()
	var err error
	for body != "" {
		var pair string
		pair, body, _ = strings.Cut(body, "&")
		if strings.Contains(pair, ";") {
			if err == nil {
				err = errors.New("invalid semicolon separator")
			}
			continue
		}
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		k, err1 := url.QueryUnescape(k)
		if err1 == nil {
			v, err1 = url.QueryUnescape(v)
		}
		if err1 != nil {
			if err == nil {
				err = err1
			}
			continue
		}
		f.add(k, v)
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// tree mounts the fields into a tree by their PHP-style keys:
// "a[b][]" is a list under "b" under "a".
func (f *form) tree() (tree, error) {
	t := make(tree, len(f.keys))
	for _, k := range f.keys {
		if err := mount(t, splitKey(k), f.values[k]); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// A tree node is a tree (a branch), a string (a field's last value) or a
// []string (all values of a "name[]" field). It marshals to the JSON that
// PHP's json_decode turns into $_POST-like arrays.
type tree map[string]any

// splitKey splits a PHP-style field name into its segments, exactly as
// RoadRunner does: "a[b][]" → ["a", "b", ""]; spaces are dropped, and
// invalid UTF-8 becomes U+FFFD.
func splitKey(s string) []string {
	const (
		normal = iota // inside a segment, outside brackets
		open          // after '['
		closed        // after ']'
	)
	keys := make([]string, 0, 4)
	var b strings.Builder
	state := normal
	for _, c := range s {
		switch c {
		case ' ':
		case '[':
			if state == normal {
				keys = append(keys, b.String())
				b.Reset()
			}
			state = open
		case ']':
			keys = append(keys, b.String())
			b.Reset()
			state = closed
		default:
			b.WriteRune(c)
			state = normal
		}
	}
	if b.Len() > 0 || len(keys) == 0 {
		keys = append(keys, b.String())
	}
	return keys
}

// mount stores values at keys in t, creating branches on the way. It follows
// RoadRunner's rules (http/v6 handler/parse.go): a scalar key takes the last
// value, "name[]" takes all of them, an empty value never replaces data, and
// data under a key that already holds other data is an error.
func mount(t tree, keys, values []string) error {
	if len(keys) > maxTreeDepth {
		return nil
	}
	for len(keys) > 0 {
		done, err := prepareNode(t, keys, values)
		if err != nil || done {
			return err
		}
		switch {
		case len(keys) == 2 && keys[1] == "":
			t[keys[0]] = values
			return nil
		case len(keys) == 1:
			if len(values) > 0 {
				t[keys[0]] = values[len(values)-1]
			} else {
				t[keys[0]] = values
			}
			return nil
		}
		next, ok := t[keys[0]].(tree)
		if !ok {
			next = make(tree, 1)
			t[keys[0]] = next
		}
		t, keys = next, keys[1:]
	}
	return nil
}

// prepareNode resolves what is already stored at keys[0] before values go
// there or below. done means values are dropped, without an error.
func prepareNode(t tree, keys, values []string) (done bool, err error) {
	existing, exists := t[keys[0]]
	if !exists {
		t[keys[0]] = make(tree)
		return false, nil
	}
	_, isBranch := existing.(tree)
	existingEmpty := isEmpty(existing)
	incomingEmpty := isEmpty(values)
	isLeaf := len(keys) == 1 || (len(keys) == 2 && keys[1] == "")

	switch {
	case !isBranch && existingEmpty && !incomingEmpty:
		t[keys[0]] = make(tree) // an empty leaf gives way to data
		return false, nil
	case !isBranch && !existingEmpty && len(keys) > 1 && keys[1] != "":
		return true, conflict(keys[0])
	case !isBranch && !existingEmpty && incomingEmpty:
		return true, nil
	case isBranch && isLeaf && !incomingEmpty:
		return true, conflict(keys[0])
	case isBranch && isLeaf && incomingEmpty:
		return true, nil
	default:
		return false, nil
	}
}

func isEmpty(v any) bool {
	switch v := v.(type) {
	case string:
		return v == ""
	case []string:
		return len(v) == 0 || (len(v) == 1 && v[0] == "")
	default:
		return v == nil
	}
}

func conflict(key string) error {
	return fmt.Errorf("invalid multiple values to key %q in tree", key)
}
