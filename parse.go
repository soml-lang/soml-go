package soml

// The parser. It checks every rule in the spec and builds a tree in which every value and key knows its offset in the source, so that a decoding error can say where it is.

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxDepth is the deepest nesting a document may have, which the spec fixes exactly: every reader accepts every document up to it, and rejects every document deeper. Every object and array counts, including a top-level object without braces.
const maxDepth = 100

type kind uint8

const (
	kindNull kind = iota
	kindBool
	kindInt
	kindFloat
	kindString
	kindInstant
	kindDuration
	kindArray
	kindObject
)

/*
A value in a parsed document.
*/
type node struct {
	kind kind

	// The nanoseconds of an instant.
	nanoseconds int32

	// The byte offset of the value in the source.
	offset int

	// A bool (0 or 1), an int, a duration in nanoseconds, the bits of a float, or the Unix seconds of an instant. One field for all of them keeps a node small, which matters, because a large document has many.
	integer int64

	text string

	// The members of an object or the items of an array. One pointer for both keeps a node small.
	collection *collection
}

func (value *node) float() float64 {
	return math.Float64frombits(uint64(value.integer))
}

func (value *node) setFloat(number float64) {
	value.integer = int64(math.Float64bits(number))
}

func (value *node) instant() time.Time {
	return time.Unix(value.integer, int64(value.nanoseconds)).UTC()
}

func (value *node) setInstant(instant time.Time) {
	value.integer = instant.Unix()
	value.nanoseconds = int32(instant.Nanosecond())
}

/*
The content of an object or an array.
*/
type collection struct {
	// The members of an object, in the order they are written.
	members []member

	// The items of an array.
	items []node
}

type member struct {
	key       string
	keyOffset int
	value     node
}

// maxUnindexedMembers is the number of members up to which an object is searched one member at a time, which is faster than hashing for the small objects that config files are made of.
const maxUnindexedMembers = 16

// maxReusedKeyIndex is the number of keys up to which the key index of a depth is cleared for the next object at that depth. A larger one is replaced, because clearing a map takes time in proportion to its size.
const maxReusedKeyIndex = 256

// hasKey reports whether the open object at a depth already has a key.
func (parser *parser) hasKey(object *collection, depth int, key string) bool {
	if len(object.members) > maxUnindexedMembers {
		_, ok := parser.keyIndexes[depth][key]
		return ok
	}

	for position := range object.members {
		if object.members[position].key == key {
			return true
		}
	}

	return false
}

// addMember adds a member to the open object at a depth.
func (parser *parser) addMember(object *collection, depth int, added member) {
	object.members = append(object.members, added)

	if len(object.members) <= maxUnindexedMembers {
		return
	}

	for len(parser.keyIndexes) <= depth {
		parser.keyIndexes = append(parser.keyIndexes, nil)
	}

	index := parser.keyIndexes[depth]

	// The object is now too large to search one member at a time, so all its keys are indexed, in the map of the last large object at this depth.
	if len(object.members) == maxUnindexedMembers+1 {
		if index == nil || len(index) > maxReusedKeyIndex {
			index = make(map[string]struct{}, 2*maxUnindexedMembers)
			parser.keyIndexes[depth] = index
		} else {
			clear(index)
		}

		for position := range object.members {
			index[object.members[position].key] = struct{}{}
		}

		return
	}

	index[added.key] = struct{}{}
}

/*
parse parses a document. The source is a string, so every string value without escapes is a substring of it, with no copy.
*/
func parse(source string) (node, *Error) {
	if err := checkCharacters(source); err != nil {
		return node{}, err
	}

	parser := parser{source: source}
	value, err := parser.parseDocument()

	if err != nil {
		return node{}, errorAt(source, err.offset, err.message)
	}

	return value, nil
}

// isValidValue reports whether text is one valid value, so that an error message only suggests a fix that works.
func isValidValue(text string) bool {
	// In an array, because a bare value is not a document. Text such as `1, 2` makes more than one item.
	value, err := parse("[" + text + "]")
	return err == nil && len(value.collection.items) == 1
}

// checkCharacters finds the characters that are errors wherever they appear, so they are checked once, up front, as in the JS reference: invalid UTF-8, a byte order mark, a carriage return, and the other raw control characters.
func checkCharacters(source string) *Error {
	if !utf8.ValidString(source) {
		for offset := 0; offset < len(source); {
			character, size := utf8.DecodeRuneInString(source[offset:])

			if character == utf8.RuneError && size == 1 {
				if !utf8.FullRuneInString(source[offset:]) {
					return errorAt(source, offset, "incomplete UTF-8 sequence at the end of the input")
				}

				return errorAt(source, offset, fmt.Sprintf("invalid UTF-8 byte 0x%02X", source[offset]))
			}

			offset += size
		}
	}

	if strings.HasPrefix(source, "\uFEFF") {
		return errorAt(source, 0, "a byte order mark (BOM) is not allowed")
	}

	// A byte below 0x80 is never part of a multibyte UTF-8 sequence, so each byte is checked on its own.
	// Note: Not using strings.IndexAny because building its character set slows short inputs.
	for offset := range len(source) {
		character := source[offset]

		if !controlBytes[character] {
			continue
		}

		if character == '\r' {
			return errorAt(source, offset, "a carriage return (U+000D) is not allowed anywhere. Use LF line endings")
		}

		return errorAt(source, offset, fmt.Sprintf("a raw control character (U+%04X) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \\u{%x} inside \"...\"", character, character))
	}

	return nil
}

