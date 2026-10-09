package soml

import (
	"encoding"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"time"
)

/*
Unmarshaler is the interface of a type that decodes itself. UnmarshalSOML gets the value as Unmarshal into an `any` gives it: a map[string]any, a []any, a string, an int64, a float64, a bool, a time.Time, a time.Duration, or nil for null. An error it returns is reported at the position of the value.
*/
type Unmarshaler interface {
	UnmarshalSOML(value any) error
}

/*
Option changes how Marshal or Unmarshal works. An option for one of them has no effect on the other.
*/
type Option struct {
	apply func(*settings)
}

type settings struct {
	rejectUnknownKeys bool
	isCanonical       bool
}

// settingsOf returns the settings that options give.
func settingsOf(options []Option) settings {
	var result settings

	for _, option := range options {
		// A zero Option does nothing.
		if option.apply != nil {
			option.apply(&result)
		}
	}

	return result
}

/*
RejectUnknownKeys makes a key that no struct field has an error, at the position of the key. Without it, such a key is ignored, as in encoding/json. It applies to structs only, because a map takes every key. It has no effect on Marshal.
*/
func RejectUnknownKeys() Option {
	return Option{apply: func(settings *settings) {
		settings.rejectUnknownKeys = true
	}}
}

/*
Unmarshal decodes a SOML document into the value that out points to. It checks every rule of the spec first, so a document that is not valid SOML is an error, also when out would not need the part that is wrong.

SOML values map to Go like this:

  - into an `any`: an object is a map[string]any, an array a []any, a string a string, an int an int64, a float a float64, a bool a bool, an instant a time.Time in UTC, a duration a time.Duration, and null is nil
  - string → string types, []byte and [N]byte (from base64), and types with an UnmarshalText method
  - int → integer types, range-checked, and float types when the int converts exactly
  - float → float types. A float never decodes into an integer type.
  - bool → bool types
  - instant → time.Time, in UTC
  - duration → time.Duration
  - null → pointers, interfaces, maps, and slices, which become nil. For any other type, null is an error, so use a pointer to accept it.
  - array → slices, and arrays of exactly that length
  - object → structs and maps. Map keys can be strings, integers (from decimal keys such as 404), types with an UnmarshalText method, an empty interface (which gets the key as a string), and pointers to them. UnmarshalSOML is not used for keys.
  - any value → a type with an UnmarshalSOML method

A struct field that the document does not set keeps its value, so a struct can be filled with defaults before it is decoded into. A map is not cleared, also one that an interface holds, as in a map[string]any, and an existing map entry is decoded into, as a struct field is, so its fields that the document does not set keep their values too. An interface that holds a non-nil pointer is decoded into what the pointer points to. Any other value that an interface holds is replaced, and so is a slice. Struct fields match keys exactly, with no case folding. See Marshal for the `soml` tag.

Every error is an *Error. When it is about a place in the document, it has a line and a column.
*/
func Unmarshal(data []byte, out any, options ...Option) error {
	pointer, err := outPointer("Unmarshal", out)

	if err != nil {
		return err
	}

	return unmarshal(string(data), pointer, options)
}

/*
UnmarshalRead decodes a SOML document that it reads from reader to its end, as Unmarshal does. An error from the reader is an *Error without a position, whose Err is that error.

Reading takes memory in proportion to the input, so limit the size of input from an untrusted source, such as with http.MaxBytesReader for a request body. Do not use io.LimitReader for it: it cuts a document short without an error, and a document cut at a line break can still be valid.
*/
func UnmarshalRead(reader io.Reader, out any, options ...Option) error {
	pointer, err := outPointer("UnmarshalRead", out)

	if err != nil {
		return err
	}

	data, err := io.ReadAll(reader)

	if err != nil {
		result := newError("cannot read the document: %s", err)
		result.Err = err
		return result
	}

	return unmarshal(string(data), pointer, options)
}

