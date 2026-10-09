package soml

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// delimitedComment is the text of a comment with its delimiters, as it is in the document.
func delimitedComment(comment Comment) string {
	if comment.IsBlock {
		return "/*" + comment.Text + "*/"
	}

	return "#" + comment.Text
}

func mustParse(t *testing.T, text string) *Document {
	t.Helper()
	document, err := Parse([]byte(text))

	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}

	return document
}

func TestTreePrintsEveryValidCorpusCaseBackUnchanged(t *testing.T) {
	for _, name := range conformanceCases(t, "valid") {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{name + ".soml", name + ".canonical.soml", name + ".formatted.soml"} {
				source := readCase(t, filepath.Join(conformanceDirectory, "valid", path))
				document := mustParse(t, string(source))

				if printed := document.String(); printed != string(source) {
					t.Fatalf("%s printed:\n%q\nwant\n%q", path, printed, source)
				}

				// The tree has the same value as Unmarshal gives.
				var fromTree, fromText any

				if err := document.Decode(&fromTree); err != nil {
					t.Fatal(err)
				}

				if err := Unmarshal(source, &fromText); err != nil {
					t.Fatal(err)
				}

				if !reflect.DeepEqual(fromTree, fromText) {
					t.Fatalf("%s: the tree decodes to %v, want %v", path, fromTree, fromText)
				}
			}
		})
	}
}

func TestTreeRejectsAnInvalidDocumentWithTheSameError(t *testing.T) {
	text := []byte("a: 1\na: 2")
	_, treeError := Parse(text)
	var value any
	unmarshalError := Unmarshal(text, &value)

	if treeError == nil || treeError.Error() != unmarshalError.Error() {
		t.Fatalf("Parse gave %v, want %v", treeError, unmarshalError)
	}
}

func TestTreePrintsADocumentThatEndsInACommentUnchanged(t *testing.T) {
	for _, text := range []string{"a: 1 # end", "a: 1\n# end", "[1] # end", "{a: 1} /* end */"} {
		if printed := mustParse(t, text).String(); printed != text {
			t.Errorf("printed %q, want %q", printed, text)
		}
	}
}

func TestTreePrintsContainersWithoutCommasUnchanged(t *testing.T) {
	for _, text := range []string{"a: [\n\t80\n\t443\n]\n", "{\n\ta: 1 # x\n\tb: 2\n}", "[1, 2\n3]", "[1,\n2]", "[1 /* a\nb */\n2]", "[\n\t1\n\n\t# c\n\t2\n]"} {
		if printed := mustParse(t, text).String(); printed != text {
			t.Errorf("printed %q as %q", text, printed)
		}
	}
}

func TestTreeAddsACommaOnlyWhereNoLineBreakSeparatesItems(t *testing.T) {
	document := mustParse(t, "[1\n2]")
	root := document.Root().(*Array)
	item, err := NewItem(3)

	if err != nil {
		t.Fatal(err)
	}

	root.Items = append(root.Items, item)

	if printed := document.String(); printed != "[1\n2,3]" {
		t.Fatalf("printed %q", printed)
	}
}

func TestTreeNodesHaveSpansAndValues(t *testing.T) {
	text := "name: 'api'\nport: 0x1F90\n'limits.memory': 512\nhosts: [\"a\", '''\n\tb\n\t''']\n"
	document := mustParse(t, text)
	root := document.Root().(*Object)

	if root.IsBraced() || len(root.Members) != 4 {
		t.Fatalf("root: braced %v, %d members", root.IsBraced(), len(root.Members))
	}

	node, _ := document.Get("port")
	port := node.(*Scalar)
	portSpan, _ := port.Span()

	if port.Value() != int64(8080) || port.Raw() != "0x1F90" || port.Radix() != 16 || text[portSpan.Start:portSpan.End] != "0x1F90" {
		t.Errorf("port: %v %q %d %v", port.Value(), port.Raw(), port.Radix(), portSpan)
	}

	if line, column := document.Position(portSpan.Start); line != 2 || column != 7 {
		t.Errorf("position %d:%d, want 2:7", line, column)
	}

	member := root.Members[2]
	memberSpan, _ := member.Span()

	if member.Key.String() != "'limits.memory'" || member.Key.Value() != "limits.memory" || member.Key.Style() != StyleLiteral || text[memberSpan.Start:memberSpan.End] != "'limits.memory': 512" {
		t.Errorf("member: %q %v", member.Key, memberSpan)
	}

	name, _ := document.Get("name")

	if style := name.(*Scalar).Style(); style != StyleLiteral {
		t.Errorf("name style %v", style)
	}

	first, _ := document.Get("hosts", 0)

	if style := first.(*Scalar).Style(); style != StyleEscaped {
		t.Errorf("first host style %v", style)
	}

	second, _ := document.Get("hosts", 1)

	if second.(*Scalar).Value() != "b" || second.(*Scalar).Style() != StyleLiteralBlock {
		t.Errorf("second host: %v %v", second.(*Scalar).Value(), second.(*Scalar).Style())
	}

	memory, ok := document.Get("limits.memory")

	if !ok || memory.(*Scalar).Value() != int64(512) {
		t.Errorf("limits.memory: %v", memory)
	}

	if _, ok := document.Get("limits", "memory"); ok {
		t.Error("a path found a key with a dot as nested keys")
	}

	if root.Members[0].Value.(*Scalar).Radix() != 0 {
		t.Error("a string has no radix")
	}
}

func TestTreeFindsCommentsWithTheirSpans(t *testing.T) {
	text := "# One\na: 1 # Two\n/* Three */ b: [1, /* Four */ 2]\n"
	comments := mustParse(t, text).Comments()
	var texts []string

	for _, comment := range comments {
		texts = append(texts, comment.Text)

		if delimited := text[comment.Span.Start:comment.Span.End]; delimited != delimitedComment(comment) {
			t.Errorf("span %v is %q", comment.Span, delimited)
		}
	}

	if !slices.Equal(texts, []string{" One", " Two", " Three ", " Four "}) {
		t.Errorf("texts %q", texts)
	}

	if comments[0].IsBlock || !comments[2].IsBlock {
		t.Error("kinds")
	}
}

func TestTreeTopLevelObjectWithoutBracesSpansItsMembers(t *testing.T) {
	text := "# Head\na: 1\nb: 2 # Tail\n"
	span, _ := mustParse(t, text).Root().Span()

	if text[span.Start:span.End] != "a: 1\nb: 2" {
		t.Errorf("span %q", text[span.Start:span.End])
	}
}

// checkChange applies a change, and checks the printed text and that it is a valid document.
func checkChange(t *testing.T, before string, change func(*Document) error, after string) {
	t.Helper()
	document := mustParse(t, before)

	if err := change(document); err != nil {
		t.Fatalf("change of %q: %v", before, err)
	}

	if printed := document.String(); printed != after {
		t.Fatalf("after a change of %q:\n got %q\nwant %q", before, printed, after)
	}

	var value any

	if err := Unmarshal([]byte(after), &value); err != nil {
		t.Fatalf("not valid: %q: %v", after, err)
	}
}

func set(value any, path ...any) func(*Document) error {
	return func(document *Document) error {
		return document.Set(value, path...)
	}
}

func remove(path ...any) func(*Document) error {
	return func(document *Document) error {
		isRemoved, err := document.Remove(path...)

		if err == nil && !isRemoved {
			return errors.New("nothing was removed")
		}

		return err
	}
}

