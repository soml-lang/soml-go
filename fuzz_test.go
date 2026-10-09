package soml

// Fuzz targets, seeded with the conformance corpus. `go test` runs the seeds. Run one for longer with `go test -run='^$' -fuzz=FuzzUnmarshal -fuzztime=10m`. For FuzzChange, add `-fuzzminimizetime=1s`, because the fuzzer otherwise spends most of its time minimizing its inputs of four values.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func addCorpusSeeds(f *testing.F) {
	f.Helper()

	err := filepath.WalkDir(conformanceDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(path, ".soml") {
			return nil
		}

		data, err := os.ReadFile(path)

		if err != nil {
			return err
		}

		f.Add(data)
		return nil
	})

	if err != nil {
		f.Fatal(err)
	}
}

// checkReadError checks that an error from reading data is an *Error with a position inside the data.
func checkReadError(t *testing.T, data []byte, err error) {
	t.Helper()
	somlError, ok := errors.AsType[*Error](err)

	if !ok {
		t.Fatalf("the error is a %T, not an *Error: %v", err, err)
	}

	if somlError.Line < 1 || somlError.Column < 1 || somlError.Offset < 0 || somlError.Offset > len(data) {
		t.Fatalf("the position %d:%d (offset %d) is outside the input of %d bytes", somlError.Line, somlError.Column, somlError.Offset, len(data))
	}

	if line, column := position(string(data), somlError.Offset); line != somlError.Line || column != somlError.Column {
		t.Fatalf("the offset %d is at %d:%d, but the error says %d:%d", somlError.Offset, line, column, somlError.Line, somlError.Column)
	}

	if !utf8.ValidString(somlError.Message) {
		t.Fatalf("the message is not valid UTF-8: %q", somlError.Message)
	}
}

// Unmarshal never panics, every error has a position inside the input, and every accepted document round-trips through canonical form.
func FuzzUnmarshal(f *testing.F) {
	addCorpusSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		var value any

		if err := Unmarshal(data, &value); err != nil {
			checkReadError(t, data, err)
			return
		}

		canonical, err := Marshal(value, Canonical())

		if err != nil {
			t.Fatalf("Marshal of an accepted document: %v", err)
		}

		var again any

		if err := Unmarshal(canonical, &again); err != nil {
			t.Fatalf("the canonical form %q does not read back: %v", canonical, err)
		}

		rewritten, err := Marshal(again, Canonical())

		if err != nil {
			t.Fatal(err)
		}

		if string(rewritten) != string(canonical) {
			t.Fatalf("canonical form is not a fixed point:\n%q\n%q", canonical, rewritten)
		}
	})
}

// The tree of an accepted document prints back to the exact input, and has the same value as Unmarshal gives.
func FuzzTree(f *testing.F) {
	addCorpusSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		document, err := Parse(data)
		var value any
		unmarshalError := Unmarshal(data, &value)

		if (err == nil) != (unmarshalError == nil) {
			t.Fatalf("Parse gave %v, but Unmarshal gave %v", err, unmarshalError)
		}

		if err != nil {
			return
		}

		if printed := document.Bytes(); string(printed) != string(data) {
			t.Fatalf("printed %q, want %q", printed, data)
		}

		for _, comment := range document.Comments() {
			if comment.Span.Start < 0 || comment.Span.End > len(data) || string(data[comment.Span.Start:comment.Span.End]) != delimitedComment(comment) {
				t.Fatalf("the comment %q has the span %v", comment.Text, comment.Span)
			}
		}

		checkSpans(t, string(data), document.Root())
		var fromTree any

		if err := document.Decode(&fromTree); err != nil {
			t.Fatal(err)
		}

		treeCanonical, _ := Marshal(fromTree)
		canonical, _ := Marshal(value)

		if string(treeCanonical) != string(canonical) {
			t.Fatalf("the tree decodes to %q, want %q", treeCanonical, canonical)
		}
	})
}

