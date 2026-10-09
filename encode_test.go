package soml

// Tests for Marshal's type mapping, canonical layout, and round trips.

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

// marshalWithin runs Marshal and fails the test when Marshal does not return within a few seconds, so that an endless loop fails one test instead of hanging the run.
func marshalWithin(t *testing.T, value any, options ...Option) ([]byte, error) {
	t.Helper()

	type result struct {
		data []byte
		err  error
	}

	done := make(chan result, 1)

	go func() {
		data, err := Marshal(value, options...)
		done <- result{data, err}
	}()

	select {
	case result := <-done:
		return result.data, result.err
	case <-time.After(5 * time.Second):
		t.Fatal("Marshal did not return within 5 seconds")
		return nil, nil
	}
}

// checkMarshal checks that Marshal writes exactly the expected text.
func checkMarshal(t *testing.T, value any, expected string, options ...Option) {
	t.Helper()
	data, err := marshalWithin(t, value, options...)

	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	if string(data) != expected {
		t.Fatalf("Marshal:\n got %q\nwant %q", data, expected)
	}
}

// checkMarshalError checks that Marshal fails with the message and no position.
func checkMarshalError(t *testing.T, value any, message string) {
	t.Helper()
	data, err := marshalWithin(t, value)

	if err == nil {
		t.Fatalf("Marshal wrote %q, want the error %q", data, message)
	}

	checkError(t, err, 0, 0, message)

	if data != nil {
		t.Errorf("Marshal returned %q with an error", data)
	}
}

type encodeServer struct {
	Port int    `soml:"port"`
	Host string `soml:"host"`
}

type encodeConfig struct {
	Name     string         `soml:"name"`
	Version  string         // The key is the field name as it is.
	Server   encodeServer   `soml:"server"`
	Tags     []string       `soml:"tags"`
	Empty    []int          `soml:"empty"`
	Nothing  map[string]int `soml:"nothing"`
	Owner    *string        `soml:"owner"`
	Ratio    float64        `soml:"ratio"`
	Enabled  bool           `soml:"enabled"`
	Started  time.Time      `soml:"started"`
	Timeout  time.Duration  `soml:"timeout"`
	Matrix   [][]int        `soml:"matrix"`
	Members  map[string]any `soml:"members"`
	Internal string         `soml:"-"`
	internal string
}

func TestMarshalCanonical(t *testing.T) {
	config := encodeConfig{
		Name:     "soml",
		Version:  "1.0.0",
		Server:   encodeServer{Port: 8080, Host: "localhost"},
		Tags:     []string{"a", "it's"},
		Ratio:    0.5,
		Enabled:  true,
		Started:  time.Date(2026, 9, 19, 16, 0, 0, 0, time.FixedZone("", 2*60*60)),
		Timeout:  90 * time.Minute,
		Matrix:   [][]int{{1, 0}, {}},
		Members:  map[string]any{"z": nil, "a b": map[string]any{"c": []int{1}}},
		Internal: "not written",
		internal: "not written",
	}

	expected := `Version: '1.0.0'
empty: []
enabled: true
matrix: [
	[
		1
		0
	]
	[]
]
members: {
	'a b': {
		c: [
			1
		]
	}
	z: null
}
name: 'soml'
nothing: {}
owner: null
ratio: 0.5
server: {
	host: 'localhost'
	port: 8080
}
started: 2026-09-19T14:00:00Z
tags: [
	'a'
	"it's"
]
timeout: 1h30m
`

	checkMarshal(t, config, expected, Canonical())

	t.Run("a pointer gives the same text", func(t *testing.T) {
		checkMarshal(t, &config, expected, Canonical())
	})

	t.Run("the canonical text reads back to the same value", func(t *testing.T) {
		var decoded encodeConfig

		if err := Unmarshal([]byte(expected), &decoded); err != nil {
			t.Fatal(err)
		}

		checkMarshal(t, decoded, expected, Canonical())
	})

	t.Run("a top-level array", func(t *testing.T) {
		checkMarshal(t, []any{1, "two", []int{}, map[string]int{}, map[string]any{"a": []bool{true}}}, `[
	1
	'two'
	[]
	{}
	{
		a: [
			true
		]
	}
]
`)
	})

	t.Run("empty top-level collections", func(t *testing.T) {
		checkMarshal(t, map[string]int{}, "{}\n")
		checkMarshal(t, map[string]int(nil), "{}\n")
		checkMarshal(t, struct{}{}, "{}\n")
		checkMarshal(t, &struct{}{}, "{}\n")
		checkMarshal(t, []int{}, "[]\n")
		checkMarshal(t, []int(nil), "[]\n")
		checkMarshal(t, [0]int{}, "[]\n")
	})

	t.Run("keys are sorted by Unicode scalar values", func(t *testing.T) {
		checkMarshal(t, map[string]int{"b": 1, "a": 2, "B": 3, "é": 4, "z": 5, "\U0001F600": 6, "\uFFFD": 7, "aa": 8, "": 9, "1": 10}, "'': 9\n1: 10\nB: 3\na: 2\naa: 8\nb: 1\nz: 5\n'é': 4\n'\uFFFD': 7\n'\U0001F600': 6\n")
	})

	t.Run("one member", func(t *testing.T) {
		checkMarshal(t, map[string]string{"a": "b"}, "a: 'b'\n")
	})
}

type orderedPackage struct {
	Name        string            `soml:"name"`
	Version     string            `soml:"version"`
	Description string            `soml:"description"`
	Author      orderedAuthor     `soml:"author"`
	Scripts     map[string]string `soml:"scripts"`
	Files       []orderedAuthor   `soml:"files"`
}

type orderedAuthor struct {
	Name  string `soml:"name"`
	Email string `soml:"email"`
}

func TestMarshalOrder(t *testing.T) {
	value := orderedPackage{
		Name:        "soml",
		Version:     "1.0.0",
		Description: "A config format",
		Author:      orderedAuthor{Name: "Sindre", Email: "s@example.com"},
		Scripts:     map[string]string{"test": "go test", "build": "go build"},
		Files:       []orderedAuthor{{Name: "a", Email: "b"}},
	}

	t.Run("struct fields keep their declared order, also nested", func(t *testing.T) {
		checkMarshal(t, value, `name: 'soml'
version: '1.0.0'
description: 'A config format'
author: {
	name: 'Sindre'
	email: 's@example.com'
}
scripts: {
	build: 'go build'
	test: 'go test'
}
files: [
	{
		name: 'a'
		email: 'b'
	}
]
`)
	})

	t.Run("map keys are sorted", func(t *testing.T) {
		checkMarshal(t, map[string]any{"b": 1, "a": orderedAuthor{Name: "n", Email: "e"}, "c": map[string]int{"y": 1, "x": 2}}, "a: {\n\tname: 'n'\n\temail: 'e'\n}\nb: 1\nc: {\n\tx: 2\n\ty: 1\n}\n")
	})

	t.Run("embedded struct fields are where the struct is embedded, as in encoding/json", func(t *testing.T) {
		embedded := struct {
			Name string `soml:"name"`
			EmbeddedBase
			Extra string `soml:"extra"`
		}{"n", EmbeddedBase{ID: 1, Kind: "k"}, "e"}

		checkMarshal(t, embedded, "name: 'n'\nid: 1\nKind: 'k'\nextra: 'e'\n")
		checkMarshal(t, embedded, "Kind: 'k'\nextra: 'e'\nid: 1\nname: 'n'\n", Canonical())
	})

	t.Run("Canonical sorts struct fields too, also nested", func(t *testing.T) {
		checkMarshal(t, value, `author: {
	email: 's@example.com'
	name: 'Sindre'
}
description: 'A config format'
files: [
	{
		email: 'b'
		name: 'a'
	}
]
name: 'soml'
scripts: {
	build: 'go build'
	test: 'go test'
}
version: '1.0.0'
`, Canonical())
	})

	t.Run("Canonical gives the same text as the value read back into an any", func(t *testing.T) {
		data, err := Marshal(value)

		if err != nil {
			t.Fatal(err)
		}

		var decoded any

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		canonical, err := Marshal(value, Canonical())

		if err != nil {
			t.Fatal(err)
		}

		checkMarshal(t, decoded, string(canonical))
		checkMarshal(t, decoded, string(canonical), Canonical())
	})

	t.Run("a zero Option does nothing", func(t *testing.T) {
		checkMarshal(t, orderedAuthor{Name: "n", Email: "e"}, "name: 'n'\nemail: 'e'\n", Option{})
	})
}

