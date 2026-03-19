package relo

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
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

	sortedTargets := sortedKeys(byTarget)
	for _, target := range sortedTargets {
		rrs := byTarget[target]
		constraints := make(map[string]bool)
		for _, rr := range rrs {
			if rr.File != nil {
				constraints[rr.File.BuildTag] = true
			}
		}

		// Check for mixed constraints.
		delete(constraints, "")
		if len(constraints) > 1 {
			cs := sortedKeys(constraints)
			plan.Warnings.Addf(
				"mixed build constraints (%s) going to %s",
				strings.Join(cs, "; "), target)
		}
	}
}

// detectConflicts checks for naming and movement conflicts (phase 5).
func detectConflicts(ix *mast.Index, resolved []*resolvedRelo, plan *Plan) error {
	// Check movement conflicts: same group moved to two different targets.
	// Use a composite key of (group, source file path) so that declarations
	// from non-overlapping build constraints can target different files.
	type moveKey struct {
		group *mast.Group
		file  string // source file path, non-empty for build-constrained files
	}
	targetsByKey := make(map[moveKey]string)
	for _, rr := range resolved {
		mk := moveKey{group: rr.Group}
		if rr.File != nil && rr.File.BuildTag != "" {
			mk.file = rr.File.Path
		}
		if existing, ok := targetsByKey[mk]; ok {
			if existing != rr.TargetFile {
				return fmt.Errorf("conflicting moves: %s targeted to both %s and %s",
					rr.Group.Name, existing, rr.TargetFile)
			}
		}
		targetsByKey[mk] = rr.TargetFile
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

	// Check for inter-relo collisions: two different relos with the same
	// TargetName going to the same directory.
	for dir, entries := range byTargetDir {
		for i := 0; i < len(entries); i++ {
			for j := i + 1; j < len(entries); j++ {
				if entries[i].name != entries[j].name {
					continue
				}
				if entries[i].reloGroup == entries[j].reloGroup {
					continue
				}
				if !constraintsMayOverlap(entries[i].buildTag, entries[j].buildTag) {
					continue
				}
				return fmt.Errorf("name collision: multiple declarations named %s are being moved to %s",
					entries[i].name, dir)
			}
		}
	}

	// Check against existing declarations in target packages.
	for dir, entries := range byTargetDir {
		targetPkg := findPkgForDir(ix, dir)
		if targetPkg == nil {
			continue
		}

		// Build set of groups being moved out of this package, keyed by
		// their original name.  We use this to skip collision checks when
		// the existing declaration at that name is the one being moved away.
		movedFromGroups := make(map[*mast.Group]bool)
		movedFromNames := make(map[string]bool)
		for _, rr := range resolved {
			if rr.File != nil && rr.File.Pkg == targetPkg && rr.TargetFile != rr.File.Path {
				movedFromGroups[rr.Group] = true
				// Only mark the name as vacated when moving to a different
				// package. Same-package moves keep the name in the package.
				if dirOf(rr.TargetFile) != dir {
					movedFromNames[rr.Group.Name] = true
				}
			}
		}

		// Build a set of groups that are leaving this package entirely
		// (cross-package moves). These vacate their names.
		leavingGroups := make(map[*mast.Group]bool)
		for _, rr := range resolved {
			if rr.File != nil && rr.File.Pkg == targetPkg && dirOf(rr.TargetFile) != dir {
				leavingGroups[rr.Group] = true
			}
		}

		for _, entry := range entries {
			// If this entry's group is leaving the package entirely,
			// it vacates the name — skip collision check.
			if leavingGroups[entry.reloGroup] {
				continue
			}
			for _, file := range targetPkg.Files {
				// Skip declarations whose name is being vacated
				// (cross-package moves that remove the name).
				if movedFromNames[entry.name] {
					continue
				}

				if !constraintsMayOverlap(entry.buildTag, file.BuildTag) {
					continue
				}

				for _, decl := range file.Syntax.Decls {
					if !nameConflicts(decl, entry.name) {
						continue
					}
					// Don't flag the entry's own declaration as a collision.
					if movedFromGroups[entry.reloGroup] && declDefinesGroup(ix, decl, entry.reloGroup) {
						continue
					}
					return fmt.Errorf("name collision: %s already exists in %s",
						entry.name, file.Path)
				}
			}
		}
	}

	// Warn about potential circular imports for cross-package moves.
	for _, rr := range resolved {
		if rr.File == nil {
			continue
		}
		targetDir := dirOf(rr.TargetFile)
		srcDir := dirOf(rr.File.Path)
		if targetDir == srcDir {
			continue
		}
		// Check if the target package imports the source package.
		srcImportPath := guessImportPath(srcDir)
		if srcImportPath == "" {
			continue
		}
		targetFile := findFileInIndex(ix, rr.TargetFile)
		if targetFile == nil {
			// Target file doesn't exist yet; find the package by
			// matching the target directory against known packages.
			targetPkg := findPkgForDir(ix, targetDir)
			if targetPkg == nil {
				continue
			}
			for _, f := range targetPkg.Files {
				for _, imp := range f.Syntax.Imports {
					impPath, _ := strconv.Unquote(imp.Path.Value)
					if impPath == srcImportPath {
						plan.Warnings.AddAtf(rr, ix,
							"moving %s to %s may create a circular import: target already imports source package %s",
							rr.Group.Name, rr.TargetFile, srcImportPath)
						break
					}
				}
			}
			continue
		}
		for _, imp := range targetFile.Syntax.Imports {
			impPath, _ := strconv.Unquote(imp.Path.Value)
			if impPath == srcImportPath {
				plan.Warnings.AddAtf(rr, ix,
					"moving %s to %s may create a circular import: target already imports source package %s",
					rr.Group.Name, rr.TargetFile, srcImportPath)
				break
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
			plan.Warnings.AddAtf(rr, ix,
				"moved decl %s has a //go:embed directive", rr.Group.Name)
		}
		if hasDirective(decl, rr.File.Syntax, ix.Fset, "go:generate") {
			plan.Warnings.AddAtf(rr, ix,
				"moved decl %s has a //go:generate directive", rr.Group.Name)
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
		// Direct negation: "linux" and "!linux" are mutually exclusive.
		if aTag == "!"+bTag || bTag == "!"+aTag {
			return false
		}

		aNeg := strings.HasPrefix(aTag, "!")
		bNeg := strings.HasPrefix(bTag, "!")
		aBase := strings.TrimPrefix(aTag, "!")
		bBase := strings.TrimPrefix(bTag, "!")

		// Resolve implies relationships (e.g. ios→darwin, android→linux).
		aResolved := resolveImplied(aBase)
		bResolved := resolveImplied(bBase)

		if aBase != bBase {
			switch {
			case !aNeg && !bNeg:
				// Two different positive exclusive tags: exclusive only if
				// neither implies the other and both are in the same
				// exclusive set.
				if aResolved != bBase && bResolved != aBase {
					if exclusiveOSTags[aBase] && exclusiveOSTags[bBase] {
						return false
					}
					if exclusiveArchTags[aBase] && exclusiveArchTags[bBase] {
						return false
					}
				}
			case aNeg && bNeg:
				// Two different negated exclusive tags: e.g. !linux and
				// !darwin both hold on FreeBSD → they overlap.
				// (conservative: return true)
			default:
				// One negated, one positive from the same exclusive set:
				// e.g. !linux and darwin → darwin implies !linux → overlap.
				// (conservative: return true)
			}
		}
	}
	return true
}

// osImplies maps GOOS values that imply another build tag.
var osImplies = map[string]string{
	"ios":     "darwin",
	"android": "linux",
}

// resolveImplied returns the tag that base implies, or "" if none.
func resolveImplied(base string) string {
	if v, ok := osImplies[base]; ok {
		return v
	}
	return ""
}

// extractConstraintTag extracts a single simple tag from a build constraint expression.
// Returns "" if the constraint is compound (contains operators or parentheses).
func extractConstraintTag(constraint string) string {
	tag := strings.TrimSpace(constraint)
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

// declDefinesGroup checks if a declaration's defining ident belongs to the given group.
func declDefinesGroup(ix *mast.Index, decl ast.Decl, grp *mast.Group) bool {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return ix.Group(d.Name) == grp
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if ix.Group(s.Name) == grp {
					return true
				}
			case *ast.ValueSpec:
				for _, n := range s.Names {
					if ix.Group(n) == grp {
						return true
					}
				}
			}
		}
	}
	return false
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
