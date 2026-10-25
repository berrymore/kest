package httpx

import (
	"net/http"
	"strings"
)

// normalizer rewrites request paths before the filters and the router: it
// removes "." and ".." segments (dots, RFC 3986 § 5.2.4) and collapses
// repeated slashes (slashes). It works on the decoded path, so "%2e%2e" is
// "..". A path that changes is set as the URL's path, without its original
// encoding, so that the router, the filters and the cluster see the same
// path; an unchanged path keeps its encoding.
func normalizer(h http.Handler, dots, slashes bool) http.Handler {
	if !dots && !slashes {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; strings.HasPrefix(p, "/") {
			if clean := cleanPath(p, dots, slashes); clean != p {
				out := *r
				u := *r.URL
				u.Path, u.RawPath = clean, ""
				out.URL = &u
				r = &out
			}
		}
		h.ServeHTTP(w, r)
	})
}

// cleanPath normalizes p, which starts with "/". A trailing slash stays, and
// a final "." or ".." leaves one, as in RFC 3986: "/a/b/.." is "/a/".
func cleanPath(p string, dots, slashes bool) string {
	if !strings.Contains(p, "//") && !strings.Contains(p, "/.") {
		return p // the common case: nothing to do
	}
	segments := strings.Split(p[1:], "/")
	out := make([]string, 0, len(segments))
	trailing := false
	for i, s := range segments {
		last := i == len(segments)-1
		switch {
		case dots && s == ".":
			trailing = last
		case dots && s == "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			trailing = last
		case slashes && s == "" && !last:
		default:
			out = append(out, s)
		}
	}
	clean := "/" + strings.Join(out, "/")
	if trailing && !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	return clean
}
