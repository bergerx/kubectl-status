package plugin

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"
)

// This file implements opt-in line-level test coverage for the embedded .tmpl files, mirroring
// what `go test -coverprofile` gives .go files -- something Go's own coverage tooling can't do,
// since it only instruments compiled .go statements and has no visibility into text/template
// content executed at runtime. It is only ever active when templateCoverageEnvVar is set (unit
// and e2e test runs via the Makefile's coverage targets); with it unset -- every normal CLI
// invocation and the default `make test` -- buildTemplateSet takes its original, unmodified
// ParseFS path with zero overhead.
//
// Approach: reparse each embedded file in isolation to get its real *parse.Tree (the same tree
// text/template itself builds), walk it to find every leaf "statement" node (action/template/
// break/continue calls, recursing into if/range/with bodies but not instrumenting the branch
// construct itself), and splice a no-op marker call immediately before each one's own opening
// delimiter -- mirroring that delimiter's `{{`/`{{-` style exactly, so the splice is provably a
// no-op regardless of whether the surrounding template relies on `{{-`/`-}}` whitespace trimming
// or on plain delimiters plus meaningful literal whitespace (both patterns exist in this
// codebase; see core/Service.tmpl's ExternalName branch for the latter). Using the real AST
// (rather than a hand-rolled lexer) means comments, multi-line actions, quoted/raw strings, etc.
// never need special-casing -- the real parser has already resolved all of that before this code
// ever sees a byte offset.
//
// Deriving splice points from the real parser this way still requires a second, actually-executed
// parse of the instrumented source (there's no exported hook into text/template's unexported
// executor), so each file is parsed twice when coverage is enabled: once (via
// instrumentTemplateSource) purely to discover coverable nodes, and once for real via the
// resulting instrumented text.

const (
	// templateCoverageEnvVar, when set to a non-empty output path, enables template instrumentation
	// in buildTemplateSet and is where FlushTemplateCoverageProfile writes the accumulated profile.
	templateCoverageEnvVar = "KUBECTL_STATUS_TEMPLATE_COVERAGE"
	// templateCoverageFuncName is the marker function spliced into instrumented template source.
	// It is only ever added to the FuncMap used for the embedded tree's instrumented parse -- never
	// to the FuncMap parseUserOverlay uses -- so a user's ~/.kubectl-status/templates overlay can
	// never invoke it.
	templateCoverageFuncName = "__templateCoverageHit"
)

func templateCoverageEnabled() bool {
	return os.Getenv(templateCoverageEnvVar) != ""
}

// templateCoverageRecorder accumulates per-line hit counts for instrumented template source
// across every buildTemplateSet call in this process (each renderEngine/test case builds its own
// templateSet, but they all share this one package-level recorder so counts accumulate across the
// whole test run rather than resetting per call).
type templateCoverageRecorder struct {
	mu      sync.Mutex
	hits    map[string]int64
	lineLen map[string]int
}

var coverageRecorder = &templateCoverageRecorder{
	hits:    make(map[string]int64),
	lineLen: make(map[string]int),
}

// register records that key ("relpath:line") is instrumented, so it's reported even if never hit.
func (r *templateCoverageRecorder) register(key string, lineLen int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hits[key]; !ok {
		r.hits[key] = 0
	}
	r.lineLen[key] = lineLen
}

// hitFunc returns the closure bound into the instrumented FuncMap as templateCoverageFuncName.
func (r *templateCoverageRecorder) hitFunc() func(string) string {
	return func(key string) string {
		r.mu.Lock()
		r.hits[key]++
		r.mu.Unlock()
		return ""
	}
}

