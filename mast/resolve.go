package mast

import (
	"fmt"
	"go/ast"
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
	// Process Defs (skip embedded fields — handled below).
	for ident, obj := range info.Defs {
		if shouldSkip(obj) {
			continue
		}
		if v, ok := obj.(*types.Var); ok && v.Embedded() {
			continue
		}
		file := fileForIdent(ident, fileMap)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, ident, file, Def)
	}

	// Process Uses.
	for ident, obj := range info.Uses {
		if shouldSkip(obj) {
			continue
		}
		file := fileForIdent(ident, fileMap)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, ident, file, Use)
	}

	// Process Selections (field/method access via selector expressions).
	for sel, selection := range info.Selections {
		obj := selection.Obj()
		if shouldSkip(obj) {
			continue
		}
		file := fileForIdent(sel.Sel, fileMap)
		if file == nil {
			continue
		}

		key := objectKeyFor(obj)
		grp := findOrCreateGroup(ix, key, obj)
		addIdent(ix, grp, sel.Sel, file, Use)
	}

	// Process embedded fields: link the embedded field ident to the
	// type name's group as well.
	for ident, obj := range info.Defs {
		v, ok := obj.(*types.Var)
		if !ok || !v.Embedded() {
			continue
		}
		file := fileForIdent(ident, fileMap)
		if file == nil {
			continue
		}

		typeName := embeddedTypeName(v.Type())
		if typeName == nil {
			continue
		}

		key := objectKeyFor(typeName)
		grp := findOrCreateGroup(ix, key, typeName)
		addIdent(ix, grp, ident, file, Use)
	}
}

// objectKeyFor computes the unique key for a types.Object.
func objectKeyFor(obj types.Object) objectKey {
	key := objectKey{
		Name: obj.Name(),
	}

	if pkg := obj.Pkg(); pkg != nil {
		key.PkgPath = pkg.Path()
	}

	switch o := obj.(type) {
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
			key.Receiver = fieldOwnerName(o)
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

// fileForIdent finds the File that contains ident.
func fileForIdent(ident *ast.Ident, fileMap map[*ast.File]*File) *File {
	for astFile, file := range fileMap {
		if astFile.FileStart <= ident.Pos() && ident.Pos() <= astFile.FileEnd {
			return file
		}
	}
	return nil
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

func addIdent(ix *Index, grp *Group, ident *ast.Ident, file *File, kind IdentKind) {
	// Deduplicate by pointer identity (same ident may appear in multiple passes).
	if existing, ok := ix.groups[ident]; ok {
		if existing != grp {
			mergeGroups(ix, existing, grp)
		}
		return
	}

	id := &Ident{
		Ident: ident,
		File:  file,
		Kind:  kind,
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

// fieldOwnerName returns the name of the struct type that owns the given field.
// It searches the field's package scope for a named struct type containing
// this field. Returns "" if the owner cannot be determined (e.g., fields
// in anonymous struct types).
func fieldOwnerName(field *types.Var) string {
	pkg := field.Pkg()
	if pkg == nil {
		return ""
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		t := tn.Type().Underlying()
		st, ok := t.(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			f := st.Field(i)
			if f == field {
				return tn.Name()
			}
			// For generic type instantiations, the field object may differ
			// from the origin type's field. Match by position instead.
			if f.Pos() == field.Pos() && f.Name() == field.Name() {
				return tn.Name()
			}
		}
	}
	return ""
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
