package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runCommand(t *testing.T, stdin string, arguments ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := run(arguments, strings.NewReader(stdin), &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestStandardInput(t *testing.T) {
	status, stdout, stderr := runCommand(t, "a: {b: 1}  # Note.")

	if status != 0 || stdout != "a: {b: 1} # Note.\n" || stderr != "" {
		t.Fatalf("got %d %q %q", status, stdout, stderr)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestOutputErrors(t *testing.T) {
	for _, arguments := range [][]string{nil, {"-l"}, {"-d"}} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			status := run(arguments, strings.NewReader("a:1"), failingWriter{}, &stderr)

			if status != 1 || !strings.Contains(stderr.String(), io.ErrClosedPipe.Error()) {
				t.Fatalf("got status %d and error %q", status, stderr.String())
			}
		})
	}
}

func TestAnInvalidDocumentGivesItsPosition(t *testing.T) {
	status, stdout, stderr := runCommand(t, "a: 1\na: 2\n")

	if status != 1 || stdout != "" || stderr != "<standard input>:2:1: duplicate key a\n" {
		t.Fatalf("got %d %q %q", status, stdout, stderr)
	}
}

func TestListAndWrite(t *testing.T) {
	folder := t.TempDir()
	unformatted := filepath.Join(folder, "config", "a.soml")
	formatted := filepath.Join(folder, "b.soml")
	other := filepath.Join(folder, "notes.txt")
	writeFile(t, unformatted, "a: [1,2]")
	writeFile(t, formatted, "a: 1\n")
	writeFile(t, other, "not: [SOML")

	status, stdout, stderr := runCommand(t, "", "-l", folder)

	if status != 0 || stdout != unformatted+"\n" || stderr != "" {
		t.Fatalf("-l gave %d %q %q", status, stdout, stderr)
	}

	status, stdout, stderr = runCommand(t, "", "-w", folder)

	if status != 0 || stdout != "" || stderr != "" {
		t.Fatalf("-w gave %d %q %q", status, stdout, stderr)
	}

	if text := readFile(t, unformatted); text != "a: [1, 2]\n" {
		t.Fatalf("the file is %q", text)
	}

	if status, stdout, _ = runCommand(t, "", "-l", folder); status != 0 || stdout != "" {
		t.Fatalf("-l after -w gave %d %q", status, stdout)
	}

	if text := readFile(t, other); text != "not: [SOML" {
		t.Fatal("a file that is not .soml in a folder was changed")
	}
}

func TestAFileGivenByNameIsFormattedWhateverItsName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "a:1")
	status, stdout, _ := runCommand(t, "", path)

	if status != 0 || stdout != "a: 1\n" {
		t.Fatalf("got %d %q", status, stdout)
	}
}

func TestAnErrorInOneFileDoesNotStopTheOthers(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, filepath.Join(folder, "a.soml"), "a: 1\na: 2")
	writeFile(t, filepath.Join(folder, "b.soml"), "b:2")
	status, stdout, stderr := runCommand(t, "", "-l", folder)

	if status != 1 || stdout != filepath.Join(folder, "b.soml")+"\n" || !strings.Contains(stderr, "a.soml:2:1: duplicate key") {
		t.Fatalf("got %d %q %q", status, stdout, stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	if status, _, stderr := runCommand(t, "", "-w"); status != 2 || !strings.Contains(stderr, "-w needs a path") {
		t.Fatalf("got %d %q", status, stderr)
	}

	if status, _, _ := runCommand(t, "", "-x"); status != 2 {
		t.Fatalf("an unknown flag gave %d", status)
	}

	if status, _, stderr := runCommand(t, "", filepath.Join(t.TempDir(), "missing.soml")); status != 1 || stderr == "" {
		t.Fatalf("a missing file gave %d %q", status, stderr)
	}
}

func TestAFileThatCannotBeReadDoesNotStopTheWalk(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read every file")
	}

	// Chmod does not make a file unreadable on Windows, so the walk finds no error there and the check of the walk's behavior is moot.
	if runtime.GOOS == "windows" {
		t.Skip("a file cannot be made unreadable on Windows")
	}

	folder := t.TempDir()
	unreadable := filepath.Join(folder, "a.soml")
	writeFile(t, unreadable, "a: 1\n")
	writeFile(t, filepath.Join(folder, "b.soml"), "b:2")
	writeFile(t, filepath.Join(folder, "sub", "c.soml"), "c:3")

	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}

	status, stdout, stderr := runCommand(t, "", "-l", folder)

	if status != 1 || stdout != filepath.Join(folder, "b.soml")+"\n"+filepath.Join(folder, "sub", "c.soml")+"\n" || !strings.Contains(stderr, "a.soml") {
		t.Fatalf("got %d %q %q", status, stdout, stderr)
	}
}

