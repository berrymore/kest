package metrics

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Handler serves the registry in the text format, gzipped when the client
// accepts it (Prometheus does).
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Vary", "Accept-Encoding")
		if req.Method == http.MethodHead {
			return
		}
		var out io.Writer = w
		if acceptsGzip(req.Header.Get("Accept-Encoding")) {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			defer gz.Close()
			out = gz
		}
		// An error here means the client went away; there is nobody to tell.
		_ = r.WriteText(out)
	})
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip: listed
// with a q above 0, or not listed and * is.
func acceptsGzip(header string) bool {
	gzipQ, starQ := -1.0, -1.0
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		q := 1.0
		for p := range strings.SplitSeq(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		switch strings.ToLower(strings.TrimSpace(coding)) {
		case "gzip":
			gzipQ = q
		case "*":
			starQ = q
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}