func TestMarshalIntegers(t *testing.T) {
	cases := []struct {
		value    any
		expected string
	}{
		{0, "0"},
		{-42, "-42"},
		{int8(-128), "-128"},
		{int16(32767), "32767"},
		{int32(math.MinInt32), "-2147483648"},
		{int64(math.MinInt64), "-9223372036854775808"},
		{int64(math.MaxInt64), "9223372036854775807"},
		{uint8(255), "255"},
		{uint16(65535), "65535"},
		{uint32(math.MaxUint32), "4294967295"},
		{uint64(math.MaxInt64), "9223372036854775807"},
		{uint(7), "7"},
		{uintptr(8), "8"},
		{port(8080), "8080"},
		{true, "true"},
		{false, "false"},
		{flag(true), "true"},
	}

	for _, testCase := range cases {
		t.Run(fmt.Sprintf("%T %v", testCase.value, testCase.value), func(t *testing.T) {
			if text := encodeMember(t, testCase.value); text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}
		})
	}

	t.Run("a uint64 above the int64 range", func(t *testing.T) {
		checkMarshalError(t, map[string]uint64{"a": math.MaxInt64 + 1}, "the integer 9223372036854775808 is outside the 64-bit signed range of a SOML int")
		checkMarshalError(t, []uint{math.MaxUint64}, "the integer 18446744073709551615 is outside the 64-bit signed range of a SOML int")
	})
}

func TestMarshalStrings(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected string
	}{
		{"plain", "hello", `'hello'`},
		{"empty", "", `''`},
		{"backslashes", `C:\Users\name`, `'C:\Users\name'`},
		{"one backslash", `\`, `'\'`},
		{"double quotes", `say "hi"`, `'say "hi"'`},
		{"an escape sequence as text", `\n\u{41}`, `'\n\u{41}'`},
		{"a single quote", "it's", `"it's"`},
		{"only a single quote", "'", `"'"`},
		{"a tab", "a\tb", `"a\tb"`},
		{"a line feed", "a\nb", `"a\nb"`},
		{"NUL", "\x00", `"\u{0}"`},
		{"U+0001", "\x01", `"\u{1}"`},
		{"escape", "\x1b[31m", `"\u{1b}[31m"`},
		{"U+001F", "\x1f", `"\u{1f}"`},
		{"DEL", "\x7f", `"\u{7f}"`},
		{"form feed", "\f", `"\u{c}"`},
		{"everything that is escaped", "it's \"x\" \\ \t\n\x00", `"it's \"x\" \\ \t\n\u{0}"`},
		{"a backslash in the escaped form", "'\\", `"'\\"`},
		{"line separators are literal", "\u2028\u2029", "'\u2028\u2029'"},
		{"non-ASCII", "é😀", "'é😀'"},
		{"a noncharacter", "\uFFFF\uFDD0", "'\uFFFF\uFDD0'"},
		{"invisible characters", "\u200B\uFEFF", "'\u200B\uFEFF'"},
		{"a C1 control is literal", "\u0085\u009b", "'\u0085\u009b'"},
		{"a bidirectional control is literal", "\u202E", "'\u202E'"},
		{"a comment marker", "# not a comment", `'# not a comment'`},
		{"looks like a number", "123", `'123'`},
		{"looks like a keyword", "true", `'true'`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			text := encodeMember(t, testCase.value)

			if text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}

			value, err := decodeMember(text)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.value) {
				t.Fatalf("read back as %q", value)
			}
		})
	}

	t.Run("a string kind", func(t *testing.T) {
		if text := encodeMember(t, byteName("it's")); text != `"it's"` {
			t.Fatalf("got %s", text)
		}
	})

	errorCases := []struct {
		name    string
		value   any
		message string
	}{
		{"invalid UTF-8", "a\xffb", "a string must be valid UTF-8, but “a\uFFFDb” is not"},
		{"a lone surrogate in UTF-8", "\xed\xa0\x80", "a string must be valid UTF-8, but “\uFFFD” is not"},
		{"a carriage return", "a\rb", "a string cannot contain a carriage return (U+000D), because SOML cannot represent one"},
		{"a carriage return in a string kind", byteName("\r\n"), "a string cannot contain a carriage return (U+000D), because SOML cannot represent one"},
		{"a carriage return in a slice", []string{"ok", "\r"}, "a string cannot contain a carriage return (U+000D), because SOML cannot represent one"},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, map[string]any{"value": testCase.value}, testCase.message)
		})
	}
}

func TestMarshalKeys(t *testing.T) {
	cases := []struct {
		key      string
		expected string
	}{
		{"name", "name"},
		{"content-type", "content-type"},
		{"_private", "_private"},
		{"404", "404"},
		{"-foo", "-foo"},
		{"-", "-"},
		{"7zip-bin", "7zip-bin"},
		{"2024-01-01", "2024-01-01"},
		{"true", "true"},
		{"null", "null"},
		{"infinity", "infinity"},
		{"ABC_def-123", "ABC_def-123"},
		{"the name", "'the name'"},
		{"a.b", "'a.b'"},
		{"3.14", "'3.14'"},
		{"", "''"},
		{"a:b", "'a:b'"},
		{"$schema", "'$schema'"},
		{"é", "'é'"},
		{`a\b`, `'a\b'`},
		{`a"b`, `'a"b'`},
		{"it's", `"it's"`},
		{"tab\t", `"tab\t"`},
		{"line\nfeed", `"line\nfeed"`},
		{"\x00", `"\u{0}"`},
		{"\u200B", "'\u200B'"},
	}

	for _, testCase := range cases {
		t.Run(testCase.key, func(t *testing.T) {
			data, err := Marshal(map[string]int{testCase.key: 1})

			if err != nil {
				t.Fatal(err)
			}

			if expected := testCase.expected + ": 1\n"; string(data) != expected {
				t.Fatalf("got %q, want %q", data, expected)
			}

			var decoded map[string]int

			if err := Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(decoded, map[string]int{testCase.key: 1}) {
				t.Fatalf("read back as %#v", decoded)
			}
		})
	}

	t.Run("struct field keys", func(t *testing.T) {
		value := struct {
			Spaced int `soml:"the key"`
			Dot    int `soml:"a.b"`
			Quote  int `soml:"it's"`
			Dash   int `soml:"-,"`
		}{1, 2, 3, 4}

		checkMarshal(t, value, "'the key': 1\n'a.b': 2\n\"it's\": 3\n-: 4\n")
	})

	t.Run("a nested key", func(t *testing.T) {
		checkMarshal(t, map[string]any{"outer": map[string]int{"the key": 1}}, "outer: {\n\t'the key': 1\n}\n")
	})

	errorCases := []struct {
		name    string
		value   any
		message string
	}{
		{"invalid UTF-8", map[string]int{"a\xff": 1}, "a key must be valid UTF-8, but “a\uFFFD” is not"},
		{"a carriage return", map[string]int{"a\rb": 1}, "a key cannot contain a carriage return (U+000D), because SOML cannot represent one"},
		{"a nested carriage return", map[string]any{"ok": map[string]int{"\r": 1}}, "a key cannot contain a carriage return (U+000D), because SOML cannot represent one"},
		{"in an array", []map[string]int{{"\r": 1}}, "a key cannot contain a carriage return (U+000D), because SOML cannot represent one"},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, testCase.value, testCase.message)
		})
	}
}

// pointKey is a map key that writes itself as text.
type pointKey struct {
	X, Y int
}

func (key pointKey) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "%d,%d", key.X, key.Y), nil
}

// lowerKey is a string kind that writes itself in lowercase, so two keys can give one text.
type lowerKey string

func (key lowerKey) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(key))), nil
}

// rawTextKey writes its text as it is, also when that is not UTF-8.
type rawTextKey string

func (key rawTextKey) MarshalText() ([]byte, error) {
	return []byte(key), nil
}

// pointerTextKey has MarshalText on its pointer, which a map key cannot be addressed for.
type pointerTextKey string

func (key *pointerTextKey) MarshalText() ([]byte, error) {
	return []byte("key " + string(*key)), nil
}

// pointerIntegerKey is an integer kind with MarshalText on its pointer.
type pointerIntegerKey int

