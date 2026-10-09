package main

// Edge cases that mutation testing found untested.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteLeavesAFormattedFileAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only file")
	}

	path := filepath.Join(t.TempDir(), "formatted.soml")
	writeFile(t, path, "a: 1\n")

	// A file that is not written cannot fail to be written.
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}

	if status, stdout, stderr := runCommand(t, "", "-w", path); status != 0 || stdout != "" || stderr != "" {
		t.Fatalf("got %d, %q, %q", status, stdout, stderr)
	}
}
