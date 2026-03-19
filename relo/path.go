package relo

import (
	"os"
	"path/filepath"
)

// dirOf returns the directory component of a path.
func dirOf(p string) string {
	return filepath.Dir(p)
}

// baseName returns the base name of a path.
func baseName(p string) string {
	return filepath.Base(p)
}

// joinPath joins path elements.
func joinPath(elem ...string) string {
	return filepath.Join(elem...)
}

// relPath returns the relative path from base to target.
func relPath(base, target string) (string, error) {
	return filepath.Rel(base, target)
}

// toSlash converts path separators to forward slashes.
func toSlash(p string) string {
	return filepath.ToSlash(p)
}

// readFile reads a file from disk.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
