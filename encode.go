package soml

// Writing: the layout and spelling of canonical form, the one text the spec defines for a value, with struct fields in their declared order unless the Canonical option sorts them too.

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

/*
Marshaler is the interface of a type that gives its own SOML value. MarshalSOML returns a Go value, such as a string or a map[string]any, which is encoded in place of the receiver.
*/
type Marshaler interface {
	MarshalSOML() (any, error)
}

/*
Marshal returns a value as SOML, in the layout and spelling of canonical form: one member per line, tab indentation, no commas, and one line feed at the end. Struct fields keep the order they are declared in, as in encoding/json, because that order reads better in a file for people. Map keys are sorted. Pass Canonical for canonical form itself, where struct fields are sorted by key too, so that equal values give equal bytes.

The value must be an object or an array in SOML, such as a struct, a map, or a slice, because a document is always a collection.

Go values map to SOML like this:

  - bool → bool
  - integer types → int. A uint64 above math.MaxInt64 is an error.
  - float64, float32 → float. NaN is an error, and -0.0 is written 0.0. A float32 is written with the shortest digits that read back as the same float32.
  - string → string. Invalid UTF-8 and a carriage return are errors, because SOML cannot represent them.
  - []byte, [N]byte → string, in base64
  - time.Time → instant, in UTC. A time outside the years 0001 to 9999 in UTC is an error.
  - time.Duration → duration
  - slice, array → array. A nil slice is [].
  - struct, map → object. A nil map is {}. Map keys can be strings, integers, types with an AppendText or MarshalText method, interfaces that hold one of them, and pointers to them. MarshalSOML is not used for keys.
  - pointer, interface → the value it holds, or null when it is nil
  - Marshaler → the value that MarshalSOML returns
  - encoding.TextAppender, encoding.TextMarshaler → string

Struct fields use the `soml` tag, as in `soml:"key,omitzero,omitempty"`. Without a tag, the key is the field name. omitzero leaves the field out when it is the zero value, or when its IsZero method says so. omitempty leaves it out when it would be written as null, an empty string, [], or {}. `soml:"-"` leaves it out always, and `soml:"-,"` is the key `-`. Embedded structs work as in encoding/json. A tag with an unknown option, an option written twice, or a comma at the end is an error, and so is a soml tag on an unexported field, as in encoding/json/v2.

Nesting deeper than 100 levels is an error, as in the spec, so Marshal never writes a document that Unmarshal rejects. This also makes a cycle of pointers an error.
*/
func Marshal(in any, options ...Option) ([]byte, error) {
	encoder := encoder{isCanonical: settingsOf(options).isCanonical}
	value, err := encoder.resolve(reflect.ValueOf(in))

	if err != nil {
		return nil, err
	}

	switch value.kind {
	case resolvedObject:
		members, err := encoder.members(value.value, 1)

		if err != nil {
			return nil, err
		}

		// A non-empty top-level object is written without braces, with its members at column 0.
		hasMembers, err := encoder.writeMembers(members, 0, 1)

		if err != nil {
			return nil, err
		}

		// Every member was left out by omitempty, or there was none.
		if !hasMembers {
			encoder.buffer = append(encoder.buffer, "{}\n"...)
		}
	case resolvedArray:
		if err := encoder.writeResolved(value, 0, 0); err != nil {
			return nil, err
		}

		encoder.buffer = append(encoder.buffer, '\n')
	case resolvedNull:
		return nil, newError("a document must be an object or an array, but %s is null", describeType(reflect.ValueOf(in)))
	default:
		return nil, newError("a document must be an object or an array, but %s is a scalar", describeType(reflect.ValueOf(in)))
	}

	return encoder.buffer, nil
}

/*
MarshalWrite writes a value to writer, as Marshal returns it. Writer errors, including io.ErrShortWrite for a short write, are wrapped in an *Error without a position. Nothing is written when the value cannot be written as SOML.
*/
func MarshalWrite(writer io.Writer, in any, options ...Option) error {
	data, err := Marshal(in, options...)

	if err != nil {
		return err
	}

	written, err := writer.Write(data)

	if written < len(data) && err == nil {
		err = io.ErrShortWrite
	}

	if err != nil {
		result := newError("cannot write the document: %s", err)
		result.Err = err
		return result
	}

	return nil
}

