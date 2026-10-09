package soml

// A copy of the language-neutral conformance suite in the soml spec repository (https://github.com/soml-lang/soml/tree/main/conformance), in testdata/conformance. Update it with `../soml/sync-conformance.sh testdata/conformance`. See its readme.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const conformanceDirectory = "testdata/conformance"

// conformanceCases returns the names of the cases in valid or invalid, such as `string/escapes`, without the extension. The canonical and formatted forms of a valid case are not cases.
func conformanceCases(t *testing.T, kind string) []string {
	t.Helper()
	root := filepath.Join(conformanceDirectory, kind)
	var names []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || !strings.HasSuffix(path, ".soml") || strings.HasSuffix(path, ".canonical.soml") || strings.HasSuffix(path, ".formatted.soml") {
			return nil
		}

		name, _ := filepath.Rel(root, strings.TrimSuffix(path, ".soml"))
		names = append(names, filepath.ToSlash(name))
		return nil
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(names) == 0 {
		t.Fatalf("no cases in %s", root)
	}

	return names
}

func readCase(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	return data
}

// tagged writes a decoded value in the tagged JSON form of the suite: a scalar is {"type": …, "value": …}, and objects and arrays are plain JSON.
func tagged(t *testing.T, value any) any {
	t.Helper()

	scalar := func(kind, text string) any {
		return map[string]any{"type": kind, "value": text}
	}

	switch value := value.(type) {
	case nil:
		return scalar("null", "null")
	case bool:
		if value {
			return scalar("bool", "true")
		}

		return scalar("bool", "false")
	case int64:
		return scalar("int", strconv.FormatInt(value, 10))
	case float64:
		text, err := appendFloat(nil, value, 64)

		if err != nil {
			t.Fatal(err)
		}

		return scalar("float", string(text))
	case string:
		return scalar("string", value)
	case time.Time:
		text, err := appendInstant(nil, value)

		if err != nil {
			t.Fatal(err)
		}

		return scalar("instant", string(text))
	case time.Duration:
		return scalar("duration", strconv.FormatInt(int64(value), 10))
	case []any:
		items := make([]any, len(value))

		for index, item := range value {
			items[index] = tagged(t, item)
		}

		return items
	case map[string]any:
		members := make(map[string]any, len(value))

		for key, member := range value {
			members[key] = tagged(t, member)
		}

		return members
	}

	t.Fatalf("unexpected decoded type %T", value)
	return nil
}

func TestConformanceValid(t *testing.T) {
	for _, name := range conformanceCases(t, "valid") {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(conformanceDirectory, "valid", name)
			source := readCase(t, base+".soml")
			canonical := readCase(t, base+".canonical.soml")
			formatted := readCase(t, base+".formatted.soml")

			var expected any

			if err := json.Unmarshal(readCase(t, base+".json"), &expected); err != nil {
				t.Fatal(err)
			}

			var value any

			if err := Unmarshal(source, &value); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			// Through encoding/json, so that both sides have the same Go types.
			actualJSON, err := json.Marshal(tagged(t, value))

			if err != nil {
				t.Fatal(err)
			}

			var actual any

			if err := json.Unmarshal(actualJSON, &actual); err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("value:\n got %s\nwant %s", actualJSON, readCase(t, base+".json"))
			}

			written, err := Marshal(value, Canonical())

			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			if string(written) != string(canonical) {
				t.Fatalf("canonical form:\n got %q\nwant %q", written, canonical)
			}

			// Canonical form reads back to the same value and writes the same bytes.
			var again any

			if err := Unmarshal(canonical, &again); err != nil {
				t.Fatalf("Unmarshal of the canonical form: %v", err)
			}

			rewritten, err := Marshal(again, Canonical())

			if err != nil {
				t.Fatalf("Marshal of the canonical form: %v", err)
			}

			if string(rewritten) != string(canonical) {
				t.Fatalf("canonical form is not a fixed point:\n got %q\nwant %q", rewritten, canonical)
			}

			formattedSource, err := Format(source)

			if err != nil {
				t.Fatalf("Format: %v", err)
			}

			if string(formattedSource) != string(formatted) {
				t.Fatalf("formatted form:\n got %q\nwant %q", formattedSource, formatted)
			}

			// The formatted form reads back to the same value and formats to the same bytes.
			var fromFormatted any

			if err := Unmarshal(formatted, &fromFormatted); err != nil {
				t.Fatalf("Unmarshal of the formatted form: %v", err)
			}

			if !reflect.DeepEqual(tagged(t, fromFormatted), tagged(t, value)) {
				t.Fatalf("the formatted form has another value")
			}

			reformatted, err := Format(formatted)

			if err != nil {
				t.Fatalf("Format of the formatted form: %v", err)
			}

			if string(reformatted) != string(formatted) {
				t.Fatalf("formatted form is not a fixed point:\n got %q\nwant %q", reformatted, formatted)
			}
		})
	}
}