func TestASymbolicLinkToAFolderIsFollowed(t *testing.T) {
	folder := t.TempDir()
	writeFile(t, filepath.Join(folder, "real", "a.soml"), "a:1")
	link := filepath.Join(folder, "link")

	if err := os.Symlink(filepath.Join(folder, "real"), link); err != nil {
		t.Skip(err)
	}

	status, stdout, stderr := runCommand(t, "", "-l", link)

	if status != 0 || stdout != filepath.Join(link, "a.soml")+"\n" || stderr != "" {
		t.Fatalf("got %d %q %q", status, stdout, stderr)
	}
}

func TestADotFileInAFolderIsSkipped(t *testing.T) {
	folder := t.TempDir()

	// An AppleDouble file, which macOS makes next to a file on a volume without extended attributes, and which is not SOML.
	appleDouble := filepath.Join(folder, "._config.soml")
	writeFile(t, appleDouble, "\x00\x05\x16\x07\x00\x02\x00\x00Mac OS X")
	hidden := filepath.Join(folder, "sub", ".draft.soml")
	writeFile(t, hidden, "a:1")
	writeFile(t, filepath.Join(folder, "sub", ".soml"), "a:1")
	writeFile(t, filepath.Join(folder, "config.soml"), "b:2")

	status, stdout, stderr := runCommand(t, "", "-l", "-w", folder)

	if status != 0 || stderr != "" || stdout != filepath.Join(folder, "config.soml")+"\n" || readFile(t, hidden) != "a:1" {
		t.Fatalf("got %d %q %q, and the hidden file is %q", status, stdout, stderr, readFile(t, hidden))
	}

	// A dot-file given by name is formatted, as any file given by name.
	if status, stdout, _ := runCommand(t, "", hidden); status != 0 || stdout != "a: 1\n" {
		t.Fatalf("by name: got %d %q", status, stdout)
	}
}

func TestASymbolicLinkInAFolderIsSkipped(t *testing.T) {
	folder := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.soml")
	writeFile(t, outside, "a:1")
	writeFile(t, filepath.Join(folder, "b.soml"), "b:2")

	if err := os.Symlink(outside, filepath.Join(folder, "link.soml")); err != nil {
		t.Skip(err)
	}

	status, _, stderr := runCommand(t, "", "-w", folder)

	if status != 0 || stderr != "" || readFile(t, outside) != "a:1" || readFile(t, filepath.Join(folder, "b.soml")) != "b: 2\n" {
		t.Fatalf("got %d %q, and the file outside is %q", status, stderr, readFile(t, outside))
	}
}

func TestDiff(t *testing.T) {
	t.Run("writes the diff of the standard input and exits with 1", func(t *testing.T) {
		status, stdout, stderr := runCommand(t, "a:   1\nb: 2\n", "-d")
		expected := "diff <standard input>.orig <standard input>\n--- <standard input>.orig\n+++ <standard input>\n@@ -1,2 +1,2 @@\n-a:   1\n+a: 1\n b: 2\n"

		if status != 1 || stdout != expected || stderr != "" {
			t.Fatalf("got %d %q %q", status, stdout, stderr)
		}
	})

	t.Run("writes nothing for a formatted document and exits with 0", func(t *testing.T) {
		status, stdout, stderr := runCommand(t, "a: 1\n", "-d")

		if status != 0 || stdout != "" || stderr != "" {
			t.Fatalf("got %d %q %q", status, stdout, stderr)
		}
	})

	t.Run("names each file in a folder and writes it with -w", func(t *testing.T) {
		folder := t.TempDir()
		unformatted := filepath.Join(folder, "a.soml")
		writeFile(t, unformatted, "a:   1\n")
		writeFile(t, filepath.Join(folder, "b.soml"), "b: 1\n")
		status, stdout, stderr := runCommand(t, "", "-d", "-w", folder)
		name := filepath.ToSlash(unformatted)
		expected := "diff " + name + ".orig " + name + "\n--- " + name + ".orig\n+++ " + name + "\n@@ -1,1 +1,1 @@\n-a:   1\n+a: 1\n"

		if status != 1 || stdout != expected || stderr != "" {
			t.Fatalf("got %d %q %q", status, stdout, stderr)
		}

		if text := readFile(t, unformatted); text != "a: 1\n" {
			t.Fatalf("the file is %q", text)
		}
	})

	t.Run("an invalid document is still an error", func(t *testing.T) {
		status, stdout, stderr := runCommand(t, "a: 1\na: 2\n", "-d")

		if status != 1 || stdout != "" || stderr != "<standard input>:2:1: duplicate key a\n" {
			t.Fatalf("got %d %q %q", status, stdout, stderr)
		}
	})
}