// Set and Remove keep the document valid, never panic, and leave the document as it was when they fail.
func FuzzChange(f *testing.F) {
	for _, text := range modelDocuments {
		f.Add([]byte(text), uint8(0), uint16(0), int64(1))
	}

	f.Fuzz(func(t *testing.T, data []byte, operation uint8, choice uint16, number int64) {
		document, err := Parse(data)

		if err != nil {
			return
		}

		var value any

		if err := document.Decode(&value); err != nil {
			t.Fatal(err)
		}

		paths := modelPaths(value, nil)

		if len(paths) == 0 {
			return
		}

		path := paths[int(choice)%len(paths)]
		before := document.String()

		switch operation % 3 {
		case 0:
			err = document.Set(number, path...)
			checkSetDidNotMakeAnInvalidDocument(t, err)
		case 1:
			err = document.Set(map[string]any{"a": []any{number, "x"}}, append(path[:len(path)-1:len(path)-1], "added")...)
			checkSetDidNotMakeAnInvalidDocument(t, err)
		default:
			_, err = document.Remove(path...)
		}

		printed := document.String()

		if err != nil {
			if printed != before {
				t.Fatalf("a failed change changed the document from %q to %q", before, printed)
			}

			return
		}

		reparsed, err := Parse([]byte(printed))

		if err != nil {
			t.Fatalf("not valid after a change of %q:\n%s\n%v", data, printed, err)
		}

		if reparsed.String() != printed {
			t.Fatalf("does not print back unchanged: %q", printed)
		}
	})
}

// Format keeps the value and the comments of an accepted document, and formatting its output gives the same text.
func FuzzFormat(f *testing.F) {
	addCorpusSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		formatted, err := Format(data)

		if err != nil {
			checkReadError(t, data, err)
			return
		}

		var value, formattedValue any

		if err := Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}

		if err := Unmarshal(formatted, &formattedValue); err != nil {
			t.Fatalf("the formatted text is not valid: %v\n%q", err, formatted)
		}

		canonical, _ := Marshal(value)
		formattedCanonical, _ := Marshal(formattedValue)

		if string(canonical) != string(formattedCanonical) {
			t.Fatalf("the value changed:\n%q\n%q", data, formatted)
		}

		again, err := Format(formatted)

		if err != nil || string(again) != string(formatted) {
			t.Fatalf("formatting is not idempotent:\n%q\n%q", formatted, again)
		}

		document, _ := Parse(data)
		formattedDocument, _ := Parse(formatted)

		if len(document.Comments()) != len(formattedDocument.Comments()) {
			t.Fatalf("the number of comments changed:\n%q\n%q", data, formatted)
		}
	})
}

// checkSpans checks that every node of a parsed tree has the span of its own text: a scalar and a key their raw text, a container its brackets, and a member from its key to the end of its value.
func checkSpans(t *testing.T, text string, node Node) {
	t.Helper()
	span, ok := node.Span()

	if !ok {
		t.Fatalf("a parsed node has no span: %#v", node)
	}

	switch node := node.(type) {
	case *Scalar:
		if text[span.Start:span.End] != node.Raw() {
			t.Fatalf("the span of %q is %q", node.Raw(), text[span.Start:span.End])
		}
	case *Array:
		if text[span.Start] != '[' || text[span.End-1] != ']' {
			t.Fatalf("the span of an array is %q", text[span.Start:span.End])
		}

		for _, item := range node.Items {
			checkSpans(t, text, item.Value)
		}
	case *Object:
		if node.IsBraced() && (text[span.Start] != '{' || text[span.End-1] != '}') {
			t.Fatalf("the span of an object is %q", text[span.Start:span.End])
		}

		for _, member := range node.Members {
			memberSpan, _ := member.Span()
			valueSpan, _ := member.Value.Span()

			keySpan, _ := member.Key.Span()

			if text[keySpan.Start:keySpan.End] != member.Key.Raw() {
				t.Fatalf("the span of the key %q is %q", member.Key.Raw(), text[keySpan.Start:keySpan.End])
			}

			if memberSpan.Start != keySpan.Start || memberSpan.End != valueSpan.End {
				t.Fatalf("the member span %v does not run from its key %v to the end of its value %v", memberSpan, keySpan, valueSpan)
			}

			checkSpans(t, text, member.Value)
		}
	}
}