func TestTreeSet(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		change        func(*Document) error
	}{
		{"replaces a value and keeps its comments", "# Head\nport: 0x1F90 # The default.\nname: 'x'\n", "# Head\nport: 8080 # The default.\nname: 'x'\n", set(8080, "port")},
		{"replaces a value in a one-line object", "{a: 1, b: 2}", "{a: 1, b: 'two'}", set("two", "b")},
		{"replaces an array item", "a: [1, 2, 3]", "a: [1, 9.5, 3]", set(9.5, "a", 1)},
		{"writes a new object on indented lines", "server: {\n\tport: 1,\n}\n", "server: {\n\tport: {\n\t\ta: 1\n\t\tb: 2\n\t},\n}\n", set(map[string]int{"b": 2, "a": 1}, "server", "port")},
		{"writes a new array on indented lines", "a: 1\n", "a: 1\nlist: [\n\t1\n\t2\n]\n", set([]int{1, 2}, "list")},
		{"writes an empty array on one line", "a: 1\n", "a: 1\nempty: []\n", set([]any{}, "empty")},
		{"adds a top-level member on a new line", "a: 1\n", "a: 1\nb: 2\n", set(2, "b")},
		{"adds to a one-line object that holds a block comment over two lines", "{a: 1, /* x\ny */ b: 2}", "{a: 1, /* x\ny */ b: 2, c: 1}", set(1, "c")},
		{"adds to an empty object after a block comment over two lines", "a: { /* x\ny */ }", "a: { /* x\ny */\n\tb: 1\n}", set(1, "a", "b")},
		{"adds to an empty object that ends in a block comment", "a: {\n\t# c\n\t/* d */ }", "a: {\n\t# c\n\t/* d */\n\tb: 1\n}", set(1, "a", "b")},
		{"adds a top-level member without a final line feed", "a: 1", "a: 1\nb: 2", set(2, "b")},
		{"adds a top-level member before a closing comment", "a: 1 # note\n# end\n", "a: 1 # note\nb: 2\n# end\n", set(2, "b")},
		{"quotes a new key that is not bare", "'the name': 1\n", "'the name': 1\n'a.b': 2\n", set(2, "a.b")},
		{"adds a member like its siblings", "server: {\n\thost: 'a',\n}\n", "server: {\n\thost: 'a',\n\tport: 1\n}\n", set(1, "server", "port")},
		{"adds a member without a comma like its siblings", "server: {\n\thost: 'a'\n}\n", "server: {\n\thost: 'a'\n\tport: 1\n}\n", set(1, "server", "port")},
		{"adds a member to a one-line object", "server: {host: 'a'}\n", "server: {host: 'a', port: 1}\n", set(1, "server", "port")},
		{"adds a member to an empty object", "server: {}\n", "server: {\n\tport: 1\n}\n", set(1, "server", "port")},
		{"adds a member to a nested empty object", "a: {\n\tb: {},\n}\n", "a: {\n\tb: {\n\t\tc: 1\n\t},\n}\n", set(1, "a", "b", "c")},
		{"adds a member to a braced top-level object", "{\n\ta: 1,\n}\n", "{\n\ta: 1,\n\tb: 2\n}\n", set(2, "b")},
		{"appends to a one-line array", "a: [1, 2]", "a: [1, 2, 3]", set(3, "a", 2)},
		{"appends on the line of the closing bracket", "a: [\n\t1]", "a: [\n\t1, 2]", set(2, "a", 1)},
		{"adds a member on the line of the closing brace", "a: {\n\tb: 1}", "a: {\n\tb: 1, c: 2}", set(2, "a", "c")},
		{"appends on a new line after a removal that ended the line", "a: [\n\t1, 2\n]\n", "a: [\n\t1\n\t3\n]\n", func(document *Document) error {
			if _, err := document.Remove("a", 1); err != nil {
				return err
			}

			return document.Set(3, "a", 1)
		}},
		{"puts a value that replaces a block string on the line of its key", "a:\n\t'''\n\tx\n\t'''\nb: 1\n", "a: 2\nb: 1\n", set(2, "a")},
		{"puts a value that replaces a nested block string on the line of its key", "o: {\n\ta:\n\t\t'''\n\t\tx\n\t\t'''\n}\n", "o: {\n\ta: 2\n}\n", set(2, "o", "a")},
		{"keeps a value that replaces a block string after a comment on its own line", "a: # c\n\t'''\n\tx\n\t'''\n", "a: # c\n\t2\n", set(2, "a")},
		{"indents a new object by the line of the value it replaces", "a: # c\n\t'''\n\tx\n\t'''\n", "a: # c\n\t{\n\t\tx: 1\n\t}\n", set(map[string]any{"x": 1}, "a")},
		{"indents a new nested array by the line of the value it replaces", "o: {\n\ta: # c\n\t\t'''\n\t\tx\n\t\t'''\n}\n", "o: {\n\ta: # c\n\t\t[\n\t\t\t1\n\t\t]\n}\n", set([]any{1}, "o", "a")},
		{"indents a new member by the line of its object", "a: # c\n\t{}\n", "a: # c\n\t{\n\t\tn: 1\n\t}\n", set(1, "a", "n")},
		{"indents a new item by the line of its array", "a: # c\n\t[]\n", "a: # c\n\t[\n\t\t1\n\t]\n", set(1, "a", 0)},
		{"appends to a multi-line array", "a: [\n\t1,\n]\n", "a: [\n\t1,\n\t2\n]\n", set(2, "a", 1)},
		{"appends to an empty array", "a: []\n", "a: [\n\t1\n]\n", set(1, "a", 0)},
		{"appends to a multi-line array without commas", "a: [\n\t1\n\t2\n]\n", "a: [\n\t1\n\t2\n\t3\n]\n", set(3, "a", 2)},
		{"appends to a one-line array with a trailing comma", "a: [1, 2,]", "a: [1, 2, 3,]", set(3, "a", 2)},
		{"appends to a top-level array", "[1]", "[1, 2]", set(2, 1)},
		{"adds a member on the line where the value of the member before it ends", "{ b: {\n\t\tx: 1\n\t}, c: 1\n}", "{ b: {\n\t\tx: 1\n\t}, c: 1\n\td: {\n\t\te: 2\n\t}\n}", set(2, "d", "e")},
		{"replaces an item on the line where the item before it ends", "a: [{\n\t\tx: 1\n\t}, [1]]\n", "a: [{\n\t\tx: 1\n\t}, [\n\t\t1\n\t\t2\n\t]]\n", set([]int{1, 2}, "a", 1)},
		{"appends an item after a comma on the closing line of the item before it", "a: [{\n\t\tx: 1\n\t}]\n", "a: [{\n\t\tx: 1\n\t}, [\n\t\t1\n\t\t2\n\t]]\n", set([]int{1, 2}, "a", 1)},
		{"appends an item on the line after a block string", "a: [\n\t'''\n\t\tx\n\t\t''', 2\n]\n", "a: [\n\t'''\n\t\tx\n\t\t''', 2\n\t\t3\n]\n", set(3, "a", 2)},
		{"adds a member to an indented empty top-level object", "\t{}\n", "\t{\n\t\ta: 1\n\t}\n", set(1, "a")},
		{"adds an item to an indented empty top-level array", "\t[]\n", "\t[\n\t\t[\n\t\t\t1\n\t\t]\n\t]\n", set([]int{1}, 0)},
		{"adds a member on the line of an indented top-level object", "\t{a: [\n\t\t1\n\t]}\n", "\t{a: [\n\t\t1\n\t], b: {\n\t\tk: 1\n\t}}\n", set(map[string]int{"k": 1}, "b")},
		{"replaces a value in an indented top-level object below a comment", "  # c\n  {a: 1\n  }\n", "  # c\n  {a: {\n  \tk: 1\n  }\n  }\n", set(map[string]int{"k": 1}, "a")},
		{"adds a member on one line to an indented top-level object on one line", "\t{a: 1}\n", "\t{a: 1, b: {k: 1}}\n", set(map[string]int{"k": 1}, "b")},
		{"replaces a value on one line in a top-level object on one line", "  # c\n  {a: 1}\n", "  # c\n  {a: {k: 1}}\n", set(map[string]int{"k": 1}, "a")},
		{"writes a new item on one line in an array on one line", "a: [1, 2]\n", "a: [1, 2, {a: 3}]\n", set(map[string]int{"a": 3}, "a", 2)},
		{"writes a value that replaces another on one line in an object on one line", "x: {x: 1}\n", "x: {x: [1, {y: 2}]}\n", set([]any{1, map[string]int{"y": 2}}, "x", "x")},
		{"writes a value on one line in a container inside a container on one line", "[[1, 2], 3]\n", "[[1, {k: 1}], 3]\n", set(map[string]int{"k": 1}, 0, 1)},
		{"writes a value on one line in a container on one line inside one that spans lines", "[[1, 2],\n3]\n", "[[1, 2, {k: 1}],\n3]\n", set(map[string]int{"k": 1}, 0, 2)},
		{"creates a missing parent on one line in an object on one line", "x: {x: 1}\n", "x: {x: 1, b: {c: 2}}\n", set(2, "x", "b", "c")},
		{"adds the first item of an array with only a comment on one line before the closing bracket", "a: [/* note */]\n", "a: [/* note */ 1]\n", set(1, "a", 0)},
		{"adds the first item of an empty array inside an array on one line on that line", "a: [1, []]\n", "a: [1, [2]]\n", set(2, "a", 1, 0)},
		{"adds the first member of an empty object with spaces inside an array on one line on that line", "a: [1, { }]\n", "a: [1, {b: {c: [1, 2]}}]\n", set(map[string]any{"c": []int{1, 2}}, "a", 1, "b")},
		{"adds the first item of an array with only spaces on its own line", "a: [ ]\n", "a: [\n\t1\n]\n", set(1, "a", 0)},
		{"writes a new value in an object on one line that holds a block comment over two lines on several lines", "{a: 1, /* x\ny */ b: 2}", "{a: 1, /* x\ny */ b: 2, c: {\n\tk: 1\n}}", set(map[string]int{"k": 1}, "c")},
		{"adds a member to an empty object whose closing line is indented more", "{\n\t}", "{\n\tn: {\n\t\tm: 1\n\t}\n}", set(1, "n", "m")},
		{"adds an item to an empty array whose closing line is indented more", "a: [\n\t]\n", "a: [\n\t[\n\t\t1\n\t\t2\n\t]\n]\n", set([]int{1, 2}, "a", 0)},
		{"adds an item below a comment in an empty array whose closing line is indented less", "a: {\n\tb: [\n\t\t# c\n]\n}\n", "a: {\n\tb: [\n\t\t# c\n\t\t1\n\t]\n}\n", set(1, "a", "b", 0)},
		{"creates the item at the index equal to the length of an array for a path below it", "b: []\n", "b: [\n\t{\n\t\tc: 1\n\t}\n]\n", set(1, "b", 0, "c")},
		{"creates a missing parent with braces", "a: 1\n", "a: 1\nserver: {\n\ttls: {\n\t\tenabled: true\n\t}\n}\n", set(true, "server", "tls", "enabled")},
		{"keeps the indentation of an indented first line", "\ta: 1\n", "\ta: 1\n\tb: 2\n", set(2, "b")},
		{"adds a key with a dot as one quoted key", "a: {b: 1}\n", "a: {b: 1}\n'a.b': 2\n", set(2, "a.b")},
		{"replaces the value of a quoted key with a dot", "\"a.b\": 1\na: {b: 2}\n", "\"a.b\": 3\na: {b: 2}\n", set(3, "a.b")},
		{"indents a new member in an indented empty object", "  a: {\n  }", "  a: {\n  \tb: 2\n  }", set(2, "a", "b")},
		{"keeps a comment at the end of a file without a line feed on its line", "a: 1 # c", "a: 1 # c\nb: 2", set(2, "b")},
		{"keeps the comment of an empty object", "a: { /* c */\n}", "a: { /* c */\n\tb: 1\n}", set(1, "a", "b")},
		{"keeps the comment of an empty array", "a: [ /* c */\n]", "a: [ /* c */\n\t1\n]", set(1, "a", 0)},
		{"keeps the comment of an empty object on one line, and the new member on that line", "a: { /* c */ }", "a: { /* c */ b: 1}", set(1, "a", "b")},
		{"keeps the comment of an empty array on one line, and the new item on that line", "a: [ /* c */ ]", "a: [ /* c */ 1]", set(1, "a", 0)},
		{"adds to an empty braced top-level object", "{ }", "{\n\tb: 1\n}", set(1, "b")},
		{"writes a struct with its fields in declared order", "a: 1\n", "a: 1\nserver: {\n\tport: 80\n\thost: 'x'\n}\n", set(struct {
			Port int    `soml:"port"`
			Host string `soml:"host"`
		}{80, "x"}, "server")},
		{"writes a struct with its fields in declared order under a missing parent", "a: 1\n", "a: 1\nb: {\n\tc: {\n\t\tport: 80\n\t\thost: 'x'\n\t}\n}\n", set(struct {
			Port int    `soml:"port"`
			Host string `soml:"host"`
		}{80, "x"}, "b", "c")},
		{"appends a struct with its fields in declared order", "a: []\n", "a: [\n\t{\n\t\tport: 80\n\t\thost: 'x'\n\t}\n]\n", set(struct {
			Port int    `soml:"port"`
			Host string `soml:"host"`
		}{80, "x"}, "a", 0)},
		{"writes a nested struct with its fields in declared order and a map sorted", "a: 1\n", "a: 1\nb: {\n\tz: {\n\t\ty: 1\n\t\tx: 2\n\t}\n\tm: {\n\t\tc: 3\n\t\td: 4\n\t}\n}\n", set(struct {
			Z struct {
				Y int `soml:"y"`
				X int `soml:"x"`
			} `soml:"z"`
			M map[string]int `soml:"m"`
		}{struct {
			Y int `soml:"y"`
			X int `soml:"x"`
		}{1, 2}, map[string]int{"d": 4, "c": 3}}, "b")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkChange(t, test.before, test.change, test.after)
		})
	}
}