// outPointer checks that the out argument of a function is a non-nil pointer.
func outPointer(function string, out any) (reflect.Value, error) {
	pointer := reflect.ValueOf(out)

	if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
		return reflect.Value{}, newError("%s needs a non-nil pointer, not %s", function, describeType(pointer))
	}

	return pointer, nil
}

func unmarshal(source string, pointer reflect.Value, options []Option) error {
	root, err := parse(source)

	if err != nil {
		return err
	}

	decoder := decoder{source: source, settings: settingsOf(options)}
	return decoder.decode(root, pointer.Elem())
}

type decoder struct {
	source string
	settings
}

// maxPreallocatedBytes is the most memory that decoding into a slice takes before its items decode, or one element when an element is larger.
const maxPreallocatedBytes = 1 << 20

var (
	unmarshalerType     = reflect.TypeFor[Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func (decoder *decoder) errorAt(offset int, format string, arguments ...any) error {
	return errorAt(decoder.source, offset, fmt.Sprintf(format, arguments...))
}

// methodError reports an error from a method of the target type at the position of the value.
func (decoder *decoder) methodError(offset int, method string, valueType reflect.Type, err error) error {
	result := errorAt(decoder.source, offset, fmt.Sprintf("%s of %s: %s", method, valueType, err))
	result.Err = err
	return result
}

// describe describes a value for an error message, such as `the string “three”` or `an object`.
func (value *node) describe() string {
	switch value.kind {
	case kindString:
		return "the string " + quote(value.text)
	case kindInt:
		return "the int " + strconv.FormatInt(value.integer, 10)
	case kindFloat:
		text, _ := appendFloat(nil, value.float(), 64)
		return "the float " + string(text)
	case kindBool:
		return "the bool " + strconv.FormatBool(value.integer == 1)
	case kindNull:
		return "null"
	case kindInstant:
		text, _ := appendInstant(nil, value.instant())
		return "the instant " + string(text)
	case kindDuration:
		return "the duration " + string(appendDuration(nil, time.Duration(value.integer)))
	case kindArray:
		return "an array"
	}

	return "an object"
}

func (decoder *decoder) mismatch(value *node, target reflect.Type) error {
	hint := ""

	switch {
	case value.kind == kindNull:
		hint = "; use a pointer to accept null"

	// The most common mistakes for a duration and an instant: quotes, and a number without a unit.
	case target == durationType && value.kind == kindString:
		hint = "; a duration is written without quotes, as in 30s"
	case target == durationType && (value.kind == kindInt || value.kind == kindFloat):
		hint = "; a duration needs a unit, as in 30s"
	case target == timeType && value.kind == kindString:
		hint = "; an instant is written without quotes, as in 2026-09-19T14:00:00Z"

	case value.kind == kindFloat && reflect.Int <= target.Kind() && target.Kind() <= reflect.Uintptr && methodsOf(target).textUnmarshaler == noMethod:
		hint = "; a float never decodes into an integer type"
	}

	return decoder.errorAt(value.offset, "cannot decode %s into a Go value of type %s%s", value.describe(), target, hint)
}

// decode decodes a node into target, which can be set.
func (decoder *decoder) decode(value node, target reflect.Value) error {
	// Pointers are followed, and made when they are nil, so that the methods below are found through the address of the value. An interface that holds a pointer is followed too, and decoded into what it points to, as in encoding/json.
	// With null, an interface that holds a pointer is not followed, so the interface itself becomes nil.
	for steps := 0; target.Kind() == reflect.Pointer || holdsPointer(target) && value.kind != kindNull; steps++ {
		if steps > maxResolveSteps {
			return decoder.errorAt(value.offset, "a Go value of type %s goes through more than %d pointers and interfaces in a row, which is most likely a cycle", target.Type(), maxResolveSteps)
		}

		if target.Kind() == reflect.Interface {
			target = target.Elem()
			continue
		}

		if value.kind == kindNull {
			target.SetZero()
			return nil
		}

		if target.IsNil() {
			target.Set(reflect.New(target.Type().Elem()))
		}

		target = target.Elem()
	}

	targetType := target.Type()

	// Before the methods, because time.Time has an UnmarshalText method, which would take a string and not an instant.
	switch targetType {
	case timeType:
		if value.kind != kindInstant {
			return decoder.mismatch(&value, targetType)
		}

		// Through its address, because reflect.ValueOf would allocate a copy.
		*target.Addr().Interface().(*time.Time) = value.instant()
		return nil
	case durationType:
		if value.kind != kindDuration {
			return decoder.mismatch(&value, targetType)
		}

		target.SetInt(value.integer)
		return nil
	}

	if targetType.Kind() == reflect.Interface {
		return decoder.decodeInterface(value, target)
	}

	methods := methodsOf(targetType)

	if methods.isTree {
		return decoder.errorAt(value.offset, "cannot decode into a Go value of type %s, which is part of a syntax tree; use Parse to read a document into a tree", targetType)
	}

	// Before the check for null, so that UnmarshalSOML gets nil for null, as its documentation says.
	if methods.unmarshaler != noMethod {
		if err := target.Addr().Interface().(Unmarshaler).UnmarshalSOML(value.toAny()); err != nil {
			return decoder.methodError(value.offset, "UnmarshalSOML", targetType, err)
		}

		return nil
	}

	// Null is nil for a slice or a map, also for one with an UnmarshalText method, such as a net.IP.
	if value.kind == kindNull {
		switch targetType.Kind() {
		case reflect.Map, reflect.Slice:
			target.SetZero()
			return nil
		}

		return decoder.mismatch(&value, targetType)
	}

	if methods.textUnmarshaler != noMethod {
		if value.kind != kindString {
			return decoder.mismatch(&value, targetType)
		}

		if err := target.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(value.text)); err != nil {
			return decoder.methodError(value.offset, "UnmarshalText", targetType, err)
		}

		return nil
	}

	switch targetType.Kind() {
	case reflect.Bool:
		if value.kind != kindBool {
			return decoder.mismatch(&value, targetType)
		}

		target.SetBool(value.integer == 1)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.kind != kindInt {
			return decoder.mismatch(&value, targetType)
		}

		if target.OverflowInt(value.integer) {
			return decoder.errorAt(value.offset, "the int %d does not fit in a Go value of type %s", value.integer, targetType)
		}

		target.SetInt(value.integer)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.kind != kindInt {
			return decoder.mismatch(&value, targetType)
		}

		if value.integer < 0 || target.OverflowUint(uint64(value.integer)) {
			return decoder.errorAt(value.offset, "the int %d does not fit in a Go value of type %s", value.integer, targetType)
		}

		target.SetUint(uint64(value.integer))
	case reflect.Float32, reflect.Float64:
		return decoder.decodeFloat(value, target)
	case reflect.String:
		if value.kind != kindString {
			return decoder.mismatch(&value, targetType)
		}

		target.SetString(value.text)
	case reflect.Slice:
		if value.kind == kindString && isBytes(targetType) {
			// Note: Not using base64.AppendDecode because there is no destination to reuse and empty input must yield a non-nil slice.
			data, err := base64.StdEncoding.DecodeString(value.text)

			if err != nil {
				return decoder.errorAt(value.offset, "the string is not valid base64, which a Go value of type %s needs", targetType)
			}

			target.SetBytes(data)
			return nil
		}

		if value.kind != kindArray {
			return decoder.mismatch(&value, targetType)
		}

		// The slice is made at its full length when that takes at most a megabyte, and otherwise it grows as its items decode, as with append, from one element when an element is larger than a megabyte. So a short document of many items cannot make a large allocation, for a large element type, before its first item fails.
		length := len(value.collection.items)

		// An element of size zero, such as struct{}, takes no memory, so it needs no limit.
		if size := targetType.Elem().Size(); size > 0 && uintptr(length) > maxPreallocatedBytes/size {
			length = int(maxPreallocatedBytes / size)
		}

		// Its items are decoded in place. Only past the preallocated length does it grow, using one addressable slice header for Grow and SetLen.
		slice := reflect.MakeSlice(targetType, length, length)

		for index, item := range value.collection.items {
			if index == slice.Len() {
				slice = addressable(slice)
				slice.Grow(1)
				slice.SetLen(index + 1)
			}

			if err := decoder.decode(item, slice.Index(index)); err != nil {
				return prependPath(err, index)
			}
		}

		target.Set(slice)
	case reflect.Array:
		if value.kind == kindString && isBytes(targetType) {
			data, err := base64.StdEncoding.DecodeString(value.text)

			if err != nil || len(data) != target.Len() {
				return decoder.errorAt(value.offset, "the string is not %d bytes in base64, which a Go value of type %s needs", target.Len(), targetType)
			}

			// Note: Not using reflect.Copy because arrays of named byte elements need not have byte as their exact element type.
			copy(target.Bytes(), data)
			return nil
		}

		if value.kind != kindArray {
			return decoder.mismatch(&value, targetType)
		}

		if len(value.collection.items) != target.Len() {
			return decoder.errorAt(value.offset, "the array has %d items, but a Go value of type %s has %d", len(value.collection.items), targetType, target.Len())
		}

		for index, item := range value.collection.items {
			element := target.Index(index)

			// An array is replaced, as a slice is, so an item does not merge into the value that was there.
			element.SetZero()

			if err := decoder.decode(item, element); err != nil {
				return prependPath(err, index)
			}
		}
	case reflect.Map:
		if value.kind != kindObject {
			return decoder.mismatch(&value, targetType)
		}

		return decoder.decodeMap(value.collection, target)
	case reflect.Struct:
		if value.kind != kindObject {
			return decoder.mismatch(&value, targetType)
		}

		return decoder.decodeStruct(value.collection, target)
	default:
		return decoder.errorAt(value.offset, "cannot decode into a Go value of type %s, which SOML has no value for", targetType)
	}

	return nil
}

func (decoder *decoder) decodeFloat(value node, target reflect.Value) error {
	targetType := target.Type()
	var number float64

	switch value.kind {
	case kindFloat:
		number = value.float()

		// A float32 can hold fewer values, so a value that rounds to infinity or to zero there is an error, as it is for a float64 in the spec. It is rounded from the digits in the source, because rounding the float64 again can give the wrong float32 for a long literal near the midpoint of two of them.
		if targetType.Kind() == reflect.Float32 && !math.IsInf(number, 0) {
			end := value.offset

			for isNumberByte(at(decoder.source, end)) {
				end++
			}

			rounded := float32(roundFloat(decoder.source[value.offset:end], 32))

			switch {
			case math.IsInf(float64(rounded), 0):
				return decoder.errorAt(value.offset, "%s is too large for a Go value of type %s", value.describe(), targetType)
			case rounded == 0 && number != 0:
				return decoder.errorAt(value.offset, "%s is too small for a Go value of type %s, which would hold it as zero", value.describe(), targetType)
			}

			// Negative zero is the same value as zero, as the parser reads it.
			if rounded != 0 {
				number = float64(rounded)
			}
		}
	case kindInt:
		// Only when the int converts exactly, so that no digit is lost.
		number = float64(value.integer)

		if targetType.Kind() == reflect.Float32 {
			number = float64(float32(number))
		}

		// The range is checked first, because a float64 of 2^63 or more does not fit in an int64, and Go leaves the result of that conversion to the platform.
		if number >= math.MaxInt64 || int64(number) != value.integer {
			return decoder.errorAt(value.offset, "the int %d cannot be a Go value of type %s exactly; write it as a float", value.integer, targetType)
		}
	default:
		return decoder.mismatch(&value, targetType)
	}

	target.SetFloat(number)
	return nil
}

// holdsPointer reports whether a value is an interface that holds a pointer that is not nil.
func holdsPointer(value reflect.Value) bool {
	return value.Kind() == reflect.Interface && !value.IsNil() && value.Elem().Kind() == reflect.Pointer && !value.Elem().IsNil()
}

func (decoder *decoder) decodeInterface(value node, target reflect.Value) error {
	if value.kind == kindNull {
		target.SetZero()
		return nil
	}

	// A map in an interface is decoded into, so that maps merge like structs do, as in encoding/json/v2. It is decoded as a copy that can be set, with every method of its type, and then put back.
	if !target.IsNil() && target.Elem().Kind() == reflect.Map && !target.Elem().IsNil() && value.kind == kindObject {
		existing := reflect.New(target.Elem().Type()).Elem()
		existing.Set(target.Elem())
		err := decoder.decode(value, existing)
		target.Set(existing)
		return err
	}

	if target.NumMethod() != 0 {
		if !target.IsNil() {
			return decoder.errorAt(value.offset, "cannot decode %s into an interface of type %s that holds a %s, because only a non-nil pointer or map in an interface is decoded into", value.describe(), target.Type(), target.Elem().Type())
		}

		return decoder.errorAt(value.offset, "cannot decode %s into a nil interface of type %s, because there is no concrete type to make", value.describe(), target.Type())
	}

	target.Set(reflect.ValueOf(value.toAny()))
	return nil
}

func (decoder *decoder) decodeMap(object *collection, target reflect.Value) error {
	targetType := target.Type()
	keyType := targetType.Key()

	if target.IsNil() {
		target.Set(reflect.MakeMapWithSize(targetType, len(object.members)))
	}

	// One element for every member, because SetMapIndex copies it, and one key too when it is a plain string.
	element := reflect.New(targetType.Elem()).Elem()
	var stringKey reflect.Value

	if keyType.Kind() == reflect.String && methodsOf(keyType).textUnmarshaler == noMethod {
		stringKey = reflect.New(keyType).Elem()
	}

	for index := range object.members {
		member := &object.members[index]
		key := stringKey

		if key.IsValid() {
			key.SetString(member.key)
		} else {
			var err error
			key, err = decoder.mapKey(member, keyType, 0)

			if err != nil {
				return prependPath(err, member.key)
			}
		}

		element.SetZero()

		// An existing entry is decoded into, so that maps merge like structs do. This holds in a map that was nil too, because two keys can give one Go key, such as with an UnmarshalText method that normalizes them.
		if existing := target.MapIndex(key); existing.IsValid() {
			element.Set(existing)
		}

		if err := decoder.decode(member.value, element); err != nil {
			return prependPath(err, member.key)
		}

		target.SetMapIndex(key, element)
	}

	return nil
}

// mapKey decodes the key of a member into a Go map key of keyType: with an UnmarshalText method, as a string, as an integer in decimal, or into a pointer to one of them. MarshalSOML and UnmarshalSOML are not used for keys, as encoding/json uses only the text methods, so that every key type reads back what Marshal writes.
//
// pointers is the number of pointers followed to keyType, so that a pointer type that points to itself is an error.
func (decoder *decoder) mapKey(member *member, keyType reflect.Type, pointers int) (reflect.Value, error) {
	if keyType.Kind() == reflect.Pointer {
		if pointers > maxResolveSteps {
			return reflect.Value{}, decoder.errorAt(member.keyOffset, "a Go map key of type %s goes through more than %d pointers in a row, which is most likely a cycle", keyType, maxResolveSteps)
		}

		element, err := decoder.mapKey(member, keyType.Elem(), pointers+1)

		if err != nil {
			return reflect.Value{}, err
		}

		pointer := reflect.New(keyType.Elem())
		pointer.Elem().Set(element)
		return pointer, nil
	}

	// An interface key can hold only the key's string, so only an empty one can be decoded into.
	if keyType.Kind() == reflect.Interface {
		if keyType.NumMethod() != 0 {
			return reflect.Value{}, decoder.errorAt(member.keyOffset, "cannot decode a key into a Go map key of type %s, because there is no concrete type to make", keyType)
		}

		return reflect.ValueOf(member.key), nil
	}

	key := reflect.New(keyType)

	if methodsOf(keyType).textUnmarshaler != noMethod {
		if err := key.Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(member.key)); err != nil {
			return reflect.Value{}, decoder.methodError(member.keyOffset, "UnmarshalText", keyType, err)
		}

		return key.Elem(), nil
	}

	key = key.Elem()

	switch keyType.Kind() {
	case reflect.String:
		key.SetString(member.key)
		return key, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number, err := strconv.ParseInt(member.key, 10, 64)

		if err != nil || strconv.FormatInt(number, 10) != member.key || key.OverflowInt(number) {
			return reflect.Value{}, decoder.errorAt(member.keyOffset, "expected the key %s to be an integer in decimal that fits in %s, like 404", quote(member.key), keyType)
		}

		key.SetInt(number)
		return key, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		number, err := strconv.ParseUint(member.key, 10, 64)

		if err != nil || strconv.FormatUint(number, 10) != member.key || key.OverflowUint(number) {
			return reflect.Value{}, decoder.errorAt(member.keyOffset, "expected the key %s to be an integer in decimal that fits in %s, like 404", quote(member.key), keyType)
		}

		key.SetUint(number)
		return key, nil
	}

	return reflect.Value{}, decoder.errorAt(member.keyOffset, "cannot decode a key into a Go map key of type %s; a key must be a string, an integer, or have an UnmarshalText method", keyType)
}

