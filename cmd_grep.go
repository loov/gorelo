package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

// cmdGrep finds functions and methods whose source contains a given glob
// pattern. The pattern is matched, as a substring, against the full
// declaration source (doc comment, signature, and body). Unlike the
// name-oriented globs elsewhere in gorelo, the pattern is unanchored:
// '*' matches any run of characters (including newlines), '?' matches any
// single character, and a pattern with no wildcards is a literal substring.
// Several alternatives may be separated by '|' ("A|B|C"); a declaration
// matches when it contains any one of them.
//
// Optional specifiers restrict which declarations are searched, using the
// standard gorelo grammar with '*' and '?' globs in the name and source
// parts:
//
//	(none)        every function and method in the module
//	./pkg.*       every declaration in package ./pkg
//	file.go       every declaration in a file (suffix match)
//	Handle*       declarations whose own name matches the glob
//	DB#Get*       methods of type DB whose name matches the glob

type cmdGrep struct {
	jsonOutput bool
	pattern    string
	args       []string
}

func (c *cmdGrep) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.pattern = params.Arg("pattern",
		"glob to find in declaration source ('*' and '?' wildcards; '|' separates alternatives; no wildcards = literal substring)").(string)
	c.args = params.Arg("specifier",
		"optional scope (e.g. ./pkg.*, file.go, Name*, Type#Method*); '*' and '?' globs supported",
		clingy.Repeated, clingy.Optional).([]string)
}

func (c *cmdGrep) Execute(ctx context.Context) error {
	if c.pattern == "" {
		return fmt.Errorf("a search pattern is required")
	}

	re, err := compileContentGlob(c.pattern)
	if err != nil {
		return fmt.Errorf("invalid pattern %q: %w", c.pattern, err)
	}

	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	specs, err := parseGrepSpecs(ix, absDir, c.args)
	if err != nil {
		return err
	}

	results := []grepResult{}
	for _, pkg := range ix.Pkgs {
		for _, file := range pkg.Files {
			if !specs.matchFile(file, pkg, absDir) {
				continue
			}
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if !specs.matchDecl(fd, file, pkg, absDir) {
					continue
				}
				if r, ok := grepDecl(ix, absDir, file, fd, re); ok {
					results = append(results, r)
				}
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		return results[i].Line < results[j].Line
	})

	w := clingy.Stdout(ctx)
	if c.jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	noun := "declarations"
	if len(results) == 1 {
		noun = "declaration"
	}
	fmt.Fprintf(w, "%d %s contain %q\n", len(results), noun, c.pattern)
	if len(results) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	for _, r := range results {
		name := r.Name
		if r.Receiver != "" {
			name = r.Receiver + "." + name
		}
		fmt.Fprintf(w, "%s:%d  %-7s %s\n", r.File, r.Line, r.Kind, name)
		for _, m := range r.Matches {
			fmt.Fprintf(w, "    %d: %s\n", m.Line, m.Text)
		}
	}
	return nil
}

type grepResult struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Receiver string      `json:"receiver,omitempty"`
	File     string      `json:"file"`
	Line     int         `json:"line"`
	End      int         `json:"end"`
	Matches  []grepMatch `json:"matches"`
}

type grepMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// grepDecl matches re against the full source of fd. It reports the
// declaration and the distinct source lines on which a match begins.
func grepDecl(ix *mast.Index, absDir string, file *mast.File, fd *ast.FuncDecl, re *regexp.Regexp) (grepResult, bool) {
	startPos := fd.Pos()
	if fd.Doc != nil && fd.Doc.Pos() < startPos {
		startPos = fd.Doc.Pos()
	}
	start := ix.Fset.Position(startPos).Offset
	end := ix.Fset.Position(fd.End()).Offset
	if start < 0 || end > len(file.Src) || start > end {
		return grepResult{}, false
	}
	src := file.Src[start:end]

	locs := re.FindAllIndex(src, -1)
	if len(locs) == 0 {
		return grepResult{}, false
	}

	tf := ix.Fset.File(fd.Pos())
	var matches []grepMatch
	seenLine := make(map[int]bool)
	for _, loc := range locs {
		line, text := lineAt(file, tf, start+loc[0])
		if seenLine[line] {
			continue
		}
		seenLine[line] = true
		matches = append(matches, grepMatch{Line: line, Text: text})
	}

	r := grepResult{
		Name:    fd.Name.Name,
		Kind:    "func",
		File:    relPath(absDir, file.Path),
		Matches: matches,
	}
	if fd.Recv != nil {
		r.Kind = "method"
		r.Receiver = mast.ReceiverTypeName(fd.Recv)
	}
	r.Line, r.End = declLines(ix.Fset, fd, fd.Doc)
	return r, true
}

