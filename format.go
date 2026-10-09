package soml

// The formatter in the spec's "Formatting" section, written as the JS reference's `format()` is, so that the two give the same text.

import (
	"strings"
)

/*
Format formats a document: it changes the layout and nothing else, so the value, the comments, the member order, and the spelling of every key and value stay. It returns an error when the data is not a valid document, as Unmarshal would. To format a Document after changes, format its Bytes.

The layout follows the formatter in the spec: one tab per level, every member and item of a container that spans lines on its own line, no commas, one space after `:`, no trailing whitespace, no runs of blank lines, and one line feed at the end. An object or an array whose brackets are on one line stays on one line, as in `ports: [80, 443]`, with a comma and a space between its members or items, and no space directly inside its brackets. To give a one-line container one member or item per line, put a line break anywhere inside it. Comments, member order, block strings, a top level with or without braces, and the spelling of every key and value stay as they are. The one change inside a block comment is the same layout rule: trailing whitespace is removed, and runs of blank lines collapse to one. Formatting a formatted document gives the same text.

In detail, as the spec states:

  - A comment at the end of a line stays at the end of that line, also when the comma before it is removed. A block comment that a value follows on the same line stays in front of that value when each item goes on its own line.
  - A value on the line after its `key:` moves up to that line, unless a comment comes between them.
  - A block string begins on the line after its key, and its delimiters and content have the indentation of that line. Its lines are not otherwise changed.
  - There are no blank lines at the start of the document or directly inside brackets.
*/
func Format(data []byte) ([]byte, error) {
	document, err := Parse(data)

	if err != nil {
		return nil, err
	}

	// The formatted text is about as long as the source, so the output starts at that size.
	writer := formatWriter{text: document.source, comments: document.Comments(), output: make([]byte, 0, len(document.source)+1)}
	items := []formatItem{itemOf(document.root)}

	if object, ok := document.root.(*Object); ok && object.isBare {
		items = appendMembers(nil, object)
	}

	// The document is a list: the members of a top-level object without braces, or one braced collection.
	writer.list(items, 0, len(writer.text), 0, false)
	return append(writer.output, '\n'), nil
}

/*
A member or an array item, with its range in the source.
*/
type formatItem struct {
	start, end int
	member     *Member
	value      Node
}

func itemOf(value Node) formatItem {
	span, _ := value.Span()
	return formatItem{start: span.Start, end: span.End, value: value}
}

// appendMembers appends the members of an object to items.
func appendMembers(items []formatItem, object *Object) []formatItem {
	for _, member := range object.Members {
		items = append(items, formatItem{start: member.span.value.Start, end: member.span.value.End, member: member})
	}

	return items
}

/*
formatWriter writes the formatted text of a document that was just parsed, so every node and comment has its range in the source.
*/
type formatWriter struct {
	text         string
	comments     []Comment
	commentIndex int
	output       []byte

	// The members and items of the containers being written. A nested container is written while the one around it is, so each container puts its items at the top of one stack and takes them off when it is written, rather than allocating a list of its own.
	items []formatItem
}

// newLine starts a new line at level, with one blank line before it when the source had one.
func (writer *formatWriter) newLine(level int, isBlank bool) {
	// The first line of the document has no line feed or blank line before it.
	if len(writer.output) == 0 {
		return
	}

	if isBlank {
		writer.output = append(writer.output, '\n')
	}

	writer.output = append(writer.output, '\n')
	writer.output = appendTabs(writer.output, level)
}

func (writer *formatWriter) isSameLine(start, end int) bool {
	return !strings.Contains(writer.text[start:end], "\n")
}

// hasBlankLine reports whether the source has a line of only spaces and tabs between start and end.
// Note: Not using strings.Cut because IndexByte is faster for these short line gaps.
func (writer *formatWriter) hasBlankLine(start, end int) bool {
	text := writer.text[start:end]

	for {
		index := strings.IndexByte(text, '\n')

		if index < 0 {
			return false
		}

		text = text[index+1:]

		if strings.HasPrefix(strings.TrimLeft(text, " \t"), "\n") {
			return true
		}
	}
}

// peekComment returns the next comment that ends at or before end, without consuming it.
func (writer *formatWriter) peekComment(end int) (Comment, bool) {
	if writer.commentIndex < len(writer.comments) && writer.comments[writer.commentIndex].Span.End <= end {
		return writer.comments[writer.commentIndex], true
	}

	return Comment{}, false
}

func (writer *formatWriter) writeComment(comment Comment) {
	writer.commentIndex++
	text := writer.text[comment.Span.Start:comment.Span.End]

	if !comment.IsBlock {
		writer.output = append(writer.output, strings.TrimRight(text, " \t")...)
		return
	}

	// The lines of a block comment keep their own indentation. Trailing whitespace is removed, and runs of blank lines collapse to one, as everywhere outside a block string.
	isPreviousEmpty := false
	isFirst := true

	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, " \t")

		if line == "" && isPreviousEmpty {
			continue
		}

		if !isFirst {
			writer.output = append(writer.output, '\n')
		}

		writer.output = append(writer.output, line...)
		isPreviousEmpty = line == ""
		isFirst = false
	}
}

