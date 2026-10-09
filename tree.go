package soml

/*
A lossless syntax tree, for tools that read and change SOML documents.

A Document holds the tree. An unchanged tree prints the exact text it was parsed from, so comments, blank lines, indentation, member order, and the author's spelling of every key and value survive. A changed tree keeps all of that outside the change.

Whitespace and comments between tokens are Decor, and every piece of it belongs to exactly one node:

- The text between an item and the next token (after the item's comma, if it has one) is split at its first line feed outside a block comment. The part up to and including that line feed is the item's trailing decor: usually a comment at the end of its line. The rest is the next item's leading decor: the comment lines above it, blank lines, and its indentation. When there is no line feed, as in `{a: 1, b: 2}`, all of it is the next item's leading decor. The one exception is the end of a document without a final line feed, where the rest of the last line, such as a comment, is the last member's trailing decor.
- The text after the last item, before `}` or `]`, is the container's closing decor. The text before and after a braced top-level collection belongs to the document.
- In a braced container, each item records whether it has a comma. Items are separated by a comma, a line break, or both, so a comma is printed only where the item has one, or where nothing else separates it from the next item.
*/

import (
	"iter"
	"strconv"
	"strings"
	"time"
)

/*
Node is a node of a Document's tree: an *Object, an *Array, or a *Scalar.
*/
type Node interface {
	// Span returns the byte range of the node in the text the document was parsed from. It reports false for a node that was added or replaced.
	Span() (Span, bool)

	isNode()
}

/*
Span is a byte range in the text a Document was parsed from, so `text[span.Start:span.End]` is the source of a node.
*/
type Span struct {
	Start int
	End   int
}

// span is the span of a node, which a node that was added does not have.
type span struct {
	value   Span
	isValid bool
}

func (span span) get() (Span, bool) {
	return span.value, span.isValid
}

func spanOf(start, end int) span {
	return span{value: Span{Start: start, End: end}, isValid: true}
}

/*
Decor is the whitespace and comments around a member or an array item.
*/
type Decor struct {
	// Leading is the text before the item: the comment lines and blank lines above it, and its indentation.
	Leading string

	// Trailing is the text after the item and its comma, if it has one, up to and including the line feed that ends its line: usually empty, a line feed, or a comment at the end of the line and a line feed.
	Trailing string
}

/*
Object is an object, with its members in the order they are written.

Changing Members can make a document that is not valid, such as one with two members with the same key. Document.Decode finds that. Document.Set and Document.Remove keep the document valid and lay out what they add.
*/
type Object struct {
	Members []*Member

	// ClosingDecor is the text after the last member: before the closing `}`, or at the end of a top-level object without braces. For a top-level object without braces and without members, which only a change to Members can make, it is printed before its `{}`.
	ClosingDecor string

	isBare bool
	span   span
}

func (*Object) isNode() {}

// Span returns the byte range of the object. For a top-level object without braces, it runs from the first member to the end of the last one.
func (object *Object) Span() (Span, bool) {
	return object.span.get()
}

// memberIndex returns the position of the first member with a key.
// Note: Not using slices.IndexFunc because a direct loop avoids a callback for every key comparison.
func (object *Object) memberIndex(key string) (int, bool) {
	for index, member := range object.Members {
		if member.Key.value == key {
			return index, true
		}
	}

	return 0, false
}

// IsBraced reports whether the object is written with braces. Only a top-level object can be written without them. A top-level object without braces and without members, which only a change to Members can make, is printed as `{}`, and it still reports false. Document.Remove of its last member gives a braced `{}` instead, as a reparse does.
func (object *Object) IsBraced() bool {
	return !object.isBare
}

/*
Array is an array.
*/
type Array struct {
	Items []*Item

	// ClosingDecor is the text after the last item, before the closing `]`.
	ClosingDecor string

	span span
}

func (*Array) isNode() {}

// Span returns the byte range of the array, from its `[` to its `]`.
func (array *Array) Span() (Span, bool) {
	return array.span.get()
}

/*
Member is a member of an object: a key and a value. Make one with NewMember.
*/
type Member struct {
	Key   Key
	Value Node
	Decor Decor

	// The text between the `:` and the value, usually one space.
	afterColon string

	separator separator
	span      span
}