// isControl reports whether a byte is a raw control character that SOML refuses: U+0000 to U+001F except tab and line feed, and U+007F.
func isControl(character byte) bool {
	return character < 0x20 && character != '\t' && character != '\n' || character == 0x7F
}

// controlBytes is isControl for every byte, because checkCharacters checks every byte of the source, and a lookup is about twice as fast.
var controlBytes = func() (table [256]bool) {
	for character := range len(table) {
		table[character] = isControl(byte(character))
	}

	return table
}()

type parser struct {
	source string
	index  int

	// Scratch space for the members and items of the open containers, one for each depth. Only one container is open at a depth, so every container at a depth reuses its scratch space. A finished container's members or items are copied into a chunk, at their exact size, so that a large document makes few allocations and has no unused capacity.
	memberScratch   [][]member
	itemScratch     [][]node
	memberChunk     []member
	itemChunk       []node
	collectionChunk []collection

	// The keys of the open object at each depth, once it is too large to search one member at a time. Like the scratch space, each one is reused by the next large object at its depth, so that an object does not allocate its own.
	keyIndexes []map[string]struct{}

	// Where the document's collection starts, after the comments and whitespace before it.
	documentStart int
}

// chunkSize is the number of members, items, or objects that a chunk holds, unless one container needs more.
const chunkSize = 256

// newChunk returns an empty chunk with space for at least needed elements. The first chunk is small and each next one is twice the last, up to chunkSize, so that a small document does not allocate a full chunk of each type.
func newChunk[T any](last []T, needed int) []T {
	return make([]T, 0, max(needed, min(chunkSize, max(16, 2*cap(last)))))
}

// takeScratch returns the empty scratch space for a depth.
func takeScratch[T any](scratch *[][]T, depth int) []T {
	// Room for the depths and the containers of most documents from the start, so that the scratch space is not grown one small step at a time.
	if *scratch == nil {
		*scratch = make([][]T, 0, 8)
	}

	for len(*scratch) <= depth {
		*scratch = append(*scratch, nil)
	}

	if (*scratch)[depth] == nil {
		(*scratch)[depth] = make([]T, 0, 16)
	}

	return (*scratch)[depth][:0]
}

// keep copies the members or items of a finished container from the scratch space of its depth into a chunk, and returns the copy, whose capacity is its length, so that an append cannot reach the next one.
func keep[T any](scratch *[][]T, chunk *[]T, depth int, items []T) []T {
	// The scratch space keeps the capacity that an append grew it to, for the next container at this depth.
	(*scratch)[depth] = items[:0]

	if cap(*chunk)-len(*chunk) < len(items) {
		*chunk = newChunk(*chunk, len(items))
	}

	start := len(*chunk)
	*chunk = append(*chunk, items...)
	return (*chunk)[start:len(*chunk):len(*chunk)]
}

// newCollection returns an empty collection from a chunk.
func (parser *parser) newCollection() *collection {
	if len(parser.collectionChunk) == cap(parser.collectionChunk) {
		parser.collectionChunk = newChunk(parser.collectionChunk, 1)
	}

	parser.collectionChunk = append(parser.collectionChunk, collection{})
	return &parser.collectionChunk[len(parser.collectionChunk)-1]
}

// newObject returns an object from a chunk, whose members are collected in the scratch space of its depth.
func (parser *parser) newObject(depth int) *collection {
	object := parser.newCollection()
	object.members = takeScratch(&parser.memberScratch, depth)
	return object
}

// finishObject moves a finished object's members out of the scratch space of its depth.
func (parser *parser) finishObject(object *collection, depth int) {
	object.members = keep(&parser.memberScratch, &parser.memberChunk, depth, object.members)
}

func (parser *parser) peek() byte {
	return at(parser.source, parser.index)
}

func (parser *parser) isAtEnd() bool {
	return parser.index >= len(parser.source)
}

func (parser *parser) parseDocument() (node, *syntaxError) {
	if _, err := parser.skipTrivia(); err != nil {
		return node{}, err
	}

	parser.documentStart = parser.index

	if parser.isAtEnd() {
		return node{}, fail(parser.index, "a document must contain an object or an array, but this one is empty")
	}

	var value node
	var err *syntaxError

	switch parser.peek() {
	case '{':
		value, err = parser.parseObject(1)
	case '[':
		value, err = parser.parseArray(1)
	default:
		value, err = parser.parseBareObject()
	}

	if err != nil {
		return node{}, err
	}

	if _, err := parser.skipTrivia(); err != nil {
		return node{}, err
	}

	if !parser.isAtEnd() {
		return node{}, fail(parser.index, "unexpected %s after the end of the document", parser.describeHere())
	}

	return value, nil
}

