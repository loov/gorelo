package mast

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
)

// objectKey uniquely identifies a logical entity across type-check passes.
type objectKey struct {
	PkgPath  string
	Name     string
	Receiver string // non-empty for methods and fields
	Scope    string // non-empty for local (non-package-scope) variables
}

// shouldSkip returns true if the object should not be tracked.
func shouldSkip(obj types.Object) bool {
	if obj == nil {
		return true
	}
	if obj.Name() == "_" {
		return true
	}
	if isBuiltinOrUniverse(obj) {
		return true
	}
	return false
}

// resolveInfo processes a single type-check pass's Info, building
// and populating groups in the index.
func resolveInfo(ix *Index, info *types.Info, fileMap map[*ast.File]*File) {
	// Build O(1) filename lookup for fileForIdent.
	byName := filesByName(fileMap)

	// Build a map from Sel ident to its package qualifier ident
	// for expressions like pkg.Name.
	qualifiers := buildQualifierMap(info, fileMap)
	fields := newFieldOwnerCache()

	// Process Defs (skip embedded fields — handled below).
	for ident, obj := range info.Defs {
		if shouldSkip(obj) {
			continue
		}
		if v, ok := obj.(*types.Var); ok && v.Embedded() {
			continue
		}
		file := fileForIdent(ident, ix.Fset, byName)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj, fields)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, ident, qualifiers[ident], file, Def)
	}

	// Process Uses.
	for ident, obj := range info.Uses {
		if shouldSkip(obj) {
			continue
		}
		file := fileForIdent(ident, ix.Fset, byName)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj, fields)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, ident, qualifiers[ident], file, Use)
	}

	// Process Selections (field/method access via selector expressions).
	for sel, selection := range info.Selections {
		obj := selection.Obj()
		if shouldSkip(obj) {
			continue
		}
		file := fileForIdent(sel.Sel, ix.Fset, byName)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj, fields)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, sel.Sel, qualifiers[sel.Sel], file, Use)
	}

	// Process type-switch guards. go/types records the implicit
	// binding for `v := expr.(type)` in info.Implicits keyed by each
	// case clause, rather than in info.Defs for the guard ident
	// itself. Register the guard ident as a Def so rewrites that
	// iterate a group's Def idents see the binding.
	processTypeSwitchGuards(ix, info, fileMap, qualifiers, byName, fields)

	// Process embedded fields: link the embedded field ident to the
	// type name's group as well.
	for ident, obj := range info.Defs {
		v, ok := obj.(*types.Var)
		if !ok || !v.Embedded() {
			continue
		}
		file := fileForIdent(ident, ix.Fset, byName)
		if file == nil {
			continue
		}

		typeName := embeddedTypeName(v.Type())
		if typeName == nil {
			continue
		}

		key := objectKeyFor(typeName, fields)
		grp := findOrCreateGroup(ix, key, typeName)
		addIdent(ix, grp, ident, qualifiers[ident], file, Use)
	}
}

// processTypeSwitchGuards registers the guard ident of every
// *ast.TypeSwitchStmt as a Def linked to the case-clause implicit
// object (all case implicits share a position — the guard ident's
// pos — so they collapse into one group via objectKeyFor's position-
// based Scope key).
func processTypeSwitchGuards(ix *Index, info *types.Info, fileMap map[*ast.File]*File, qualifiers map[*ast.Ident]*ast.Ident, byName map[string]*File, fields *fieldOwnerCache) {
	for astFile := range fileMap {
		ast.Inspect(astFile, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSwitchStmt)
			if !ok {
				return true
			}
			guardIdent := typeSwitchGuardIdent(ts)
			if guardIdent == nil {
				return true
			}
			var obj types.Object
			if ts.Body != nil {
				for _, stmt := range ts.Body.List {
					cc, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					if o := info.Implicits[cc]; o != nil {
						obj = o
						break
					}
				}
			}
			if obj == nil || shouldSkip(obj) {
				return true
			}
			file := fileForIdent(guardIdent, ix.Fset, byName)
			if file == nil {
				return true
			}
			key := objectKeyFor(obj, fields)
			grp := findOrCreateGroup(ix, key, obj)
			addIdent(ix, grp, guardIdent, qualifiers[guardIdent], file, Def)
			return true
		})
	}
}

