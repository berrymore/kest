// Package docs checks that the documentation agrees with the repository:
// links resolve, make targets and IDs exist, the package layout, the config
// reference and the tool versions match the code. `make docs-check` runs it
// alone; `make test` runs it with the rest. The checks a program can't do are
// in .ai/review.md.
package docs

import (
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/cluster"
	_ "github.com/berrymore/kest/internal/cluster/phpworkers" // the extensions of cmd/kest/extensions.go
	"github.com/berrymore/kest/internal/config"
	_ "github.com/berrymore/kest/internal/filters/accesslog"
	_ "github.com/berrymore/kest/internal/filters/clientip"
	_ "github.com/berrymore/kest/internal/filters/compression"
	_ "github.com/berrymore/kest/internal/filters/cors"
	_ "github.com/berrymore/kest/internal/filters/headers"
	_ "github.com/berrymore/kest/internal/filters/recovery"
	_ "github.com/berrymore/kest/internal/filters/requestid"
	_ "github.com/berrymore/kest/internal/filters/sendfile"
	_ "github.com/berrymore/kest/internal/filters/static"
	"github.com/berrymore/kest/internal/httpx"
)

var linkRE = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// Relative links point to files that exist, and anchors to headings that
// exist.
func TestLinks(t *testing.T) {
	docs := map[string]*doc{}
	load := func(p string) *doc {
		if docs[p] == nil {
			docs[p] = parseDoc(t, p)
		}
		return docs[p]
	}
	for _, file := range markdownFiles(t) {
		d := load(file)
		for _, l := range d.prose {
			for _, m := range linkRE.FindAllStringSubmatch(l.text, -1) {
				target := m[1]
				if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				file, anchor, _ := strings.Cut(target, "#")
				switch {
				case file == "":
					file = d.path
				case strings.HasPrefix(file, "/"):
					file = strings.TrimPrefix(file, "/")
				default:
					file = path.Join(path.Dir(d.path), file)
				}
				if !exists(file) {
					t.Errorf("%s: link to %s: no such file", d.at(l.n), target)
					continue
				}
				if anchor != "" && strings.HasSuffix(file, ".md") && !load(file).anchors()[anchor] {
					t.Errorf("%s: link to %s: no heading for #%s in %s", d.at(l.n), target, anchor, file)
				}
			}
		}
	}
}

var (
	targetRE     = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*):`)
	helpTargetRE = regexp.MustCompile(`(?m)^([a-zA-Z][a-zA-Z0-9_-]*):.*## `)
	makeRE       = regexp.MustCompile(`\bmake ([a-z][a-z0-9_-]*)`)
)

// Every make target in the help is in the AGENTS.md command table, and every
// `make x` quoted in the docs exists.
func TestMakeTargets(t *testing.T) {
	makefile := readFile(t, "Makefile")
	targets := map[string]bool{}
	for _, m := range targetRE.FindAllStringSubmatch(makefile, -1) {
		targets[m[1]] = true
	}

	agents := parseDoc(t, "AGENTS.md")
	listed := map[string]bool{}
	for _, l := range agents.code {
		for _, m := range makeRE.FindAllStringSubmatch(l.text, -1) {
			listed[m[1]] = true
		}
	}
	for _, m := range helpTargetRE.FindAllStringSubmatch(makefile, -1) {
		if !listed[m[1]] {
			t.Errorf("AGENTS.md: the command table has no `make %s` (Makefile help target)", m[1])
		}
	}

	for _, file := range markdownFiles(t) {
		d := parseDoc(t, file)
		for _, l := range d.code {
			for _, m := range makeRE.FindAllStringSubmatch(l.text, -1) {
				if !targets[m[1]] {
					t.Errorf("%s: `make %s`: no such Makefile target", d.at(l.n), m[1])
				}
			}
		}
	}
}

var (
	backlogItemRE = regexp.MustCompile(`(?m)^- \[(.)\] (K-\d{3})\b`)
	itemRefRE     = regexp.MustCompile(`\bK-\d{3}\b`)
	adrRefRE      = regexp.MustCompile(`\bADR-(\d{4})\b`)
)

// Backlog IDs are unique, and every K-NNN and ADR-NNNN mentioned in docs or
// code exists.
func TestIDs(t *testing.T) {
	backlog := readFile(t, ".ai/backlog.md")
	items := map[string]bool{}
	for _, m := range backlogItemRE.FindAllStringSubmatch(backlog, -1) {
		if items[m[2]] {
			t.Errorf(".ai/backlog.md: %s is defined twice", m[2])
		}
		items[m[2]] = true
	}
	adrs := map[string]bool{}
	for _, f := range adrFiles(t) {
		adrs[f[:4]] = true
	}

	files := markdownFiles(t)
	files = append(files, goFiles(t)...)
	for _, file := range files {
		for i, text := range strings.Split(readFile(t, file), "\n") {
			n := i + 1
			for _, id := range itemRefRE.FindAllString(text, -1) {
				if !items[id] {
					t.Errorf("%s:%d: %s is not in .ai/backlog.md", file, n, id)
				}
			}
			for _, m := range adrRefRE.FindAllStringSubmatch(text, -1) {
				if !adrs[m[1]] && m[1] != "0000" {
					t.Errorf("%s:%d: ADR-%s: no such file in .ai/decisions", file, n, m[1])
				}
			}
		}
	}
}