// FlushTemplateCoverageProfile writes this process's accumulated template line-hit counts, in `go
// tool cover`'s "mode: count" profile format, to the path named by templateCoverageEnvVar. It is a
// no-op if the env var is unset, or if this process never actually recorded any hits -- the latter
// matters because pkg/plugin's and cmd's test binaries are separate processes each with their own
// copy of coverageRecorder, and `make test-e2e` runs `go test ./... -run 'TestE2E*'`, under which
// pkg/plugin's own TestMain would otherwise fire with zero hits and clobber a sibling process's
// already-written profile at the same path.
//
// Exported for pkg/plugin's and cmd's TestMain to call after m.Run().
func FlushTemplateCoverageProfile() error {
	path := os.Getenv(templateCoverageEnvVar)
	if path == "" {
		return nil
	}
	// `go test ./...` runs each package's test binary with its cwd set to that package's own
	// source directory, not wherever `go test`/`make` was invoked from -- so a relative path here
	// would silently land inside pkg/plugin/ or cmd/ instead of wherever the caller (e.g.
	// `make template-cover-html`) actually looks for it, for a process-boundary reason that isn't
	// obvious from the resulting "file not found". Fail loudly instead.
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute path (got %q): go test runs each package's test binary from that package's own directory, not the repo root", templateCoverageEnvVar, path)
	}
	return coverageRecorder.writeProfile(path)
}

