package soml

import (
	"bytes"
	"fmt"
	"iter"
	"slices"
	"strings"
	"unicode"
)

/*
Document is a SOML document as a lossless syntax tree, to read with positions, change, and print.

An unchanged document prints the exact text it was parsed from. A changed one keeps the comments, blank lines, indentation, member order, and the author's spelling of every key and value outside the change. New values are written as Marshal writes them: in canonical style, but with the fields of a struct in their declared order. In a container on one line, they are written on one line, as in `ports: [80, 443]`.

A path into a document is a list of keys (strings) and array positions (ints), so the path "server", "port" finds `server: {port: 1}`.

The nodes can also be changed directly, through Root. A node must then appear in the tree at most once, and Members and Items must not hold nil, nor a node that is a nil pointer. A tree that breaks these rules is not supported: a node used twice changes in both places, and a cycle never ends.
*/
type Document struct {
	leading  string
	root     Node
	trailing string
	source   string
}

/*
Parse parses a document into a syntax tree, with the same checks and errors as Unmarshal.
*/
func Parse(data []byte) (*Document, error) {
	source := string(data)

	// Checked first, because buildTree trusts its input.
	if _, err := parse(source); err != nil {
		return nil, err
	}

	leading, root, trailing := buildTree(source)
	return &Document{leading: leading, root: root, trailing: trailing, source: source}, nil
}

// Root returns the top-level *Object or *Array. Changes to it can make a document that is not valid, such as one with two members with the same key; Decode finds that. Set and Remove keep the document valid.
func (document *Document) Root() Node {
	return document.root
}

// OuterDecor returns the text before a top-level collection written with braces, and after it. Both are empty for a top-level object without braces, where that text belongs to its first member and to the object itself.
func (document *Document) OuterDecor() (leading, trailing string) {
	return document.leading, document.trailing
}

// Bytes returns the text of the document: the exact text it was parsed from, with any changes.
func (document *Document) Bytes() []byte {
	printer := document.newPrinter()
	printer.document(document.leading, document.root, document.trailing)
	return printer.output
}

// newPrinter returns a printer for the whole document. An unchanged document prints the text it was parsed from, so the output starts at that size and does not grow.
func (document *Document) newPrinter() treePrinter {
	return treePrinter{output: make([]byte, 0, len(document.source))}
}

// String returns the text of the document, as Bytes does.
func (document *Document) String() string {
	return string(document.Bytes())
}

// Comments returns the comments, in order, with their positions in the document's current text, which is the parsed text when nothing changed.
func (document *Document) Comments() []Comment {
	printer := document.newPrinter()
	printer.wantComments = true
	printer.document(document.leading, document.root, document.trailing)
	return printer.comments
}

// Position returns the 1-based line and column of a byte offset in the text the document was parsed from, as for a node's span. The column counts Unicode scalar values.
func (document *Document) Position(offset int) (line, column int) {
	return position(document.source, offset)
}

/*
Decode decodes the document into the value that out points to, as Unmarshal does, with every check of the spec, such as for duplicate keys, which a change through Root can make.

It prints the document and reads it back, so an error's position refers to the printed text, which is the parsed text when nothing changed.
*/
func (document *Document) Decode(out any, options ...Option) error {
	return Unmarshal(document.Bytes(), out, options...)
}

/*
Get returns the node at a path, and the root for an empty path. It reports false when nothing is there, also for a path with an element that is not a string or an int that is not negative.
*/
func (document *Document) Get(path ...any) (Node, bool) {
	if !isPath(path) {
		return nil, false
	}

	node := document.root

	for _, element := range path {
		index, ok := step(node, element)

		if !ok {
			return nil, false
		}

		switch container := node.(type) {
		case *Object:
			node = container.Members[index].Value
		case *Array:
			node = container.Items[index].Value
		}
	}

	return node, true
}

/*
All returns an iterator over every node with its path, in the order the document writes them, with each node before the nodes inside it. The root comes first, with an empty path. A path is in the form that Get takes, so `server: {port: 1}` gives the path "server", "port" for the 1. Each path is a new slice, which the caller may keep.

	for path, node := range document.All() {
		if scalar, ok := node.(*soml.Scalar); ok {
			fmt.Println(path, scalar.Raw())
		}
	}
*/
func (document *Document) All() iter.Seq2[[]any, Node] {
	return func(yield func([]any, Node) bool) {
		walk(nil, document.root, yield)
	}
}

