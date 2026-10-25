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
