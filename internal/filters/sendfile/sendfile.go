// Package sendfile implements the sendfile HTTP filter: when the route's
// cluster answers 200 with an X-Sendfile header naming a file, kest sends the
// file itself, so that PHP neither reads it nor holds a worker while it goes
// out, and ranges and conditional requests work. RoadRunner's sendfile
// middleware reads the same header.
package sendfile

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "sendfile"

// Header names the file in a response; TypeHeader asks the application for
// it (Symfony's BinaryFileResponse reads it).
const (
	Header     = "X-Sendfile"
	TypeHeader = "X-Sendfile-Type"
)

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type:      Type,
		NewConfig: func() any { return &Config{} },
		Build: func(bc registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("sendfile: unexpected config type")
			}
			return New(c, bc.ConfigDir, bc.Logger)
		},
	})
}

// Config configures the filter.
type Config struct {
	// Dirs are the directories files may be sent from, relative to the
	// config file. A file outside them is refused.
	Dirs []string `yaml:"dirs"`
}

// Validate reports a missing dirs.
func (c *Config) Validate() error {
	if len(c.Dirs) == 0 {
		return &config.FieldError{Field: "dirs", Msg: "at least one directory is required"}
	}
	return nil
}

// New returns the filter for a validated config; configDir resolves relative
// dirs. A dir that isn't a directory is a *config.FieldError, so that kest
// validate reports it.
func New(cfg *Config, configDir string, logger *slog.Logger) (httpx.Filter, error) {
	f := &filter{logger: logger}
	for i, dir := range cfg.Dirs {
		if configDir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(configDir, dir)
		}
		// Files are matched by their real path, so the directories are too.
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			var fi os.FileInfo
			if fi, err = os.Stat(resolved); err == nil && !fi.IsDir() {
				err = fmt.Errorf("%s is not a directory", dir)
			}
		}
		if err != nil {
			return nil, &config.FieldError{Field: fmt.Sprintf("dirs[%d]", i), Msg: err.Error()}
		}
		f.dirs = append(f.dirs, resolved)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set(TypeHeader, Header) // ours, not the client's
			sw := &writer{ResponseWriter: w, r: r, f: f}
			next.ServeHTTP(sw, r)
		})
	}, nil
}

type filter struct {
	dirs   []string // real paths
	logger *slog.Logger
}

// writer passes a response through, unless its final status is 200 with
// X-Sendfile: then it sends the file and drops what the handler writes.
type writer struct {
	http.ResponseWriter
	r       *http.Request
	f       *filter
	decided bool // the final status came
	sent    bool // the file went out instead of the handler's body
}

func (w *writer) WriteHeader(code int) {
	if w.decided {
		return
	}
	if code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code) // informational: the final status follows
		return
	}
	w.decided = true
	name := w.Header().Get(Header)
	w.Header().Del(Header)
	if code != http.StatusOK || name == "" {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.sent = true
	w.f.send(w.ResponseWriter, w.r, name)
}

func (w *writer) Write(b []byte) (int, error) {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if w.sent {
		return len(b), nil // the application's body, empty for X-Sendfile
	}
	return w.ResponseWriter.Write(b)
}

// Flush passes through, unless the file was sent.
func (w *writer) Flush() {
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	if !w.sent {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *writer) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// send answers with the file the application named, keeping the headers it
// set (Content-Type, Content-Disposition, Cache-Control, ETag) except
// Content-Length, which is the file's.
func (f *filter) send(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Del("Content-Length")
	file, status, err := f.open(name)
	if err != nil {
		f.logger.Error("X-Sendfile refused", "method", r.Method, "path", r.URL.Path, "file", name, "err", err)
		for _, h := range []string{"Content-Type", "Content-Disposition", "Content-Encoding", "Etag", "Last-Modified"} {
			w.Header().Del(h)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil {
		f.logger.Error("X-Sendfile refused", "method", r.Method, "path", r.URL.Path, "file", name, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), file)
}

// open opens a regular file inside one of the directories, by its real path,
// symbolic links resolved. It returns the status to answer when it can't:
// 404 for a missing file or not a regular one, 403 outside the directories.
func (f *filter) open(name string) (*os.File, int, error) {
	if !filepath.IsAbs(name) {
		return nil, http.StatusForbidden, errors.New("not an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	if !f.allowed(resolved) {
		return nil, http.StatusForbidden, fmt.Errorf("%s is outside the sendfile filter's dirs", resolved)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	if fi, err := file.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = file.Close()
		return nil, http.StatusNotFound, fmt.Errorf("%s is not a regular file", resolved)
	}
	return file, 0, nil
}

// allowed reports whether a real path is inside one of the directories.
func (f *filter) allowed(resolved string) bool {
	for _, dir := range f.dirs {
		if strings.HasPrefix(resolved, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
