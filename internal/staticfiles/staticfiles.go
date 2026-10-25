// Package staticfiles serves files from a directory: the static option of
// php_workers and the static_files cluster type use it.
package staticfiles

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
)

// Options say what to serve.
type Options struct {
	// Dir is the directory served, absolute.
	Dir string
	// Index is served for a directory's path ("/docs/"); "" serves no
	// directories.
	Index string
	// Forbid lists extensions (".php") that are never served.
	Forbid []string
	// Headers are added to every file served.
	Headers map[string]string
}

// ValidateFields checks forbid and headers fields of a config whose field
// names start with prefix ("static." or "").
func ValidateFields(prefix string, forbid []string, headers map[string]string) error {
	var errs []error
	for i, ext := range forbid {
		if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext[1:], "./\\\x00") {
			errs = append(errs, &config.FieldError{Field: fmt.Sprintf("%sforbid[%d]", prefix, i),
				Msg: fmt.Sprintf("invalid extension %q: want a dot and a name, like .php", ext)})
		}
	}
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		switch {
		case !httpx.IsToken(name):
			errs = append(errs, &config.FieldError{Field: prefix + "headers." + name, Msg: fmt.Sprintf("invalid header name %q", name)})
		case !httpx.IsHeaderValue(headers[name]):
			errs = append(errs, &config.FieldError{Field: prefix + "headers." + name, Msg: "the value has control characters"})
		}
	}
	return errors.Join(errs...)
}

// Files serves the files of a directory. A URL path with a segment that
// starts with a dot is never served, so ".." can't lead out of it. Symbolic
// links are followed, as nginx does: they are the operator's, such as
// Laravel's public/storage.
type Files struct {
	opts    Options
	forbid  []string // lower case
	headers http.Header
}

// New returns Files for opts; nothing is opened yet.
func New(opts Options) *Files {
	f := &Files{opts: opts, headers: http.Header{}}
	for _, ext := range opts.Forbid {
		f.forbid = append(f.forbid, strings.ToLower(ext))
	}
	for name, value := range opts.Headers {
		f.headers.Set(name, value)
	}
	return f
}

// Check reports a directory that doesn't exist or isn't one; components
// call it when they start.
func (f *Files) Check() error {
	fi, err := os.Stat(f.opts.Dir)
	if err == nil && !fi.IsDir() {
		err = fmt.Errorf("%s is not a directory", f.opts.Dir)
	}
	if err != nil {
		return fmt.Errorf("static files: %w", err)
	}
	return nil
}

// Serve answers a GET or HEAD request for a file in the directory and
// reports whether it did. It doesn't answer for other methods, missing
// files, forbidden extensions, hidden paths (a segment starting with a dot,
// except /.well-known/) or directories without an index.
func (f *Files) Serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	name, ok := f.name(r.URL.Path)
	if !ok {
		return false
	}
	// name has no empty, "." or ".." segment: it stays in Dir.
	file, err := os.Open(filepath.Join(f.opts.Dir, filepath.FromSlash(name))) //nolint:gosec // see above
	if err != nil {
		return false
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		return false
	}
	if fi.IsDir() {
		if f.opts.Index == "" || strings.HasSuffix(r.URL.Path, "/") {
			return false
		}
		// As http.FileServer does: a directory's URL ends with a slash, so
		// that relative links in its index resolve. The path has no empty
		// segment, so the target can't start with "//" (another host).
		target := r.URL.EscapedPath() + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // see above
		return true
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	h := w.Header()
	maps.Copy(h, f.headers)
	h.Set("Etag", `W/"`+strconv.FormatInt(fi.Size(), 16)+"-"+strconv.FormatInt(fi.ModTime().UnixNano(), 16)+`"`)
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), file)
	return true
}

// name maps a URL path to a name in the directory, or reports that it must
// not be served.
func (f *Files) name(urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, "/")
	if strings.HasSuffix(urlPath, "/") || name == "" {
		if f.opts.Index == "" {
			return "", false
		}
		name += f.opts.Index
	}
	if strings.IndexByte(name, 0) >= 0 {
		return "", false
	}
	for i, segment := range strings.Split(name, "/") {
		if segment == "" || (strings.HasPrefix(segment, ".") && (i > 0 || segment != ".well-known")) {
			return "", false
		}
	}
	if slices.Contains(f.forbid, strings.ToLower(path.Ext(name))) {
		return "", false
	}
	return name, true
}
