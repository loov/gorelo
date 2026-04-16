package relo

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/loov/gorelo/mast"
)

// computeDetachEdits generates edits for detaching methods (converting to
// standalone functions) and attaching functions (converting to methods).
// Declaration edits are stored in the renameSet (for same-file application)
// and call-site edits are also merged into renames.
//
// For cross-file moves, declaration edits must also be applied during span
// extraction — see structuralDeclEdits used in assembleTargets.
func computeDetachEdits(ix *mast.Index, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, plan *Plan) {
	for _, rr := range resolved {
		switch {
		case rr.Relo.Detach:
			detachMethod(ix, rr, resolved, spans, renames, imports, plan)
		case rr.Relo.MethodOf != "":
			attachMethod(ix, rr, renames, imports, plan)
		}
	}
}

// detachMethod converts a method to a standalone function.
func detachMethod(ix *mast.Index, rr *resolvedRelo, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, plan *Plan) {
	if rr.File == nil {
		return
	}

	fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
	if fd == nil || fd.Recv == nil {
		plan.Warnings.AddAtf(rr, ix, "cannot find method declaration for %s", rr.Group.Name)
		return
	}

	// Add declaration edits to renameSet for same-file (rename-only)
	// operations. Cross-file moves use structuralDeclEdits during
	// extraction — emitting decl edits here as well would double-apply
	// the receiver insertion through the in-span rename pass.
	filePath := rr.File.Path
	if !rr.isCrossFileMove() {
		renames.byFile[filePath] = append(renames.byFile[filePath], detachDeclEdits(ix, rr, fd)...)
	}

	// For cross-package moves, the detached function's parameter references
	// the receiver type. Add the import for the receiver type's final
	// location — which may differ from the source package when the type
	// itself is being moved in the same run.
	if rr.isCrossFileMove() {
		recvImportPath := detachedReceiverImportPath(ix, rr, fd, resolved)
		if recvImportPath != "" {
			addImportToFile(imports, ix, rr.TargetFile, recvImportPath)
		}
	}

	detachCallSites(ix, rr, fd, resolved, spans, renames, imports, plan)
}

// detachedReceiverImportPath returns the import path the detached
// function's target file needs to import in order to reference the
// receiver type. It takes into account concurrent renames/moves of
// the receiver type. Returns "" when no import is needed (receiver
// type resolves to the same package as the detach target).
func detachedReceiverImportPath(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo) string {
	tgtDir := finalDir(rr)
	var recvDir string
	if _, recvTargetFile, ok := receiverTypeResolved(ix, fd, resolved); ok {
		recvDir = filepath.Dir(recvTargetFile)
	} else {
		recvDir = filepath.Dir(rr.File.Path)
	}
	if recvDir == tgtDir {
		return ""
	}
	return guessImportPath(recvDir)
}

// receiverTypeResolved returns the post-rename name and target file of
// the method receiver's type when that type is itself being moved or
// renamed in the same run. Returns (_, _, false) when the receiver
// type is not in the resolved set.
func receiverTypeResolved(ix *mast.Index, fd *ast.FuncDecl, resolved []*resolvedRelo) (name string, targetFile string, ok bool) {
	id := receiverTypeIdent(fd.Recv)
	if id == nil {
		return "", "", false
	}
	grp := ix.Group(id)
	if grp == nil {
		return "", "", false
	}
	for _, r := range resolved {
		if r.Group == grp {
			return r.TargetName, r.TargetFile, true
		}
	}
	return "", "", false
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

// detachDeclEdits returns edits to convert a method declaration to a function.
// Edits are in absolute file offsets.
func detachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	// Remove receiver: from opening paren to closing paren + trailing space.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}
	edits = append(edits, edit{Start: recvOpen, End: recvEnd, New: ""})

	// Rename ident if needed.
	if rr.TargetName != rr.Group.Name {
		nameStart := fset.Position(fd.Name.Pos()).Offset
		nameEnd := nameStart + len(fd.Name.Name)
		edits = append(edits, edit{Start: nameStart, End: nameEnd, New: rr.TargetName})
	}

	// Insert receiver as first parameter.
	recvParam := formatRecvAsParam(fd.Recv, fset, "", "")
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0
	insertText := recvParam
	if hasParams {
		insertText += ", "
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: paramsOpen + 1, New: insertText})

	return edits
}