func TestTreeSetRejectsWhatDoesNotFit(t *testing.T) {
	text := "a: 1\nb: [1]\nserver: {port: 1}\n"
	document := mustParse(t, text)

	tests := []struct {
		name  string
		value any
		path  []any
	}{
		{"through a scalar", 1, []any{"a", "b"}},
		{"a key on an array", 1, []any{"b", "x"}},
		{"past the end of an array", 1, []any{"b", 5}},
		{"an index on a scalar", 1, []any{"a", 0}},
		{"an index on an object", 1, []any{0}},
		{"an empty path", 1, nil},
		{"a path element of another type", 1, []any{true}},
		{"a negative position", 1, []any{"b", -1}},
		{"NaN", math.NaN(), []any{"c"}},
		{"a carriage return", "a\rb", []any{"c"}},
		{"a key with a carriage return", 1, []any{"x\ry"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := document.Set(test.value, test.path...)

			if _, ok := errors.AsType[*Error](err); !ok {
				t.Fatalf("Set gave %v, want an *Error", err)
			}

			if printed := document.String(); printed != text {
				t.Fatalf("the document changed to %q", printed)
			}
		})
	}
}

// The messages are those of `edit()` in the JS reference, for the same text and path, with a lowercase first letter, as a Go error has.
func TestTreeEditErrorsMatchTheJSReference(t *testing.T) {
	longPath := []any{"a"}

	for index := range 10 {
		longPath = append(longPath, strings.Repeat(strconv.Itoa(index), 40))
	}

	tests := []struct {
		text    string
		path    []any
		remove  bool
		message string
	}{
		{"a: 1\n", []any{"a", "b"}, false, "cannot edit a.b, because a is not an object or an array"},
		{"a: [1]\n", []any{"a", "b"}, false, "cannot edit a.b, because a is an array, so it needs an index, not a key"},
		{"a: {b: 1}\n", []any{"a", "b", "c", "d"}, false, "cannot edit a.b.c.d, because a.b is not an object or an array"},
		{"[1]\n", []any{"a"}, false, "cannot edit a, because the document is an array, so it needs an index, not a key"},
		{"a: 1\n", []any{"a", 0}, false, "cannot edit a[0], because a is not an object or an array"},
		{"a: {}\n", []any{"a", 0}, false, "cannot edit a[0], because a is an object, so it needs a key, not an index"},
		{"a: 1\n", []any{0}, false, "cannot edit [0], because the document is an object, so it needs a key, not an index"},
		{"a: {b: {c: 1}}\n", []any{"a", "b", 0, "x"}, false, "cannot edit a.b[0].x, because a.b is an object, so it needs a key, not an index"},
		{"a: [1]\n", []any{"a", 3}, false, "cannot edit a[3], because the array at a has 1 item. Add an item at index 1"},
		{"a: [1, 2]\n", []any{"a", 3}, false, "cannot edit a[3], because the array at a has 2 items. Add an item at index 2"},
		{"[1, 2]\n", []any{3}, false, "cannot edit [3], because the document has 2 items. Add an item at index 2"},
		{"a: 1\n", []any{"b", 0}, false, "cannot edit b[0], because b does not exist, so it has no index 0"},
		{"a: [1]\n", []any{"a", 1, 0}, false, "cannot edit a[1][0], because a[1] does not exist, so it has no index 0"},
		{"a: 1\n", []any{"b", 0, 1}, false, "cannot edit b[0][1], because b[0] does not exist, so it has no index 1"},
		{"a: 1\n", []any{"b", "c", 2, "d"}, false, "cannot edit b.c[2].d, because b.c does not exist, so it has no index 2"},
		{"a: 1\n", []any{"the key", "b c", 0}, false, "cannot edit \"the key\".\"b c\"[0], because \"the key\".\"b c\" does not exist, so it has no index 0"},
		{"'a.b': 1\n", []any{"a.b", 0}, false, "cannot edit \"a.b\"[0], because \"a.b\" is not an object or an array"},
		{"a: [1]\n", []any{"a", "é"}, false, "cannot edit a.\"é\", because a is an array, so it needs an index, not a key"},
		{"\"a\\u{1b}\\u{202e}\\u{7f}\\u{85}\\u{2028}\\u{ad}\": 1\n", []any{"a\u001b\u202e\u007f\u0085\u2028\u00ad", "b"}, false, "cannot edit \"a\\u001b\\u{202e}\\u{7f}\\u{85}\\u{2028}\\u{ad}\".b, because \"a\\u001b\\u{202e}\\u{7f}\\u{85}\\u{2028}\\u{ad}\" is not an object or an array"},
		{"a: 1\n", longPath, false, "cannot edit a.0000000000000000000000000000000000000000.1111111111111111111111111111111111111111.2222222222222222222222222222222222222222.3333333333333333333333333333333333333333.4444444444444444444444444444444444…, because a is not an object or an array"},
		{"a: 1\n", []any{}, false, "the path must be a non-empty array of keys and array indexes"},
		{"a: 1\n", []any{true}, false, "the path must be a non-empty array of keys and array indexes"},
		{"a: 1\n", []any{"b", -1}, false, "the path must be a non-empty array of keys and array indexes"},
		{"a: 1\n", []any{"a", "b"}, true, "cannot edit a.b, because a is not an object or an array"},
		{"[1]\n", []any{"a"}, true, "cannot edit a, because the document is an array, so it needs an index, not a key"},
		{"a: {}\n", []any{"a", 0}, true, "cannot edit a[0], because a is an object, so it needs a key, not an index"},
		{"a: [1]\n", []any{"a", 0, "b"}, true, "cannot edit a[0].b, because a[0] is not an object or an array"},
		{"a: [1]\n", []any{"a", "x"}, true, "cannot edit a.x, because a is an array, so it needs an index, not a key"},
		{"a: [[1]]\n", []any{"a", 0, "x"}, true, "cannot edit a[0].x, because a[0] is an array, so it needs an index, not a key"},
		{"a: {b: [1]}\n", []any{"a", "b", "x", 0}, true, "cannot edit a.b.x[0], because a.b is an array, so it needs an index, not a key"},
		{"a: 1\n", []any{}, true, "the path must be a non-empty array of keys and array indexes"},
	}

	for _, test := range tests {
		document := mustParse(t, test.text)
		var err error

		if test.remove {
			_, err = document.Remove(test.path...)
		} else {
			err = document.Set(1, test.path...)
		}

		if somlError, ok := errors.AsType[*Error](err); !ok || somlError.Message != test.message {
			t.Errorf("%q %v: got %v, want %q", test.text, test.path, err, test.message)
		}
	}
}