// typeSwitchGuardIdent returns the ident on the LHS of a
// TypeSwitchStmt's assign, or nil when the switch has no bound
// name (e.g. `switch v.(type) { ... }`).
func typeSwitchGuardIdent(ts *ast.TypeSwitchStmt) *ast.Ident {
	assign, ok := ts.Assign.(*ast.AssignStmt)
	if !ok || len(assign.Lhs) == 0 {
		return nil
	}
	id, ok := assign.Lhs[0].(*ast.Ident)
	if !ok {
		return nil
	}
	return id
}

// buildQualifierMap walks the AST to find selector expressions where
// X is a package name (e.g. pkg.Type). It returns a map from the Sel
// ident to the package qualifier ident.
func buildQualifierMap(info *types.Info, fileMap map[*ast.File]*File) map[*ast.Ident]*ast.Ident {
	qualifiers := map[*ast.Ident]*ast.Ident{}
	for astFile := range fileMap {
		ast.Inspect(astFile, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			xIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Check if X resolves to a package name.
			if obj, exists := info.Uses[xIdent]; exists {
				if _, isPkg := obj.(*types.PkgName); isPkg {
					qualifiers[sel.Sel] = xIdent
				}
			}
			return true
		})
	}
	return qualifiers
}

// objectKeyFor computes the unique key for a types.Object.
func objectKeyFor(obj types.Object, fields *fieldOwnerCache) objectKey {
	key := objectKey{
		Name: obj.Name(),
	}

	if pkg := obj.Pkg(); pkg != nil {
		key.PkgPath = pkg.Path()
	}

	switch o := obj.(type) {
	case *types.TypeName:
		// Generic type parameters and locally-declared named types share
		// the same name with package-level types across the program
		// (e.g. `T` in `func F[T any]()` vs `type T int`). Distinguish
		// them by declaring position when they are not at package scope.
		if isLocalScope(o) {
			key.Scope = fmt.Sprintf("%d", o.Pos())
		}
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok {
			if recv := sig.Recv(); recv != nil {
				key.Receiver = baseTypeName(recv.Type())
			}
		}
		// Go allows multiple init() functions per package/file.
		// Distinguish them by position.
		if o.Name() == "init" {
			key.Scope = fmt.Sprintf("%d", o.Pos())
		}
	case *types.Var:
		if o.IsField() {
			key.Receiver = fields.lookup(o)
			if key.Receiver == "" {
				// Anonymous struct field or generic instantiation field:
				// use declaring position to avoid collisions.
				key.Scope = fmt.Sprintf("%d", o.Pos())
			}
		} else if isLocalScope(o) {
			key.Scope = fmt.Sprintf("%d", o.Pos())
		}
	case *types.Const:
		if isLocalScope(o) {
			key.Scope = fmt.Sprintf("%d", o.Pos())
		}
	case *types.Label:
		key.Scope = fmt.Sprintf("%d", o.Pos())
	case *types.PkgName:
		// Import aliases are file-scoped. Two files with the same alias
		// for different packages must not merge.
		key.Scope = fmt.Sprintf("%d", o.Pos())
	}

	return key
}

// isBuiltinOrUniverse returns true if obj is a builtin or universe-scope object.
func isBuiltinOrUniverse(obj types.Object) bool {
	if obj.Pkg() == nil {
		return true
	}
	_, isBuiltin := obj.(*types.Builtin)
	return isBuiltin
}

// fileForIdent finds the File that contains ident using the FileSet for O(1) lookup.
func fileForIdent(ident *ast.Ident, fset *token.FileSet, byName map[string]*File) *File {
	if f := fset.File(ident.Pos()); f != nil {
		return byName[f.Name()]
	}
	return nil
}

// filesByName builds a lookup map from filename to *File.
func filesByName(fileMap map[*ast.File]*File) map[string]*File {
	m := make(map[string]*File, len(fileMap))
	for _, f := range fileMap {
		m[f.Path] = f
	}
	return m
}

func findOrCreateGroup(ix *Index, key objectKey, obj types.Object) *Group {
	if grp, ok := ix.groupsByKey[key]; ok {
		return grp
	}

	grp := &Group{
		Name: key.Name,
		Kind: objectKindFor(obj),
		Pkg:  key.PkgPath,
	}
	ix.groupsByKey[key] = grp
	return grp
}

func addIdent(ix *Index, grp *Group, ident *ast.Ident, qualifier *ast.Ident, file *File, kind IdentKind) {
	// Deduplicate by pointer identity (same ident may appear in multiple passes).
	if existing, ok := ix.groups[ident]; ok {
		if existing != grp {
			mergeGroups(ix, existing, grp)
		}
		return
	}

	id := &Ident{
		Ident:     ident,
		Qualifier: qualifier,
		File:      file,
		Kind:      kind,
	}
	grp.Idents = append(grp.Idents, id)
	ix.groups[ident] = grp
}

