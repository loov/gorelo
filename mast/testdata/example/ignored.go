//go:build ignore

package example

// This file uses //go:build ignore. Mast should still parse and include it.
var IgnoredVar = "this file is normally excluded by go build"

func IgnoredFunc() string {
	return IgnoredVar
}