// walk yields a node and every node inside it, and reports whether the iteration goes on. The path is copied for each node, because the next nodes reuse its backing array.
func walk(path []any, node Node, yield func([]any, Node) bool) bool {
	if !yield(slices.Clone(path), node) {
		return false
	}

	switch node := node.(type) {
	case *Object:
		for _, member := range node.Members {
			if !walk(append(path, member.Key.value), member.Value, yield) {
				return false
			}
		}
	case *Array:
		for index, item := range node.Items {
			if !walk(append(path, index), item.Value, yield) {
				return false
			}
		}
	}

	return true
}

/*
Set sets the value at a path, replacing what is there or adding it. The value can be any Go value that Marshal accepts. It follows the spec's rules for editing, so a change to a formatted document leaves it formatted, with the same bytes as the JS reference gives.

  - An existing value is replaced, and the comments around it stay.
  - A new value is written as Marshal writes it, at the indentation of its line: with braces, and without block strings.
  - A new member goes after the last member of its object, with the indentation of that member.
  - A new member or item on its own line gets no comma, because the line break separates it, and the commas of the other entries stay as they are. It goes after the comments that the entry before it owns (see Remove), and before a comment on a line of its own that follows them. When something follows the entry before it on its line, such as the closing bracket in `{a: 1}` or the next member, the new one goes on that line, after a comma, and with a comma after it when the entry before has one. So in an array on one line whose last item has a block comment after it, a new item goes after that comment and a comma.
  - A value that is added or replaced in a container that stays on one line is written on one line, so setting item 2 of `[1, 2]` to `map[string]int{"a": 3}` gives `[1, 2, {a: 3}]`. A container stays on one line when its brackets are on one line and something other than spaces and tabs is between them, such as an item or a block comment, or when it is inside a container that stays on one line. A top-level object without braces never does.
  - A new member or item in an empty container goes on a line of its own, so `deps: {}` gets its first member on a new line, unless the container stays on one line: then it goes before the closing bracket, after a block comment there, and `[1, []]` becomes `[1, [2]]`.
  - A missing parent object is created with braces.
  - An index equal to an array's length appends. A path that goes on below it makes the new item of new objects, so setting "b", 0, "c" to 1 in `b: []` adds the item `{c: 1}`.

It returns an error when the path is empty, when an element of it is not a string or an int that is not negative, when it goes through a scalar, when it uses a key on an array or an index on an object, when an index is past the end of an array, or past the end of a new one, when Marshal would return an error for the value, and when the result would not be a valid document, such as one nested more than 100 levels deep. The document is unchanged after an error.

Each change reads the result back to check it, so it takes time in proportion to the size of the document.
*/
func (document *Document) Set(value any, path ...any) error {
	if err := checkPath(path); err != nil {
		return err
	}

	node, err := valueNode(value)

	if err != nil {
		// The path of an error in the value starts at the path it is set at.
		if somlError, ok := err.(*Error); ok {
			somlError.Path = append(slices.Clone(path), somlError.Path...)
		}

		return err
	}

	// The change is made on a copy first and read back, which checks every rule of the spec, such as the nesting limit at the place the value goes. Only a valid change is then made on the tree itself, so a failed change leaves the document as it was, and the nodes outside the change stay the same nodes.
	changed := Document{leading: document.leading, root: cloneNode(document.root), trailing: document.trailing}

	// The indentation of the line a braced top-level collection starts on, so that what is added inside it lines up with it in a document that is indented as a whole.
	indentation := leadingIndentation(ownLine(document.leading))

	if err := setIn(changed.root, path, path, node, indentation, false); err != nil {
		return err
	}

	if _, err := parse(changed.String()); err != nil {
		return &Error{Message: "cannot edit " + describePath(path) + ", because the change would make a document that is not valid: " + err.Message, Err: err}
	}

	// The same node can go to both trees, because layOut puts a copy of it in the tree.
	return setIn(document.root, path, path, node, indentation, false)
}