var (
	adrStatusRE = regexp.MustCompile(`(?m)^- \*\*Status:\*\* (\w+)`)
	adrRowRE    = regexp.MustCompile(`(?m)^\| \[(\d{4})\]\(([^)]+)\) \|[^|]*\| (\w+)`)
)

// The ADR index lists every ADR with the status the ADR has.
func TestADRIndex(t *testing.T) {
	index := map[string]string{} // file → status
	for _, m := range adrRowRE.FindAllStringSubmatch(readFile(t, ".ai/decisions/README.md"), -1) {
		index[m[2]] = m[3]
	}
	for _, f := range adrFiles(t) {
		m := adrStatusRE.FindStringSubmatch(readFile(t, ".ai/decisions/"+f))
		switch {
		case m == nil:
			t.Errorf(".ai/decisions/%s: no `- **Status:**` line", f)
		case index[f] == "":
			t.Errorf(".ai/decisions/README.md: %s is not in the index", f)
		case index[f] != m[1]:
			t.Errorf(".ai/decisions/README.md: %s is %s in the index, %s in the ADR", f, index[f], m[1])
		}
	}
}

// Every directory of internal/ and test/ is in the layouts of AGENTS.md and
// architecture.md § 7; their paths exist; architecture.md marks planned (⚪) packages only
// while they don't exist.
func TestLayout(t *testing.T) {
	agents := layoutBlock(t, "AGENTS.md", "## Repository layout")
	for p := range agents {
		if !exists(p) {
			t.Errorf("AGENTS.md: the layout lists %s, which doesn't exist", p)
		}
	}
	arch := layoutBlock(t, ".ai/architecture.md", "## 7. Package layout")
	for p, status := range arch {
		switch {
		case strings.Contains(p, "<"): // a pattern such as internal/filters/<name>/
		case status == "⚪" && exists(p):
			t.Errorf(".ai/architecture.md § 7: %s exists but is marked planned (⚪)", p)
		case status != "⚪" && !exists(p):
			t.Errorf(".ai/architecture.md § 7: %s is marked %s but doesn't exist", p, status)
		}
	}
	for _, dir := range []string{"internal", "test"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := dir + "/" + e.Name() + "/"
			if _, ok := agents[p]; !ok {
				t.Errorf("AGENTS.md: the layout doesn't list %s", p)
			}
			if _, ok := arch[p]; !ok {
				t.Errorf(".ai/architecture.md § 7: the layout doesn't list %s", p)
			}
		}
	}
}

// layoutBlock returns the paths of the first code block after heading in
// file, with the status marker that follows each path, if any.
func layoutBlock(t *testing.T, file, heading string) map[string]string {
	t.Helper()
	d := parseDoc(t, file)
	start := slices.IndexFunc(d.lines, func(l line) bool { return l.text == heading })
	if start < 0 {
		t.Fatalf("%s: no %q heading", file, heading)
	}
	out := map[string]string{}
	inBlock := false
	for _, l := range d.lines[start:] {
		if strings.HasPrefix(l.text, "```") {
			if inBlock {
				return out
			}
			inBlock = true
			continue
		}
		if fields := strings.Fields(l.text); inBlock && len(fields) > 0 {
			status := ""
			if len(fields) > 1 && slices.Contains([]string{"🟢", "🟡", "⚪"}, fields[1]) {
				status = fields[1]
			}
			out[fields[0]] = status
		}
	}
	t.Fatalf("%s: no code block after %q", file, heading)
	return nil
}

var (
	fieldRowRE   = regexp.MustCompile("^\\| `([^`]+)` \\|")
	fieldNameRE  = regexp.MustCompile("^(?:#{2,6} .*|\\*\\*)`([^`]+)`")
	tableStartRE = regexp.MustCompile(`^\| Field \|`)
)