func (decoder *decoder) decodeStruct(object *collection, target reflect.Value) error {
	fields, err := cachedFields(target.Type())

	if err != nil {
		return newError("%s", err)
	}

	for index := range object.members {
		member := &object.members[index]
		field, ok := fields.byKey[member.key]

		if !ok {
			if decoder.rejectUnknownKeys {
				return prependPath(decoder.errorAt(member.keyOffset, "unknown key %s; a Go value of type %s has no field for it", formatPath([]any{member.key}), target.Type()), member.key)
			}

			continue
		}

		fieldValue, err := decoder.settableField(target, field, member.keyOffset)

		if err != nil {
			return prependPath(err, member.key)
		}

		if err := decoder.decode(member.value, fieldValue); err != nil {
			return prependPath(err, member.key)
		}
	}

	return nil
}

// settableField returns a struct field to decode into, and makes the embedded structs on the way to it when they are nil pointers.
func (decoder *decoder) settableField(target reflect.Value, field *field, offset int) (reflect.Value, error) {
	value := target

	for position, fieldIndex := range field.index {
		if position > 0 && value.Kind() == reflect.Pointer {
			if value.IsNil() {
				if !value.CanSet() {
					return reflect.Value{}, decoder.errorAt(offset, "cannot set the field %s, because it is in an embedded pointer to an unexported struct type %s", field.key, value.Type().Elem())
				}

				value.Set(reflect.New(value.Type().Elem()))
			}

			value = value.Elem()
		}

		value = value.Field(fieldIndex)
	}

	return value, nil
}

// toAny is the plain Go value of a node, as Unmarshal into an `any` gives it.
func (value *node) toAny() any {
	switch value.kind {
	case kindNull:
		return nil
	case kindBool:
		return value.integer == 1
	case kindInt:
		return value.integer
	case kindFloat:
		return value.float()
	case kindString:
		return value.text
	case kindInstant:
		return value.instant()
	case kindDuration:
		return time.Duration(value.integer)
	case kindArray:
		items := make([]any, len(value.collection.items))

		for index := range value.collection.items {
			items[index] = value.collection.items[index].toAny()
		}

		return items
	}

	members := make(map[string]any, len(value.collection.members))

	for index := range value.collection.members {
		members[value.collection.members[index].key] = value.collection.members[index].value.toAny()
	}

	return members
}
