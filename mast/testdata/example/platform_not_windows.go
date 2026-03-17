//go:build !windows

package example

// IsWindows reports whether the platform is Windows.
func IsWindows() bool { return false }
