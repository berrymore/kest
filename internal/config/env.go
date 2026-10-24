package config

import (
	"fmt"
	"strings"
)

// expandEnv replaces environment variable references in s:
//
//   - ${NAME} is replaced by the value of NAME; it is an error if NAME is unset.
//   - ${NAME:-default} is replaced by default if NAME is unset or empty.
//   - $${ produces a literal "${".
//
// Any other "$" is kept as is, so values like "pa$$word" need no escaping.
func expandEnv(s string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}

	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "$${"):
			b.WriteString("${")
			i += 3
		case strings.HasPrefix(s[i:], "${"):
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated variable reference %q", s[i:])
			}
			value, err := resolveVar(s[i+2:i+2+end], lookup)
			if err != nil {
				return "", err
			}
			b.WriteString(value)
			i += 2 + end + 1
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), nil
}

// resolveVar resolves the inside of a ${...} reference.
func resolveVar(expr string, lookup func(string) (string, bool)) (string, error) {
	name, def, hasDefault := strings.Cut(expr, ":-")
	if !isVarName(name) {
		return "", fmt.Errorf("invalid variable reference ${%s}: use ${NAME} or ${NAME:-default}", expr)
	}
	if strings.Contains(def, "${") {
		return "", fmt.Errorf("nested variable references are not supported: ${%s}", expr)
	}

	value, ok := lookup(name)
	switch {
	case ok && value != "":
		return value, nil
	case hasDefault:
		return def, nil
	case ok:
		return "", nil
	default:
		return "", fmt.Errorf("environment variable %s is not set (use ${%s:-default} to make it optional)", name, name)
	}
}

func isVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
