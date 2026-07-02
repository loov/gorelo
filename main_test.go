package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"gorelo": func() { os.Exit(run()) },
	})
}

// TestScript is the CLI's primary end-to-end suite. It is deliberately not
// tagged as an integration test — the scripts under testdata/script are the
// package's main tests and run by default.
func TestScript(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			// Inherit the parent environment so the go toolchain can find
			// its caches and platform-specific directories (notably GOCACHE
			// and %LocalAppData% on Windows). HOME and GOPATH are then
			// overridden to keep test state isolated to the work directory.
			env.Vars = append(env.Vars, os.Environ()...)
			env.Setenv("HOME", filepath.Join(env.WorkDir, "home"))
			env.Setenv("GOPATH", filepath.Join(env.WorkDir, "gopath"))
			return nil
		},
	})
}
