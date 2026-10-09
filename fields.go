package soml

// Struct field information: which fields a struct has in SOML, under which keys, with which options. It follows the rules of `encoding/json` for embedded structs, and it is computed once per type.

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

type field struct {
	key       string
	isTagged  bool
	index     []int
	omitZero  bool
	omitEmpty bool

	// isZero reports whether the field's value is zero, for omitzero. It uses an IsZero method when the type has one.
	isZero func(reflect.Value) bool
}

type structFields struct {
	// The fields in the order they are declared in, as encoding/json writes them, with the fields of an embedded struct where it is.
	list []field

	byKey map[string]*field
}

type fieldsResult struct {
	fields *structFields
	err    error
}

var fieldCache sync.Map // map[reflect.Type]fieldsResult

// cachedFields returns the SOML fields of a struct type, or an error for an invalid `soml` tag.
func cachedFields(structType reflect.Type) (*structFields, error) {
	if cached, ok := fieldCache.Load(structType); ok {
		result := cached.(fieldsResult)
		return result.fields, result.err
	}

	fields, err := typeFields(structType)
	cached, _ := fieldCache.LoadOrStore(structType, fieldsResult{fields, err})
	result := cached.(fieldsResult)
	return result.fields, result.err
}

/*
How a type has one of the methods that encode or decode it: not at all, with a value receiver, or with a pointer receiver.
*/
type receiver uint8

const (
	noMethod receiver = iota
	valueMethod
	pointerMethod
)

func receiverOf(valueType, method reflect.Type) receiver {
	switch {
	case valueType.Implements(method):
		return valueMethod
	case reflect.PointerTo(valueType).Implements(method):
		return pointerMethod
	}

	return noMethod
}

/*
The methods of a type that encode or decode it, which are looked up once per type, because a method lookup is slow.
*/
type typeMethods struct {
	marshaler, textAppender, textMarshaler, unmarshaler, textUnmarshaler receiver

	// Whether the type is part of a syntax tree, which holds its state in unexported fields, so it cannot be written or read as a value.
	isTree bool
}

// encodes reports whether the type has a method that encodes it.
func (methods *typeMethods) encodes() bool {
	return methods.marshaler != noMethod || methods.textAppender != noMethod || methods.textMarshaler != noMethod
}

// decodes reports whether the type has a method that decodes it.
func (methods *typeMethods) decodes() bool {
	return methods.unmarshaler != noMethod || methods.textUnmarshaler != noMethod
}

var methodCache sync.Map // map[reflect.Type]*typeMethods

// noMethods is the methods of a type that has none.
var noMethods = &typeMethods{}

func methodsOf(valueType reflect.Type) *typeMethods {
	// A predeclared type, such as int, and a type without a name, such as []string, have no methods. Of the types without a name, only a struct (from an embedded field), a pointer, and an interface can have them.
	if kind := valueType.Kind(); kind != reflect.Struct && kind != reflect.Pointer && kind != reflect.Interface && valueType.PkgPath() == "" {
		return noMethods
	}

	if cached, ok := methodCache.Load(valueType); ok {
		return cached.(*typeMethods)
	}

	methods := &typeMethods{
		marshaler:       receiverOf(valueType, marshalerType),
		textAppender:    receiverOf(valueType, textAppenderType),
		textMarshaler:   receiverOf(valueType, textMarshalerType),
		unmarshaler:     receiverOf(valueType, unmarshalerType),
		textUnmarshaler: receiverOf(valueType, textUnmarshalerType),
		isTree:          slices.Contains(treeTypes, valueType),
	}

	cached, _ := methodCache.LoadOrStore(valueType, methods)
	return cached.(*typeMethods)
}

type isZeroer interface {
	IsZero() bool
}

var isZeroerType = reflect.TypeFor[isZeroer]()