type invalidReason struct {
	Reason string `json:"reason"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// normalizeMessage makes a message comparable with the JS reference's: the JS messages begin with a capital letter, and these begin in lowercase, as Go error strings do. Everything else, including the quotation marks, must be the same.
func normalizeMessage(message string) string {
	if message != "" {
		message = strings.ToLower(message[:1]) + message[1:]
	}

	return message
}

func TestConformanceInvalid(t *testing.T) {
	var reasons map[string]invalidReason

	if err := json.Unmarshal(readCase(t, filepath.Join(conformanceDirectory, "invalid-reasons.json")), &reasons); err != nil {
		t.Fatal(err)
	}

	names := conformanceCases(t, "invalid")

	if len(names) != len(reasons) {
		t.Errorf("%d invalid cases, but %d reasons", len(names), len(reasons))
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			source := readCase(t, filepath.Join(conformanceDirectory, "invalid", name+".soml"))
			reason, ok := reasons[name]

			if !ok {
				t.Fatalf("no reason for %s", name)
			}

			var value any
			err := Unmarshal(source, &value)

			if err == nil {
				t.Fatalf("Unmarshal accepted the document, want %q", reason.Reason)
			}

			somlError, ok := errors.AsType[*Error](err)

			if !ok {
				t.Fatalf("the error is a %T, not an *Error", err)
			}

			if somlError.Line != reason.Line || somlError.Column != reason.Column {
				t.Errorf("position %d:%d, want %d:%d (%q, want %q)", somlError.Line, somlError.Column, reason.Line, reason.Column, somlError.Message, reason.Reason)
			}

			if normalizeMessage(somlError.Message) != normalizeMessage(reason.Reason) {
				t.Errorf("message:\n got %q\nwant %q", somlError.Message, reason.Reason)
			}
		})
	}
}

// untagged returns the value of the tagged JSON form of the suite, as Unmarshal into an `any` gives it. An object becomes a map, whose keys Marshal sorts, so the members of an object in a case must be in sorted order.
func untagged(t *testing.T, value any) any {
	t.Helper()

	switch value := value.(type) {
	case []any:
		items := make([]any, len(value))

		for index, item := range value {
			items[index] = untagged(t, item)
		}

		return items
	case map[string]any:
		kind, isScalar := value["type"].(string)
		text, hasText := value["value"].(string)

		if !isScalar || !hasText || len(value) != 2 {
			members := make(map[string]any, len(value))

			for key, member := range value {
				members[key] = untagged(t, member)
			}

			return members
		}

		switch kind {
		case "string":
			return text
		case "duration":
			nanoseconds, err := strconv.ParseInt(text, 10, 64)

			if err != nil {
				t.Fatal(err)
			}

			return time.Duration(nanoseconds)
		}

		// The other scalars are written in the suite as a document writes them.
		var items []any

		if err := Unmarshal([]byte("["+text+"]"), &items); err != nil {
			t.Fatalf("the %s %q: %v", kind, text, err)
		}

		return items[0]
	}

	t.Fatalf("unexpected tagged value %v", value)
	return nil
}

type editCase struct {
	Document string `json:"document"`
	Path     []any  `json:"path"`
	Value    any    `json:"value"`
	Expected string `json:"expected"`
	Error    bool   `json:"error"`
}

func TestConformanceEdit(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformanceDirectory, "edit", "*.json"))

	if err != nil {
		t.Fatal(err)
	}

	if len(paths) < 40 {
		t.Fatalf("only %d edit cases", len(paths))
	}

	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			var test editCase

			if err := json.Unmarshal(readCase(t, path), &test); err != nil {
				t.Fatal(err)
			}

			// A JSON number is a float64, and an index in a path is an int.
			for index, segment := range test.Path {
				if number, ok := segment.(float64); ok {
					test.Path[index] = int(number)
				}
			}

			if formatted, err := Format([]byte(test.Document)); err != nil || string(formatted) != test.Document {
				t.Fatalf("the document is not formatted: %q %v", formatted, err)
			}

			document, err := Parse([]byte(test.Document))

			if err != nil {
				t.Fatal(err)
			}

			var isRemoved bool

			if test.Value == nil {
				isRemoved, err = document.Remove(test.Path...)
			} else {
				err = document.Set(untagged(t, test.Value), test.Path...)
			}

			if test.Error {
				if err == nil {
					t.Fatalf("no error, and the result %q", document.String())
				}

				if document.String() != test.Document {
					t.Fatalf("the failed change changed the document to %q", document.String())
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			// A removal that leads to nothing changes nothing, and reports that.
			if test.Value == nil && isRemoved != (test.Expected != test.Document) {
				t.Fatalf("Remove reports %v", isRemoved)
			}

			if document.String() != test.Expected {
				t.Fatalf("\n got %q\nwant %q", document.String(), test.Expected)
			}

			if formatted, err := Format([]byte(test.Expected)); err != nil || string(formatted) != test.Expected {
				t.Fatalf("the result is not formatted: %q %v", formatted, err)
			}
		})
	}
}
