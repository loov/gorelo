package rules

import (
	"reflect"
	"testing"
)

func TestParseForward(t *testing.T) {
	t.Parallel()

	input := `Server ServerOption -> server.go`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOption"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseReverse(t *testing.T) {
	t.Parallel()

	input := `server.go <- Server ServerOption`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOption"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultilineTab(t *testing.T) {
	t.Parallel()

	input := "server.go\t<-\n\tServer\n\tServerOption"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOption"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultiline(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tServer\n\tServerOption"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOption"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultilineIndentedComment(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tServer\n\t# a comment\n\tHandler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "Handler"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseRenames(t *testing.T) {
	t.Parallel()

	input := `Server=Core ServerOptions=Options -> server/core.go`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core.go",
		Items: []Item{
			{Name: "Server", Rename: "Core"},
			{Name: "ServerOptions", Rename: "Options"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseFieldRenames(t *testing.T) {
	t.Parallel()

	input := "server/core.go <-\n\tServerOptions=Options\n\tServerOptions#Listen=Address"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core.go",
		Items: []Item{
			{Name: "ServerOptions", Rename: "Options"},
			{Name: "ServerOptions", Field: "Listen", FieldRename: "Address"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAnonymousFieldRename(t *testing.T) {
	t.Parallel()

	input := "server/core.go <-\n\tServerOptions#Limits.min=Min"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core.go",
		Items: []Item{
			{Name: "ServerOptions", Field: "Limits.min", FieldRename: "Min"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseSourceFile(t *testing.T) {
	t.Parallel()

	input := "server/core_linux.go <-\n\tserver_linux.go:File"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core_linux.go",
		Items: []Item{
			{Source: "server_linux.go", Name: "File"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseSourceFilePath(t *testing.T) {
	t.Parallel()

	input := "server/core_linux.go <-\n\t./util/file_linux.go:File"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core_linux.go",
		Items: []Item{
			{Source: "./util/file_linux.go", Name: "File"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseSourcePackage(t *testing.T) {
	t.Parallel()

	input := "server/core_linux.go <-\n\t./util.File"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server/core_linux.go",
		Items: []Item{
			{Source: "./util", Name: "File"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseComments(t *testing.T) {
	t.Parallel()

	input := "# Move server types\nServer -> server.go\n# Done"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest:  "server.go",
		Items: []Item{{Name: "Server"}},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseInlineComment(t *testing.T) {
	t.Parallel()

	input := "server.go <- Server # move it"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest:  "server.go",
		Items: []Item{{Name: "Server"}},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultipleRules(t *testing.T) {
	t.Parallel()

	input := "server.go <- Server\n\nhandler.go <- Handler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{
		{Dest: "server.go", Items: []Item{{Name: "Server"}}},
		{Dest: "handler.go", Items: []Item{{Name: "Handler"}}},
	}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultilineMultipleRules(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tServer\n\nhandler.go <-\n\tHandler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{
		{Dest: "server.go", Items: []Item{{Name: "Server"}}},
		{Dest: "handler.go", Items: []Item{{Name: "Handler"}}},
	}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseEmpty(t *testing.T) {
	t.Parallel()

	file, err := Parse("test", []byte(""))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseCommentsOnly(t *testing.T) {
	t.Parallel()

	file, err := Parse("test", []byte("# just a comment\n# another"))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseRenameOnly(t *testing.T) {
	t.Parallel()

	input := `Foo=Bar`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Items: []Item{{Name: "Foo", Rename: "Bar"}},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseFieldRenameOnly(t *testing.T) {
	t.Parallel()

	input := "Config#Host=Hostname\nConfig#Port=ListenPort"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{
		{Items: []Item{{Name: "Config", Field: "Host", FieldRename: "Hostname"}}},
		{Items: []Item{{Name: "Config", Field: "Port", FieldRename: "ListenPort"}}},
	}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseFieldWithoutRenameNoArrow(t *testing.T) {
	t.Parallel()

	// A bare field reference without rename is valid (no-op field selection).
	input := `Server#Listen`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Items: []Item{{Name: "Server", Field: "Listen"}},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"no arrow", "Server"},
		{"missing dest", "Server -> "},
		{"missing items forward", " -> server.go"},
		{"missing dest reverse", " <- Server"},
		{"unexpected indent", "\tServer"},
		{"empty field", "server.go <- Server#"},
		{"empty name colon", "server.go <- file.go:"},
		{"empty rename", "server.go <- Server="},
		{"empty field rename", "server.go <- Server#Field="},
		{"empty field name in rename", "server.go <- Server#=New"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse("test", []byte(tt.input))
			if err == nil {
				t.Fatalf("expected error for input %q", tt.input)
			}
		})
	}
}

func TestParseFieldWithoutRename(t *testing.T) {
	t.Parallel()

	input := "server.go <- Server#Listen"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server", Field: "Listen"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveEquals(t *testing.T) {
	t.Parallel()

	input := `@stubs=true`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Directives: []Directive{{Key: "stubs", Value: "true"}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveSpace(t *testing.T) {
	t.Parallel()

	input := `@fmt goimports`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Directives: []Directive{{Key: "fmt", Value: "goimports"}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveNoValue(t *testing.T) {
	t.Parallel()

	input := `@verbose`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Directives: []Directive{{Key: "verbose"}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveWithRules(t *testing.T) {
	t.Parallel()

	input := "@fmt goimports\n@stubs=true\nServer -> server.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Directives) != 2 {
		t.Fatalf("got %d directives, want 2", len(file.Directives))
	}
	if file.Directives[0].Key != "fmt" || file.Directives[0].Value != "goimports" {
		t.Errorf("directive 0: %+v", file.Directives[0])
	}
	if file.Directives[1].Key != "stubs" || file.Directives[1].Value != "true" {
		t.Errorf("directive 1: %+v", file.Directives[1])
	}
	if len(file.Rules) != 1 || file.Rules[0].Dest != "server.go" {
		t.Errorf("rules: %+v", file.Rules)
	}
}

func TestParseDirectiveIndented(t *testing.T) {
	t.Parallel()

	input := "  @fmt goimports"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Directives: []Directive{{Key: "fmt", Value: "goimports"}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveBreaksMultiline(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tServer\n@fmt goimports\nhandler.go <- Handler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Directives) != 1 || file.Directives[0].Key != "fmt" {
		t.Errorf("directives: %+v", file.Directives)
	}
	if len(file.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(file.Rules))
	}
	if len(file.Rules[0].Items) != 1 || file.Rules[0].Items[0].Name != "Server" {
		t.Errorf("rule 0: %+v", file.Rules[0])
	}
}

func TestParseComplex(t *testing.T) {
	t.Parallel()

	input := `# Move server types to their own file:
Server ServerOption -> server.go

# Or alternatively using the reverse notation
server.go <- Server ServerOption

# Or using multiline notation:
server.go <-
    Server
    ServerOption

# It should also allow defining renames for moves:
Server=Core ServerOptions=Options -> server/core.go

# It should allow defining renames for field names:
server/core.go <-
    ServerOptions=Options
    ServerOptions#Listen=Address

# When a struct contains anonymous fields:
server/core.go <-
    ServerOptions#Limits.min=Min

# Source file references:
server/core_linux.go <-
    server_linux.go:File

server/core_linux.go <-
    ./util/file_linux.go:File

server/core_linux.go <-
    ./util.File
`
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}

	if len(file.Rules) != 9 {
		t.Fatalf("got %d rules, want 9", len(file.Rules))
	}

	// Spot-check a few rules.
	r := file.Rules[0]
	if r.Dest != "server.go" || len(r.Items) != 2 || r.Items[0].Name != "Server" {
		t.Errorf("rule 0: %+v", r)
	}

	r = file.Rules[3]
	if r.Dest != "server/core.go" || r.Items[0].Rename != "Core" || r.Items[1].Rename != "Options" {
		t.Errorf("rule 3: %+v", r)
	}

	r = file.Rules[5]
	if r.Dest != "server/core.go" || r.Items[0].Field != "Limits.min" || r.Items[0].FieldRename != "Min" {
		t.Errorf("rule 5: %+v", r)
	}

	r = file.Rules[7]
	if r.Dest != "server/core_linux.go" || r.Items[0].Source != "./util/file_linux.go" || r.Items[0].Name != "File" {
		t.Errorf("rule 7: %+v", r)
	}

	r = file.Rules[8]
	if r.Dest != "server/core_linux.go" || r.Items[0].Source != "./util" || r.Items[0].Name != "File" {
		t.Errorf("rule 8: %+v", r)
	}
}

func TestParseCRLF(t *testing.T) {
	t.Parallel()

	input := "server.go <-\r\n\tServer\r\n\tHandler\r\n"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "Handler"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseCR(t *testing.T) {
	t.Parallel()

	input := "server.go <- Server\rhandler.go <- Handler\r"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(file.Rules))
	}
}

func TestParseDirectiveTab(t *testing.T) {
	t.Parallel()

	input := "@fmt\tgoimports"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Directives: []Directive{{Key: "fmt", Value: "goimports"}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDirectiveEmptyKeyIsError(t *testing.T) {
	t.Parallel()

	// "@" alone, "@=value", and "@ " are not valid directives or rules.
	for _, input := range []string{"@", "@=value", "@ "} {
		_, err := Parse("test", []byte(input))
		if err == nil {
			t.Errorf("input %q: expected error", input)
		}
	}
}

func TestParseMultilineNoItems(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\n"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(file.Rules))
	}
	if len(file.Rules[0].Items) != 0 {
		t.Errorf("got %d items, want 0", len(file.Rules[0].Items))
	}
}

func TestParseMultilineContinuesWithoutBlank(t *testing.T) {
	t.Parallel()

	// A non-indented rule line ends the previous multiline block.
	input := "server.go <-\n\tServer\nhandler.go <- Handler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(file.Rules))
	}
	if file.Rules[0].Dest != "server.go" || len(file.Rules[0].Items) != 1 {
		t.Errorf("rule 0: %+v", file.Rules[0])
	}
	if file.Rules[1].Dest != "handler.go" || len(file.Rules[1].Items) != 1 {
		t.Errorf("rule 1: %+v", file.Rules[1])
	}
}

func TestParseSourceWithRename(t *testing.T) {
	t.Parallel()

	input := "server.go <- file.go:Server=Core"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "file.go", Name: "Server", Rename: "Core"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseSourceWithFieldRename(t *testing.T) {
	t.Parallel()

	input := "server.go <- ./util.Server#Listen=Address"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "./util", Name: "Server", Field: "Listen", FieldRename: "Address"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAbsolutePackage(t *testing.T) {
	t.Parallel()

	input := "server.go <- github.com/loov/gorelo.Server"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "github.com/loov/gorelo", Name: "Server"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAbsolutePackageWithRename(t *testing.T) {
	t.Parallel()

	input := "server.go <- github.com/loov/gorelo.Server=Core"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "github.com/loov/gorelo", Name: "Server", Rename: "Core"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAbsolutePackageWithFieldRename(t *testing.T) {
	t.Parallel()

	input := "server.go <- github.com/loov/gorelo.Server#Listen=Address"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "github.com/loov/gorelo", Name: "Server", Field: "Listen", FieldRename: "Address"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAbsolutePackageMultiline(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tgithub.com/loov/gorelo.Server\n\tgithub.com/loov/gorelo.Handler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Source: "github.com/loov/gorelo", Name: "Server"},
			{Source: "github.com/loov/gorelo", Name: "Handler"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseMultipleItemsOnMultilineLine(t *testing.T) {
	t.Parallel()

	// An indented line can contain multiple space-separated items.
	input := "server.go <-\n\tServer Handler"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules[0].Items) != 2 {
		t.Fatalf("got %d items, want 2", len(file.Rules[0].Items))
	}
}

func TestParseFieldRenameInMoveBlock(t *testing.T) {
	t.Parallel()

	input := "server.go <-\n\tServer\n\tServerOptions#Listen=Address"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOptions", Field: "Listen", FieldRename: "Address"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseFieldRenameInForwardBlock(t *testing.T) {
	t.Parallel()

	input := "Server ServerOptions#Listen=Address -> server.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Server"},
			{Name: "ServerOptions", Field: "Listen", FieldRename: "Address"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDetach(t *testing.T) {
	t.Parallel()

	input := "@detach\nServer#Start -> util.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "util.go",
		Items: []Item{
			{Name: "Server", Field: "Start", Detach: true},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDetachWithRename(t *testing.T) {
	t.Parallel()

	input := "@detach\nServer#Start=StartServer -> util.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "util.go",
		Items: []Item{
			{Name: "Server", Field: "Start", FieldRename: "StartServer", Detach: true},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDetachMultiline(t *testing.T) {
	t.Parallel()

	input := "@detach\nutil.go <-\n\tServer#Start\n\tServer#Stop"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "util.go",
		Items: []Item{
			{Name: "Server", Field: "Start", Detach: true},
			{Name: "Server", Field: "Stop", Detach: true},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDetachSameFile(t *testing.T) {
	t.Parallel()

	input := "@detach\nServer#Start"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Items: []Item{
			{Name: "Server", Field: "Start", Detach: true},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseDetachDoesNotApplyToNextRule(t *testing.T) {
	t.Parallel()

	input := "@detach\nServer#Start\n\nHandler -> handler.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(file.Rules))
	}
	if !file.Rules[0].Items[0].Detach {
		t.Error("expected detach on first rule")
	}
	if file.Rules[1].Items[0].Detach {
		t.Error("unexpected detach on second rule")
	}
}

func TestParseDetachBlankLineBefore(t *testing.T) {
	t.Parallel()

	// A blank line between @detach and the rule should not eat the directive.
	input := "@detach\n\nServer#Start"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(file.Rules))
	}
	if !file.Rules[0].Items[0].Detach {
		t.Error("expected detach on rule after blank line")
	}
}

func TestParseAttach(t *testing.T) {
	t.Parallel()

	input := "@attach Server\nStart -> server.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "Start", MethodOf: "Server"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAttachWithRename(t *testing.T) {
	t.Parallel()

	input := "@attach Server\nStartServer=Start -> server.go"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{Rules: []Rule{{
		Dest: "server.go",
		Items: []Item{
			{Name: "StartServer", Rename: "Start", MethodOf: "Server"},
		},
	}}}
	if !reflect.DeepEqual(file, want) {
		t.Errorf("got %+v, want %+v", file, want)
	}
}

func TestParseAttachNoTypeName(t *testing.T) {
	t.Parallel()

	_, err := Parse("test", []byte("@attach\nStart"))
	if err == nil {
		t.Fatal("expected error for @attach without type name")
	}
}

func TestParseDetachThenAttach(t *testing.T) {
	t.Parallel()

	// @attach after @detach should override.
	input := "@detach\n@attach Server\nStart"
	file, err := Parse("test", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(file.Rules))
	}
	item := file.Rules[0].Items[0]
	if item.Detach {
		t.Error("expected detach to be overridden by @attach")
	}
	if item.MethodOf != "Server" {
		t.Errorf("got MethodOf=%q, want Server", item.MethodOf)
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"# comment",
		"@fmt goimports",
		"@stubs=true",
		"@verbose",
		"Server -> server.go",
		"server.go <- Server",
		"server.go <-\n\tServer\n\tHandler",
		"Server=Core -> server.go",
		"server.go <- S#Listen=Addr",
		"server.go <- S#Limits.min=Min",
		"server.go <- file.go:Server",
		"server.go <- ./util.File",
		"server.go <- ./util/file.go:File",
		"server.go <- github.com/loov/gorelo.Server",
		"server.go <- github.com/loov/gorelo.Server=Core",
		"server.go <- github.com/loov/gorelo.Server#F=G",
		"Foo=Bar",
		"Server#Listen=Address",
		"Server#Listen",
		"A B C -> x.go",
		"x.go <- A=B C#D=E",
		"# comment\nServer -> server.go\n# end",
		"server.go <-\r\n\tServer\r\n",
		"@ ",
		"@=val",
		"@detach\nServer#Start -> util.go",
		"@attach Server\nStart -> server.go",
		"@detach\nutil.go <-\n\tServer#Start\n\tServer#Stop",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		file, err := Parse("fuzz", []byte(input))
		if err != nil {
			return // parse errors are fine
		}

		// Validate invariants on successfully parsed output.
		for ri, r := range file.Rules {
			for ii, item := range r.Items {
				if item.Name == "" {
					t.Fatalf("rule %d item %d: empty name", ri, ii)
				}
				if item.Field != "" && item.Rename != "" {
					t.Fatalf("rule %d item %d: has both field %q and rename %q", ri, ii, item.Field, item.Rename)
				}
				if item.FieldRename != "" && item.Field == "" {
					t.Fatalf("rule %d item %d: has field rename %q without field", ri, ii, item.FieldRename)
				}
				if item.Detach && item.MethodOf != "" {
					t.Fatalf("rule %d item %d: has both Detach and MethodOf", ri, ii)
				}
			}
		}
		for di, d := range file.Directives {
			if d.Key == "" {
				t.Fatalf("directive %d: empty key", di)
			}
		}
	})
}
