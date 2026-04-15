package relo

import (
	"fmt"
	"path/filepath"

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

			var r Relo

			switch {
			case item.Detach:
				// @detach: Server#Start — find method Start on type Server.
				id := ix.FindFieldDef(item.Name, item.Field, item.Source)
				if id == nil {
					return nil, nil, fmt.Errorf("could not find method %q on type %q", item.Field, item.Name)
				}
				r.Ident = id
				r.Rename = item.FieldRename
				r.Detach = true

			case item.MethodOf != "":
				// @attach Server: Start — find function Start.
				id := ix.FindDef(item.Name, item.Source)
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
				id := ix.FindFieldDef(item.Name, item.Field, item.Source)
				if id == nil {
					return nil, nil, fmt.Errorf("could not find field %q in struct %q", item.Field, item.Name)
				}
				r.Ident = id
				r.Rename = item.FieldRename

			default:
				id := ix.FindDef(item.Name, item.Source)
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
