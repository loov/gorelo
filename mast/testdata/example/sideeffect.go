//go:build linux

package example

import _ "example/linux"

// Side-effect import: the _ ident should be untracked.
// This file just ensures the import doesn't cause issues.
var SideEffectVar = "loaded"
