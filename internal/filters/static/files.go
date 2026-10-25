package static

import (
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// files serves the files of a directory. A URL path with a segment that
// starts with a dot is never served, so ".." can't lead out of it. Symbolic
// links are followed, as nginx does: they are the operator's, such as
// Laravel's public/storage.
type files struct {
	dir     string   // absolute
	forbid  []string // extensions never served, lower case
	headers http.Header
}

func newFiles(dir string, forbid []string, headers map[string]string) *files {
	f := &files{dir: dir, headers: http.Header{}}
	for _, ext := range forbid {
		f.forbid = append(f.forbid, strings.ToLower(ext))
	}
	for name, value := range headers {
		f.headers.Set(name, value)
	}
	return f
}

// serve answers a GET or HEAD request for a regular file in the directory
// and reports whether it did. It doesn't answer for other methods, missing
// files, directories, forbidden extensions or hidden paths (a segment
// starting with a dot, except /.well-known/).
func (f *files) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	name, ok := f.name(r.URL.Path)
	if !ok {
		return false
	}
	// name has no empty, "." or ".." segment: it stays in dir.
	file, err := os.Open(filepath.Join(f.dir, filepath.FromSlash(name))) //nolint:gosec // see above
	if err != nil {
		return false
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || !fi.Mode().IsRegular() {
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
func (f *files) name(urlPath string) (string, bool) {
	name := strings.TrimPrefix(urlPath, "/")
	if strings.IndexByte(name, 0) >= 0 {
		return "", false
	}
	// An empty segment is a directory's path ("/", "/docs/") or "//".
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