func TestTreeSetRefusesAChangeThatMakesTheDocumentTooDeep(t *testing.T) {
	// The top-level object and 98 more are 99 levels, so the leaf can become one more array but not two.
	text := strings.Repeat("a: {", 98) + "a: 1" + strings.Repeat("}", 98)
	document := mustParse(t, text)
	path := make([]any, 99)

	for index := range path {
		path[index] = "a"
	}

	if err := document.Set([]any{[]any{}}, path...); err == nil || err.Error() != "soml: cannot edit "+strings.Repeat("a.", 98)+"a, because the change would make a document that is not valid: the document is nested more than 100 levels deep" {
		t.Fatalf("a change past the nesting limit gave %v", err)
	}

	if document.String() != text {
		t.Fatal("a failed change changed the document")
	}

	if err := document.Set([]any{}, path...); err != nil {
		t.Fatal(err)
	}
}

func TestTreeSetKeepsTheOtherNodes(t *testing.T) {
	document := mustParse(t, "a: 1\nb: {c: 2}\n")
	root := document.Root().(*Object)
	member := root.Members[1]

	if err := document.Set(3, "b", "d"); err != nil {
		t.Fatal(err)
	}

	if document.Root() != root || root.Members[1] != member || len(member.Value.(*Object).Members) != 2 {
		t.Fatal("Set replaced nodes outside the change")
	}
}

func TestTreeAppendingToAContainerKeepsTheOthers(t *testing.T) {
	document := mustParse(t, "a: [[1], [2]]\nb: [3]\nc: {d: 4}\ne: {f: 5}\ng: []\nh: {}\n")
	root := document.Root().(*Object)
	item, err := NewItem(6)

	if err != nil {
		t.Fatal(err)
	}

	member, err := NewMember(NewKey("x"), 7)

	if err != nil {
		t.Fatal(err)
	}

	// Each one is appended to a container that has another one of its kind after it, at its own depth and in the one around it.
	item.Decor.Leading = " "
	member.Decor.Leading = " "
	first := root.Members[0].Value.(*Array).Items[0].Value.(*Array)
	first.Items = append(first.Items, item)
	object := root.Members[2].Value.(*Object)
	object.Members = append(object.Members, member)

	if got, want := document.String(), "a: [[1, 6], [2]]\nb: [3]\nc: {d: 4, x: 7}\ne: {f: 5}\ng: []\nh: {}\n"; got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}

	if root.Members[4].Value.(*Array).Items != nil || root.Members[5].Value.(*Object).Members != nil {
		t.Fatal("an empty container has entries that are not nil")
	}
}