// parseBareObject reads `bare-object = entry ( entry-sep entry )*`, where a separator is one or more line breaks.
func (parser *parser) parseBareObject() (node, *syntaxError) {
	start := parser.index
	object := parser.newObject(1)

	if err := parser.parseEntry(object, 1); err != nil {
		if diagnosis := parser.diagnoseBareValue(start); diagnosis != nil {
			return node{}, diagnosis
		}

		return node{}, err
	}

	for {
		hasLineBreak, err := parser.skipTrivia()

		if err != nil {
			return node{}, err
		}

		if parser.isAtEnd() {
			parser.finishObject(object, 1)
			return node{kind: kindObject, offset: start, collection: object}, nil
		}

		if parser.peek() == ',' {
			return node{}, fail(parser.index, "top-level entries are separated by line breaks, not commas. Use braces for a one-line object")
		}

		if !hasLineBreak {
			return node{}, fail(parser.index, "expected a line break before the next entry, but found %s%s", parser.describeHere(), parser.slashCommentHint())
		}

		if err := parser.parseEntry(object, 1); err != nil {
			return node{}, err
		}
	}
}

// diagnoseBareValue reports a whole document that is one scalar, such as `5` or an instant, as what it is. Such a document fails as an entry.
func (parser *parser) diagnoseBareValue(start int) *syntaxError {
	// A diagnosis runs after the parse has failed, and every error has its own offset, so it may move the index.
	parser.index = start

	if _, err := parser.parseValue(1); err != nil {
		return nil
	}

	if _, err := parser.skipTrivia(); err != nil || !parser.isAtEnd() {
		return nil
	}

	return fail(start, "a bare value is not a document. A document is an object or an array, so write it as `key: value` or `[value]`")
}

// skipTrivia skips spaces, tabs, line breaks, and comments, and reports whether it crossed a line break. A line break inside a block comment does not count.
func (parser *parser) skipTrivia() (bool, *syntaxError) {
	end, hasLineBreak, err := skipTrivia(parser.source, parser.index)
	parser.index = end
	return hasLineBreak, err
}

// skipTrivia returns the index after the spaces, tabs, line breaks, and comments at index, and whether it crossed a line break outside a block comment.
func skipTrivia(source string, index int) (int, bool, *syntaxError) {
	hasLineBreak := false

	for {
		switch at(source, index) {
		case ' ', '\t':
			index++
		case '\n':
			hasLineBreak = true
			index++
		case '#':
			index = lineEnd(source, index)
		case '/':
			if at(source, index+1) != '*' {
				return index, hasLineBreak, nil
			}

			end, err := skipBlockComment(source, index)

			if err != nil {
				return index, false, err
			}

			index = end
		default:
			return index, hasLineBreak, nil
		}
	}
}

// lineEnd is the index of the line feed that ends the line at index, or the length of the source.
func lineEnd(source string, index int) int {
	if length := strings.IndexByte(source[index:], '\n'); length >= 0 {
		return index + length
	}

	return len(source)
}

// skipBlockComment returns the index after the block comment that starts at start.
func skipBlockComment(source string, start int) (int, *syntaxError) {
	end, ok := blockCommentEnd(source, start)

	if !ok {
		return 0, fail(start, "unterminated block comment")
	}

	// The body may not contain `/*`. An opening that overlaps the closing `*/`, as in `/*/`, is not inside the body.
	if nested := strings.Index(source[start+2:end], "/*"); nested >= 0 {
		return 0, fail(start+2+nested, "block comments cannot be nested, and their body may not contain “/*”")
	}

	return end + 2, nil
}

// blockCommentEnd is the index of the closing delimiter of the block comment that starts at start.
func blockCommentEnd(source string, start int) (int, bool) {
	length := strings.Index(source[start+2:], "*/")

	if length < 0 {
		return 0, false
	}

	return start + 2 + length, true
}

// parseEntry reads `entry = key ":" ws value` into object.
func (parser *parser) parseEntry(object *collection, depth int) *syntaxError {
	keyStart := parser.index
	key, err := parser.parseKey()

	if err != nil {
		return err
	}

	// Nothing may come between a key and its `:`, so no whitespace or comment is skipped here.
	if parser.peek() == '.' {
		return parser.failDotInKey(keyStart)
	}

	if parser.peek() != ':' {
		return parser.failMissingColon(keyStart)
	}

	colon := parser.index
	parser.index++
	isValueOnNextLine, err := parser.skipTrivia()

	if err != nil {
		return err
	}

	valueStart := parser.index
	value, err := parser.parseValue(depth + 1)

	if err != nil {
		if diagnosis := parser.diagnoseBadValue(err, key, keyStart, colon, valueStart, isValueOnNextLine); diagnosis != nil {
			return diagnosis
		}

		return err
	}

	// After the value, as in the JS reference, so that an error in the value is reported first.
	if parser.hasKey(object, depth, key) {
		return fail(keyStart, "duplicate key %s", formatPath([]any{key}))
	}

	parser.addMember(object, depth, member{key: key, keyOffset: keyStart, value: value})
	return nil
}

