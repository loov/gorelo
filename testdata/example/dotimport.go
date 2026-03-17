//go:build linux

package example

import . "example/linux"

// Dot import: Name() is called without package qualifier.
// It should link to linux.Name's group.
func DotImportName() string {
	return Name()
}

// Dot import: Info is used without package qualifier.
func DotImportInfo() Info {
	return GetInfo()
}
