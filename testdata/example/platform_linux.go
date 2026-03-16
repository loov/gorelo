//go:build linux

package example

import "example/linux"

// File is the platform-specific file handle for linux.
type File struct {
	Name string
	FD   uint64
}

func PlatformName() string {
	return linux.Name()
}

// Cross-package type used as field type and accessed.
func LinuxDistro() string {
	info := linux.GetInfo()
	return info.Distro
}
