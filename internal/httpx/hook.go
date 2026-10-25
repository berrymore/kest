package httpx

import "net/http"

// BeforeHeader wraps w so that edit runs once, right before the response
// header goes out: on WriteHeader with a final status, or on the first Write
// or Flush (status 200). Informational (1xx) responses pass without it. The
// wrapper passes Flush and http.ResponseController calls through.
func BeforeHeader(w http.ResponseWriter, edit func(h http.Header, status int)) http.ResponseWriter {
	return &hookWriter{ResponseWriter: w, edit: edit}
}

type hookWriter struct {
	http.ResponseWriter
	edit func(h http.Header, status int)
	done bool
}

func (w *hookWriter) before(status int) {
	if !w.done {
		w.done = true
		w.edit(w.Header(), status)
	}
}

func (w *hookWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.before(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *hookWriter) Write(b []byte) (int, error) {
	w.before(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

func (w *hookWriter) Flush() {
	w.before(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (w *hookWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