func TestTreeRemove(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		change        func(*Document) error
	}{
		{"takes the comment at the end of its line", "a: 1\nb: 2 # About b.\nc: 3\n", "a: 1\nc: 3\n", remove("b")},
		{"removes the first member of a one-line object", "{a: 1, b: 2}", "{b: 2}", remove("a")},
		{"removes the last member of a one-line object", "{a: 1, b: 2}", "{a: 1}", remove("b")},
		{"removes the last member of a multi-line object", "{\n\ta: 1,\n\tb: 2,\n}\n", "{\n\ta: 1,\n}\n", remove("b")},
		{"removes a member of a multi-line object without commas", "{\n\ta: 1\n\tb: 2\n\tc: 3\n}\n", "{\n\ta: 1\n\tc: 3\n}\n", remove("b")},
		{"removes the last item of a multi-line array without commas", "a: [\n\t1\n\t2\n]\n", "a: [\n\t1\n]\n", remove("a", 1)},
		{"removes the first member of a multi-line object", "{\n\ta: 1,\n\tb: 2,\n}\n", "{\n\tb: 2,\n}\n", remove("a")},
		{"leaves an empty object", "x: {\n\ta: 1,\n}\n", "x: {}\n", remove("x", "a")},
		{"removes an array item", "a: [1, 2, 3]", "a: [1, 3]", remove("a", 1)},
		{"removes the only item of an array on one line with the comment after it, which it owns", "a: [1 /* c */]\n", "a: []\n", remove("a", 0)},
		{"removes the only member of an object on one line with the comment after it, which it owns", "a: {x: 1 /* c */}\n", "a: {}\n", remove("a", "x")},
		{"takes the comma before a removed last item with the block comment before it, which it owns", "a: [1, /* c */ 2]\n", "a: [1]\n", remove("a", 1)},
		{"keeps a comment after the comma of a removed item that is not at the end of its line", "a: [1, /* c */]\n", "a: [/* c */]\n", remove("a", 0)},
		{"keeps the comment lines above", "# Head\na: 1\nb: 2\n", "# Head\nb: 2\n", remove("a")},
		{"keeps a section comment", "a: 1\n\n# Section\nb: 2\nc: 3\n", "a: 1\n\n# Section\nc: 3\n", remove("b")},
		{"keeps the comment above the last member", "{\n\ta: 1,\n\t# Last\n\tb: 2,\n}\n", "{\n\ta: 1,\n\t# Last\n}\n", remove("b")},
		{"leaves an empty top-level object", "a: 1\n", "{}\n", remove("a")},
		{"removes a quoted key with a dot and not the nested key", "'a.b': 1\na: {b: 2}\n", "a: {b: 2}\n", remove("a.b")},
		{"keeps the comment lines and the comment of the only member that {} replaces", "# h\na: 1 # c\n", "# h\n{} # c\n", remove("a")},
		{"keeps a block comment that spans lines whole", "{\n\ta: 1, # x\n\t/* t\nwo */ b: 2,\n}\n", "{\n\ta: 1, # x\n}\n", remove("b")},
		{"keeps a block comment on its own lines", "{\n\ta: 1,\n\t/* t\nwo */\n\tb: 2,\n\tc: 3,\n}\n", "{\n\ta: 1,\n\t/* t\nwo */\n\tc: 3,\n}\n", remove("b")},
		{"keeps a comment before the comma it drops", "{a: 1 /* c */, b: 2}", "{a: 1 /* c */}", remove("b")},
		{"leaves no space where a comma went away", "{\n\ta: 1 , b: 2\n}\n", "{\n\ta: 1\n}\n", remove("b")},
		{"keeps a comment before the comma of an array item", "[1 /* x */, 2]", "[1 /* x */]", remove(1)},
		{"keeps a comment in front of the next top-level member", "a: 1\n/* c */ b: 2", "/* c */ b: 2", remove("a")},
		{"keeps a comment in front of the next member", "{a: 1,\n\t/* c */ b: 2}", "{\n\t/* c */ b: 2}", remove("a")},
		{"keeps the comment line above the next member on its own line", "a: {b: 1,\n\t# about c\n\tc: 2,\n}", "a: {\n\t# about c\n\tc: 2,\n}", remove("a", "b")},
		{"keeps the comment line above the next item on its own line", "a: [\n\t1, 2,\n\t# c\n\t3,\n]", "a: [\n\t1,\n\t# c\n\t3,\n]", remove("a", 1)},
		{"keeps the closing comment line on its own line", "{\n\ta: 1, b: 2\n\t# c\n}", "{\n\ta: 1\n\t# c\n}", remove("b")},
		{"keeps the closing bracket on its own line", "a: [\n\t1, 2,\n]", "a: [\n\t1,\n]", remove("a", 1)},
		{"gives the next item the place of the removed one on its line", "a: [\n\t1,\n\t2, 3,\n]", "a: [\n\t1,\n\t3,\n]", remove("a", 1)},
		{"leaves the indentation of a member on its own line", "{\n  a: 1,\n\tb: 2,\n}", "{\n\tb: 2,\n}", remove("a")},
		{"keeps a comment in front of the next item", "[1, /* c */ 2]", "[/* c */ 2]", remove(0)},
		{"removes the last member of a file that ends in a comment", "a: 1\nb: 2 # t", "a: 1\n", remove("b")},
		{"keeps the comma before a removed last item on another line", "a: [\n\t1,\n\t2\n]", "a: [\n\t1,\n]", remove("a", 1)},
		{"keeps the comma before a removed last member on another line", "a: {\n\tb: 1,\n\tc: 2\n}", "a: {\n\tb: 1,\n}", remove("a", "c")},
		{"adds no comma when the removed last item has one", "a: [\n\t1\n\t2,\n]", "a: [\n\t1\n]", remove("a", 1)},
		{"keeps the comma before a comment at the end of the line", "a: [1, # x\n2]", "a: [1, # x\n]", remove("a", 1)},
		{"keeps one run of blank lines where a member was", "a: 1\n\nb: 2\n\nc: 3\n", "a: 1\n\nc: 3\n", remove("b")},
		{"keeps the blank lines below a removed member", "a: 1\n\nb: 2\n\n\nc: 3\n", "a: 1\n\n\nc: 3\n", remove("b")},
		{"keeps the blank line below a comment above a removed member", "a: 1\n\n# x\n\nb: 2\nc: 3\n", "a: 1\n\n# x\n\nc: 3\n", remove("b")},
		{"keeps one run of blank lines where an item was", "a: [\n\t1,\n\n\t2,\n\n\t3\n]", "a: [\n\t1,\n\n\t3\n]", remove("a", 1)},
		{"leaves no blank line at the start of a container", "{\n\ta: 1\n\n\tb: 2\n}\n", "{\n\tb: 2\n}\n", remove("a")},
		{"leaves no blank line at the start of a document", "a: 1\n\nb: 2\n\nc: 3\n", "b: 2\n\nc: 3\n", remove("a")},
		{"keeps the blank line below a heading comment", "# head\n\na: 1\nb: 2\n", "# head\n\nb: 2\n", remove("a")},
		{"leaves no blank line at the end of a container", "a: [\n\t1\n\n\t2\n]", "a: [\n\t1\n]", remove("a", 1)},
		{"leaves no blank line at the end of a document", "a: 1\n\nb: 2\n", "a: 1\n", remove("b")},
		{"keeps the blank line below a heading comment of an emptied top-level object", "# h\n\na: 1\n", "# h\n\n{}\n", remove("a")},
		{"keeps the comment of the member that {} replaces", "a: 1 # c\n", "{} # c\n", remove("a")},
		{"keeps one run of blank lines below a heading comment", "# Lead\n\na: 1\n\nb: 2\n", "# Lead\n\nb: 2\n", remove("a")},
		{"keeps a block comment on its own line below an emptied top-level object", "a: 1\n/* c */", "{}\n/* c */", remove("a")},
		{"keeps the blank lines at the start of a container", "[\n\n\t1\n\n\t2\n\n]", "[\n\n\t2\n\n]", remove(0)},
		{"leaves no blank line at the start of an emptied array with a closing comment", "[\n\t1\n\n\t# c\n]\n", "[\n\t# c\n]\n", remove(0)},
		{"leaves no blank line at the start of an emptied object with a closing comment", "x: {\n\ta: 1\n\n\t# c\n}\n", "x: {\n\t# c\n}\n", remove("x", "a")},
		{"leaves no blank line below a comment on the opening line of an emptied object", "x: { # o\n\ta: 1\n\n\t# c\n}", "x: { # o\n\t# c\n}", remove("x", "a")},
		{"leaves no blank line below a comment on the opening line of an array", "x: [ # o\n\t1\n\n\t2\n]", "x: [ # o\n\t2\n]", remove("x", 0)},
		{"keeps the blank line above a removed last member when a comment follows", "q: 1\n\nb: 1\n# c\n", "q: 1\n\n# c\n", remove("b")},
		{"keeps the blank line above a removed last member of an object when a comment follows", "x: {\n\tq: 1\n\n\tb: 1\n\t# c\n}\n", "x: {\n\tq: 1\n\n\t# c\n}\n", remove("x", "b")},
		{"keeps the blank line above the removed only item between two comments", "x: [\n\t# c\n\n\t0\n\t# e\n]\n", "x: [\n\t# c\n\n\t# e\n]\n", remove("x", 0)},
		{"leaves no blank line at the end of an emptied array with a comment on its opening line", "[ # c\n\n\t1\n]\n", "[ # c\n]\n", remove(0)},
		{"writes {} where the only member of a top-level object was", "# Head\n\na: 1\n\n# End\n", "# Head\n\n{}\n\n# End\n", remove("a")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkChange(t, test.before, test.change, test.after)
		})
	}
}

func TestTreeChainedChangesGiveWhatAReparseGives(t *testing.T) {
	tests := []struct {
		before, after string
		changes       []func(*Document) error
	}{
		{"[\n\t1, 2\n\t3\n]\n", "[\n\t1,\n]\n", []func(*Document) error{remove(1), remove(1)}},
		{"{\n\ta: 1, b: 2\n\tc: 3\n}\n", "{\n\ta: 1,\n}\n", []func(*Document) error{remove("b"), remove("c")}},
		{"{\n\ta: 1, b: 2,\n\t# c\n}\n", "{\n\t# c\n}\n", []func(*Document) error{remove("b"), remove("a")}},
		{"[\n\t# c\n\t1, 2\n]\n", "[\n\t# c\n]\n", []func(*Document) error{remove(1), remove(0)}},
		{"[\n\t1, 2,\n\t3,\n\n\t# c\n]\n", "[\n\t# c\n]\n", []func(*Document) error{remove(2), remove(1), remove(0)}},
		{"# c\na: 1", "# c\nb: 2", []func(*Document) error{set(2, "b"), remove("a")}},
	}

	for _, test := range tests {
		chained := mustParse(t, test.before)
		reparsed := mustParse(t, test.before)

		for _, change := range test.changes {
			if err := change(chained); err != nil {
				t.Fatal(err)
			}

			if err := change(reparsed); err != nil {
				t.Fatal(err)
			}

			reparsed = mustParse(t, reparsed.String())
		}

		if chained.String() != test.after || reparsed.String() != test.after {
			t.Errorf("%q:\n chained  %q\n reparsed %q\n     want %q", test.before, chained.String(), reparsed.String(), test.after)
		}
	}
}

func TestTreeRemoveReportsWhatItDid(t *testing.T) {
	document := mustParse(t, "a: 1\nb: [1]\n")

	// Removing what is not there changes nothing and is not an error: a missing member, a missing object or array on the way, and an index at or past the end of its array, also with more of the path below it.
	for _, path := range [][]any{{"missing"}, {"missing", "c"}, {"missing", 0}, {"missing", 0, "c"}, {"b", 1}, {"b", 3}, {"b", 3, "c"}, {"b", 3, 0}} {
		if isRemoved, err := document.Remove(path...); isRemoved || err != nil {
			t.Errorf("%v: %v %v", path, isRemoved, err)
		}
	}

	for _, test := range []struct {
		text string
		path []any
	}{
		{"[1, 2]\n", []any{2}},
		{"[1, 2]\n", []any{5, "x"}},
		{"a: {b: 1}\n", []any{"a", "c"}},
		{"a: {b: 1}\n", []any{"a", "c", 0}},
		{"a: {b: [1]}\n", []any{"a", "b", 1, 0}},
	} {
		other := mustParse(t, test.text)

		if isRemoved, err := other.Remove(test.path...); isRemoved || err != nil || other.String() != test.text {
			t.Errorf("%q %v: %v %v %q", test.text, test.path, isRemoved, err, other.String())
		}
	}

	// So removing the same path twice is safe.
	if isRemoved, err := document.Remove("b", 0); !isRemoved || err != nil {
		t.Errorf("the first removal: %v %v", isRemoved, err)
	}

	if isRemoved, err := document.Remove("b", 0); isRemoved || err != nil {
		t.Errorf("the second removal: %v %v", isRemoved, err)
	}

	if document.String() != "a: 1\nb: []\n" {
		t.Errorf("got %q", document.String())
	}

	if _, err := document.Remove("a", "b"); err == nil {
		t.Error("through a scalar")
	}

	if _, err := document.Remove("b", "x"); err == nil {
		t.Error("a key on an array")
	}

	if _, err := document.Remove(); err == nil {
		t.Error("an empty path")
	}
}