// Span returns the byte range of the member, from its key to the end of its value.
func (member *Member) Span() (Span, bool) {
	return member.span.get()
}

/*
Item is an item of an array. Make one with NewItem.
*/
type Item struct {
	Value Node
	Decor Decor

	separator separator
}

/*
The comma of a member or an item.
*/
type separator struct {
	// Whether the item has a comma after it. An item that is not the last also gets one when printed if no line break separates it from the next item. In a top-level object without braces, commas are never printed.
	hasComma bool

	// The text between the value and its comma, usually empty.
	beforeComma string
}

/*
A member of a braced object or an item of an array, which both have decor and may have a comma, so that inserting, removing, and printing them is one piece of code.
*/
type entry interface {
	*Member | *Item

	decor() *Decor
	comma() *separator
	setValue(Node)

	// print writes the entry without its decor and comma.
	print(*treePrinter)
}

func (member *Member) decor() *Decor {
	return &member.Decor
}

func (member *Member) comma() *separator {
	return &member.separator
}

func (member *Member) setValue(value Node) {
	member.Value = value
}

func (member *Member) print(printer *treePrinter) {
	printer.member(member)
}

func (item *Item) decor() *Decor {
	return &item.Decor
}

func (item *Item) comma() *separator {
	return &item.separator
}

func (item *Item) setValue(value Node) {
	item.Value = value
}

func (item *Item) print(printer *treePrinter) {
	printer.node(item.Value)
}

/*
Key is the key of a member. Make one with NewKey.
*/
type Key struct {
	value string
	raw   string
	span  span
}

/*
NewKey returns a key, which is written bare when it can be, and quoted otherwise, so a key that holds a `.` is one key.
*/
func NewKey(value string) Key {
	return Key{value: value}
}

// Value returns the key, decoded.
func (key Key) Value() string {
	return key.value
}

// Raw returns the text the author wrote for the key, or "" for a key that was added, which is written bare when it can be and quoted otherwise.
func (key Key) Raw() string {
	return key.raw
}

// Style returns how the key is written: StyleBare, StyleLiteral, or StyleEscaped.
func (key Key) Style() StringStyle {
	switch {
	case key.raw != "":
		return styleOf(key.raw)
	case isBareKey(key.value):
		return StyleBare
	case needsEscapes(key.value):
		return StyleEscaped
	}

	return StyleLiteral
}

// Span returns the byte range of the key. It reports false for a key that was added.
func (key Key) Span() (Span, bool) {
	return key.span.get()
}

// String returns the key as it is written in the document.
func (key Key) String() string {
	return string(key.appendTo(nil))
}

func (key Key) appendTo(buffer []byte) []byte {
	if key.raw != "" {
		return append(buffer, key.raw...)
	}

	return appendKey(buffer, key.value)
}

/*
StringStyle is how a string or a key is written.
*/
type StringStyle int

const (
	// StyleNone is the style of a scalar that is not a string.
	StyleNone StringStyle = iota

	// StyleBare is a bare key, without quotes. Only a key can be bare.
	StyleBare

	// StyleLiteral is `'...'`, with no escapes.
	StyleLiteral

	// StyleEscaped is `"..."`, with escapes.
	StyleEscaped

	// StyleLiteralBlock is `'''...'''`, a literal block string.
	StyleLiteralBlock

	// StyleEscapedBlock is `"""..."""`, a block string with escapes.
	StyleEscapedBlock
)

func styleOf(raw string) StringStyle {
	switch {
	case strings.HasPrefix(raw, "'''"):
		return StyleLiteralBlock
	case strings.HasPrefix(raw, `"""`):
		return StyleEscapedBlock
	case strings.HasPrefix(raw, "'"):
		return StyleLiteral
	case strings.HasPrefix(raw, `"`):
		return StyleEscaped
	}

	return StyleBare
}

/*
Scalar is a string, number, bool, null, instant, or duration: its value, and the text the author wrote for it. A scalar does not change. To change a value, replace the node, such as with NewNode.
*/
type Scalar struct {
	value any
	raw   string
	span  span
}

func (*Scalar) isNode() {}

// Span returns the byte range of the value's text.
func (scalar *Scalar) Span() (Span, bool) {
	return scalar.span.get()
}

