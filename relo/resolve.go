package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"unicode"

	"github.com/loov/gorelo/mast"
)

// resolvedRelo is a validated and enriched relo.
type resolvedRelo struct {
	Relo         Relo
	Group        *mast.Group
	DefIdent     *mast.Ident
	File         *mast.File
	TargetFile   string
	TargetName   string
	Synthesized  bool
	FromFileMove *fileMoveInfo // non-nil when this relo was synthesised from a whole-file move
}

// isCrossFileMove reports whether this relo moves a declaration to a different file.
func (rr *resolvedRelo) isCrossFileMove() bool {
	return rr.File != nil && rr.TargetFile != rr.File.Path
}

// resolve validates, deduplicates, and synthesizes relos (phases 0-1).
//
// Phase 0 runs PRE-RESOLUTION validation per relo: kind compatibility,
// rename-target identifier validity, dedup. Checks that depend on the
// full resolved set (e.g. unexported cross-package moves riding along
// with a file move) live in validate.go and run after resolve returns.
func resolve(ix *mast.Index, relos []Relo, fmInfos []*fileMoveInfo, plan *Plan) ([]*resolvedRelo, error) {
	// Phase 0: validate each relo.
	seen := make(map[seenKey]*resolvedRelo)
	var resolved []*resolvedRelo

	for _, r := range relos {
		if r.Ident == nil {
			return nil, fmt.Errorf("relo has nil Ident")
		}
		grp := ix.Group(r.Ident)
		if grp == nil {
			return nil, fmt.Errorf("ident %q at %v is not tracked by the index", r.Ident.Name, r.Ident.Pos())
		}

		// Find the Def ident in the group that matches this ast.Ident.
		var defIdent *mast.Ident
		for _, id := range grp.Idents {
			if id.Ident == r.Ident && id.Kind == mast.Def {
				defIdent = id
				break
			}
		}
		if defIdent == nil {
			// The provided ident might not be the def itself;
			// find any def in the group.
			for _, id := range grp.Idents {
				if id.Kind == mast.Def {
					defIdent = id
					break
				}
			}
		}
		if defIdent == nil {
			return nil, fmt.Errorf("no definition found for ident %q", r.Ident.Name)
		}

		// Validate kind.
		switch grp.Kind {
		case mast.TypeName, mast.Func, mast.Method, mast.Const, mast.Var:
			// OK for both MoveTo and Rename.
		case mast.Field:
			if r.MoveTo != "" {
				return nil, fmt.Errorf("field %q cannot be moved, only renamed", grp.Name)
			}
		case mast.Label, mast.PackageName, mast.Unknown:
			return nil, fmt.Errorf("cannot relocate %q (kind %d)", grp.Name, grp.Kind)
		}

		// Validate detach/attach.
		if r.Detach && grp.Kind != mast.Method {
			return nil, fmt.Errorf("@detach requires a method, but %q is %v", grp.Name, grp.Kind)
		}
		if r.MethodOf != "" && grp.Kind != mast.Func {
			return nil, fmt.Errorf("@attach requires a function, but %q is %v", grp.Name, grp.Kind)
		}

		// Update group Kind for detach/attach so the rest of the
		// pipeline treats the declaration with its new kind.
		if r.Detach {
			grp.Kind = mast.Func
		}
		if r.MethodOf != "" {
			grp.Kind = mast.Method
		}

		// Validate rename target is a valid Go identifier.
		if r.Rename != "" && !token.IsIdentifier(r.Rename) {
			return nil, fmt.Errorf("rename target %q is not a valid Go identifier", r.Rename)
		}

		// Warn about init/main rename semantics.
		if r.Rename != "" && grp.Kind == mast.Func {
			if grp.Name == "init" && r.Rename != "init" {
				plan.Warnings.Addf("renaming init function loses automatic execution semantics")
			}
			if grp.Name != "init" && r.Rename == "init" {
				plan.Warnings.Addf("renaming %q to init gains automatic execution semantics", grp.Name)
			}
			if defIdent.File != nil && defIdent.File.Pkg != nil && defIdent.File.Pkg.Name == "main" {
				if grp.Name == "main" && r.Rename != "main" {
					plan.Warnings.Addf("renaming main function in main package loses entry-point semantics")
				}
				if grp.Name != "main" && r.Rename == "main" {
					plan.Warnings.Addf("renaming %q to main in main package gains entry-point semantics", grp.Name)
				}
			}
		}

		// Compute the seen key. Normally we deduplicate by group alone,
		// but when the definition comes from a build-constrained file, we
		// include the file path so that same-name declarations from
		// non-overlapping build tags can coexist.
		sk := seenKey{Group: grp}
		if defIdent.File != nil && defIdent.File.BuildTag != "" {
			sk.File = defIdent.File.Path
		}

		// Deduplicate by group (+ optional file).
		if existing, ok := seen[sk]; ok {
			// Two explicit relos for the same key: error if they conflict.
			newTarget := r.MoveTo
			if newTarget == "" && defIdent.File != nil {
				newTarget = defIdent.File.Path
			}
			newName := r.Rename
			if newName == "" {
				newName = grp.Name
			}
			if existing.TargetFile != newTarget || existing.TargetName != newName {
				return nil, fmt.Errorf("conflicting relos for %q: (%s, %s) vs (%s, %s)",
					grp.Name, existing.TargetFile, existing.TargetName, newTarget, newName)
			}
			// Identical — silently deduplicate.
			continue
		}

		rr := &resolvedRelo{
			Relo:       r,
			Group:      grp,
			DefIdent:   defIdent,
			File:       defIdent.File,
			TargetFile: r.MoveTo,
			TargetName: grp.Name,
		}
		if r.Rename != "" {
			rr.TargetName = r.Rename
		}
		if rr.TargetFile == "" && rr.File != nil {
			rr.TargetFile = rr.File.Path
		}

		seen[sk] = rr
		resolved = append(resolved, rr)
	}

	// Phase 1: synthesize additional relos.
	resolved = synthesize(ix, resolved, seen, plan)

	return resolved, nil
}