func (key *pointerIntegerKey) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "key %d", *key), nil
}

type failingTextKey int

var errMarshalText = errors.New("this text cannot be written")

func (failingTextKey) MarshalText() ([]byte, error) {
	return nil, errMarshalText
}

func TestMarshalMapKeys(t *testing.T) {
	checkMarshal(t, map[int]string{404: "a", -1: "b", 10: "c", 0: "d"}, "-1: 'b'\n0: 'd'\n10: 'c'\n404: 'a'\n")
	checkMarshal(t, map[uint8]bool{255: true, 7: false}, "255: true\n7: false\n")
	checkMarshal(t, map[int64]int{math.MinInt64: 1}, "-9223372036854775808: 1\n")
	checkMarshal(t, map[uint64]int{math.MaxUint64: 1}, "18446744073709551615: 1\n")
	checkMarshal(t, map[byteName]int{"b": 1, "a": 2}, "a: 2\nb: 1\n")
	checkMarshal(t, map[pointKey]int{{1, 2}: 3, {0, 0}: 4}, "'0,0': 4\n'1,2': 3\n")
	checkMarshal(t, map[*pointKey]int{{5, 6}: 1}, "'5,6': 1\n")
	checkMarshal(t, map[lowerKey]int{"ABC": 1}, "abc: 1\n")
	checkMarshal(t, map[pointerIntegerKey]int{1: 1}, "'key 1': 1\n")
	checkMarshal(t, map[netip.Addr]int{netip.MustParseAddr("::1"): 1}, "'::1': 1\n")

	// MarshalText on the pointer is used for an integer kind above, and for a value of a string kind, so a key of a string kind should use it too.
	t.Run("a string kind with MarshalText on its pointer", func(t *testing.T) {
		checkMarshal(t, map[pointerTextKey]int{"a": 1}, "'key a': 1\n")
	})

	t.Run("integer keys read back", func(t *testing.T) {
		original := map[int]string{404: "a", -1: "b", 0: "c", math.MaxInt: "d", math.MinInt: "e"}
		data, err := Marshal(original)

		if err != nil {
			t.Fatal(err)
		}

		var decoded map[int]string

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(decoded, original) {
			t.Fatalf("got %#v", decoded)
		}
	})

	errorCases := []struct {
		name    string
		value   any
		message string
	}{
		{"two keys with one text", map[lowerKey]int{"A": 1, "a": 2}, "two map keys give the same key a"},
		{"two keys with one text that needs quotes", map[lowerKey]int{"X Y": 1, "x y": 2}, "two map keys give the same key \"x y\""},
		{"float keys", map[float64]int{1.5: 1}, "a map key of type float64 cannot be written as SOML; a key must be a string, an integer, or have a MarshalText method"},
		{"bool keys", map[bool]int{true: 1}, "a map key of type bool cannot be written as SOML; a key must be a string, an integer, or have a MarshalText method"},
		{"struct keys", map[encodeServer]int{{}: 1}, "a map key of type soml.encodeServer cannot be written as SOML; a key must be a string, an integer, or have a MarshalText method"},
		{"a nil pointer key", map[*pointKey]int{nil: 1}, "a map key of type *soml.pointKey is nil"},
		{"a MarshalText error", map[failingTextKey]int{1: 1}, "MarshalText of soml.failingTextKey: this text cannot be written"},
		{"a key text that is not UTF-8", map[rawTextKey]int{"\xff": 1}, "a key must be valid UTF-8, but “\uFFFD” is not"},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, testCase.value, testCase.message)
		})
	}

	t.Run("a MarshalText error is wrapped", func(t *testing.T) {
		_, err := Marshal(map[failingTextKey]int{1: 1})

		if !errors.Is(err, errMarshalText) {
			t.Fatalf("errors.Is does not find the cause: %v", err)
		}
	})
}

type zeroWhenEven int

func (value zeroWhenEven) IsZero() bool {
	return value%2 == 0
}

// pointerZero has IsZero on its pointer.
type pointerZero struct {
	Value int
}

func (value *pointerZero) IsZero() bool {
	return value.Value < 0
}

type omitZeroValues struct {
	Int       int            `soml:"int,omitzero"`
	String    string         `soml:"string,omitzero"`
	Pointer   *int           `soml:"pointer,omitzero"`
	Slice     []int          `soml:"slice,omitzero"`
	Map       map[string]int `soml:"map,omitzero"`
	Time      time.Time      `soml:"time,omitzero"`
	TimeAt    *time.Time     `soml:"timeAt,omitzero"`
	Struct    encodeServer   `soml:"struct,omitzero"`
	Custom    zeroWhenEven   `soml:"custom,omitzero"`
	Interface any            `soml:"interface,omitzero"`
	Float     float64        `soml:"float,omitzero"`
	Bool      bool           `soml:"bool,omitzero"`
	Duration  time.Duration  `soml:"duration,omitzero"`
	Array     [2]int         `soml:"array,omitzero"`
}

type omitZeroWithoutKey struct {
	Count int `soml:",omitzero"`
	Name  string
}

type omitPointerZero struct {
	Value pointerZero `soml:"value,omitzero"`
}

func TestMarshalOmitZero(t *testing.T) {
	checkMarshal(t, omitZeroValues{}, "{}\n")
	checkMarshal(t, omitZeroValues{Custom: 2}, "{}\n")

	number := 0
	zeroTime := time.Time{}

	checkMarshal(t, omitZeroValues{
		Int:       1,
		String:    "s",
		Pointer:   &number,
		Slice:     []int{},
		Map:       map[string]int{},
		Time:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		TimeAt:    &zeroTime,
		Struct:    encodeServer{Port: 1},
		Custom:    3,
		Interface: 0,
		Float:     0.5,
		Bool:      true,
		Duration:  time.Second,
		Array:     [2]int{0, 1},
	}, `int: 1
string: 's'
pointer: 0
slice: []
map: {}
time: 2026-01-01T00:00:00Z
struct: {
	port: 1
	host: ''
}
custom: 3
interface: 0
float: 0.5
bool: true
duration: 1s
array: [
	0
	1
]
`)

	t.Run("a tag with options and no key", func(t *testing.T) {
		checkMarshal(t, omitZeroWithoutKey{Name: "n"}, "Name: 'n'\n")
		checkMarshal(t, omitZeroWithoutKey{Count: 1}, "Count: 1\nName: ''\n")
	})

	t.Run("an IsZero method on the pointer", func(t *testing.T) {
		checkMarshal(t, omitPointerZero{Value: pointerZero{Value: -1}}, "{}\n")
		checkMarshal(t, omitPointerZero{Value: pointerZero{Value: 0}}, "value: {\n\tValue: 0\n}\n")
		checkMarshal(t, &omitPointerZero{Value: pointerZero{Value: -1}}, "{}\n")

		// A map value cannot be addressed.
		checkMarshal(t, map[string]omitPointerZero{"a": {Value: pointerZero{Value: -1}}}, "a: {}\n")
	})

	t.Run("an interface that holds a nil pointer", func(t *testing.T) {
		value := struct {
			Value fmt.Stringer `soml:"value,omitzero"`
		}{Value: (*stringer)(nil)}

		checkMarshal(t, value, "value: null\n")
	})

	t.Run("an interface with IsZero", func(t *testing.T) {
		type zeroer interface {
			IsZero() bool
		}

		value := struct {
			Nil     zeroer `soml:"nil,omitzero"`
			Zero    zeroer `soml:"zero,omitzero"`
			NotZero zeroer `soml:"notZero,omitzero"`
			NilTime zeroer `soml:"nilTime,omitzero"`
		}{Zero: zeroWhenEven(4), NotZero: zeroWhenEven(5), NilTime: (*time.Time)(nil)}

		checkMarshal(t, value, "notZero: 5\n")
	})
}

// emptyMarshaler writes itself as an empty string.
type emptyMarshaler struct{}

func (emptyMarshaler) MarshalSOML() (any, error) {
	return "", nil
}