// typeFields finds the fields of a struct type: its own exported fields and those of its embedded structs, with Go's rules for which of two fields with one key wins.
// Note: Not using reflect.VisibleFields because it resolves Go names before soml tags, which can hide distinct tagged fields.
func typeFields(structType reflect.Type) (*structFields, error) {
	type queued struct {
		structType reflect.Type
		index      []int
	}

	// The embedded structs to look at on this level and the next.
	current := []queued{}
	next := []queued{{structType: structType}}

	// How many times each struct type is queued on this level and the next.
	var count, nextCount map[reflect.Type]int

	visited := map[reflect.Type]bool{}
	var fields []field

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}

		for _, embedded := range current {
			// A struct type is looked at only once, at its shallowest depth, so that a cycle of embedded pointers ends.
			if visited[embedded.structType] {
				continue
			}

			visited[embedded.structType] = true

			for fieldIndex := range embedded.structType.NumField() {
				structField := embedded.structType.Field(fieldIndex)

				embeddedType := structField.Type

				if embeddedType.Kind() == reflect.Pointer {
					embeddedType = embeddedType.Elem()
				}

				// An embedded unexported struct may still have exported fields. Any other unexported field is left out.
				if !structField.IsExported() && !(structField.Anonymous && embeddedType.Kind() == reflect.Struct) {
					if tag, ok := structField.Tag.Lookup("soml"); ok && tag != "-" {
						return nil, fmt.Errorf("the unexported field %s of %s has a soml tag; only an exported field can be in SOML", structField.Name, structType)
					}

					continue
				}

				tag := structField.Tag.Get("soml")

				if tag == "-" {
					continue
				}

				// `-,` is the key `-`, as in encoding/json, because `-` alone leaves the field out.
				if strings.HasSuffix(tag, ",") && tag != "-," {
					return nil, fmt.Errorf("the soml tag of the field %s of %s ends with a comma", structField.Name, structType)
				}

				key, options, _ := strings.Cut(tag, ",")

				// With a key, an embedded field is a field of its own, so it must be exported, as any other field with a soml tag.
				if structField.Anonymous && !structField.IsExported() && key != "" {
					return nil, fmt.Errorf("the unexported embedded field %s of %s has a key in its soml tag; only an exported field can be in SOML", structField.Name, structType)
				}

				omitZero, omitEmpty := false, false

				if options != "" {
					for option := range strings.SplitSeq(options, ",") {
						switch option {
						case "omitzero":
							if omitZero {
								return nil, fmt.Errorf("the soml tag of the field %s of %s has the option %q twice", structField.Name, structType, option)
							}

							omitZero = true
						case "omitempty":
							if omitEmpty {
								return nil, fmt.Errorf("the soml tag of the field %s of %s has the option %q twice", structField.Name, structType, option)
							}

							omitEmpty = true
						default:
							return nil, fmt.Errorf("the soml tag of the field %s of %s has the unknown option %q; the options are omitzero and omitempty", structField.Name, structType, option)
						}
					}
				}

				if checkRepresentable(key, "key") != nil {
					return nil, fmt.Errorf("the soml tag of the field %s of %s has a key that SOML cannot represent", structField.Name, structType)
				}

				// A clone, because every field of the embedded struct appends to its index, which could share spare capacity.
				index := append(slices.Clone(embedded.index), fieldIndex)
				fieldType := structField.Type

				if fieldType.Name() == "" && fieldType.Kind() == reflect.Pointer {
					fieldType = fieldType.Elem()
				}

				if key != "" || !structField.Anonymous || fieldType.Kind() != reflect.Struct {
					isTagged := key != ""

					if key == "" {
						key = structField.Name
					}

					fields = append(fields, field{
						key:       key,
						isTagged:  isTagged,
						index:     index,
						omitZero:  omitZero,
						omitEmpty: omitEmpty,
						isZero:    zeroFunction(structField.Type),
					})

					// When the struct is embedded more than once on this level, a second copy makes the field ambiguous below.
					if count[embedded.structType] > 1 {
						fields = append(fields, fields[len(fields)-1])
					}

					continue
				}

				nextCount[fieldType]++

				if nextCount[fieldType] == 1 {
					next = append(next, queued{structType: fieldType, index: index})
				}
			}
		}
	}

	// By key, then by depth, then tagged first, then by position.
	slices.SortFunc(fields, func(left, right field) int {
		return cmp.Or(
			strings.Compare(left.key, right.key),
			cmp.Compare(len(left.index), len(right.index)),
			compareTagged(left.isTagged, right.isTagged),
			slices.Compare(left.index, right.index),
		)
	})

	// Of the fields with one key, keep the one that Go's rules for embedded fields make visible, or none when it is ambiguous.
	result := &structFields{byKey: map[string]*field{}}

	for start := 0; start < len(fields); {
		end := start + 1

		for end < len(fields) && fields[end].key == fields[start].key {
			end++
		}

		if dominant, ok := dominantField(fields[start:end]); ok {
			result.list = append(result.list, dominant)
		}

		start = end
	}

	// Back to the order the fields are declared in, as in encoding/json.
	slices.SortFunc(result.list, func(left, right field) int {
		return slices.Compare(left.index, right.index)
	})

	for index := range result.list {
		result.byKey[result.list[index].key] = &result.list[index]
	}

	return result, nil
}

func compareTagged(left, right bool) int {
	switch {
	case left == right:
		return 0
	case left:
		return -1
	}

	return 1
}

// dominantField returns the field that wins among fields with one key, which are sorted by depth and then tagged first. The shallowest wins, and of several at that depth, the only tagged one. Otherwise no field wins.
func dominantField(fields []field) (field, bool) {
	if len(fields) > 1 && len(fields[0].index) == len(fields[1].index) && fields[0].isTagged == fields[1].isTagged {
		return field{}, false
	}

	return fields[0], true
}

// zeroFunction returns how omitzero decides whether a value of the type is zero: with its IsZero method when it has one, and otherwise with reflect.Value.IsZero.
func zeroFunction(valueType reflect.Type) func(reflect.Value) bool {
	switch {
	case valueType.Kind() == reflect.Interface && valueType.Implements(isZeroerType):
		return func(value reflect.Value) bool {
			// A nil interface, or an interface that holds a nil pointer, cannot call IsZero.
			return value.IsNil() || value.Elem().Kind() == reflect.Pointer && value.Elem().IsNil() || value.Interface().(isZeroer).IsZero()
		}
	case valueType.Kind() == reflect.Pointer && valueType.Implements(isZeroerType):
		return func(value reflect.Value) bool {
			// A nil pointer is zero without a call, because an IsZero method may not handle a nil receiver.
			return value.IsNil() || value.Interface().(isZeroer).IsZero()
		}
	case valueType.Implements(isZeroerType):
		return func(value reflect.Value) bool {
			zeroer, _ := reflect.TypeAssert[isZeroer](value)
			return zeroer.IsZero()
		}
	case reflect.PointerTo(valueType).Implements(isZeroerType):
		return func(value reflect.Value) bool {
			return addressable(value).Addr().Interface().(isZeroer).IsZero()
		}
	}

	return reflect.Value.IsZero
}
