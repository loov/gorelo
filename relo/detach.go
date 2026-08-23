package relo

import (
	"go/ast"
	"go/token"
	"go/version"
	"path/filepath"
	"slices"
	"strings"

	ed "github.com/loov/gorelo/edit"
	"github.com/loov/gorelo/mast"
)

// computeDetachEdits emits primitives that detach methods (converting
// to standalone functions) and attach functions (converting to
// methods). Declaration edits land in the shared edits Plan; call-site
// edits are emitted onto the same Plan. For cross-file moves, the decl
// edits sit inside the moved span and ride along with the enclosing
// Move (or carryPlanInSpans for file-move targets).
func computeDetachEdits(cc *compileCtx) {
	for _, rr := range cc.resolved {
		switch {
		case rr.Relo.Detach:
			detachMethod(cc.ix, rr, cc.reloByGroup, cc.edits, cc.imports, cc.plan)
		case rr.Relo.MethodOf != "":
			attachMethod(cc.ix, rr, cc.edits, cc.plan)
		}
	}
}

// detachMethod converts a method to a standalone function.
func detachMethod(ix *mast.Index, rr *resolvedRelo, reloByGroup map[*mast.Group]*resolvedRelo, edits *ed.Plan, imports *importSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil || fd.Recv == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find method declaration for %q", rr.Group.Name)
		return
	}

	typeParams, ok := detachTypeParams(ix, rr, fd, plan)
	if !ok {
		return
	}

	var recvParam string
	if rr.isCrossFileMove() {
		recvParam = detachRecvParamForTarget(ix, rr, fd, reloByGroup)
	} else {
		recvParam = formatRecvAsParam(fd.Recv, ix.Fset, "", "")
	}
	detachDeclEdits(ix, rr, fd, recvParam, typeParams, edits)

	if rr.isCrossFileMove() {
		recvImportPath := detachedReceiverImportPath(ix, rr, fd, reloByGroup)
		if recvImportPath != "" {
			addImportEntry(imports, ix, rr.TargetFile, importEntry{Path: recvImportPath})
		}
	}

	detachCallSites(ix, rr, edits, imports, plan)
}

// detachedReceiverImportPath returns the import path the detached
// function's target file needs to import in order to reference the
// receiver type. Returns "" when no import is needed (receiver type
// resolves to the same package as the detach target).
func detachedReceiverImportPath(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, reloByGroup map[*mast.Group]*resolvedRelo) string {
	recvDir, _ := resolvedReceiverLocation(ix, rr, fd, reloByGroup)
	if recvDir == finalDir(rr) {
		return ""
	}
	return guessImportPath(recvDir)
}

// resolvedReceiverLocation returns the directory and post-rename name
// of the receiver type, accounting for concurrent moves/renames of
// the type in the same run.
func resolvedReceiverLocation(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, reloByGroup map[*mast.Group]*resolvedRelo) (dir, newName string) {
	id := receiverTypeIdent(fd.Recv)
	if id != nil {
		if grp := ix.Group(id); grp != nil {
			if r, ok := reloByGroup[grp]; ok {
				return filepath.Dir(r.TargetFile), r.TargetName
			}
		}
	}
	return rr.SourceDir, ""
}

// receiverTypeIdent returns the *ast.Ident naming the receiver type
// (the T in `func (r *T)`, `func (r T)`, `func (r *T[P])`, or
// `func (r T[P, Q])`). Returns nil for shapes we don't rewrite.
func receiverTypeIdent(recv *ast.FieldList) *ast.Ident {
	if recv == nil || len(recv.List) == 0 {
		return nil
	}
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id
		}
	}
	return nil
}

// typeArgExprs returns the type arguments of an instantiated type
// expression (T[A] or T[A, B]), after stripping a pointer. Returns nil
// for non-instantiated types.
func typeArgExprs(t ast.Expr) []ast.Expr {
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		return []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		return x.Indices
	}
	return nil
}

// typeSpecFor returns the TypeSpec declaring the type that id refers
// to, or nil when it cannot be found in the index.
func typeSpecFor(ix *mast.Index, id *ast.Ident) *ast.TypeSpec {
	grp := ix.Group(id)
	if grp == nil {
		return nil
	}
	def := grp.DefIdent()
	if def == nil || def.File == nil {
		return nil
	}
	for _, decl := range def.File.Syntax.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name == def.Ident {
				return ts
			}
		}
	}
	return nil
}