type omitEmptyValues struct {
	Pointer   *int           `soml:"pointer,omitempty"`
	String    string         `soml:"string,omitempty"`
	Slice     []int          `soml:"slice,omitempty"`
	NilSlice  []int          `soml:"nilSlice,omitempty"`
	Map       map[string]int `soml:"map,omitempty"`
	NilMap    map[string]int `soml:"nilMap,omitempty"`
	Bytes     []byte         `soml:"bytes,omitempty"`
	Array     [0]int         `soml:"array,omitempty"`
	Interface any            `soml:"interface,omitempty"`
	Marshaler emptyMarshaler `soml:"marshaler,omitempty"`
	Struct    struct {
		Hidden int `soml:"hidden,omitzero"`
	} `soml:"struct,omitempty"`
	Zero  int     `soml:"zero,omitempty"`
	False bool    `soml:"false,omitempty"`
	Float float64 `soml:"float,omitempty"`
	Quote string  `soml:"quote,omitempty"`
}

func TestMarshalOmitEmpty(t *testing.T) {
	values := omitEmptyValues{Slice: []int{}, Map: map[string]int{}, Bytes: []byte{}, Quote: "'"}
	checkMarshal(t, values, "zero: 0\nfalse: false\nfloat: 0.0\nquote: \"'\"\n")

	t.Run("inside braces", func(t *testing.T) {
		checkMarshal(t, map[string]any{"inner": values}, "inner: {\n\tzero: 0\n\tfalse: false\n\tfloat: 0.0\n\tquote: \"'\"\n}\n")
	})

	t.Run("every member left out", func(t *testing.T) {
		type allEmpty struct {
			Pointer *int   `soml:"pointer,omitempty"`
			String  string `soml:"string,omitempty"`
		}

		checkMarshal(t, allEmpty{}, "{}\n")
		checkMarshal(t, map[string]any{"inner": allEmpty{}}, "inner: {}\n")
		checkMarshal(t, []any{allEmpty{}, allEmpty{String: "x"}}, "[\n\t{}\n\t{\n\t\tstring: 'x'\n\t}\n]\n")
	})

	t.Run("not empty", func(t *testing.T) {
		number := 0
		value := omitEmptyValues{Pointer: &number, String: "s", Slice: []int{0}, Map: map[string]int{"a": 0}, Interface: false}
		checkMarshal(t, value, "pointer: 0\nstring: 's'\nslice: [\n\t0\n]\nmap: {\n\ta: 0\n}\ninterface: false\nzero: 0\nfalse: false\nfloat: 0.0\n")
	})
}

type ignoredFields struct {
	Kept     string `soml:"kept"`
	Ignored  string `soml:"-"`
	Dash     string `soml:"-,"`
	private  string
	Function func() `soml:"-"`
}

func TestMarshalStructFields(t *testing.T) {
	t.Run("ignored fields", func(t *testing.T) {
		checkMarshal(t, ignoredFields{Kept: "k", Ignored: "i", Dash: "d", private: "p", Function: func() {}}, "kept: 'k'\n-: 'd'\n")
	})

	t.Run("embedded structs", func(t *testing.T) {
		value := struct {
			EmbeddedBase
			Name string `soml:"name"`
		}{EmbeddedBase{ID: 1, Kind: "k"}, "n"}

		checkMarshal(t, value, "id: 1\nKind: 'k'\nname: 'n'\n")
	})

	t.Run("an embedded unexported struct", func(t *testing.T) {
		value := struct {
			embeddedHidden
		}{embeddedHidden{Secret: 7}}

		checkMarshal(t, value, "Secret: 7\n")
	})

	t.Run("an embedded nil pointer", func(t *testing.T) {
		value := struct {
			*EmbeddedBase
			Name string
		}{Name: "n"}

		checkMarshal(t, value, "Name: 'n'\n")

		value.EmbeddedBase = &EmbeddedBase{ID: 2}
		checkMarshal(t, value, "id: 2\nKind: ''\nName: 'n'\n")
	})

	t.Run("a shallower field wins", func(t *testing.T) {
		value := struct {
			EmbeddedBase
			Kind int
		}{EmbeddedBase{ID: 1, Kind: "hidden"}, 3}

		checkMarshal(t, value, "id: 1\nKind: 3\n")
	})

	t.Run("two fields at one depth are ambiguous and left out", func(t *testing.T) {
		value := struct {
			EmbeddedBase
			embeddedOther
		}{EmbeddedBase{ID: 1, Kind: "a"}, embeddedOther{Kind: "b", Extra: "e"}}

		checkMarshal(t, value, "id: 1\nExtra: 'e'\n")
	})

	t.Run("one struct embedded twice is ambiguous", func(t *testing.T) {
		value := sharedBoth{sharedLeft{sharedInner{1}, 2}, sharedRight{sharedInner{3}}}
		checkMarshal(t, value, "Left: 2\n")
	})

	t.Run("a tagged field wins at one depth", func(t *testing.T) {
		value := struct {
			EmbeddedBase
			embeddedTagged
		}{EmbeddedBase{Kind: "untagged"}, embeddedTagged{Kind: "tagged"}}

		checkMarshal(t, value, "id: 0\nKind: 'tagged'\n")
	})

	t.Run("two tagged fields with one key are left out", func(t *testing.T) {
		value := struct {
			First  int `soml:"same"`
			Second int `soml:"same"`
			Other  int `soml:"other"`
		}{1, 2, 3}

		checkMarshal(t, value, "other: 3\n")
	})

	t.Run("an embedded struct with a tag", func(t *testing.T) {
		value := struct {
			EmbeddedBase `soml:"base"`
		}{EmbeddedBase{ID: 1}}

		checkMarshal(t, value, "base: {\n\tid: 1\n\tKind: ''\n}\n")
	})

	t.Run("an embedded type that is not a struct", func(t *testing.T) {
		value := struct {
			Label
			byteName
		}{"l", "hidden"}

		checkMarshal(t, value, "Label: 'l'\n")
	})

	t.Run("nil values", func(t *testing.T) {
		value := struct {
			Map       map[string]int `soml:"map"`
			Slice     []string       `soml:"slice"`
			Pointer   *int           `soml:"pointer"`
			Interface any            `soml:"interface"`
			Bytes     []byte         `soml:"bytes"`
			Struct    *encodeServer  `soml:"struct"`
		}{}

		checkMarshal(t, value, "map: {}\nslice: []\npointer: null\ninterface: null\nbytes: ''\nstruct: null\n")
	})

	t.Run("tag errors", func(t *testing.T) {
		for _, testCase := range tagErrors {
			t.Run(testCase.name, func(t *testing.T) {
				checkMarshalError(t, testCase.value, testCase.message)
			})
		}
	})
}

func TestMarshalBytesAndTime(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		{"bytes", []byte("hello"), "'aGVsbG8='"},
		{"empty bytes", []byte{}, "''"},
		{"nil bytes", []byte(nil), "''"},
		{"a byte array", [4]byte{1, 2, 3, 4}, "'AQIDBA=='"},
		{"a named byte slice", blob("hello"), "'aGVsbG8='"},
		{"bytes with a slash and a plus", []byte{0xfb, 0xff}, "'+/8='"},
		{"time", time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC), "2026-09-19T14:00:00Z"},
		{"a pointer to a time", new(time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)), "2026-09-19T14:00:00Z"},
		{"duration", 90 * time.Minute, "1h30m"},
		{"a pointer to a duration", new(time.Second), "1s"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if text := encodeMember(t, testCase.value); text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}
		})
	}

	t.Run("bytes read back", func(t *testing.T) {
		value := struct {
			Bytes []byte  `soml:"bytes"`
			Array [3]byte `soml:"array"`
		}{[]byte{0, 1, 2, 0xff}, [3]byte{9, 8, 7}}

		data, err := Marshal(value)

		if err != nil {
			t.Fatal(err)
		}

		var decoded struct {
			Bytes []byte  `soml:"bytes"`
			Array [3]byte `soml:"array"`
		}

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(decoded, value) {
			t.Fatalf("got %#v", decoded)
		}
	})

	timeErrors := []struct {
		name    string
		value   time.Time
		message string
	}{
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), "the time 10000-01-01T00:00:00Z is outside the years 0001 to 9999 in UTC, which is the range of a SOML instant"},
		{"year 0", time.Date(0, 12, 31, 23, 59, 59, 0, time.UTC), "the time 0000-12-31T23:59:59Z is outside the years 0001 to 9999 in UTC, which is the range of a SOML instant"},
		{"year 10000 in UTC", time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -2*60*60)), "the time 10000-01-01T01:00:00Z is outside the years 0001 to 9999 in UTC, which is the range of a SOML instant"},
		{"year 0 in UTC", time.Date(1, 1, 1, 0, 30, 0, 0, time.FixedZone("", 60*60)), "the time 0000-12-31T23:30:00Z is outside the years 0001 to 9999 in UTC, which is the range of a SOML instant"},
	}

	for _, testCase := range timeErrors {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, map[string]any{"value": testCase.value}, testCase.message)
		})
	}
}