// Value returns the value, as Unmarshal into an `any` gives it: a string, an int64, a float64, a bool, a time.Time, a time.Duration, or nil for null.
func (scalar *Scalar) Value() any {
	return scalar.value
}

// Raw returns the text the author wrote for the value, such as `0xFF`, `'text'`, or `90m`, or "" for a value that was added, which is printed in canonical form.
func (scalar *Scalar) Raw() string {
	return scalar.raw
}

// Style returns how a string is written, or StyleNone for a value that is not a string.
func (scalar *Scalar) Style() StringStyle {
	text, ok := scalar.value.(string)

	switch {
	case !ok:
		return StyleNone
	case scalar.raw != "":
		return styleOf(scalar.raw)
	case needsEscapes(text):
		return StyleEscaped
	}

	return StyleLiteral
}

// Radix returns the radix an int is written in: 2, 8, 10, or 16. It returns 0 for a value that is not an int.
func (scalar *Scalar) Radix() int {
	if _, ok := scalar.value.(int64); !ok {
		return 0
	}

	switch {
	case strings.HasPrefix(scalar.raw, "0x"):
		return 16
	case strings.HasPrefix(scalar.raw, "0o"):
		return 8
	case strings.HasPrefix(scalar.raw, "0b"):
		return 2
	}

	return 10
}

// appendTo writes the text the author wrote, or the canonical form of a value that was added.
func (scalar *Scalar) appendTo(buffer []byte) []byte {
	if scalar.raw != "" {
		return append(buffer, scalar.raw...)
	}

	return appendPlainScalar(buffer, scalar.value)
}

// appendPlainScalar writes a scalar of the types that Unmarshal into an `any` gives, which a tree has already checked, in canonical form.
func appendPlainScalar(buffer []byte, value any) []byte {
	switch value := value.(type) {
	case nil:
		return append(buffer, "null"...)
	case bool:
		return strconv.AppendBool(buffer, value)
	case int64:
		return strconv.AppendInt(buffer, value, 10)
	case float64:
		text, _ := appendFloat(buffer, value, 64)
		return text
	case string:
		return appendString(buffer, value)
	case time.Time:
		text, _ := appendInstant(buffer, value)
		return text
	case time.Duration:
		return appendDuration(buffer, value)
	}

	panic("soml: a scalar holds a value of an unexpected type")
}

/*
Comment is a comment in a document.
*/
type Comment struct {
	// Text is the text of the comment, without its delimiters.
	Text string

	// IsBlock reports whether it is a `/* */` block comment, rather than a `#` line comment.
	IsBlock bool

	// Span is the byte range of the comment, with its delimiters, in the document's current text.
	Span Span
}

/*
NewNode returns a node for a value, with no decor, printed as Marshal writes it, so a struct's fields keep their declared order. The value can be any Go value that Marshal accepts. Use it to replace the value of a member or an item, which keeps the decor around it.

It returns an error when Marshal would, and for a value nested so deep that it fits in no document.
*/
func NewNode(value any) (Node, error) {
	node, err := valueNode(value)

	if err != nil {
		return nil, err
	}

	return layOut(node, "", false), nil
}

/*
NewMember returns a member with a key and a value, with no decor. The value can be any Go value that Marshal accepts.
*/
func NewMember(key Key, value any) (*Member, error) {
	if err := checkRepresentable(key.value, "key"); err != nil {
		return nil, err
	}

	node, err := NewNode(value)

	if err != nil {
		return nil, err
	}

	return &Member{Key: key, Value: node, afterColon: " "}, nil
}

/*
NewItem returns an array item with a value, with no decor. The value can be any Go value that Marshal accepts.
*/
func NewItem(value any) (*Item, error) {
	node, err := NewNode(value)

	if err != nil {
		return nil, err
	}

	return &Item{Value: node}, nil
}

// valueNode converts a Go value to a node, with every check of Marshal, by writing it and parsing it back. Its members are in the order Marshal writes them in.
func valueNode(value any) (Node, error) {
	// In an array, because a document cannot be a scalar, and a top-level object would be written without braces.
	text, err := Marshal([]any{value})

	// The path of an error starts with the position in the array around the value, which is not part of it.
	if somlError, ok := err.(*Error); ok && len(somlError.Path) > 0 {
		somlError.Path = somlError.Path[1:]

		if len(somlError.Path) == 0 {
			somlError.Path = nil
		}
	}

	if err != nil {
		return nil, err
	}

	document, err := Parse(text)

	if err != nil {
		return nil, err
	}

	return document.root.(*Array).Items[0].Value, nil
}

