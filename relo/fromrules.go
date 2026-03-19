package relo

import (
	"fmt"
	"path/filepath"

	"github.com/loov/gorelo/mast"
	"github.com/loov/gorelo/rules"
)

// FromRules converts parsed rules into Relo instructions by resolving
// each item's definition in the index. The dir parameter is joined with
// rule destinations to produce absolute MoveTo paths; pass "." to use
// destinations as-is.
func FromRules(ix *mast.Index, parsed []rules.Rule, dir string) ([]Relo, error) {
	var relos []Relo
	for _, rule := range parsed {
		for _, item := range rule.Items {
			var r Relo
			if item.Field != "" {
				id := ix.FindFieldDef(item.Name, item.Field, item.Source)
				if id == nil {
					return nil, fmt.Errorf("could not find field %q in struct %q", item.Field, item.Name)
				}
				r.Ident = id
				r.Rename = item.FieldRename
			} else {
				id := ix.FindDef(item.Name, item.Source)
				if id == nil {
					src := ""
					if item.Source != "" {
						src = " in " + item.Source
					}
					return nil, fmt.Errorf("could not find definition for %q%s", item.Name, src)
				}
				r.Ident = id
				r.Rename = item.Rename
			}
			if rule.Dest != "" {
				r.MoveTo = filepath.Join(dir, rule.Dest)
			}
			relos = append(relos, r)
		}
	}
	return relos, nil
}