// Set and Remove must change a document's value as they change a copy of the value, and removing it again must give the value before the set. The seeds cover the conformance corpus, so comments, blank lines, and odd layouts are exercised too.
func FuzzChangeValueSemantics(f *testing.F) {
	for _, text := range modelDocuments {
		f.Add([]byte(text))
	}

	addCorpusSeeds(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		document, err := Parse(data)

		if err != nil {
			return
		}

		var value any

		if err := document.Decode(&value); err != nil {
			t.Fatal(err)
		}

		paths := modelPaths(value, nil)

		if len(paths) == 0 {
			return
		}

		choice := 0

		if len(data) >= 2 {
			choice = int(data[0]) + int(data[1])*256
		}

		path := paths[choice%len(paths)]

		// An array item is appended at the end half of the time, which exercises adding rather than replacing.
		if len(path) > 1 {
			if array, ok := valueAtPath(value, path[:len(path)-1]).([]any); ok {
				if _, isIndex := path[len(path)-1].(int); isIndex && len(data) > 1 && data[1]%2 == 0 {
					path[len(path)-1] = len(array)
				}
			}
		}

		item := []any{int64(42), "new", false, nil}[byteOr(data, 1)%4]

		expected := cloneAny(value)
		expected = setInValue(expected, path, item)

		if err := document.Set(item, path...); err != nil {
			t.Fatalf("Set(%v, %v) failed on %q: %v", item, path, data, err)
		}

		checkValueMatches(t, document, expected)

		// Removing the value again must give the value before the set.
		expected = removeInValue(expected, path)
		removed, err := document.Remove(path...)

		if err != nil {
			t.Fatalf("Remove(%v) failed on %q: %v", path, document.String(), err)
		}

		if !removed {
			t.Fatalf("Remove(%v) reported nothing on %q", path, document.String())
		}

		checkValueMatches(t, document, expected)
	})
}

func byteOr(data []byte, index int) byte {
	for len(data) <= index {
		data = append(data, 0)
	}

	return data[index]
}

func valueAtPath(value any, path []any) any {
	current := value

	for _, element := range path {
		switch container := current.(type) {
		case map[string]any:
			current = container[element.(string)]
		case []any:
			current = container[element.(int)]
		default:
			return nil
		}
	}

	return current
}

// setInValue sets item at path in a copy of value, mirroring Document.Set: missing objects are made, and an index equal to the length appends.
func setInValue(value any, path []any, item any) any {
	switch key := path[0].(type) {
	case string:
		object := value.(map[string]any)

		if len(path) == 1 {
			object[key] = cloneAny(item)
			return value
		}

		next, ok := object[key]

		if !ok {
			next = map[string]any{}
		}

		object[key] = setInValue(next, path[1:], item)
		return value
	case int:
		array := value.([]any)

		if len(path) == 1 {
			if key == len(array) {
				return append(array, cloneAny(item))
			}

			array[key] = cloneAny(item)
			return array
		}

		if key == len(array) {
			array = append(array, map[string]any{})
		}

		array[key] = setInValue(array[key], path[1:], item)
		return array
	}

	panic("a path element is neither a key nor an index")
}

// removeInValue removes the value at path from a copy, mirroring Document.Remove.
func removeInValue(value any, path []any) any {
	switch key := path[0].(type) {
	case string:
		object := value.(map[string]any)

		if len(path) == 1 {
			delete(object, key)
			return value
		}

		next, ok := object[key]

		if ok {
			object[key] = removeInValue(next, path[1:])
		}

		return value
	case int:
		array := value.([]any)

		if len(path) == 1 {
			if key >= len(array) {
				return array
			}

			return append(array[:key], array[key+1:]...)
		}

		if key < len(array) {
			array[key] = removeInValue(array[key], path[1:])
		}

		return array
	}

	panic("a path element is neither a key nor an index")
}

// cloneAny copies a decoded value, so that the model and the document cannot share nodes.
func cloneAny(value any) any {
	switch value := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(value))

		for key, item := range value {
			copied[key] = cloneAny(item)
		}

		return copied
	case []any:
		copied := make([]any, len(value))

		for index, item := range value {
			copied[index] = cloneAny(item)
		}

		return copied
	}

	return value
}

// checkValueMatches fails when the document's value is not the expected value.
func checkValueMatches(t *testing.T, document *Document, expected any) {
	t.Helper()
	var actual any

	if err := document.Decode(&actual); err != nil {
		t.Fatalf("Decode of %q: %v", document.String(), err)
	}

	actualText, err := Marshal(actual, Canonical())

	if err != nil {
		t.Fatalf("Marshal(actual): %v", err)
	}

	expectedText, err := Marshal(expected, Canonical())

	if err != nil {
		t.Fatalf("Marshal(expected): %v", err)
	}

	if string(actualText) != string(expectedText) {
		t.Fatalf("the value changed wrongly:\n document %q\n actual   %q\n expected %q", document.String(), actualText, expectedText)
	}
}

