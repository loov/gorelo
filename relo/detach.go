package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"
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
func computeDetachEdits(ix *mast.Index, resolved []*resolvedRelo, edits *ed.Plan, imports *importSet, plan *Plan) {
	for _, rr := range resolved {
		switch {
		case rr.Relo.Detach:
			detachMethod(ix, rr, resolved, edits, imports, plan)
		case rr.Relo.MethodOf != "":
			attachMethod(ix, rr, edits, plan)
		}
	}
}

// detachMethod converts a method to a standalone function.
func detachMethod(ix *mast.Index, rr *resolvedRelo, resolved []*resolvedRelo, edits *ed.Plan, imports *importSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil || fd.Recv == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find method declaration for %s", rr.Group.Name)
		return
	}

	// Emit declaration edits unconditionally. The cross-file path uses
	// a package-qualified recvParam so the receiver type compiles in
	// the target package; the decl edits sit inside the moved span and
	// ride along with the enclosing Move at apply time.
	//
	// The declaration rename (Method → TargetName) is NOT emitted here;
	// the rename pass handles it uniformly for all groups.
	var recvParam string
	if rr.isCrossFileMove() {
		recvParam = detachRecvParamForTarget(ix, rr, fd, resolved)
	} else {
		recvParam = formatRecvAsParam(fd.Recv, ix.Fset, "", "")
	}
	detachDeclEdits(ix, rr, fd, recvParam, edits)

	// For cross-package moves, the detached function's parameter references
	// the receiver type. Add the import for the receiver type's final
	// location — which may differ from the source package when the type
	// itself is being moved in the same run.
	if rr.isCrossFileMove() {
		recvImportPath := detachedReceiverImportPath(ix, rr, fd, resolved)
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
func detachedReceiverImportPath(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo) string {
	recvDir, _ := resolvedReceiverLocation(ix, rr, fd, resolved)
	if recvDir == finalDir(rr) {
		return ""
	}
	return guessImportPath(recvDir)
}

// resolvedReceiverLocation returns the directory and post-rename name
// of the receiver type, accounting for concurrent moves/renames of
// the type in the same run.
func resolvedReceiverLocation(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo) (dir, newName string) {
	id := receiverTypeIdent(fd.Recv)
	if id != nil {
		if grp := ix.Group(id); grp != nil {
			for _, r := range resolved {
				if r.Group == grp {
					return filepath.Dir(r.TargetFile), r.TargetName
				}
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

// detachDeclEdits emits primitives onto edits that convert a method
// declaration into a standalone function. recvParam is the receiver
// text formatted as a function parameter; callers decide whether to
// qualify it with a package prefix and/or substitute a renamed base
// type. The declaration rename (if any) is handled by the rename pass.
func detachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, recvParam string, edits *ed.Plan) {
	fset := ix.Fset
	src := fileContent(rr.File)
	path := rr.File.Path

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
	edits.Insert(ed.Anchor{Path: path, Offset: paramsOpen + 1}, insertText, ed.Before, "detach-insert-param")
}

// detachRecvParamForTarget returns the receiver text formatted as a
// parameter for a cross-package detach. When the receiver type is
// itself being moved or renamed in the same run, the post-operation
// name and package qualifier are substituted.
func detachRecvParamForTarget(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo) string {
	recvDir, recvNewName := resolvedReceiverLocation(ix, rr, fd, resolved)
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
		if id.Kind != mast.Use || id.File == nil {
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
			edits.Insert(ed.Anchor{Path: filePath, Offset: lparen + 1}, insertText, ed.Before, "detach-callsite-recv-arg")
		} else {
			plan.Warnings.Addf(
				"method value reference to %s.%s will change signature after detach",
				recvText, rr.Group.Name)
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
		plan.Warnings.AddAtf(rr, ix, "cannot find function declaration for %s", rr.Group.Name)
		return
	}
	if fd.Recv != nil {
		plan.Warnings.AddAtf(rr, ix, "%s is already a method", rr.Group.Name)
		return
	}
	if fd.Type.TypeParams != nil && len(fd.Type.TypeParams.List) > 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: generic functions cannot become methods", rr.Group.Name)
		return
	}
	if fd.Type.Params == nil || len(fd.Type.Params.List) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: no parameters", rr.Group.Name)
		return
	}

	firstField := fd.Type.Params.List[0]
	if _, isEllipsis := firstField.Type.(*ast.Ellipsis); isEllipsis {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: first parameter is variadic", rr.Group.Name)
		return
	}
	if len(firstField.Names) == 0 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: first parameter has no name", rr.Group.Name)
		return
	}
	if len(firstField.Names) > 1 {
		plan.Warnings.AddAtf(rr, ix, "cannot attach %s as method: first parameter field has multiple names", rr.Group.Name)
		return
	}

	recvTypeName := typeExprName(firstField.Type)
	if recvTypeName != rr.Relo.MethodOf {
		plan.Warnings.AddAtf(rr, ix,
			"cannot attach %s as method on %s: first parameter type is %s",
			rr.Group.Name, rr.Relo.MethodOf, recvTypeName)
		return
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
	attachDeclEdits(ix, rr, fd, recvText, edits)

	attachCallSites(ix, rr, edits)
}

// attachDeclEdits emits primitives onto edits that convert a function
// declaration into a method. recvText is the receiver formatted as the
// field inside the method's receiver parens. The declaration rename
// (if any) is handled by the rename pass on the ident region.
func attachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, recvText string, edits *ed.Plan) {
	fset := ix.Fset
	path := rr.File.Path
	firstField := fd.Type.Params.List[0]

	// Insert receiver before the function name.
	nameStart := fset.Position(fd.Name.Pos()).Offset
	edits.Insert(ed.Anchor{Path: path, Offset: nameStart},
		"("+recvText+") ", ed.Before, "attach-insert-recv")

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
func attachCallSites(ix *mast.Index, rr *resolvedRelo, edits *ed.Plan) {
	for _, id := range rr.Group.Idents {
		if id.Kind != mast.Use || id.File == nil {
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
			if s, ok := x.Fun.(*ast.SelectorExpr); ok && s.Sel == ident {
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
		switch fun := call.Fun.(type) {
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
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}