/*
Remove removes the member or item at a path. It follows the spec's rules for editing, so a change to a formatted document leaves it formatted, with the same bytes as the JS reference gives.

  - It takes the comments it owns, which are the ones that Format keeps with it: the comments after it on its line, also after its comma when nothing else follows there, and the block comments before it on its line, after the comma or bracket before it. So removing `2` from `[1, 2]` with a block comment before `2` gives `[1]`. A comment on a line of its own belongs to no member or item, so it stays.
  - It removes its lines when nothing else is on them, and it leaves one run of blank lines where it was, and none at the start or end of its container. Removing every item of a container closes it up to `[]` or `{}`, unless a comment is left inside.
  - It takes its comma with it, the one after it. When the removed entries are the last ones and the last of them has no comma, they take the comma directly before them instead, when only spaces, tabs, and the comments they own are between, so `[1, 2]` becomes `[1]`. Every other comma stays, so when an item that ends its line with a comma is the last one left, it keeps that comma, which is valid.
  - Removing the only member of a top-level object without braces leaves a braced `{}` in its place, because a document is never empty, and the comments that the member owned stay. Root then returns a new node.

It reports whether something was removed. Removing a value that does not exist changes nothing and is not an error: a missing member, a missing object or array on the way, or an index at or past the end of its array. So removing the same path twice is safe. It returns an error when the path is empty, when an element of it is not a string or an int that is not negative, or when, before it reaches a value that does not exist, it goes through a scalar, or uses a key on an array or an index on an object. The document is unchanged after an error.
*/
func (document *Document) Remove(path ...any) (bool, error) {
	if err := checkPath(path); err != nil {
		return false, err
	}

	return removeIn(document, document.root, path, path)
}

// checkPath checks that a path for a change is not empty, and that it is a path, as isPath says. One message covers both, as in the JS reference.
func checkPath(path []any) error {
	if len(path) == 0 || !isPath(path) {
		return newError("the path must be a non-empty array of keys and array indexes")
	}

	return nil
}

