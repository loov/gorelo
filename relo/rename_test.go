package relo

import (
	"go/ast"
	"testing"

	"github.com/loov/gorelo/mast"
)

func TestTypeHasEmbeddedUses(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// fieldType identifies which form the embedded field takes.
		// We'll find the Use ident in the AST to match.
		findIdent func(file *ast.File) *ast.Ident
		want      bool
	}{
		{
			name: "simple ident embed",
			src:  "package p\ntype T struct{}\ntype S struct{ T }",
			findIdent: func(file *ast.File) *ast.Ident {
				// Find the T ident used in the embedded field of S.
				s := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				field := s.Type.(*ast.StructType).Fields.List[0]
				return field.Type.(*ast.Ident)
			},
			want: true,
		},
		{
			name: "star ident embed",
			src:  "package p\ntype T struct{}\ntype S struct{ *T }",
			findIdent: func(file *ast.File) *ast.Ident {
				s := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				field := s.Type.(*ast.StructType).Fields.List[0]
				return field.Type.(*ast.StarExpr).X.(*ast.Ident)
			},
			want: true,
		},
		{
			name: "selector embed (pkg.Type)",
			src:  "package p\nimport \"pkg\"\ntype S struct{ pkg.Type }",
			findIdent: func(file *ast.File) *ast.Ident {
				s := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				field := s.Type.(*ast.StructType).Fields.List[0]
				return field.Type.(*ast.SelectorExpr).Sel
			},
			want: true,
		},
		{
			name: "star selector embed (*pkg.Type)",
			src:  "package p\nimport \"pkg\"\ntype S struct{ *pkg.Type }",
			findIdent: func(file *ast.File) *ast.Ident {
				s := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				field := s.Type.(*ast.StructType).Fields.List[0]
				return field.Type.(*ast.StarExpr).X.(*ast.SelectorExpr).Sel
			},
			want: true,
		},
		{
			name: "non-embedded named field",
			src:  "package p\ntype T struct{}\ntype S struct{ x T }",
			findIdent: func(file *ast.File) *ast.Ident {
				s := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				field := s.Type.(*ast.StructType).Fields.List[0]
				return field.Type.(*ast.Ident)
			},
			want: false,
		},
		{
			name: "ident not in any embedded field",
			src:  "package p\ntype T struct{}\nvar x T",
			findIdent: func(file *ast.File) *ast.Ident {
				// Use the T ident in a var declaration, which is not an embedded field.
				v := file.Decls[1].(*ast.GenDecl).Specs[0].(*ast.ValueSpec)
				return v.Type.(*ast.Ident)
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, _ := parseSource(t, tt.src)
			ident := tt.findIdent(file)

			mastFile := &mast.File{
				Path:   "test.go",
				Syntax: file,
			}
			grp := &mast.Group{
				Idents: []*mast.Ident{
					{
						Ident: ident,
						File:  mastFile,
						Kind:  mast.Use,
					},
				},
			}

			got := typeHasEmbeddedUses(nil, grp)
			if got != tt.want {
				t.Errorf("typeHasEmbeddedUses() = %v, want %v", got, tt.want)
			}
		})
	}
}