// marshalerValue writes the value it holds in its place.
type marshalerValue struct {
	value any
}

func (value marshalerValue) MarshalSOML() (any, error) {
	return value.value, nil
}

type failingMarshaler struct{}

var errMarshal = errors.New("this value cannot be written")

func (failingMarshaler) MarshalSOML() (any, error) {
	return nil, errMarshal
}

// loopMarshaler returns itself, forever.
type loopMarshaler struct{}

func (value loopMarshaler) MarshalSOML() (any, error) {
	return value, nil
}

// pointerLoopMarshaler returns itself as a pointer, forever.
type pointerLoopMarshaler struct{}

func (value *pointerLoopMarshaler) MarshalSOML() (any, error) {
	return value, nil
}

// nestingMarshaler returns an array that holds itself, forever.
type nestingMarshaler struct{}

func (value nestingMarshaler) MarshalSOML() (any, error) {
	return []any{value}, nil
}

// pointerMarshaler has MarshalSOML on its pointer.
type pointerMarshaler struct {
	Name string
}

func (value *pointerMarshaler) MarshalSOML() (any, error) {
	return "marshaled " + value.Name, nil
}

// marshalerAndText has every method, and MarshalSOML wins.
type marshalerAndText struct{}

func (marshalerAndText) MarshalSOML() (any, error) {
	return "MarshalSOML", nil
}

func (marshalerAndText) MarshalText() ([]byte, error) {
	return []byte("MarshalText"), nil
}

func (marshalerAndText) AppendText(buffer []byte) ([]byte, error) {
	return append(buffer, "AppendText"...), nil
}

func TestMarshalMarshaler(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		{"a string", marshalerValue{"text"}, "'text'"},
		{"an int", marshalerValue{int64(1)}, "1"},
		{"nil", marshalerValue{nil}, "null"},
		{"a map", marshalerValue{map[string]any{"b": 1, "a": []int{}}}, "{\n\ta: []\n\tb: 1\n}"},
		{"another Marshaler", marshalerValue{marshalerValue{2.5}}, "2.5"},
		{"a pointer", &marshalerValue{true}, "true"},
		{"a time", marshalerValue{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, "2026-01-01T00:00:00Z"},
		{"MarshalSOML wins", marshalerAndText{}, "'MarshalSOML'"},
		{"a pointer receiver", &pointerMarshaler{"p"}, "'marshaled p'"},
		{"a pointer receiver on a value that cannot be addressed", pointerMarshaler{"v"}, "'marshaled v'"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if text := encodeMember(t, testCase.value); text != testCase.expected {
				t.Fatalf("got %q, want %q", text, testCase.expected)
			}
		})
	}

	t.Run("pointer receivers on map values and struct fields", func(t *testing.T) {
		checkMarshal(t, map[string]pointerMarshaler{"a": {"x"}}, "a: 'marshaled x'\n")
		checkMarshal(t, struct{ Value pointerMarshaler }{pointerMarshaler{"y"}}, "Value: 'marshaled y'\n")
		checkMarshal(t, []pointerMarshaler{{"z"}}, "[\n\t'marshaled z'\n]\n")
	})

	t.Run("at the top level", func(t *testing.T) {
		checkMarshal(t, marshalerValue{map[string]int{"a": 1}}, "a: 1\n")
		checkMarshal(t, marshalerValue{[]int{1}}, "[\n\t1\n]\n")
		checkMarshalError(t, marshalerValue{"text"}, "a document must be an object or an array, but a value of type soml.marshalerValue is a scalar")
	})

	t.Run("an error is wrapped", func(t *testing.T) {
		_, err := Marshal(map[string]any{"value": failingMarshaler{}})
		checkError(t, err, 0, 0, "MarshalSOML of soml.failingMarshaler: this value cannot be written")

		if !errors.Is(err, errMarshal) {
			t.Errorf("errors.Is does not find the cause: %v", err)
		}
	})

	t.Run("a method that returns its own type is an error, not a hang", func(t *testing.T) {
		checkMarshalError(t, map[string]any{"value": loopMarshaler{}}, "a value of type soml.loopMarshaler goes through more than 100 pointers, interfaces, and MarshalSOML calls in a row, which is most likely a cycle")
		checkMarshalError(t, loopMarshaler{}, "a value of type soml.loopMarshaler goes through more than 100 pointers, interfaces, and MarshalSOML calls in a row, which is most likely a cycle")
		checkMarshalError(t, map[string]any{"value": &pointerLoopMarshaler{}}, "a value of type *soml.pointerLoopMarshaler goes through more than 100 pointers, interfaces, and MarshalSOML calls in a row, which is most likely a cycle")
	})

	t.Run("a method that nests itself is an error, not a stack overflow", func(t *testing.T) {
		checkMarshalError(t, map[string]any{"value": nestingMarshaler{}}, "the value is nested more than 100 levels deep, so no reader would accept the document")
	})
}

// textMarshaler writes itself as text.
type textMarshaler struct {
	text string
}

func (value textMarshaler) MarshalText() ([]byte, error) {
	return []byte(value.text), nil
}

// textAppender has both text methods, and AppendText wins.
type textAppender struct{}

func (textAppender) AppendText(buffer []byte) ([]byte, error) {
	return append(buffer, "appended"...), nil
}

func (textAppender) MarshalText() ([]byte, error) {
	return []byte("marshaled"), nil
}

// pointerText has MarshalText on its pointer.
type pointerText struct {
	text string
}

func (value *pointerText) MarshalText() ([]byte, error) {
	return []byte("pointer " + value.text), nil
}

type failingText struct{}

func (failingText) MarshalText() ([]byte, error) {
	return nil, errMarshalText
}

type failingAppender struct{}

func (failingAppender) AppendText([]byte) ([]byte, error) {
	return nil, errMarshalText
}

// textByteOut is a byte that writes itself as text, so that a slice of it is not base64.
type textByteOut uint8

func (value textByteOut) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "byte %d", value), nil
}

func TestMarshalText(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		{"MarshalText", textMarshaler{"text"}, "'text'"},
		{"MarshalText that needs escapes", textMarshaler{"it's\n"}, `"it's\n"`},
		{"AppendText wins", textAppender{}, "'appended'"},
		{"a pointer receiver", &pointerText{"a"}, "'pointer a'"},
		{"a pointer receiver on a value that cannot be addressed", pointerText{"b"}, "'pointer b'"},
		{"standard library", netip.MustParseAddr("127.0.0.1"), "'127.0.0.1'"},
		{"a slice", []textMarshaler{{"a"}, {"b"}}, "[\n\t'a'\n\t'b'\n]"},
		{"a slice of text bytes is not base64", []textByteOut{1, 2}, "[\n\t'byte 1'\n\t'byte 2'\n]"},
		{"a time is an instant, not text", time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC), "2026-09-19T14:00:00Z"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if text := encodeMember(t, testCase.value); text != testCase.expected {
				t.Fatalf("got %q, want %q", text, testCase.expected)
			}
		})
	}

	t.Run("a pointer receiver on a map value", func(t *testing.T) {
		checkMarshal(t, map[string]pointerText{"a": {"x"}}, "a: 'pointer x'\n")
	})

	errorCases := []struct {
		name    string
		value   any
		message string
	}{
		{"a MarshalText error", failingText{}, "MarshalText of soml.failingText: this text cannot be written"},
		{"an AppendText error", failingAppender{}, "AppendText of soml.failingAppender: this text cannot be written"},
		{"text that is not UTF-8", textMarshaler{"\xff"}, "a string must be valid UTF-8, but “\uFFFD” is not"},
		{"text with a carriage return", textMarshaler{"\r"}, "a string cannot contain a carriage return (U+000D), because SOML cannot represent one"},
	}

	for _, testCase := range errorCases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, map[string]any{"value": testCase.value}, testCase.message)
		})
	}

	_, err := Marshal(map[string]any{"value": failingText{}})

	if !errors.Is(err, errMarshalText) {
		t.Errorf("errors.Is does not find the cause: %v", err)
	}
}