func (r *templateCoverageRecorder) writeProfile(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.hits) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.hits))
	for k := range r.hits {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteString("mode: count\n")
	for _, key := range keys {
		idx := strings.LastIndexByte(key, ':')
		file, line := key[:idx], key[idx+1:]
		length := r.lineLen[key]
		if length < 1 {
			length = 1
		}
		fmt.Fprintf(&buf, "%s:%s.1,%s.%d 1 %d\n", file, line, line, length+1, r.hits[key])
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// buildInstrumentedEmbedded is the coverage-enabled replacement for buildTemplateSet's normal
// `template.New("templates").Funcs(funcs).ParseFS(templatesFS, ...)` call. It mirrors ParseFS's
// own naming semantics (see stdlib text/template/helper.go's parseFiles) exactly, but parses each
// file's instrumented source instead of its raw content.
func buildInstrumentedEmbedded(funcs template.FuncMap) (*template.Template, error) {
	instrumentedFuncs := make(template.FuncMap, len(funcs)+1)
	for name, fn := range funcs {
		instrumentedFuncs[name] = fn
	}
	instrumentedFuncs[templateCoverageFuncName] = coverageRecorder.hitFunc()

	// Pre-named "templates", exactly like buildTemplateSet's own root -- since no embedded file is
	// ever named "templates.tmpl", every file below becomes a t.New(name) child and this root stays
	// an empty, never-executed template, matching today's behavior.
	t := template.New("templates").Funcs(instrumentedFuncs)
	for _, pattern := range []string{"templates/*.tmpl", "templates/*/*.tmpl"} {
		filenames, err := fs.Glob(templatesFS, pattern)
		if err != nil {
			return nil, err
		}
		for _, filename := range filenames {
			raw, err := fs.ReadFile(templatesFS, filename)
			if err != nil {
				return nil, err
			}
			instrumented, err := instrumentTemplateSource(filename, raw, funcs)
			if err != nil {
				return nil, err
			}
			if _, err := t.New(filepath.Base(filename)).Parse(string(instrumented)); err != nil {
				return nil, err
			}
		}
	}
	return t, nil
}

// coverPoint is one located, not-yet-spliced coverable node: a byte offset (into the node's own
// content, just past its opening delimiter -- see parse.Node.Position's doc) and the node's line.
type coverPoint struct {
	pos  int
	line int
}

// instrumentTemplateSource parses filename's raw content in isolation (using the real FuncMap so
// every function call resolves, but not yet the coverage marker) purely to discover coverable
// nodes via the real *parse.Tree, then splices a marker before each one's own opening delimiter in
// the raw bytes and returns the result. filename is the templatesFS-relative path (e.g.
// "templates/workloads/Pod.tmpl"); every discovered line is registered with coverageRecorder under
// the repo-root-relative key "./pkg/plugin/<filename>:<line>" so `go tool cover -html` (which
// reads report paths directly via os.ReadFile for any path starting with "." or "/", bypassing its
// usual go-list package resolution) renders it correctly.
func instrumentTemplateSource(filename string, raw []byte, funcs template.FuncMap) ([]byte, error) {
	probe, err := template.New(filepath.Base(filename)).Funcs(funcs).Parse(string(raw))
	if err != nil {
		return nil, err
	}

	var points []coverPoint
	for _, tmpl := range probe.Templates() {
		if tmpl.Tree == nil {
			continue
		}
		collectCoverPoints(tmpl.Tree.Root, &points)
	}
	sort.Slice(points, func(i, j int) bool { return points[i].pos < points[j].pos })

	relPath := "./pkg/plugin/" + filename
	lineLens := lineLengths(raw)

	var out bytes.Buffer
	last := 0
	for _, p := range points {
		delimStart, dashed, ok := findOpenDelim(raw, p.pos)
		if !ok || delimStart < last {
			// Can't safely locate (or would overlap an already-placed marker for) this point's own
			// opening delimiter -- skip instrumenting it rather than risk corrupting output.
			continue
		}
		out.Write(raw[last:delimStart])
		key := fmt.Sprintf("%s:%d", relPath, p.line)
		lineLen := 1
		if p.line < len(lineLens) {
			lineLen = lineLens[p.line]
		}
		coverageRecorder.register(key, lineLen)
		if dashed {
			fmt.Fprintf(&out, `{{- %s %s -}}`, templateCoverageFuncName, strconv.Quote(key))
		} else {
			fmt.Fprintf(&out, `{{%s %s}}`, templateCoverageFuncName, strconv.Quote(key))
		}
		last = delimStart
	}
	out.Write(raw[last:])
	return out.Bytes(), nil
}

// collectCoverPoints recursively walks list, collecting one coverPoint per leaf "statement" node:
// action calls, {{template}} calls, and {{break}}/{{continue}}. if/range/with nodes are not
// themselves instrumented -- only their List/ElseList bodies are recursed into -- since the
// coverage signal that matters is which branch/iteration ran, which the body nodes already give
// (including, for free, empty-vs-nonempty range coverage via List vs. ElseList). Comments never
// appear as nodes at all unless parse.ParseComments was set (it isn't, here), so this walk skips
// every comment -- including multi-line block comments -- automatically.
func collectCoverPoints(list *parse.ListNode, out *[]coverPoint) {
	if list == nil {
		return
	}
	for _, n := range list.Nodes {
		switch node := n.(type) {
		case *parse.ActionNode:
			*out = append(*out, coverPoint{pos: int(node.Pos), line: node.Line})
		case *parse.TemplateNode:
			*out = append(*out, coverPoint{pos: int(node.Pos), line: node.Line})
		case *parse.BreakNode:
			*out = append(*out, coverPoint{pos: int(node.Pos), line: node.Line})
		case *parse.ContinueNode:
			*out = append(*out, coverPoint{pos: int(node.Pos), line: node.Line})
		case *parse.IfNode:
			collectCoverPoints(node.List, out)
			collectCoverPoints(node.ElseList, out)
		case *parse.RangeNode:
			collectCoverPoints(node.List, out)
			collectCoverPoints(node.ElseList, out)
		case *parse.WithNode:
			collectCoverPoints(node.List, out)
			collectCoverPoints(node.ElseList, out)
		}
	}
}

// findOpenDelim locates the true `{{`/`{{-` opening delimiter of the node whose own content starts
// at contentPos (parse.Node.Position() -- confirmed against the Go 1.26 lexer, this is the byte
// just past the delimiter and its optional trim-marker-plus-space, not the delimiter itself). The
// only bytes that can legally appear between a node's true delimiter and its own first content
// token are whitespace, plus an optional "- " trim marker -- so a bounded backward scan for that
// exact shape is sufficient; it doesn't need to (and must not try to) reason about anything a real
// lexer would have to.
func findOpenDelim(src []byte, contentPos int) (delimStart int, dashed bool, ok bool) {
	if contentPos > len(src) {
		return 0, false, false
	}
	i := contentPos
	for i > 0 && isTemplateSpace(src[i-1]) {
		i--
	}
	if i >= 3 && src[i-1] == '-' && string(src[i-3:i-1]) == "{{" {
		return i - 3, true, true
	}
	if i >= 2 && string(src[i-2:i]) == "{{" {
		return i - 2, false, true
	}
	return 0, false, false
}

func isTemplateSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// lineLengths returns, 1-indexed (index 0 unused), the byte length of each line in raw excluding
// its trailing newline.
func lineLengths(raw []byte) []int {
	lines := bytes.Split(raw, []byte("\n"))
	lens := make([]int, len(lines)+1)
	for i, l := range lines {
		lens[i+1] = len(l)
	}
	return lens
}