// findComma returns the offset of the `,` after an item in the source, between start and end, where there is only whitespace, comments, and the comma.
func (writer *formatWriter) findComma(start, end int) (int, bool) {
	index, _, _ := skipTrivia(writer.text, start)
	return index, index < end && writer.text[index] == ','
}

/*
list writes items one per line at level, with the comments between them, from start to end in the source. The line breaks separate the items, so no comma is written. isBraced reports whether the items are inside brackets, where the source may have commas.

A comment on the line of the item before it, or of the opening bracket, stays at the end of that line. The exception is a block comment after the comma that the next item follows on the same line: it stays in front of that item. Every other comment gets its own line.
*/
func (writer *formatWriter) list(items []formatItem, start, end, level int, isBraced bool) {
	previousEnd := start
	itemEnd := -1

	// Whether a comment may stay at the end of the line before it, which needs an item or an opening bracket there.
	canTrail := isBraced

	// Whether a comment or an item got a line of its own, because no blank line comes directly inside a bracket, also after a comment at the end of the bracket's line.
	hasWritten := false
	isOnSameLine := false

	for position := 0; position <= len(items); position++ {
		itemStart := end
		hasItem := position < len(items)

		if hasItem {
			itemStart = items[position].start
		}

		comma, hasComma := 0, false

		if isBraced {
			comma, hasComma = writer.findComma(previousEnd, itemStart)
		}

		// The last line break before the item, so that each comment can tell whether the item is on its line without a scan to the item, which would take quadratic time for many comments on one line.
		// Without a line break, LastIndexByte gives -1, so it is then less than previousEnd, as isTrailing expects.
		itemLineBreak := previousEnd + strings.LastIndexByte(writer.text[previousEnd:itemStart], '\n')

		for comment, ok := writer.peekComment(itemStart); ok; comment, ok = writer.peekComment(itemStart) {
			isTrailing := canTrail && !isOnSameLine && writer.isTrailing(comment, previousEnd, itemEnd, itemLineBreak, hasItem, comma, hasComma)

			if isTrailing || isOnSameLine {
				writer.output = append(writer.output, ' ')
			} else {
				writer.newLine(level, hasWritten && writer.hasBlankLine(previousEnd, comment.Span.Start))
			}

			writer.writeComment(comment)
			hasWritten = hasWritten || !isTrailing

			// A block comment that the next comment or item follows on the same line keeps it on that line.
			nextStart := itemStart

			if next, ok := writer.peekComment(itemStart); ok {
				nextStart = next.Span.Start
			}

			isOnSameLine = comment.IsBlock && !isTrailing && (hasItem || nextStart < itemStart) && writer.isSameLine(comment.Span.End, nextStart)
			previousEnd = comment.Span.End
		}

		if !hasItem {
			return
		}

		if isOnSameLine {
			writer.output = append(writer.output, ' ')
		} else {
			writer.newLine(level, hasWritten && writer.hasBlankLine(previousEnd, itemStart))
		}

		item := items[position]

		if item.member != nil {
			writer.member(item.member, level)
		} else {
			writer.value(item.value, level)
		}

		previousEnd = item.end
		itemEnd = previousEnd
		canTrail = true
		hasWritten = true
		isOnSameLine = false
	}
}

// isTrailing reports whether a comment stays at the end of the line before it. That line ends at the item before it, or at the comma after that item. itemLineBreak is the offset of the last line break before the next item, or less than previousEnd when there is none.
func (writer *formatWriter) isTrailing(comment Comment, previousEnd, itemEnd, itemLineBreak int, hasItem bool, comma int, hasComma bool) bool {
	isAfterComma := hasComma && comment.Span.Start > comma

	// A block comment after the comma that the next item follows on the same line stays in front of that item.
	if hasItem && comment.IsBlock && (!hasComma || isAfterComma) && comment.Span.End > itemLineBreak {
		return false
	}

	// A comment after the comma ends the comma's line, which is later than the item's when a block comment that spans lines comes between them. The comma goes after the item, so that only holds when nothing came between them.
	lineEnd := previousEnd

	if isAfterComma && previousEnd == itemEnd {
		lineEnd = comma + 1
	}

	return writer.isSameLine(lineEnd, comment.Span.Start)
}

func (writer *formatWriter) member(member *Member, level int) {
	writer.output = append(writer.output, member.Key.raw...)
	writer.output = append(writer.output, ':')
	valueSpan, _ := member.Value.Span()
	isBlockString := false

	if scalar, ok := member.Value.(*Scalar); ok {
		isBlockString = scalar.Style() == StyleLiteralBlock || scalar.Style() == StyleEscapedBlock
	}

	if _, ok := writer.peekComment(valueSpan.Start); !ok {
		// A block string begins on the next line, one level deeper, so its delimiters and content line up.
		if isBlockString {
			writer.newLine(level+1, false)
			writer.value(member.Value, level+1)
			return
		}

		writer.output = append(writer.output, ' ')
		writer.value(member.Value, level)
		return
	}

	// With comments between the `:` and the value, the line breaks between them are kept, and a value on a new line is one level deeper.
	previousEnd := member.Key.span.value.End + 1
	valueLevel := level

	for comment, ok := writer.peekComment(valueSpan.Start); ok; comment, ok = writer.peekComment(valueSpan.Start) {
		if !writer.isSameLine(previousEnd, comment.Span.Start) {
			valueLevel = level + 1
		}

		writer.writeSeparator(previousEnd, comment.Span.Start, level+1)
		writer.writeComment(comment)
		previousEnd = comment.Span.End
	}

	if isBlockString {
		writer.newLine(level+1, false)
		valueLevel = level + 1
	} else {
		if !writer.isSameLine(previousEnd, valueSpan.Start) {
			valueLevel = level + 1
		}

		writer.writeSeparator(previousEnd, valueSpan.Start, level+1)
	}

	writer.value(member.Value, valueLevel)
}