// detachCallSites rewrites call sites from s.Method(args) → Func(s, args)
// or pkg.Func(s, args) when moving cross-package. Qualification is
// based on the caller's FINAL location — if the caller is itself being
// moved to the same target package as the detached function, no
// qualifier is needed.
func detachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo, spans map[*resolvedRelo]*span, renames *renameSet, imports *importSet, plan *Plan) {
	newName := finalName(rr)

	var detachTgtDir string
	if rr.TargetFile != "" {
		detachTgtDir = finalDir(rr)
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

		// Determine the caller's final dir + file, accounting for any
		// enclosing decl that is itself being moved in this run.
		callerFinalDir := filepath.Dir(filePath)
		callerFinalFile := filePath
		identOff := fset.Position(id.Ident.Pos()).Offset
		for _, r := range resolved {
			if r.File == nil || r.File.Path != filePath || !r.isCrossFileMove() {
				continue
			}
			s := spans[r]
			if s == nil {
				continue
			}
			if identOff >= s.Start && identOff < s.End {
				callerFinalDir = filepath.Dir(r.TargetFile)
				callerFinalFile = r.TargetFile
				break
			}
		}

		// Determine the qualified function name for this call site.
		qualName := newName
		if detachTgtDir != "" && callerFinalDir != detachTgtDir {
			if tgtImportPath := guessImportPath(detachTgtDir); tgtImportPath != "" {
				qualName = guessImportLocalName(tgtImportPath) + "." + newName
				// Add import to the caller's FINAL file.
				addImportToFile(imports, ix, callerFinalFile, tgtImportPath)
			}
		}

		// Replace "recv.Method" with the (possibly qualified) function name.
		selStart := fset.Position(sel.Sel.Pos()).Offset
		selEnd := selStart + len(sel.Sel.Name)
		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: xStart, End: selEnd, New: qualName,
		})

		if call != nil {
			lparen := fset.Position(call.Lparen).Offset
			hasArgs := len(call.Args) > 0
			insertText := recvText
			if hasArgs {
				insertText += ", "
			}
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: lparen + 1, New: insertText,
			})
		} else {
			plan.Warnings.Addf(
				"method value reference to %s.%s will change signature after detach",
				recvText, rr.Group.Name)
		}
	}
}

// attachMethod converts a standalone function to a method.
func attachMethod(ix *mast.Index, rr *resolvedRelo, renames *renameSet, imports *importSet, plan *Plan) {
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

	// Add declaration edits to renameSet.
	filePath := rr.File.Path
	renames.byFile[filePath] = append(renames.byFile[filePath], attachDeclEdits(ix, rr, fd)...)

	attachCallSites(ix, rr, fd, renames, imports, plan)
}

// attachDeclEdits returns edits to convert a function declaration to a method.
// Edits are in absolute file offsets.
func attachDeclEdits(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	firstField := fd.Type.Params.List[0]
	paramStart := fset.Position(firstField.Pos()).Offset
	paramEnd := fset.Position(firstField.End()).Offset
	recvText := string(src[paramStart:paramEnd])

	// Replace function name with receiver + name (possibly renamed).
	nameStart := fset.Position(fd.Name.Pos()).Offset
	nameEnd := nameStart + len(fd.Name.Name)
	edits = append(edits, edit{
		Start: nameStart, End: nameEnd,
		New: "(" + recvText + ") " + rr.TargetName,
	})

	// Remove first parameter from parameter list.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	removeEnd := paramEnd
	if len(fd.Type.Params.List) > 1 {
		nextStart := fset.Position(fd.Type.Params.List[1].Pos()).Offset
		removeEnd = nextStart
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: removeEnd, New: ""})

	return edits
}

// attachDeclEditsTarget is like attachDeclEdits but unqualifies the receiver
// type when the declaration moves to the receiver type's package (self-import
// removal). E.g., `s *srv.Server` → `s *Server` when moving into package srv.
func attachDeclEditsTarget(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	firstField := fd.Type.Params.List[0]
	paramStart := fset.Position(firstField.Pos()).Offset
	paramEnd := fset.Position(firstField.End()).Offset
	recvText := string(src[paramStart:paramEnd])

	// When moving to a different package, check if the receiver type's
	// package qualifier should be removed (self-import).
	tgtImportPath := finalImportPath(rr)
	if tgtImportPath != "" {
		// Check if the first parameter's type references the target package.
		// Unqualify the type when moving into the receiver's package.
		if sel, ok := firstField.Type.(*ast.SelectorExpr); ok {
			if qualIdent, ok := sel.X.(*ast.Ident); ok {
				if qualImportPath := findImportPathForIdent(rr.File, qualIdent.Name); qualImportPath == tgtImportPath {
					// Value receiver: "s srv.Server" → "s Server"
					nameStr := ""
					if len(firstField.Names) > 0 {
						nameStr = firstField.Names[0].Name + " "
					}
					recvText = nameStr + sel.Sel.Name
				}
			}
		} else if star, ok := firstField.Type.(*ast.StarExpr); ok {
			if sel, ok := star.X.(*ast.SelectorExpr); ok {
				if qualIdent, ok := sel.X.(*ast.Ident); ok {
					if qualImportPath := findImportPathForIdent(rr.File, qualIdent.Name); qualImportPath == tgtImportPath {
						// Pointer receiver: "s *srv.Server" → "s *Server"
						nameStr := ""
						if len(firstField.Names) > 0 {
							nameStr = firstField.Names[0].Name + " "
						}
						recvText = nameStr + "*" + sel.Sel.Name
					}
				}
			}
		}
	}

	// Replace function name with receiver + name (possibly renamed).
	nameStart := fset.Position(fd.Name.Pos()).Offset
	nameEnd := nameStart + len(fd.Name.Name)
	edits = append(edits, edit{
		Start: nameStart, End: nameEnd,
		New: "(" + recvText + ") " + rr.TargetName,
	})

	// Remove first parameter from parameter list.
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	removeEnd := paramEnd
	if len(fd.Type.Params.List) > 1 {
		nextStart := fset.Position(fd.Type.Params.List[1].Pos()).Offset
		removeEnd = nextStart
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: removeEnd, New: ""})

	return edits
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
func attachCallSites(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, renames *renameSet, imports *importSet, plan *Plan) {
	newName := rr.TargetName

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

		renames.byFile[filePath] = append(renames.byFile[filePath], edit{
			Start: editStart, End: identStart + len(id.Ident.Name),
			New: recvText + "." + newName,
		})

		lparen := fset.Position(call.Lparen).Offset
		if len(call.Args) > 1 {
			secondArg := call.Args[1]
			secondStart := fset.Position(secondArg.Pos()).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: secondStart, New: "",
			})
		} else {
			rparen := fset.Position(call.Rparen).Offset
			renames.byFile[filePath] = append(renames.byFile[filePath], edit{
				Start: lparen + 1, End: rparen, New: "",
			})
		}
	}
}

