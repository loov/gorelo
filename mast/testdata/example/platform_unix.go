//go:build linux || darwin

package example

// IsUnix reports whether the platform is Unix-like.
func IsUnix() bool { return true }
