//go:build windows

package example

import (
	lnx "example/windows"
)

// Same import alias "lnx" but pointing to a different package.
// This should be in a separate group from lnx in imports.go.
func WindowsNameRenamed() string {
	return lnx.Name()
}