/*
layOut returns a new node with the value of a node, in the same order, without its source text, positions, and decor. Its members and items are on their own lines, indented by indentation and one tab more, as in canonical form. With isOneLine, they are on one line instead, with a comma and a space between them, as in `[1, {a: 2}]`, for a value inside a container that stays on one line. Every member and item but the last then has its comma, as in a parsed tree.
*/
func layOut(node Node, indentation string, isOneLine bool) Node {
	inner := indentation + "\t"

	switch node := node.(type) {
	case *Object:
		object := &Object{}

		for index, member := range node.Members {
			object.Members = append(object.Members, &Member{
				Key:        NewKey(member.Key.value),
				Value:      layOut(member.Value, inner, isOneLine),
				Decor:      laidOutDecor(inner, index == 0, isOneLine),
				afterColon: " ",
				separator:  separator{hasComma: isOneLine && index < len(node.Members)-1},
			})
		}

		if len(node.Members) > 0 && !isOneLine {
			object.ClosingDecor = indentation
		}

		return object
	case *Array:
		array := &Array{}

		for index, item := range node.Items {
			array.Items = append(array.Items, &Item{
				Value:     layOut(item.Value, inner, isOneLine),
				Decor:     laidOutDecor(inner, index == 0, isOneLine),
				separator: separator{hasComma: isOneLine && index < len(node.Items)-1},
			})
		}

		if len(node.Items) > 0 && !isOneLine {
			array.ClosingDecor = indentation
		}

		return array
	}

	return &Scalar{value: node.(*Scalar).value}
}

// laidOutDecor is the decor of an item on its own line, as layOut lays it out: the first item's line feed is in its leading decor, because it follows the opening bracket, and every item's trailing decor ends its line. On one line, a space comes before every item but the first.
func laidOutDecor(indentation string, isFirst, isOneLine bool) Decor {
	if isOneLine {
		if isFirst {
			return Decor{}
		}

		return Decor{Leading: " "}
	}

	if isFirst {
		return Decor{Leading: "\n" + indentation, Trailing: "\n"}
	}

	return Decor{Leading: indentation, Trailing: "\n"}
}

// buildTree builds the tree of a document that the parser has already accepted, so it trusts its input. It returns the text before a braced top-level collection, the collection, and the text after it.
func buildTree(source string) (leading string, root Node, trailing string) {
	builder := treeBuilder{source: source}
	start := builder.skipTrivia(0)

	if character := source[start]; character == '{' || character == '[' {
		root, end := builder.value(start)
		return source[:start], root, source[end:]
	}

	return "", builder.bareObject(), ""
}

// leadingIndentation returns the spaces and tabs at the start of text.
func leadingIndentation(text string) string {
	return text[:len(text)-len(strings.TrimLeft(text, " \t"))]
}

type treeBuilder struct {
	source string

	// The chunks the tree's nodes come from. A large document has a node per member, item, container, and scalar, so building one directly would make many small allocations; a chunk makes fewer, larger ones.
	objectChunk []Object
	arrayChunk  []Array
	memberChunk []Member
	itemChunk   []Item
	scalarChunk []Scalar

	// The members and items of the open containers. A nested container is finished before the next entry of the one around it, so every container collects its entries at the top of one stack, and they are then copied out at their size. A slice of its own for each container would allocate every time it grows.
	memberStack []*Member
	itemStack   []*Item
}

// treeChunkSize is the size of the first chunk of tree nodes, which is small, so that a small document does not allocate a large chunk of each type. A chunk bigger than a cache line does not matter here, because the tree is built once and then read.
const treeChunkSize = 16

