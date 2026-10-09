package soml

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

/*
Error is the error that every function of this package returns: for a document that is not valid SOML, for a document that does not fit the Go value it is decoded into, and for a Go value that SOML cannot represent.

When the error is about a place in a document, Line and Column give it. They are 0 otherwise, such as for an error from Marshal.

	var config Config
	err := soml.Unmarshal(data, &config)

	if somlError, ok := errors.AsType[*soml.Error](err); ok {
		fmt.Println(somlError.Line, somlError.Column, somlError.Message)
	}
*/
type Error struct {
	// Message says what is wrong, without the position.
	Message string

	// Line is the 1-based line of the error, or 0 when the error has no position.
	Line int

	// Column is the 1-based column of the error, counted in Unicode scalar values, or 0 when the error has no position.
	Column int

	// Offset is the 0-based byte offset of the error in the input. It is only meaningful when Line is not 0.
	Offset int

	// Path is the path to the value or key the error is about, as keys (strings) and array positions (ints), the same as a path for Document.Get, so ["servers", 1, "port"] is servers[1].port. It is nil for an error in the syntax of a document, which has a position, and for an error about the whole value. The keys are as the document or the Go value has them, so they can hold control characters, which Message and Error replace.
	Path []any

	// Err is the cause, such as an error from an UnmarshalText or UnmarshalSOML method, or nil.
	Err error
}

// Error returns the message with its position, as in `soml: line 2, column 1: duplicate key a`.
func (e *Error) Error() string {
	text := fmt.Sprintf("soml: line %d, column %d: %s", e.Line, e.Column, e.Message)

	if e.Line == 0 {
		// Without a position, the path says where the problem is.
		text = "soml: " + e.Message

		if len(e.Path) > 0 {
			text = "soml: " + shortPath(e.Path) + ": " + e.Message
		}
	}

	// A message from Marshal can hold text from the Go value, such as a key.
	return sanitize(text)
}

// Unwrap returns the cause, Err.
func (e *Error) Unwrap() error {
	return e.Err
}

// newError makes an error without a position. The message can hold text from a Go value, such as a key, so its unsafe characters are replaced, as errorAt does.
func newError(format string, arguments ...any) *Error {
	return &Error{Message: sanitize(fmt.Sprintf(format, arguments...))}
}

// errorAt makes an error at a byte offset in source.
func errorAt(source string, offset int, message string) *Error {
	line, column := position(source, offset)
	return &Error{Message: sanitize(message), Line: line, Column: column, Offset: offset}
}

// formatPath writes a path for a message, such as `servers[1].port` or `"the name".port`, with each key shortened, and a key that is not bare written as JSON writes it, as in the JS reference.
func formatPath(path []any) string {
	var text []byte

	for position, element := range path {
		switch element := element.(type) {
		case int:
			text = append(text, '[')
			text = strconv.AppendInt(text, int64(element), 10)
			text = append(text, ']')
		case string:
			if position > 0 {
				text = append(text, '.')
			}

			// Whether the key needs quotes depends on the whole key, because the “…” of a shortened key is not a bare key character.
			if short := abbreviate(element, 40); isBareKey(element) {
				text = append(text, short...)
			} else {
				text = appendJSONString(text, short)
			}
		}
	}

	return string(text)
}

// appendJSONString appends text in "…" as JSON.stringify writes it: a quote, a backslash, and a control character below U+0020 are escaped, and every other character is as it is.
// Note: Not using strconv.AppendQuote or encoding/json because their Go escapes or HTML and Unicode separator escapes differ from JSON.stringify.
func appendJSONString(buffer []byte, text string) []byte {
	buffer = append(buffer, '"')

	for _, character := range text {
		switch character {
		case '"', '\\':
			buffer = append(buffer, '\\', byte(character))
		case '\b':
			buffer = append(buffer, `\b`...)
		case '\f':
			buffer = append(buffer, `\f`...)
		case '\n':
			buffer = append(buffer, `\n`...)
		case '\r':
			buffer = append(buffer, `\r`...)
		case '\t':
			buffer = append(buffer, `\t`...)
		default:
			if character < 0x20 {
				// Lowercase hex, as JSON.stringify writes it.
				buffer = fmt.Appendf(buffer, `\u%04x`, character)
			} else {
				buffer = utf8.AppendRune(buffer, character)
			}
		}
	}

	return append(buffer, '"')
}

// shortPath writes a path for the text of an error, shortened in the middle when it is very long, such as the path of a cycle.
func shortPath(path []any) string {
	if len(path) <= 20 {
		return formatPath(path)
	}

	tail := path[len(path)-5:]
	// formatPath writes no `.` before the first key, so a tail that starts with a key needs one after the `…`.
	separator := ""

	if _, isKey := tail[0].(string); isKey {
		separator = "."
	}

	return formatPath(path[:10]) + ".…" + separator + formatPath(tail)
}

// prependPath puts a step, a key or an array position, at the start of the path of an error, on the way out of the value that the step leads to.
func prependPath(err error, step any) error {
	if somlError, ok := err.(*Error); ok {
		somlError.Path = append([]any{step}, somlError.Path...)
	}

	return err
}

// position is the 1-based line and column of a byte offset in source. The column counts Unicode scalar values.
func position(source string, offset int) (line, column int) {
	offset = max(0, min(offset, len(source)))
	before := source[:offset]
	lineStart := strings.LastIndexByte(before, '\n') + 1
	return strings.Count(before, "\n") + 1, utf8.RuneCountInString(before[lineStart:]) + 1
}

// abbreviate shortens text to at most maximumLength Unicode scalar values, with a “…” when it is cut.
func abbreviate(text string, maximumLength int) string {
	count := 0

	for index := range text {
		if count == maximumLength {
			return text[:index] + "…"
		}

		count++
	}

	return text
}

// sanitize replaces the characters that could change how a message prints in a terminal, or look like something else, with U+FFFD, as the JS reference does: control characters other than tab, bidirectional controls, and invalid UTF-8.
func sanitize(text string) string {
	// Note: Not using strings.Map alone because it replaces each invalid byte instead of each run of invalid bytes.
	return strings.Map(func(character rune) rune {
		if character != '\t' && unicode.IsControl(character) || unicode.Is(unicode.Bidi_Control, character) {
			return utf8.RuneError
		}

		return character
	}, strings.ToValidUTF8(text, "\uFFFD"))
}

// quote writes text from a document for an error message, in “…” and shortened. Unsafe characters are escaped later, for the whole message.
func quote(text string) string {
	return "“" + abbreviate(text, 40) + "”"
}