/*
Canonical makes Marshal write canonical form, the bytes the spec defines for a value: struct fields are sorted by key too, as map keys always are. Equal values then give equal bytes, so the output can be hashed, signed, or compared. It has no effect on Unmarshal.
*/
func Canonical() Option {
	return Option{apply: func(settings *settings) {
		settings.isCanonical = true
	}}
}

type encoder struct {
	buffer []byte

	// Whether struct fields are sorted by key, for canonical form, rather than written in their declared order.
	isCanonical bool

	// The member lists of the objects being written, one for each depth, so that every object at a depth reuses one list.
	memberScratch [][]encodedMember

	// Whether the encoder writes a value only to see if omitempty leaves it out, so that it does not do that again inside it, which a cycle would make endless.
	isCheckingEmpty bool
}

var (
	marshalerType     = reflect.TypeFor[Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	textAppenderType  = reflect.TypeFor[encoding.TextAppender]()
	timeType          = reflect.TypeFor[time.Time]()
	durationType      = reflect.TypeFor[time.Duration]()

	// The types of a syntax tree, which hold the tree's own state in unexported fields, so writing them would give only `{}`.
	treeTypes = []reflect.Type{reflect.TypeFor[Document](), reflect.TypeFor[Object](), reflect.TypeFor[Array](), reflect.TypeFor[Scalar](), reflect.TypeFor[Member](), reflect.TypeFor[Item](), reflect.TypeFor[Key]()}
)

type resolvedKind uint8

const (
	resolvedNull resolvedKind = iota

	// A bool, a number, a string, bytes, a time.Time, or a time.Duration, which is written by its type.
	resolvedScalar

	// The text from a MarshalText or AppendText method, which is written as a string.
	resolvedText

	resolvedArray
	resolvedObject
)

/*
A value after pointers, interfaces, and MarshalSOML methods are followed.
*/
type resolved struct {
	kind  resolvedKind
	value reflect.Value
	text  string
}

func describeType(value reflect.Value) string {
	if !value.IsValid() {
		return "nil"
	}

	return "a value of type " + value.Type().String()
}

// maxResolveSteps is the most pointers, interfaces, and MarshalSOML calls in a row that Marshal and Unmarshal follow, for a value and for a map key, so that a cycle of pointers, or a MarshalSOML method that returns its own type again, is an error and not an endless loop.
const maxResolveSteps = maxDepth

// resolve follows pointers, interfaces, and the custom methods until it reaches a SOML value.
func (encoder *encoder) resolve(value reflect.Value) (resolved, error) {
	for steps := 0; ; steps++ {
		if !value.IsValid() {
			return resolved{kind: resolvedNull}, nil
		}

		valueType := value.Type()

		if steps > maxResolveSteps {
			return resolved{}, newError("a value of type %s goes through more than %d pointers, interfaces, and MarshalSOML calls in a row, which is most likely a cycle", valueType, maxResolveSteps)
		}

		// Pointers and interfaces are followed first, so that a pointer to a time.Time is an instant, and a method with a pointer receiver is found through the address of the value.
		if valueType.Kind() == reflect.Pointer || valueType.Kind() == reflect.Interface {
			if value.IsNil() {
				return resolved{kind: resolvedNull}, nil
			}

			value = value.Elem()
			continue
		}

		// Before the methods, because time.Time has AppendText and MarshalText methods, which would write it as a string and not an instant.
		if valueType == timeType || valueType == durationType {
			return resolved{kind: resolvedScalar, value: value}, nil
		}

		methods := methodsOf(valueType)

		if methods.isTree {
			return resolved{}, newError("a value of type %s is part of a syntax tree, so it cannot be written as a value", valueType)
		}

		if methods.marshaler != noMethod {
			result, err := methodValue(value, methods.marshaler).Interface().(Marshaler).MarshalSOML()

			if err != nil {
				return resolved{}, wrapMethodError("MarshalSOML", valueType, err)
			}

			value = reflect.ValueOf(result)
			continue
		}

		if text, ok, err := methods.marshalText(value); ok {
			return resolved{kind: resolvedText, text: text}, err
		}

		switch valueType.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr, reflect.Float32, reflect.Float64, reflect.String:
			return resolved{kind: resolvedScalar, value: value}, nil
		case reflect.Slice, reflect.Array:
			if isBytes(valueType) {
				return resolved{kind: resolvedScalar, value: value}, nil
			}

			return resolved{kind: resolvedArray, value: value}, nil
		case reflect.Map, reflect.Struct:
			return resolved{kind: resolvedObject, value: value}, nil
		}

		return resolved{}, newError("a value of type %s cannot be written as SOML", valueType)
	}
}