// addImportToFile adds an import for impPath to the given file in the import set,
// checking if it's already present.
func addImportToFile(imports *importSet, ix *mast.Index, filePath, impPath string) {
	ic := imports.ensureFile(filePath)
	// Check if already being added.
	for _, entry := range ic.Add {
		if entry.Path == impPath {
			return
		}
	}
	// Check if already imported.
	if f := ix.FilesByPath[filePath]; f != nil {
		for _, imp := range f.Syntax.Imports {
			if importPath(imp) == impPath {
				return
			}
		}
	}
	ic.Add = append(ic.Add, importEntry{Path: impPath})
}

// structuralDeclEdits computes span-relative edits for the declaration
// of a detach/attach relo. Used during span extraction in assembleTargets.
func structuralDeclEdits(ix *mast.Index, rr *resolvedRelo, s *span, resolved []*resolvedRelo) []edit {
	if rr.File == nil || s == nil {
		return nil
	}

	var absEdits []edit

	if rr.Relo.Detach {
		fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
		if fd == nil || fd.Recv == nil {
			return nil
		}
		// For cross-package moves, qualify the receiver type.
		absEdits = detachDeclEditsTarget(ix, rr, fd, resolved)
	} else if rr.Relo.MethodOf != "" {
		fd := findFuncDecl(rr.File.Syntax, rr.DefIdent.Ident)
		if fd == nil {
			return nil
		}
		absEdits = attachDeclEditsTarget(ix, rr, fd)
	}

	// Convert to span-relative offsets.
	var relEdits []edit
	for _, e := range absEdits {
		relEdits = append(relEdits, edit{
			Start: e.Start - s.Start,
			End:   e.End - s.Start,
			New:   e.New,
		})
	}
	return relEdits
}

// detachDeclEditsTarget is like detachDeclEdits but qualifies the receiver
// type for cross-package moves. When the receiver type is itself being
// renamed or moved in the same run, the post-rename name and post-move
// package qualifier are used.
func detachDeclEditsTarget(ix *mast.Index, rr *resolvedRelo, fd *ast.FuncDecl, resolved []*resolvedRelo) []edit {
	fset := ix.Fset
	src := fileContent(rr.File)
	var edits []edit

	// Remove receiver.
	recvOpen := fset.Position(fd.Recv.Opening).Offset
	recvClose := fset.Position(fd.Recv.Closing).Offset
	recvEnd := recvClose + 1
	for recvEnd < len(src) && src[recvEnd] == ' ' {
		recvEnd++
	}
	edits = append(edits, edit{Start: recvOpen, End: recvEnd, New: ""})

	// Rename func ident if needed.
	if rr.TargetName != rr.Group.Name {
		nameStart := fset.Position(fd.Name.Pos()).Offset
		nameEnd := nameStart + len(fd.Name.Name)
		edits = append(edits, edit{Start: nameStart, End: nameEnd, New: rr.TargetName})
	}

	// Determine the receiver type's final name and package qualifier,
	// taking into account any concurrent rename/move of that type.
	tgtDir := finalDir(rr)
	recvNewName := ""
	var recvDir string
	if name, recvTargetFile, ok := receiverTypeResolved(ix, fd, resolved); ok {
		recvNewName = name
		recvDir = filepath.Dir(recvTargetFile)
	} else {
		recvDir = filepath.Dir(rr.File.Path)
	}
	var pkgQualifier string
	if recvDir != tgtDir {
		if recvImportPath := guessImportPath(recvDir); recvImportPath != "" {
			pkgQualifier = guessImportLocalName(recvImportPath)
		}
	}

	// Insert receiver as first parameter (possibly qualified and renamed).
	recvParam := formatRecvAsParam(fd.Recv, fset, pkgQualifier, recvNewName)
	paramsOpen := fset.Position(fd.Type.Params.Opening).Offset
	hasParams := fd.Type.Params != nil && len(fd.Type.Params.List) > 0
	insertText := recvParam
	if hasParams {
		insertText += ", "
	}
	edits = append(edits, edit{Start: paramsOpen + 1, End: paramsOpen + 1, New: insertText})

	return edits
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