// lineAt returns the 1-based source line containing the byte offset and the
// trimmed text of that line.
func lineAt(file *mast.File, tf *token.File, offset int) (int, string) {
	if offset >= len(file.Src) {
		offset = len(file.Src) - 1
	}
	if offset < 0 {
		offset = 0
	}
	line := tf.Line(tf.Pos(offset))
	ls := tf.Offset(tf.LineStart(line))
	le := ls
	for le < len(file.Src) && file.Src[le] != '\n' {
		le++
	}
	return line, strings.TrimSpace(string(file.Src[ls:le]))
}

// compileContentGlob translates a content glob into an unanchored, dotall
// regexp. The pattern may hold several alternatives separated by '|'; a
// declaration matches when it contains any one of them. Within each
// alternative '*' becomes ".*", '?' becomes ".", and every other character
// is matched literally, so an alternative with no wildcards behaves as a
// plain substring search.
func compileContentGlob(pattern string) (*regexp.Regexp, error) {
	var alts []string
	for alt := range strings.SplitSeq(pattern, "|") {
		if alt == "" {
			continue
		}
		alts = append(alts, globToRegexp(alt))
	}
	if len(alts) == 0 {
		return nil, fmt.Errorf("no non-empty patterns")
	}
	return regexp.Compile("(?s)(?:" + strings.Join(alts, "|") + ")")
}

// globToRegexp translates a single glob alternative into a regexp fragment.
func globToRegexp(glob string) string {
	var sb strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return sb.String()
}

// grepSpec is a parsed scope filter restricting which declarations are
// searched. An empty grepSpecs matches everything.
type grepSpec struct {
	source string // resolved source: package path, file suffix, or glob
	name   string // glob over the declaration's own name ("*" = any)
	field  string // glob over the method name; non-empty restricts to methods
}

type grepSpecs []grepSpec

func parseGrepSpecs(ix *mast.Index, absDir string, args []string) (grepSpecs, error) {
	var specs grepSpecs
	for _, arg := range args {
		item, err := rules.ParseItem(arg)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", arg, err)
		}
		if item.Rename != "" || item.FieldRename != "" || item.Detach || item.MethodOf != "" {
			return nil, fmt.Errorf("grep specifier %q must not contain '=' or a rename/attach", arg)
		}

		spec := grepSpec{
			source: item.Source,
			name:   item.Name,
			field:  item.Field,
		}
		if item.IsFileMove { // bare "file.go": scope to that file, any name
			spec.source = item.Source
			spec.name = "*"
		}
		if spec.name == "" {
			spec.name = "*"
		}
		if spec.source != "" && !hasGlob(spec.source) {
			spec.source = relo.ResolveSource(ix, spec.source, absDir)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// matchFile reports whether any spec could match a declaration in file. It is
// a cheap pre-filter; matchDecl makes the final per-declaration decision.
func (specs grepSpecs) matchFile(file *mast.File, pkg *mast.Package, absDir string) bool {
	if len(specs) == 0 {
		return true
	}
	for _, s := range specs {
		if matchSource(file, pkg, s.source, absDir) {
			return true
		}
	}
	return false
}

func (specs grepSpecs) matchDecl(fd *ast.FuncDecl, file *mast.File, pkg *mast.Package, absDir string) bool {
	if len(specs) == 0 {
		return true
	}
	for _, s := range specs {
		if s.matchDecl(fd, file, pkg, absDir) {
			return true
		}
	}
	return false
}

func (s grepSpec) matchDecl(fd *ast.FuncDecl, file *mast.File, pkg *mast.Package, absDir string) bool {
	if !matchSource(file, pkg, s.source, absDir) {
		return false
	}
	if s.field != "" {
		if fd.Recv == nil {
			return false
		}
		return matchName(mast.ReceiverTypeName(fd.Recv), s.name) &&
			matchName(fd.Name.Name, s.field)
	}
	return matchName(fd.Name.Name, s.name)
}
