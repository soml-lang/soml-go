package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		diff          string
	}{
		{"no difference", "a\nb\n", "a\nb\n", ""},
		{
			"a change with its context",
			"1\n2\n3\n4\n5\n6\n7\n8\n9\n",
			"1\n2\n3\n4\nfive\n6\n7\n8\n9\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n",
		},
		{
			"changes with five equal lines between them share a hunk",
			"1\n2\n3\n4\n5\n6\n7\n",
			"one\n2\n3\n4\n5\n6\nseven\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -1,7 +1,7 @@\n-1\n+one\n 2\n 3\n 4\n 5\n 6\n-7\n+seven\n",
		},
		{
			"changes with six equal lines between them get their own hunks, as in gofmt -d",
			"1\n2\n3\n4\n5\n6\n7\n8\n",
			"one\n2\n3\n4\n5\n6\n7\neight\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -1,4 +1,4 @@\n-1\n+one\n 2\n 3\n 4\n@@ -5,4 +5,4 @@\n 5\n 6\n 7\n-8\n+eight\n",
		},
		{
			"changes far apart get their own hunks",
			"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n",
			"one\n2\n3\n4\n5\n6\n7\n8\n9\nten\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -1,4 +1,4 @@\n-1\n+one\n 2\n 3\n 4\n@@ -7,4 +7,4 @@\n 7\n 8\n 9\n-10\n+ten\n",
		},
		{
			"an added line",
			"a\nb\n",
			"a\nnew\nb\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -1,2 +1,3 @@\n a\n+new\n b\n",
		},
		{
			"a missing final line feed",
			"a\nb",
			"a\nb\n",
			"diff a.orig a\n--- a.orig\n+++ a\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if diff := unifiedDiff("a.orig", test.before, "a", test.after); diff != test.diff {
				t.Fatalf("got\n%s\nwant\n%s", diff, test.diff)
			}
		})
	}
}

// Every diff, applied to the text before, gives the text after, and every hunk has the line numbers and counts of its lines.
func TestUnifiedDiffAppliesBack(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))

	for range 2000 {
		before := randomLines(random)
		after := editLines(random, before)
		beforeText, afterText := strings.Join(before, ""), strings.Join(after, "")
		diff := unifiedDiff("a.orig", beforeText, "a", afterText)

		if applied, err := applyDiff(beforeText, diff); err != nil || applied != afterText {
			t.Fatalf("the diff of %q and %q is\n%s\nwhich gives %q, %v", beforeText, afterText, diff, applied, err)
		}
	}
}

func randomLines(random *rand.Rand) []string {
	lines := make([]string, random.IntN(30))

	for index := range lines {
		// Few distinct lines, so that many lines repeat, as braces and blank lines do in a document.
		lines[index] = strconv.Itoa(random.IntN(6)) + "\n"
	}

	if len(lines) > 0 && random.IntN(4) == 0 {
		lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
	}

	return lines
}

func editLines(random *rand.Rand, lines []string) []string {
	edited := append([]string(nil), lines...)

	for range random.IntN(6) {
		position := random.IntN(len(edited) + 1)

		switch random.IntN(3) {
		case 0:
			edited = append(edited[:position], append([]string{strconv.Itoa(random.IntN(9)) + "\n"}, edited[position:]...)...)
		case 1:
			if position < len(edited) {
				edited = append(edited[:position], edited[position+1:]...)
			}
		default:
			if position < len(edited) {
				edited[position] = strconv.Itoa(random.IntN(9)) + "\n"
			}
		}
	}

	// Only the last line may lack a line feed.
	for index := range len(edited) - 1 {
		if !strings.HasSuffix(edited[index], "\n") {
			edited[index] += "\n"
		}
	}

	return edited
}

// applyDiff applies a unified diff to text, and checks the line numbers and counts of each hunk and the lines of its context.
func applyDiff(text, diff string) (string, error) {
	if diff == "" {
		return text, nil
	}

	lines := splitLines(text)
	diffLines := splitLines(diff)[3:]
	var output []string
	next := 0

	for len(diffLines) > 0 {
		var beforeStart, beforeCount, afterStart, afterCount int

		if _, err := fmt.Sscanf(diffLines[0], "@@ -%d,%d +%d,%d @@\n", &beforeStart, &beforeCount, &afterStart, &afterCount); err != nil {
			return "", fmt.Errorf("hunk header %q: %w", diffLines[0], err)
		}

		diffLines = diffLines[1:]

		// An empty side is numbered by the line before it.
		if beforeCount > 0 {
			beforeStart--
		}

		if afterCount > 0 {
			afterStart--
		}

		output = append(output, lines[next:beforeStart]...)
		next = beforeStart

		if len(output) != afterStart {
			return "", fmt.Errorf("a hunk starts at line %d after, but %d lines come before it", afterStart+1, len(output))
		}

		removed, added := 0, 0

		for len(diffLines) > 0 && !strings.HasPrefix(diffLines[0], "@@") {
			kind, line := diffLines[0][0], diffLines[0][1:]
			diffLines = diffLines[1:]

			if len(diffLines) > 0 && diffLines[0] == "\\ No newline at end of file\n" {
				line = strings.TrimSuffix(line, "\n")
				diffLines = diffLines[1:]
			}

			if kind != '+' {
				if next >= len(lines) || lines[next] != line {
					return "", fmt.Errorf("line %d is not %q", next+1, line)
				}

				next++
				removed++
			}

			if kind != '-' {
				output = append(output, line)
				added++
			}
		}

		if removed != beforeCount || added != afterCount {
			return "", fmt.Errorf("a hunk has %d and %d lines, but its header says %d and %d", removed, added, beforeCount, afterCount)
		}
	}

	return strings.Join(append(output, lines[next:]...), ""), nil
}

// Every diff applies with patch, and gives the text after.
func TestDiffAppliesWithPatch(t *testing.T) {
	// The patch of Git for Windows converts the line endings of a file it patches, so the text after it is not the text the diff describes. The diff itself does not change, so on Windows the check of a working diff against patch is not possible.
	if runtime.GOOS == "windows" {
		t.Skip("patch on Windows normalizes line endings")
	}

	patch, err := exec.LookPath("patch")

	if err != nil {
		t.Skip("patch is not installed")
	}

	random := rand.New(rand.NewPCG(3, 4))
	lines := []string{"a\n", "b\n", "c\n", "a", "b", "\n", "x\n", "y\n"}
	file := filepath.Join(t.TempDir(), "file")

	text := func() string {
		var builder strings.Builder

		for range random.IntN(30) {
			builder.WriteString(lines[random.IntN(len(lines))])
		}

		return builder.String()
	}

	for range 300 {
		before, after := text(), text()

		// A small change, as formatting makes, half of the time.
		if random.IntN(2) == 0 {
			parts := strings.SplitAfter(before, "\n")
			parts[random.IntN(len(parts))] = lines[random.IntN(len(lines))]
			after = strings.Join(parts, "")
		}

		diff := unifiedDiff("file.orig", before, "file", after)

		if diff == "" {
			continue
		}

		if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}

		command := exec.Command(patch, "-s", "-f", file)
		command.Stdin = strings.NewReader(diff)

		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("patch: %v %s\n%s", err, output, diff)
		}

		if patched, _ := os.ReadFile(file); string(patched) != after {
			t.Fatalf("patching %q gave %q, want %q\n%s", before, patched, after, diff)
		}
	}
}
