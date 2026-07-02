package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zeebo/clingy"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/relo"
	"github.com/loov/gorelo/rules"
)

// cmdCoverage measures which methods of a target type are transitively
// reached from a user-supplied set of entry-point declarations. For each
// entry it reports the set of target methods reachable from that entry;
// optionally inverted with --by-method.
//
// Entry specifiers use the standard gorelo grammar (rules.ParseItem) and
// additionally accept '*' and '?' glob wildcards in both the source and
// name parts.

type cmdCoverage struct {
	jsonOutput bool
	byMethod   bool
	forType    string
	args       []string
}

func (c *cmdCoverage) Setup(params clingy.Parameters) {
	c.jsonOutput = params.Flag("json", "emit JSON output", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.byMethod = params.Flag("by-method", "group by target method instead of by entry", false,
		clingy.Transform(strconv.ParseBool), clingy.Boolean).(bool)
	c.forType = params.Flag("for", "target type whose methods to measure (e.g. ./pkg.DB)", "").(string)
	c.args = params.Arg("entry",
		"entry-point specifier (e.g. Test*, *_test.go:Test*, ./pkg.Func); '*' and '?' globs supported",
		clingy.Repeated).([]string)
}

func (c *cmdCoverage) Execute(ctx context.Context) error {
	if c.forType == "" {
		return fmt.Errorf("--for is required")
	}
	if len(c.args) == 0 {
		return fmt.Errorf("at least one entry specifier is required")
	}

	ix, err := mast.Load(&mast.Config{Dir: "."}, "./...")
	if err != nil {
		return fmt.Errorf("loading packages: %w", err)
	}

	absDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	typeGrp, methods, filter, err := resolveTargetMethods(ix, absDir, c.forType)
	if err != nil {
		return err
	}
	targetSet := make(map[*mast.Group]bool, len(methods))
	for _, m := range methods {
		targetSet[m] = true
	}

	entries, err := collectEntries(ix, absDir, c.args)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no entries matched %v", c.args)
	}

	type entryHit struct {
		grp     *mast.Group
		methods []*mast.Group
	}
	hits := make([]entryHit, 0, len(entries))
	for _, e := range entries {
		reached := reachableTargets(ix, e, targetSet)
		ms := make([]*mast.Group, 0, len(reached))
		for m := range reached {
			ms = append(ms, m)
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].Name < ms[j].Name })
		hits = append(hits, entryHit{grp: e, methods: ms})
	}

	sort.Slice(hits, func(i, j int) bool {
		if len(hits[i].methods) != len(hits[j].methods) {
			return len(hits[i].methods) < len(hits[j].methods)
		}
		return hits[i].grp.Name < hits[j].grp.Name
	})

	result := coverageResult{
		Type:    typeGrp.Name,
		Package: typeGrp.Pkg,
		Filter:  filter,
		// non-nil slices emit [] rather than null in --json output
		Methods: make([]coverageMethod, 0, len(methods)),
		Entries: make([]coverageEntry, 0, len(hits)),
	}

	methodHits := make(map[*mast.Group][]*mast.Group, len(methods))
	for _, h := range hits {
		for _, m := range h.methods {
			methodHits[m] = append(methodHits[m], h.grp)
		}
	}

	for _, m := range methods {
		def := m.DefIdent()
		entry := coverageMethod{
			Name:    m.Name,
			Entries: make([]coverageLoc, 0, len(methodHits[m])),
		}
		if def != nil && def.File != nil {
			entry.File = relPath(absDir, def.File.Path)
			entry.Line = ix.Fset.Position(def.Ident.Pos()).Line
		}
		callers := methodHits[m]
		sort.Slice(callers, func(i, j int) bool { return callers[i].Name < callers[j].Name })
		for _, t := range callers {
			entry.Entries = append(entry.Entries, groupLoc(ix, absDir, t))
		}
		result.Methods = append(result.Methods, entry)
	}

	for _, h := range hits {
		entry := coverageEntry{
			Name:    h.grp.Name,
			Methods: make([]string, 0, len(h.methods)),
		}
		loc := groupLoc(ix, absDir, h.grp)
		entry.File = loc.File
		entry.Line = loc.Line
		for _, m := range h.methods {
			entry.Methods = append(entry.Methods, m.Name)
		}
		result.Entries = append(result.Entries, entry)
	}

	w := clingy.Stdout(ctx)
	if c.jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	if c.byMethod {
		printCoverageByMethod(w, result)
	} else {
		printCoverageByEntry(w, result)
	}
	return nil
}