// fieldListTypes returns one type expression per declared name in fl
// (so `[E, F any]` yields two entries), or nil for a nil list.
func fieldListTypes(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		for range f.Names {
			out = append(out, f.Type)
		}
	}
	return out
}

// detachTypeParams returns the bracketed type parameter list text for
// the detached function, or "" when it has none. The method's own type
// parameters (Go 1.27 generic methods) come first, followed by the
// receiver's type parameters with constraints copied from the type
// declaration. Method parameters go first so explicit instantiations
// at call sites stay valid after rewriting — l.Apply[string](f) becomes
// Apply[string](l, f), with the receiver's type arguments inferred from
// the receiver argument. Reports false after warning when the receiver
// type parameters cannot be reconstructed.
func detachTypeParams(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, plan *Plan) (string, bool) {
	var parts []string
	if tp := fd.Type.TypeParams; tp != nil && len(tp.List) > 0 {
		parts = append(parts, formatTypeParams(tp, ix.Fset))
	}
	args := typeArgExprs(fd.Recv.List[0].Type)
	if len(args) > 0 {
		ts := typeSpecFor(ix, receiverTypeIdent(fd.Recv))
		var constraints []ast.Expr
		if ts != nil {
			constraints = fieldListTypes(ts.TypeParams)
		}
		if len(constraints) != len(args) {
			plan.Warnings.AddAtf(rr, ix, "cannot detach %q: cannot resolve type parameters of receiver type", rr.Group.Name)
			return "", false
		}
		for i, a := range args {
			id, ok := a.(*ast.Ident)
			if !ok || id.Name == "_" {
				plan.Warnings.AddAtf(rr, ix, "cannot detach %q: receiver type argument is not a named type parameter", rr.Group.Name)
				return "", false
			}
			// ponytail: constraint text is copied verbatim from the type
			// declaration; constraints naming imports or unexported
			// identifiers of the source package are not re-qualified
			// for cross-package targets.
			parts = append(parts, id.Name+" "+nodeString(constraints[i], ix.Fset))
		}
	}
	if len(parts) == 0 {
		return "", true
	}
	return "[" + strings.Join(parts, ", ") + "]", true
}

// detachDeclEdits emits primitives onto edits that convert a method
// declaration into a standalone function. recvParam is the receiver
// text formatted as a function parameter; callers decide whether to
// qualify it with a package prefix and/or substitute a renamed base
// type. The declaration rename (if any) is handled by the rename pass.
func detachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, recvParam, typeParams string, edits *ed.Plan) {
	fset := ix.Fset
	src := fileContent(rr.File)
	path := rr.File.Path

	// Rewrite (or insert) the type parameter list.
	if tp := fd.Type.TypeParams; tp != nil {
		emitEdit(edits, path, fset.Position(tp.Opening).Offset, fset.Position(tp.Closing).Offset+1, typeParams, "detach-type-params")
	} else if typeParams != "" {
		emitEdit(edits, path, fset.Position(fd.Type.Params.Opening).Offset, fset.Position(fd.Type.Params.Opening).Offset, typeParams, "detach-type-params")
	}

	// Remove receiver: from opening paren to closing paren + trailing space.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}
	edits.Delete(ed.Span{Path: path, Start: recvOpen, End: recvEnd}, "detach-remove-recv")

	// Insert receiver as first parameter.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0
	insertText := recvParam
	if hasParams {
		insertText += ", "
	}
	edits.Insert(ed.Anchor{Path: path, Offset: paramsOpen + 1}, insertText, ed.SideBefore, "detach-insert-param")
}

// detachRecvParamForTarget returns the receiver text formatted as a
// parameter for a cross-package detach. When the receiver type is
// itself being moved or renamed in the same run, the post-operation
// name and package qualifier are substituted.
func detachRecvParamForTarget(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, reloByGroup map[*mast.Group]*resolvedRelo) string {
	recvDir, recvNewName := resolvedReceiverLocation(ix, rr, fd, reloByGroup)
	var pkgQualifier string
	if recvDir != finalDir(rr) {
		if recvImportPath := guessImportPath(recvDir); recvImportPath != "" {
			pkgQualifier = packageNameForImport(ix, recvImportPath)
		}
	}
	return formatRecvAsParam(fd.Recv, ix.Fset, pkgQualifier, recvNewName)
}