// diagnoseBadValue reports a value that failed to parse as what the entry was meant to be, when that is clear. The value starts at valueStart, after the trivia that follows the colon.
func (parser *parser) diagnoseBadValue(err *syntaxError, key string, keyStart, colon, valueStart int, isValueOnNextLine bool) *syntaxError {
	// A key that contains a `:`, as in `12:30: 'lunch'`, ends at the first `:`, and the rest is read as the value.
	if isBareKeyByte(at(parser.source, colon+1)) {
		if diagnosis := parser.diagnoseKeyWithColon(keyStart); diagnosis != nil {
			return diagnosis
		}
	}

	commentHint := parser.describeCommentAsValue(colon)

	if isValueOnNextLine {
		if diagnosis := parser.diagnoseMissingValue(valueStart, key, keyStart, commentHint); diagnosis != nil {
			return diagnosis
		}
	}

	// A value that was left out at the end of the document or of an object.
	if commentHint != "" && err.offset == valueStart && (valueStart >= len(parser.source) || parser.source[valueStart] == '}') {
		return fail(valueStart, "%s%s", err.message, commentHint)
	}

	return nil
}

// diagnoseKeyWithColon reports a bare key followed directly by `:` and more of a key, up to a `:` that ends the key, as in `a:b: 1`.
func (parser *parser) diagnoseKeyWithColon(keyStart int) *syntaxError {
	source := parser.source

	for index := keyStart; index < keyStart+maxDiagnosedLength; index++ {
		character := at(source, index)

		if character == ':' {
			if endsKey(at(source, index+1)) {
				return fail(keyStart, "a key that contains “:” must be quoted, as in '%s'", abbreviate(source[keyStart:index], 40))
			}
		} else if !isBareKeyByte(character) {
			return nil
		}
	}

	return nil
}

// describeCommentAsValue is the hint for a `#` directly after a `:`, as in `color: #FFF`, which starts a comment rather than a value.
func (parser *parser) describeCommentAsValue(colon int) string {
	source := parser.source
	hash := skipSpaces(source, colon+1)

	if at(source, hash) != '#' {
		return ""
	}

	// A `#` followed by a space, or by another `#`, starts an ordinary comment. A separator or a closing bracket after the value is not part of it. Only the start of a long comment is looked at, which is cut short in the message anyway.
	limit := min(len(source), hash+maxDiagnosedLength)
	end := hash + 1

	if end >= limit || strings.IndexByte("\t\n #,]}", source[end]) >= 0 {
		return ""
	}

	for end < limit && strings.IndexByte("\t\n ,]}", source[end]) < 0 {
		end++
	}

	return ". “#” starts a comment, so a value that starts with “#” must be quoted" + quotingExample(source[hash:end])
}

// diagnoseMissingValue reports an entry whose value was left out, as in `a:` followed by `'b': 1` on the next line, which reads the next key as the value, as what it is. parent and keyStart are the key of the entry, and commentHint is the hint for a value that starts with “#”.
func (parser *parser) diagnoseMissingValue(start int, parent string, keyStart int, commentHint string) *syntaxError {
	// A YAML block sequence.
	if at(parser.source, start) == '-' && isSpace(at(parser.source, start+1)) {
		return fail(start, "expected a value, but found a “-” list. An array is written in brackets, as in [80, 443]")
	}

	parser.index = start
	key, err := parser.parseKey()

	if err != nil || parser.peek() != ':' {
		return nil
	}

	// Whitespace or the end follows the `:` of a bare key. A digit follows the `:` in an instant such as `2026-09-19T25:00:00Z`, whose own error is more precise. A quoted key cannot be part of a value, so anything may follow its `:`. The source has no NUL, so `at` gives 0 only at the end.
	isQuoted := parser.source[start] == '\'' || parser.source[start] == '"'

	if !isQuoted && !endsKey(at(parser.source, parser.index+1)) {
		return nil
	}

	hint := commentHint

	if hint == "" {
		hint = parser.describeIndentedKey(parent, key, keyStart, start)
	}

	return fail(start, "expected a value, but found the key %s%s", formatPath([]any{key}), hint)
}

// describeIndentedKey is the hint for a key at start that is indented under the entry whose value is missing, as YAML nests an object.
func (parser *parser) describeIndentedKey(parent, key string, keyStart, start int) string {
	keyIndentation, isKeyIndented := indentation(parser.source, keyStart)
	valueIndentation, isValueIndented := indentation(parser.source, start)

	if !isKeyIndented || !isValueIndented || valueIndentation <= keyIndentation {
		return ""
	}

	// The keys are written as in a document, so that the suggestion is valid.
	return ". Indentation does not nest objects, so write " + abbreviate(string(appendKey(nil, parent)), 200) + ": {" + abbreviate(string(appendKey(nil, key)), 200) + ": …}"
}

// indentation is the number of spaces and tabs before offset on its line, and false when something else comes before it.
func indentation(source string, offset int) (int, bool) {
	lineStart := strings.LastIndexByte(source[:offset], '\n') + 1

	if skipSpaces(source, lineStart) != offset {
		return 0, false
	}

	return offset - lineStart, true
}

// endsKey reports whether the character after a `:` makes it the one that ends a key: a space, a tab, a line feed, or the end, which `at` gives as 0.
func endsKey(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == 0
}

