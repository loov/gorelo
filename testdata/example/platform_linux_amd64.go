//go:build linux && amd64

package example

// Arch is the CPU architecture string for linux/amd64.
var Arch = "amd64"

// ArchBits returns the pointer size for this platform.
func ArchBits() int { return 64 }
