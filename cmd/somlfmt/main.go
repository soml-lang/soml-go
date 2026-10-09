/*
Somlfmt formats SOML documents, as gofmt formats Go code.

It changes only the layout, as the formatter in the SOML specification says: the comments, the member order, and the spelling of every value stay.

Usage:

	somlfmt [flags] [path ...]

Without a path, it formats the standard input and writes the result to the standard output. A path can be a file or a folder, and a folder is searched for .soml files, except those whose name starts with a dot, as gofmt does. A symbolic link given as a path is followed, but one found in a folder is skipped.

The flags are:

	-d
		Write the changes as a unified diff, instead of writing the formatted text.
	-l
		List the files whose formatting differs, instead of writing the formatted text.
	-w
		Write the result to each file, instead of to the standard output.

The exit status is 2 for a wrong flag or -w without a path, 1 when a file is not a valid document, or cannot be read or written, or with -d when the formatting of a file differs, as gofmt -d does, and 0 otherwise. An error in one file does not stop the others. To check in CI that every file is formatted, and show what is not:

	somlfmt -d .
*/
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/soml-lang/soml-go"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type options struct {
	diff  bool
	list  bool
	write bool
}

// run runs the command with arguments, and returns its exit status.
func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("somlfmt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := options{}
	flags.BoolVar(&options.diff, "d", false, "write the changes as a unified diff")
	flags.BoolVar(&options.list, "l", false, "list the files whose formatting differs")
	flags.BoolVar(&options.write, "w", false, "write the result to each file")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: somlfmt [flags] [path ...]")
		flags.PrintDefaults()
	}

	if err := flags.Parse(arguments); err != nil {
		return 2
	}

	if flags.NArg() == 0 {
		if options.write {
			fmt.Fprintln(stderr, "somlfmt: -w needs a path")
			return 2
		}

		data, err := io.ReadAll(stdin)

		if err != nil {
			fmt.Fprintln(stderr, "somlfmt:", err)
			return 1
		}

		// The name gofmt gives the standard input in its messages.
		isChanged, err := formatFile("<standard input>", data, options, stdout)

		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}

		return exitStatus(isChanged, options)
	}

	status := 0

	for _, root := range flags.Args() {
		// A symbolic link to a folder, given by name, is followed. The walk itself does not follow links.
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			root += string(filepath.Separator)
		}

		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			// An error is reported, and the walk goes on, so that one file that cannot be read does not hide the others.
			isChanged := false

			if err == nil {
				isChanged, err = formatPath(root, path, entry, options, stdout)
			}

			if err != nil {
				fmt.Fprintln(stderr, err)
				status = 1
			}

			status = max(status, exitStatus(isChanged, options))
			return nil
		})
	}

	return status
}

// exitStatus is the exit status for a document that was formatted without an error: 1 with -d when its formatting differs, as gofmt -d does, and 0 otherwise.
func exitStatus(isChanged bool, options options) int {
	if options.diff && isChanged {
		return 1
	}

	return 0
}

// formatPath formats one path of a walk: a file given by name whatever its name, and a .soml file in a folder. A symbolic link in a folder is skipped, as the walk does not follow links, so that -w cannot write outside the folder. A file in a folder whose name starts with a dot is skipped too, as gofmt does, such as the AppleDouble file `._config.soml` that macOS makes on some volumes, which is not SOML. It reports whether the formatting of the file differs.
func formatPath(root, path string, entry fs.DirEntry, options options, stdout io.Writer) (bool, error) {
	if path != root && (entry.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(path) != ".soml") || entry.IsDir() {
		return false, nil
	}

	data, err := os.ReadFile(path)

	if err != nil {
		return false, err
	}

	return formatFile(path, data, options, stdout)
}

// formatFile formats one document, and writes it, writes its diff, lists its path, or writes it back to its file. It reports whether its formatting differs.
func formatFile(path string, data []byte, options options, stdout io.Writer) (bool, error) {
	formatted, err := soml.Format(data)

	if err != nil {
		if somlError, ok := errors.AsType[*soml.Error](err); ok && somlError.Line != 0 {
			return false, fmt.Errorf("%s:%d:%d: %s", path, somlError.Line, somlError.Column, somlError.Message)
		}

		return false, fmt.Errorf("%s: %w", path, err)
	}

	isChanged := !bytes.Equal(data, formatted)

	if options.list && isChanged {
		if _, err := fmt.Fprintln(stdout, path); err != nil {
			return isChanged, err
		}
	}

	if options.write && isChanged {
		info, err := os.Stat(path)

		if err != nil {
			return isChanged, err
		}

		// The file is written in place, which keeps its owner and its links, as gofmt does. When that fails, the original text is written back, so the file is not left half written.
		if err := os.WriteFile(path, formatted, info.Mode().Perm()); err != nil {
			_ = os.WriteFile(path, data, info.Mode().Perm())
			return isChanged, err
		}
	}

	// After the file is written, so a diff is only shown for a change that happened.
	if options.diff && isChanged {
		// Named as gofmt -d names them, with slashes on every system.
		name := filepath.ToSlash(path)

		if _, err := io.WriteString(stdout, unifiedDiff(name+".orig", string(data), name, string(formatted))); err != nil {
			return isChanged, err
		}
	}

	if !options.list && !options.diff && !options.write {
		_, err := stdout.Write(formatted)
		return isChanged, err
	}

	return isChanged, nil
}
