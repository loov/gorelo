//go:build windows

package example

import "example/windows"

// File is the platform-specific file handle for windows.
type File struct {
	Name   string
	Handle uint64
}

func PlatformName() string {
	return windows.Name()
}