// Every config field is in docs/config.md (a row of a Field table, a heading
// or a bold label), and every row of a Field table is a config field. Fields
// are matched by name, not by path: a new or removed name is caught, a field
// added under a name used elsewhere is not.
func TestConfigReference(t *testing.T) {
	fields := map[string]bool{}
	collectFields(reflect.TypeFor[config.Config](), fields)
	for _, typ := range cluster.Types.Types() {
		f, _ := cluster.Types.Lookup(typ)
		collectFields(reflect.TypeOf(f.NewConfig()), fields)
	}
	for _, typ := range httpx.Filters.Types() {
		if f, _ := httpx.Filters.Lookup(typ); f.NewConfig != nil {
			collectFields(reflect.TypeOf(f.NewConfig()), fields)
		}
	}

	documented := map[string]bool{}
	d := parseDoc(t, "docs/config.md")
	inTable := false
	for _, l := range d.lines {
		name := ""
		if m := fieldRowRE.FindStringSubmatch(l.text); m != nil {
			name = m[1]
		} else if m := fieldNameRE.FindStringSubmatch(l.text); m != nil {
			name = m[1]
		}
		// A path such as `listeners[].http` or `match.prefix` names every
		// field on it.
		var names []string
		for p := range strings.SplitSeq(name, ".") {
			if p = strings.TrimSuffix(p, "[]"); p != "" {
				names = append(names, p)
				documented[p] = true
			}
		}

		switch {
		case tableStartRE.MatchString(l.text):
			inTable = true
		case !strings.HasPrefix(l.text, "|"):
			inTable = false
		case inTable:
			for _, n := range names {
				if !fields[n] {
					t.Errorf("%s: `%s` is documented but is not a config field", d.at(l.n), n)
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !documented[name] {
			t.Errorf("docs/config.md: config field `%s` is not documented", name)
		}
	}
}

// collectFields adds the YAML names of the fields of t, a type of this
// module, and of the types it contains.
func collectFields(t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || !strings.HasPrefix(t.PkgPath(), "github.com/berrymore/kest/") {
		return
	}
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch {
		case name == "-" || !f.IsExported():
			continue
		case strings.Contains(opts, "inline"):
		case name != "":
			out[name] = true
		default:
			continue // not decoded by name
		}
		collectFields(f.Type, out)
	}
}

// The extensions the binary compiles in are the ones this package imports.
func TestExtensionsImported(t *testing.T) {
	want := importsOf(t, "test/docs/docs_test.go")
	for _, p := range importsOf(t, "cmd/kest/extensions.go") {
		if !slices.Contains(want, p) {
			t.Errorf("cmd/kest/extensions.go imports %s: import it in test/docs/docs_test.go too", p)
		}
	}
}

func importsOf(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, file), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, p)
	}
	return out
}

var (
	goDirectiveRE = regexp.MustCompile(`(?m)^go (\d+\.\d+)`)
	lintVersionRE = regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION\s*:=\s*(\S+)`)
)

// AGENTS.md names the Go version of go.mod and the pinned golangci-lint.
func TestToolVersions(t *testing.T) {
	agents := readFile(t, "AGENTS.md")
	if m := goDirectiveRE.FindStringSubmatch(readFile(t, "go.mod")); m == nil {
		t.Error("go.mod: no go directive")
	} else if !strings.Contains(agents, "Go "+m[1]) {
		t.Errorf("AGENTS.md doesn't mention Go %s (go.mod)", m[1])
	}
	if m := lintVersionRE.FindStringSubmatch(readFile(t, "Makefile")); m == nil {
		t.Error("Makefile: no GOLANGCI_LINT_VERSION")
	} else if !strings.Contains(agents, "golangci-lint "+m[1]) {
		t.Errorf("AGENTS.md doesn't mention golangci-lint %s (Makefile)", m[1])
	}
}

var (
	lastUpdatedRE = regexp.MustCompile(`(?m)^_Last updated: (\d{4}-\d{2}-\d{2})`)
	sessionRE     = regexp.MustCompile(`(?m)^- \*\*(\d{4}-\d{2}-\d{2})\*\*`)
)

// status.md is dated no earlier than its newest session, and the session
// log is newest first.
func TestStatusDates(t *testing.T) {
	status := readFile(t, ".ai/status.md")
	m := lastUpdatedRE.FindStringSubmatch(status)
	if m == nil {
		t.Fatal(".ai/status.md: no `_Last updated: YYYY-MM-DD` line")
	}
	sessions := sessionRE.FindAllStringSubmatch(status, -1)
	for i, s := range sessions {
		if i > 0 && s[1] > sessions[i-1][1] {
			t.Errorf(".ai/status.md: the session log is not newest first: %s after %s", s[1], sessions[i-1][1])
		}
	}
	if len(sessions) > 0 && sessions[0][1] > m[1] {
		t.Errorf(".ai/status.md: last updated %s, before the newest session (%s)", m[1], sessions[0][1])
	}
}

var (
	planDoneRE = regexp.MustCompile(`(?m)^- \*\*Status:\*\* Done`)
	openStepRE = regexp.MustCompile(`(?m)^\s*\d+\. \[[ ~]\] .*`)
)

// A plan marked Done has no open steps.
func TestDonePlans(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(root, ".ai", "plans"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "README.md" {
			continue
		}
		plan := readFile(t, ".ai/plans/"+e.Name())
		if planDoneRE.MatchString(plan) {
			for _, step := range openStepRE.FindAllString(plan, -1) {
				t.Errorf(".ai/plans/%s is Done but has an open step: %s", e.Name(), strings.TrimSpace(step))
			}
		}
	}
}

func adrFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".ai", "decisions"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); len(n) > 5 && n[4] == '-' && n != "0000-template.md" {
			out = append(out, n)
		}
	}
	return out
}

// goFiles returns the Go files of the module, relative to root.
func goFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"cmd", "internal", "test"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && (e.Name() == "vendor" || e.Name() == "testdata") {
				return filepath.SkipDir
			}
			if strings.HasSuffix(p, ".go") {
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
