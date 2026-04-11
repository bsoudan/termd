package tui

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestCleanupStaleOldBinaries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nxterm.exe")

	// Files that should be removed.
	cleanup := []string{
		"nxterm.exe.old",
		"nxterm.exe.old.1",
		"nxterm.exe.old.1700000000123456789",
		"nxterm.exe.old.42",
	}
	// Files that must be left alone.
	keep := []string{
		"nxterm.exe",          // the target itself
		"nxterm.exe.bak",      // unrelated suffix
		"nxterm.exe.old.txt",  // .old.<non-numeric>
		"other.exe.old",       // different basename
		"nxterm.exeold",       // missing dot before "old"
		"nxterm.exe.older",    // ".older" not ".old."
		"nxterm.exe.old.x",    // ".old.<non-numeric>"
	}

	for _, name := range append(cleanup, keep...) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	cleanupStaleOldBinaries(target)

	// Verify cleanup files are gone.
	for _, name := range cleanup {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s should have been removed", name)
		}
	}

	// Verify keep files are still present.
	for _, name := range keep {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should have been preserved: %v", name, err)
		}
	}

	// And verify the directory now contains exactly the keep set.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := append([]string(nil), keep...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("dir contents: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("dir contents[%d]: got %q, want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

func TestCleanupStaleOldBinariesNonexistentDir(t *testing.T) {
	// Should not panic on a non-existent target directory.
	cleanupStaleOldBinaries("/nonexistent/dir/nxterm.exe")
}
