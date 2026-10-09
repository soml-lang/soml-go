package soml

// Tests for Unmarshal's type mapping, errors, and positions.

import (
	"encoding"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/rand/v2"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"
)

// decodeMemberInto decodes the document `value: <text>` into a struct whose one field has the type T.
func decodeMemberInto[T any](text string) (T, error) {
	var document struct {
		Value T `soml:"value"`
	}

	err := Unmarshal([]byte("value: "+text+"\n"), &document)
	return document.Value, err
}

// decodeCase is one decoding: run decodes, and on success the value must equal expected. With a message, it must fail with that message at the start of the value.
type decodeCase struct {
	name     string
	run      func() (any, error)
	expected any
	message  string
}

func decodeInto[T any](text string) func() (any, error) {
	return func() (any, error) {
		return decodeMemberInto[T](text)
	}
}

func checkDecodeCases(t *testing.T, cases []decodeCase) {
	t.Helper()

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := testCase.run()

			if testCase.message != "" {
				checkError(t, err, 1, len("value: ")+1, testCase.message)
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(value, testCase.expected) {
				t.Fatalf("got %#v (%T), want %#v (%T)", value, value, testCase.expected, testCase.expected)
			}
		})
	}
}

func TestDecodeAny(t *testing.T) {
	source := `string: 'text'
int: 42
float: 1.5
whole: 1.0
bool: true
null: null
instant: 2026-09-19T14:00:00+02:00
duration: 1h30m
array: [1, 'two', [], {}]
object: {a: 1}
'quoted.key': 1
`

	var value any

	if err := Unmarshal([]byte(source), &value); err != nil {
		t.Fatal(err)
	}

	expected := map[string]any{
		"string":     "text",
		"int":        int64(42),
		"float":      1.5,
		"whole":      1.0,
		"bool":       true,
		"null":       nil,
		"instant":    time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		"duration":   90 * time.Minute,
		"array":      []any{int64(1), "two", []any{}, map[string]any{}},
		"object":     map[string]any{"a": int64(1)},
		"quoted.key": int64(1),
	}

	if !reflect.DeepEqual(value, expected) {
		t.Fatalf("got %#v\nwant %#v", value, expected)
	}

	// An int and a float stay different.
	members := value.(map[string]any)

	if _, ok := members["whole"].(float64); !ok {
		t.Errorf("1.0 is a %T, not a float64", members["whole"])
	}

	if instant := members["instant"].(time.Time); instant.Location() != time.UTC {
		t.Errorf("the instant is in %v, not UTC", instant.Location())
	}

	t.Run("a top-level array", func(t *testing.T) {
		var value any

		if err := Unmarshal([]byte("[1, [2], {}]"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value, []any{int64(1), []any{int64(2)}, map[string]any{}}) {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("empty collections are not nil", func(t *testing.T) {
		var array any

		if err := Unmarshal([]byte("[]"), &array); err != nil {
			t.Fatal(err)
		}

		if items, ok := array.([]any); !ok || items == nil || len(items) != 0 {
			t.Errorf("[] is %#v", array)
		}

		var object any

		if err := Unmarshal([]byte("{}"), &object); err != nil {
			t.Fatal(err)
		}

		if members, ok := object.(map[string]any); !ok || members == nil || len(members) != 0 {
			t.Errorf("{} is %#v", object)
		}
	})

	t.Run("an existing map merges, as in encoding/json/v2", func(t *testing.T) {
		var value any = map[string]any{"old": 1, "nested": map[string]any{"a": 1}, "list": []any{1}}

		if err := Unmarshal([]byte("new: 2\nnested: {b: 2}\nlist: [3]"), &value); err != nil {
			t.Fatal(err)
		}

		expected := map[string]any{"old": 1, "new": int64(2), "nested": map[string]any{"a": 1, "b": int64(2)}, "list": []any{int64(3)}}

		if !reflect.DeepEqual(value, expected) {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an existing map in an interface is decoded with the methods of its type", func(t *testing.T) {
		var value any = mergedMap{}

		if err := Unmarshal([]byte("a: 1"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value, mergedMap{"called": true}) {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an existing value that is not a map is replaced", func(t *testing.T) {
		var value any = "old"

		if err := Unmarshal([]byte("new: 2"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value, map[string]any{"new": int64(2)}) {
			t.Fatalf("got %#v", value)
		}
	})
}

type mergedMap map[string]any

func (value mergedMap) UnmarshalSOML(any) error {
	value["called"] = true
	return nil
}

type byteName string

type blob []byte

func TestDecodeStrings(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "string", run: decodeInto[string]("'text'"), expected: "text"},
		{name: "string kind", run: decodeInto[byteName]("'text'"), expected: byteName("text")},
		{name: "empty", run: decodeInto[string]("''"), expected: ""},
		{name: "base64 into bytes", run: decodeInto[[]byte]("'aGVsbG8='"), expected: []byte("hello")},
		{name: "base64 into a named byte slice", run: decodeInto[blob]("'aGVsbG8='"), expected: blob("hello")},
		{name: "empty base64", run: decodeInto[[]byte]("''"), expected: []byte{}},
		{name: "base64 into a byte array", run: decodeInto[[4]byte]("'AQIDBA=='"), expected: [4]byte{1, 2, 3, 4}},
		{name: "an array into bytes", run: decodeInto[[]byte]("[1, 2]"), expected: []byte{1, 2}},
		{name: "invalid base64", run: decodeInto[[]byte]("'not base64!'"), message: "the string is not valid base64, which a Go value of type []uint8 needs"},
		{name: "unpadded base64", run: decodeInto[[]byte]("'aGVsbG8'"), message: "the string is not valid base64, which a Go value of type []uint8 needs"},
		{name: "base64 of the wrong length", run: decodeInto[[4]byte]("'AQID'"), message: "the string is not 4 bytes in base64, which a Go value of type [4]uint8 needs"},
		{name: "an int into a string", run: decodeInto[string]("1"), message: "cannot decode the int 1 into a Go value of type string"},
		{name: "an int into bytes", run: decodeInto[[]byte]("1"), message: "cannot decode the int 1 into a Go value of type []uint8"},
		{name: "null into a string", run: decodeInto[string]("null"), message: "cannot decode null into a Go value of type string; use a pointer to accept null"},
	})
}

type port uint16

func TestDecodeIntegers(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "int", run: decodeInto[int]("42"), expected: 42},
		{name: "int8 largest", run: decodeInto[int8]("127"), expected: int8(127)},
		{name: "int8 smallest", run: decodeInto[int8]("-128"), expected: int8(-128)},
		{name: "int8 too large", run: decodeInto[int8]("128"), message: "the int 128 does not fit in a Go value of type int8"},
		{name: "int8 too small", run: decodeInto[int8]("-129"), message: "the int -129 does not fit in a Go value of type int8"},
		{name: "int16", run: decodeInto[int16]("32767"), expected: int16(32767)},
		{name: "int16 too large", run: decodeInto[int16]("32768"), message: "the int 32768 does not fit in a Go value of type int16"},
		{name: "int32", run: decodeInto[int32]("-2147483648"), expected: int32(math.MinInt32)},
		{name: "int32 too large", run: decodeInto[int32]("2147483648"), message: "the int 2147483648 does not fit in a Go value of type int32"},
		{name: "int64 largest", run: decodeInto[int64]("9223372036854775807"), expected: int64(math.MaxInt64)},
		{name: "int64 smallest", run: decodeInto[int64]("-9223372036854775808"), expected: int64(math.MinInt64)},
		{name: "uint8", run: decodeInto[uint8]("0xFF"), expected: uint8(255)},
		{name: "uint8 too large", run: decodeInto[uint8]("256"), message: "the int 256 does not fit in a Go value of type uint8"},
		{name: "uint8 negative", run: decodeInto[uint8]("-1"), message: "the int -1 does not fit in a Go value of type uint8"},
		{name: "uint16", run: decodeInto[uint16]("65535"), expected: uint16(65535)},
		{name: "uint32", run: decodeInto[uint32]("4294967295"), expected: uint32(math.MaxUint32)},
		{name: "uint32 too large", run: decodeInto[uint32]("4294967296"), message: "the int 4294967296 does not fit in a Go value of type uint32"},
		{name: "uint64", run: decodeInto[uint64]("9223372036854775807"), expected: uint64(math.MaxInt64)},
		{name: "uint64 negative", run: decodeInto[uint64]("-9223372036854775808"), message: "the int -9223372036854775808 does not fit in a Go value of type uint64"},
		{name: "uint negative", run: decodeInto[uint]("-1"), message: "the int -1 does not fit in a Go value of type uint"},
		{name: "uintptr", run: decodeInto[uintptr]("1"), expected: uintptr(1)},
		{name: "integer kind", run: decodeInto[port]("8080"), expected: port(8080)},
		{name: "integer kind too large", run: decodeInto[port]("65536"), message: "the int 65536 does not fit in a Go value of type soml.port"},
		{name: "a float", run: decodeInto[int]("1.5"), message: "cannot decode the float 1.5 into a Go value of type int; a float never decodes into an integer type"},
		{name: "a whole float", run: decodeInto[int64]("1.0"), message: "cannot decode the float 1.0 into a Go value of type int64; a float never decodes into an integer type"},
		{name: "infinity", run: decodeInto[uint8]("infinity"), message: "cannot decode the float infinity into a Go value of type uint8; a float never decodes into an integer type"},
		{name: "a string", run: decodeInto[int]("'1'"), message: "cannot decode the string “1” into a Go value of type int"},
		{name: "a bool", run: decodeInto[int]("true"), message: "cannot decode the bool true into a Go value of type int"},
		{name: "a duration", run: decodeInto[int64]("1s"), message: "cannot decode the duration 1s into a Go value of type int64"},
		{name: "null", run: decodeInto[int]("null"), message: "cannot decode null into a Go value of type int; use a pointer to accept null"},
	})
}

type ratio float64

func TestDecodeFloats(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "float64", run: decodeInto[float64]("1.5"), expected: 1.5},
		{name: "float32", run: decodeInto[float32]("1.5"), expected: float32(1.5)},
		{name: "float32 tenth", run: decodeInto[float32]("0.1"), expected: float32(0.1)},
		{name: "float kind", run: decodeInto[ratio]("0.25"), expected: ratio(0.25)},
		{name: "infinity into float32", run: decodeInto[float32]("infinity"), expected: float32(math.Inf(1))},
		{name: "negative infinity into float32", run: decodeInto[float32]("-infinity"), expected: float32(math.Inf(-1))},
		{name: "the largest float32", run: decodeInto[float32]("3.4028235e38"), expected: float32(math.MaxFloat32)},
		{name: "the largest float32 in full", run: decodeInto[float32]("340282346638528859811704183484516925440.0"), expected: float32(math.MaxFloat32)},
		{name: "too large for float32", run: decodeInto[float32]("3.5e38"), message: "the float 3.5e38 is too large for a Go value of type float32"},
		{name: "too small for float32", run: decodeInto[float32]("-1e39"), message: "the float -1e39 is too large for a Go value of type float32"},
		{name: "float64 largest into float32", run: decodeInto[float32]("1.7976931348623157e308"), message: "the float 1.7976931348623157e308 is too large for a Go value of type float32"},
		// Just above the midpoint between two float32 values, where the nearest float64 is the midpoint itself, so rounding to a float64 first would round to the even float32 below.
		{name: "just above a float32 midpoint", run: decodeInto[float32]("1.0000001788139343253045132620115964527940377593040466308593749"), expected: float32(1.00000011920928955078125)},
		{name: "just above a float32 midpoint with underscores", run: decodeInto[float32]("1.000_000_178_813_934_325_304_513_262_011_596_452_794_037_759_304_046_630_859_374_9"), expected: float32(1.00000011920928955078125)},
		{name: "just below the float32 overflow threshold", run: decodeInto[float32]("3.40282356779733661637539395458142568447e38"), expected: float32(math.MaxFloat32)},
		{name: "just above half of the smallest float32", run: decodeInto[float32]("7.006492321624085354618647916449580656401309709382578858785341419448955413429303007433190941810607910156251e-46"), expected: float32(math.SmallestNonzeroFloat32)},
		// An int converts only when it is exact.
		{name: "an int into float64", run: decodeInto[float64]("1"), expected: 1.0},
		{name: "an int into a float kind", run: decodeInto[ratio]("3"), expected: ratio(3)},
		{name: "a negative int into float64", run: decodeInto[float64]("-42"), expected: -42.0},
		{name: "2^53 into float64", run: decodeInto[float64]("9007199254740992"), expected: 9007199254740992.0},
		{name: "2^53 + 1 into float64", run: decodeInto[float64]("9007199254740993"), message: "the int 9007199254740993 cannot be a Go value of type float64 exactly; write it as a float"},
		{name: "-2^63 into float64", run: decodeInto[float64]("-9223372036854775808"), expected: -9223372036854775808.0},
		{name: "2^63 - 1 into float64", run: decodeInto[float64]("9223372036854775807"), message: "the int 9223372036854775807 cannot be a Go value of type float64 exactly; write it as a float"},
		{name: "2^62 into float64", run: decodeInto[float64]("0x4000000000000000"), expected: 4611686018427387904.0},
		{name: "2^24 into float32", run: decodeInto[float32]("16777216"), expected: float32(16777216)},
		{name: "2^24 + 1 into float32", run: decodeInto[float32]("16777217"), message: "the int 16777217 cannot be a Go value of type float32 exactly; write it as a float"},
		{name: "-2^63 into float32", run: decodeInto[float32]("-9223372036854775808"), expected: float32(-9223372036854775808.0)},
		{name: "a string", run: decodeInto[float64]("'1.5'"), message: "cannot decode the string “1.5” into a Go value of type float64"},
		{name: "a bool", run: decodeInto[float32]("false"), message: "cannot decode the bool false into a Go value of type float32"},
		{name: "null", run: decodeInto[float64]("null"), message: "cannot decode null into a Go value of type float64; use a pointer to accept null"},
	})
}

type flag bool

func TestDecodeBools(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "true", run: decodeInto[bool]("true"), expected: true},
		{name: "false", run: decodeInto[bool]("false"), expected: false},
		{name: "bool kind", run: decodeInto[flag]("true"), expected: flag(true)},
		{name: "an int", run: decodeInto[bool]("1"), message: "cannot decode the int 1 into a Go value of type bool"},
		{name: "a string", run: decodeInto[bool]("'true'"), message: "cannot decode the string “true” into a Go value of type bool"},
		{name: "null", run: decodeInto[bool]("null"), message: "cannot decode null into a Go value of type bool; use a pointer to accept null"},
	})
}

