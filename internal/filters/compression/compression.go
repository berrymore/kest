// Package compression implements the compression HTTP filter: responses of
// compressible types are sent gzipped to clients that accept it.
package compression

import (
	"compress/gzip"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/berrymore/kest/internal/config"
	"github.com/berrymore/kest/internal/httpx"
	"github.com/berrymore/kest/internal/registry"
)

// Type is the filter type name in configuration.
const Type = "compression"

func init() {
	httpx.Filters.Register(registry.Factory[httpx.Filter]{
		Type: Type,
		NewConfig: func() any {
			c := DefaultConfig()
			return &c
		},
		Build: func(_ registry.BuildContext, cfg any) (httpx.Filter, error) {
			c, ok := cfg.(*Config)
			if !ok {
				return nil, errors.New("compression: unexpected config type")
			}
			return New(*c), nil
		},
	})
}

// Config configures the filter.
type Config struct {
	// Level is the gzip level, from 1 (fastest) to 9 (smallest).
	Level int `yaml:"level"`
	// MinLength skips bodies whose Content-Length is smaller.
	MinLength config.ByteSize `yaml:"min_length"`
	// ContentTypes are the media types compressed: "text/html", or "text/*"
	// for a whole type.
	ContentTypes []string `yaml:"content_types"`
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{
		Level:     5,
		MinLength: 1 << 10,
		ContentTypes: []string{"text/*", "application/javascript", "application/json", "application/xml",
			"application/ld+json", "application/manifest+json", "application/problem+json", "application/rss+xml",
			"application/atom+xml", "application/xhtml+xml", "image/svg+xml", "application/wasm"},
	}
}

// Validate reports an invalid level or media type.
func (c *Config) Validate() error {
	var errs []error
	if c.Level < gzip.BestSpeed || c.Level > gzip.BestCompression {
		errs = append(errs, &config.FieldError{Field: "level", Msg: fmt.Sprintf("must be between 1 and 9, got %d", c.Level)})
	}
	if c.MinLength < 0 {
		errs = append(errs, &config.FieldError{Field: "min_length", Msg: "must not be negative"})
	}
	if len(c.ContentTypes) == 0 {
		errs = append(errs, &config.FieldError{Field: "content_types", Msg: "at least one media type is required"})
	}
	for i, t := range c.ContentTypes {
		major, minor, ok := strings.Cut(t, "/")
		if !ok || !httpx.IsToken(major) || !httpx.IsToken(minor) || major == "*" {
			errs = append(errs, &config.FieldError{Field: fmt.Sprintf("content_types[%d]", i),
				Msg: fmt.Sprintf(`invalid media type %q: want type/subtype or type/*`, t)})
		}
	}
	return errors.Join(errs...)
}

// compressor is a validated config with its pool of gzip writers.
type compressor struct {
	minLength int64
	exact     []string // media types, lower case
	majors    []string // "text/" for "text/*"
	writers   sync.Pool
}

// New returns the filter for a validated config.
func New(cfg Config) httpx.Filter {
	c := &compressor{minLength: int64(cfg.MinLength)}
	for _, t := range cfg.ContentTypes {
		t = strings.ToLower(t)
		if major, ok := strings.CutSuffix(t, "/*"); ok {
			c.majors = append(c.majors, major+"/")
		} else {
			c.exact = append(c.exact, t)
		}
	}
	level := cfg.Level
	c.writers.New = func() any {
		w, _ := gzip.NewWriterLevel(nil, level) // the level is validated
		return w
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cw := &responseWriter{ResponseWriter: w, c: c, accepts: acceptsGzip(r), head: r.Method == http.MethodHead}
			defer cw.finish()
			next.ServeHTTP(cw, r)
		})
	}
}

// compressible reports whether responses of this Content-Type are compressed.
func (c *compressor) compressible(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return slices.Contains(c.exact, mt) || slices.ContainsFunc(c.majors, func(m string) bool { return strings.HasPrefix(mt, m) })
}

// acceptsGzip reads Accept-Encoding: gzip, or *, with a q-value above 0.
func acceptsGzip(r *http.Request) bool {
	gz, star := -1.0, -1.0
	for _, v := range r.Header.Values("Accept-Encoding") {
		for part := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(part, ";")
			q := 1.0
			if name, value, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.TrimSpace(name) == "q" {
				if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
					q = f
				}
			}
			switch strings.ToLower(strings.TrimSpace(coding)) {
			case "gzip", "x-gzip":
				gz = q
			case "*":
				star = q
			}
		}
	}
	if gz >= 0 {
		return gz > 0
	}
	return star > 0
}

// responseWriter decides whether to compress when the response header goes
// out: on the first Write or Flush, or when the handler returns, since the
// Content-Type may come only with the body (net/http sniffs it).
type responseWriter struct {
	http.ResponseWriter
	c       *compressor
	accepts bool
	head    bool

	status  int          // set by WriteHeader, not sent yet; 0: none
	decided bool         // the header went out
	gz      *gzip.Writer // non-nil while compressing
}

func (w *responseWriter) WriteHeader(code int) {
	if w.decided {
		w.ResponseWriter.WriteHeader(code) // net/http logs the superfluous call
		return
	}
	if code == http.StatusSwitchingProtocols {
		w.decided = true // the connection becomes something else
	}
	if code < 200 {
		w.ResponseWriter.WriteHeader(code) // informational: the final one follows
		return
	}
	if w.status == 0 {
		w.status = code
	}
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.decide(b)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush sends what is buffered, compressed data included, so streams stay
// streams.
func (w *responseWriter) Flush() {
	if !w.decided {
		w.decide(nil)
	}
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// decide sends the header, compressing if the response qualifies. first is
// the start of the body, for a Content-Type to be sniffed from.
func (w *responseWriter) decide(first []byte) {
	w.decided = true
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	h := w.Header()
	ct := h.Get("Content-Type")
	if ct == "" && len(first) > 0 && h["Content-Type"] == nil {
		// net/http would sniff the compressed bytes: sniff the plain ones.
		ct = http.DetectContentType(first)
		h.Set("Content-Type", ct)
	}
	if w.qualifies(status, h, ct) {
		h.Add("Vary", "Accept-Encoding")
		if w.accepts && !w.head {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			if etag := h.Get("Etag"); etag != "" && !strings.HasPrefix(etag, "W/") {
				h.Set("Etag", "W/"+etag) // the bytes differ now
			}
			gz := w.c.writers.Get().(*gzip.Writer) //nolint:forcetypeassert // the pool holds only these
			gz.Reset(w.ResponseWriter)
			w.gz = gz
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

// qualifies reports whether a response could be compressed, whoever asks:
// its status, type, size and encoding allow it.
func (w *responseWriter) qualifies(status int, h http.Header, contentType string) bool {
	switch status {
	case http.StatusNoContent, http.StatusNotModified, http.StatusPartialContent:
		return false
	}
	if h.Get("Content-Encoding") != "" || !w.c.compressible(contentType) {
		return false
	}
	if strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform") {
		return false
	}
	if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n < w.c.minLength {
		return false
	}
	return true
}

// finish ends the response once the handler returned: it sends a header that
// nothing sent yet, and the end of the compressed stream.
func (w *responseWriter) finish() {
	if !w.decided && w.status != 0 {
		w.decide(nil)
	}
	if w.gz != nil {
		_ = w.gz.Close()
		w.gz.Reset(nil)
		w.c.writers.Put(w.gz)
		w.gz = nil
	}
}