// isBytes reports whether a slice or array type is base64 bytes in SOML: its items are bytes without a method that encodes or decodes them. Marshal and Unmarshal use the same rule.
func isBytes(valueType reflect.Type) bool {
	methods := methodsOf(valueType.Elem())
	return valueType.Elem().Kind() == reflect.Uint8 && !methods.encodes() && !methods.decodes()
}

// writeScalar writes a value that resolve gave as a resolvedScalar.
func (encoder *encoder) writeScalar(value reflect.Value) error {
	switch value.Type() {
	case timeType:
		instant, _ := reflect.TypeAssert[time.Time](value)
		text, err := appendInstant(encoder.buffer, instant)

		if err != nil {
			return err
		}

		encoder.buffer = text
		return nil
	case durationType:
		encoder.buffer = appendDuration(encoder.buffer, time.Duration(value.Int()))
		return nil
	}

	switch value.Kind() {
	case reflect.Bool:
		encoder.buffer = strconv.AppendBool(encoder.buffer, value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		encoder.buffer = strconv.AppendInt(encoder.buffer, value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.Uint() > math.MaxInt64 {
			return newError("the integer %d is outside the 64-bit signed range of a SOML int", value.Uint())
		}

		encoder.buffer = strconv.AppendUint(encoder.buffer, value.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		text, err := appendFloat(encoder.buffer, value.Float(), value.Type().Bits())

		if err != nil {
			return err
		}

		encoder.buffer = text
	case reflect.String:
		return encoder.writeText(value.String())
	case reflect.Array:
		// Bytes needs an array that can be addressed.
		value = addressable(value)
		fallthrough
	case reflect.Slice:
		// Base64 has no character that needs escapes.
		encoder.buffer = append(encoder.buffer, '\'')
		encoder.buffer = base64.StdEncoding.AppendEncode(encoder.buffer, value.Bytes())
		encoder.buffer = append(encoder.buffer, '\'')
	}

	return nil
}

func (encoder *encoder) writeText(text string) error {
	if err := checkRepresentable(text, "string"); err != nil {
		return err
	}

	encoder.buffer = appendString(encoder.buffer, text)
	return nil
}

// methodValue returns the value to call a method on: the value itself for a value receiver, or its address for a pointer receiver. A value that cannot be addressed is copied first.
func methodValue(value reflect.Value, receiver receiver) reflect.Value {
	if receiver == valueMethod {
		return value
	}

	return addressable(value).Addr()
}

// addressable returns the value when it can be addressed, and otherwise a copy of it that can be.
func addressable(value reflect.Value) reflect.Value {
	if value.CanAddr() {
		return value
	}

	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	return copied
}

// marshalText returns the text of an AppendText or a MarshalText method of the value, and reports whether the type has one.
// Note: Not using reflect.TypeAssert because interface results still require boxing and it is slower here.
func (methods *typeMethods) marshalText(value reflect.Value) (string, bool, error) {
	var text []byte
	var err error

	switch {
	case methods.textAppender != noMethod:
		text, err = methodValue(value, methods.textAppender).Interface().(encoding.TextAppender).AppendText(nil)

		if err != nil {
			return "", true, wrapMethodError("AppendText", value.Type(), err)
		}
	case methods.textMarshaler != noMethod:
		text, err = methodValue(value, methods.textMarshaler).Interface().(encoding.TextMarshaler).MarshalText()

		if err != nil {
			return "", true, wrapMethodError("MarshalText", value.Type(), err)
		}
	default:
		return "", false, nil
	}

	return string(text), true, nil
}

func wrapMethodError(method string, valueType reflect.Type, err error) error {
	result := newError("%s of %s: %s", method, valueType, err)
	result.Err = err
	return result
}

// checkRepresentable checks that SOML can represent a string: it must be valid UTF-8, and it must not have a carriage return, which SOML cannot represent, not even as an escape.
func checkRepresentable(text, what string) error {
	if !utf8.ValidString(text) {
		return newError("a %s must be valid UTF-8, but %s is not", what, quote(sanitize(text)))
	}

	if strings.Contains(text, "\r") {
		return newError("a %s cannot contain a carriage return (U+000D), because SOML cannot represent one", what)
	}

	return nil
}

// writeResolved writes a value as it appears after a key or as an array item, with indentation tabs for the lines inside it. depth is the number of collections around it.
func (encoder *encoder) writeResolved(value resolved, indentation, depth int) error {
	switch value.kind {
	case resolvedNull:
		encoder.buffer = append(encoder.buffer, "null"...)
	case resolvedScalar:
		return encoder.writeScalar(value.value)
	case resolvedText:
		return encoder.writeText(value.text)
	case resolvedArray:
		if depth+1 > maxDepth {
			return errorTooDeep()
		}

		length := value.value.Len()

		if length == 0 {
			encoder.buffer = append(encoder.buffer, "[]"...)
			return nil
		}

		encoder.buffer = append(encoder.buffer, "[\n"...)

		for index := range length {
			encoder.buffer = appendTabs(encoder.buffer, indentation+1)

			if err := encoder.writeValue(value.value.Index(index), indentation+1, depth+1); err != nil {
				return prependPath(err, index)
			}

			encoder.buffer = append(encoder.buffer, '\n')
		}

		encoder.buffer = appendTabs(encoder.buffer, indentation)
		encoder.buffer = append(encoder.buffer, ']')
	case resolvedObject:
		if depth+1 > maxDepth {
			return errorTooDeep()
		}

		members, err := encoder.members(value.value, depth+1)

		if err != nil {
			return err
		}

		opening := len(encoder.buffer)
		encoder.buffer = append(encoder.buffer, "{\n"...)
		hasMembers, err := encoder.writeMembers(members, indentation+1, depth+1)

		if err != nil {
			return err
		}

		if !hasMembers {
			encoder.buffer = append(encoder.buffer[:opening], "{}"...)
			return nil
		}

		encoder.buffer = appendTabs(encoder.buffer, indentation)
		encoder.buffer = append(encoder.buffer, '}')
	}

	return nil
}

func (encoder *encoder) writeValue(value reflect.Value, indentation, depth int) error {
	resolvedValue, err := encoder.resolve(value)

	if err != nil {
		return err
	}

	return encoder.writeResolved(resolvedValue, indentation, depth)
}

func errorTooDeep() error {
	return newError("the value is nested more than %d levels deep, so no reader would accept the document", maxDepth)
}

type encodedMember struct {
	key       string
	value     reflect.Value
	omitEmpty bool
}

// members returns the members of a map or a struct at a depth, with the fields that omitzero leaves out already removed. A map's are sorted by key, and a struct's are in their declared order, or sorted by key for canonical form. The list is reused for the next object at that depth.
func (encoder *encoder) members(value reflect.Value, depth int) ([]encodedMember, error) {
	members := takeScratch(&encoder.memberScratch, depth)
	defer func() {
		encoder.memberScratch[depth] = members[:0]
	}()

	if value.Kind() == reflect.Struct {
		fields, err := cachedFields(value.Type())

		if err != nil {
			return nil, newError("%s", err)
		}

		for _, field := range fields.list {
			fieldValue, ok := fieldByIndex(value, field.index)

			// A field of a nil embedded pointer is not there.
			if !ok {
				continue
			}

			if field.omitZero && field.isZero(fieldValue) {
				continue
			}

			members = append(members, encodedMember{key: field.key, value: fieldValue, omitEmpty: field.omitEmpty})
		}

		// A struct's keys are unique, so it needs no check for two members with one key.
		if encoder.isCanonical {
			sortMembers(members)
		}

		return members, nil
	}

	iterator := value.MapRange()

	// One Go key for every entry, because only its text is kept. Iterator.Key would allocate a copy of each.
	mapKey := reflect.New(value.Type().Key()).Elem()

	for iterator.Next() {
		mapKey.SetIterKey(iterator)
		key, err := mapKeyText(mapKey)

		if err != nil {
			return nil, err
		}

		// A struct field's key is checked once, with its type's fields.
		if err := checkRepresentable(key, "key"); err != nil {
			return nil, prependPath(err, key)
		}

		members = append(members, encodedMember{key: key, value: iterator.Value()})
	}

	sortMembers(members)

	for index := 1; index < len(members); index++ {
		if members[index].key == members[index-1].key {
			return nil, newError("two map keys give the same key %s", formatPath([]any{members[index].key}))
		}
	}

	return members, nil
}

// sortMembers sorts members by key. Go compares strings by their UTF-8 bytes, which is the order of Unicode scalar values that canonical form requires.
func sortMembers(members []encodedMember) {
	slices.SortFunc(members, func(left, right encodedMember) int {
		return strings.Compare(left.key, right.key)
	})
}

// fieldByIndex is reflect.Value.FieldByIndex, except that it reports a nil embedded pointer on the way instead of panicking.
// Note: Not using reflect.Value.FieldByIndexErr because it allocates an error for a nil embedded pointer.
func fieldByIndex(value reflect.Value, index []int) (reflect.Value, bool) {
	for position, fieldIndex := range index {
		if position > 0 && value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return reflect.Value{}, false
			}

			value = value.Elem()
		}

		value = value.Field(fieldIndex)
	}

	return value, true
}

// mapKeyText is the SOML key of a map key: the text of an AppendText or MarshalText method, a string, or an integer in decimal, also through pointers and interfaces. MarshalSOML is not used for keys, as Unmarshal does not use UnmarshalSOML for them.
func mapKeyText(key reflect.Value) (string, error) {
	keyType := key.Type()

	for steps := 0; key.Kind() == reflect.Pointer || key.Kind() == reflect.Interface; steps++ {
		if key.IsNil() {
			return "", newError("a map key of type %s is nil", keyType)
		}

		if steps > maxResolveSteps {
			return "", newError("a map key of type %s goes through more than %d pointers and interfaces in a row, which is most likely a cycle", keyType, maxResolveSteps)
		}

		key = key.Elem()
	}

	if text, ok, err := methodsOf(key.Type()).marshalText(key); ok {
		return text, err
	}

	switch key.Kind() {
	case reflect.String:
		return key.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(key.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(key.Uint(), 10), nil
	}

	return "", newError("a map key of type %s cannot be written as SOML; a key must be a string, an integer, or have a MarshalText method", keyType)
}

// writeMembers writes each member on its own line at indentation, and leaves out a member that omitempty removes. It reports whether it wrote any.
func (encoder *encoder) writeMembers(members []encodedMember, indentation, depth int) (bool, error) {
	hasMembers := false

	for _, member := range members {
		start := len(encoder.buffer)
		encoder.buffer = appendTabs(encoder.buffer, indentation)
		isOmitted, err := encoder.writeMember(member, indentation, depth)

		if err != nil {
			return false, err
		}

		if isOmitted {
			encoder.buffer = encoder.buffer[:start]
			continue
		}

		hasMembers = true
		encoder.buffer = append(encoder.buffer, '\n')
	}

	return hasMembers, nil
}

// writeMember writes `key: value`. With omitempty, it reports whether the value is empty, and then the caller removes what it wrote.
func (encoder *encoder) writeMember(member encodedMember, indentation, depth int) (bool, error) {
	// The buffer doubles when it grows, because append grows a large slice by only a quarter, which copies a large document about five times.
	if cap(encoder.buffer)-len(encoder.buffer) < 1024 {
		encoder.buffer = slices.Grow(encoder.buffer, max(4096, cap(encoder.buffer)))
	}

	encoder.buffer = appendKey(encoder.buffer, member.key)
	encoder.buffer = append(encoder.buffer, ": "...)
	valueStart := len(encoder.buffer)

	if err := encoder.writeValue(member.value, indentation, depth); err != nil {
		// An object or an array one level past the nesting limit is an error before its members are written, but when it is empty, omitempty leaves it out, so it adds no level. Whether it is empty is found by writing it again from the top.
		if member.omitEmpty && depth == maxDepth && !encoder.isCheckingEmpty && isWrittenEmpty(member.value) {
			return true, nil
		}

		return false, prependPath(err, member.key)
	}

	// By the text that is written, so that a value from a MarshalSOML or MarshalText method that is empty is left out too.
	if member.omitEmpty {
		switch string(encoder.buffer[valueStart:]) {
		case "null", "''", "[]", "{}":
			return true, nil
		}
	}

	return false, nil
}

// isWrittenEmpty reports whether a value is written as an empty object or array, which omitempty leaves out.
func isWrittenEmpty(value reflect.Value) bool {
	encoder := encoder{isCheckingEmpty: true}
	err := encoder.writeValue(value, 0, 0)
	return err == nil && (string(encoder.buffer) == "{}" || string(encoder.buffer) == "[]")
}

// appendTabs appends count tabs.
// Note: Not using strings.Repeat because it adds overhead at common shallow depths and allocates for deeper indentation.
func appendTabs(buffer []byte, count int) []byte {
	for range count {
		buffer = append(buffer, '\t')
	}

	return buffer
}

// appendKey appends a key: bare when it is non-empty and every character is a letter, a digit, `_`, or `-`, and otherwise as a string.
func appendKey(buffer []byte, key string) []byte {
	if isBareKey(key) {
		return append(buffer, key...)
	}

	return appendString(buffer, key)
}

// needsEscapes reports whether a string needs `"..."`: it has a `'`, a tab, a line feed, another C0 control, or U+007F.
func needsEscapes(text string) bool {
	for index := range len(text) {
		if character := text[index]; character == '\'' || character < 0x20 || character == 0x7F {
			return true
		}
	}

	return false
}

/*
appendString appends a string: `'...'` unless the content has a `'`, a tab, a line feed, another C0 control, or U+007F, and then `"..."` with escapes. A `\` or a `"` alone does not need `"..."`, because a literal string holds both as they are.

Note: Not using strconv.AppendQuote because Go escapes do not match SOML's escapes or canonical quoting.
*/
func appendString(buffer []byte, text string) []byte {
	if !needsEscapes(text) {
		buffer = append(buffer, '\'')
		buffer = append(buffer, text...)
		return append(buffer, '\'')
	}

	buffer = append(buffer, '"')
	chunkStart := 0

	for index := range len(text) {
		character := text[index]
		var escape string

		switch {
		case character == '\\':
			escape = `\\`
		case character == '"':
			escape = `\"`
		case character == '\n':
			escape = `\n`
		case character == '\t':
			escape = `\t`
		case character < 0x20 || character == 0x7F:
			escape = `\u{` + strconv.FormatUint(uint64(character), 16) + `}`
		default:
			continue
		}

		buffer = append(buffer, text[chunkStart:index]...)
		buffer = append(buffer, escape...)
		chunkStart = index + 1
	}

	buffer = append(buffer, text[chunkStart:]...)
	return append(buffer, '"')
}

// zeros is enough zeros for the layouts of appendFloat, which write at most 20 before the decimal point and 5 after it.
const zeros = "00000000000000000000"

/*
appendFloat appends a float in canonical form (spec rule 11): the shortest digits that read back as the same value, laid out like ECMAScript's `Number::toString`, with `.0` when it would otherwise read as an int. bitSize is 32 for a float32, which gets the shortest digits that read back as the same float32.

`strconv` gives the digits. When several shortest digit strings exist, it takes the one closest to the exact value, and of two equally close ones the even one, as the spec requires. The layout is written here, because it is part of the spec.

Note: Not using strconv.AppendFloat with 'g' because its exponent thresholds and spelling differ from canonical form.
*/
func appendFloat(buffer []byte, value float64, bitSize int) ([]byte, error) {
	switch {
	case math.IsNaN(value):
		return nil, newError("NaN is not a SOML value")
	case math.IsInf(value, 1):
		return append(buffer, "infinity"...), nil
	case math.IsInf(value, -1):
		return append(buffer, "-infinity"...), nil
	case value == 0:
		// Zero has one value whatever its sign.
		return append(buffer, "0.0"...), nil
	}

	if value < 0 {
		buffer = append(buffer, '-')
		value = -value
	}

	// The shortest digits in the form d.ddde±nn.
	var scratch [32]byte
	text := strconv.AppendFloat(scratch[:0], value, 'e', -1, bitSize)

	// A reader rounds the digits to a float64 and then to a float32. Rounding twice can give the next float32, so for the rare value where it does, the digits of the float64 are written.
	if bitSize == 32 {
		if parsed, _ := strconv.ParseFloat(string(text), 64); float32(parsed) != float32(value) {
			text = strconv.AppendFloat(scratch[:0], value, 'e', -1, 64)
		}
	}

	exponentIndex := bytes.IndexByte(text, 'e')
	exponent, _ := strconv.Atoi(string(text[exponentIndex+1:]))
	digits := slices.DeleteFunc(text[:exponentIndex], func(character byte) bool {
		return character == '.'
	})

	// The value is 0.d₁d₂…dₖ × 10ⁿ.
	count := len(digits)
	point := exponent + 1

	switch {
	case count <= point && point <= 21:
		buffer = append(buffer, digits...)
		buffer = append(buffer, zeros[:point-count]...)
		buffer = append(buffer, ".0"...)
	case 0 < point && point < count:
		buffer = append(buffer, digits[:point]...)
		buffer = append(buffer, '.')
		buffer = append(buffer, digits[point:]...)
	case -6 < point && point <= 0:
		buffer = append(buffer, "0."...)
		buffer = append(buffer, zeros[:-point]...)
		buffer = append(buffer, digits...)
	default:
		buffer = append(buffer, digits[0])

		if count > 1 {
			buffer = append(buffer, '.')
			buffer = append(buffer, digits[1:]...)
		}

		buffer = append(buffer, 'e')
		buffer = strconv.AppendInt(buffer, int64(point-1), 10)
	}

	return buffer, nil
}

// appendInstant appends an instant in canonical form: in UTC, with `Z`, and with the fractional second only when it is not zero, without trailing zeros.
func appendInstant(buffer []byte, instant time.Time) ([]byte, error) {
	// Note: Not using time.Time.AppendText because the SOML range check already validates the UTC instant.
	if !isInstantInRange(instant) {
		return nil, newError("the time %s is outside the years 0001 to 9999 in UTC, which is the range of a SOML instant", instant.UTC().Format(time.RFC3339Nano))
	}

	return instant.UTC().AppendFormat(buffer, time.RFC3339Nano), nil
}

// appendDuration appends a duration in canonical form: `-` when negative, then the hours, minutes, and seconds, each left out when zero, with the seconds' fraction written like an instant's. Zero is `0s`.
// Note: Not using time.Duration.String because its zero units and subsecond spellings are not canonical SOML.
func appendDuration(buffer []byte, duration time.Duration) []byte {
	if duration == 0 {
		return append(buffer, "0s"...)
	}

	magnitude := uint64(duration)

	if duration < 0 {
		buffer = append(buffer, '-')

		// Negated as a uint64, so that math.MinInt64, whose negation does not fit in an int64, is correct too.
		magnitude = -magnitude
	}

	const second = uint64(time.Second)
	hours := magnitude / (3600 * second)
	minutes := magnitude / (60 * second) % 60
	seconds := magnitude / second % 60
	nanoseconds := magnitude % second

	if hours > 0 {
		buffer = strconv.AppendUint(buffer, hours, 10)
		buffer = append(buffer, 'h')
	}

	if minutes > 0 {
		buffer = strconv.AppendUint(buffer, minutes, 10)
		buffer = append(buffer, 'm')
	}

	if seconds > 0 || nanoseconds > 0 {
		buffer = strconv.AppendUint(buffer, seconds, 10)

		if nanoseconds > 0 {
			var scratch [10]byte

			// Adding a second gives a leading 1 followed by the nanoseconds padded to nine digits, and the 1 is then removed.
			fraction := strconv.AppendUint(scratch[:0], nanoseconds+second, 10)[1:]
			buffer = append(buffer, '.')
			buffer = append(buffer, bytes.TrimRight(fraction, "0")...)
		}

		buffer = append(buffer, 's')
	}

	return buffer
}