// seenKey is used by resolve and synthesize to deduplicate resolved relos.
// When File is empty, it matches any entry for the same Group. When File
// is set (for build-constrained definitions), entries from different files
// can coexist independently.
type seenKey struct {
	Group *mast.Group
	File  string
}

// synthesize adds methods for moved types and generates warnings.
func synthesize(ix *mast.Index, resolved []*resolvedRelo, seen map[seenKey]*resolvedRelo, plan *Plan) []*resolvedRelo {
	// Collect moved type names for method synthesis and warnings.
	movedTypes := make(map[string]*resolvedRelo) // "pkg.TypeName" -> relo
	movedNames := make(map[string]*resolvedRelo) // "pkg.Name" -> relo

	for _, rr := range resolved {
		key := rr.Group.Pkg + "." + rr.Group.Name
		movedNames[key] = rr
		if rr.Group.Kind == mast.TypeName {
			movedTypes[key] = rr
		}
	}

	// For each moved type, find and auto-add its methods.
	// Methods must be in the same package as the receiver type.
	movedPkgs := make(map[string]bool)
	for _, rr := range resolved {
		if rr.Group.Kind == mast.TypeName {
			movedPkgs[rr.Group.Pkg] = true
		}
	}
	for _, pkg := range ix.Pkgs {
		if !movedPkgs[pkg.Path] {
			continue
		}
		for _, file := range pkg.Files {
			for _, decl := range file.Syntax.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv == nil {
					continue
				}
				recvType := mast.ReceiverTypeName(fd.Recv)
				if recvType == "" {
					continue
				}
				typeKey := pkg.Path + "." + recvType
				typeRelo, ok := movedTypes[typeKey]
				if !ok {
					continue
				}

				// Find the group for this method's name ident.
				grp := ix.Group(fd.Name)
				if grp == nil {
					continue
				}
				if _, already := seen[seenKey{Group: grp}]; already {
					continue
				}

				// Find the def ident.
				var defIdent *mast.Ident
				for _, id := range grp.Idents {
					if id.Kind == mast.Def {
						defIdent = id
						break
					}
				}
				if defIdent == nil {
					continue
				}

				// Auto-export unexported methods that have external callers
				// when moving cross-package. Methods only called from
				// sibling methods of the same type stay unexported.
				targetName := grp.Name
				var rename string
				if defIdent.File != nil && defIdent.File.Pkg != nil &&
					!isSamePackageDir(defIdent.File.Pkg, typeRelo.TargetFile) {
					if !token.IsExported(grp.Name) &&
						methodHasExternalUses(grp, recvType) {
						runes := []rune(grp.Name)
						runes[0] = unicode.ToUpper(runes[0])
						targetName = string(runes)
						rename = targetName
					}
				}

				rr := &resolvedRelo{
					Relo: Relo{
						Ident:  fd.Name,
						MoveTo: typeRelo.TargetFile,
						Rename: rename,
					},
					Group:       grp,
					DefIdent:    defIdent,
					File:        defIdent.File,
					TargetFile:  typeRelo.TargetFile,
					TargetName:  targetName,
					Synthesized: true,
				}
				seen[seenKey{Group: grp}] = rr
				resolved = append(resolved, rr)
			}
		}
	}

	// Warnings.
	for _, typeRelo := range movedTypes {
		// Warn about NewT constructors.
		ctorKey := typeRelo.Group.Pkg + ".New" + typeRelo.Group.Name
		if _, moved := movedNames[ctorKey]; !moved {
			// Check if the constructor exists.
			ctorFound := false
			for _, pkg := range ix.Pkgs {
				if pkg.Path != typeRelo.Group.Pkg {
					continue
				}
				for _, file := range pkg.Files {
					for _, decl := range file.Syntax.Decls {
						fd, ok := decl.(*ast.FuncDecl)
						if !ok || fd.Recv != nil {
							continue
						}
						if fd.Name.Name == "New"+typeRelo.Group.Name {
							plan.Warnings.AddAtf(typeRelo, ix,
								"constructor New%s exists but is not being moved with type %s",
								typeRelo.Group.Name, typeRelo.Group.Name)
							ctorFound = true
							break
						}
					}
					if ctorFound {
						break
					}
				}
				if ctorFound {
					break
				}
			}
		}
	}

	// Warn about orphaned methods.
	for _, rr := range resolved {
		if rr.Group.Kind != mast.Method {
			continue
		}
		// Find the receiver type name from the method's function decl.
		recvType := findMethodReceiverType(rr)
		if recvType == "" {
			continue
		}
		typeKey := rr.Group.Pkg + "." + recvType
		if _, ok := movedTypes[typeKey]; !ok {
			plan.Warnings.AddAtf(rr, ix,
				"method %s.%s is being moved but type %s is not",
				recvType, rr.Group.Name, recvType)
		}
	}

	return resolved
}