// detachCallSites rewrites call sites from s.Method(args) → Func(s, args)
// or pkg.Func(s, args) when moving cross-package. Qualification is
// based on the caller's FINAL location — if the caller is itself being
// moved to the same target package as the detached function, no
// qualifier is needed.
// detachCallSites rewrites call sites from s.Method(args) so that the
// receiver expression is removed from the selector and inserted as the
// first argument. It only performs structural edits: Delete "recv." in
// the qualifier region and Insert "recv, " as the first argument.
// Package qualification (adding "pkg." for cross-package moves) is
// handled independently by computeConsumerEdits. The ident region
// rename is handled by computeRenames.
func detachCallSites(ix *mast.Index, rr *resolvedRelo, edits *ed.Plan, imports *importSet, plan *Plan) {
	// For same-package detaches, callers in other packages need the
	// source package qualifier because the receiver expression that
	// provided implicit package scoping is being removed. For
	// cross-package detaches, computeConsumerEdits handles the target
	// qualifier independently.
	var srcQualifier string
	var srcImportPath string
	if !rr.isCrossPackageMove() && rr.File != nil {
		srcDir := rr.SourceDir
		srcImportPath = guessImportPath(srcDir)
		if srcImportPath != "" {
			srcQualifier = packageLocalName(ix, srcDir)
		}
	}

	for _, id := range rr.Group.Idents {
		if id.Kind != mast.IdentUse || id.File == nil {
			continue
		}
		sel, call := enclosingCallExpr(id.File.Syntax, id.Ident)
		if sel == nil {
			continue
		}

		filePath := id.File.Path
		fset := ix.Fset
		src := fileContent(id.File)

		xStart := fset.Position(sel.X.Pos()).Offset
		xEnd := fset.Position(sel.X.End()).Offset
		recvText := string(src[xStart:xEnd])

		selStart := fset.Position(sel.Sel.Pos()).Offset

		// Delete the qualifier region [xStart, selStart) — removes "recv.".
		emitEdit(edits, filePath, xStart, selStart, "", "detach-callsite-qualifier")

		// For same-package detaches, add the source package qualifier
		// at cross-package call sites.
		if srcQualifier != "" && id.File.Pkg != rr.File.Pkg {
			emitEdit(edits, filePath, selStart, selStart, srcQualifier+".", "detach-callsite-pkg-qualifier")
			addImportEntry(imports, ix, filePath, importEntry{Path: srcImportPath})
		}

		if call != nil {
			lparen := fset.Position(call.Lparen).Offset
			hasArgs := len(call.Args) > 0
			insertText := recvText
			if hasArgs {
				insertText += ", "
			}
			edits.Insert(ed.Anchor{Path: filePath, Offset: lparen + 1}, insertText, ed.SideBefore, "detach-callsite-recv-arg")
		} else {
			plan.Warnings.Addf(
				"method value reference to %q will change signature after detach",
				recvText+"."+rr.Group.Name)
		}
	}
}

// attachMethod converts a standalone function to a method.
func attachMethod(ix *mast.Index, rr *resolvedRelo, edits *ed.Plan, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find function declaration for %q", rr.Group.Name)
		return
	}
	if fd.Recv != nil {
		plan.Warnings.AddAtf(rr, ix, "%q is already a method", rr.Group.Name)
		return
	}
	if fd.Type.Params == nil || len(fd.Type.Params.List) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %q as method: no parameters", rr.Group.Name)
		return
	}

	firstField := fd.Type.Params.List[0]
	if _, isEllipsis := firstField.Type.(*ast.Ellipsis); isEllipsis {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %q as method: first parameter is variadic", rr.Group.Name)
		return
	}
	if len(firstField.Names) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %q as method: first parameter has no name", rr.Group.Name)
		return
	}
	if len(firstField.Names) > 1 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %q as method: first parameter field has multiple names", rr.Group.Name)
		return
	}

	recvTypeName := typeExprName(firstField.Type)
	if recvTypeName != rr.Relo.MethodOf {
		plan.Warnings.AddAtf(rr, ix,
			"cannot attach %q as method on %q: first parameter type is %q",
			rr.Group.Name, rr.Relo.MethodOf, recvTypeName)
		return
	}

	recvParams, ok := attachRecvTypeParams(ix, rr, fd, firstField, plan)
	if !ok {
		return
	}
	if tp := fd.Type.TypeParams; tp != nil && len(fieldListTypes(tp)) > len(recvParams) {
		if v := rr.File.Pkg.GoVersion; v != "" && version.Compare("go"+v, "go1.27") < 0 {
			plan.Warnings.AddAtf(rr, ix, "cannot attach %q as method: generic methods require go1.27 or later (module is go%s)", rr.Group.Name, v)
			return
		}
	}

	// Emit declaration edits unconditionally. The cross-file path
	// strips the receiver type's package qualifier when moving into
	// that type's package (self-import removal); the decl edits sit
	// inside the moved span and ride along with the enclosing Move at
	// apply time.
	unqualifyPkgPath := ""
	if rr.isCrossFileMove() {
		unqualifyPkgPath = finalImportPath(rr)
	}
	recvText := attachRecvText(rr.File, ix.Fset, fd, unqualifyPkgPath)
	attachDeclEdits(ix, rr, fd, recvText, recvParams, edits)

	attachCallSites(ix, rr, recvParams, edits)
}