// writeSeparator writes a space when end is on the line of start in the source, and otherwise starts a new line at level.
func (writer *formatWriter) writeSeparator(start, end, level int) {
	if writer.isSameLine(start, end) {
		writer.output = append(writer.output, ' ')
	} else {
		writer.newLine(level, false)
	}
}

func (writer *formatWriter) value(node Node, level int) {
	span, _ := node.Span()
	base := len(writer.items)
	opening, closing := byte('{'), byte('}')

	switch node := node.(type) {
	case *Object:
		writer.items = appendMembers(writer.items, node)
	case *Array:
		opening, closing = '[', ']'

		for _, item := range node.Items {
			writer.items = append(writer.items, itemOf(item.Value))
		}
	case *Scalar:
		text := writer.text[span.Start:span.End]

		if style := node.Style(); style == StyleLiteralBlock || style == StyleEscapedBlock {
			text = reindentBlockString(text, strings.Repeat("\t", level))
		}

		writer.output = append(writer.output, text...)
		return
	}

	// The nested containers only use the stack above these items, so they stay as they are, also when the stack grows.
	items := writer.items[base:]
	defer func() {
		writer.items = writer.items[:base]
	}()

	if _, hasComment := writer.peekComment(span.End); len(items) == 0 && !hasComment {
		writer.output = append(writer.output, opening, closing)
		return
	}

	if writer.isSameLine(span.Start, span.End) {
		writer.oneLine(items, span.Start, span.End, level, opening, closing)
		return
	}

	writer.output = append(writer.output, opening)
	writer.list(items, span.Start+1, span.End-1, level+1, true)
	writer.newLine(level, false)
	writer.output = append(writer.output, closing)
}

// oneLine writes a container that is on one line in the source on one line, with a comma and a space between its members or items and no trailing comma. Only a block comment can be inside it, and each one stays where it is among the items and commas. start and end are the range of the container, with its brackets.
func (writer *formatWriter) oneLine(items []formatItem, start, end, level int, opening, closing byte) {
	writer.output = append(writer.output, opening)
	previousEnd := start + 1
	hasWrittenToken := false

	for position := 0; position <= len(items); position++ {
		itemStart := end - 1
		hasItem := position < len(items)
		comma, hasComma := 0, false

		// The comma after the last item is left out.
		if hasItem {
			itemStart = items[position].start
			comma, hasComma = writer.findComma(previousEnd, itemStart)
		}

		hasWrittenComma := false

		for comment, ok := writer.peekComment(itemStart); ok; comment, ok = writer.peekComment(itemStart) {
			if !hasWrittenComma && hasComma && comma < comment.Span.Start {
				writer.output = append(writer.output, ',')
				hasWrittenComma = true
			}

			if hasWrittenToken {
				writer.output = append(writer.output, ' ')
			}

			writer.writeComment(comment)
			hasWrittenToken = true
		}

		if !hasItem {
			break
		}

		if hasComma && !hasWrittenComma {
			writer.output = append(writer.output, ", "...)
		} else if hasWrittenToken {
			writer.output = append(writer.output, ' ')
		}

		item := items[position]

		if item.member != nil {
			writer.member(item.member, level)
		} else {
			writer.value(item.value, level)
		}

		previousEnd = item.end
		hasWrittenToken = true
	}

	writer.output = append(writer.output, closing)
}

// reindentBlockString moves a block string to a new indentation. Its content is relative to the closing delimiter's indentation, so replacing that indentation on every line keeps the value. A blank line is left as it is, because it becomes an empty line either way.
// Note: Not using strings.ReplaceAll because it would also change whitespace-only lines, whose spelling must stay intact.
func reindentBlockString(text, indentation string) string {
	oldIndentation := leadingIndentation(text[strings.LastIndexByte(text, '\n')+1:])
	first, rest, _ := strings.Cut(text, "\n")
	var output strings.Builder
	output.Grow(len(text))
	output.WriteString(first)

	for line := range strings.SplitSeq(rest, "\n") {
		output.WriteByte('\n')

		// The parser checked that every line that is not blank starts with the closing delimiter's indentation, so the cut is safe.
		if strings.Trim(line, " \t") != "" {
			output.WriteString(indentation)
			line = line[len(oldIndentation):]
		}

		output.WriteString(line)
	}

	return output.String()
}
