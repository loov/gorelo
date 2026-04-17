package relo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/rules"
)

// FromRules converts parsed rules into Relo and FileMove instructions by
// resolving each item's definition in the index. The dir parameter is joined
// with rule destinations to produce absolute MoveTo paths; pass "." to use
// destinations as-is.
func FromRules(ix *mast.Index, parsed []rules.Rule, dir string) ([]Relo, []FileMove, error) {
	var relos []Relo
	var fileMoves []FileMove
	for _, rule := range parsed {
		for _, item := range rule.Items {
			if item.IsFileMove {
				from := item.Source
				if !filepath.IsAbs(from) {
					from = filepath.Join(dir, from)
				}
				to := rule.Dest
				if !filepath.IsAbs(to) {
					to = filepath.Join(dir, to)
				}
				fileMoves = append(fileMoves, FileMove{From: from, To: to})
				continue
			}

			source := ResolveSource(ix, item.Source, dir)

			var r Relo

			switch {
			case item.Detach:
				// @detach: Server#Start — find method Start on type Server.
				id := ix.FindFieldDef(item.Name, item.Field, source)
				if id == nil {
					return nil, nil, fmt.Errorf("could not find method %q on type %q", item.Field, item.Name)
				}
				r.Ident = id
				r.Rename = item.FieldRename
				r.Detach = true

			case item.MethodOf != "":
				// @attach Server: Start — find function Start.
				id := ix.FindDef(item.Name, source)
				if id == nil {
					src := ""
					if item.Source != "" {
						src = " in " + item.Source
					}
					return nil, nil, fmt.Errorf("could not find definition for %q%s", item.Name, src)
				}
				r.Ident = id
				r.Rename = item.Rename
				r.MethodOf = item.MethodOf

			case item.Field != "":
				id := ix.FindFieldDef(item.Name, item.Field, source)
				if id == nil {
					return nil, nil, fmt.Errorf("could not find field %q in struct %q", item.Field, item.Name)
				}
				r.Ident = id
				r.Rename = item.FieldRename

			default:
				id := ix.FindDef(item.Name, source)
				if id == nil {
					src := ""
					if item.Source != "" {
						src = " in " + item.Source
					}
					return nil, nil, fmt.Errorf("could not find definition for %q%s", item.Name, src)
				}
				r.Ident = id
				r.Rename = item.Rename
			}

			if rule.Dest != "" {
				// Fields cannot be moved, only renamed. Skip MoveTo
				// for field renames so they work inside <- / -> blocks.
				// Bare field references (no rename) still get MoveTo
				// so that resolve catches them as errors.
				grp := ix.Group(r.Ident)
				if grp == nil || grp.Kind != mast.Field || r.Rename == "" {
					r.MoveTo = filepath.Join(dir, rule.Dest)
				}
			}
			relos = append(relos, r)
		}
	}
	return relos, fileMoves, nil
}

// ResolveSource rewrites a user-supplied source qualifier into a form the
// mast.Index lookups accept. A source like "./pkg" or an absolute directory
// is translated to the matching package's import path; a file-like source
// (ending in .go) or an import-path-like source passes through unchanged.
func ResolveSource(ix *mast.Index, source, dir string) string {
	if source == "" {
		return ""
	}
	if strings.HasSuffix(source, ".go") {
		return source
	}
	// Leave an exact import-path match alone.
	for _, pkg := range ix.Pkgs {
		if pkg.Path == source {
			return source
		}
	}
	// Directory-like source: resolve against dir and compare with the
	// directory of each package's first file.
	if !strings.Contains(source, "/") && !strings.HasPrefix(source, ".") {
		return source
	}
	abs := source
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, abs)
	}
	abs = filepath.Clean(abs)
	for _, pkg := range ix.Pkgs {
		if len(pkg.Files) == 0 {
			continue
		}
		if filepath.Dir(pkg.Files[0].Path) == abs {
			return pkg.Path
		}
	}
	return source
}