// isPath reports whether every element of a path is a string (a key) or an int (an array position) that is not negative.
func isPath(path []any) bool {
	for _, element := range path {
		switch element := element.(type) {
		case string:
		case int:
			if element < 0 {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// step returns the position of the member or item that an element of a path names in a node.
func step(node Node, element any) (int, bool) {
	switch node := node.(type) {
	case *Object:
		if key, ok := element.(string); ok {
			return node.memberIndex(key)
		}
	case *Array:
		if index, ok := element.(int); ok && index < len(node.Items) {
			return index, true
		}
	}

	return 0, false
}

// setIn sets value at path inside node, whose line has indentation. fullPath is for error messages. isInsideOneLine is whether node is inside a container that stays on one line, so that node stays on one line too.
func setIn(node Node, path, fullPath []any, value Node, indentation string, isInsideOneLine bool) error {
	switch node := node.(type) {
	case *Object:
		return setInObject(node, path, fullPath, value, indentation, isInsideOneLine)
	case *Array:
		index, ok := path[0].(int)

		if !ok {
			return newError("cannot edit %s, because %s is an array, so it needs an index, not a key", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
		}

		count := len(node.Items)

		if index > count {
			owner := "the document"

			if parentLength := len(fullPath) - len(path); parentLength > 0 {
				owner = "the array at " + describePath(fullPath[:parentLength])
			}

			items := fmt.Sprintf("%d items", count)

			if count == 1 {
				items = "1 item"
			}

			return newError("cannot edit %s, because %s has %s. Add an item at index %d", describePath(fullPath), owner, items, count)
		}

		isOneLine := isInsideOneLine || staysOnOneLine(node)

		if index == count {
			// The rest of the path is in the new item, so it is made of new objects, as for a missing member.
			nested, err := nestedValue(value, path[1:], fullPath)

			if err != nil {
				return err
			}

			node.Items, node.ClosingDecor = appendEntry(node.Items, node.ClosingDecor, true, &Item{}, nested, indentation, isOneLine)
			return nil
		}

		itemIndentation := entryIndentation(node.Items, index, false, indentation)
		item := node.Items[index]

		if len(path) == 1 {
			item.Value = layOut(value, itemIndentation, isOneLine)
			return nil
		}

		return setIn(item.Value, path[1:], fullPath, value, itemIndentation, isOneLine)
	}

	return newError("cannot edit %s, because %s is not an object or an array", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
}

func setInObject(object *Object, path, fullPath []any, value Node, indentation string, isInsideOneLine bool) error {
	key, ok := path[0].(string)

	if !ok {
		return newError("cannot edit %s, because %s is an object, so it needs a key, not an index", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
	}

	isOneLine := isInsideOneLine || staysOnOneLine(object)

	if index, ok := object.memberIndex(key); ok {
		member := object.Members[index]
		memberIndentation := entryIndentation(object.Members, index, object.isBare, indentation)

		if len(path) == 1 {
			// A block string begins on the line after its key, and the new value is never a block string, so when only whitespace comes between the `:` and a block string, the new value goes on the key's line, as the formatter writes it.
			if scalar, ok := member.Value.(*Scalar); ok && (scalar.Style() == StyleLiteralBlock || scalar.Style() == StyleEscapedBlock) && strings.Trim(member.afterColon, " \t\n") == "" {
				member.afterColon = " "
			}
		}

		// A value on the line after its key, after a comment, is indented by its own line.
		valueIndentation := memberIndentation

		if start, ok := lastLineStart(member.afterColon); ok {
			valueIndentation = leadingIndentation(member.afterColon[start:])
		}

		if len(path) == 1 {
			member.Value = layOut(value, valueIndentation, isOneLine)
			return nil
		}

		return setIn(member.Value, path[1:], fullPath, value, valueIndentation, isOneLine)
	}

	// Missing parent objects are created with braces, as canonical form writes them.
	nested, err := nestedValue(value, path[1:], fullPath)

	if err != nil {
		return err
	}

	// The new member goes last.
	member := &Member{Key: NewKey(key), afterColon: " "}
	object.Members, object.ClosingDecor = appendEntry(object.Members, object.ClosingDecor, !object.isBare, member, nested, indentation, isOneLine)
	return nil
}

// nestedValue returns value inside new objects for the keys of path, which a change makes because they are missing. An index in path is an error, because it names an item of an array that is not there. The path is the end of fullPath.
func nestedValue(value Node, path, fullPath []any) (Node, error) {
	for index := len(path) - 1; index >= 0; index-- {
		key, ok := path[index].(string)

		if !ok {
			return nil, newError("cannot edit %s, because %s does not exist, so it has no index %d", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)+index), path[index])
		}

		value = &Object{Members: []*Member{{Key: NewKey(key), Value: value}}}
	}

	return value, nil
}

// describePath writes a path for a message of the editor, as the JS reference does: as formatPath writes it, cut short, and with the invisible characters and those that a terminal acts on written as escapes, such as \u{202e}, which a key in quotes can have.
func describePath(path []any) string {
	var text strings.Builder

	for _, character := range abbreviate(formatPath(path), 200) {
		if unicode.In(character, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			fmt.Fprintf(&text, `\u{%x}`, character)
		} else {
			text.WriteRune(character)
		}
	}

	return text.String()
}

// describeParent writes the value that holds the element of path at index: its path, or "the document".
func describeParent(path []any, index int) string {
	if index == 0 {
		return "the document"
	}

	return describePath(path[:index])
}

// staysOnOneLine reports whether a container stays on one line when a value in it changes, as the spec's rules for editing say: its brackets are on one line, and something other than spaces and tabs is between them, as in `[1, 2]` and `[/* note */]`. An empty `[]` or `{}` says nothing about layout, because the formatter writes every empty container that way, and a top-level object without braces never stays on one line.
func staysOnOneLine(node Node) bool {
	if object, ok := node.(*Object); ok && object.isBare {
		return false
	}

	printer := treePrinter{}
	printer.node(node)
	text := printer.output
	return bytes.IndexByte(text, '\n') < 0 && len(bytes.Trim(text[1:len(text)-1], " \t")) != 0
}

// entryIndentation is the indentation of the line an entry starts on. When the entry shares its line with entries before it, it is the indentation of the first of them, or of the last line of one of them that spans lines, and when that is the first entry of a container written on one line, the indentation of the container's line. startsLine is whether the first entry starts a line, which is true at the top of a document.
func entryIndentation[T entry](entries []T, index int, startsLine bool, indentation string) string {
	for current := index; current >= 0; current-- {
		previousTrailing := ""

		if current > 0 {
			previousTrailing = entries[current-1].decor().Trailing
		} else if startsLine {
			previousTrailing = "\n"
		}

		decor := previousTrailing + entries[current].decor().Leading

		if start, ok := lastLineStart(decor); ok {
			return leadingIndentation(decor[start:])
		}

		// Without a line feed in the decor, the entry is on the line where the entry before it ends, which may be a later line than the one it starts on.
		if current > 0 {
			if lastLine, ok := lastLineIndentation(entries[current-1]); ok {
				return lastLine
			}
		}
	}

	return indentation
}

// lastLineIndentation returns the indentation of the last line of an entry that spans lines, as an object, an array, or a block string can. It reports false for an entry on one line.
func lastLineIndentation[T entry](entry T) (string, bool) {
	printer := treePrinter{}
	entry.print(&printer)
	lineFeed := bytes.LastIndexByte(printer.output, '\n')

	if lineFeed < 0 {
		return "", false
	}

	return leadingIndentation(string(printer.output[lineFeed+1:])), true
}

// endsLine reports whether decor ends at the start of a line: after its last line feed outside a block comment, it has only spaces and tabs.
func endsLine(decor string) bool {
	return hasLineFeed(decor) && strings.Trim(ownLine(decor), " \t") == ""
}

// commentLines returns the comment lines and blank lines of leading decor, without the indentation of the item's own line.
func commentLines(leading string) string {
	if start, ok := lastLineStart(leading); ok {
		return leading[:start]
	}

	return ""
}

// ownLine returns the indentation of the item's own line in leading decor.
func ownLine(leading string) string {
	if start, ok := lastLineStart(leading); ok {
		return leading[start:]
	}

	return leading
}

// lastLineStart returns the index after the last line feed in decor that is not inside a block comment, which is where the last line starts. A block comment that spans lines is part of the line it ends on.
func lastLineStart(decor string) (int, bool) {
	start, ok := 0, false

	for part := range decorParts(decor) {
		if part.kind == lineFeedPart {
			start, ok = part.end, true
		}
	}

	return start, ok
}

// appendEntry appends an entry with a value, laid out like the entry before it, or on a new line when the container is empty. isBraced is false only for a top-level object without braces, and indentation is that of the container's line. isOneLine is whether the container stays on one line, so that the value is on one line too, and in an empty container, the entry goes before the closing bracket. It returns the entries and the closing decor.
func appendEntry[T entry](entries []T, closing string, isBraced bool, entry T, value Node, indentation string, isOneLine bool) ([]T, string) {
	var leading, trailing, line string
	hasComma := false

	switch {
	case len(entries) > 0:
		line = entryIndentation(entries, len(entries)-1, !isBraced, indentation)
		previous := entries[len(entries)-1]
		decor := previous.decor()

		// The new entry goes after the comments that the entry before owns, as the spec's rules for editing say. After the last entry, those on its line before the closing bracket are in the closing decor, so they move before its comma, which the printer writes between the two entries, as a reparse gives.
		if isBraced && !previous.comma().hasComma && !hasLineFeed(decor.Trailing) {
			owned := ownedCommentsEnd(closing)
			previous.comma().beforeComma += closing[:owned]
			closing = closing[owned:]
		}

		// Whether the closing bracket follows the previous entry on its line. A comment at the end of the line does not count.
		isFollowed := !hasLineFeed(decor.Trailing) && !hasLineFeed(closing)

		switch {
		case isBraced && isFollowed:
			// The new entry goes on that line, with a comma after it when the entry before has one. The printer writes the comma between them. On its own line, an entry needs no comma. That line is the one the entry before ends on.
			leading = " "
			hasComma = previous.comma().hasComma

			if lastLine, ok := lastLineIndentation(previous); ok {
				line = lastLine
			}
		case strings.HasSuffix(decor.Trailing, "\n"):
			leading, trailing = line, "\n"
		default:
			// The line feed that ends the line of the entry before goes in its trailing decor, where a parsed tree has it, so that a later change sees the same layout as after a reparse.
			decor.Trailing += "\n"
			leading = line
		}
	case !isBraced:
		// The closing decor of an empty top-level object, such as a heading comment, goes above the new member, which starts a line.
		leading, closing = closing, ""
		trailing = "\n"

		if leading != "" && !endsLine(leading) {
			leading += "\n"
		}
	case isOneLine:
		// A comment in the empty container stays, before the new entry, so `[/* note */]` becomes `[/* note */ 1]`.
		leading = strings.TrimRight(closing, " \t")

		if leading != "" {
			leading += " "
		}

		closing = ""
	default:
		line = indentation + "\t"

		// A comment in the empty container stays, before the new entry, which starts a line. That line takes the indentation the entry's value is written for, whatever the closing bracket's line had.
		if endsLine(closing) {
			leading = commentLines(closing) + line
		} else {
			leading = strings.TrimRight(closing, " \t") + "\n" + line
		}

		closing = indentation
		trailing = "\n"
	}

	entry.setValue(layOut(value, line, isOneLine))
	*entry.decor() = Decor{Leading: leading, Trailing: trailing}
	entry.comma().hasComma = hasComma
	// Note: Not using slices.Insert because new entries always go last and its general insertion checks are unnecessary.
	return append(entries, entry), closing
}

// ownedCommentsEnd returns the end of the comments at the start of decor that only spaces and tabs come before, which the entry before the decor owns, as the spec's rules for editing say. It is 0 when there are none.
func ownedCommentsEnd(decor string) int {
	end := 0

	for part := range decorParts(decor) {
		if part.kind == lineFeedPart || strings.Trim(decor[end:part.start], " \t") != "" {
			break
		}

		end = part.end
	}

	return end
}

// removeIn removes what is at path inside node, which is in document.
func removeIn(document *Document, node Node, path, fullPath []any) (bool, error) {
	switch node := node.(type) {
	case *Object:
		key, ok := path[0].(string)

		if !ok {
			return false, newError("cannot edit %s, because %s is an object, so it needs a key, not an index", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
		}

		index, ok := node.memberIndex(key)

		if !ok {
			return false, nil
		}

		if len(path) > 1 {
			return removeIn(document, node.Members[index].Value, path[1:], fullPath)
		}

		// A document is never empty, so a top-level object without braces becomes `{}`, where its only member was, and the comments that the member owned stay. That is a braced object, as a reparse gives.
		if node.isBare && len(node.Members) == 1 {
			member := node.Members[0]
			document.leading = member.Decor.Leading
			document.root = &Object{}
			document.trailing = member.separator.beforeComma + member.Decor.Trailing + node.ClosingDecor
			return true, nil
		}

		node.Members, node.ClosingDecor = removeEntries(node.Members, node.ClosingDecor, !node.isBare, func(position int) bool {
			return position == index
		})

		return true, nil
	case *Array:
		index, ok := path[0].(int)

		if !ok {
			return false, newError("cannot edit %s, because %s is an array, so it needs an index, not a key", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
		}

		if index >= len(node.Items) {
			return false, nil
		}

		if len(path) > 1 {
			return removeIn(document, node.Items[index].Value, path[1:], fullPath)
		}

		node.Items, node.ClosingDecor = removeEntries(node.Items, node.ClosingDecor, true, func(position int) bool {
			return position == index
		})

		return true, nil
	}

	return false, newError("cannot edit %s, because %s is not an object or an array", describePath(fullPath), describeParent(fullPath, len(fullPath)-len(path)))
}

// removeEntries removes the entries for which isRemoved reports true, in one pass. The comment lines above a removed entry stay: they move to the next entry that stays, or to the closing decor when none does. An entry that shared a line with removed entries takes their place on it, and a line that a removed entry ended still ends, so a comment line never joins another line. A removed entry takes its own comma, the one after it. When the last entries are removed and the last of them has no comma, they take the comma directly before them, if only spaces, tabs, and the block comments that the first of them owns are between, and the last of them takes the comments after it on the line of the closing bracket. Every other comma stays, as the spec's rules for editing say. A removal leaves one run of blank lines where it was, and adds none at the start or end of the container. isBraced is false only for a top-level object without braces. It returns the entries and the closing decor.
func removeEntries[T entry](entries []T, closing string, isBraced bool, isRemoved func(int) bool) ([]T, string) {
	kept := make([]T, 0, len(entries))

	// The comment lines of the entries removed since the last one that stays. It is a byte slice, so that the blank lines at its end can be cut off in place, which keeps a removal of many entries linear.
	var moved []byte
	hasRemoved := false
	var lastRemoved T

	// Whether the entry before the current one ended its line, which the first entry of a top-level object without braces always does.
	previousEndsLine := !isBraced

	// Whether the current line starts with a removed entry, so it goes away as a whole.
	isLineRemoved := false

	// Whether a removed entry ended the line of the entry that stays before it, so the next text must start a new line.
	hasEndedLine := false

	// The text before the removed entries on the current line, which the next entry takes when it is on that line, because it takes their place.
	place, hasPlace := "", false

	// The comment lines of the first entry, when it is removed, which start moved until an entry stays.
	firstLines := ""

	// movedWithoutBlankLines returns moved without the blank lines at its end, which go away when blank lines follow, or nothing does, so that a removal leaves one run of blank lines. In a braced container, the first line feed of the first entry ends the line of the opening bracket, so it is not a blank line. keepsFirstLines keeps the lines of the first entry whole.
	movedWithoutBlankLines := func(keepsFirstLines bool) []byte {
		floor := 0

		if keepsFirstLines {
			floor = len(firstLines)
		} else if isBraced {
			floor, _ = firstLineFeedEnd(firstLines)
		}

		return moved[:trailingBlankLinesStart(moved, floor)]
	}

	// isAtStart reports whether no entry stays before the current one and moved holds no comment line, so that what comes next starts the container. The end of the opening bracket's line, with a comment on it, does not count.
	isAtStart := func() bool {
		floor := 0

		if isBraced {
			floor, _ = firstLineFeedEnd(firstLines)
		}

		return len(kept) == 0 && len(bytes.Trim(moved[min(floor, len(moved)):], " \t\n")) == 0
	}

	// The text between the entry that stays before the removed entries and the first of them, which decides whether the comma before them goes too.
	gap := ""

	// endLine ends the line of the last entry that stays, when a removed entry ended it. The line feed goes in that entry's trailing decor, where a parsed tree has it, so that the blank lines after it, and a later change, see the same layout as after a reparse. Before the first entry that stays, it starts the text that follows.
	endLine := func() string {
		if !hasEndedLine {
			return ""
		}

		if len(kept) > 0 {
			kept[len(kept)-1].decor().Trailing += "\n"
			return ""
		}

		return "\n"
	}

	for index, entry := range entries {
		decor := entry.decor()
		startsLine := previousEndsLine || hasLineFeed(decor.Leading)
		previousEndsLine = !isBraced || hasLineFeed(decor.Trailing)

		if !isRemoved(index) {
			if hasRemoved {
				own := ownLine(decor.Leading)

				if hasPlace {
					own = place + strings.TrimLeft(own, " \t")
				}

				above, below := string(moved), commentLines(decor.Leading)

				// An entry that becomes the first keeps the blank lines at the start of the container, and no others.
				if isAtStart() {
					above, below = firstLines, below[leadingBlankLinesEnd(below):]
				} else if leadingBlankLinesEnd(below) > 0 {
					above = string(movedWithoutBlankLines(false))
				}

				decor.Leading = endLine() + above + below + own
				moved = moved[:0]
				firstLines = ""
				hasRemoved, hasEndedLine, hasPlace = false, false, false
			}

			isLineRemoved = false
			kept = append(kept, entry)
			continue
		}

		if startsLine || !hasRemoved {
			place, hasPlace = leadingIndentation(ownLine(decor.Leading)), true
		}

		if startsLine {
			isLineRemoved = true
		}

		if previousEndsLine {
			hasEndedLine = hasEndedLine || !isLineRemoved
			hasPlace = false
		}

		if !hasRemoved && len(kept) > 0 {
			gap = kept[len(kept)-1].decor().Trailing + decor.Leading
		}

		lines := commentLines(decor.Leading)

		if index == 0 {
			firstLines = lines
		}

		// The blank lines between the comment lines of two removed entries are one run, as where an entry stays after them. At the start of the container, they go, as the container keeps only the blank lines it starts with.
		if end := leadingBlankLinesEnd(lines); end > 0 && hasRemoved && isAtStart() {
			lines = lines[end:]
		} else if end > 0 {
			moved = movedWithoutBlankLines(false)
		}

		moved = append(moved, lines...)
		hasRemoved = true
		lastRemoved = entry
	}

	if hasRemoved {
		// The last entries were removed. They take the comma directly before them when the last of them has none and only spaces and tabs are between that comma and the comments that the first of them owns, which are block comments on its line, as the spec's rules for editing say. Every other comma stays.
		if len(kept) > 0 {
			separator := kept[len(kept)-1].comma()

			if separator.hasComma && !lastRemoved.comma().hasComma && !hasLineFeed(gap) {
				separator.hasComma = false

				// The text before a comma that goes away stays at the end of the line.
				if hasEndedLine {
					separator.beforeComma = strings.TrimRight(separator.beforeComma, " \t")
				}
			}
		}

		// The comments after the last entry on the line of the closing bracket are its own, so they go with it, and so do the spaces around them, as the spec's rules for editing say.
		if isBraced && !lastRemoved.comma().hasComma && !hasLineFeed(lastRemoved.decor().Trailing) {
			closing = ""
		}

		above := string(moved)

		// When a comment follows, the blank lines stay where the removed entries were.
		switch {
		// When nothing but whitespace follows, the blank lines go, so a removal adds none at the end of the container. The closing decor of an emptied top-level object without braces is printed before `{}`, at the start of the document, so the lines of its first member stay whole there.
		case strings.Trim(closing, " \t\n") == "":
			above = string(movedWithoutBlankLines(!isBraced && len(kept) == 0))

		// As for an entry that stays: when none does, the container keeps the blank lines at its start, and no others.
		case isAtStart():
			above, closing = firstLines, closing[leadingBlankLinesEnd(closing):]

		// Blank lines that follow the removed entries join theirs in one run.
		case leadingBlankLinesEnd(closing) > 0:
			above = string(movedWithoutBlankLines(false))
		}

		closing = endLine() + above + closing
	}

	if len(kept) == 0 && strings.Trim(closing, " \t\n") == "" {
		closing = ""
	}

	// A comment that is left alone in a container on one line comes directly after the opening bracket, as the formatter writes it, so `[1 /* note */]` becomes `[/* note */]`.
	if isBraced && len(kept) == 0 && !strings.Contains(closing, "\n") {
		closing = strings.TrimLeft(closing, " \t")
	}

	return kept, closing
}

// trailingBlankLinesStart returns where the blank lines at the end of comment lines start. The text before floor is not looked at, such as the end of the line of an opening bracket.
func trailingBlankLinesStart(lines []byte, floor int) int {
	end := len(lines)

	for end > floor {
		start := bytes.LastIndexByte(lines[:end-1], '\n') + 1

		if len(bytes.Trim(lines[start:end], " \t\n")) != 0 {
			break
		}

		end = start
	}

	return end
}

// leadingBlankLinesEnd returns where the blank lines at the start of comment lines end.
func leadingBlankLinesEnd(lines string) int {
	start := 0

	for start < len(lines) {
		length := strings.IndexByte(lines[start:], '\n')

		// A last line without a line feed is the indentation of what follows, such as a closing bracket, not a blank line.
		if length < 0 {
			break
		}

		end := start + length + 1

		if strings.Trim(lines[start:end], " \t\n") != "" {
			break
		}

		start = end
	}

	return start
}

// cloneNode copies a tree, so that a change can be made on the copy. Scalars do not change, so they are shared.
func cloneNode(node Node) Node {
	switch node := node.(type) {
	case *Object:
		copied := *node
		copied.Members = make([]*Member, len(node.Members))

		for index, member := range node.Members {
			memberCopy := *member
			memberCopy.Value = cloneNode(member.Value)
			copied.Members[index] = &memberCopy
		}

		return &copied
	case *Array:
		copied := *node
		copied.Items = make([]*Item, len(node.Items))

		for index, item := range node.Items {
			itemCopy := *item
			itemCopy.Value = cloneNode(item.Value)
			copied.Items[index] = &itemCopy
		}

		return &copied
	}

	return node
}