// A value of a Go type is written by Marshal, read back into that type, and written again: the two writes must be the same bytes, and decoding into the type must agree with decoding into an `any`.
func FuzzTypedRoundTrip(f *testing.F) {
	types := []reflect.Type{
		reflect.TypeOf(struct {
			A int     `soml:"a"`
			B string  `soml:"b"`
			C float64 `soml:"c"`
		}{}),
		reflect.TypeOf(struct {
			M map[string]int `soml:"m"`
			S []string       `soml:"s"`
			P *int           `soml:"p"`
			I any            `soml:"i"`
			T time.Time      `soml:"t"`
			D time.Duration  `soml:"d"`
		}{}),
		reflect.TypeOf(struct {
			B []byte  `soml:"b"`
			F [4]byte `soml:"f"`
			U uint64  `soml:"u"`
			X float32 `soml:"x"`
		}{}),
		reflect.TypeOf(map[string]any{}),
		reflect.TypeOf([]map[string]int{}),
		reflect.TypeOf(struct {
			Nested struct {
				Deep []struct {
					V int `soml:"v"`
				} `soml:"deep"`
			} `soml:"nested"`
		}{}),
		reflect.TypeOf(map[int64]string{}),
		reflect.TypeOf(map[time.Time]int{}),
		reflect.TypeOf(map[string]*int{}),
	}

	// The first bytes pick the type, so a seed makes every combination at least once.
	for seed := 0; seed < 64; seed++ {
		f.Add([]byte{byte(seed), byte(seed >> 8), 1, 2, 3, 4, 5, 6})
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		generator := &fuzzValueGenerator{data: data}
		valueType := types[generator.byte()%uint(len(types))]
		value := generator.namedValue(valueType, 4)

		text, err := Marshal(value.Interface(), Canonical())

		if err != nil {
			// A value that Marshal cannot write, such as a uint64 above the int64 range, is not a round trip.
			return
		}

		decoded := reflect.New(valueType).Elem()

		if err := Unmarshal(text, decoded.Addr().Interface()); err != nil {
			t.Fatalf("Unmarshal(%q) into %s: %v", text, valueType, err)
		}

		var decodedAny any

		if err := Unmarshal(text, &decodedAny); err != nil {
			t.Fatalf("Unmarshal(%q) into an any: %v", text, err)
		}

		rewritten, err := Marshal(decoded.Interface(), Canonical())

		if err != nil {
			t.Fatalf("Marshal of %s after a round trip: %v", valueType, err)
		}

		if string(rewritten) != string(text) {
			t.Fatalf("not a fixed point:\n first %q\n again %q", text, rewritten)
		}

		anyWritten, err := Marshal(decodedAny, Canonical())

		if err != nil {
			t.Fatalf("Marshal of the any: %v", err)
		}

		if string(anyWritten) != string(rewritten) {
			t.Fatalf("decoding into %s and into an any disagree:\n typed %q\n any   %q", valueType, rewritten, anyWritten)
		}
	})
}

// fuzzValueGenerator builds a value of any type from the fuzz input, so that the compiler drives the shapes it exercises.
type fuzzValueGenerator struct {
	data []byte
	at   int
}

func (generator *fuzzValueGenerator) byte() uint {
	if generator.at >= len(generator.data) {
		return 0
	}

	value := generator.data[generator.at]
	generator.at++
	return uint(value)
}

func (generator *fuzzValueGenerator) int64Value() int64 {
	var value int64

	for range 6 {
		value = value<<8 | int64(generator.byte())
	}

	return value
}

func (generator *fuzzValueGenerator) text() string {
	length := int(generator.byte() % 7)
	result := make([]byte, length)
	choices := []byte("ab '\"\\\n\t\x00\x7fé{:#_-0.,[]")

	for index := range result {
		result[index] = choices[generator.byte()%uint(len(choices))]
	}

	return string(result)
}