// nestedArrays is an array nested depth levels deep, counting itself.
func nestedArrays(depth int) any {
	var value any = []any{}

	for range depth - 1 {
		value = []any{value}
	}

	return value
}

// nestedObjects is an object nested depth levels deep, counting itself.
func nestedObjects(depth int) any {
	var value any = map[string]any{}

	for range depth - 1 {
		value = map[string]any{"a": value}
	}

	return value
}

type omittedDeepNode struct {
	Level  int              `soml:"level,omitzero"`
	Next   *omittedDeepNode `soml:"next,omitempty"`
	Map    map[string]int   `soml:"map,omitempty"`
	Slice  []int            `soml:"slice,omitempty"`
	Struct *omittedDeepNode `soml:"struct,omitempty"`
}

type cycleNode struct {
	Next *cycleNode `soml:"next"`
}

func TestMarshalNesting(t *testing.T) {
	const tooDeep = "the value is nested more than 100 levels deep, so no reader would accept the document"

	for _, nested := range []struct {
		name  string
		build func(int) any
	}{
		{"arrays", nestedArrays},
		{"objects", nestedObjects},
	} {
		t.Run(nested.name, func(t *testing.T) {
			data, err := Marshal(nested.build(100))

			if err != nil {
				t.Fatalf("100 levels: %v", err)
			}

			var decoded any

			if err := Unmarshal(data, &decoded); err != nil {
				t.Fatalf("100 levels do not read back: %v", err)
			}

			if !reflect.DeepEqual(decoded, nested.build(100)) {
				t.Fatal("100 levels read back as another value")
			}

			checkMarshalError(t, nested.build(101), tooDeep)
		})
	}

	t.Run("the limit agrees with Unmarshal", func(t *testing.T) {
		var decoded any

		if err := Unmarshal([]byte(strings.Repeat("[", 100)+strings.Repeat("]", 100)), &decoded); err != nil {
			t.Fatalf("100 levels: %v", err)
		}

		err := Unmarshal([]byte(strings.Repeat("[", 101)+strings.Repeat("]", 101)), &decoded)
		checkError(t, err, 1, 101, "the document is nested more than 100 levels deep")
	})

	t.Run("a struct inside a map counts", func(t *testing.T) {
		checkMarshal(t, map[string]any{"a": encodeServer{Port: 1}}, "a: {\n\tport: 1\n\thost: ''\n}\n")

		var value any = encodeServer{}

		for range 99 {
			value = []any{value}
		}

		if _, err := Marshal(value); err != nil {
			t.Fatalf("100 levels: %v", err)
		}

		checkMarshalError(t, []any{value}, tooDeep)
	})

	// An empty object or array that omitempty leaves out is not written, so it adds no level, also one past the limit.
	t.Run("an omitted empty collection one level past the limit", func(t *testing.T) {
		for _, testCase := range []struct {
			name  string
			build func() *omittedDeepNode
		}{
			{"nil map and slice", func() *omittedDeepNode {
				return &omittedDeepNode{}
			}},
			{"empty map and slice", func() *omittedDeepNode {
				return &omittedDeepNode{Map: map[string]int{}, Slice: []int{}}
			}},
			{"struct with only omitted fields", func() *omittedDeepNode {
				return &omittedDeepNode{Struct: &omittedDeepNode{}}
			}},
			{"struct whose omitted fields are past the limit too", func() *omittedDeepNode {
				return &omittedDeepNode{Struct: &omittedDeepNode{Map: map[string]int{}, Struct: &omittedDeepNode{}}}
			}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				root := &omittedDeepNode{Level: 1}
				node := root

				for level := 2; level <= 100; level++ {
					node.Next = &omittedDeepNode{Level: level}
					node = node.Next
				}

				deepest := testCase.build()
				node.Map, node.Slice, node.Struct = deepest.Map, deepest.Slice, deepest.Struct
				data, err := Marshal(root)

				if err != nil {
					t.Fatalf("100 levels: %v", err)
				}

				var decoded any

				if err := Unmarshal(data, &decoded); err != nil {
					t.Fatalf("100 levels do not read back: %v", err)
				}

				var expected any = map[string]any{"level": int64(100)}

				for level := 99; level >= 1; level-- {
					expected = map[string]any{"level": int64(level), "next": expected}
				}

				if !reflect.DeepEqual(decoded, expected) {
					t.Fatalf("got:\n%s", data)
				}
			})
		}
	})

	t.Run("an empty collection one level past the limit that is not omitted", func(t *testing.T) {
		for _, last := range []any{map[string]any{}, []any{}, struct{}{}, &omittedDeepNode{}} {
			var value any = map[string]any{"a": last}

			for range 99 {
				value = map[string]any{"a": value}
			}

			checkMarshalError(t, value, tooDeep)
		}
	})

	t.Run("a non-empty collection past the limit with omitempty", func(t *testing.T) {
		root := &omittedDeepNode{Level: 1}
		node := root

		for level := 2; level <= 100; level++ {
			node.Next = &omittedDeepNode{Level: level}
			node = node.Next
		}

		node.Map = map[string]int{"a": 1}
		checkMarshalError(t, root, tooDeep)

		node.Map = nil
		node.Slice = []int{1}
		checkMarshalError(t, root, tooDeep)

		node.Slice = nil
		node.Struct = &omittedDeepNode{Slice: []int{1}}
		checkMarshalError(t, root, tooDeep)
	})

	t.Run("a cycle through an omitempty pointer", func(t *testing.T) {
		node := &omittedDeepNode{}
		node.Next = node
		checkMarshalError(t, node, tooDeep)

		node = &omittedDeepNode{}
		node.Struct = node
		checkMarshalError(t, node, tooDeep)
	})

	t.Run("a cycle through a struct pointer", func(t *testing.T) {
		node := &cycleNode{}
		node.Next = node
		checkMarshalError(t, node, tooDeep)
	})

	t.Run("a cycle through a map", func(t *testing.T) {
		members := map[string]any{}
		members["self"] = members
		checkMarshalError(t, members, tooDeep)
	})

	t.Run("a cycle through a slice", func(t *testing.T) {
		items := []any{nil}
		items[0] = items
		checkMarshalError(t, items, tooDeep)
	})

	t.Run("a cycle of pointers without a collection", func(t *testing.T) {
		var value any
		value = &value
		_, err := marshalWithin(t, map[string]any{"value": value})

		if err == nil {
			t.Fatal("a cycle of pointers was written")
		}
	})
}

func TestMarshalTopLevel(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		message string
	}{
		{"a string", "text", "a document must be an object or an array, but a value of type string is a scalar"},
		{"a bool", true, "a document must be an object or an array, but a value of type bool is a scalar"},
		{"a float", 1.5, "a document must be an object or an array, but a value of type float64 is a scalar"},
		{"a time", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "a document must be an object or an array, but a value of type time.Time is a scalar"},
		{"a duration", time.Second, "a document must be an object or an array, but a value of type time.Duration is a scalar"},
		{"bytes", []byte("x"), "a document must be an object or an array, but a value of type []uint8 is a scalar"},
		{"text", textMarshaler{"x"}, "a document must be an object or an array, but a value of type soml.textMarshaler is a scalar"},
		{"a pointer to a scalar", new("x"), "a document must be an object or an array, but a value of type *string is a scalar"},
		{"nil", nil, "a document must be an object or an array, but nil is null"},
		{"a nil pointer", (*encodeServer)(nil), "a document must be an object or an array, but a value of type *soml.encodeServer is null"},
		{"a nil map behind a pointer", (*map[string]int)(nil), "a document must be an object or an array, but a value of type *map[string]int is null"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, testCase.value, testCase.message)
		})
	}

	t.Run("an int", func(t *testing.T) {
		_, err := Marshal(42)
		somlError, ok := errors.AsType[*Error](err)

		if !ok || somlError.Line != 0 || !strings.HasPrefix(somlError.Message, "a document must be an object or an array, but ") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestMarshalUnsupportedTypes(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		message string
	}{
		{"channel", make(chan int), "a value of type chan int cannot be written as SOML"},
		{"nil channel", (chan int)(nil), "a value of type chan int cannot be written as SOML"},
		{"function", func() {}, "a value of type func() cannot be written as SOML"},
		{"complex128", complex(1, 2), "a value of type complex128 cannot be written as SOML"},
		{"complex64", complex64(complex(1, 2)), "a value of type complex64 cannot be written as SOML"},
		{"in a slice", []any{1, make(chan bool)}, "a value of type chan bool cannot be written as SOML"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			checkMarshalError(t, map[string]any{"value": testCase.value}, testCase.message)
		})
	}
}

