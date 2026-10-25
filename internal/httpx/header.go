package httpx

import "strings"

// IsToken reports whether s is an HTTP token (RFC 9110 § 5.6.2), as header
// names and methods are.
func IsToken(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// IsHeaderValue reports whether s can be a header value: no control
// characters other than tab, so no CR or LF that would split the header.
func IsHeaderValue(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
