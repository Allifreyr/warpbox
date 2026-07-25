package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHookWorkDirFromConfigPath(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(cfg, []byte("x: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Absolute config path → parent dir is absolute and equals dir.
	got := hookWorkDirFromConfigPath(cfg)
	if got != dir {
		// On some platforms TempDir may differ by clean form; compare Abs.
		wantAbs, _ := filepath.Abs(dir)
		gotAbs, _ := filepath.Abs(got)
		if gotAbs != wantAbs {
			t.Errorf("absolute config: got workDir %q, want %q", got, dir)
		}
	}

	// Relative config path (from dir) still resolves to an absolute workDir
	// pointing at that folder, not bare ".".
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	relGot := hookWorkDirFromConfigPath("config.yml")
	if !filepath.IsAbs(relGot) {
		t.Errorf("relative config: workDir %q should be absolute", relGot)
	}
	relAbs, _ := filepath.Abs(relGot)
	dirAbs, _ := filepath.Abs(dir)
	if relAbs != dirAbs {
		t.Errorf("relative config: workDir %q, want %q", relAbs, dirAbs)
	}
}

func TestRunItemsHook_WorkingDir(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	workDir := t.TempDir()

	// Write a script that creates a sentinel file in the working directory.
	script := filepath.Join(workDir, "test.sh")
	sentinel := filepath.Join(workDir, "hook_ran")
	scriptContent := []byte("#!/bin/sh\ntouch hook_ran\n")
	if err := os.WriteFile(script, scriptContent, 0755); err != nil {
		t.Fatal(err)
	}

	// Relative script path resolves against workDir (config file directory).
	runItemsHook("sh test.sh", 30, []string{"dummy"}, workDir)

	if _, err := os.Stat(sentinel); os.IsNotExist(err) {
		t.Error("script did not run in the expected working directory; sentinel file not found")
	}
}

func TestRunItemsHook_WorkingDirWithoutItems(t *testing.T) {
	// When items is empty, the hook should return early without executing.
	runItemsHook("sh anything.sh", 30, nil, t.TempDir())
}

func TestRunItemsHook_AbsolutePath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "abs.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch abs_hook_ran\n"), 0755); err != nil {
		t.Fatal(err)
	}

	workDir := t.TempDir()
	sentinel := filepath.Join(workDir, "abs_hook_ran")

	// Absolute script path; cmd.Dir still used for files the script creates.
	runItemsHook("sh "+script, 30, []string{"dummy"}, workDir)

	if _, err := os.Stat(sentinel); os.IsNotExist(err) {
		t.Error("absolute path script did not execute; sentinel file not found")
	}
}

func TestRunItemsHook_ErrorDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	runItemsHook("sh nonexistent.sh", 5, []string{"dummy"}, dir)
}
