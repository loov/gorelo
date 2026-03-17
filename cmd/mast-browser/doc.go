// Command mast-browser serves a web UI for browsing Go packages with
// identifier rename-group highlighting.
//
// It loads all packages under a directory (including all build
// constraints and test files), then presents the source in a browser.
// Clicking any identifier highlights every occurrence that belongs to
// the same rename group and opens a side panel showing all references
// with surrounding context.
//
// # Installation
//
//	go install github.com/loov/gorelo/cmd/mast-browser@latest
//
// # Usage
//
//	mast-browser -dir . -listen 127.0.0.1:8080
//
// Flags:
//
//	-dir      directory to load (default ".")
//	-listen   listen address (default "127.0.0.1:8080")
package main
