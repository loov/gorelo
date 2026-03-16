//go:build linux

package example

import (
	lnx "example/linux"
)

// Renamed import: lnx is a PackageName def.
func LinuxNameRenamed() string {
	return lnx.Name()
}

// Using the renamed import's type.
func LinuxInfoRenamed() lnx.Info {
	return lnx.GetInfo()
}
