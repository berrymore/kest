package docs

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// root is the repository root, seen from this package.
const root = "../.."

// line is one line of a file, numbered from 1.
type line struct {
	n    int
	text string
}

// doc is a markdown file split into prose and code.
type doc struct {
	path  string // relative to root, with forward slashes
	lines []line // all lines
	prose []line // lines outside fenced code blocks, with inline code removed
	code  []line // lines of fenced code blocks and inline code spans
}

func (d *doc) at(n int) string {
	return d.path + ":" + strconv.Itoa(n)
}

// readFile returns the content of a file relative to root.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
	return err == nil
}

func parseDoc(t *testing.T, path string) *doc {
	t.Helper()
	d := &doc{path: path}
	fence := ""
	sc := bufio.NewScanner(strings.NewReader(readFile(t, path)))
	for n := 1; sc.Scan(); n++ {
		text := sc.Text()
		d.lines = append(d.lines, line{n, text})
		trimmed := strings.TrimSpace(text)
		switch {
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			} else {
				d.code = append(d.code, line{n, text})
			}
		case strings.HasPrefix(trimmed, "```"), strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		default:
			prose, spans := splitCode(text)
			d.prose = append(d.prose, line{n, prose})
			for _, s := range spans {
				d.code = append(d.code, line{n, s})
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return d
}

// splitCode removes the inline code spans from text and returns them apart.
func splitCode(text string) (prose string, spans []string) {
	var b strings.Builder
	for {
		i := strings.IndexByte(text, '`')
		if i < 0 {
			b.WriteString(text)
			return b.String(), spans
		}
		run := len(text[i:]) - len(strings.TrimLeft(text[i:], "`"))
		delim := text[i : i+run]
		end := strings.Index(text[i+run:], delim)
		if end < 0 {
			b.WriteString(text)
			return b.String(), spans
		}
		b.WriteString(text[:i])
		spans = append(spans, text[i+run:i+run+end])
		text = text[i+run+end+run:]
	}
}

// headingRE matches an ATX heading outside code.
var headingRE = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*$`)

// anchors returns the heading anchors of d, as GitHub generates them.
func (d *doc) anchors() map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, l := range d.prose {
		m := headingRE.FindStringSubmatch(d.lines[l.n-1].text)
		if m == nil {
			continue
		}
		s := slug(m[1])
		if k := seen[s]; k > 0 {
			out[s+"-"+strconv.Itoa(k)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

var linkTextRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// slug turns a heading into its anchor: links become their text, letters are
// lowercased, spaces become hyphens, and punctuation other than - and _ goes.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(linkTextRE.ReplaceAllString(heading, "$1")) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// markdownFiles returns the repository's markdown files, relative to root,
// leaving out .git and directories that .gitignore names (dependencies,
// build output, local playgrounds).
func markdownFiles(t *testing.T) []string {
	t.Helper()
	skip := map[string]bool{".git": true}
	for l := range strings.Lines(readFile(t, ".gitignore")) {
		l = strings.TrimSpace(l)
		if strings.HasSuffix(l, "/") && !strings.ContainsAny(l, "*?[!#") {
			skip[strings.Trim(l, "/")] = true
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && path != root && skip[e.Name()] {
			return filepath.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

func TestSlug(t *testing.T) {
	for heading, want := range map[string]string{
		"9. Git":                       "9-git",
		"`php_workers`":                "php_workers",
		"`listeners[].http`":           "listenershttp",
		"M3 — Operability":             "m3--operability",
		"Pool and supervisor in RR ✅":  "pool-and-supervisor-in-rr-",
		"[Getting started](x.md) next": "getting-started-next",
	} {
		if got := slug(heading); got != want {
			t.Errorf("slug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestSplitCode(t *testing.T) {
	prose, spans := splitCode("run `make check`, then ``a ` b`` and `open")
	if prose != "run , then  and `open" || !slices.Equal(spans, []string{"make check", "a ` b"}) {
		t.Errorf("splitCode = %q, %q", prose, spans)
	}
}