func TestTreeNodeLevelChangesPrintAndAreCheckedByDecode(t *testing.T) {
	document := mustParse(t, "a: 1\n")
	root := document.Root().(*Object)
	member, err := NewMember(NewKey("b"), 2)

	if err != nil {
		t.Fatal(err)
	}

	root.Members = append(root.Members, member)

	if printed := document.String(); printed != "a: 1\nb: 2" {
		t.Fatalf("printed %q", printed)
	}

	var value map[string]int

	if err := document.Decode(&value); err != nil || value["b"] != 2 {
		t.Fatalf("Decode: %v %v", value, err)
	}

	duplicate, _ := NewMember(NewKey("a"), 3)
	root.Members = append(root.Members, duplicate)
	err = document.Decode(&value)

	if somlError, ok := errors.AsType[*Error](err); !ok || somlError.Message != "duplicate key a" {
		t.Fatalf("Decode gave %v", err)
	}

	if _, err := NewMember(NewKey("a\rb"), 1); err == nil {
		t.Error("a key with a carriage return")
	}

	if _, err := NewNode(math.NaN()); err == nil {
		t.Error("NaN")
	}
}

// A member keeps the block comment before its comma when it moves into a top-level object without braces, which prints no commas.
func TestTreeAMemberMovedToTheTopLevelKeepsTheCommentBeforeItsComma(t *testing.T) {
	document := mustParse(t, "a: {x: 1 /* keep */, y: 2}\n")
	root := document.Root().(*Object)
	inner := root.Members[0].Value.(*Object)
	moved := inner.Members[0]
	inner.Members = inner.Members[1:]
	root.Members = append(root.Members, moved)

	if printed := document.String(); printed != "a: { y: 2}\nx: 1 /* keep */" {
		t.Fatalf("printed %q", printed)
	}

	if comments := document.Comments(); len(comments) != 1 || comments[0].Text != " keep " {
		t.Fatalf("comments %v", comments)
	}

	// Back in a braced object, it prints its comma after the comment again.
	root.Members = root.Members[:1]
	inner.Members = append([]*Member{moved}, inner.Members...)

	if printed := document.String(); printed != "a: {x: 1 /* keep */, y: 2}\n" {
		t.Fatalf("printed %q", printed)
	}
}

func TestTreeSetAfterRemovingEveryTopLevelMemberAddsInsideTheBraces(t *testing.T) {
	document := mustParse(t, "a: 1\n/* c */")

	if _, err := document.Remove("a"); err != nil {
		t.Fatal(err)
	}

	if err := document.Set(2, "b"); err != nil {
		t.Fatal(err)
	}

	if printed := document.String(); printed != "{\n\tb: 2\n}\n/* c */" {
		t.Fatalf("printed %q", printed)
	}
}

func TestTreeEndsChangedTopLevelDecorThatEndsInALineCommentOnce(t *testing.T) {
	document := mustParse(t, "a: 1\nb: 2\n")
	document.Root().(*Object).Members[0].Decor.Trailing = " # note"

	if printed := document.String(); printed != "a: 1 # note\nb: 2\n" {
		t.Fatalf("printed %q", printed)
	}
}

func TestTreeEndsChangedDecorThatEndsInALineComment(t *testing.T) {
	document := mustParse(t, "{a: 1, b: 2}")
	document.Root().(*Object).Members[0].Decor.Trailing = " # note"

	if printed := document.String(); printed != "{a: 1, # note\n b: 2}" {
		t.Fatalf("printed %q", printed)
	}

	var value any

	if err := document.Decode(&value); err != nil {
		t.Fatal(err)
	}
}

func TestTreeCommentsOfAnUnterminatedBlockCommentInChangedDecor(t *testing.T) {
	document := mustParse(t, "a: 1")
	document.Root().(*Object).Members[0].Decor.Leading = "/* éa"

	if comments := document.Comments(); comments[0].Text != " éa" {
		t.Fatalf("comments %v", comments)
	}
}

func TestTreeReplacesAValueThroughGetAndNewNode(t *testing.T) {
	document := mustParse(t, "port: 1 # The default.\n")
	node, err := NewNode(8080)

	if err != nil {
		t.Fatal(err)
	}

	document.Root().(*Object).Members[0].Value = node

	if printed := document.String(); printed != "port: 8080 # The default.\n" {
		t.Fatalf("printed %q", printed)
	}

	if _, ok := node.Span(); ok {
		t.Error("a new node has a span")
	}
}

func TestTreeKeys(t *testing.T) {
	if key := NewKey("the name"); key.String() != "'the name'" {
		t.Errorf("key %q", key)
	}

	if key := NewKey("a.b"); key.String() != "'a.b'" {
		t.Errorf("key %q", key)
	}

	if style := NewKey("it's").Style(); style != StyleEscaped {
		t.Errorf("style %v", style)
	}
}

// modelDocuments have comments in odd places, quoted keys, one-line and multi-line containers, block strings, and indentation, for the model test.
var modelDocuments = []string{
	"# Head\nname: 'api' # Name.\nserver: {host: 'a', 'the port': 0x1F90}\n\n# Lists\nhosts: ['a', /* b */ 'b']\nlimits: {cpu: 1, memory: 2}\n",
	"{\n\ta: {\n\t\tb: 1, # one\n\t\tc: [1, 2],\n\t},\n\t/* d */ d: '''\n\t\ttext\n\t\t''',\n}\n",
	"[\n\t{a: 1},\n\t[1, 2, 3],\n\t# c\n\t'x',\n]",
	"  a: 1\n  b: {'c.d': {d: []}}\n  e: { /* empty */ }",
	"x: {y: {z: 1}} # end",
	"list: [\n    1,\n    2\n]\nobject: {\n    k: 'v'\n}\n",
}

// modelPaths returns the paths to every value in a plain value.
func modelPaths(value any, prefix []any) [][]any {
	var paths [][]any

	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))

		for key := range value {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		for _, key := range keys {
			path := append(slices.Clone(prefix), key)
			paths = append(paths, path)
			paths = append(paths, modelPaths(value[key], path)...)
		}
	case []any:
		for index, item := range value {
			path := append(slices.Clone(prefix), index)
			paths = append(paths, path)
			paths = append(paths, modelPaths(item, path)...)
		}
	}

	return paths
}

// modelAt returns the value at a path in a plain value.
func modelAt(value any, path []any) any {
	for _, step := range path {
		switch container := value.(type) {
		case map[string]any:
			value = container[step.(string)]
		case []any:
			value = container[step.(int)]
		}
	}

	return value
}

// modelSet sets a value at a path in a plain value, and returns the changed root, because a slice can grow.
func modelSet(root any, path []any, value any) any {
	if len(path) == 0 {
		return value
	}

	switch container := root.(type) {
	case map[string]any:
		key := path[0].(string)
		container[key] = modelSet(container[key], path[1:], value)
		return container
	case []any:
		index := path[0].(int)

		if index == len(container) {
			return append(container, value)
		}

		container[index] = modelSet(container[index], path[1:], value)
		return container
	}

	panic("the path is not in the model")
}

func modelRemove(root any, path []any) any {
	if len(path) == 1 {
		switch container := root.(type) {
		case map[string]any:
			delete(container, path[0].(string))
			return container
		case []any:
			return slices.Delete(container, path[0].(int), path[0].(int)+1)
		}
	}

	switch container := root.(type) {
	case map[string]any:
		key := path[0].(string)
		container[key] = modelRemove(container[key], path[1:])
		return container
	case []any:
		index := path[0].(int)
		container[index] = modelRemove(container[index], path[1:])
		return container
	}

	panic("the path is not in the model")
}