// namedValue builds a value of valueType. A scalar is wrapped in an array when the depth ran out, so that the root of the document is a collection.
func (generator *fuzzValueGenerator) namedValue(valueType reflect.Type, depth int) reflect.Value {
	if depth <= 0 {
		switch valueType.Kind() {
		case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Pointer, reflect.Interface:
			valueType = reflect.TypeOf(0)
		}
	}

	switch valueType.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(generator.byte()%2 == 0)
	case reflect.Int:
		return reflect.ValueOf(int(generator.int64Value()))
	case reflect.Int8:
		return reflect.ValueOf(int8(generator.int64Value()))
	case reflect.Int16:
		return reflect.ValueOf(int16(generator.int64Value()))
	case reflect.Int32:
		return reflect.ValueOf(int32(generator.int64Value()))
	case reflect.Int64:
		if valueType == durationType {
			return reflect.ValueOf(time.Duration(generator.int64Value()))
		}

		return reflect.ValueOf(generator.int64Value())
	case reflect.Uint:
		return reflect.ValueOf(uint(generator.int64Value()))
	case reflect.Uint8:
		return reflect.ValueOf(uint8(generator.int64Value()))
	case reflect.Uint16:
		return reflect.ValueOf(uint16(generator.int64Value()))
	case reflect.Uint32:
		return reflect.ValueOf(uint32(generator.int64Value()))
	case reflect.Uint64:
		return reflect.ValueOf(uint64(generator.int64Value()))
	case reflect.Uintptr:
		return reflect.ValueOf(uintptr(generator.int64Value()))
	case reflect.Float32:
		return reflect.ValueOf(float32(generator.int64Value()) / (1 << 24))
	case reflect.Float64:
		return reflect.ValueOf(float64(generator.int64Value()) / (1 << 24))
	case reflect.String:
		text := reflect.ValueOf(generator.text())

		if text.Type() != valueType {
			text = text.Convert(valueType)
		}

		return text
	case reflect.Pointer:
		if generator.byte()%4 == 0 {
			return reflect.Zero(valueType)
		}

		pointer := reflect.New(valueType.Elem())
		pointer.Elem().Set(generator.namedValue(valueType.Elem(), depth-1))
		return pointer
	case reflect.Interface:
		if valueType.NumMethod() == 0 {
			item := generator.anyValue(depth - 1)

			if item == nil {
				return reflect.Zero(valueType)
			}

			return reflect.ValueOf(item)
		}

		return reflect.Zero(valueType)
	case reflect.Slice:
		if valueType.Elem().Kind() == reflect.Uint8 {
			count := int(generator.byte() % 11)
			data := make([]byte, count)

			for index := range data {
				data[index] = byte(generator.byte())
			}

			return reflect.ValueOf(data)
		}

		count := int(generator.byte() % 4)
		slice := reflect.MakeSlice(valueType, count, count)

		for index := range count {
			slice.Index(index).Set(generator.namedValue(valueType.Elem(), depth-1))
		}

		return slice
	case reflect.Array:
		array := reflect.New(valueType).Elem()

		for index := range valueType.Len() {
			array.Index(index).Set(generator.namedValue(valueType.Elem(), depth-1))
		}

		return array
	case reflect.Map:
		count := int(generator.byte() % 4)
		value := reflect.MakeMapWithSize(valueType, count)

		for range count {
			value.SetMapIndex(generator.mapKey(valueType.Key()), generator.namedValue(valueType.Elem(), depth-1))
		}

		return value
	case reflect.Struct:
		if valueType == timeType {
			return reflect.ValueOf(time.Unix(0, generator.int64Value()).UTC())
		}

		value := reflect.New(valueType).Elem()

		for fieldIndex := range valueType.NumField() {
			field := valueType.Field(fieldIndex)

			if !field.IsExported() {
				continue
			}

			value.Field(fieldIndex).Set(generator.namedValue(field.Type, depth-1))
		}

		return value
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return reflect.Zero(valueType)
	}

	return reflect.Zero(valueType)
}

func (generator *fuzzValueGenerator) mapKey(keyType reflect.Type) reflect.Value {
	switch keyType.Kind() {
	case reflect.String:
		return reflect.ValueOf(generator.text())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		key := reflect.New(keyType).Elem()
		key.SetInt(generator.int64Value()%1000 - 500)
		return key
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		key := reflect.New(keyType).Elem()
		key.SetUint(uint64(generator.int64Value() % 1000))
		return key
	case reflect.Pointer:
		pointer := reflect.New(keyType.Elem())
		pointer.Elem().Set(generator.mapKey(keyType.Elem()))
		return pointer
	case reflect.Struct:
		if keyType == timeType {
			return reflect.ValueOf(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
		}
	}

	return reflect.ValueOf(generator.text())
}

func (generator *fuzzValueGenerator) anyValue(depth int) any {
	switch generator.byte() % 9 {
	case 0:
		return nil
	case 1:
		return generator.byte()%2 == 0
	case 2:
		return generator.int64Value()
	case 3:
		return float64(generator.int64Value()) / (1 << 24)
	case 4:
		return generator.text()
	case 5:
		return time.Unix(0, generator.int64Value()).UTC()
	case 6:
		return time.Duration(generator.int64Value())
	case 7:
		count := int(generator.byte() % 3)
		items := make([]any, count)

		for index := range count {
			items[index] = generator.anyValue(depth - 1)
		}

		return items
	}

	count := int(generator.byte() % 3)
	value := map[string]any{}

	for range count {
		value[generator.text()] = generator.anyValue(depth - 1)
	}

	return value
}