// attachRecvTypeParams returns, for a function becoming a method, the
// positions within fd's type parameter list of the parameters that
// move onto the receiver (the E in `func F[E, G any](l List[E], ...)`
// → `func (l List[E]) F[G any](...)`). The rest stay on the method as
// Go 1.27 generic method type parameters. Reports false after warning
// when the receiver's type arguments are not distinct type parameters
// of fd.
func attachRecvTypeParams(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, firstField *ast.Field, plan *Plan) ([]int, bool) {
	args := typeArgExprs(firstField.Type)
	if len(args) == 0 {
		return nil, true
	}
	var names []string
	if fd.Type.TypeParams != nil {
		for _, f := range fd.Type.TypeParams.List {
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
		}
	}
	var positions []int
	for _, a := range args {
		id, ok := a.(*ast.Ident)
		pos := -1
		if ok {
			pos = slices.Index(names, id.Name)
		}
		if pos < 0 || slices.Contains(positions, pos) {
			plan.Warnings.AddAtf(rr, ix,
				"cannot attach %q as method: receiver type arguments must be distinct type parameters of the function",
				rr.Group.Name)
			return nil, false
		}
		positions = append(positions, pos)
	}
	return positions, true
}

// attachTypeParamsText returns the rewritten bracketed type parameter
// list of fd with the parameters at the given positions removed, or ""
// when none remain.
func attachTypeParamsText(fd *ast.FuncDecl, removed []int, fset *token.FileSet) string {
	kept := &ast.FieldList{}
	pos := 0
	for _, f := range fd.Type.TypeParams.List {
		nf := &ast.Field{Type: f.Type}
		for _, n := range f.Names {
			if !slices.Contains(removed, pos) {
				nf.Names = append(nf.Names, n)
			}
			pos++
		}
		if len(nf.Names) > 0 {
			kept.List = append(kept.List, nf)
		}
	}
	if len(kept.List) == 0 {
		return ""
	}
	return "[" + formatTypeParams(kept, fset) + "]"
}

// attachDeclEdits emits primitives onto edits that convert a function
// declaration into a method. recvText is the receiver formatted as the
// field inside the method's receiver parens. The declaration rename
// (if any) is handled by the rename pass on the ident region.
func attachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, recvText string, recvParams []int, edits *ed.Plan) {
	fset := ix.Fset
	path := rr.File.Path
	firstField := fd.Type.Params.List[0]

	// Drop the type parameters that moved onto the receiver.
	if tp := fd.Type.TypeParams; tp != nil && len(recvParams) > 0 {
		emitEdit(edits, path, fset.Position(tp.Opening).Offset, fset.Position(tp.Closing).Offset+1,
			attachTypeParamsText(fd, recvParams, fset), "attach-type-params")
	}

	// Insert receiver before the function name.
	nameStart := fset.Position(fd.Name.Pos()).Offset
	edits.Insert(ed.Anchor{Path: path, Offset: nameStart},
		"("+recvText+") ", ed.SideBefore, "attach-insert-recv")

	// Remove first parameter from parameter list.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	paramEnd := fset.Position(firstField.End()).Offset
	removeEnd := paramEnd
	if len(fd.Type.Params.List) > 1 {
		nextStart := fset.Position(fd.Type.Params.List[1].Pos()).Offset
		removeEnd = nextStart
	}
	edits.Delete(ed.Span{Path: path, Start: paramsOpen + 1, End: removeEnd}, "attach-remove-first-param")
}