// findMethodReceiverType finds the receiver type name for a method relo
// by scanning the file's AST for the matching FuncDecl.
func findMethodReceiverType(rr *resolvedRelo) string {
	if rr.File == nil {
		return ""
	}
	for _, decl := range rr.File.Syntax.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil {
			continue
		}
		if fd.Name == rr.DefIdent.Ident {
			return mast.ReceiverTypeName(fd.Recv)
		}
	}
	return ""
}

// methodHasExternalUses reports whether grp (a method on recvType) has any
// Use idents that are NOT inside another method of the same receiver type.
// If all uses are within sibling methods (which move together), returning
// false means the method does not need to be exported.
func methodHasExternalUses(grp *mast.Group, recvType string) bool {
	for _, id := range grp.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}
		enclosing := enclosingFuncDecl(id.File.Syntax, id.Ident.Pos())
		if enclosing == nil || enclosing.Recv == nil {
			return true
		}
		if mast.ReceiverTypeName(enclosing.Recv) != recvType {
			return true
		}
	}
	return false
}

// enclosingFuncDecl returns the FuncDecl containing the given token.Pos,
// or nil if none.
func enclosingFuncDecl(file *ast.File, pos token.Pos) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if pos >= fd.Pos() && pos < fd.End() {
			return fd
		}
	}
	return nil
}

// buildDetachGroups returns the set of groups that have a detach or
// attach operation, so other phases can skip them.
func buildDetachGroups(resolved []*resolvedRelo) map[*mast.Group]bool {
	m := make(map[*mast.Group]bool)
	for _, rr := range resolved {
		if rr.Relo.Detach || rr.Relo.MethodOf != "" {
			m[rr.Group] = true
		}
	}
	return m
}

// groupByTarget groups resolved relos by target file path.
func groupByTarget(resolved []*resolvedRelo) map[string][]*resolvedRelo {
	m := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		m[rr.TargetFile] = append(m[rr.TargetFile], rr)
	}
	return m
}

// groupBySource groups resolved relos by source file path,
// skipping relos with no source file.
func groupBySource(resolved []*resolvedRelo) map[string][]*resolvedRelo {
	m := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		if rr.File != nil {
			m[rr.File.Path] = append(m[rr.File.Path], rr)
		}
	}
	return m
}

// hasExternalUses reports whether grp has a Use ident outside the set of
// files being file-moved to targetDir. Uses in files that accompany the def
// to the same destination directory do not count as external.
func hasExternalUses(grp *mast.Group, targetDir string, fileMoveTargetDir map[string]string) bool {
	for _, id := range grp.Idents {
		if id.Kind != mast.Use || id.File == nil {
			continue
		}
		if dir, ok := fileMoveTargetDir[id.File.Path]; ok && dir == targetDir {
			continue
		}
		return true
	}
	return false
}

// isSamePackageDir checks if targetFile is in the same directory as pkg.
func isSamePackageDir(pkg *mast.Package, targetFile string) bool {
	if len(pkg.Files) == 0 {
		return false
	}
	absTarget, err := filepath.Abs(targetFile)
	if err != nil {
		return false
	}
	return filepath.Dir(pkg.Files[0].Path) == filepath.Dir(absTarget)
}