// newNode returns a spare node of the tree from a chunk. A node that is returned keeps its address as the chunk grows, because each chunk is allocated whole. The next chunk is one and a half times the last, so that a large document does not waste a chunk twice its size.
func newNode[T any](chunk *[]T) *T {
	if len(*chunk) == cap(*chunk) {
		*chunk = make([]T, 0, max(treeChunkSize, cap(*chunk)+cap(*chunk)/2))
	}

	var zero T
	*chunk = append(*chunk, zero)
	return &(*chunk)[len(*chunk)-1]
}

func (builder *treeBuilder) newObject() *Object {
	return newNode(&builder.objectChunk)
}

func (builder *treeBuilder) newArray() *Array {
	return newNode(&builder.arrayChunk)
}

func (builder *treeBuilder) newMember() *Member {
	return newNode(&builder.memberChunk)
}

func (builder *treeBuilder) newItem() *Item {
	return newNode(&builder.itemChunk)
}

func (builder *treeBuilder) newScalar() *Scalar {
	return newNode(&builder.scalarChunk)
}

// skipTrivia returns the index after the spaces, tabs, line feeds, and comments at index.
func (builder *treeBuilder) skipTrivia(index int) int {
	end, _, _ := skipTrivia(builder.source, index)
	return end
}

// splitAfterLineFeed splits the decor between start and end after its first line feed outside a block comment. It returns the index of the split, which is start when there is no line feed.
func (builder *treeBuilder) splitAfterLineFeed(start, end int) int {
	if lineFeedEnd, ok := firstLineFeedEnd(builder.source[start:end]); ok {
		return start + lineFeedEnd
	}

	return start
}

func (builder *treeBuilder) bareObject() *Object {
	object := builder.newObject()
	object.isBare = true
	leadingStart := 0

	for {
		keyStart := builder.skipTrivia(leadingStart)
		member, valueEnd := builder.member(keyStart)
		member.Decor.Leading = builder.source[leadingStart:keyStart]
		next := builder.skipTrivia(valueEnd)
		split := builder.splitAfterLineFeed(valueEnd, next)

		// The last line of a document without a final line feed ends at the end of the text, so a comment there is the last member's.
		if next >= len(builder.source) && split == valueEnd {
			split = len(builder.source)
		}

		member.Decor.Trailing = builder.source[valueEnd:split]
		object.Members = append(object.Members, member)

		if next >= len(builder.source) {
			// From the first member to the end of the last, as in the JS reference. The text around them is decor.
			object.span = spanOf(object.Members[0].span.value.Start, valueEnd)
			object.ClosingDecor = builder.source[split:]
			return object
		}

		leadingStart = split
	}
}

// member reads a member, without its decor, and returns the index after its value.
func (builder *treeBuilder) member(start int) (*Member, int) {
	reader := parser{source: builder.source, index: start}
	key, _ := reader.parseKey()
	colon := reader.index

	// Past the `:`.
	valueStart := builder.skipTrivia(colon + 1)
	value, valueEnd := builder.value(valueStart)
	member := builder.newMember()
	member.Key = Key{value: key, raw: builder.source[start:colon], span: spanOf(start, colon)}
	member.Value = value
	member.afterColon = builder.source[colon+1 : valueStart]
	member.span = spanOf(start, valueEnd)
	return member, valueEnd
}

// value reads a value, and returns the index after it. A scalar is read by the parser, so the two cannot disagree.
func (builder *treeBuilder) value(start int) (Node, int) {
	switch builder.source[start] {
	case '{':
		return builder.bracedObject(start)
	case '[':
		return builder.array(start)
	}

	reader := parser{source: builder.source, index: start}
	scalar, _ := reader.parseValue(0)
	end := reader.index
	node := builder.newScalar()
	node.value = scalar.toAny()
	node.raw = builder.source[start:end]
	node.span = spanOf(start, end)
	return node, end
}

func (builder *treeBuilder) bracedObject(start int) (Node, int) {
	object := builder.newObject()
	var end int
	object.Members, object.ClosingDecor, end = readEntries(builder, &builder.memberStack, start, '}', builder.member)

	object.span = spanOf(start, end)
	return object, end
}

func (builder *treeBuilder) array(start int) (Node, int) {
	array := builder.newArray()
	var end int
	array.Items, array.ClosingDecor, end = readEntries(builder, &builder.itemStack, start, ']', func(index int) (*Item, int) {
		value, end := builder.value(index)
		item := builder.newItem()
		item.Value = value
		return item, end
	})

	array.span = spanOf(start, end)
	return array, end
}