// mergeGroups merges src into dst, updating all ident and key mappings.
func mergeGroups(ix *Index, dst, src *Group) {
	if dst == src {
		return
	}
	for _, id := range src.Idents {
		ix.groups[id.Ident] = dst
	}
	dst.Idents = append(dst.Idents, src.Idents...)
	src.Idents = nil

	for k, g := range ix.groupsByKey {
		if g == src {
			ix.groupsByKey[k] = dst
		}
	}
}

// objectKindFor classifies a types.Object.
func objectKindFor(obj types.Object) ObjectKind {
	switch o := obj.(type) {
	case *types.TypeName:
		return TypeName
	case *types.Func:
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			return Method
		}
		return Func
	case *types.Var:
		if o.IsField() {
			return Field
		}
		return Var
	case *types.Const:
		return Const
	case *types.PkgName:
		return PackageName
	case *types.Label:
		return Label
	default:
		return Unknown
	}
}

// embeddedTypeName returns the *types.TypeName for an embedded field's type.
func embeddedTypeName(t types.Type) *types.TypeName {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj()
	}
	return nil
}

// isLocalScope returns true if the object is declared in a local scope
// (not at package level). Local variables, constants, etc. need position-based
// keys to avoid merging same-named locals from different functions.
func isLocalScope(obj types.Object) bool {
	parent := obj.Parent()
	if parent == nil {
		return false
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	return parent != pkg.Scope()
}

// fieldOwnerCache caches the mapping from struct fields to their owning
// type name, per package scope. This avoids repeatedly calling Scope.Names()
// (which allocates and sorts) for every field lookup.
type fieldOwnerCache struct {
	byScope map[*types.Scope]map[fieldKey]string
}

type fieldKey struct {
	pos  token.Pos
	name string
}

func newFieldOwnerCache() *fieldOwnerCache {
	return &fieldOwnerCache{byScope: map[*types.Scope]map[fieldKey]string{}}
}

func (c *fieldOwnerCache) lookup(field *types.Var) string {
	pkg := field.Pkg()
	if pkg == nil {
		return ""
	}
	scope := pkg.Scope()

	m, ok := c.byScope[scope]
	if !ok {
		m = map[fieldKey]string{}
		var walk func(parent string, st *types.Struct)
		walk = func(parent string, st *types.Struct) {
			for f := range st.Fields() {
				m[fieldKey{pos: f.Pos(), name: f.Name()}] = parent
				if inner, ok := f.Type().Underlying().(*types.Struct); ok {
					walk(parent+"."+f.Name(), inner)
				}
			}
		}
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			tn, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			st, ok := tn.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			walk(tn.Name(), st)
		}
		c.byScope[scope] = m
	}

	return m[fieldKey{pos: field.Pos(), name: field.Name()}]
}

// resolveUntracked links idents that were not resolved during type-checking
// to matching package-level groups. This handles cross-partition references
// caused by build-tag partitioning: when a name is defined in one build
// variant and used in another, the type checker in the using variant can't
// resolve it.
//
// We use ast.File.Unresolved, which the parser populates with identifiers
// it couldn't resolve within the file (cross-file references, built-ins).
// After type-checking, many are resolved and tracked in groups; the ones
// that remain untracked are cross-partition references we link here.
func resolveUntracked(ix *Index) {
	for _, pkg := range ix.Pkgs {
		// Build a lookup of package-level groups by name.
		pkgGroups := map[string]*Group{}
		for key, grp := range ix.groupsByKey {
			if key.PkgPath == pkg.Path && key.Receiver == "" && key.Scope == "" {
				pkgGroups[key.Name] = grp
			}
		}
		if len(pkgGroups) == 0 {
			continue
		}

		for _, file := range pkg.Files {
			for _, ident := range file.Syntax.Unresolved {
				if _, tracked := ix.groups[ident]; tracked {
					continue
				}
				grp, ok := pkgGroups[ident.Name]
				if !ok {
					continue
				}
				addIdent(ix, grp, ident, nil, file, Use)
			}
		}
	}
}

// baseTypeName returns a string representation of the base type for a receiver.
func baseTypeName(t types.Type) string {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return t.String()
}
