package rules

// File represents a parsed rules file.
type File struct {
	Directives []Directive
	Rules      []Rule
}

// Directive is a key-value pair declared with the "@" prefix.
type Directive struct {
	Key   string
	Value string
}

// Rule maps one or more items to a destination file path.
type Rule struct {
	Dest  string // destination file path
	Items []Item
}

// Item describes a single declaration to move or rename.
type Item struct {
	Source      string // optional source file or package path
	Name        string // declaration name
	Rename      string // optional new name for the declaration
	Field       string // optional field path (e.g. "Listen", "Limits.min")
	FieldRename string // optional new field name
	Detach      bool   // convert method to standalone function (Type#Name=!newName)
	MethodOf    string // convert function to method on this type (name=Type#Method)
}