// readEntries reads the members or items of a braced container that opens at start, with read reading one entry and returning the index after it, and sets each entry's separator and decor. The entries are collected on stack. It returns the entries, the closing decor, and the index after the closing bracket.
func readEntries[T entry](builder *treeBuilder, stack *[]T, start int, closingBracket byte, read func(int) (T, int)) ([]T, string, int) {
	base := len(*stack)
	leadingStart := start + 1

	for {
		itemStart := builder.skipTrivia(leadingStart)

		if builder.source[itemStart] == closingBracket {
			// A copy, because the stack is reused, and nil for an empty container.
			// Note: Not using slices.Clone because an empty container must get nil even when its scratch slice is non-nil.
			entries := append([]T(nil), (*stack)[base:]...)
			// Note: Not using clear because the tree retains these entries and the scratch stack is discarded after parsing.
			*stack = (*stack)[:base]
			return entries, builder.source[leadingStart:itemStart], itemStart + 1
		}

		entry, valueEnd := read(itemStart)
		decor := entry.decor()
		decor.Leading = builder.source[leadingStart:itemStart]
		afterValue := builder.skipTrivia(valueEnd)
		trailingStart := valueEnd

		if builder.source[afterValue] == ',' {
			*entry.comma() = separator{hasComma: true, beforeComma: builder.source[valueEnd:afterValue]}
			trailingStart = afterValue + 1
		}

		next := builder.skipTrivia(trailingStart)
		split := builder.splitAfterLineFeed(trailingStart, next)
		decor.Trailing = builder.source[trailingStart:split]
		*stack = append(*stack, entry)
		leadingStart = split
	}
}

/*
treePrinter writes a tree, and finds the comments in it on the way.
*/
type treePrinter struct {
	output []byte

	// The comments, when they are wanted.
	comments     []Comment
	wantComments bool

	// Whether changed decor ended inside a line comment, so a line feed must come before anything else is printed. It is dropped at the end of the document, so a document that ends in a comment prints back unchanged.
	needsLineFeed bool
}

// write writes text that is not decor.
func (printer *treePrinter) write(text string) {
	printer.endLineComment()
	printer.output = append(printer.output, text...)
}

func (printer *treePrinter) endLineComment() {
	if printer.needsLineFeed {
		printer.output = append(printer.output, '\n')
		printer.needsLineFeed = false
	}
}

func (printer *treePrinter) document(leading string, root Node, trailing string) {
	printer.decor(leading)
	printer.node(root)
	printer.decor(trailing)
}

func (printer *treePrinter) node(node Node) {
	switch node := node.(type) {
	case *Object:
		printer.object(node)
	case *Array:
		printer.array(node)
	case *Scalar:
		printer.endLineComment()
		printer.output = node.appendTo(printer.output)
	default:
		// A nil value, which only a node-level change can make, prints as null, so printing never fails.
		printer.write("null")
	}
}

func (printer *treePrinter) object(object *Object) {
	if object.isBare {
		// A top-level object without braces needs at least one member. Its closing decor, such as the comment lines above a member that was removed, goes before the braces, where it was.
		if len(object.Members) == 0 {
			printer.decor(object.ClosingDecor)

			// A block comment that ends the decor ends its line too, as a line comment does.
			if strings.Trim(object.ClosingDecor, " \t\n") != "" && !endsLine(object.ClosingDecor) {
				printer.needsLineFeed = true
			}

			printer.write("{}\n")
			return
		}

		for index, member := range object.Members {
			// Members are separated by a line break, which an added member may not have in its decor. It is written before what comes next, as after a line comment, so that it is written once.
			if index > 0 && !hasLineFeed(object.Members[index-1].Decor.Trailing) && !hasLineFeed(member.Decor.Leading) {
				printer.needsLineFeed = true
			}

			printer.decor(member.Decor.Leading)
			printer.member(member)

			// A member moved here from braces can have a comment before its comma. The comma is not printed here, but the comment is, so it is not lost.
			printer.decor(member.separator.beforeComma)
			printer.decor(member.Decor.Trailing)
		}

		printer.decor(object.ClosingDecor)
		return
	}

	printer.write("{")
	printEntries(printer, object.Members)
	printer.decor(object.ClosingDecor)
	printer.write("}")
}