type roundTripValue struct {
	String    string                 `soml:"string"`
	Escaped   string                 `soml:"escaped"`
	Int       int                    `soml:"int"`
	Int8      int8                   `soml:"int8"`
	Uint64    uint64                 `soml:"uint64"`
	Float64   float64                `soml:"float64"`
	Float32   float32                `soml:"float32"`
	Infinity  float64                `soml:"infinity"`
	Bool      bool                   `soml:"bool"`
	Time      time.Time              `soml:"time"`
	Duration  time.Duration          `soml:"duration"`
	Bytes     []byte                 `soml:"bytes"`
	Array     [3]int                 `soml:"array"`
	ByteArray [2]byte                `soml:"byteArray"`
	Slice     []string               `soml:"slice"`
	Map       map[string]int         `soml:"map"`
	IntMap    map[int]bool           `soml:"intMap"`
	TextMap   map[netip.Addr]string  `soml:"textMap"`
	Pointer   *encodeServer          `soml:"pointer"`
	Nil       *encodeServer          `soml:"nil"`
	Interface any                    `soml:"interface"`
	Nested    []map[string][]float64 `soml:"nested"`
	Address   netip.Addr             `soml:"address"`
	EmbeddedBase
}

// treeNode is a recursive type.
type treeNode struct {
	Name     string               `soml:"name"`
	Children []treeNode           `soml:"children,omitempty"`
	Parent   *treeNode            `soml:"parent,omitempty"`
	ByName   map[string]*treeNode `soml:"byName,omitempty"`
}

func TestMarshalRecursiveTypes(t *testing.T) {
	original := treeNode{
		Name:     "root",
		Children: []treeNode{{Name: "a", Children: []treeNode{{Name: "a1"}}}, {Name: "b"}},
		ByName:   map[string]*treeNode{"c": {Name: "c", Parent: &treeNode{Name: "root"}}},
	}

	expected := `name: 'root'
children: [
	{
		name: 'a'
		children: [
			{
				name: 'a1'
			}
		]
	}
	{
		name: 'b'
	}
]
byName: {
	c: {
		name: 'c'
		parent: {
			name: 'root'
		}
	}
}
`

	checkMarshal(t, original, expected)

	var decoded treeNode

	if err := Unmarshal([]byte(expected), &decoded); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("got %#v", decoded)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	original := roundTripValue{
		String:    "text",
		Escaped:   "it's\t\"quoted\"\n\x00\u2028",
		Int:       -42,
		Int8:      math.MinInt8,
		Uint64:    math.MaxInt64,
		Float64:   0.1,
		Float32:   0.1,
		Infinity:  math.Inf(-1),
		Bool:      true,
		Time:      time.Date(2026, 9, 19, 14, 0, 0, 123_456_789, time.UTC),
		Duration:  -90*time.Minute - 1,
		Bytes:     []byte{0, 1, 0xff},
		Array:     [3]int{1, 2, 3},
		ByteArray: [2]byte{4, 5},
		Slice:     []string{"a", ""},
		Map:       map[string]int{"a b": 1, "": 2},
		IntMap:    map[int]bool{-1: true, 404: false},
		TextMap:   map[netip.Addr]string{netip.MustParseAddr("10.0.0.1"): "a"},
		Pointer:   &encodeServer{Port: 1, Host: "h"},
		Interface: map[string]any{"a": []any{int64(1), 1.5, "x", nil, false, time.Duration(5)}},
		Nested:    []map[string][]float64{{"a": {1e300, -5e-324}}, {}},
		Address:   netip.MustParseAddr("::1"),
		EmbeddedBase: EmbeddedBase{
			ID:   7,
			Kind: "embedded",
		},
	}

	data, err := Marshal(original)

	if err != nil {
		t.Fatal(err)
	}

	var decoded roundTripValue

	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%v in\n%s", err, data)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("got %#v\nwant %#v", decoded, original)
	}

	again, err := Marshal(decoded)

	if err != nil {
		t.Fatal(err)
	}

	if string(again) != string(data) {
		t.Fatalf("canonical form is not a fixed point:\n%s\n%s", data, again)
	}

	t.Run("float32 extremes", func(t *testing.T) {
		original := []float32{math.MaxFloat32, -math.MaxFloat32, math.SmallestNonzeroFloat32, 0.1, 16777216, float32(math.Inf(1))}
		data, err := Marshal(original)

		if err != nil {
			t.Fatal(err)
		}

		var decoded []float32

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%v in\n%s", err, data)
		}

		if !reflect.DeepEqual(decoded, original) {
			t.Fatalf("got %v, want %v", decoded, original)
		}
	})

	t.Run("float64 extremes", func(t *testing.T) {
		original := []float64{math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, 2.2250738585072014e-308, 1e21, 1e-7, 9007199254740993}
		data, err := Marshal(original)

		if err != nil {
			t.Fatal(err)
		}

		var decoded []float64

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%v in\n%s", err, data)
		}

		if !reflect.DeepEqual(decoded, original) {
			t.Fatalf("got %v, want %v", decoded, original)
		}
	})
}

type appendOnlyKey string

func (key appendOnlyKey) AppendText(buffer []byte) ([]byte, error) {
	return append(buffer, "key "+string(key)...), nil
}

func TestMarshalUnusualTypes(t *testing.T) {
	t.Run("a nil interface map key is an error, not a panic", func(t *testing.T) {
		if _, err := Marshal(map[encoding.TextMarshaler]int{nil: 1}); err == nil || !strings.Contains(err.Error(), "is nil") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a map key uses AppendText as a value does", func(t *testing.T) {
		data, err := Marshal(map[appendOnlyKey]appendOnlyKey{"a": "b"})

		if err != nil || string(data) != "'key a': 'key b'\n" {
			t.Fatalf("got %q, %v", data, err)
		}
	})

	t.Run("a float32 whose shortest digits round twice reads back", func(t *testing.T) {
		value := math.Float32frombits(0x15ae43fd)
		data, err := Marshal([]float32{value})

		if err != nil {
			t.Fatal(err)
		}

		var decoded []float32

		if err := Unmarshal(data, &decoded); err != nil || math.Float32bits(decoded[0]) != 0x15ae43fd {
			t.Fatalf("%q read back as %x, %v", data, math.Float32bits(decoded[0]), err)
		}
	})

	t.Run("a key with a bidirectional control is sanitized in the message", func(t *testing.T) {
		_, err := Marshal(map[lowerKey]int{"a\u202e": 1, "A\u202e": 2})

		somlError, ok := errors.AsType[*Error](err)

		if !ok || strings.ContainsRune(err.Error(), '\u202e') || strings.ContainsRune(somlError.Message, '\u202e') {
			t.Fatalf("got %q", err)
		}
	})
}

type namedLevel int

func (level namedLevel) MarshalSOML() (any, error) {
	return []string{"low", "high"}[level], nil
}

// Every key type that Marshal writes reads back as the same key, because keys use only the text methods, strings, and integers, also through pointers.
func TestMapKeysRoundTrip(t *testing.T) {
	instant := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	number := 5

	t.Run("time.Time keys use their text methods", func(t *testing.T) {
		data, err := Marshal(map[time.Time]int{instant: 1})

		if err != nil || string(data) != "'2026-01-02T03:04:05Z': 1\n" {
			t.Fatalf("got %q, %v", data, err)
		}

		var decoded map[time.Time]int

		if err := Unmarshal(data, &decoded); err != nil || decoded[instant] != 1 {
			t.Fatalf("got %v, %v", decoded, err)
		}
	})

	t.Run("pointer keys", func(t *testing.T) {
		data, err := Marshal(map[*int]string{&number: "a"})

		if err != nil || string(data) != "5: 'a'\n" {
			t.Fatalf("got %q, %v", data, err)
		}

		var decoded map[*int]string

		if err := Unmarshal(data, &decoded); err != nil || len(decoded) != 1 {
			t.Fatalf("got %v, %v", decoded, err)
		}

		for key, value := range decoded {
			if *key != 5 || value != "a" {
				t.Fatalf("got %v: %v", *key, value)
			}
		}
	})

	t.Run("time.Duration keys are integers, as their kind is", func(t *testing.T) {
		data, err := Marshal(map[time.Duration]int{5: 1})

		if err != nil || string(data) != "5: 1\n" {
			t.Fatalf("got %q, %v", data, err)
		}
	})

	t.Run("MarshalSOML is not used for a key", func(t *testing.T) {
		data, err := Marshal(map[namedLevel]int{0: 5, 1: 6})

		if err != nil || string(data) != "0: 5\n1: 6\n" {
			t.Fatalf("got %q, %v", data, err)
		}

		var decoded map[namedLevel]int

		if err := Unmarshal(data, &decoded); err != nil || decoded[1] != 6 {
			t.Fatalf("got %v, %v", decoded, err)
		}
	})
}