func TestDecodeTime(t *testing.T) {
	instant := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)

	checkDecodeCases(t, []decodeCase{
		{name: "instant", run: decodeInto[time.Time]("2026-09-19T16:00:00+02:00"), expected: instant},
		{name: "instant into a pointer", run: decodeInto[*time.Time]("2026-09-19T14:00:00Z"), expected: &instant},
		{name: "instant into any", run: decodeInto[any]("2026-09-19T14:00:00Z"), expected: instant},
		{name: "a string never decodes into a time.Time", run: decodeInto[time.Time]("'2026-09-19T14:00:00Z'"), message: "cannot decode the string “2026-09-19T14:00:00Z” into a Go value of type time.Time; an instant is written without quotes, as in 2026-09-19T14:00:00Z"},
		{name: "an int into a time.Time", run: decodeInto[time.Time]("0"), message: "cannot decode the int 0 into a Go value of type time.Time"},
		{name: "a duration into a time.Time", run: decodeInto[time.Time]("1h"), message: "cannot decode the duration 1h into a Go value of type time.Time"},
		{name: "null into a time.Time", run: decodeInto[time.Time]("null"), message: "cannot decode null into a Go value of type time.Time; use a pointer to accept null"},
		{name: "null into a pointer to a time.Time", run: decodeInto[*time.Time]("null"), expected: (*time.Time)(nil)},
		{name: "duration", run: decodeInto[time.Duration]("1h30m"), expected: 90 * time.Minute},
		{name: "negative duration", run: decodeInto[time.Duration]("-1.5s"), expected: -1500 * time.Millisecond},
		{name: "duration into a pointer", run: decodeInto[*time.Duration]("0s"), expected: new(time.Duration)},
		{name: "a string never decodes into a time.Duration", run: decodeInto[time.Duration]("'1h'"), message: "cannot decode the string “1h” into a Go value of type time.Duration; a duration is written without quotes, as in 30s"},
		{name: "an int never decodes into a time.Duration", run: decodeInto[time.Duration]("5"), message: "cannot decode the int 5 into a Go value of type time.Duration; a duration needs a unit, as in 30s"},
		{name: "an instant into a time.Duration", run: decodeInto[time.Duration]("2026-09-19T14:00:00Z"), message: "cannot decode the instant 2026-09-19T14:00:00Z into a Go value of type time.Duration"},
		{name: "a float into a time.Duration", run: decodeInto[time.Duration]("1.5"), message: "cannot decode the float 1.5 into a Go value of type time.Duration; a duration needs a unit, as in 30s"},
	})
}

func TestDecodeNull(t *testing.T) {
	number := 5

	type nullable struct {
		Pointer   *int           `soml:"pointer"`
		Interface any            `soml:"interface"`
		Map       map[string]int `soml:"map"`
		Slice     []int          `soml:"slice"`
		Bytes     []byte         `soml:"bytes"`
	}

	value := nullable{
		Pointer:   &number,
		Interface: "set",
		Map:       map[string]int{"a": 1},
		Slice:     []int{1},
		Bytes:     []byte("x"),
	}

	if err := Unmarshal([]byte("pointer: null\ninterface: null\nmap: null\nslice: null\nbytes: null\n"), &value); err != nil {
		t.Fatal(err)
	}

	if value.Pointer != nil || value.Interface != nil || value.Map != nil || value.Slice != nil || value.Bytes != nil {
		t.Fatalf("null did not set nil: %#v", value)
	}

	if number != 5 {
		t.Errorf("null changed what the pointer pointed to: %d", number)
	}

	checkDecodeCases(t, []decodeCase{
		{name: "a struct", run: decodeInto[decodeServer]("null"), message: "cannot decode null into a Go value of type soml.decodeServer; use a pointer to accept null"},
		{name: "an array", run: decodeInto[[2]int]("null"), message: "cannot decode null into a Go value of type [2]int; use a pointer to accept null"},
		{name: "a pointer to a pointer", run: decodeInto[**int]("null"), expected: (**int)(nil)},
	})
}

func TestDecodeSlicesAndArrays(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "slice", run: decodeInto[[]int]("[1, 2, 3]"), expected: []int{1, 2, 3}},
		{name: "empty slice", run: decodeInto[[]int]("[]"), expected: []int{}},
		{name: "nested", run: decodeInto[[][]string]("[['a'], [], ['b', 'c']]"), expected: [][]string{{"a"}, {}, {"b", "c"}}},
		{name: "slice of any", run: decodeInto[[]any]("[1, 'a', null]"), expected: []any{int64(1), "a", nil}},
		{name: "array", run: decodeInto[[3]int]("[1, 2, 3]"), expected: [3]int{1, 2, 3}},
		{name: "empty array", run: decodeInto[[0]int]("[]"), expected: [0]int{}},
		{name: "too few items", run: decodeInto[[3]int]("[1, 2]"), message: "the array has 2 items, but a Go value of type [3]int has 3"},
		{name: "too many items", run: decodeInto[[3]int]("[1, 2, 3, 4]"), message: "the array has 4 items, but a Go value of type [3]int has 3"},
		{name: "an object into a slice", run: decodeInto[[]int]("{}"), message: "cannot decode an object into a Go value of type []int"},
		{name: "a string into a slice", run: decodeInto[[]string]("'a'"), message: "cannot decode the string “a” into a Go value of type []string"},
		{name: "an object into an array", run: decodeInto[[1]int]("{a: 1}"), message: "cannot decode an object into a Go value of type [1]int"},
	})

	t.Run("a slice is replaced", func(t *testing.T) {
		value := struct {
			Numbers []int            `soml:"numbers"`
			Servers []decodeServer   `soml:"servers"`
			Array   [3]int           `soml:"array"`
			Nested  map[string][]int `soml:"nested"`
		}{
			Numbers: []int{1, 2, 3},
			Servers: []decodeServer{{Host: "kept?", Port: 1}},
			Array:   [3]int{7, 8, 9},
			Nested:  map[string][]int{"a": {1, 2}},
		}

		if err := Unmarshal([]byte("numbers: [4]\nservers: [{port: 2}]\narray: [1, 2, 3]\nnested: {a: [3]}\n"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value.Numbers, []int{4}) {
			t.Errorf("numbers: %v", value.Numbers)
		}

		if !reflect.DeepEqual(value.Servers, []decodeServer{{Port: 2}}) {
			t.Errorf("servers: %#v", value.Servers)
		}

		if value.Array != [3]int{1, 2, 3} {
			t.Errorf("array: %v", value.Array)
		}

		if !reflect.DeepEqual(value.Nested, map[string][]int{"a": {3}}) {
			t.Errorf("nested: %v", value.Nested)
		}
	})

	t.Run("the position of an item", func(t *testing.T) {
		var value []int
		err := Unmarshal([]byte("[\n\t1,\n\t'two',\n]\n"), &value)
		checkError(t, err, 3, 2, "cannot decode the string “two” into a Go value of type int")
	})
}

type decodeServer struct {
	Host string `soml:"host"`
	Port int    `soml:"port"`
}

type decodeConfig struct {
	Name        string            `soml:"name"`
	ReleaseDate string            // The key is the field name as it is.
	Server      decodeServer      `soml:"server"`
	Backup      *decodeServer     `soml:"backup"`
	Tags        []string          `soml:"tags"`
	Limits      map[string]int    `soml:"limits"`
	Timeout     time.Duration     `soml:"timeout,omitzero"`
	Owner       *string           `soml:"owner,omitempty"`
	Internal    string            `soml:"-"`
	Dash        string            `soml:"-,"`
	Labels      map[string]string `soml:"labels,omitzero,omitempty"`
	unexported  string
}

func TestDecodeStruct(t *testing.T) {
	source := `# A config.
name: 'soml'
ReleaseDate: '2026-10-06'
server: {
	host: 'localhost',
	port: 8080, # the port
}
backup: {host: 'backup', port: 8081}
tags: ['a', 'b']
limits: {cpu: 2}
timeout: 30s
owner: 'sindre'
Internal: 'ignored'
-: 'dash'
labels: {}
unexported: 'ignored'
`

	var config decodeConfig

	if err := Unmarshal([]byte(source), &config); err != nil {
		t.Fatal(err)
	}

	owner := "sindre"

	expected := decodeConfig{
		Name:        "soml",
		ReleaseDate: "2026-10-06",
		Server:      decodeServer{Host: "localhost", Port: 8080},
		Backup:      &decodeServer{Host: "backup", Port: 8081},
		Tags:        []string{"a", "b"},
		Limits:      map[string]int{"cpu": 2},
		Timeout:     30 * time.Second,
		Owner:       &owner,
		Dash:        "dash",
		Labels:      map[string]string{},
	}

	if !reflect.DeepEqual(config, expected) {
		t.Fatalf("got %#v\nwant %#v", config, expected)
	}

	t.Run("keys match exactly", func(t *testing.T) {
		var config decodeConfig

		if err := Unmarshal([]byte("Name: 'a'\nreleasedate: 'b'\nrelease-date: 'c'\nRELEASEDATE: 'd'\nrelease_date: 'e'\n"), &config); err != nil {
			t.Fatal(err)
		}

		if config.Name != "" || config.ReleaseDate != "" {
			t.Fatalf("a key matched a field without being exactly its key: %#v", config)
		}
	})

	t.Run("a field that the document does not set keeps its value", func(t *testing.T) {
		config := decodeConfig{
			Name:       "default",
			Server:     decodeServer{Host: "default-host", Port: 1},
			Backup:     &decodeServer{Host: "backup-host", Port: 2},
			Tags:       []string{"default"},
			Limits:     map[string]int{"memory": 512, "cpu": 1},
			Internal:   "internal",
			unexported: "unexported",
		}

		backup := config.Backup

		if err := Unmarshal([]byte("server: {port: 9000}\nbackup: {port: 9001}\nlimits: {cpu: 4}\nInternal: 'x'\nunexported: 'x'\n"), &config); err != nil {
			t.Fatal(err)
		}

		expected := decodeConfig{
			Name:       "default",
			Server:     decodeServer{Host: "default-host", Port: 9000},
			Backup:     &decodeServer{Host: "backup-host", Port: 9001},
			Tags:       []string{"default"},
			Limits:     map[string]int{"memory": 512, "cpu": 4},
			Internal:   "internal",
			unexported: "unexported",
		}

		if !reflect.DeepEqual(config, expected) {
			t.Fatalf("got %#v\nwant %#v", config, expected)
		}

		if config.Backup != backup {
			t.Error("the existing pointer was replaced instead of decoded into")
		}
	})

	t.Run("not an object", func(t *testing.T) {
		var config decodeConfig
		err := Unmarshal([]byte("[1]"), &config)
		checkError(t, err, 1, 1, "cannot decode an array into a Go value of type soml.decodeConfig")

		_, err = decodeMemberInto[decodeServer]("'localhost'")
		checkError(t, err, 1, 8, "cannot decode the string “localhost” into a Go value of type soml.decodeServer")
	})
}

type EmbeddedBase struct {
	ID   int `soml:"id"`
	Kind string
}

type embeddedHidden struct {
	Secret int
}

type embeddedOther struct {
	Kind  string
	Extra string
}

type embeddedTagged struct {
	Kind string `soml:"Kind"`
}

type Label string

// sharedInner is embedded in two structs, so a struct that embeds both has its field twice at one depth.
type sharedInner struct {
	Shared int
}

type sharedLeft struct {
	sharedInner
	Left int
}

type sharedRight struct {
	sharedInner
}

type sharedBoth struct {
	sharedLeft
	sharedRight
}