func (printer *treePrinter) member(member *Member) {
	printer.endLineComment()
	printer.output = member.Key.appendTo(printer.output)
	printer.output = append(printer.output, ':')
	printer.decor(member.afterColon)
	printer.node(member.Value)
}

func (printer *treePrinter) array(array *Array) {
	printer.write("[")
	printEntries(printer, array.Items)
	printer.decor(array.ClosingDecor)
	printer.write("]")
}

// printEntries writes the members or items of a braced container, with a comma after each one that has one, and after each one that is not the last when no line break separates it from the next, so that printing never fails.
func printEntries[T entry](printer *treePrinter, entries []T) {
	for index, entry := range entries {
		printer.decor(entry.decor().Leading)
		entry.print(printer)

		// Printed also when the comma was dropped, so a comment before it is not lost.
		printer.decor(entry.comma().beforeComma)

		if entry.comma().hasComma || index+1 < len(entries) && !isLineSeparated(entry, entries[index+1]) {
			printer.write(",")
		}

		printer.decor(entry.decor().Trailing)
	}
}

// isLineSeparated reports whether a line break outside a block comment comes between an entry and the next one.
func isLineSeparated[T entry](entry, next T) bool {
	return hasLineFeed(entry.decor().Trailing) || hasLineFeed(next.decor().Leading)
}

// decor writes decor, and records the comments in it.
func (printer *treePrinter) decor(decor string) {
	// Decor that starts with a line feed ends a pending line comment itself.
	if strings.HasPrefix(decor, "\n") {
		printer.needsLineFeed = false
	} else if decor != "" {
		printer.endLineComment()
	}

	start := len(printer.output)
	printer.output = append(printer.output, decor...)

	for part := range decorParts(decor) {
		// A line comment at the end of changed decor would otherwise swallow whatever is printed next.
		printer.needsLineFeed = part.kind == lineCommentPart && part.end == len(decor)

		if !printer.wantComments || part.kind == lineFeedPart {
			continue
		}

		isBlock := part.kind == blockCommentPart
		text := decor[part.start+1 : part.end]

		if isBlock {
			text = decor[part.start+2 : part.end]

			// An unterminated block comment, which only changed decor can have, runs to the end.
			text = strings.TrimSuffix(text, "*/")
		}

		printer.comments = append(printer.comments, Comment{Text: text, IsBlock: isBlock, Span: Span{Start: start + part.start, End: start + part.end}})
	}
}

type decorPartKind int

const (
	// A line feed outside a comment.
	lineFeedPart decorPartKind = iota
	lineCommentPart
	blockCommentPart
)

type decorPart struct {
	kind       decorPartKind
	start, end int
}

// decorParts finds the line feeds and comments in decor, with their ranges. Decor holds only whitespace and comments, so a `#` or the opening of a block comment in it always starts a comment. A block comment without its end, which only changed decor can have, runs to the end.
func decorParts(decor string) iter.Seq[decorPart] {
	return func(yield func(decorPart) bool) {
		for index := 0; index < len(decor); {
			start := index
			var part decorPart

			switch {
			case decor[index] == '\n':
				index++
				part = decorPart{lineFeedPart, start, index}
			case decor[index] == '#':
				index = lineEnd(decor, index)
				part = decorPart{lineCommentPart, start, index}
			case decor[index] == '/' && at(decor, index+1) == '*':
				if end, ok := blockCommentEnd(decor, index); ok {
					index = end + 2
				} else {
					index = len(decor)
				}

				part = decorPart{blockCommentPart, start, index}
			default:
				index++
				continue
			}

			if !yield(part) {
				return
			}
		}
	}
}

// hasLineFeed reports whether decor has a line feed outside its block comments.
func hasLineFeed(decor string) bool {
	_, ok := firstLineFeedEnd(decor)
	return ok
}

// firstLineFeedEnd returns the index after the first line feed in decor that is not inside a block comment.
func firstLineFeedEnd(decor string) (int, bool) {
	for part := range decorParts(decor) {
		if part.kind == lineFeedPart {
			return part.end, true
		}
	}

	return 0, false
}