func (parser *parser) failMissingColon(keyStart int) *syntaxError {
	next := parser.index

	for at(parser.source, next) == ' ' || at(parser.source, next) == '\t' {
		next++
	}

	nextCharacter := at(parser.source, next)

	if next > parser.index && nextCharacter == ':' {
		return fail(parser.index, "whitespace is not allowed between a key and its “:”")
	}

	// A block comment followed by the “:” was meant to come before it.
	if strings.HasPrefix(parser.source[next:], "/*") {
		if end, ok := blockCommentEnd(parser.source, next); ok && strings.HasPrefix(strings.TrimLeft(parser.source[end+2:], " \t"), ":") {
			return fail(next, "a comment is not allowed between a key and its “:”")
		}
	}

	// A key with a space in it, such as `the name: 1`. The hint is only given for plain words, because quoting a quoted key would change what it means, and for a `:` that ends a key, because in `server localhost:8080` it does not.
	if next > parser.index && isBareKeyByte(nextCharacter) {
		if colon := strings.IndexByte(parser.source[next:lineEnd(parser.source, next)], ':'); colon >= 0 && endsKey(at(parser.source, next+colon+1)) {
			key := strings.TrimRight(parser.source[keyStart:next+colon], " \t")

			if strings.Trim(key, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_- \t") == "" {
				return fail(keyStart, "a bare key cannot contain spaces. Quote it, as in '%s'", abbreviate(key, 40))
			}
		}
	}

	if parser.isAtEnd() || parser.peek() == '\n' {
		return fail(parser.index, "expected “:” after the key")
	}

	// A character directly after a bare key is most likely meant to be part of it.
	hint := ""

	if previous := at(parser.source, parser.index-1); previous != '\'' && previous != '"' && next == parser.index && parser.startsQuotableKey(next) {
		hint = keyQuotingHint
	}

	parser.index = next
	return fail(next, "expected “:” after the key, but found %s%s", parser.describeHere(), hint)
}

func (parser *parser) parseKey() (string, *syntaxError) {
	start := parser.index

	if character := parser.peek(); character == '\'' || character == '"' {
		if isBlockString(parser.source, start) {
			return "", fail(start, "a block string cannot be a key")
		}

		// A key is a string, so it may hold anything a string can, including a line feed written as an escape.
		value, end, err := readString(parser.source, start)

		if err != nil {
			return "", err
		}

		parser.index = end
		return value, nil
	}

	end := start

	for isBareKeyByte(at(parser.source, end)) {
		end++
	}

	if end == start {
		if parser.peek() == '[' {
			if diagnosis := parser.diagnoseTableHeader(false); diagnosis != nil {
				return "", diagnosis
			}
		}

		if parser.isAtEnd() {
			return "", fail(start, "expected a key")
		}

		return "", fail(start, "expected a key, but found %s%s%s", parser.describeHere(), parser.keyQuotingHint(), parser.slashCommentHint())
	}

	parser.index = end
	return parser.source[start:end], nil
}

// failDotInKey reports a `.` after a key, as in `example.com: 1`. A key is never a path, so the `.` is meant either as part of the key or as nesting.
func (parser *parser) failDotInKey(keyStart int) *syntaxError {
	source := parser.source
	end := parser.index

	for end < keyStart+maxDiagnosedLength && (isBareKeyByte(at(source, end)) || at(source, end) == '.') {
		end++
	}

	key := source[keyStart:end]
	segments := strings.Split(key, ".")

	// The suggestions are only given for a bare key whose dots each have a word on both sides, so that both are valid.
	if at(source, end) != ':' || !isBareKeyByte(source[keyStart]) || slices.Contains(segments, "") {
		return fail(parser.index, "a key cannot contain “.” unless it is quoted. Quote the whole key, or use braces to nest, as in a: {b: …}")
	}

	nested := segments[len(segments)-1] + ": …"

	for index := len(segments) - 2; index >= 0; index-- {
		nested = segments[index] + ": {" + nested + "}"
	}

	return fail(parser.index, "a bare key cannot contain “.”. Quote it, as in '%s', or use braces to nest, as in %s", abbreviate(key, 40), abbreviate(nested, 40))
}

const keyQuotingHint = ". A key that contains characters other than letters, digits, “_”, and “-” must be quoted"

// isQuotableKeyCharacter reports whether a character may be meant as part of a key although a bare key cannot hold it: a printable character that has no meaning in the grammar. A `=` is left out, because after a key, as in `name=foo`, it was meant as the `:` of INI and TOML.
func isQuotableKeyCharacter(character rune) bool {
	return !isIgnorableOrOther(character) && !unicode.IsSpace(character) && !strings.ContainsRune(`"#'*,/:=[]{}`, character)
}

func (parser *parser) startsQuotableKey(index int) bool {
	character, size := utf8.DecodeRuneInString(parser.source[index:])
	return size > 0 && isQuotableKeyCharacter(character)
}

// keyQuotingHint is the hint for a key that starts with a character a bare key cannot hold, such as the `$` in `$schema`, or nothing for a character with another meaning, such as `}`. When the key ends at a “:”, the hint shows it quoted.
func (parser *parser) keyQuotingHint() string {
	if !parser.startsQuotableKey(parser.index) {
		return ""
	}

	end := parser.index

	for end < len(parser.source) {
		character, size := utf8.DecodeRuneInString(parser.source[end:])

		if !isQuotableKeyCharacter(character) {
			break
		}

		end += size
	}

	if at(parser.source, end) != ':' {
		return keyQuotingHint
	}

	return keyQuotingHint + ", as in '" + abbreviate(parser.source[parser.index:end], 40) + "'"
}