type selfKey *selfKey

func TestMapKeysThatPointToThemselvesAreAnError(t *testing.T) {
	var key selfKey
	key = &key

	if _, err := Marshal(map[selfKey]int{key: 1}); err == nil || !strings.Contains(err.Error(), "most likely a cycle") {
		t.Fatalf("Marshal gave %v", err)
	}

	var value map[selfKey]int

	if err := Unmarshal([]byte("a: 1"), &value); err == nil || !strings.Contains(err.Error(), "most likely a cycle") {
		t.Fatalf("Unmarshal gave %v", err)
	}
}

type onlyUnmarshalByte byte

func (value *onlyUnmarshalByte) UnmarshalText(text []byte) error {
	*value = onlyUnmarshalByte(len(text))
	return nil
}

// A byte type with a method that encodes or decodes it is not base64 bytes, the same in Marshal and Unmarshal.
func TestBytesWithADecodingMethodAreAnArray(t *testing.T) {
	data, err := Marshal(map[string]any{"a": []onlyUnmarshalByte{1, 2}})

	if err != nil || string(data) != "a: [\n\t1\n\t2\n]\n" {
		t.Fatalf("got %q, %v", data, err)
	}
}

func TestMarshalWrite(t *testing.T) {
	t.Run("writes what Marshal returns", func(t *testing.T) {
		value := map[string]any{"b": []int{1}, "a": 'x'}
		expected, err := Marshal(value)

		if err != nil {
			t.Fatal(err)
		}

		var output bytes.Buffer

		if err := MarshalWrite(&output, value); err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(output.Bytes(), expected) {
			t.Fatalf("got %q, want %q", output.String(), expected)
		}
	})

	t.Run("takes the options of Marshal", func(t *testing.T) {
		var output bytes.Buffer

		if err := MarshalWrite(&output, orderedAuthor{Name: "n", Email: "e"}, Canonical()); err != nil {
			t.Fatal(err)
		}

		if output.String() != "email: 'e'\nname: 'n'\n" {
			t.Fatalf("got %q", output.String())
		}
	})

	t.Run("writes nothing for a value that is not SOML", func(t *testing.T) {
		var output bytes.Buffer
		err := MarshalWrite(&output, map[string]any{"a": math.NaN()})

		if err == nil || output.Len() != 0 {
			t.Fatalf("got %v and %q", err, output.String())
		}
	})

	t.Run("an error from the writer is the cause", func(t *testing.T) {
		cause := errors.New("disk full")
		err := MarshalWrite(failingWriter{cause}, map[string]any{"a": 1})
		somlError, ok := errors.AsType[*Error](err)

		if !ok || !errors.Is(err, cause) || somlError.Error() != "soml: cannot write the document: disk full" {
			t.Fatalf("got %#v", err)
		}
	})

	t.Run("a short write is an error", func(t *testing.T) {
		for _, count := range []int{0, 4} {
			err := MarshalWrite(shortWriter{count}, map[string]any{"a": 1})
			somlError, ok := errors.AsType[*Error](err)

			if !ok || !errors.Is(err, io.ErrShortWrite) || somlError.Line != 0 || somlError.Error() != "soml: cannot write the document: short write" {
				t.Fatalf("writing %d bytes: got %#v", count, err)
			}
		}
	})
}

type shortWriter struct {
	count int
}

func (writer shortWriter) Write([]byte) (int, error) {
	return writer.count, nil
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

// Random float32 values, durations, and instants read back exactly, also an instant written with another offset.
func TestMarshalRoundTripsRandomScalars(t *testing.T) {
	random := rand.New(rand.NewPCG(5, 6))

	for range 100_000 {
		value := math.Float32frombits(random.Uint32())

		if math.IsNaN(float64(value)) {
			continue
		}

		data, err := Marshal([]float32{value})
		var items []float32

		if err != nil || Unmarshal(data, &items) != nil || items[0] != value {
			t.Fatalf("%v gave %q, which reads back as %v", value, data, items)
		}
	}

	for range 100_000 {
		value := time.Duration(random.Int64()) / time.Duration(1+random.Int64N(1e12))

		if random.IntN(2) == 0 {
			value = -value
		}

		data, err := Marshal([]time.Duration{value})
		var items []time.Duration

		if err != nil || Unmarshal(data, &items) != nil || items[0] != value {
			t.Fatalf("%d gave %q, which reads back as %v", int64(value), data, items)
		}
	}

	earliest := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	latest := time.Date(9999, 12, 30, 23, 59, 59, 0, time.UTC).Unix()

	for range 50_000 {
		value := time.Unix(earliest+random.Int64N(latest-earliest), random.Int64N(1e9)).UTC()
		data, err := Marshal([]time.Time{value})
		var items []time.Time

		if err != nil || Unmarshal(data, &items) != nil || !items[0].Equal(value) {
			t.Fatalf("%v gave %q, which reads back as %v", value, data, items)
		}

		offset := value.In(time.FixedZone("", (random.IntN(48)-24)*1800)).Format("2006-01-02T15:04:05.999999999Z07:00")

		if err := Unmarshal([]byte("["+offset+"]"), &items); err != nil || !items[0].Equal(value) {
			t.Fatalf("%s reads back as %v, want %v: %v", offset, items, value, err)
		}
	}
}

// limitNode is a struct at level 100, whose omitempty field is at level 101, one past the nesting limit.
func limitNode(value any) any {
	var node any = struct {
		Value any `soml:"value,omitempty"`
		Level int `soml:"level"`
	}{Value: value, Level: 100}

	for range 99 {
		node = map[string]any{"a": node}
	}

	return node
}

func TestMarshalOmitEmptyOneLevelPastTheLimit(t *testing.T) {
	const tooDeep = "the value is nested more than 100 levels deep, so no reader would accept the document"

	t.Run("left out", func(t *testing.T) {
		for _, value := range []any{
			map[string]any{},
			[]any{},
			[]int(nil),
			map[string]int(nil),
			struct{}{},
			&struct{}{},
			marshalerValue{map[string]any{}},
			marshalerValue{[]any{}},
			marshalerValue{marshalerValue{struct{}{}}},
			struct {
				Inner []int `soml:"inner,omitempty"`
			}{},
			nil,
			"",
		} {
			data, err := Marshal(limitNode(value))

			if err != nil {
				t.Errorf("%#v: %v", value, err)
				continue
			}

			if strings.Contains(string(data), "value") {
				t.Errorf("%#v is written:\n%s", value, data)
			}
		}
	})

	t.Run("an error", func(t *testing.T) {
		for _, value := range []any{
			map[string]any{"a": 1},
			[]any{1},
			struct{ A int }{},
			marshalerValue{[]any{nil}},
			nestingMarshaler{},
		} {
			_, err := Marshal(limitNode(value))
			somlError, ok := errors.AsType[*Error](err)

			if !ok || somlError.Message != tooDeep || len(somlError.Path) != 100 || somlError.Path[99] != "value" {
				t.Errorf("%#v: %v", value, err)
			}
		}
	})

	t.Run("an error of a MarshalSOML method is kept", func(t *testing.T) {
		_, err := Marshal(limitNode(failingMarshaler{}))

		if !errors.Is(err, errMarshal) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("a scalar at the limit is written", func(t *testing.T) {
		data, err := Marshal(limitNode(1))

		if err != nil || !strings.Contains(string(data), "value: 1") {
			t.Fatalf("got %s, %v", data, err)
		}
	})
}