// attachRecvText returns the receiver text for an attach declaration.
// When unqualifyPkgPath is non-empty and matches the first parameter's
// package qualifier, that qualifier is stripped (self-import removal
// when moving into the receiver type's package). The default — passing
// "" — preserves the literal source text.
func attachRecvText(file *mast.File, fset *token.FileSet, fd *ast.FuncDecl, unqualifyPkgPath string) string {
	if file == nil {
		return ""
	}
	firstField := fd.Type.Params.List[0]
	if unqualifyPkgPath != "" {
		if stripped, ok := strippedRecvText(file, firstField, unqualifyPkgPath); ok {
			return stripped
		}
	}
	paramStart := fset.Position(firstField.Pos()).Offset
	paramEnd := fset.Position(firstField.End()).Offset
	return string(file.Src[paramStart:paramEnd])
}

// strippedRecvText attempts to rewrite a first-parameter field as a
// receiver with its package qualifier removed, returning the new text
// and true when the field's type matches unqualifyPkgPath. Handles
// both value (`s srv.Server`) and pointer (`s *srv.Server`) receivers.
func strippedRecvText(file *mast.File, firstField *ast.Field, unqualifyPkgPath string) (string, bool) {
	nameStr := ""
	if len(firstField.Names) > 0 {
		nameStr = firstField.Names[0].Name + " "
	}
	typ := firstField.Type
	prefix := ""
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
		prefix = "*"
	}
	if sel, ok := typ.(*ast.SelectorExpr); ok {
		if qualIdent, ok := sel.X.(*ast.Ident); ok {
			if findImportPathForIdent(file, qualIdent.Name) == unqualifyPkgPath {
				return nameStr + prefix + sel.Sel.Name, true
			}
		}
	}
	return "", false
}

// findImportPathForIdent returns the import path associated with a package
// qualifier ident name in the given file, or "" if not found.
func findImportPathForIdent(f *mast.File, name string) string {
	if f == nil {
		return ""
	}
	for _, imp := range f.Syntax.Imports {
		localName := importLocalName(imp, importPath(imp))
		if localName == name {
			return importPath(imp)
		}
	}
	return ""
}

// attachCallSites rewrites call sites from Func(s, args) → s.Method(args).
// It edits the qualifier region [editStart, identStart) to replace `pkg.`
// or bare with `recv.`, and emits structural edits to remove the first arg.
// The ident region rename is handled by the rename pass.
func attachCallSites(ix *mast.Index, rr *resolvedRelo, recvParams []int, edits *ed.Plan) {
	for _, id := range rr.Group.Idents {
		if id.Kind != mast.IdentUse || id.File == nil {
			continue
		}

		filePath := id.File.Path
		fset := ix.Fset
		src := fileContent(id.File)

		call := enclosingCallOnly(id.File.Syntax, id.Ident)
		if call == nil || len(call.Args) == 0 {
			continue
		}

		firstArg := call.Args[0]
		argStart := fset.Position(firstArg.Pos()).Offset
		argEnd := fset.Position(firstArg.End()).Offset
		recvText := string(src[argStart:argEnd])

		identStart := fset.Position(id.Ident.Pos()).Offset
		editStart := identStart
		if id.Qualifier != nil {
			editStart = fset.Position(id.Qualifier.Pos()).Offset
		}

		// Edit qualifier region: replace `pkg.` or bare prefix with `recv.`
		emitEdit(edits, filePath, editStart, identStart, recvText+".", "attach-callsite-qualifier")

		// Drop the receiver's type arguments from an explicit
		// instantiation: Apply[int, string](l, f) → l.Apply[string](f).
		if args := typeArgExprs(call.Fun); len(args) > 0 && len(recvParams) > 0 {
			var kept []string
			for i, a := range args {
				if !slices.Contains(recvParams, i) {
					kept = append(kept, string(src[fset.Position(a.Pos()).Offset:fset.Position(a.End()).Offset]))
				}
			}
			text := ""
			if len(kept) > 0 {
				text = "[" + strings.Join(kept, ", ") + "]"
			}
			emitEdit(edits, filePath, identStart+len(id.Ident.Name), fset.Position(call.Fun.End()).Offset, text, "attach-callsite-type-args")
		}

		lparen := fset.Position(call.Lparen).Offset
		if len(call.Args) > 1 {
			secondArg := call.Args[1]
			secondStart := fset.Position(secondArg.Pos()).Offset
			edits.Delete(ed.Span{Path: filePath, Start: lparen + 1, End: secondStart}, "attach-callsite-strip-recv-arg")
		} else {
			rparen := fset.Position(call.Rparen).Offset
			edits.Delete(ed.Span{Path: filePath, Start: lparen + 1, End: rparen}, "attach-callsite-empty-args")
		}
	}
}