type coverageResult struct {
	Type    string           `json:"type"`
	Package string           `json:"package"`
	Filter  string           `json:"filter,omitempty"`
	Methods []coverageMethod `json:"methods"`
	Entries []coverageEntry  `json:"entries"`
}

type coverageMethod struct {
	Name    string        `json:"name"`
	File    string        `json:"file,omitempty"`
	Line    int           `json:"line,omitempty"`
	Entries []coverageLoc `json:"entries"`
}

type coverageEntry struct {
	Name    string   `json:"name"`
	File    string   `json:"file,omitempty"`
	Line    int      `json:"line,omitempty"`
	Methods []string `json:"methods"`
}

type coverageLoc struct {
	Name string `json:"name"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
}

func groupLoc(ix *mast.Index, absDir string, grp *mast.Group) coverageLoc {
	loc := coverageLoc{Name: grp.Name}
	def := grp.DefIdent()
	if def != nil && def.File != nil {
		loc.File = relPath(absDir, def.File.Path)
		loc.Line = ix.Fset.Position(def.Ident.Pos()).Line
	}
	return loc
}

func printCoverageByEntry(w io.Writer, r coverageResult) {
	reaching := 0
	for _, e := range r.Entries {
		if len(e.Methods) > 0 {
			reaching++
		}
	}
	fmt.Fprintf(w, "%s — %d of %d entries reach %d of %d methods\n\n",
		qualifyType(r), reaching, len(r.Entries),
		countNonEmptyMethods(r.Methods), len(r.Methods))

	if reaching == 0 {
		fmt.Fprintln(w, "  (no entries reach any methods)")
		return
	}
	for _, e := range r.Entries {
		if len(e.Methods) == 0 {
			continue
		}
		loc := e.File
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.File, e.Line)
		}
		fmt.Fprintf(w, "  %-40s %-30s %s\n", e.Name, loc, strings.Join(e.Methods, ", "))
	}
}

func printCoverageByMethod(w io.Writer, r coverageResult) {
	reaching := 0
	for _, e := range r.Entries {
		if len(e.Methods) > 0 {
			reaching++
		}
	}
	fmt.Fprintf(w, "%s — %d of %d methods reached by %d of %d entries\n\n",
		qualifyType(r),
		countNonEmptyMethods(r.Methods), len(r.Methods),
		reaching, len(r.Entries))

	methods := make([]coverageMethod, len(r.Methods))
	copy(methods, r.Methods)
	sort.SliceStable(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })

	for i, m := range methods {
		if i > 0 {
			fmt.Fprintln(w)
		}
		loc := m.File
		if m.Line > 0 {
			loc = fmt.Sprintf("%s:%d", m.File, m.Line)
		}
		n := len(m.Entries)
		word := "entries"
		if n == 1 {
			word = "entry"
		}
		fmt.Fprintf(w, "  %-30s %d %s   %s\n", m.Name, n, word, loc)
		for _, e := range m.Entries {
			eloc := e.File
			if e.Line > 0 {
				eloc = fmt.Sprintf("%s:%d", e.File, e.Line)
			}
			fmt.Fprintf(w, "    %-40s %s\n", e.Name, eloc)
		}
	}
}

func qualifyType(r coverageResult) string {
	name := r.Type
	if r.Package != "" {
		name = r.Package + "." + r.Type
	}
	if r.Filter != "" {
		name += "#" + r.Filter
	}
	return name
}

func countNonEmptyMethods(ms []coverageMethod) int {
	n := 0
	for _, m := range ms {
		if len(m.Entries) > 0 {
			n++
		}
	}
	return n
}

// resolveTargetMethods locates the target type named by --for and returns
// the set of its methods that should serve as coverage targets, along with
// an optional method-name filter (the substring after '#', if any) for
// display purposes.
//
// Accepted forms:
//
//	DB               every method of DB
//	DB#Get           the single method DB.Get
//	DB#Get*          every method of DB whose name matches the glob
//
// Globs in the type name or source qualifier are not accepted.
func resolveTargetMethods(ix *mast.Index, absDir, arg string) (*mast.Group, []*mast.Group, string, error) {
	item, err := rules.ParseItem(arg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parsing --for %q: %w", arg, err)
	}
	if hasGlob(item.Name) || hasGlob(item.Source) {
		return nil, nil, "", fmt.Errorf("--for does not accept globs in the type or source (got %q)", arg)
	}
	if err := validateGlob(item.Field); err != nil {
		return nil, nil, "", fmt.Errorf("parsing --for %q: %w", arg, err)
	}
	source := relo.ResolveSource(ix, item.Source, absDir)
	id := ix.FindDef(item.Name, source)
	if id == nil {
		src := ""
		if item.Source != "" {
			src = " in " + item.Source
		}
		return nil, nil, "", fmt.Errorf("could not find %q%s", item.Name, src)
	}
	grp := ix.Group(id)
	if grp == nil {
		return nil, nil, "", fmt.Errorf("no group for %q", arg)
	}
	if grp.Kind != mast.ObjectTypeName {
		return nil, nil, "", fmt.Errorf("%q is not a type (kind %s)", arg, objectKindString(grp.Kind))
	}

	all := collectTypeMethods(ix, grp)
	if len(all) == 0 {
		return nil, nil, "", fmt.Errorf("type %q has no methods", grp.Name)
	}

	if item.Field == "" {
		return grp, all, "", nil
	}

	var matched []*mast.Group
	for _, m := range all {
		if matchName(m.Name, item.Field) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		return nil, nil, "", fmt.Errorf("no methods of %q match %q", grp.Name, item.Field)
	}
	return grp, matched, item.Field, nil
}

// collectEntries enumerates package-scope FuncDecls (functions and methods)
// whose name and source match each user-supplied specifier. Both name and
// source parts may contain '*' and '?' globs.
func collectEntries(ix *mast.Index, absDir string, args []string) ([]*mast.Group, error) {
	seen := make(map[*mast.Group]bool)
	var entries []*mast.Group
	for _, arg := range args {
		item, err := rules.ParseItem(arg)
		if err != nil {
			return nil, fmt.Errorf("parsing %q: %w", arg, err)
		}
		if item.Field != "" {
			return nil, fmt.Errorf("entry specifier must not include a field (got %q)", arg)
		}
		if item.Name == "" {
			return nil, fmt.Errorf("entry specifier %q missing name", arg)
		}
		if err := validateGlob(item.Name); err != nil {
			return nil, fmt.Errorf("parsing %q: %w", arg, err)
		}
		if err := validateGlob(item.Source); err != nil {
			return nil, fmt.Errorf("parsing %q: %w", arg, err)
		}

		// Pre-resolve a non-glob source so "./pkg" turns into a package path.
		resolvedSource := item.Source
		if !hasGlob(item.Source) {
			resolvedSource = relo.ResolveSource(ix, item.Source, absDir)
		}

		for _, pkg := range ix.Pkgs {
			for _, file := range pkg.Files {
				if !matchSource(file, pkg, resolvedSource, absDir) {
					continue
				}
				for _, decl := range file.Syntax.Decls {
					fd, ok := decl.(*ast.FuncDecl)
					if !ok {
						continue
					}
					if !matchName(fd.Name.Name, item.Name) {
						continue
					}
					g := ix.Group(fd.Name)
					if g == nil || seen[g] {
						continue
					}
					seen[g] = true
					entries = append(entries, g)
				}
			}
		}
	}
	return entries, nil
}

func hasGlob(s string) bool { return strings.ContainsAny(s, "*?") }

// validateGlob rejects malformed glob patterns up front so that match sites
// can safely ignore the path.Match error. Patterns without wildcards are
// matched literally and need no validation.
func validateGlob(pattern string) error {
	if !hasGlob(pattern) {
		return nil
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	return nil
}

// matchName reports whether name matches pattern. With no wildcards the
// match is exact; otherwise '*' and '?' globs are honored via path.Match.
func matchName(name, pattern string) bool {
	if !hasGlob(pattern) {
		return name == pattern
	}
	ok, _ := path.Match(pattern, name) // pattern validated at parse time by validateGlob
	return ok
}

// matchSource reports whether a file/package matches the source pattern.
// An empty pattern matches everything. A pattern without globs is treated
// as a file suffix or a package import path. A pattern with globs matches
// against the file path: against the basename if it contains no '/',
// otherwise against the relative path from absDir.
func matchSource(file *mast.File, pkg *mast.Package, pattern, absDir string) bool {
	if pattern == "" {
		return true
	}
	if !hasGlob(pattern) {
		if pkg.Path == pattern {
			return true
		}
		return fileHasSuffix(file.Path, pattern)
	}
	if strings.Contains(pattern, "/") {
		rel, err := filepath.Rel(absDir, file.Path)
		if err != nil {
			rel = file.Path
		}
		rel = filepath.ToSlash(rel)
		ok, _ := path.Match(pattern, rel) // pattern validated at parse time by validateGlob
		return ok
	}
	base := filepath.Base(file.Path)
	ok, _ := path.Match(pattern, base) // pattern validated at parse time by validateGlob
	return ok
}

// fileHasSuffix reports whether filePath ends with suffix at a path boundary.
// Mirrors mast's internal fileMatchesSource so cmd-side matching behaves the
// same as the library's lookups.
func fileHasSuffix(filePath, suffix string) bool {
	s := filepath.FromSlash(suffix)
	if filePath == s {
		return true
	}
	return strings.HasSuffix(filePath, string(filepath.Separator)+s)
}

// collectTypeMethods returns groups for every method declared on typeGrp's
// type, scoped to the package where typeGrp is defined. Both concrete
// (FuncDecl receiver) methods and interface methods are enumerated.
// Embedded interfaces are not expanded — only leaf method names declared
// directly on the interface are returned.
func collectTypeMethods(ix *mast.Index, typeGrp *mast.Group) []*mast.Group {
	var methods []*mast.Group
	seen := make(map[*mast.Group]bool)

	for _, pkg := range ix.Pkgs {
		if pkg.Path != typeGrp.Pkg {
			continue
		}
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil {
					continue
				}
				if mast.ReceiverTypeName(fd.Recv) != typeGrp.Name {
					continue
				}
				g := ix.Group(fd.Name)
				if g == nil || seen[g] {
					continue
				}
				seen[g] = true
				methods = append(methods, g)
			}
		}
	}

	if it := findInterfaceType(typeGrp); it != nil && it.Methods != nil {
		for _, field := range it.Methods.List {
			for _, name := range field.Names {
				g := ix.Group(name)
				if g == nil || seen[g] {
					continue
				}
				seen[g] = true
				methods = append(methods, g)
			}
		}
	}

	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
	return methods
}

// findInterfaceType returns the InterfaceType node for typeGrp if its
// declaration is `type T interface { ... }`, or nil otherwise.
func findInterfaceType(typeGrp *mast.Group) *ast.InterfaceType {
	def := typeGrp.DefIdent()
	if def == nil || def.File == nil {
		return nil
	}
	for _, decl := range def.File.Syntax.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name != def.Ident {
				continue
			}
			it, _ := ts.Type.(*ast.InterfaceType)
			return it
		}
	}
	return nil
}

// reachableTargets returns the subset of targets that are transitively
// reachable from start by walking the bodies of called functions and
// methods.
func reachableTargets(ix *mast.Index, start *mast.Group, targets map[*mast.Group]bool) map[*mast.Group]bool {
	visited := make(map[*mast.Group]bool)
	result := make(map[*mast.Group]bool)

	var visit func(g *mast.Group)
	visit = func(g *mast.Group) {
		if visited[g] {
			return
		}
		visited[g] = true

		if targets[g] {
			result[g] = true
		}

		def := g.DefIdent()
		if def == nil || def.File == nil {
			return
		}
		node := findDeclNode(def.File.Syntax, def.Ident)
		if node == nil {
			return
		}

		ast.Inspect(node, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			ref := ix.Group(ident)
			if ref == nil || !ref.IsPackageScope() {
				return true
			}
			if targets[ref] {
				result[ref] = true
			}
			if ref.Kind == mast.ObjectFunc || ref.Kind == mast.ObjectMethod {
				visit(ref)
			}
			return true
		})
	}

	visit(start)
	return result
}
