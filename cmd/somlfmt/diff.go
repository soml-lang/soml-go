package main

// A unified diff of two texts, as `gofmt -d` writes it.

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// contextLines is the number of unchanged lines around a change in a hunk.
const contextLines = 3

/*
A line of a diff: unchanged (' '), removed ('-'), or added ('+'). The text ends with its line feed, except the last line of a text without one.
*/
type diffLine struct {
	kind byte
	text string
}

/*
unifiedDiff returns the difference between two texts in the unified format, with a header that names them, or nothing when they are equal.

The lines that appear exactly once in each text are anchors, and the longest run of anchors in the same order in both is kept, as in patience diff. Between two anchors, the lines that match at the start and at the end are kept, and the rest is removed and added. This takes O(n log n) time, so a large file that is formatted for the first time cannot make it slow, and the result is readable for the small changes that formatting makes.
*/
func unifiedDiff(beforeName, before, afterName, after string) string {
	if before == after {
		return ""
	}

	lines := diffLines(splitLines(before), splitLines(after))
	var output strings.Builder
	// Note: Not using string concatenation because it allocates a complete temporary header before copying it into the builder.
	fmt.Fprintf(&output, "diff %s %s\n--- %s\n+++ %s\n", beforeName, afterName, beforeName, afterName)

	// The line numbers, in both texts, of the first line that is not written yet.
	position := lineNumbers{1, 1}

	for start := 0; start < len(lines); {
		// A hunk runs from the context before a change to the context after the last change that is close enough to share it: one with fewer equal lines before it than two contexts, as in gofmt -d.
		change := slices.IndexFunc(lines[start:], isChange)

		if change < 0 {
			break
		}

		change += start
		hunkStart := max(start, change-contextLines)
		hunkEnd := change

		for hunkEnd < len(lines) {
			next := slices.IndexFunc(lines[hunkEnd:], isChange)

			if next < 0 || next >= 2*contextLines {
				break
			}

			hunkEnd += next + 1
		}

		hunkEnd = min(len(lines), hunkEnd+contextLines)
		position = position.advanced(lines[start:hunkStart])
		writeHunk(&output, lines[hunkStart:hunkEnd], position)
		position = position.advanced(lines[hunkStart:hunkEnd])
		start = hunkEnd
	}

	return output.String()
}

// lineNumbers are line numbers, or line counts, in the text before and the text after.
type lineNumbers struct {
	before, after int
}

// advanced returns the line numbers after lines.
func (numbers lineNumbers) advanced(lines []diffLine) lineNumbers {
	for _, line := range lines {
		if line.kind != '+' {
			numbers.before++
		}

		if line.kind != '-' {
			numbers.after++
		}
	}

	return numbers
}

func isChange(line diffLine) bool {
	return line.kind != ' '
}

// writeHunk writes the lines of a hunk, which starts at the line numbers start, with a header that gives its lines in both texts.
func writeHunk(output *strings.Builder, lines []diffLine, start lineNumbers) {
	count := lineNumbers{}.advanced(lines)

	// An empty side is numbered by the line before it, as diff does.
	if count.before == 0 {
		start.before--
	}

	if count.after == 0 {
		start.after--
	}

	fmt.Fprintf(output, "@@ -%d,%d +%d,%d @@\n", start.before, count.before, start.after, count.after)

	for _, line := range lines {
		output.WriteByte(line.kind)
		output.WriteString(line.text)

		if !strings.HasSuffix(line.text, "\n") {
			output.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

// splitLines splits text after each line feed. The last line has no line feed when the text does not end with one.
// Note: Not using slices.Collect(strings.Lines(text)) because it grows the slice repeatedly instead of allocating once.
func splitLines(text string) []string {
	lines := strings.SplitAfter(text, "\n")

	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return lines
}

// diffLines returns the lines of both texts as unchanged, removed, and added lines, in order.
func diffLines(before, after []string) []diffLine {
	var lines []diffLine
	beforeIndex, afterIndex := 0, 0

	// The end of both texts is the last anchor.
	for _, anchor := range append(anchors(before, after), [2]int{len(before), len(after)}) {
		// The lines that match at the start and at the end of the gap before the anchor stay.
		for beforeIndex < anchor[0] && afterIndex < anchor[1] && before[beforeIndex] == after[afterIndex] {
			lines = append(lines, diffLine{' ', before[beforeIndex]})
			beforeIndex++
			afterIndex++
		}

		suffix := 0

		for beforeIndex < anchor[0]-suffix && afterIndex < anchor[1]-suffix && before[anchor[0]-suffix-1] == after[anchor[1]-suffix-1] {
			suffix++
		}

		for _, text := range before[beforeIndex : anchor[0]-suffix] {
			lines = append(lines, diffLine{'-', text})
		}

		for _, text := range after[afterIndex : anchor[1]-suffix] {
			lines = append(lines, diffLine{'+', text})
		}

		for _, text := range before[anchor[0]-suffix : anchor[0]] {
			lines = append(lines, diffLine{' ', text})
		}

		if anchor[0] < len(before) {
			lines = append(lines, diffLine{' ', before[anchor[0]]})
		}

		beforeIndex, afterIndex = anchor[0]+1, anchor[1]+1
	}

	return lines
}

// anchors returns the positions of the lines that appear exactly once in each text, as pairs of a position in before and one in after, keeping the longest run that is in the same order in both.
func anchors(before, after []string) [][2]int {
	beforeCounts := map[string]int{}

	for _, line := range before {
		beforeCounts[line]++
	}

	afterCounts := map[string]int{}
	afterPositions := map[string]int{}

	for position, line := range after {
		afterCounts[line]++
		afterPositions[line] = position
	}

	var pairs [][2]int

	for position, line := range before {
		if beforeCounts[line] == 1 && afterCounts[line] == 1 {
			pairs = append(pairs, [2]int{position, afterPositions[line]})
		}
	}

	// The longest increasing run of after positions, by patience sorting: tails[length-1] is the pair that ends the best run of that length, and previous links each pair to the one before it in its run.
	var tails []int
	previous := make([]int, len(pairs))

	for index, pair := range pairs {
		// Note: Not using slices.BinarySearchFunc because its extra equality check is unnecessary and slower here.
		length := sort.Search(len(tails), func(tail int) bool {
			return pairs[tails[tail]][1] >= pair[1]
		})

		previous[index] = -1

		if length > 0 {
			previous[index] = tails[length-1]
		}

		if length == len(tails) {
			tails = append(tails, index)
		} else {
			tails[length] = index
		}
	}

	if len(tails) == 0 {
		return nil
	}

	run := make([][2]int, len(tails))

	for index, position := len(tails)-1, tails[len(tails)-1]; index >= 0; index, position = index-1, previous[position] {
		run[index] = pairs[position]
	}

	return run
}
