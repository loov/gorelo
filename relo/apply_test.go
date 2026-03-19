package relo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPlan_WriteNewFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "new.go")

	plan := &Plan{
		Edits: []FileEdit{
			{Path: path, IsNew: true, Content: "package p\n"},
		},
	}
	if err := applyPlan(plan); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package p\n" {
		t.Errorf("file content = %q, want %q", string(data), "package p\n")
	}
}

func TestApplyPlan_WriteNewFileCreatesDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "new.go")

	plan := &Plan{
		Edits: []FileEdit{
			{Path: path, IsNew: true, Content: "package p\n"},
		},
	}
	if err := applyPlan(plan); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("new file does not exist: %v", err)
	}
}

func TestApplyPlan_ModifyExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "existing.go")
	os.WriteFile(path, []byte("package old\n"), 0644)

	plan := &Plan{
		Edits: []FileEdit{
			{Path: path, Content: "package new\n"},
		},
	}
	if err := applyPlan(plan); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "package new\n" {
		t.Errorf("file content = %q, want %q", string(data), "package new\n")
	}
}

func TestApplyPlan_DeleteFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "todelete.go")
	os.WriteFile(path, []byte("package p\n"), 0644)

	plan := &Plan{
		Edits: []FileEdit{
			{Path: path, IsDelete: true},
		},
	}
	if err := applyPlan(plan); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted, but it still exists")
	}
}

func TestApplyPlan_DeleteNonexistent(t *testing.T) {
	t.Parallel()

	plan := &Plan{
		Edits: []FileEdit{
			{Path: "/tmp/relo_test_nonexistent_file.go", IsDelete: true},
		},
	}
	err := applyPlan(plan)
	if err == nil {
		t.Error("expected error when deleting non-existent file")
	}
}

func TestApplyPlan_EmptyPlan(t *testing.T) {
	t.Parallel()

	plan := &Plan{}
	if err := applyPlan(plan); err != nil {
		t.Errorf("empty plan should not error: %v", err)
	}
}

func TestApplyPlan_MultipleEdits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.go")
	path2 := filepath.Join(dir, "b.go")
	path3 := filepath.Join(dir, "c.go")
	os.WriteFile(path3, []byte("package old\n"), 0644)

	plan := &Plan{
		Edits: []FileEdit{
			{Path: path1, IsNew: true, Content: "package a\n"},
			{Path: path2, IsNew: true, Content: "package b\n"},
			{Path: path3, IsDelete: true},
		},
	}
	if err := applyPlan(plan); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path1); err != nil {
		t.Error("a.go should exist")
	}
	if _, err := os.Stat(path2); err != nil {
		t.Error("b.go should exist")
	}
	if _, err := os.Stat(path3); !os.IsNotExist(err) {
		t.Error("c.go should be deleted")
	}
}