// Random changes keep the document valid, give the value that the same changes give a plain value, and leave text that prints back unchanged.
func TestTreeRandomChangesMatchTheSameChangesOnAValue(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	changes := 0

	for range 3000 {
		text := modelDocuments[random.IntN(len(modelDocuments))]
		document := mustParse(t, text)
		var expected any

		if err := document.Decode(&expected); err != nil {
			t.Fatal(err)
		}

		var history []string

		for range 1 + random.IntN(5) {
			paths := modelPaths(expected, nil)

			if len(paths) == 0 {
				break
			}

			path := paths[random.IntN(len(paths))]
			number := int64(random.IntN(10) - 5)

			switch random.IntN(3) {
			// Replace a value that is there.
			case 0:
				history = append(history, "set", formatAnyPath(path))

				err := document.Set(number, path...)
				checkSetDidNotMakeAnInvalidDocument(t, err)

				if err == nil {
					expected = modelSet(expected, path, number)
					changes++
				}
			// Add a member next to a value in an object.
			case 1:
				parentPath := path[:len(path)-1]
				parent, isObject := modelAt(expected, parentPath).(map[string]any)

				if !isObject {
					continue
				}

				if _, exists := parent["added"]; exists {
					continue
				}

				newPath := append(slices.Clone(parentPath), "added")
				history = append(history, "add", formatAnyPath(newPath))

				err := document.Set(number, newPath...)
				checkSetDidNotMakeAnInvalidDocument(t, err)

				if err == nil {
					expected = modelSet(expected, newPath, number)
					changes++
				}
			// Remove a value.
			default:
				history = append(history, "remove", formatAnyPath(path))
				isRemoved, err := document.Remove(path...)

				if err != nil {
					t.Fatalf("Remove(%v) of %q: %v", path, text, err)
				}

				if !isRemoved {
					continue
				}

				expected = modelRemove(expected, path)
				changes++
			}

			printed := document.String()
			var value any

			if err := Unmarshal([]byte(printed), &value); err != nil {
				t.Fatalf("not valid after %v on %q:\n%s\n%v", history, text, printed, err)
			}

			if !reflect.DeepEqual(value, expected) {
				t.Fatalf("after %v on %q:\n%s\ngot  %v\nwant %v", history, text, printed, value, expected)
			}

			if reparsed := mustParse(t, printed).String(); reparsed != printed {
				t.Fatalf("does not print back unchanged: %q", printed)
			}
		}
	}

	// Most changes must succeed, or the test checks little.
	if changes < 4000 {
		t.Fatalf("only %d changes succeeded", changes)
	}

	t.Logf("%d changes", changes)
}

func formatAnyPath(path []any) string {
	var parts []string

	for _, step := range path {
		switch step := step.(type) {
		case string:
			parts = append(parts, step)
		case int:
			parts = append(parts, "["+strconv.Itoa(step)+"]")
		}
	}

	return strings.Join(parts, ".")
}

func TestTreeReviewRegressions(t *testing.T) {
	t.Run("a new member gets the indentation of a line it shares with earlier members", func(t *testing.T) {
		checkChange(t, "a: {\n    b: 1, c: 2\n}\n", set(map[string]int{"k": 1}, "a", "d"), "a: {\n    b: 1, c: 2\n    d: {\n    \tk: 1\n    }\n}\n")
		checkChange(t, "a: {\n  b: 1, c: 2\n}\n", set(map[string]int{"k": 1}, "a", "c"), "a: {\n  b: 1, c: {\n  \tk: 1\n  }\n}\n")
	})

	t.Run("a Node is not a value", func(t *testing.T) {
		node, _ := NewNode("hello")
		document := mustParse(t, "a: 1\n")

		if err := document.Set(node, "a"); err == nil {
			t.Error("Set accepted a Node")
		}

		if _, err := NewNode(node); err == nil {
			t.Error("NewNode accepted a Node")
		}
	})

}

func TestTreeErrorPathOfAValue(t *testing.T) {
	document := mustParse(t, "a: {b: 1}\n")
	err := document.Set(map[string]any{"x": []any{1, math.NaN()}}, "a", "c")

	if err == nil || err.Error() != "soml: a.c.x[1]: NaN is not a SOML value" {
		t.Fatalf("Set gave %v", err)
	}

	_, err = NewNode(map[string]any{"x": math.NaN()})

	if err == nil || err.Error() != "soml: x: NaN is not a SOML value" {
		t.Fatalf("NewNode gave %v", err)
	}
}

func TestTreeAccessors(t *testing.T) {
	text := "{\n\t'a b': {c: 0o17},\n\td: [0b101, 12, \"x\", '''\n\t\ty\n\t\t'''],\n} # end\n"
	document := mustParse(t, text)

	if leading, trailing := document.OuterDecor(); leading != "" || trailing != " # end\n" {
		t.Errorf("outer decor %q %q", leading, trailing)
	}

	member := document.Root().(*Object).Members[0]
	key := member.Key
	span, ok := key.Span()

	if key.Value() != "a b" || key.Raw() != "'a b'" || !ok || text[span.Start:span.End] != "'a b'" || key.Style() != StyleLiteral {
		t.Errorf("key %q %q %v %v", key.Value(), key.Raw(), span, key.Style())
	}

	tests := []struct {
		path   []any
		radix  int
		style  StringStyle
		number any
	}{
		{[]any{"a b", "c"}, 8, StyleNone, int64(15)},
		{[]any{"d", 0}, 2, StyleNone, int64(5)},
		{[]any{"d", 1}, 10, StyleNone, int64(12)},
		{[]any{"d", 2}, 0, StyleEscaped, "x"},
		{[]any{"d", 3}, 0, StyleLiteralBlock, "y"},
	}

	for _, test := range tests {
		node, _ := document.Get(test.path...)
		scalar := node.(*Scalar)

		if scalar.Radix() != test.radix || scalar.Style() != test.style || scalar.Value() != test.number {
			t.Errorf("%v: radix %d, style %v, value %v", test.path, scalar.Radix(), scalar.Style(), scalar.Value())
		}
	}

	item, err := NewItem(1.5)

	if err != nil || item.Value.(*Scalar).Value() != 1.5 {
		t.Fatalf("NewItem gave %v, %v", item, err)
	}

	if _, err := NewItem(math.NaN()); err == nil {
		t.Error("NewItem accepted NaN")
	}
}

func TestTreeSetPrintsEveryKindOfValueInCanonicalForm(t *testing.T) {
	document := mustParse(t, "a: 1\n")
	instant := time.Date(2026, 9, 19, 14, 0, 0, 500_000_000, time.UTC)

	for _, change := range []struct {
		key   string
		value any
	}{
		{"null", nil},
		{"instant", instant},
		{"duration", 90 * time.Minute},
		{"bool", true},
		{"float", 0.1},
		{"string", "it's"},
	} {
		if err := document.Set(change.value, change.key); err != nil {
			t.Fatal(err)
		}
	}

	expected := "a: 1\nnull: null\ninstant: 2026-09-19T14:00:00.5Z\nduration: 1h30m\nbool: true\nfloat: 0.1\nstring: \"it's\"\n"

	if printed := document.String(); printed != expected {
		t.Fatalf("got %q", printed)
	}
}

func TestTreeRemoveRejectsAWrongPath(t *testing.T) {
	document := mustParse(t, "a: {b: 1}\nlist: [1]\n")

	if _, err := document.Remove("a", 1.5); err == nil {
		t.Error("a path element of another type")
	}

	if _, err := document.Remove("a", 0); err == nil {
		t.Error("an index on an object")
	}

	if _, err := document.Remove("list", -1); err == nil {
		t.Error("a negative position")
	}
}

func TestTreeCommentSpansAreInTheCurrentText(t *testing.T) {
	document := mustParse(t, "a: 1 # x\nb: 2 /* y */\n")

	if err := document.Set("a longer value", "a"); err != nil {
		t.Fatal(err)
	}

	text := document.String()

	for _, comment := range document.Comments() {
		if delimited := text[comment.Span.Start:comment.Span.End]; delimited != delimitedComment(comment) {
			t.Errorf("the span %v of %q is %q in %q", comment.Span, comment.Text, delimited, text)
		}
	}
}

func TestTreeNodesAreNotValues(t *testing.T) {
	node, _ := NewNode("hello")
	document := mustParse(t, "a: 1\n")

	for _, value := range []any{node, []any{node}, map[string]any{"x": document.Root()}, document, *document} {
		if _, err := Marshal([]any{value}); err == nil || !strings.Contains(err.Error(), "part of a syntax tree") {
			t.Errorf("Marshal of %T gave %v", value, err)
		}

		if err := document.Set(value, "b"); err == nil {
			t.Errorf("Set of %T gave no error", value)
		}
	}
}

func TestTreeAHeadingCommentStaysAboveWhenTheLastMemberIsRemovedAndAnotherIsAdded(t *testing.T) {
	document := mustParse(t, "# heading\na: 1\n")

	if _, err := document.Remove("a"); err != nil {
		t.Fatal(err)
	}

	if printed := document.String(); printed != "# heading\n{}\n" {
		t.Fatalf("after Remove: %q", printed)
	}

	if err := document.Set(2, "b"); err != nil {
		t.Fatal(err)
	}

	if printed := document.String(); printed != "# heading\n{\n\tb: 2\n}\n" {
		t.Fatalf("after Set: %q", printed)
	}
}