func TestDecodeEmbedded(t *testing.T) {
	t.Run("promoted fields", func(t *testing.T) {
		var value struct {
			EmbeddedBase
			Name string `soml:"name"`
		}

		if err := Unmarshal([]byte("id: 1\nKind: 'k'\nname: 'n'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.ID != 1 || value.Kind != "k" || value.Name != "n" {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an embedded unexported struct", func(t *testing.T) {
		var value struct {
			embeddedHidden
		}

		if err := Unmarshal([]byte("Secret: 1\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Secret != 1 {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an embedded pointer is made when a field in it is set", func(t *testing.T) {
		var value struct {
			*EmbeddedBase
			Name string
		}

		if err := Unmarshal([]byte("Name: 'n'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.EmbeddedBase != nil {
			t.Fatalf("the embedded pointer was made for a document that does not use it: %#v", value.EmbeddedBase)
		}

		if err := Unmarshal([]byte("id: 2\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.EmbeddedBase == nil || value.ID != 2 || value.Name != "n" {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an embedded pointer to an unexported struct", func(t *testing.T) {
		var value struct {
			*embeddedHidden
		}

		err := Unmarshal([]byte("\nSecret: 1\n"), &value)
		checkError(t, err, 2, 1, "cannot set the field Secret, because it is in an embedded pointer to an unexported struct type soml.embeddedHidden")

		// When it is not nil, it can be decoded into.
		value.embeddedHidden = &embeddedHidden{}

		if err := Unmarshal([]byte("Secret: 1\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Secret != 1 {
			t.Fatalf("got %#v", value.embeddedHidden)
		}
	})

	t.Run("a shallower field wins", func(t *testing.T) {
		var value struct {
			EmbeddedBase
			Kind int
		}

		if err := Unmarshal([]byte("Kind: 3\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Kind != 3 || value.EmbeddedBase.Kind != "" {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("two fields at one depth are ambiguous and ignored", func(t *testing.T) {
		var value struct {
			EmbeddedBase
			embeddedOther
		}

		if err := Unmarshal([]byte("Kind: 'k'\nExtra: 'e'\nid: 1\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.EmbeddedBase.Kind != "" || value.embeddedOther.Kind != "" {
			t.Fatalf("an ambiguous field was set: %#v", value)
		}

		if value.Extra != "e" || value.ID != 1 {
			t.Fatalf("got %#v", value)
		}

		err := Unmarshal([]byte("\nKind: 'k'\n"), &value, RejectUnknownKeys())
		checkError(t, err, 2, 1, "unknown key Kind; a Go value of type struct { soml.EmbeddedBase; soml.embeddedOther } has no field for it")
	})

	t.Run("one struct embedded twice is ambiguous", func(t *testing.T) {
		var value sharedBoth

		if err := Unmarshal([]byte("Shared: 1\nLeft: 2\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.sharedLeft.Shared != 0 || value.sharedRight.Shared != 0 || value.Left != 2 {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("a tagged field wins at one depth", func(t *testing.T) {
		var value struct {
			EmbeddedBase
			embeddedTagged
		}

		if err := Unmarshal([]byte("Kind: 'k'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.embeddedTagged.Kind != "k" || value.EmbeddedBase.Kind != "" {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an embedded struct with a tag is one field", func(t *testing.T) {
		var value struct {
			EmbeddedBase `soml:"base"`
		}

		if err := Unmarshal([]byte("base: {id: 1}\nid: 2\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.ID != 1 {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("an embedded type that is not a struct", func(t *testing.T) {
		var value struct {
			Label
			byteName
		}

		if err := Unmarshal([]byte("Label: 'l'\nbyteName: 'b'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Label != "l" || value.byteName != "" {
			t.Fatalf("got %#v", value)
		}
	})
}

func TestDecodeRejectUnknownKeys(t *testing.T) {
	t.Run("ignored by default", func(t *testing.T) {
		var server decodeServer

		if err := Unmarshal([]byte("host: 'a'\nextra: 1\n"), &server); err != nil {
			t.Fatal(err)
		}

		if server.Host != "a" {
			t.Fatalf("got %#v", server)
		}
	})

	cases := []struct {
		name    string
		source  string
		line    int
		column  int
		message string
	}{
		{"top level", "host: 'a'\nextra: 1\n", 2, 1, "unknown key extra; a Go value of type soml.decodeServer has no field for it"},
		{"quoted key", "host: 'a'\n'the key': 1\n", 2, 1, "unknown key \"the key\"; a Go value of type soml.decodeServer has no field for it"},
		{"case differs", "Host: 'a'\n", 1, 1, "unknown key Host; a Go value of type soml.decodeServer has no field for it"},
		{"braced", "{host: 'a', port: 1, bogus: 2}", 1, 22, "unknown key bogus; a Go value of type soml.decodeServer has no field for it"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var server decodeServer
			err := Unmarshal([]byte(testCase.source), &server, RejectUnknownKeys())
			checkError(t, err, testCase.line, testCase.column, testCase.message)
		})
	}

	t.Run("nested", func(t *testing.T) {
		var config decodeConfig
		err := Unmarshal([]byte("name: 'x'\nserver: {\n\thost: 'a',\n\tbogus: 1,\n}\n"), &config, RejectUnknownKeys())
		checkError(t, err, 4, 2, "unknown key bogus; a Go value of type soml.decodeServer has no field for it")
	})

	t.Run("an ignored field is unknown", func(t *testing.T) {
		var config decodeConfig
		err := Unmarshal([]byte("Internal: 'x'\n"), &config, RejectUnknownKeys())
		checkError(t, err, 1, 1, "unknown key Internal; a Go value of type soml.decodeConfig has no field for it")
	})

	t.Run("a map takes every key", func(t *testing.T) {
		var value struct {
			Limits map[string]int `soml:"limits"`
		}

		if err := Unmarshal([]byte("limits: {anything: 1}\n"), &value, RejectUnknownKeys()); err != nil {
			t.Fatal(err)
		}

		var members map[string]any

		if err := Unmarshal([]byte("anything: 1\n"), &members, RejectUnknownKeys()); err != nil {
			t.Fatal(err)
		}
	})
}

// upperKey is a map key that decodes itself from text, in uppercase.
type upperKey string

var errBadKey = errors.New("this key is not allowed")

func (key *upperKey) UnmarshalText(text []byte) error {
	if string(text) == "bad" {
		return errBadKey
	}

	*key = upperKey(strings.ToUpper(string(text)))
	return nil
}

func TestDecodeMaps(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "string keys", run: decodeInto[map[string]int]("{a: 1, 'b c': 2}"), expected: map[string]int{"a": 1, "b c": 2}},
		{name: "string kind keys", run: decodeInto[map[byteName]int]("{a: 1}"), expected: map[byteName]int{"a": 1}},
		{name: "int keys", run: decodeInto[map[int]string]("{404: 'not found', -1: 'negative', 0: 'zero'}"), expected: map[int]string{404: "not found", -1: "negative", 0: "zero"}},
		{name: "int8 keys", run: decodeInto[map[int8]bool]("{127: true, -128: false}"), expected: map[int8]bool{127: true, -128: false}},
		{name: "uint64 keys", run: decodeInto[map[uint64]int]("{18446744073709551615: 1}"), expected: map[uint64]int{math.MaxUint64: 1}},
		{name: "integer kind keys", run: decodeInto[map[port]string]("{8080: 'http'}"), expected: map[port]string{8080: "http"}},
		{name: "text keys", run: decodeInto[map[upperKey]int]("{a: 1, b: 2}"), expected: map[upperKey]int{"A": 1, "B": 2}},
		{name: "empty", run: decodeInto[map[string]int]("{}"), expected: map[string]int{}},
		{name: "values of any", run: decodeInto[map[string]any]("{a: 1, b: [true]}"), expected: map[string]any{"a": int64(1), "b": []any{true}}},
		{name: "null values", run: decodeInto[map[string]*int]("{a: null}"), expected: map[string]*int{"a": nil}},
		{name: "an array", run: decodeInto[map[string]int]("[]"), message: "cannot decode an array into a Go value of type map[string]int"},
	})

	keyErrors := []struct {
		name    string
		run     func(string) error
		key     string
		message string
	}{
		{"leading zero", decodeKey[map[int]string], "0404", "expected the key “0404” to be an integer in decimal that fits in int, like 404"},
		{"hexadecimal", decodeKey[map[int]string], "0x194", "expected the key “0x194” to be an integer in decimal that fits in int, like 404"},
		{"plus sign", decodeKey[map[int]string], "'+5'", "expected the key “+5” to be an integer in decimal that fits in int, like 404"},
		{"negative zero", decodeKey[map[int]string], "-0", "expected the key “-0” to be an integer in decimal that fits in int, like 404"},
		{"underscore", decodeKey[map[int]string], "1_000", "expected the key “1_000” to be an integer in decimal that fits in int, like 404"},
		{"space", decodeKey[map[int]string], "' 1'", "expected the key “ 1” to be an integer in decimal that fits in int, like 404"},
		{"empty", decodeKey[map[int]string], "''", "expected the key “” to be an integer in decimal that fits in int, like 404"},
		{"word", decodeKey[map[int]string], "abc", "expected the key “abc” to be an integer in decimal that fits in int, like 404"},
		{"too large for int8", decodeKey[map[int8]string], "128", "expected the key “128” to be an integer in decimal that fits in int8, like 404"},
		{"too large for int64", decodeKey[map[int64]string], "9223372036854775808", "expected the key “9223372036854775808” to be an integer in decimal that fits in int64, like 404"},
		{"negative for uint", decodeKey[map[uint]string], "-1", "expected the key “-1” to be an integer in decimal that fits in uint, like 404"},
		{"leading zero for uint", decodeKey[map[uint]string], "01", "expected the key “01” to be an integer in decimal that fits in uint, like 404"},
		{"too large for uint8", decodeKey[map[uint8]string], "256", "expected the key “256” to be an integer in decimal that fits in uint8, like 404"},
		{"float keys", decodeKey[map[float64]string], "1", "cannot decode a key into a Go map key of type float64; a key must be a string, an integer, or have an UnmarshalText method"},
		{"bool keys", decodeKey[map[bool]string], "true", "cannot decode a key into a Go map key of type bool; a key must be a string, an integer, or have an UnmarshalText method"},
	}

	for _, testCase := range keyErrors {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.run(testCase.key)
			checkError(t, err, 2, 2, testCase.message)
		})
	}

	t.Run("an UnmarshalText error of a key", func(t *testing.T) {
		err := decodeKey[map[upperKey]int]("bad")
		checkError(t, err, 2, 2, "UnmarshalText of soml.upperKey: this key is not allowed")

		if !errors.Is(err, errBadKey) {
			t.Errorf("the error does not wrap the cause: %v", err)
		}
	})

	t.Run("entries are merged", func(t *testing.T) {
		servers := map[string]decodeServer{
			"a": {Host: "a-host", Port: 1},
			"b": {Host: "b-host", Port: 2},
		}

		if err := Unmarshal([]byte("a: {port: 10}\nc: {host: 'c-host'}\n"), &servers); err != nil {
			t.Fatal(err)
		}

		expected := map[string]decodeServer{
			"a": {Host: "a-host", Port: 10},
			"b": {Host: "b-host", Port: 2},
			"c": {Host: "c-host"},
		}

		if !reflect.DeepEqual(servers, expected) {
			t.Fatalf("got %#v", servers)
		}
	})

	t.Run("entries behind pointers are merged", func(t *testing.T) {
		existing := &decodeServer{Host: "a-host", Port: 1}
		servers := map[string]*decodeServer{"a": existing}

		if err := Unmarshal([]byte("a: {port: 10}\n"), &servers); err != nil {
			t.Fatal(err)
		}

		if servers["a"] != existing || *existing != (decodeServer{Host: "a-host", Port: 10}) {
			t.Fatalf("got %#v", servers["a"])
		}
	})

	t.Run("two keys that give one Go key merge, also in a map that was nil", func(t *testing.T) {
		data := []byte("'2001:db8::1': {host: 'a'}\n'2001:DB8::1': {port: 2}\n")
		expected := map[netip.Addr]decodeServer{netip.MustParseAddr("2001:db8::1"): {Host: "a", Port: 2}}
		var fromNil map[netip.Addr]decodeServer
		fromEmpty := map[netip.Addr]decodeServer{}

		for _, servers := range []*map[netip.Addr]decodeServer{&fromNil, &fromEmpty} {
			if err := Unmarshal(data, servers); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(*servers, expected) {
				t.Errorf("got %#v", *servers)
			}
		}
	})

	t.Run("a nil map is made", func(t *testing.T) {
		var value struct {
			Limits map[string]int `soml:"limits"`
		}

		if err := Unmarshal([]byte("limits: {cpu: 1}\n"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value.Limits, map[string]int{"cpu": 1}) {
			t.Fatalf("got %#v", value.Limits)
		}
	})

	t.Run("the position of a value", func(t *testing.T) {
		var value map[string]int
		err := Unmarshal([]byte("a: 1\nb: 'x'\n"), &value)
		checkError(t, err, 2, 4, "cannot decode the string “x” into a Go value of type int")
	})
}

// decodeKey decodes an object with one member, whose key is on line 2, column 2, into a map type.
func decodeKey[T any](key string) error {
	var value T
	return Unmarshal([]byte("{\n\t"+key+": 'x',\n}\n"), &value)
}

func TestDecodePointers(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "a pointer is made", run: decodeInto[*int]("1"), expected: new(1)},
		{name: "a pointer to a pointer", run: decodeInto[**string]("'a'"), expected: new(new("a"))},
		{name: "null into a pointer", run: decodeInto[*int]("null"), expected: (*int)(nil)},
		{name: "a pointer to a struct", run: decodeInto[*decodeServer]("{port: 1}"), expected: &decodeServer{Port: 1}},
	})

	t.Run("an existing pointer is decoded into", func(t *testing.T) {
		number := 1
		pointer := &number

		value := struct {
			Number **int `soml:"number"`
		}{Number: &pointer}

		if err := Unmarshal([]byte("number: 2\n"), &value); err != nil {
			t.Fatal(err)
		}

		if *value.Number != pointer || number != 2 {
			t.Fatalf("got %d through %p, want 2 through %p", **value.Number, *value.Number, pointer)
		}
	})
}

type stringer struct {
	text string
}

func (value *stringer) String() string {
	return value.text
}

func TestDecodeInterfaces(t *testing.T) {
	t.Run("an interface that holds a pointer is decoded into", func(t *testing.T) {
		server := &decodeServer{Host: "kept"}
		var value any = server

		if err := Unmarshal([]byte("port: 1\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value != any(server) || *server != (decodeServer{Host: "kept", Port: 1}) {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("a field that holds a pointer", func(t *testing.T) {
		number := 1

		value := struct {
			Value any `soml:"value"`
		}{Value: &number}

		if err := Unmarshal([]byte("value: 2\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Value != any(&number) || number != 2 {
			t.Fatalf("got %#v, number %d", value.Value, number)
		}
	})

	t.Run("an interface that holds a value is replaced", func(t *testing.T) {
		value := struct {
			Value any `soml:"value"`
		}{Value: decodeServer{Host: "replaced"}}

		if err := Unmarshal([]byte("value: {port: 1}\n"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value.Value, map[string]any{"port": int64(1)}) {
			t.Fatalf("got %#v", value.Value)
		}
	})

	t.Run("null into an interface that holds a pointer", func(t *testing.T) {
		number := 1

		value := struct {
			Value any `soml:"value"`
		}{Value: &number}

		if err := Unmarshal([]byte("value: null\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Value != nil || number != 1 {
			t.Fatalf("got %#v, number %d", value.Value, number)
		}
	})

	t.Run("a nil interface with methods", func(t *testing.T) {
		_, err := decodeMemberInto[fmt.Stringer]("'x'")
		checkError(t, err, 1, 8, "cannot decode the string “x” into a nil interface of type fmt.Stringer, because there is no concrete type to make")

		value, err := decodeMemberInto[fmt.Stringer]("null")

		if err != nil || value != nil {
			t.Fatalf("null into a nil fmt.Stringer: %#v, %v", value, err)
		}
	})

	t.Run("an interface with methods that holds a pointer", func(t *testing.T) {
		target := &stringer{}

		value := struct {
			Value fmt.Stringer `soml:"value"`
		}{Value: target}

		if err := Unmarshal([]byte("value: {}\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Value != fmt.Stringer(target) {
			t.Fatalf("got %#v", value.Value)
		}
	})
}

// recorder keeps the value that UnmarshalSOML gets.
type recorder struct {
	value  any
	called bool
}

func (recorder *recorder) UnmarshalSOML(value any) error {
	recorder.value = value
	recorder.called = true
	return nil
}

type failingUnmarshaler struct{}

var errUnmarshal = errors.New("this value is not allowed")

func (*failingUnmarshaler) UnmarshalSOML(any) error {
	return errUnmarshal
}

// bothMethods has UnmarshalSOML and UnmarshalText, and UnmarshalSOML wins.
type bothMethods struct {
	method string
}

func (value *bothMethods) UnmarshalSOML(any) error {
	value.method = "UnmarshalSOML"
	return nil
}

func (value *bothMethods) UnmarshalText([]byte) error {
	value.method = "UnmarshalText"
	return nil
}

func TestDecodeUnmarshaler(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		expected any
	}{
		{"object", "{a: 1, b: [1.5, 'x'], c: {d: true}}", map[string]any{"a": int64(1), "b": []any{1.5, "x"}, "c": map[string]any{"d": true}}},
		{"array", "[1, null]", []any{int64(1), nil}},
		{"int", "0xFF", int64(255)},
		{"float", "1.0", 1.0},
		{"string", "'text'", "text"},
		{"bool", "false", false},
		{"null", "null", nil},
		{"instant", "2026-09-19T16:00:00+02:00", time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)},
		{"duration", "1.5h", 90 * time.Minute},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := decodeMemberInto[recorder](testCase.text)

			if err != nil {
				t.Fatal(err)
			}

			if !value.called || !reflect.DeepEqual(value.value, testCase.expected) {
				t.Fatalf("UnmarshalSOML got %#v (%T), want %#v (%T)", value.value, value.value, testCase.expected, testCase.expected)
			}
		})
	}

	t.Run("a nil pointer to an Unmarshaler is set to nil for null", func(t *testing.T) {
		value, err := decodeMemberInto[*recorder]("null")

		if err != nil || value != nil {
			t.Fatalf("got %#v, %v", value, err)
		}
	})

	t.Run("in a slice and a map", func(t *testing.T) {
		items, err := decodeMemberInto[[]recorder]("[1, 'a']")

		if err != nil {
			t.Fatal(err)
		}

		if len(items) != 2 || items[0].value != int64(1) || items[1].value != "a" {
			t.Fatalf("got %#v", items)
		}

		members, err := decodeMemberInto[map[string]recorder]("{a: true}")

		if err != nil {
			t.Fatal(err)
		}

		if members["a"].value != true {
			t.Fatalf("got %#v", members)
		}
	})

	t.Run("an error is wrapped at the position of the value", func(t *testing.T) {
		var value struct {
			Second struct {
				Value failingUnmarshaler `soml:"value"`
			} `soml:"second"`
		}

		err := Unmarshal([]byte("first: 1\nsecond: {\n\tvalue: [1, 2],\n}\n"), &value)
		checkError(t, err, 3, 9, "UnmarshalSOML of soml.failingUnmarshaler: this value is not allowed")

		if !errors.Is(err, errUnmarshal) {
			t.Errorf("errors.Is does not find the cause: %v", err)
		}

		if somlError, ok := errors.AsType[*Error](err); !ok || somlError.Unwrap() != errUnmarshal || somlError.Err != errUnmarshal {
			t.Errorf("Unwrap does not give the cause: %#v", err)
		}

		if expected := "soml: line 3, column 9: UnmarshalSOML of soml.failingUnmarshaler: this value is not allowed"; err.Error() != expected {
			t.Errorf("Error() = %q, want %q", err.Error(), expected)
		}
	})

	t.Run("an Unmarshaler wins over UnmarshalText", func(t *testing.T) {
		value, err := decodeMemberInto[bothMethods]("'text'")

		if err != nil {
			t.Fatal(err)
		}

		if value.method != "UnmarshalSOML" {
			t.Fatalf("%s was called", value.method)
		}
	})
}

// textValue decodes itself from text.
type textValue struct {
	text string
}

var errBadText = errors.New("bad text")

func (value *textValue) UnmarshalText(text []byte) error {
	if string(text) == "bad" {
		return errBadText
	}

	value.text = "text:" + string(text)
	return nil
}

// textByte is a byte that decodes itself from text, so that a slice of it is not base64.
type textByte uint8

func (value *textByte) UnmarshalText(text []byte) error {
	*value = textByte(len(text))
	return nil
}

func TestDecodeTextUnmarshaler(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "string", run: decodeInto[textValue]("'hello'"), expected: textValue{text: "text:hello"}},
		{name: "pointer", run: decodeInto[*textValue]("'hello'"), expected: &textValue{text: "text:hello"}},
		{name: "slice", run: decodeInto[[]textValue]("['a', 'b']"), expected: []textValue{{"text:a"}, {"text:b"}}},
		{name: "a slice of text bytes is not base64", run: decodeInto[[]textByte]("['a', 'abc']"), expected: []textByte{1, 3}},
		{name: "a string into a slice of text bytes", run: decodeInto[[]textByte]("'YQ=='"), message: "cannot decode the string “YQ==” into a Go value of type []soml.textByte"},
		{name: "standard library type", run: decodeInto[netip.Addr]("'127.0.0.1'"), expected: netip.MustParseAddr("127.0.0.1")},
		{name: "an int", run: decodeInto[textValue]("1"), message: "cannot decode the int 1 into a Go value of type soml.textValue"},
		{name: "an object", run: decodeInto[textValue]("{}"), message: "cannot decode an object into a Go value of type soml.textValue"},
		{name: "an error", run: decodeInto[textValue]("'bad'"), message: "UnmarshalText of soml.textValue: bad text"},
		{name: "a standard library error", run: decodeInto[netip.Addr]("'nope'"), message: `UnmarshalText of netip.Addr: ParseAddr("nope"): unable to parse IP`},
	})

	_, err := decodeMemberInto[textValue]("'bad'")

	if !errors.Is(err, errBadText) {
		t.Errorf("errors.Is does not find the cause: %v", err)
	}
}

func TestDecodeOut(t *testing.T) {
	cases := []struct {
		name    string
		out     any
		message string
	}{
		{"nil", nil, "Unmarshal needs a non-nil pointer, not nil"},
		{"a map", map[string]any{}, "Unmarshal needs a non-nil pointer, not a value of type map[string]interface {}"},
		{"a struct", decodeServer{}, "Unmarshal needs a non-nil pointer, not a value of type soml.decodeServer"},
		{"a nil pointer", (*decodeServer)(nil), "Unmarshal needs a non-nil pointer, not a value of type *soml.decodeServer"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := Unmarshal([]byte("host: 'a'\n"), testCase.out)
			checkError(t, err, 0, 0, testCase.message)

			if expected := "soml: " + testCase.message; err.Error() != expected {
				t.Errorf("Error() = %q, want %q", err.Error(), expected)
			}
		})
	}
}

type unknownOption struct {
	Name string `soml:"name,omitzeor"`
}

type unsupportedOption struct {
	Port int `soml:"port,string"`
}

type duplicateOption struct {
	Name string `soml:"name,omitzero,omitzero"`
}

type duplicateEmptyOption struct {
	Name string `soml:"name,omitempty,omitzero,omitempty"`
}

type badUTF8TagKey struct {
	Name string "soml:\"\\xff\""
}

type taggedUnexported struct {
	name string `soml:"name"`
}

type unexportedInt int

type taggedUnexportedEmbedded struct {
	unexportedInt `soml:"n"`
}

type ignoredUnexported struct {
	Name string `soml:"name"`
	note string `soml:"-"`
}

type badTagKey struct {
	Name string `soml:"a\rb"`
}

// tagErrors are the struct types with an invalid `soml` tag, with the error that Marshal and Unmarshal give for them.
var tagErrors = []struct {
	name    string
	value   any
	message string
}{
	{"unknown option", &unknownOption{}, `the soml tag of the field Name of soml.unknownOption has the unknown option "omitzeor"; the options are omitzero and omitempty`},
	{"unsupported option", &unsupportedOption{}, `the soml tag of the field Port of soml.unsupportedOption has the unknown option "string"; the options are omitzero and omitempty`},
	{"duplicate option", &duplicateOption{}, `the soml tag of the field Name of soml.duplicateOption has the option "omitzero" twice`},
	{"duplicate option among others", &duplicateEmptyOption{}, `the soml tag of the field Name of soml.duplicateEmptyOption has the option "omitempty" twice`},
	{"unexported field with a tag", &taggedUnexported{name: "unused"}, "the unexported field name of soml.taggedUnexported has a soml tag; only an exported field can be in SOML"},
	// The field is set so that staticcheck counts it as used: it exists only to name an unexported field in the error above.
	{"unexported embedded field that is not a struct with a tag", &taggedUnexportedEmbedded{unexportedInt: 1}, "the unexported field unexportedInt of soml.taggedUnexportedEmbedded has a soml tag; only an exported field can be in SOML"},
	{"a key with a carriage return", &badTagKey{}, "the soml tag of the field Name of soml.badTagKey has a key that SOML cannot represent"},
	{"a key that is not UTF-8", &badUTF8TagKey{}, "the soml tag of the field Name of soml.badUTF8TagKey has a key that SOML cannot represent"},
}

func TestDecodeTagErrors(t *testing.T) {
	for _, testCase := range tagErrors {
		t.Run(testCase.name, func(t *testing.T) {
			err := Unmarshal([]byte("name: 'x'\n"), testCase.value)
			checkError(t, err, 0, 0, testCase.message)
		})
	}

	t.Run("nested", func(t *testing.T) {
		var value struct {
			Inner unknownOption `soml:"inner"`
		}

		err := Unmarshal([]byte("inner: {name: 'x'}\n"), &value)
		checkError(t, err, 0, 0, `the soml tag of the field Name of soml.unknownOption has the unknown option "omitzeor"; the options are omitzero and omitempty`)
	})

	t.Run("an unexported field with the tag “-” is fine", func(t *testing.T) {
		value := ignoredUnexported{note: "kept"}

		if err := Unmarshal([]byte("name: 'x'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value.Name != "x" || value.note != "kept" {
			t.Fatalf("got %#v", value)
		}
	})
}

func TestDecodeUnsupportedTypes(t *testing.T) {
	checkDecodeCases(t, []decodeCase{
		{name: "channel", run: decodeInto[chan int]("1"), message: "cannot decode into a Go value of type chan int, which SOML has no value for"},
		{name: "function", run: decodeInto[func()]("'f'"), message: "cannot decode into a Go value of type func(), which SOML has no value for"},
		{name: "complex", run: decodeInto[complex128]("1.0"), message: "cannot decode into a Go value of type complex128, which SOML has no value for"},
	})
}

func TestLineBreaksSeparateItemsInBrackets(t *testing.T) {
	valid := []struct {
		input    string
		expected any
	}{
		{"[\n80\n443\n]", []any{int64(80), int64(443)}},
		{"{\na: 1\nb: 2\n}", map[string]any{"a": int64(1), "b": int64(2)}},
		{"[1, 2\n3]", []any{int64(1), int64(2), int64(3)}},
		{"[1,\n2]", []any{int64(1), int64(2)}},
		{"[1, # c\n2]", []any{int64(1), int64(2)}},
		{"[1 /* a\n */, 2]", []any{int64(1), int64(2)}},
		{"[1\n\n\t2]", []any{int64(1), int64(2)}},
		{"[1,]", []any{int64(1)}},
		{"[1,\n]", []any{int64(1)}},
		{"[1 # x\n2]", []any{int64(1), int64(2)}},
		{"[1 /* x */\n2]", []any{int64(1), int64(2)}},
		{"[1\n/* x */ 2]", []any{int64(1), int64(2)}},
		{"{a:\n1}", map[string]any{"a": int64(1)}},
		{"a: {\n\tb: [\n\t\t1\n\t\t'x'\n\t]\n\tc: {}\n}", map[string]any{"a": map[string]any{"b": []any{int64(1), "x"}, "c": map[string]any{}}}},
	}

	for _, testCase := range valid {
		var value any

		if err := Unmarshal([]byte(testCase.input), &value); err != nil {
			t.Errorf("Unmarshal(%q): %v", testCase.input, err)
			continue
		}

		if !reflect.DeepEqual(value, testCase.expected) {
			t.Errorf("Unmarshal(%q) = %#v, want %#v", testCase.input, value, testCase.expected)
		}
	}

	invalid := []struct {
		input        string
		line, column int
		message      string
	}{
		{"[1 2]", 1, 4, "expected “,”, a line break, or “]” after an array item, but found “2”"},
		{"{a: 1 b: 2}", 1, 7, "expected “,”, a line break, or “}” after an object member, but found “b”"},
		{"[1 /*\n*/ 2]", 2, 4, "expected “,”, a line break, or “]” after an array item, but found “2”. A line break inside a block comment does not separate items"},
		{"[,1]", 1, 2, "expected a value, but found “,”"},
		{"[1,,2]", 1, 4, "expected a value, but found “,”"},
		{"[1\n,,2]", 2, 1, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma"},
		{"[1\n,\n2]", 2, 1, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma"},
		{"[1\n,]", 2, 1, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma"},
		{"[1 # c\n, 2]", 2, 1, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma"},
		{"{a: 1\n\t, b: 2}", 2, 2, "a comma must be on the same line as the item before it. The line break already separates the items, so remove the comma"},
		{"[1,\n,2]", 2, 1, "expected a value, but found “,”"},
		{"[,]", 1, 2, "expected a value, but found “,”"},
		{"{a:\nb: 2}", 2, 1, "expected a value, but found the key b"},
		{"a: 1, b: 2", 1, 5, "top-level entries are separated by line breaks, not commas. Use braces for a one-line object"},
		{"a: 1\n, b: 2", 2, 1, "top-level entries are separated by line breaks, not commas. Use braces for a one-line object"},
		{"a:\n'b c': 2", 2, 1, "expected a value, but found the key \"b c\""},
		{"a:\nb.c: 2", 2, 1, "unexpected “b”. A string value must be quoted, as in 'b.c: 2'"},
		{"a:\nb:", 2, 1, "expected a value, but found the key b"},
		{"a:\n'b.c': 1", 2, 1, "expected a value, but found the key \"b.c\""},
		{"a:\n404: 'x'", 2, 1, "expected a value, but found the key 404"},
		{"a:\n1.5: 2", 2, 1, "invalid number “1.5:”"},
		{"a:\n-1: 2", 2, 1, "expected a value, but found the key -1"},
		{"a:\ntrue: 1", 2, 1, "expected a value, but found the key true"},
		{"a:\ninfinity:\t1", 2, 1, "expected a value, but found the key infinity"},
		{"a:\n2026-01-01: 1", 2, 1, "expected a value, but found the key 2026-01-01"},
		{"a:\n1h: 1", 2, 1, "expected a value, but found the key 1h"},
		{"{a:\n'b': 1}", 2, 1, "expected a value, but found the key b"},
		{"a:\n" + strings.Repeat("b", 100) + ": 1", 2, 1, "expected a value, but found the key " + strings.Repeat("b", 40) + "…"},
		{strings.Repeat("b", 100) + ": 1\n" + strings.Repeat("b", 100) + ": 2", 2, 1, "duplicate key " + strings.Repeat("b", 40) + "…"},
	}

	for _, testCase := range invalid {
		var value any
		err := Unmarshal([]byte(testCase.input), &value)

		if err == nil {
			t.Errorf("Unmarshal(%q) accepted the document", testCase.input)
			continue
		}

		somlError, ok := errors.AsType[*Error](err)

		if !ok || somlError.Line != testCase.line || somlError.Column != testCase.column || somlError.Message != testCase.message {
			t.Errorf("Unmarshal(%q):\n got %v\nwant %d:%d %q", testCase.input, err, testCase.line, testCase.column, testCase.message)
		}
	}
}

func TestDecodeErrorPositions(t *testing.T) {
	t.Run("a nested value", func(t *testing.T) {
		var config decodeConfig
		err := Unmarshal([]byte("name: 'x'\nserver: {\n\thost: 'h',\n\tport: 'eighty',\n}\n"), &config)
		checkError(t, err, 4, 8, "cannot decode the string “eighty” into a Go value of type int")

		if expected := "soml: line 4, column 8: cannot decode the string “eighty” into a Go value of type int"; err.Error() != expected {
			t.Errorf("Error() = %q, want %q", err.Error(), expected)
		}
	})

	t.Run("columns count Unicode scalar values", func(t *testing.T) {
		source := "{host: 'éé😀', port: 'x'}"
		var server decodeServer
		err := Unmarshal([]byte(source), &server)
		offset := strings.Index(source, "'x'")
		checkError(t, err, 1, utf8.RuneCountInString(source[:offset])+1, "cannot decode the string “x” into a Go value of type int")

		if somlError, ok := errors.AsType[*Error](err); !ok || somlError.Offset != offset {
			t.Errorf("offset %d, want %d", somlError.Offset, offset)
		}
	})

	t.Run("a long string is shortened", func(t *testing.T) {
		_, err := decodeMemberInto[int]("'" + strings.Repeat("a", 100) + "'")
		checkError(t, err, 1, 8, "cannot decode the string “"+strings.Repeat("a", 40)+"…” into a Go value of type int")
	})

	t.Run("control characters in a message are replaced", func(t *testing.T) {
		_, err := decodeMemberInto[int](`"a\u{1b}[31mb\u{202e}\tc"`)
		checkError(t, err, 1, 8, "cannot decode the string “a\uFFFD[31mb\uFFFD\tc” into a Go value of type int")
	})

	t.Run("the document is checked before it is decoded", func(t *testing.T) {
		var server decodeServer
		err := Unmarshal([]byte("host: 1\nignored: [1,\n"), &server)
		checkError(t, err, 2, 10, "unterminated array: expected “]”")
	})
}

// A Go value can go through many interfaces that hold pointers in total, as long as no node of the document goes through more than the limit in a row.
func TestDecodeManyInterfacesThatHoldPointersAreNotACycle(t *testing.T) {
	type deep struct {
		Value any `soml:"v"`
	}

	var root any
	target := &root
	source := ""

	for range 60 {
		next := &deep{}
		var pointer any = next
		*target = &pointer
		target = &next.Value
		source += "v: {"
	}

	source = strings.TrimSuffix(source, "v: {") + "v: 1" + strings.Repeat("}", 59)

	if err := Unmarshal([]byte(source), &root); err != nil {
		t.Fatal(err)
	}
}

// The type caches are shared, so Marshal and Unmarshal must be safe to call from many goroutines at once. Run with -race.
func TestConcurrentUse(t *testing.T) {
	type inner struct {
		Values []float64 `soml:"values"`
	}

	type config struct {
		Name  string           `soml:"name"`
		Inner inner            `soml:"inner"`
		Map   map[string]inner `soml:"map"`
	}

	data := []byte("name: 'x'\ninner: {values: [1.5, 2.0]}\nmap: {a: {values: [3.0]}}\n")
	var group sync.WaitGroup

	for range 16 {
		group.Go(func() {
			for range 200 {
				var value config

				if err := Unmarshal(data, &value); err != nil {
					t.Error(err)
					return
				}

				if _, err := Marshal(value); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}

	group.Wait()
}

func TestDecodeTagWithATrailingComma(t *testing.T) {
	type name struct {
		Value int `soml:"value,"`
	}

	type options struct {
		Value int `soml:"value,omitzero,"`
	}

	var first name
	var second options

	for _, target := range []any{&first, &second} {
		if err := Unmarshal([]byte("value: 1"), target); err == nil || !strings.Contains(err.Error(), "ends with a comma") {
			t.Errorf("%T: %v", target, err)
		}

		if _, err := Marshal(target); err == nil || !strings.Contains(err.Error(), "ends with a comma") {
			t.Errorf("Marshal %T: %v", target, err)
		}
	}
}

func TestDecodeNullIntoATypeWithAMethod(t *testing.T) {
	var value struct {
		Address netip.Addr `soml:"address"`
	}

	err := Unmarshal([]byte("address: null"), &value)

	if err == nil || err.Error() != "soml: line 1, column 10: cannot decode null into a Go value of type netip.Addr; use a pointer to accept null" {
		t.Fatalf("got %v", err)
	}
}

func TestErrorPath(t *testing.T) {
	type server struct {
		Port uint16 `soml:"port"`
	}

	type config struct {
		Servers []server          `soml:"servers"`
		Limits  map[string]uint16 `soml:"limits"`
		Codes   map[int]string    `soml:"codes"`
	}

	tests := []struct {
		name     string
		document string
		options  []Option
		path     []any
	}{
		{"an array item", "servers: [{port: 1}, {port: 70000}]", nil, []any{"servers", 1, "port"}},
		{"a nested key", "limits: {cpu: -1}", nil, []any{"limits", "cpu"}},
		{"a map key", "codes: {x: 'a'}", nil, []any{"codes", "x"}},
		{"an unknown key", "servers: [{port: 1, prot: 2}]", []Option{RejectUnknownKeys()}, []any{"servers", 0, "prot"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value config
			err := Unmarshal([]byte(test.document), &value, test.options...)
			somlError, ok := errors.AsType[*Error](err)

			if !ok || !reflect.DeepEqual(somlError.Path, test.path) {
				t.Fatalf("got %v (%#v), want the path %v", err, somlError, test.path)
			}

			// The path finds the same place in the syntax tree.
			document, parseError := Parse([]byte(test.document))

			if parseError != nil {
				t.Fatal(parseError)
			}

			if _, found := document.Get(test.path...); !found {
				t.Errorf("the path %v is not in the tree", test.path)
			}
		})
	}

	t.Run("a syntax error has no path", func(t *testing.T) {
		var value config
		err := Unmarshal([]byte("servers: [1,,]"), &value)

		if somlError, _ := errors.AsType[*Error](err); somlError == nil || somlError.Path != nil {
			t.Fatalf("got %#v", somlError)
		}
	})

	t.Run("an error from Marshal says its path", func(t *testing.T) {
		_, err := Marshal(map[string]any{"metrics": []any{1.5, map[string]any{"the ratio": math.NaN()}}})

		if err == nil || err.Error() != "soml: metrics[1].\"the ratio\": NaN is not a SOML value" {
			t.Fatalf("got %v", err)
		}
	})
}

type namedByte uint8

type selfPointer *selfPointer

func TestDecodeUnusualTypes(t *testing.T) {
	t.Run("a pointer map key with UnmarshalText", func(t *testing.T) {
		var value map[*big.Int]string

		if err := Unmarshal([]byte("5: 'x'"), &value); err != nil {
			t.Fatal(err)
		}

		for key, text := range value {
			if key.Int64() != 5 || text != "x" {
				t.Fatalf("got %v: %v", key, text)
			}
		}
	})

	t.Run("an empty interface map key", func(t *testing.T) {
		var value map[any]int

		if err := Unmarshal([]byte("a: 1"), &value); err != nil || value["a"] != 1 {
			t.Fatalf("got %v, %v", value, err)
		}
	})

	t.Run("an interface map key with methods is an error, not a panic", func(t *testing.T) {
		var value map[encoding.TextUnmarshaler]string

		if err := Unmarshal([]byte("a: 'x'"), &value); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("an array of a named byte type from base64", func(t *testing.T) {
		var value struct {
			Bytes [4]namedByte `soml:"bytes"`
		}

		if err := Unmarshal([]byte("bytes: 'AQIDBA=='"), &value); err != nil || value.Bytes != [4]namedByte{1, 2, 3, 4} {
			t.Fatalf("got %v, %v", value.Bytes, err)
		}

		data, err := Marshal(value)

		if err != nil || string(data) != "bytes: 'AQIDBA=='\n" {
			t.Fatalf("got %q, %v", data, err)
		}
	})

	t.Run("a zero Option does nothing", func(t *testing.T) {
		var value map[string]int

		if err := Unmarshal([]byte("a: 1"), &value, Option{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an interface that holds a pointer to itself is an error", func(t *testing.T) {
		var value any
		value = &value

		if err := Unmarshal([]byte("a: 1"), &value); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("a pointer type that points to itself is an error", func(t *testing.T) {
		var value selfPointer

		if err := Unmarshal([]byte("[1]"), &value); err == nil {
			t.Fatal("no error")
		}
	})

	t.Run("null into a slice with UnmarshalText is nil", func(t *testing.T) {
		value := struct {
			Address net.IP `soml:"address"`
		}{Address: net.IPv4(1, 2, 3, 4)}

		if err := Unmarshal([]byte("address: null"), &value); err != nil || value.Address != nil {
			t.Fatalf("got %v, %v", value.Address, err)
		}
	})

	t.Run("an embedded unexported struct with a key is an error", func(t *testing.T) {
		type inner struct {
			X int
		}

		var value struct {
			*inner `soml:"in"`
		}

		if err := Unmarshal([]byte("in: {X: 1}"), &value); err == nil || !strings.Contains(err.Error(), "unexported embedded field") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("an interface that holds a value says so", func(t *testing.T) {
		var value struct {
			Name fmt.Stringer `soml:"name"`
		}

		value.Name = net.IPv4(1, 2, 3, 4)
		err := Unmarshal([]byte("name: 'x'"), &value)

		if err == nil || !strings.Contains(err.Error(), "only a non-nil pointer or map in an interface is decoded into") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestDecodeMoreErrors(t *testing.T) {
	t.Run("a float too small for a float32", func(t *testing.T) {
		var value struct {
			Ratio float32 `soml:"ratio"`
		}

		err := Unmarshal([]byte("ratio: 1e-50"), &value)

		if err == nil || !strings.Contains(err.Error(), "too small for a Go value of type float32") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("the path of an item of a Go array", func(t *testing.T) {
		var value struct {
			Ports [2]uint8 `soml:"ports"`
		}

		err := Unmarshal([]byte("ports: [1, 300]"), &value)
		somlError, ok := errors.AsType[*Error](err)

		if !ok || !reflect.DeepEqual(somlError.Path, []any{"ports", 1}) {
			t.Fatalf("got %v", err)
		}
	})
}

// A document of many items that fail cannot make a large allocation before the first one fails.
func TestDecodeASliceGrowsAsItsItemsDecode(t *testing.T) {
	data := []byte("a: [" + strings.Repeat("0,", 100000) + "]")
	var value struct {
		A [][4096]byte `soml:"a"`
	}

	var statistics runtime.MemStats
	runtime.ReadMemStats(&statistics)
	before := statistics.TotalAlloc

	if err := Unmarshal(data, &value); err == nil {
		t.Fatal("no error")
	}

	runtime.ReadMemStats(&statistics)

	if bytes := statistics.TotalAlloc - before; bytes > 64<<20 {
		t.Fatalf("decoding allocated %d bytes", bytes)
	}
}

func TestDecodeSliceGrowthPreservesElements(t *testing.T) {
	type element struct {
		Value   *int
		Padding [4096]byte
	}

	// More than a megabyte of elements crosses the initial allocation limit.
	data := []byte("[" + strings.Repeat("{Value: 1}, {},", 150) + "]")
	var value []element

	if err := Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}

	if len(value) != 300 {
		t.Fatalf("got %d elements, want 300", len(value))
	}

	for index, item := range value {
		if index%2 == 0 {
			if item.Value == nil || *item.Value != 1 {
				t.Fatalf("element %d did not retain its value", index)
			}
		} else if item.Value != nil {
			t.Fatalf("element %d did not start at zero", index)
		}

		if item.Padding != [4096]byte{} {
			t.Fatalf("element %d has nonzero padding", index)
		}
	}

	original := new(9)
	value = []element{{Value: original}}
	invalid := strings.TrimSuffix(string(data), "]") + "{Value: 'bad'}]"

	if err := Unmarshal([]byte(invalid), &value); err == nil {
		t.Fatal("accepted a string as an integer")
	}

	if len(value) != 1 || value[0].Value != original || *original != 9 {
		t.Fatal("a failed decode changed the original slice")
	}
}

func TestErrorTextShortensALongPath(t *testing.T) {
	path := make([]any, 30)

	for index := range path {
		path[index] = "k"
	}

	somlError := &Error{Message: "problem", Path: path}

	if text := somlError.Error(); text != "soml: k.k.k.k.k.k.k.k.k.k.….k.k.k.k.k: problem" {
		t.Fatalf("got %q", text)
	}

	path[25] = 7

	if text := somlError.Error(); text != "soml: k.k.k.k.k.k.k.k.k.k.…[7].k.k.k.k: problem" {
		t.Fatalf("got %q", text)
	}
}

func TestDecodeIntoASyntaxTreeTypeIsAnError(t *testing.T) {
	var document Document
	var object *Object

	for _, target := range []any{&document, &object} {
		if err := Unmarshal([]byte("a: 1\n"), target); err == nil || !strings.Contains(err.Error(), "use Parse") {
			t.Errorf("%T: %v", target, err)
		}
	}

	// A duplicate key with a dot in it is written as JSON writes it, as in the JS reference.
	err := Unmarshal([]byte("'a.b': 1\n\"a.b\": 2\n"), new(any))

	if err == nil || err.Error() != `soml: line 2, column 1: duplicate key "a.b"` {
		t.Fatalf("got %v", err)
	}
}

func TestUnmarshalRead(t *testing.T) {
	t.Run("reads a document to its end", func(t *testing.T) {
		var server decodeServer

		if err := UnmarshalRead(strings.NewReader("host: 'a'\nport: 80\n"), &server); err != nil {
			t.Fatal(err)
		}

		if server.Host != "a" || server.Port != 80 {
			t.Fatalf("got %+v", server)
		}
	})

	t.Run("takes the options of Unmarshal", func(t *testing.T) {
		var server decodeServer
		err := UnmarshalRead(strings.NewReader("host: 'a'\nprot: 80\n"), &server, RejectUnknownKeys())
		checkError(t, err, 2, 1, "unknown key prot; a Go value of type soml.decodeServer has no field for it")
	})

	t.Run("an error from the reader is the cause", func(t *testing.T) {
		cause := errors.New("connection reset")
		var value any
		err := UnmarshalRead(io.MultiReader(strings.NewReader("a: 1\n"), iotest.ErrReader(cause)), &value)
		somlError, ok := errors.AsType[*Error](err)

		if !ok || !errors.Is(err, cause) || somlError.Line != 0 || somlError.Error() != "soml: cannot read the document: connection reset" {
			t.Fatalf("got %#v", err)
		}
	})

	t.Run("does not read when out is not a pointer", func(t *testing.T) {
		reader := strings.NewReader("a: 1\n")
		err := UnmarshalRead(reader, map[string]any{})

		if err == nil || err.Error() != "soml: UnmarshalRead needs a non-nil pointer, not a value of type map[string]interface {}" || reader.Len() != 5 {
			t.Fatalf("got %v, with %d bytes left", err, reader.Len())
		}
	})
}

// A literal at the midpoint of two float32 values, or just above it, decodes as strconv.ParseFloat rounds it to a float32, once.
func TestDecodeFloat32RoundsOnceNearAMidpoint(t *testing.T) {
	random := rand.New(rand.NewPCG(8, 9))

	for range 2000 {
		low := math.Float32frombits(random.Uint32() & 0x7F7FFFFF)
		high := math.Nextafter32(low, float32(math.Inf(1)))

		if math.IsInf(float64(high), 0) {
			continue
		}

		middle := new(big.Float).SetPrec(200).Add(big.NewFloat(float64(low)), big.NewFloat(float64(high)))
		middle.Quo(middle, big.NewFloat(2))

		// As `d.ddd…e±NN`, which SOML writes without the `+` and the leading zeros of the exponent.
		mantissa, exponent, _ := strings.Cut(middle.Text('e', 80), "e")
		exponent = strings.TrimPrefix(exponent, "+")
		isNegative := strings.HasPrefix(exponent, "-")
		exponent = strings.TrimLeft(strings.TrimPrefix(exponent, "-"), "0")

		if exponent == "" {
			exponent = "0"
		} else if isNegative {
			exponent = "-" + exponent
		}

		for _, literal := range []string{mantissa + "1e" + exponent, mantissa + "e" + exponent} {
			expected, _ := strconv.ParseFloat(literal, 32)
			var items []float32
			err := Unmarshal([]byte("["+literal+"]"), &items)

			if math.IsInf(expected, 0) || expected == 0 {
				if err == nil {
					t.Fatalf("%s: got %v, want an error", literal, items[0])
				}

				continue
			}

			if err != nil || items[0] != float32(expected) {
				t.Fatalf("%s: got %v %v, want %v", literal, items, err, float32(expected))
			}
		}
	}
}

// checkDocumentErrors checks the position and the message of the error for each document.
func checkDocumentErrors(t *testing.T, cases []documentError) {
	t.Helper()

	for _, testCase := range cases {
		t.Run(abbreviate(testCase.input, 40), func(t *testing.T) {
			var value any
			checkError(t, Unmarshal([]byte(testCase.input), &value), testCase.line, testCase.column, testCase.message)
		})
	}
}

type documentError struct {
	input        string
	line, column int
	message      string
}

// The hint to quote a key with spaces is only given for a `:` that ends a key, because quoting the words before any other `:` gives another meaning, or still no valid document.
func TestKeyWithSpacesHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"server localhost:8080", 1, 8, "expected “:” after the key, but found “l”"},
		{"time 12:30", 1, 6, "expected “:” after the key, but found “1”"},
		{"homepage https://example.com", 1, 10, "expected “:” after the key, but found “h”"},
		{"a b:c: 1", 1, 3, "expected “:” after the key, but found “b”"},
		{"{a b:c}", 1, 4, "expected “:” after the key, but found “b”"},
		{"the name: 1", 1, 1, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
		{"the name:\t1", 1, 1, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
		{"the name:\n\t1", 1, 1, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
		{"the name:", 1, 1, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
		{"the full name : 1", 1, 1, "a bare key cannot contain spaces. Quote it, as in 'the full name'"},
		{"{the name: 1}", 1, 2, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
		{"a: 1\nthe name: 2", 2, 1, "a bare key cannot contain spaces. Quote it, as in 'the name'"},
	})
}

// The suggestion for an unquoted string quotes all of it, up to the end of the line, a comma, a closing bracket, or a comment, so that it holds what was meant.
func TestUnquotedStringHintQuotesTheWholeValue(t *testing.T) {
	cases := []struct {
		input, text string
	}{
		{"name: John Smith", "John Smith"},
		{"name: John Smith # The owner", "John Smith"},
		{"name: John Smith /* The owner */", "John Smith"},
		{"a: [hello world\t/* note */]", "hello world"},
		{"a: src/*.js", "src/*.js"},
		{"border: 1px solid black", "1px solid black"},
		{"font: 12pt Arial # Body", "12pt Arial"},
		{"a: [1px solid, 2]", "1px solid"},
		{"a: 1px solid /* note */", "1px solid"},
		{"a: 1px", "1px"},
		{"name: John Smith \t\nisAdmin: true", "John Smith"},
		{"a: foo.bar", "foo.bar"},
		{"a: foo/bar", "foo/bar"},
		{"a: v1.2.3", "v1.2.3"},
		{"a: C# and F#", "C# and F#"},
		{"a: say \"hi\"", "say \"hi\""},
		{"a: [foo bar, true]", "foo bar"},
		{"a: [foo bar]", "foo bar"},
		{"a: {b: foo bar}", "foo bar"},
		{"a: {b: foo bar, c: true}", "foo bar"},
		{"a: [\n\tfoo bar\n]", "foo bar"},
		{"a: héllo wörld", "héllo wörld"},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			var value any
			err := Unmarshal([]byte(testCase.input), &value)
			somlError, ok := errors.AsType[*Error](err)

			if !ok || !strings.HasSuffix(somlError.Message, "A string value must be quoted, as in '"+testCase.text+"'") {
				t.Fatalf("got %v", err)
			}

			// With the text quoted as suggested, the document is valid and holds it.
			if err := Unmarshal([]byte(strings.Replace(testCase.input, testCase.text, "'"+testCase.text+"'", 1)), &value); err != nil || !strings.Contains(fmt.Sprint(value), testCase.text) {
				t.Fatalf("the suggestion gives %v, %v", value, err)
			}
		})
	}

	checkDocumentErrors(t, []documentError{
		// The word itself is still what is unexpected.
		{"name: John Smith", 1, 7, "unexpected “John”. A string value must be quoted, as in 'John Smith'"},
		{"a: hello", 1, 4, "unexpected “hello”. A string value must be quoted, as in 'hello'"},
		{"a: hello\n", 1, 4, "unexpected “hello”. A string value must be quoted, as in 'hello'"},
		// A `'...'` string cannot hold a `'`, so then there is no simple suggestion.
		{"a: it's here", 1, 4, "unexpected “it”. A string value must be quoted"},
		{"a: 3x it's", 1, 4, "invalid number “3x”. A string value must be quoted"},
		// A number with more text after it suggests quoting all of it, and the octal suggestion is only for the number on its own.
		{"border: 1px solid black", 1, 9, "invalid number “1px”. A string value must be quoted, as in '1px solid black'"},
		{"phone: 0412 345 678", 1, 8, "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '0412 345 678'"},
		{"date: 01/02/2026", 1, 7, "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '01/02/2026'"},
		{"mode: 0644", 1, 7, "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o644, and an identifier, such as a ZIP code, as a string: '0644'"},
		{"mode: 0644 # Owner only", 1, 7, "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o644, and an identifier, such as a ZIP code, as a string: '0644'"},
		{"a: [0644, 1]", 1, 5, "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o644, and an identifier, such as a ZIP code, as a string: '0644'"},
		// A long value is cut short in the suggestion.
		{"a: " + strings.Repeat("word ", 1000), 1, 4, "unexpected “word”. A string value must be quoted, as in '" + strings.Repeat("word ", 8) + "…'"},
		{"a: x" + strings.Repeat("é", 2000), 1, 4, "unexpected “x”. A string value must be quoted, as in 'x" + strings.Repeat("é", 39) + "…'"},
	})
}

// A word after the `:` and the space of a member, as in a URL, is that member's value, an unquoted string. Anywhere else, a word directly followed by `:` is reported as a key, as in the JS reference.
func TestUnquotedStringWithAColonIsNotAKey(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"url: https://example.com", 1, 6, "unexpected “https”. A string value must be quoted, as in 'https://example.com'"},
		{"url: https://example.com/a?b=c # The API", 1, 6, "unexpected “https”. A string value must be quoted, as in 'https://example.com/a?b=c'"},
		{"host: localhost:8080", 1, 7, "unexpected “localhost”. A string value must be quoted, as in 'localhost:8080'"},
		{"a: {url: https://x, b: true}", 1, 10, "unexpected “https”. A string value must be quoted, as in 'https://x'"},
		{"a: x:y", 1, 4, "unexpected “x”. A string value must be quoted, as in 'x:y'"},
		{"a: x::", 1, 4, "unexpected “x”. A string value must be quoted, as in 'x::'"},
		// A word after the `:` and the space of its member is an unquoted string, also when whitespace or the end follows its own `:`.
		{"msg: Error: file not found", 1, 6, "unexpected “Error”. A string value must be quoted, as in 'Error: file not found'"},
		{"a: {msg: Error: x}", 1, 10, "unexpected “Error”. A string value must be quoted, as in 'Error: x'"},
		{"a: b: 1", 1, 4, "unexpected “b”. A string value must be quoted, as in 'b: 1'"},
		{"a:\tb:", 1, 4, "unexpected “b”. A string value must be quoted, as in 'b:'"},
		{"a: b:", 1, 4, "unexpected “b”. A string value must be quoted, as in 'b:'"},
		// Anywhere else, a key where a value should be is reported as a key, as when the value of an entry is left out, or in an array, even when it was meant as text.
		{"[b:]", 1, 2, "expected a value, but found the key b"},
		{"a:\nb:c", 2, 1, "expected a value, but found the key b"},
		{"a:\nb:1\nc: 2", 2, 1, "expected a value, but found the key b"},
		{"{a:\nb:1}", 2, 1, "expected a value, but found the key b"},
		{"[a:1]", 1, 2, "expected a value, but found the key a"},
		{"x: [a:b, c]", 1, 5, "expected a value, but found the key a"},
		{"a: [https://x, true]", 1, 5, "expected a value, but found the key https"},
		{"a:\nb: 1", 2, 1, "expected a value, but found the key b"},
		{"a:\nb:\t1", 2, 1, "expected a value, but found the key b"},
		{"a:\nb:", 2, 1, "expected a value, but found the key b"},
		{"a:\n\tb: 1", 2, 2, "expected a value, but found the key b. Indentation does not nest objects, so write a: {b: …}"},
		{"a: [\n\t3\n\tc: 5\n]", 3, 2, "expected a value, but found the key c"},
		{"a: [Note: x, true]", 1, 5, "expected a value, but found the key Note"},
		{"[b: 1]", 1, 2, "expected a value, but found the key b"},
		{"a: [1, b: 2]", 1, 8, "expected a value, but found the key b"},
	})
}

// A value that starts with `.` is only reported as a number when a digit follows, because `.env` is a string that `0.env` would not fix.
func TestValueStartingWithADotIsOnlyANumberBeforeADigit(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: .5", 1, 4, "a number cannot begin with “.”; write a digit before it, as in 0.5"},
		{"a: .5e3", 1, 4, "a number cannot begin with “.”; write a digit before it, as in 0.5"},
		{"a: .env", 1, 4, "unexpected “.”. A string value must be quoted, as in '.env'"},
		{"a: ./foo # The path", 1, 4, "unexpected “.”. A string value must be quoted, as in './foo'"},
		{"files: [.env, .git]", 1, 9, "unexpected “.”. A string value must be quoted, as in '.env'"},
		{"a: ...", 1, 4, "unexpected “.”. A string value must be quoted, as in '...'"},
		{"a: .", 1, 4, "unexpected “.”. A string value must be quoted, as in '.'"},
		{"a: .it's", 1, 4, "unexpected “.”. A string value must be quoted"},
		{"a: -.5", 1, 4, "a number cannot begin with “.”; write a digit before it, as in -0.5"},
	})
}

// A keyword hint is only given for a word on its own, because following it for longer text, as in `Yes please` or `Nan Goldin`, would leave text behind or change the value.
func TestKeywordHintIsOnlyForAWordOnItsOwn(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: Yes", 1, 4, "“Yes” is not a value. Booleans are written true and false, in lowercase"},
		{"a: TRUE\n", 1, 4, "“TRUE” is not a value. Booleans are written true and false, in lowercase"},
		{"a: None # Unset", 1, 4, "“None” is not a value. Null is written null, in lowercase"},
		{"a: [NaN, 1]", 1, 5, "NaN is not representable. Use null for a missing value"},
		{"a: Inf", 1, 4, "“Inf” is not a value. Infinity is written infinity, in lowercase"},
		{"a: {b: nil}", 1, 8, "“nil” is not a value. Null is written null, in lowercase"},
		{"a: Yes please", 1, 4, "unexpected “Yes”. A string value must be quoted, as in 'Yes please'"},
		{"status: On hold", 1, 9, "unexpected “On”. A string value must be quoted, as in 'On hold'"},
		{"a: None of the above", 1, 4, "unexpected “None”. A string value must be quoted, as in 'None of the above'"},
		{"name: Nan Goldin", 1, 7, "unexpected “Nan”. A string value must be quoted, as in 'Nan Goldin'"},
		{"a: [Inf loop, 1]", 1, 5, "unexpected “Inf”. A string value must be quoted, as in 'Inf loop'"},
		{"a: no.", 1, 4, "unexpected “no”. A string value must be quoted, as in 'no.'"},
	})
}

// Every string that an error suggests to quote, as in `as in 'hello world'`, gives a valid document when it is quoted, and the value holds that text. The same holds for a key that an error suggests to quote.
func TestQuotingSuggestionsGiveTheMeantValue(t *testing.T) {
	words := []string{"a", "Yes", "None", "nan", "Inf", "1px", "0412", "0644", "0xff", "2026-02-30", "https", "://x", ":", ": ", ".env", ".", "..", "/", "*", "#", " #", "/*", " /* c */", " ", "\t", "-", "-x", "1", "1.5", "e5", "é", "it's", "\"", "\\", "1h", "x:y", "true", "C#"}
	random := rand.New(rand.NewPCG(3, 7))
	checked := 0

	for range 20000 {
		var builder strings.Builder

		for range 1 + random.IntN(4) {
			builder.WriteString(words[random.IntN(len(words))])
		}

		text := builder.String()

		for _, prefix := range []string{"a: ", "a: [", "a: {b: "} {
			input := prefix + text
			var value any
			err := Unmarshal([]byte(input), &value)
			somlError, ok := errors.AsType[*Error](err)

			if !ok {
				continue
			}

			_, suggestion, isSuggested := strings.Cut(somlError.Message, "A string value must be quoted, as in '")

			if !isSuggested || strings.HasSuffix(suggestion, "…'") {
				continue
			}

			suggestion = strings.TrimSuffix(suggestion, "'")
			start := somlError.Offset

			if !strings.HasPrefix(input[start:], suggestion) {
				t.Errorf("%q suggests '%s', which is not the text at the error", input, suggestion)
				continue
			}

			quoted := input[:start] + "'" + suggestion + "'" + input[start+len(suggestion):]

			// The rest of a document can have other errors, so the suggestion is checked on its own, with the text that ended it.
			for _, end := range []string{"", "]", "}", "]}"} {
				if err := Unmarshal([]byte(quoted+end), &value); err == nil {
					if !strings.Contains(fmt.Sprint(value), suggestion) {
						t.Errorf("%q suggests '%s', which gives %v", input, suggestion, value)
					}

					checked++
					break
				}
			}
		}
	}

	if checked < 1000 {
		t.Fatalf("only %d suggestions were checked", checked)
	}
}

func TestKeyQuotingSuggestionsGiveTheMeantKey(t *testing.T) {
	words := []string{"a", "b", "name", " ", "\t", "  ", "localhost", ":", ": ", ":\t", ":8080", "://x", "1", "12:30", "-", "_", ".", "'", "é"}
	random := rand.New(rand.NewPCG(5, 11))
	checked := 0

	for range 100000 {
		var builder strings.Builder

		for range 2 + random.IntN(5) {
			builder.WriteString(words[random.IntN(len(words))])
		}

		input := builder.String() + " 1"
		var value any
		err := Unmarshal([]byte(input), &value)
		somlError, ok := errors.AsType[*Error](err)

		if !ok {
			continue
		}

		_, key, isSuggested := strings.Cut(somlError.Message, "a bare key cannot contain spaces. Quote it, as in '")

		if !isSuggested {
			continue
		}

		key = strings.TrimSuffix(key, "'")
		rest, ok := strings.CutPrefix(strings.TrimLeft(input, " \t"), key)

		if !ok {
			t.Errorf("%q suggests '%s', which is not the start of the document", input, key)
			continue
		}

		rest = strings.TrimLeft(rest, " \t")

		if !strings.HasPrefix(rest, ":") || rest[1] != ' ' && rest[1] != '\t' {
			t.Errorf("%q suggests '%s', but no “:” that ends a key follows it", input, key)
		}

		checked++
	}

	if checked < 100 {
		t.Fatalf("only %d suggestions were checked", checked)
	}
}

// A `=` after a key was meant as the `:` of INI and TOML, so it is not a reason to quote the key.
func TestEqualsSignAfterAKeyIsNotAReasonToQuoteIt(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"name=foo", 1, 5, "expected “:” after the key, but found “=”"},
		{"port=8080", 1, 5, "expected “:” after the key, but found “=”"},
		{"{a=1}", 1, 3, "expected “:” after the key, but found “=”"},
		{"a = 1", 1, 3, "expected “:” after the key, but found “=”"},
		{"a=b: 1", 1, 2, "expected “:” after the key, but found “=”"},
		{"=: 1", 1, 1, "expected a key, but found “=”"},
		// Other characters directly after a key are still most likely part of it.
		{"a$: 1", 1, 2, "expected “:” after the key, but found “$”. A key that contains characters other than letters, digits, “_”, and “-” must be quoted"},
		{"$a=1", 1, 1, "expected a key, but found “$”. A key that contains characters other than letters, digits, “_”, and “-” must be quoted"},
	})
}

// A `.` after a key is an error at the `.`, because a key is never a path. Quoting the key and nesting with braces are only suggested for a bare key whose dots each have a word on both sides, so that both are valid.
func TestDotInKey(t *testing.T) {
	const general = "a key cannot contain “.” unless it is quoted. Quote the whole key, or use braces to nest, as in a: {b: …}"

	checkDocumentErrors(t, []documentError{
		{"a.b: 1", 1, 2, "a bare key cannot contain “.”. Quote it, as in 'a.b', or use braces to nest, as in a: {b: …}"},
		{"example.com: 1", 1, 8, "a bare key cannot contain “.”. Quote it, as in 'example.com', or use braces to nest, as in example: {com: …}"},
		{"3.14: 'x'", 1, 2, "a bare key cannot contain “.”. Quote it, as in '3.14', or use braces to nest, as in 3: {14: …}"},
		{"1.5s: 1", 1, 2, "a bare key cannot contain “.”. Quote it, as in '1.5s', or use braces to nest, as in 1: {5s: …}"},
		{"a.b.c: 1", 1, 2, "a bare key cannot contain “.”. Quote it, as in 'a.b.c', or use braces to nest, as in a: {b: {c: …}}"},
		{"{a.b: 1}", 1, 3, "a bare key cannot contain “.”. Quote it, as in 'a.b', or use braces to nest, as in a: {b: …}"},
		{"x: {a.b: 1}", 1, 6, "a bare key cannot contain “.”. Quote it, as in 'a.b', or use braces to nest, as in a: {b: …}"},
		{"[{a.b: 1}]", 1, 4, "a bare key cannot contain “.”. Quote it, as in 'a.b', or use braces to nest, as in a: {b: …}"},
		{"x: 1\nsocket.io: '^4'", 2, 7, "a bare key cannot contain “.”. Quote it, as in 'socket.io', or use braces to nest, as in socket: {io: …}"},
		{strings.Repeat("a", 50) + ".b: 1", 1, 51, "a bare key cannot contain “.”. Quote it, as in '" + strings.Repeat("a", 40) + "…', or use braces to nest, as in " + strings.Repeat("a", 40) + "…"},
		{"'a'.b: 1", 1, 4, general},
		{"\"a\".b: 1", 1, 4, general},
		{"a.'b': 1", 1, 2, general},
		{"a.: 1", 1, 2, general},
		{"a..b: 1", 1, 2, general},
		{"a.b c: 1", 1, 2, general},
		{"a.$b: 1", 1, 2, general},
		{"a.b", 1, 2, general},
		{".a: 1", 1, 1, "expected a key, but found “.”. A key that contains characters other than letters, digits, “_”, and “-” must be quoted, as in '.a'"},
		{".env: 1", 1, 1, "expected a key, but found “.”. A key that contains characters other than letters, digits, “_”, and “-” must be quoted, as in '.env'"},
		{"files: {.eslintrc.json: 1}", 1, 9, "expected a key, but found “.”. A key that contains characters other than letters, digits, “_”, and “-” must be quoted, as in '.eslintrc.json'"},
	})

	var value any

	if err := Unmarshal([]byte("'a.b': 1"), &value); err != nil || !reflect.DeepEqual(value, map[string]any{"a.b": int64(1)}) {
		t.Fatalf("a quoted key with a dot: %#v, %v", value, err)
	}
}

// A key that ends with a space character other than a space or a tab gets no quoting hint, and a character that is invisible, such as a default ignorable one, is shown by code point and is not a reason to quote the key.
func TestKeyQuotingHintCharacters(t *testing.T) {
	const hint = ". A key that contains characters other than letters, digits, “_”, and “-” must be quoted"

	checkDocumentErrors(t, []documentError{
		{"the name\u00a0: 1", 1, 5, "expected “:” after the key, but found “n”"},
		{"the name\u2028: 1", 1, 5, "expected “:” after the key, but found “n”"},
		{"the name\ufeff: 1", 1, 5, "expected “:” after the key, but found “n”"},
		{"$schema: 'x'", 1, 1, "expected a key, but found “$”" + hint + ", as in '$schema'"},
		{"a.$b: 1", 1, 2, "a key cannot contain “.” unless it is quoted. Quote the whole key, or use braces to nest, as in a: {b: …}"},
		{"@a b: 1", 1, 1, "expected a key, but found “@”" + hint},
		{"\u3164a: 1", 1, 1, "expected a key, but found U+3164"},
		{"a\ufe0f: 1", 1, 2, "expected “:” after the key, but found U+FE0F"},
		{"a\u034fb: 1", 1, 2, "expected “:” after the key, but found U+034F"},
		{"$a\U000E0100: 1", 1, 1, "expected a key, but found “$”" + hint},
	})
}

// A number followed by a space and a word, such as `512 MiB` or `10 seconds`, gets a hint, and the duration without the space is only suggested when it is valid. The string is always offered, because a unit such as `m` may mean meters rather than minutes.
func TestUnitAfterASpaceHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: 512 MiB", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '512 MiB'"},
		{"a: 10 seconds", 1, 7, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '10 seconds'"},
		{"a: 100 ms", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 100ms, and anything else, such as a size, as a string, as in '100 ms'"},
		{"a: 10 m", 1, 7, "a unit cannot follow a number after a space. Write a duration without the space, as in 10m, and anything else, such as a size, as a string, as in '10 m'"},
		{"a: 1.5 h # note", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 1.5h, and anything else, such as a size, as a string, as in '1.5 h'"},
		{"a: [100 ms]", 1, 9, "a unit cannot follow a number after a space. Write a duration without the space, as in 100ms, and anything else, such as a size, as a string, as in '100 ms'"},
		{"a: {b: 100 ms}", 1, 12, "a unit cannot follow a number after a space. Write a duration without the space, as in 100ms, and anything else, such as a size, as a string, as in '100 ms'"},
		{"a: 1.5 us", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 1.5us, and anything else, such as a size, as a string, as in '1.5 us'"},
		{"a: -5 m", 1, 7, "a unit cannot follow a number after a space. Write a duration without the space, as in -5m, and anything else, such as a size, as a string, as in '-5 m'"},
		{"a: 1_000 ms", 1, 10, "a unit cannot follow a number after a space. Write a duration without the space, as in 1_000ms, and anything else, such as a size, as a string, as in '1_000 ms'"},
		{"a: 2562047 h", 1, 12, "a unit cannot follow a number after a space. Write a duration without the space, as in 2562047h, and anything else, such as a size, as a string, as in '2562047 h'"},
		// Not a whole number of nanoseconds.
		{"a: 1.5 ns", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '1.5 ns'"},
		{"a: 0.0000000001 s", 1, 17, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '0.0000000001 s'"},
		// Zero has no negative spelling.
		{"a: -0.0 s", 1, 9, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '-0.0 s'"},
		// Outside the 64-bit range of nanoseconds.
		{"a: 2562048 h", 1, 12, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '2562048 h'"},
		{"a: 3000000 h", 1, 12, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '3000000 h'"},
		// An exponent or a radix prefix cannot be part of a duration.
		{"a: 1e3 ms", 1, 8, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '1e3 ms'"},
		{"a: 0x10 s", 1, 9, "a unit cannot follow a number after a space. Write a duration without the space, as in 10s, and anything else, such as a size, as a string, as in '0x10 s'"},
		// A keyword after a number is a missing comma, and a word that more text follows may be a key or a sentence.
		{"a: [1 true]", 1, 7, "expected “,”, a line break, or “]” after an array item, but found “t”"},
		{"a: 5 and more", 1, 6, "expected a line break before the next entry, but found “a”"},
		{"a: 'x' MiB", 1, 8, "expected a line break before the next entry, but found “M”"},
		// A “/” only ends the word when a comment starts there, so in `km/h` the text goes on.
		{"speed: 100 km/h", 1, 12, "expected a line break before the next entry, but found “k”"},
		{"a: [5 m/s]", 1, 7, "expected “,”, a line break, or “]” after an array item, but found “m”"},
		{"a: 5 m/* c */", 1, 6, "a unit cannot follow a number after a space. Write a duration without the space, as in 5m, and anything else, such as a size, as a string, as in '5 m'"},
	})

	for _, number := range []string{"1", "-1", "0", "0.5", "1.5", "-1.5", "1_000", "1e3", "0x1F", "0o7", "0b1", "-0.0", "9223372036854775807", "0.000000001", "0.0000000001"} {
		for _, unit := range []string{"h", "m", "s", "ms", "us", "ns"} {
			var value any
			err := Unmarshal([]byte("a: "+number+" "+unit), &value)
			somlError, _ := errors.AsType[*Error](err)
			duration, _, _ := strings.Cut(strings.TrimPrefix(somlError.Message, "a unit cannot follow a number after a space. Write a duration without the space, as in "), ",")

			if err := Unmarshal([]byte("a: "+duration), &value); err != nil {
				t.Errorf("%s %s suggests %s, which is invalid: %v", number, unit, duration, err)
			}
		}
	}
}

// A duration written with `µs`, as Go writes microseconds, is only suggested with `us` when that is valid.
func TestMicroSignHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: 1.5µs", 1, 7, "the unit for microseconds is written us, as in 1.5us"},
		{"a: 3μs", 1, 5, "the unit for microseconds is written us, as in 3us"},
		{"a: 1µs", 1, 5, "the unit for microseconds is written us, as in 1us"},
		{"a: -1_000μs", 1, 10, "the unit for microseconds is written us, as in -1_000us"},
		{"a: 0.001µs", 1, 9, "the unit for microseconds is written us, as in 0.001us"},
		// Not a whole number of nanoseconds.
		{"a: 1.0005µs", 1, 10, "the unit for microseconds is written us"},
		// Zero has no negative spelling.
		{"a: -0.0µs", 1, 8, "the unit for microseconds is written us"},
		// An exponent or a radix prefix cannot be part of a duration.
		{"a: 1e3µs", 1, 7, "the unit for microseconds is written us"},
		{"a: 0x10µs", 1, 8, "the unit for microseconds is written us"},
		// Outside the 64-bit range of nanoseconds.
		{"a: 9223372036854775µs", 1, 20, "the unit for microseconds is written us, as in 9223372036854775us"},
		{"a: 9223372036854776µs", 1, 20, "the unit for microseconds is written us"},
	})
}

// A `//` comment, as in JavaScript, gets a hint where it fails.
func TestSlashCommentHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"// note\na: 1", 1, 1, "expected a key, but found “/”. A comment starts with “#”"},
		{"a: 1 // note", 1, 6, "expected a line break before the next entry, but found “/”. A comment starts with “#”"},
		{"a: [1 // note\n]", 1, 7, "expected “,”, a line break, or “]” after an array item, but found “/”. A comment starts with “#”"},
		{"a: [1, // note\n2]", 1, 8, "expected a value, but found “/”. A comment starts with “#”"},
		{"a: 1 / 2", 1, 6, "expected a line break before the next entry, but found “/”"},
	})
}

// A YAML literal block scalar gets a hint, and a folded one, which joins its lines as a block string does not, gets none.
func TestBlockScalarHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"key: |\n  text", 1, 6, "expected a value, but found “|”. Write a multiline string as a block string, between ''' lines"},
		{"key: >-\n  text", 1, 6, "expected a value, but found “>”"},
		{"key: | x", 1, 6, "expected a value, but found “|”"},
	})
}

// A TOML table header is only reported where a table could be written instead, and for a name that is a valid key, so that the suggestion is valid.
func TestTableHeaderHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"[server]\nport: 1", 1, 2, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"[[servers.http]]\nport: 1", 1, 3, "unexpected “servers”. A string value must be quoted, as in 'servers.http'"},
		{"name: 'x'\n\n[server]\nport: 1", 3, 1, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"# The server.\n[server]\nport: 1", 2, 2, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"\t[server]\nport: 1", 1, 3, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"[[servers]]\nport: 1", 1, 3, "there are no table headers. Write the table as an object, as in servers: {…}"},
		{"a: 1\n[server]\nport: 1", 2, 1, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"a: {\n\t[server]\n}", 2, 2, "there are no table headers. Write the table as an object, as in server: {…}"},
		{"a: [server]", 1, 5, "unexpected “server”. A string value must be quoted, as in 'server'"},
		{"a: 1\n[", 2, 1, "expected a key, but found “[”"},
		// An item of a nested array cannot be a member, so a word in brackets there is an unquoted string.
		{"roles: [\n\t[admin]\n]", 2, 3, "unexpected “admin”. A string value must be quoted, as in 'admin'"},
		{"a: [\n\t1,\n\t[x]\n]", 3, 3, "unexpected “x”. A string value must be quoted, as in 'x'"},
		{"[\n\t1,\n\t[x]\n]", 3, 3, "unexpected “x”. A string value must be quoted, as in 'x'"},
		{"[\n\t[[x]]\n]", 2, 4, "unexpected “x”. A string value must be quoted, as in 'x'"},
		// A value left out before a word in brackets on the next line, as YAML nests a flow sequence, is not a table either.
		{"a:\n[server]\nport: 1", 2, 2, "unexpected “server”. A string value must be quoted, as in 'server'"},
		{"a:\n  [server]", 2, 4, "unexpected “server”. A string value must be quoted, as in 'server'"},
		{"{a:\n[server]\n}", 2, 2, "unexpected “server”. A string value must be quoted, as in 'server'"},
		// A name with a dot is not a valid key, so it is not reported as a table.
		{"[a.b]\nport: 1", 1, 2, "unexpected “a”. A string value must be quoted, as in 'a.b'"},
		{"[[a.b]]\nport: 1", 1, 3, "unexpected “a”. A string value must be quoted, as in 'a.b'"},
		{"x: 1\n[a.b]\nport: 1", 2, 1, "expected a key, but found “[”"},
	})
}

// A bare key directly followed by `:` and more of a key, as in `12:30: 'lunch'`, ends at the first `:`, so the whole key is suggested quoted.
func TestKeyWithAColonHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"{2026-09-19T14:00:00Z: 1}", 1, 2, "a key that contains “:” must be quoted, as in '2026-09-19T14:00:00Z'"},
		{"12:30: 'lunch'", 1, 1, "a key that contains “:” must be quoted, as in '12:30'"},
		{"a:b: 1", 1, 1, "a key that contains “:” must be quoted, as in 'a:b'"},
		{"a:b", 1, 3, "unexpected “b”. A string value must be quoted, as in 'b'"},
		{"a:{b: nope}", 1, 7, "unexpected “nope”. A string value must be quoted, as in 'nope'"},
	})
}

// Digits with colons between them are a time of day or another value that must be quoted.
func TestValueWithAColonHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a:\n\t12:30", 2, 2, "a time of day is a string, so it must be quoted"},
		{"a: 07:32:00", 1, 4, "a time of day is a string, so it must be quoted"},
		{"a: 12:00", 1, 4, "a time of day is a string, so it must be quoted"},
		{"a: 10:30 PM", 1, 4, "a time of day is a string, so it must be quoted"},
		{"a: 8080:80", 1, 4, "invalid number “8080:80”. A value that contains “:” must be quoted, as in '8080:80'"},
		// A value that starts with a zero, such as a MAC address, has no leading zero to remove.
		{"mac: 00:1A:2B:3C:4D:5E", 1, 6, "invalid number “00:1A:2B:3C:4D:5E”. A string value must be quoted, as in '00:1A:2B:3C:4D:5E'"},
		{"a: 0123:abcd::1", 1, 4, "invalid number “0123:abcd::1”. A string value must be quoted, as in '0123:abcd::1'"},
		{"a: -01:30", 1, 4, "invalid number “-01:30”"},
	})
}

// A value that starts with `#` is a comment, so the value is missing. The value is only shown quoted when a literal string can hold it.
func TestValueThatStartsWithAHashHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"color: #FFF\nsize: 1", 2, 1, "expected a value, but found the key size. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"{color: #FFF\nsize: 1}", 2, 1, "expected a value, but found the key size. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"color: #FFF", 1, 12, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"color: #FFF\n", 2, 1, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"{color: #FFF\n}", 2, 1, "expected a value, but found “}”. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"color: #FFF, size: 1", 1, 21, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#FFF'"},
		{"color: # The color\nsize: 1", 2, 1, "expected a value, but found the key size"},
		{"a: #don't", 1, 10, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted"},
		{"a: #'x'", 1, 8, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted"},
		{"a: #it's\nb: 1", 2, 1, "expected a value, but found the key b. “#” starts a comment, so a value that starts with “#” must be quoted"},
		{"{a: #it's\n}", 2, 1, "expected a value, but found “}”. “#” starts a comment, so a value that starts with “#” must be quoted"},
		{"a: #dont", 1, 9, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#dont'"},
		{"a: #say\"hi\"", 1, 12, "expected a value, but reached the end of the document. “#” starts a comment, so a value that starts with “#” must be quoted, as in '#say\"hi\"'"},
	})

	for _, text := range []string{"#FFF", `#a\b`, `#say"hi"`, "#1.5", "#é😀"} {
		var value any
		err := Unmarshal([]byte("a: "+text), &value)
		somlError, _ := errors.AsType[*Error](err)
		_, example, _ := strings.Cut(somlError.Message, "as in ")

		if err := Unmarshal([]byte("a: "+example), &value); err != nil || !reflect.DeepEqual(value, map[string]any{"a": text}) {
			t.Errorf("%s suggests %s, which gives %v, %v", text, example, value, err)
		}
	}
}

// A key indented under an entry whose value is missing, as YAML nests an object, and a YAML list, get a hint. The keys in the suggestion are written as in a document, so that it is valid.
func TestIndentationHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"server:\n  port: 80", 2, 3, "expected a value, but found the key port. Indentation does not nest objects, so write server: {port: …}"},
		{"ports:\n  - 80\n  - 443", 2, 3, "expected a value, but found a “-” list. An array is written in brackets, as in [80, 443]"},
		{"ports:\n- 80", 2, 1, "expected a value, but found a “-” list. An array is written in brackets, as in [80, 443]"},
		{"{\n\ta:\n\tb: 1\n}", 3, 2, "expected a value, but found the key b"},
		{"'the server':\n  'the port': 80", 2, 3, "expected a value, but found the key \"the port\". Indentation does not nest objects, so write 'the server': {'the port': …}"},
		{"\"it's\":\n  b: 1", 2, 3, "expected a value, but found the key b. Indentation does not nest objects, so write \"it's\": {b: …}"},
		{"\"a\\u{1}\":\n  b: 1", 2, 3, "expected a value, but found the key b. Indentation does not nest objects, so write \"a\\u{1}\": {b: …}"},
		{"a:\n  \"\\u{8}\\u{c}\\u{7f}\": 1", 2, 3, "expected a value, but found the key \"\\b\\f\uFFFD\". Indentation does not nest objects, so write a: {\"\\u{8}\\u{c}\\u{7f}\": …}"},
		{"a:\n  \"\\u{1}\\\\\": 1", 2, 3, "expected a value, but found the key \"\\u0001\\\\\". Indentation does not nest objects, so write a: {\"\\u{1}\\\\\": …}"},
		{"a:\n  \"b\\\\c\": 1", 2, 3, "expected a value, but found the key \"b\\\\c\". Indentation does not nest objects, so write a: {'b\\c': …}"},
	})

	suggestion := regexp.MustCompile(`so write (.+): \{(.+): …\}$`)

	for _, keys := range [][2]string{{"a", "b"}, {"'a b'", `"c\u{1}"`}, {`"\u{7f}"`, `"\t"`}, {"'x.y'", "'é😀'"}} {
		var value any
		err := Unmarshal([]byte(keys[0]+":\n  "+keys[1]+": 1"), &value)
		somlError, _ := errors.AsType[*Error](err)
		match := suggestion.FindStringSubmatch(somlError.Message)
		var expected, braced any

		if match == nil || Unmarshal([]byte(keys[0]+": {"+keys[1]+": 1}"), &expected) != nil || Unmarshal([]byte(match[1]+": {"+match[2]+": 1}"), &braced) != nil || !reflect.DeepEqual(braced, expected) {
			t.Errorf("%v: %v suggests %v, want %v", keys, err, braced, expected)
		}
	}
}

// A date and a time with a space between them are only shown as an instant when it is valid, and a local time is described as what it is.
func TestSpaceBetweenADateAndATimeHint(t *testing.T) {
	const separator = "the date and time separator in an instant is an uppercase “T”, not a space"
	const general = separator + ". An instant is written as 2026-09-19T14:00:00Z, with an optional fraction of up to nine digits and an offset of Z or ±HH:MM"

	checkDocumentErrors(t, []documentError{
		{"a: 1979-05-27 07:32:00Z", 1, 4, separator + ", as in 1979-05-27T07:32:00Z"},
		{"a: 1979-05-27 07:32:00.5+02:00", 1, 4, separator + ", as in 1979-05-27T07:32:00.5+02:00"},
		{"a: [1979-05-27 07:32:00Z]", 1, 5, separator + ", as in 1979-05-27T07:32:00Z"},
		{"a: 1979-05-27 07:32:00", 1, 4, separator + ", and an instant needs the offset it was meant in, as in 1979-05-27T07:32:00Z for UTC. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '1979-05-27 07:32:00'"},
		{"a: 1979-05-27 07:32:00,5Z", 1, 4, general},
		{"a: 1979-05-27 07:32", 1, 4, general},
		{"a: 1979-05-27 # note", 1, 4, "1979-05-27 is a date, not an instant. Write a date as a string, as in '1979-05-27'. An instant needs a time and an offset, as in 1979-05-27T00:00:00Z"},
		{"a: 2024-02-29 14:00:00Z", 1, 4, separator + ", as in 2024-02-29T14:00:00Z"},
		{"a: 2026-01-01 14:00:00.123456789Z", 1, 4, separator + ", as in 2026-01-01T14:00:00.123456789Z"},
		{"a: 2026-02-30 14:00:00Z", 1, 4, general},
		{"a: 2025-02-29 14:00:00Z", 1, 4, general},
		{"a: 2026-01-01 24:00:00Z", 1, 4, general},
		{"a: 2026-01-01 14:60:00Z", 1, 4, general},
		{"a: 2026-01-01 14:00:60Z", 1, 4, general},
		{"a: 2026-01-01 14:00:00.1234567890Z", 1, 4, general},
		{"a: 2026-01-01 14:00:00-00:00", 1, 4, general},
		{"a: 2026-01-01 14:00:00+24:00", 1, 4, general},
		// In UTC, these fall outside the years 0001 to 9999.
		{"a: 0001-01-01 00:00:00+01:00", 1, 4, general},
		{"a: 9999-12-31 23:59:59-01:00", 1, 4, general},
		// A local time is only suggested in UTC when that is valid.
		{"a: 2024-02-29 14:00:00", 1, 4, separator + ", and an instant needs the offset it was meant in, as in 2024-02-29T14:00:00Z for UTC. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '2024-02-29 14:00:00'"},
		{"a: 2026-02-30 14:00:00", 1, 4, general},
		{"a: 2026-01-01 25:00:00", 1, 4, general},
		{"a: 0000-01-01 00:00:00", 1, 4, general},
	})
}

// An offset after a space is the offset the instant was meant in, so the instant is not a local time to write as a string. The instant with the offset is only suggested when it is valid.
func TestOffsetAfterASpaceHint(t *testing.T) {
	const reason = "an instant's offset follows its time directly, without a space"
	const local = "an instant needs an offset: Z or ±HH:MM"

	checkDocumentErrors(t, []documentError{
		{"a: 2026-09-19T14:00:00 Z", 1, 4, reason + ", as in 2026-09-19T14:00:00Z"},
		{"a: 2026-09-19T14:00:00.5 -07:30", 1, 4, reason + ", as in 2026-09-19T14:00:00.5-07:30"},
		{"a: [2026-09-19T14:00:00\t\t+02:00, 1]", 1, 5, reason + ", as in 2026-09-19T14:00:00+02:00"},
		{"a: 2026-09-19T14:00:00 +02:00 # é", 1, 4, reason + ", as in 2026-09-19T14:00:00+02:00"},
		{"a: 2026-09-19T14:00:00 Z/* c */", 1, 4, reason + ", as in 2026-09-19T14:00:00Z"},
		{"a: 2026-09-19T14:00:00 Z//c", 1, 4, reason + ", as in 2026-09-19T14:00:00Z"},
		{"a: 2026-02-30T14:00:00 Z", 1, 4, reason},
		{"a: 2026-09-19T14:00:00 +24:00", 1, 4, reason},
		{"a: 0001-01-01T00:00:00 +00:01", 1, 4, reason},
		// A “/” that does not start a comment is more of the value, as in an interval, so the instant is not the whole value.
		{"a: 2026-09-19T14:00:00/2026-09-20T15:00:00", 1, 4, local},
		{"a: [2026-09-19T14:00:00/P1D]", 1, 5, local},
		{"a: 2026-09-19T14:00:00 Z/x", 1, 4, local + ". A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '2026-09-19T14:00:00', or add the offset it was meant in"},
	})
}

// A block string's closing delimiter at the end of a content line, as TOML allows, leaves it unterminated, and the hint names that line.
func TestClosingDelimiterAtTheEndOfALineHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: '''\nWelcome!'''", 1, 4, "unterminated block string. Its closing delimiter must start a line, so move the ''' at the end of line 2 to a new line"},
		{"a: \"\"\"\n\tHello\n\tWelcome!\"\"\"  \nb: 1", 1, 4, "unterminated block string. Its closing delimiter must start a line, so move the \"\"\" at the end of line 3 to a new line"},
		// A line of only quotes, longer than the delimiter, is content.
		{"a: '''\n''''\nx", 1, 4, "unterminated block string"},
	})
}

// A comma used as a thousands separator makes the group after it a separate item, and the number is only suggested when it is in range.
func TestThousandsSeparatorHint(t *testing.T) {
	prefix := func(number string) string {
		return "leading zeros are not allowed in a decimal number. A comma separates items, so " + number + " is not one number"
	}

	checkDocumentErrors(t, []documentError{
		{"a: [1,000]", 1, 7, prefix("1,000") + ". Write it as 1_000 or 1000"},
		{"a: [-1,000]", 1, 8, prefix("-1,000") + ". Write it as -1_000 or -1000"},
		{"a: [12,345,000]", 1, 12, prefix("12,345,000") + ". Write it as 12_345_000 or 12345000"},
		{"a: [1,234,567,000]", 1, 15, prefix("1,234,567,000") + ". Write it as 1_234_567_000 or 1234567000"},
		{"a: [12,050,000]", 1, 8, prefix("12,050,000") + ". Write it as 12_050_000 or 12050000"},
		{"a: [0x1,000]", 1, 9, "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o0, and an identifier, such as a ZIP code, as a string: '000'"},
		{"a: [1, 000]", 1, 8, "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o0, and an identifier, such as a ZIP code, as a string: '000'"},
		{"a: [9,223,372,036,854,775,807]", 1, 15, prefix("9,223,372,036,854,775,807") + ". Write it as 9_223_372_036_854_775_807 or 9223372036854775807"},
		{"a: [-9,223,372,036,854,775,808]", 1, 16, prefix("-9,223,372,036,854,775,808") + ". Write it as -9_223_372_036_854_775_808 or -9223372036854775808"},
		{"a: [9,223,372,036,854,775,808]", 1, 15, prefix("9,223,372,036,854,775,808")},
		{"a: [-9,223,372,036,854,775,809]", 1, 16, prefix("-9,223,372,036,854,775,809")},
		{"a: [10,000,000,000,000,000,000]", 1, 8, prefix("10,000,000,000,000,000,000")},
	})

	suggestions := regexp.MustCompile(`Write it as (\S+) or (\S+)$`)

	for _, number := range []string{"1,000", "-1,000", "999,000", "12,345,000", "9,223,372,036,854,775,807", "9,223,372,036,854,775,808", "-9,223,372,036,854,775,809"} {
		var value any
		err := Unmarshal([]byte("a: ["+number+"]"), &value)
		somlError, _ := errors.AsType[*Error](err)
		match := suggestions.FindStringSubmatch(somlError.Message)
		expected, isInRange := strconv.ParseInt(strings.ReplaceAll(number, ",", ""), 10, 64)

		if match == nil {
			if isInRange == nil {
				t.Errorf("%s: no suggestion", number)
			}

			continue
		}

		for _, suggestion := range match[1:] {
			if err := Unmarshal([]byte("a: ["+suggestion+"]"), &value); err != nil || !reflect.DeepEqual(value, map[string]any{"a": []any{expected}}) {
				t.Errorf("%s suggests %s, which gives %v, %v", number, suggestion, value, err)
			}
		}
	}
}

// A suggested octal integer is in range, and holds the value the digits mean in octal.
func TestSuggestedOctalIsInRange(t *testing.T) {
	octal := func(digits, text string) string {
		return "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o" + digits + ", and an identifier, such as a ZIP code, as a string: '" + text + "'"
	}

	identifier := func(text string) string {
		return "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '" + text + "'"
	}

	checkDocumentErrors(t, []documentError{
		{"a: 0777777777777777777777", 1, 4, octal("777777777777777777777", "0777777777777777777777")},
		{"a: 0000777777777777777777777", 1, 4, octal("777777777777777777777", "0000777777777777777777777")},
		// The largest octal int is 0o777777777777777777777, so one more digit is outside the 64-bit range.
		{"a: 01000000000000000000000", 1, 4, identifier("01000000000000000000000")},
		{"a: 01777777777777777777777", 1, 4, identifier("01777777777777777777777")},
		{"a: 07777777777777777777777777", 1, 4, identifier("07777777777777777777777777")},
	})

	for _, digits := range []string{"00", "07", "0644", "0755", "00007", "0777777777777777777777", "01000000000000000000000", strings.Repeat("0", 30)} {
		var value any
		err := Unmarshal([]byte("a: "+digits), &value)
		somlError, _ := errors.AsType[*Error](err)
		_, suggestion, isSuggested := strings.Cut(somlError.Message, "as 0o")
		expected, isInRange := strconv.ParseInt(digits, 8, 64)

		if !isSuggested {
			if isInRange == nil {
				t.Errorf("%s: no suggestion", digits)
			}

			continue
		}

		suggestion, _, _ = strings.Cut(suggestion, ",")

		if err := Unmarshal([]byte("a: 0o"+suggestion), &value); err != nil || !reflect.DeepEqual(value, map[string]any{"a": expected}) {
			t.Errorf("%s suggests 0o%s, which gives %v, %v", digits, suggestion, value, err)
		}
	}
}

// Two single quotes inside a '...' string, as SQL and YAML escape a quote, end the string.
func TestDoubledQuoteInALiteralStringHint(t *testing.T) {
	checkDocumentErrors(t, []documentError{
		{"a: 'it''s'", 1, 8, "there is no '' escape in a '...' string. Write a string that contains ' as \"...\", as in \"it's\""},
	})
}

// Every fix that an error suggests, where the fix is a value, a key, or an escape, gives a valid document. The errors are those for mutations of every valid conformance case.
func TestEveryFixThatAnErrorSuggestsIsValid(t *testing.T) {
	// The patterns that find a fix in a message, with the document that each fix makes. A fix cut short with “…” is left out, because it is not the whole fix.
	patterns := []struct {
		pattern  *regexp.Regexp
		document func(match []string) []string
	}{
		{regexp.MustCompile(`as in ('[^\n']*')(?:$|[,.] | for )`), nil},
		// An offset, as in `as in +07:00`, is part of an instant rather than a value.
		{regexp.MustCompile(`as in ([^\s'+][^\s']*)(?:, and anything else|$)`), nil},
		{regexp.MustCompile(`uppercase: (0x\S+)$`), nil},
		{regexp.MustCompile(`as (0o\S+), and`), nil},
		{regexp.MustCompile(`Write it as (\S+) or (\S+)$`), nil},
		{regexp.MustCompile(`Use (-?infinity) if you mean it`), nil},
		{regexp.MustCompile(`as in (\S+)Z for UTC`), func(match []string) []string {
			return []string{"a: " + match[1] + "Z"}
		}},
		{regexp.MustCompile(`[Ww]rite (\\u\{[\da-f]+\})`), func(match []string) []string {
			return []string{`a: "` + match[1] + `"`}
		}},
		{regexp.MustCompile(`so write (.+): \{(.+): …\}$`), func(match []string) []string {
			return []string{match[1] + ": {" + match[2] + ": 1}"}
		}},
		{regexp.MustCompile(`as in (\S+): \{…\}$`), func(match []string) []string {
			return []string{match[1] + ": {}"}
		}},
		{regexp.MustCompile(`Quote it, as in ('[^'…]+'), or use braces to nest, as in ([^…]+)…(\}*)$`), func(match []string) []string {
			return []string{match[1] + ": 1", match[2] + "1" + match[3]}
		}},
	}

	suggestedDocuments := func(message string) []string {
		var documents []string

		for _, pattern := range patterns {
			for _, match := range pattern.pattern.FindAllStringSubmatch(message, -1) {
				var made []string

				if pattern.document == nil {
					for _, value := range match[1:] {
						made = append(made, "a: "+value)
					}
				} else {
					made = pattern.document(match)
				}

				for _, document := range made {
					if !strings.Contains(document, "…") {
						documents = append(documents, document)
					}
				}
			}
		}

		return documents
	}

	// Pieces that make the mistakes that the errors suggest fixes for.
	pieces := []string{" ", "\n", "\n  ", ",", ":", ".", "..", "'", "\"", "#", "\\", "-", "_", "0", "00", "1", "9", "a", "e", "x", "T", " T", "Z", "z", "s", "m", "h", " h", " ms", " s", " ns", "µs", "0x", "0o", "f", `\u00`, `\u{0`, `\u{1}`, "[", "]", "[a]", "[a..b]", " 14:00:00", ":00", "+02:00", "-00:00", ",000", "0000"}
	random := rand.New(rand.NewPCG(1, 2))
	checked := 0

	for _, name := range conformanceCases(t, "valid") {
		original := string(readCase(t, filepath.Join(conformanceDirectory, "valid", name+".soml")))

		for range 25 {
			position := random.IntN(len(original) + 1)
			end := position

			if random.IntN(2) == 0 {
				end = min(len(original), position+1+random.IntN(3))
			}

			mutated := original[:position] + pieces[random.IntN(len(pieces))] + original[end:]
			var value any
			somlError, ok := errors.AsType[*Error](Unmarshal([]byte(mutated), &value))

			if !ok {
				continue
			}

			for _, document := range suggestedDocuments(somlError.Message) {
				checked++

				if err := Unmarshal([]byte(document), &value); err != nil {
					t.Errorf("the error for %q, %q, suggests %q, which is invalid: %v", mutated, somlError.Message, document, err)
				}
			}
		}
	}

	// The mutations reach the suggestions, so the test checks something.
	if checked < 1000 {
		t.Fatalf("only %d suggestions were checked", checked)
	}
}