func (parser *parser) parseObject(depth int) (node, *syntaxError) {
	object := parser.newObject(depth)
	start, err := parser.parseBraced(depth, '}', "object", "member", func() *syntaxError {
		return parser.parseEntry(object, depth)
	})

	if err != nil {
		return node{}, err
	}

	parser.finishObject(object, depth)
	return node{kind: kindObject, offset: start, collection: object}, nil
}

func (parser *parser) parseArray(depth int) (node, *syntaxError) {
	items := takeScratch(&parser.itemScratch, depth)
	start, err := parser.parseBraced(depth, ']', "array", "item", func() *syntaxError {
		item, err := parser.parseValue(depth + 1)

		if err != nil {
			return err
		}

		items = append(items, item)
		return nil
	})

	if err != nil {
		return node{}, err
	}

	array := parser.newCollection()
	array.items = keep(&parser.itemScratch, &parser.itemChunk, depth, items)
	return node{kind: kindArray, offset: start, collection: array}, nil
}

// parseBraced reads a braced container that opens here, with item reading one item. The items are separated by a comma, a line break, or both, and a trailing comma is allowed. A line break inside a block comment does not separate items. It returns the offset of the opening bracket.
func (parser *parser) parseBraced(depth int, closing byte, container, itemName string, item func() *syntaxError) (int, *syntaxError) {
	start := parser.index

	if depth > maxDepth {
		return 0, fail(start, "the document is nested more than %d levels deep", maxDepth)
	}

	parser.index++

	if _, err := parser.skipTrivia(); err != nil {
		return 0, err
	}

	for {
		if parser.peek() == closing {
			parser.index++
			return start, nil
		}

		if parser.isAtEnd() {
			return 0, fail(start, "unterminated %s: expected “%c”", container, closing)
		}

		if err := item(); err != nil {
			return 0, err
		}

		itemEnd := parser.index
		hasLineBreak, err := parser.skipTrivia()

		if err != nil {
			return 0, err
		}

		switch {
		case parser.peek() == ',':
			if hasLineBreak {
				return 0, fail(parser.index, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma")
			}

			parser.index++

			if _, err := parser.skipTrivia(); err != nil {
				return 0, err
			}
		case parser.peek() == closing:
		case parser.isAtEnd():
			return 0, fail(start, "unterminated %s: expected “%c”", container, closing)
		case hasLineBreak:
		default:
			// Without a line break that separates, a line break in the gap is inside a block comment.
			hint := ""

			if strings.Contains(parser.source[itemEnd:parser.index], "\n") {
				hint = ". A line break inside a block comment does not separate items"
			} else {
				hint = parser.slashCommentHint()
			}

			return 0, fail(parser.index, "expected “,”, a line break, or “%c” after an %s %s, but found %s%s", closing, container, itemName, parser.describeHere(), hint)
		}
	}
}

// isValueEnd reports whether a byte may directly follow a scalar: whitespace, a separator, a closing bracket, or the start of a comment. The end of the document may follow too.
func isValueEnd(character byte) bool {
	switch character {
	case ' ', '\t', '\n', ',', ']', '}', '#', '/':
		return true
	}

	return false
}

// isValueEndAt reports whether a value ends at index in source, so that a suggestion that ends there leaves nothing out. A `/` only starts a comment with a `*` after it, or a `//` comment of another language, which the error for what follows names, so in `km/h` the text goes on.
func isValueEndAt(source string, index int) bool {
	if index >= len(source) {
		return true
	}

	next := at(source, index+1)
	return isValueEnd(source[index]) && (source[index] != '/' || next == '*' || next == '/')
}

func (parser *parser) parseValue(depth int) (node, *syntaxError) {
	start := parser.index
	value := node{offset: start}
	// An int or a float written with digits.
	isNumber := false

	switch character := parser.peek(); {
	case character == '{':
		return parser.parseObject(depth)
	case character == '[':
		return parser.parseArray(depth)
	case character == '\'' || character == '"':
		text, end, err := readString(parser.source, start)

		if err != nil {
			return node{}, err
		}

		parser.index = end
		value.kind = kindString
		value.text = text
	case character == 't' && parser.isKeyword("true"):
		value.kind = kindBool
		value.integer = 1
	case character == 'f' && parser.isKeyword("false"):
		value.kind = kindBool
	case character == 'n' && parser.isKeyword("null"):
		value.kind = kindNull
	case character == 'i' && parser.isKeyword("infinity"):
		value.kind = kindFloat
		value.setFloat(math.Inf(1))
	case character == '-' && parser.isKeyword("-infinity"):
		value.kind = kindFloat
		value.setFloat(math.Inf(-1))
	case isDigit(character) || character == '-':
		if plain, end, ok := parser.plainNumber(start); ok {
			// plainNumber only accepts a number that a value end follows, so the check after the switch is not needed.
			parser.index = end

			if isSpace(parser.peek()) {
				if err := parser.diagnoseUnitAfterSpace(start); err != nil {
					return node{}, err
				}
			}

			return plain, nil
		}

		end := start + 1

		for isNumberByte(at(parser.source, end)) {
			end++
		}

		parser.index = end
		token, reason := tokenValue(parser.source, start, end)

		if reason != "" {
			return node{}, fail(start, "%s", reason)
		}

		token.offset = start
		value = token
		isNumber = token.kind == kindInt || token.kind == kindFloat
	default:
		return node{}, parser.failUnexpectedValue()
	}

	if !parser.isAtEnd() && !isValueEnd(parser.peek()) {
		return node{}, parser.failAfterValue(start, isNumber)
	}

	if isNumber && isSpace(parser.peek()) {
		if err := parser.diagnoseUnitAfterSpace(start); err != nil {
			return node{}, err
		}
	}

	return value, nil
}

// failAfterValue is the error for a character that cannot follow the value that starts at start.
func (parser *parser) failAfterValue(start int, isNumber bool) *syntaxError {
	character, _ := utf8.DecodeRuneInString(parser.source[parser.index:])

	// Go writes microseconds as `µs`, with the micro sign or the Greek letter mu, which are both two bytes in UTF-8. The duration is only suggested when it is valid, which a number with an exponent or a radix prefix, for example, is not.
	if isNumber && (character == 'µ' || character == 'μ') && at(parser.source, parser.index+2) == 's' {
		number := parser.source[start:parser.index]
		example := ""

		if isValidValue(number + "us") {
			example = ", as in " + abbreviate(number, 40) + "us"
		}

		return fail(parser.index, "the unit for microseconds is written us%s", example)
	}

	// A `''` inside a '...' string, as SQL and YAML escape a quote, ends the string.
	if character == '\'' && parser.source[start] == '\'' {
		return fail(parser.index, "there is no '' escape in a '...' string. Write a string that contains ' as \"...\", as in \"it's\"")
	}

	return fail(parser.index, "unexpected %s after a value", parser.describeHere())
}

// diagnoseUnitAfterSpace reports a number followed by a space and a word, such as `512 MiB` or `10 seconds`, which is an error anyway. A word followed by more than spaces, a separator, or a comment is left to the general errors, because it may be a key, as in `a: 1 b: 2`, or a sentence.
func (parser *parser) diagnoseUnitAfterSpace(start int) *syntaxError {
	source := parser.source
	unitStart := skipSpaces(source, parser.index)
	unitEnd := unitStart

	for isLetter(at(source, unitEnd)) {
		unitEnd++
	}

	unit := source[unitStart:unitEnd]
	after := skipSpaces(source, unitEnd)

	// A keyword after a number is a missing comma.
	if unit == "" || !isValueEndAt(source, after) || unit == "true" || unit == "false" || unit == "null" || unit == "infinity" {
		return nil
	}

	number := source[start:parser.index]
	// The duration is only suggested when it is valid, which a number with an exponent or a radix prefix, a fraction of a nanosecond, a negative zero, or a value outside the 64-bit range is not. The string is always offered, because a unit such as `m` may mean meters rather than minutes.
	duration := "10s"

	if isDurationUnit(unit) && isValidValue(number+unit) {
		duration = abbreviate(number, 40) + unit
	}

	return fail(unitStart, "a unit cannot follow a number after a space. Write a duration without the space, as in %s, and anything else, such as a size, as a string, as in '%s'", duration, abbreviate(number+" "+unit, 40))
}

// plainNumber reads the common case of a short decimal int or float, such as `8080`, `-3`, or `30.5`, without the general path's scan and checks. Anything else, including every error, is left to the general path.
func (parser *parser) plainNumber(start int) (node, int, bool) {
	source := parser.source
	integerStart := start

	if source[start] == '-' {
		integerStart++
	}

	integerEnd := integerStart

	for isDigit(at(source, integerEnd)) {
		integerEnd++
	}

	integerLength := integerEnd - integerStart

	// No digits, or a leading zero, which is either `0` alone or an error.
	if integerLength == 0 || integerLength > 1 && source[integerStart] == '0' {
		return node{}, 0, false
	}

	end := integerEnd

	if at(source, end) == '.' {
		fractionEnd := end + 1

		for isDigit(at(source, fractionEnd)) {
			fractionEnd++
		}

		if fractionEnd == end+1 {
			return node{}, 0, false
		}

		end = fractionEnd
	}

	// At most 18 digits, so an int cannot overflow. A character that may continue a number, such as `e`, `_`, or `:`, needs the general path.
	if end-integerStart > 18 || end < len(source) && !isValueEnd(source[end]) {
		return node{}, 0, false
	}

	text := source[start:end]

	// At most 18 digits always parse, and never overflow.
	if end > integerEnd {
		value, _ := strconv.ParseFloat(text, 64)

		// Negative zero is the same value as zero.
		if value == 0 {
			value = 0
		}

		result := node{kind: kindFloat, offset: start}
		result.setFloat(value)
		return result, end, true
	}

	// `-0` is an error, which the general path reports.
	if text == "-0" {
		return node{}, 0, false
	}

	value, _ := strconv.ParseInt(text, 10, 64)
	return node{kind: kindInt, offset: start, integer: value}, end, true
}

// isKeyword reports whether the keyword is here, as a whole word, and if so, moves past it. A longer word, such as `nullable`, is not the keyword.
func (parser *parser) isKeyword(word string) bool {
	end := parser.index + len(word)

	if !strings.HasPrefix(parser.source[parser.index:], word) || isBareKeyByte(at(parser.source, end)) {
		return false
	}

	parser.index = end
	return true
}

func (parser *parser) failUnexpectedValue() *syntaxError {
	start := parser.index

	switch {
	case parser.isAtEnd():
		return fail(start, "expected a value, but reached the end of the document")
	case parser.peek() == '+':
		return fail(start, "a “+” sign is not allowed. A number without a sign is positive")
	// A `.` that no digit follows begins a string, such as `.env` or `./foo`, rather than a number.
	case parser.peek() == '.' && isDigit(at(parser.source, start+1)):
		return fail(start, "a number cannot begin with “.”; write a digit before it, as in 0.5")
	case parser.peek() == '.':
		return fail(start, "%s", describeUnknownWord(".", unquotedText(parser.source, start)))
	}

	wordEnd := start

	for isBareKeyByte(at(parser.source, wordEnd)) {
		wordEnd++
	}

	if wordEnd > start {
		word := parser.source[start:wordEnd]

		// A key where a value should be, as when the value of an entry is left out and the next line has a key, or as in `[a: 1]`, is reported as a key, as in the JS reference. A word after the `:` and the space of its member, as in `msg: Error: file not found` or `url: https://example.com`, is that member's value instead, an unquoted string. Without the space, as in `a:b: 1`, the first `:` was most likely meant as part of the key.
		valueStart := len(strings.TrimRight(parser.source[:start], " \t"))
		isAfterColon := valueStart < start && at(parser.source, valueStart-1) == ':'

		if !isAfterColon && at(parser.source, wordEnd) == ':' {
			return fail(start, "expected a value, but found the key %s", abbreviate(word, 40))
		}

		if diagnosis := parser.diagnoseTableHeader(true); diagnosis != nil {
			return diagnosis
		}

		return fail(start, "%s", describeUnknownWord(word, unquotedText(parser.source, start)))
	}

	hint := parser.slashCommentHint()

	if parser.isBlockScalarIndicator() {
		hint = ". Write a multiline string as a block string, between ''' lines"
	}

	return fail(start, "expected a value, but found %s%s", parser.describeHere(), hint)
}

// tableHeader matches a line that is a TOML table header, as in `[server]` or `[[servers]]`, whose name is a valid key, so that the suggestion is valid.
var tableHeader = regexp.MustCompile(`^[\t ]*\[\[?([A-Z_a-z][\w-]*)\]\]?[\t ]*$`)

// diagnoseTableHeader reports a TOML table header, as in `[server]` or `[[servers]]` on a line of its own, which reads as an array that holds a word, or as a key that starts with “[”. Where a value is expected, isValue, it is only a table header when its bracket opens the document, because a table cannot be written as an item of an array inside it.
func (parser *parser) diagnoseTableHeader(isValue bool) *syntaxError {
	source := parser.source
	lineStart := strings.LastIndexByte(source[:parser.index], '\n') + 1

	if isValue && skipSpaces(source, lineStart) != parser.documentStart {
		return nil
	}

	match := tableHeader.FindStringSubmatch(source[lineStart:min(lineEnd(source, parser.index), lineStart+maxDiagnosedLength)])

	if match == nil {
		return nil
	}

	return fail(parser.index, "there are no table headers. Write the table as an object, as in %s: {…}", abbreviate(match[1], 40))
}

// isBlockScalarIndicator reports whether a YAML literal block scalar indicator, as in `key: |` or `key: |-`, is here, at the end of its line.
func (parser *parser) isBlockScalarIndicator() bool {
	// A folded block scalar, `>`, joins its lines, which a block string does not, so only `|` gets the hint.
	if parser.peek() != '|' {
		return false
	}

	end := parser.index + 1

	if chomping := at(parser.source, end); chomping == '-' || chomping == '+' {
		end++
	}

	end = skipSpaces(parser.source, end)
	return end >= len(parser.source) || parser.source[end] == '\n'
}

// slashCommentHint is the hint for a `//` comment, as in JavaScript.
func (parser *parser) slashCommentHint() string {
	if parser.peek() == '/' && at(parser.source, parser.index+1) == '/' {
		return ". A comment starts with “#”"
	}

	return ""
}

// unquotedText returns the text at start that was most likely meant as one unquoted string, such as `John Smith`: up to the end of the line, a comma, a closing bracket, or a comment after a space or a tab, without the spaces and tabs at its end.
func unquotedText(source string, start int) string {
	end := start
	limit := min(len(source), start+maxDiagnosedLength)

	for ; end < limit; end++ {
		character := source[end]
		// The text starts with a word or a `.`, so a comment is never at start, and `end-1` is never before it.
		isComment := character == '#' || character == '/' && at(source, end+1) == '*'

		if character == '\n' || character == ',' || character == ']' || character == '}' || isComment && (source[end-1] == ' ' || source[end-1] == '\t') {
			break
		}
	}

	return strings.TrimRight(source[start:end], " \t")
}

func (parser *parser) describeHere() string {
	if parser.isAtEnd() {
		return "the end of the document"
	}

	if parser.peek() == '\n' {
		return "a line break"
	}

	character, _ := utf8.DecodeRuneInString(parser.source[parser.index:])
	return describeCharacter(character)
}