func TestTreeGetWithAnEmptyPathGivesTheRoot(t *testing.T) {
	document := mustParse(t, "a: 1\n")

	if node, ok := document.Get(); !ok || node != document.Root() {
		t.Fatalf("got %v %v", node, ok)
	}
}

// checkSetDidNotMakeAnInvalidDocument fails when Set refused a change because its own result was not valid, which means that Set laid out a change wrongly. Only the nesting limit can make a correct change invalid.
func checkSetDidNotMakeAnInvalidDocument(t *testing.T, err error) {
	t.Helper()

	if somlError, ok := errors.AsType[*Error](err); ok && strings.Contains(somlError.Message, ", because the change would make a document that is not valid: ") && !strings.Contains(somlError.Message, "nested more than") {
		t.Fatalf("Set made an invalid document: %v", err)
	}
}

func TestTreeKeysAreNotValues(t *testing.T) {
	document := mustParse(t, "a: 1\n")
	key := document.Root().(*Object).Members[0].Key

	if _, err := Marshal(map[string]any{"v": key}); err == nil {
		t.Errorf("Marshal of %T gave no error", key)
	}

	if _, err := NewNode(math.NaN()); err == nil || err.(*Error).Path != nil {
		t.Errorf("a whole-value error has the path %#v", err.(*Error).Path)
	}
}

func TestTreeAllYieldsEveryNodeWithItsPath(t *testing.T) {
	document := mustParse(t, "# Head\nserver: {host: 'a'}\nlist: [1, {b: true}]\n'server.port': 0x50\n")
	var got []string

	for path, node := range document.All() {
		text := "root"

		switch node := node.(type) {
		case *Scalar:
			text = node.Raw()
		case *Object:
			text = "object"
		case *Array:
			text = "array"
		}

		got = append(got, fmt.Sprint(path, " ", text))
	}

	expected := []string{
		"[] object",
		"[server] object",
		"[server host] 'a'",
		"[list] array",
		"[list 0] 1",
		"[list 1] object",
		"[list 1 b] true",
		"[server.port] 0x50",
	}

	if !slices.Equal(got, expected) {
		t.Fatalf("got %q\nwant %q", got, expected)
	}
}

func TestTreeAllStopsWhenTheLoopStops(t *testing.T) {
	document := mustParse(t, "a: [1, 2, 3]\nb: 4\n")
	count := 0

	for path := range document.All() {
		count++

		if slices.Equal(path, []any{"a", 1}) {
			break
		}
	}

	if count != 4 {
		t.Fatalf("yielded %d nodes, want 4", count)
	}
}

func TestTreeAllGivesPathsThatCanBeKept(t *testing.T) {
	document := mustParse(t, "a: {b: [1, 2]}\nc: {d: 3}\n")
	var paths [][]any

	for path := range document.All() {
		paths = append(paths, path)
	}

	expected := [][]any{nil, {"a"}, {"a", "b"}, {"a", "b", 0}, {"a", "b", 1}, {"c"}, {"c", "d"}}

	if !reflect.DeepEqual(paths, expected) {
		t.Fatalf("got %v\nwant %v", paths, expected)
	}
}

// Get finds every node that All yields at its path, in every valid corpus document.
func TestTreeAllAgreesWithGetOnTheCorpus(t *testing.T) {
	for _, name := range conformanceCases(t, "valid") {
		document := mustParse(t, string(readCase(t, filepath.Join(conformanceDirectory, "valid", name+".soml"))))

		for path, node := range document.All() {
			if found, ok := document.Get(path...); !ok || found != node {
				t.Fatalf("%s: Get(%v) does not find the node that All yields", name, path)
			}
		}
	}
}

var (
	blankLineAfterOpening  = regexp.MustCompile(`[\[{][ \t]*(#[^\n]*)?\n[ \t]*\n`)
	blankLineBeforeClosing = regexp.MustCompile(`\n[ \t]*\n[ \t]*[\]}]`)
	twoBlankLines          = regexp.MustCompile(`\n[ \t]*\n[ \t]*\n`)
)

// Every change to every corpus document and model document keeps it valid, keeps a formatted document formatted, and adds no run of two blank lines and no blank line directly inside brackets.
func TestTreeEveryChangeKeepsTheLayoutRules(t *testing.T) {
	// Blank lines and comments around entries that a change removes or adds next to.
	texts := append(slices.Clone(modelDocuments), "# Head\n\na: 1\n\n# End\n", "[\n\t1\n\n\t# c\n]\n", "x: {\n\ta: 1\n\n\t# c\n}\n", "# h\n\nx: 1\n\n# c\ny: 2\n\nb: 3\n", "{\n\t# h\n\n\tx: 1\n\n\t# c\n\ty: 2\n}\n", "a: 1\n\n# One\nb: [\n\t1\n\n\t2\n]\n\n# Two\nc: {d: 1}\n", "{\n\tp: 1\n\n\t# c\n\tq: 1\n\tz: 2\n}\n", "p: 1\n\n# c\nq: 1\nz: 2\n", "x: { # o\n\ta: 1\n\n\t# c\n}\n", "x: [ # o\n\t1\n\n\t2\n]\n")

	for _, name := range conformanceCases(t, "valid") {
		texts = append(texts, string(readCase(t, filepath.Join(conformanceDirectory, "valid", name+".soml"))))
	}

	for _, text := range slices.Clone(texts) {
		if formatted, err := Format([]byte(text)); err == nil {
			texts = append(texts, string(formatted))
		}
	}

	type change struct {
		name  string
		apply func(*Document) error
	}

	for _, text := range texts {
		var value any

		if err := Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}

		var changes []change

		for _, path := range modelPaths(value, nil) {
			changes = append(changes, change{fmt.Sprint("set ", path), set(int64(5), path...)})
			changes = append(changes, change{fmt.Sprint("set an object at ", path), set(map[string]any{"k": []any{int64(1)}}, path...)})
			changes = append(changes, change{fmt.Sprint("remove ", path), remove(path...)})
			parent := path[:len(path)-1]

			if _, ok := modelAt(value, parent).(map[string]any); ok {
				changes = append(changes, change{fmt.Sprint("add next to ", path), set(int64(5), append(slices.Clone(parent), "added", "x")...)})
			}

			if array, ok := modelAt(value, path).([]any); ok {
				changes = append(changes, change{fmt.Sprint("append to ", path), set(int64(5), append(slices.Clone(path), len(array))...)})
			}
		}

		isFormatted := false

		if formatted, err := Format([]byte(text)); err == nil {
			isFormatted = string(formatted) == text
		}

		// The lines of a block string are content, so they are left out of the blank line checks.
		hasBlockString := strings.Contains(text, "'''") || strings.Contains(text, `"""`)

		for _, change := range changes {
			document := mustParse(t, text)

			// A change that Set refuses, such as one through a scalar, leaves the document as it was.
			if err := change.apply(document); err != nil {
				if document.String() != text {
					t.Fatalf("%s of %q failed and changed the document: %v", change.name, text, err)
				}

				continue
			}

			printed := document.String()
			reparsed, err := Parse([]byte(printed))

			if err != nil || reparsed.String() != printed {
				t.Fatalf("%s of %q gave a document that is not valid: %q %v", change.name, text, printed, err)
			}

			if isFormatted {
				if formatted, _ := Format([]byte(printed)); string(formatted) != printed {
					t.Errorf("%s of the formatted %q:\n got %q\nwant %q", change.name, text, printed, formatted)
				}
			}

			if hasBlockString {
				continue
			}

			for _, pattern := range []*regexp.Regexp{twoBlankLines, blankLineAfterOpening, blankLineBeforeClosing} {
				if pattern.MatchString(printed) && !pattern.MatchString(text) {
					t.Errorf("%s of %q gave %q, which matches %s", change.name, text, printed, pattern)
				}
			}
		}
	}
}

// Removing what is not there changes nothing, also in an empty container.
func TestTreeRemovingWhatIsNotThereChangesNothing(t *testing.T) {
	for _, text := range []string{"a: {\n}\n", "a: { }\n", "c: {  }", "a: {\n\t\n}\n", "{ }", "[\n]", "a: [ ]\n"} {
		document := mustParse(t, text)

		for _, path := range [][]any{{0}, {"a", "missing"}, {"missing"}, {"c", "missing"}, {"a", 0}} {
			if isRemoved, err := document.Remove(path...); isRemoved {
				t.Errorf("%q: Remove(%v) reports that it removed something: %v", text, path, err)
			}
		}

		if document.String() != text {
			t.Errorf("%q became %q", text, document.String())
		}
	}
}
