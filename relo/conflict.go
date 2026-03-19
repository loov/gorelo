package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/loov/gorelo/mast"
)

// checkConstraints warns about build constraint issues (phase 4).
func checkConstraints(resolved []*resolvedRelo, plan *Plan) {
	// Group relos by target file.
	byTarget := make(map[string][]*resolvedRelo)
	for _, rr := range resolved {
		byTarget[rr.TargetFile] = append(byTarget[rr.TargetFile], rr)
	}

	for target, rrs := range byTarget {
		constraints := make(map[string]bool)
		for _, rr := range rrs {
			if rr.File != nil {
				constraints[rr.File.BuildTag] = true
			}
		}

		// Check for mixed constraints.
		delete(constraints, "")
		if len(constraints) > 1 {
			var cs []string
			for c := range constraints {
				cs = append(cs, c)
			}
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"mixed build constraints (%s) going to %s",
				strings.Join(cs, "; "), target))
		}
	}
}

// detectConflicts checks for naming and movement conflicts (phase 5).
func detectConflicts(ix *mast.Index, resolved []*resolvedRelo, plan *Plan) error {
	// Check movement conflicts: same group moved to two different targets.
	targetsByGroup := make(map[*mast.Group]string)
	for _, rr := range resolved {
		if existing, ok := targetsByGroup[rr.Group]; ok {
			if existing != rr.TargetFile {
				return fmt.Errorf("conflicting moves: %s targeted to both %s and %s",
					rr.Group.Name, existing, rr.TargetFile)
			}
		}
		targetsByGroup[rr.Group] = rr.TargetFile
	}

	// Check naming conflicts in target packages.
	// Build a map of names being placed into each target directory.
	type targetEntry struct {
		name      string
		buildTag  string
		reloGroup *mast.Group
	}
	byTargetDir := make(map[string][]targetEntry)
	for _, rr := range resolved {
		if rr.Group.Kind == mast.Method || rr.Group.Kind == mast.Field {
			continue
		}
		dir := dirOf(rr.TargetFile)
		tag := ""
		if rr.File != nil {
			tag = rr.File.BuildTag
		}
		byTargetDir[dir] = append(byTargetDir[dir], targetEntry{
			name:      rr.TargetName,
			buildTag:  tag,
			reloGroup: rr.Group,
		})
	}

	// Check against existing declarations in target packages.
	for dir, entries := range byTargetDir {
		// Find existing package in this directory.
		var targetPkg *mast.Package
		for _, pkg := range ix.Pkgs {
			if len(pkg.Files) > 0 {
				for _, f := range pkg.Files {
					if dirOf(f.Path) == dir {
						targetPkg = pkg
						break
					}
				}
				if targetPkg != nil {
					break
				}
			}
		}
		if targetPkg == nil {
			continue
		}

		// Build set of names being moved from this package (to exclude from collision check).
		movedFrom := make(map[string]bool)
		for _, rr := range resolved {
			if rr.File != nil && rr.File.Pkg == targetPkg {
				movedFrom[rr.Group.Name] = true
			}
		}

		for _, entry := range entries {
			for _, file := range targetPkg.Files {
				// Skip files that are sources of moved declarations.
				if movedFrom[entry.name] {
					continue
				}

				if !constraintsMayOverlap(entry.buildTag, file.BuildTag) {
					continue
				}

				for _, decl := range file.Syntax.Decls {
					if nameConflicts(decl, entry.name) {
						return fmt.Errorf("name collision: %s already exists in %s",
							entry.name, file.Path)
					}
				}
			}
		}
	}

	// Warn about go:embed / go:generate directives.
	for _, rr := range resolved {
		if rr.File == nil {
			continue
		}
		decl := findEnclosingDecl(rr.File.Syntax, rr.DefIdent.Ident)
		if decl == nil {
			continue
		}
		if hasDirective(decl, rr.File.Syntax, ix.Fset, "go:embed") {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"moved decl %s has a //go:embed directive", rr.Group.Name))
		}
		if hasDirective(decl, rr.File.Syntax, ix.Fset, "go:generate") {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf(
				"moved decl %s has a //go:generate directive", rr.Group.Name))
		}
	}

	return nil
}

// constraintsMayOverlap returns true if two build constraints could coexist.
func constraintsMayOverlap(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	if a == b {
		return true
	}

	aTag := extractConstraintTag(a)
	bTag := extractConstraintTag(b)
	if aTag != "" && bTag != "" {
		if aTag == "!"+bTag || bTag == "!"+aTag {
			return false
		}
		aBase := strings.TrimPrefix(aTag, "!")
		bBase := strings.TrimPrefix(bTag, "!")
		if aBase != bBase {
			if exclusiveOSTags[aBase] && exclusiveOSTags[bBase] {
				return false
			}
			if exclusiveArchTags[aBase] && exclusiveArchTags[bBase] {
				return false
			}
		}
	}
	return true
}

// extractConstraintTag extracts a single tag from "//go:build <tag>".
func extractConstraintTag(constraint string) string {
	tag := strings.TrimPrefix(constraint, "//go:build ")
	tag = strings.TrimSpace(tag)
	check := strings.TrimPrefix(tag, "!")
	if strings.ContainsAny(check, "&|!() ") {
		return ""
	}
	return tag
}

var exclusiveOSTags = map[string]bool{
	"linux": true, "darwin": true, "windows": true, "freebsd": true,
	"openbsd": true, "netbsd": true, "dragonfly": true, "solaris": true,
	"illumos": true, "plan9": true, "aix": true, "js": true, "wasip1": true,
	"ios": true, "android": true,
}

var exclusiveArchTags = map[string]bool{
	"amd64": true, "arm64": true, "arm": true, "386": true,
	"ppc64": true, "ppc64le": true, "mips": true, "mipsle": true,
	"mips64": true, "mips64le": true, "s390x": true, "riscv64": true,
	"wasm": true, "loong64": true,
}

// nameConflicts checks if a declaration defines the given name.
func nameConflicts(decl ast.Decl, name string) bool {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv == nil && d.Name.Name == name {
			return true
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.Name == name {
					return true
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if n.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// hasDirective checks if a declaration has a comment with the given directive.
func hasDirective(decl ast.Decl, file *ast.File, fset *token.FileSet, directive string) bool {
	prefix := "//" + directive

	var doc *ast.CommentGroup
	switch d := decl.(type) {
	case *ast.FuncDecl:
		doc = d.Doc
	case *ast.GenDecl:
		doc = d.Doc
	}
	if doc != nil {
		for _, c := range doc.List {
			if strings.HasPrefix(c.Text, prefix) {
				return true
			}
		}
	}

	// Check comments near the decl.
	declPos := decl.Pos()
	declEnd := decl.End()
	for _, cg := range file.Comments {
		if cg.Pos() >= declPos && cg.End() <= declEnd {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, prefix) {
					return true
				}
			}
		}
		if cg.End() <= declPos {
			endLine := fset.Position(cg.End()).Line
			declLine := fset.Position(declPos).Line
			if declLine-endLine <= 1 {
				for _, c := range cg.List {
					if strings.HasPrefix(c.Text, prefix) {
						return true
					}
				}
			}
		}
	}
	return false
}