// findFuncDecl returns the FuncDecl whose Name matches ident.
func findFuncDecl(file *ast.File, ident *ast.Ident) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name == ident {
			return fd
		}
	}
	return nil
}

// enclosingCallExpr finds the SelectorExpr containing ident as Sel,
// and optionally the enclosing CallExpr if it's being called.
func enclosingCallExpr(file *ast.File, ident *ast.Ident) (sel *ast.SelectorExpr, call *ast.CallExpr) {
	ast.Inspect(file, func(n ast.Node) bool {
		if sel != nil {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			if s, ok := unwrapIndex(x.Fun).(*ast.SelectorExpr); ok && s.Sel == ident {
				sel = s
				call = x
				return false
			}
		case *ast.SelectorExpr:
			if x.Sel == ident {
				sel = x
				return false
			}
		}
		return true
	})
	return
}

// unwrapIndex strips an explicit instantiation (f[T] or f[T, U]) from
// a call's Fun expression, returning the instantiated expression.
func unwrapIndex(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.IndexExpr:
		return x.X
	case *ast.IndexListExpr:
		return x.X
	}
	return e
}

// enclosingCallOnly finds the CallExpr where ident is the function being called.
func enclosingCallOnly(file *ast.File, ident *ast.Ident) *ast.CallExpr {
	var result *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := unwrapIndex(call.Fun).(type) {
		case *ast.Ident:
			if fun == ident {
				result = call
				return false
			}
		case *ast.SelectorExpr:
			if fun.Sel == ident {
				result = call
				return false
			}
		}
		return true
	})
	return result
}

// formatRecvAsParam formats a receiver field list as a parameter string.
// If pkgQualifier is non-empty, the receiver type is qualified (e.g.,
// "s *pkg.Server"). If typeNewName is non-empty, the receiver type's
// base name is replaced (used when the type is being renamed in the
// same run). Pointer indirection and generic type arguments are
// preserved.
func formatRecvAsParam(recv *ast.FieldList, fset *token.FileSet, pkgQualifier, typeNewName string) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	field := recv.List[0]
	var typeStr string
	if typeNewName != "" {
		typeStr = formatTypeWithRenamedIdent(field.Type, fset, typeNewName)
	} else {
		typeStr = nodeString(field.Type, fset)
	}
	if pkgQualifier != "" {
		typeStr = qualifyTypeStr(typeStr, pkgQualifier)
	}
	if len(field.Names) > 0 {
		return field.Names[0].Name + " " + typeStr
	}
	return typeStr
}

// formatTypeWithRenamedIdent serializes a receiver type expression,
// replacing the innermost type-name Ident with newName. Pointer wraps
// and generic type-argument lists are preserved as-is.
func formatTypeWithRenamedIdent(expr ast.Expr, fset *token.FileSet, newName string) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return "*" + formatTypeWithRenamedIdent(e.X, fset, newName)
	case *ast.Ident:
		return newName
	case *ast.IndexExpr:
		return newName + "[" + nodeString(e.Index, fset) + "]"
	case *ast.IndexListExpr:
		parts := make([]string, 0, len(e.Indices))
		for _, idx := range e.Indices {
			parts = append(parts, nodeString(idx, fset))
		}
		return newName + "[" + strings.Join(parts, ", ") + "]"
	}
	return nodeString(expr, fset)
}

// qualifyTypeStr prepends a package qualifier to a type string,
// handling pointer indirection (e.g., "*Server" → "*pkg.Server").
func qualifyTypeStr(typeStr, pkg string) string {
	if len(typeStr) > 0 && typeStr[0] == '*' {
		return "*" + pkg + "." + typeStr[1:]
	}
	return pkg + "." + typeStr
}

// typeExprName returns the base type name from a type expression,
// stripping pointer indirection and package qualifiers.
func typeExprName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	expr = unwrapIndex(expr)
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}
