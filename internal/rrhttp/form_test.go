package rrhttp

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestSplitKey(t *testing.T) {
	tests := map[string][]string{
		"a":          {"a"},
		"a[]":        {"a", ""},
		"a[b]":       {"a", "b"},
		"a[b][]":     {"a", "b", ""},
		"a[b][c][d]": {"a", "b", "c", "d"},
		"a[][b]":     {"a", "", "b"},
		"first name": {"firstname"},
		"a[ b ]":     {"a", "b"},
		"":           {""},
		"[a]":        {"", "a"},
		"a]b":        {"a", "b"},
		"a[b":        {"a", "b"},
		"a[[b]]":     {"a", "b", ""},
		"a[b]c":      {"a", "b", "c"},
		"é[ü]":       {"é", "ü"},
	}
	for in, want := range tests {
		if got := splitKey(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormTree(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // JSON, or "error: ..."
	}{
		{"scalar takes the last value", "a=1&a=2", `{"a":"2"}`},
		{"list", "a[]=1&a[]=2", `{"a":["1","2"]}`},
		{"nested", "u[name]=x&u[tags][]=a&u[tags][]=b", `{"u":{"name":"x","tags":["a","b"]}}`},
		{"deep", "a[b][c][d]=1", `{"a":{"b":{"c":{"d":"1"}}}}`},
		{"empty value", "a=", `{"a":""}`},
		{"empty list value", "a[]=", `{"a":[""]}`},
		{"empty leaf gives way to a branch", "a=&a[b]=1", `{"a":{"b":"1"}}`},
		{"empty value doesn't replace a branch", "a[b]=1&a=", `{"a":{"b":"1"}}`},
		{"empty list doesn't replace a scalar", "a=1&a[]=", `{"a":"1"}`},
		{"the last value may be empty", "a=1&a=", `{"a":""}`},
		{"even an empty branch conflicts with a scalar", "a=1&a[b]=", "error: invalid multiple values to key \"a\" in tree"},
		{"list replaces a scalar", "a=1&a[]=2", `{"a":["2"]}`},
		{"index in brackets", "a[0]=x&a[1]=y", `{"a":{"0":"x","1":"y"}}`},
		{"list inside a list", "a[][b]=1", `{"a":{"":{"b":"1"}}}`},
		{"scalar then branch", "a=1&a[b]=2", "error: invalid multiple values to key \"a\" in tree"},
		{"branch then scalar", "a[b]=2&a=1", "error: invalid multiple values to key \"a\" in tree"},
		{"no fields", "", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseURLEncoded(tt.body)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := f.tree()
			var got string
			if err != nil {
				got = "error: " + err.Error()
			} else {
				b, _ := json.Marshal(tr)
				got = string(b)
			}
			if got != tt.want {
				t.Errorf("tree(%q) = %s, want %s", tt.body, got, tt.want)
			}
		})
	}
}

func TestFormTreeTooDeep(t *testing.T) {
	key := "a" + strings.Repeat("[x]", maxTreeDepth)
	f := newForm()
	f.add(key, "dropped")
	f.add("b", "kept")
	tr, err := f.tree()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(tr); string(b) != `{"b":"kept"}` {
		t.Errorf("tree = %s", b)
	}
}

func TestFormKeepsOrder(t *testing.T) {
	f, err := parseURLEncoded("b=1&a=2&b=3&c=")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b", "a", "c"}; !reflect.DeepEqual(f.keys, want) {
		t.Errorf("keys = %q, want %q", f.keys, want)
	}
	if want := []string{"1", "3"}; !reflect.DeepEqual(f.values["b"], want) {
		t.Errorf("b = %q, want %q", f.values["b"], want)
	}
}

// FuzzParseURLEncoded checks that parseURLEncoded agrees with url.ParseQuery.
func FuzzParseURLEncoded(f *testing.F) {
	for _, s := range []string{"a=1&b=2", "a[]=1&a[]=2", "a=%zz", "a;b", "&&a=&=b&", "a+b=c%20d", "%"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got, err := parseURLEncoded(body)
		want, wantErr := url.ParseQuery(body)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("parseURLEncoded(%q) err = %v, url.ParseQuery err = %v", body, err, wantErr)
		}
		if err != nil {
			return
		}
		if len(got.keys) != len(want) || len(got.values) != len(want) {
			t.Fatalf("parseURLEncoded(%q): %d keys, url.ParseQuery: %d", body, len(got.keys), len(want))
		}
		for k, v := range want {
			if !reflect.DeepEqual(got.values[k], v) {
				t.Fatalf("parseURLEncoded(%q)[%q] = %q, want %q", body, k, got.values[k], v)
			}
		}
	})
}

// FuzzFormTree checks that any form mounts into a tree that marshals to
// JSON, or fails with an error, without panicking.
func FuzzFormTree(f *testing.F) {
	for _, s := range []string{"a[b][]=1&a[b][]=2", "a=&a[b]=1", "a[b]=1&a=", "[]=1&[[=2&]]=3", "a[][][]=x&a[]="} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		fm, err := parseURLEncoded(body)
		if err != nil {
			return
		}
		tr, err := fm.tree()
		if err != nil {
			return
		}
		if _, err := json.Marshal(tr); err != nil {
			t.Fatalf("tree of %q doesn't marshal: %v", body, err)
		}
	})
}
