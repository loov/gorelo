//go:build linux && arm64

package example

// Arch is the CPU architecture string for linux/arm64.
var Arch = "arm64"

// ArchBits returns the pointer size for this platform.
func ArchBits() int { return 64 }
