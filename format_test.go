package soml

import (
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func mustFormat(t *testing.T, text string) string {
	t.Helper()
	formatted, err := Format([]byte(text))

	if err != nil {
		t.Fatalf("Format(%q): %v", text, err)
	}

	return string(formatted)
}

// commentTexts returns the comment texts of a document, sorted, without the trailing whitespace and the blank lines that the formatter removes inside a block comment.
func commentTexts(t *testing.T, text string) []string {
	t.Helper()
	var texts []string

	for _, comment := range mustParse(t, text).Comments() {
		var lines []string

		for line := range strings.SplitSeq(comment.Text, "\n") {
			if line = strings.TrimRight(line, " \t"); line != "" {
				lines = append(lines, line)
			}
		}

		texts = append(texts, strings.Join(lines, "\n"))
	}

	slices.Sort(texts)
	return texts
}

// checkFormatProperties checks every property the formatter must keep: the value, the comments, idempotence, no trailing whitespace outside block strings, and one line feed at the end.
func checkFormatProperties(t *testing.T, text string) {
	t.Helper()
	formatted := mustFormat(t, text)
	var value, expected any

	if err := Unmarshal([]byte(formatted), &value); err != nil {
		t.Fatalf("the formatted text of %q is not valid: %v\n%s", text, err, formatted)
	}

	if err := Unmarshal([]byte(text), &expected); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(value, expected) {
		t.Fatalf("the value of %q changed:\n%s", text, formatted)
	}

	if again := mustFormat(t, formatted); again != formatted {
		t.Fatalf("formatting %q is not idempotent:\n--- once\n%s\n--- twice\n%s", text, formatted, again)
	}

	if !slices.Equal(commentTexts(t, text), commentTexts(t, formatted)) {
		t.Fatalf("the comments of %q changed:\n%s", text, formatted)
	}

	if !strings.Contains(text, "'''") && !strings.Contains(text, `"""`) {
		for line := range strings.SplitSeq(formatted, "\n") {
			if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
				t.Fatalf("trailing whitespace in the formatted text of %q: %q", text, formatted)
			}
		}
	}

	if !strings.HasSuffix(formatted, "\n") || strings.HasSuffix(formatted, "\n\n") {
		t.Fatalf("the formatted text of %q does not end with exactly one line feed: %q", text, formatted)
	}
}

func TestFormatKeepsTheValueAndCommentsOfEveryCorpusDocument(t *testing.T) {
	for _, name := range conformanceCases(t, "valid") {
		t.Run(name, func(t *testing.T) {
			checkFormatProperties(t, string(readCase(t, filepath.Join(conformanceDirectory, "valid", name+".soml"))))
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name, text, formatted string
	}{
		{"lays out a document and keeps its comments and spelling", "\n\n# Head\n\nname:   'api'    # name\npool: {min: 2, max: 0x10,   /* inline */ extra: [1, /* c */ 2, 3 /* before comma */, 4\n]}\nempty: {}\nnoted: { # opening\n}\na: # after colon\n  1\n\n\n\n'tail.key': true  # end\n# Trailing comment\n", "# Head\n\nname: 'api' # name\npool: {\n\tmin: 2\n\tmax: 0x10\n\t/* inline */ extra: [\n\t\t1\n\t\t/* c */ 2\n\t\t3 /* before comma */\n\t\t4\n\t]\n}\nempty: {}\nnoted: { # opening\n}\na: # after colon\n\t1\n\n'tail.key': true # end\n# Trailing comment\n"},
		{"keeps a braced top level", "# Before\n{a: 1, b: {}} # After\n# Last\n", "# Before\n{a: 1, b: {}} # After\n# Last\n"},
		{"keeps a top-level array", "[1, [2, 3],\n{a: null}]", "[\n\t1\n\t[2, 3]\n\t{a: null}\n]\n"},
		{"removes commas", "a: [\n\t1,\n\t2,\n]\nb: {c: 1, d: 2,\n}", "a: [\n\t1\n\t2\n]\nb: {\n\tc: 1\n\td: 2\n}\n"},
		{"keeps a container on one line, without its trailing comma", "a: {b: 1,  c: [1,2,], d: {}}", "a: {b: 1, c: [1, 2], d: {}}\n"},
		{"removes the spaces directly inside the brackets of a container on one line, and before a comma", "a: [ 1 , { b: 2 } ]", "a: [1, {b: 2}]\n"},
		{"a line break splits only the containers around it", "m: [\n[1,0], [0, 1]\n]", "m: [\n\t[1, 0]\n\t[0, 1]\n]\n"},
		{"a line break inside a block comment splits the container", "a: [1, /* x\n */ 2]", "a: [\n\t1\n\t/* x\n */ 2\n]\n"},
		{"keeps a container with only a comment on one line", "a: [[ /* x */ ], 1]", "a: [[/* x */], 1]\n"},
		{"keeps the block comments of a container on one line in place", "[ /* a */ 1 /* b */ , /* c */ 2, /* d */ ]", "[/* a */ 1 /* b */, /* c */ 2 /* d */]\n"},
		{"lays out items that line breaks separate", "a: [\n80\n443\n]\nb: {\nc: 1\nd: 2\n}\nc: [1, 2\n3 # x\n4]", "a: [\n\t80\n\t443\n]\nb: {\n\tc: 1\n\td: 2\n}\nc: [\n\t1\n\t2\n\t3 # x\n\t4\n]\n"},
		{"keeps an empty array", "[]", "[]\n"},
		{"keeps a comment on the comma's line before a block comment that spans lines", "a: [1, /* a */ /* b\n */ 2]\n", "a: [\n\t1 /* a */\n\t/* b\n */ 2\n]\n"},
		{"keeps a comment on the opening line before a block comment that spans lines", "a: [/* a */ /* b\n */ 2]\n", "a: [ /* a */\n\t/* b\n */ 2\n]\n"},
		{"keeps a comment on the comma's line before a block comment that spans lines in an object", "a: {x: 1, /* a */ /* b\n */ y: 2}\n", "a: {\n\tx: 1 /* a */\n\t/* b\n */ y: 2\n}\n"},
		{"keeps the line of several block comments that span lines", "a: [1, /* a */ /* b\n */ /* c\n */ 2]\n", "a: [\n\t1 /* a */ /* b\n */\n\t/* c\n */ 2\n]\n"},
		{"keeps a comment in an empty array", "[ # nothing yet\n]", "[ # nothing yet\n]\n"},
		{"removes blank lines inside brackets and at the ends", "\n\na: [\n\n\t1,\n\n\n\t2,\n\n]\n\n\n", "a: [\n\t1\n\n\t2\n]\n"},
		{"indents with tabs", "a: {\n    b: {\n        c: 1\n    }\n}", "a: {\n\tb: {\n\t\tc: 1\n\t}\n}\n"},
		{"reindents a block string", "a: {\n    text: '''\n        first\n          indented\n\n        last\n        ''', b: 1}", "a: {\n\ttext:\n\t\t'''\n\t\tfirst\n\t\t  indented\n\n\t\tlast\n\t\t'''\n\tb: 1\n}\n"},
		{"reindents an escaped block string with a longer delimiter, and keeps a line of only spaces, which is content", "a: \"\"\"\"\n\\t\"\"\" quoted\n   \nend\n\"\"\"\"", "a:\n\t\"\"\"\"\n\t\\t\"\"\" quoted\n   \n\tend\n\t\"\"\"\"\n"},
		{"keeps the lines of a block comment", "a: [\n  /* one\n     two */\n  1]", "a: [\n\t/* one\n     two */\n\t1\n]\n"},
		{"removes trailing whitespace and blank lines inside a block comment", "a: [\n  /* one   \n\n\n     two */\n  1]", "a: [\n\t/* one\n\n     two */\n\t1\n]\n"},
		{"removes trailing whitespace inside a block comment at the top", "/* a \t\n\n\n\nb */\na: 1\n", "/* a\n\nb */\na: 1\n"},
		{"removes trailing whitespace inside a block comment at the end of a line", "a: 1 /* x  \n  \n y */\n", "a: 1 /* x\n\n y */\n"},
		{"a line comment after a block comment still ends the line", "'a.b':\n\t/* two */ # e */,\n 1", "'a.b':\n\t/* two */ # e */,\n\t1\n"},
		{"a comment at the end without a line feed stays on its line", "a: {b: 1}  # Note.", "a: {b: 1} # Note.\n"},
		{"a comment after the last item stays on its line", "[1 /* last */\n]", "[\n\t1 /* last */\n]\n"},
		{"a comment after a braced top level stays on its line", "{a: 1} /* after */", "{a: 1} /* after */\n"},
		{"a comment after a braced top level that spans lines stays on its line", "{a: 1\n} /* after */", "{\n\ta: 1\n} /* after */\n"},
		{"a comment in an empty object stays", "a: { /* empty */ }", "a: {/* empty */}\n"},
		{"a comment in an empty object that spans lines stays", "a: { /* empty */\n}", "a: { /* empty */\n}\n"},
		{"a value after a line comment and a block comment is indented by its line", "k: # a\n /* b */ [1\n]\n", "k: # a\n\t/* b */ [\n\t\t1\n\t]\n"},
		{"a nested value after a line comment and a block comment", "a: {\nk: # a\n /* b */ {x: 1\n}}", "a: {\n\tk: # a\n\t\t/* b */ {\n\t\t\tx: 1\n\t\t}\n}\n"},
		{"a block string after a line comment and a block comment", "k: # a\n /* b */ '''\n x\n '''", "k: # a\n\t/* b */\n\t'''\n\tx\n\t'''\n"},
		{"keeps a blank line after a top comment", "# c\n\n[1]\n", "# c\n\n[1]\n"},
		{"keeps a blank line before an end comment", "[1]\n\n# c\n", "[1]\n\n# c\n"},
		{"keeps a blank line before an end comment of a bare object", "a: 1\n\n# c\n", "a: 1\n\n# c\n"},
		{"keeps a blank line before a comment in brackets", "[\n1,\n\n# c\n]\n", "[\n\t1\n\n\t# c\n]\n"},
		{"keeps a block comment before a braced top level", "/* x */ {a: 1}", "/* x */ {a: 1}\n"},
		{"keeps Unicode whitespace at the end of a line comment", "a: 1 # a\u00a0  \n", "a: 1 # a\u00a0\n"},
		{"a comment on its own line between a key and its value keeps its line", "a:\n# c\n1\n", "a:\n\t# c\n\t1\n"},
		{"two comment lines between a key and its value", "a:\n\t# c\n\t# d\n\t1\nb: 2\n", "a:\n\t# c\n\t# d\n\t1\nb: 2\n"},
		{"blank lines between a key and its value go", "a:\n\n# c\n\n1\n", "a:\n\t# c\n\t1\n"},
		{"a comment between a key and its value in braces", "x: {a:\n# c\n1}\n", "x: {\n\ta:\n\t\t# c\n\t\t1\n}\n"},
		{"a comment between a key and an array", "a:\n# c\n[1\n]\n", "a:\n\t# c\n\t[\n\t\t1\n\t]\n"},
		{"a comment between a key and a block string", "a:\n# c\n'''\n x\n '''", "a:\n\t# c\n\t'''\n\tx\n\t'''\n"},
		{"a comment at the end of the key's line", "a: # c\n1\n", "a: # c\n\t1\n"},
		{"a comment at the end of the key's line and one below", "a: # c\n# d\n1\n", "a: # c\n\t# d\n\t1\n"},
		{"a block comment at the end of the key's line", "a: /* c */\n1\n", "a: /* c */\n\t1\n"},
		{"a block comment at the end of the key's line and one below", "a: /* c */\n/* d */\n1\n", "a: /* c */\n\t/* d */\n\t1\n"},
		{"a comment before the value on its line", "a:\n/* c */ 1\n", "a:\n\t/* c */ 1\n"},
		{"a comment line and a comment before the value", "a:\n# c\n/* d */ 1\n", "a:\n\t# c\n\t/* d */ 1\n"},
		{"a value below its key moves up", "a:\n1\n", "a: 1\n"},
		{"a block comment before the value on the key's line", "a: /* c */ 1\n", "a: /* c */ 1\n"},
		{"a block comment over two lines before the value", "a: /* x\ny */ 1", "a: /* x\ny */ 1\n"},
		{"a comment before a comma stays at the end of the line", "[1 /* c */, 2\n]\n", "[\n\t1 /* c */\n\t2\n]\n"},
		{"a comment before a comma in an object", "{a: 1 /* c */, b: 2\n}\n", "{\n\ta: 1 /* c */\n\tb: 2\n}\n"},
		{"two comments before a comma", "[1 /* c */ /* d */, 2\n]\n", "[\n\t1 /* c */ /* d */\n\t2\n]\n"},
		{"a block comment over two lines before a comma", "[1 /* c\nd */, 2]\n", "[\n\t1 /* c\nd */\n\t2\n]\n"},
		{"a comment after the comma stays before the next item", "[1 /* a */, /* b */ 2\n]", "[\n\t1 /* a */\n\t/* b */ 2\n]\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if formatted := mustFormat(t, test.text); formatted != test.formatted {
				t.Fatalf("Format(%q):\n got %q\nwant %q", test.text, formatted, test.formatted)
			}

			checkFormatProperties(t, test.text)
		})
	}
}

func TestFormatAfterChanges(t *testing.T) {
	document := mustParse(t, "a: 1 # Note.\n")

	if _, err := document.Remove("a"); err != nil {
		t.Fatal(err)
	}

	if formatted := mustFormat(t, document.String()); formatted != "{} # Note.\n" || mustFormat(t, formatted) != "{} # Note.\n" {
		t.Fatalf("got %q", formatted)
	}

	document = mustParse(t, "server: {host: 'a'}")

	if err := document.Set(8080, "server", "port"); err != nil {
		t.Fatal(err)
	}

	if formatted := mustFormat(t, document.String()); formatted != "server: {host: 'a', port: 8080}\n" {
		t.Fatalf("got %q", formatted)
	}
}

func TestFormatRejectsADocumentThatIsNotValid(t *testing.T) {
	if _, err := Format([]byte("a: 1\na: 2")); err == nil {
		t.Fatal("no error")
	}
}

// A respelling from the property test is formatted with every property kept.
func TestFormatKeepsThePropertiesOfRandomDocuments(t *testing.T) {
	generator := newGenerator(5)

	for range 3000 {
		checkFormatProperties(t, generator.document(map[string]any{"root": generator.value(3), "other": generator.value(2)}))
	}
}

// The golden cases of the JS reference's formatter tests, soml-js/test/format.ts.
func TestFormatMatchesTheReferenceCases(t *testing.T) {
	cases := [][2]string{
		{"a: {b: 1, c: [1,\n2], d: {}, e: [\n]}", "a: {\n\tb: 1\n\tc: [\n\t\t1\n\t\t2\n\t]\n\td: {}\n\te: []\n}\n"},
		{"a: {b: 1,  c: [1,2,], d: {}}", "a: {b: 1, c: [1, 2], d: {}}\n"},
		{"m: [\n[1,0], [0, 1]\n]", "m: [\n\t[1, 0]\n\t[0, 1]\n]\n"},
		{"a: [[ /* x */ ], 1]", "a: [[/* x */], 1]\n"},
		{"{a: 1}", "{a: 1}\n"},
		{"{\na: 1}", "{\n\ta: 1\n}\n"},
		{"[1,\n[2]]", "[\n\t1\n\t[2]\n]\n"},
		{"a:1\nb:    2", "a: 1\nb: 2\n"},
		{"a:\n\t1", "a: 1\n"},
		{"'b c': [0xFF, 1_000, 90m, \"x\", 'y', 2026-09-19T16:00:00+02:00]", "'b c': [0xFF, 1_000, 90m, \"x\", 'y', 2026-09-19T16:00:00+02:00]\n"},
		{"\n\n# Header   \n\n\n\na: 1  \n\n\n\nb: 2\n\n\n", "# Header\n\na: 1\n\nb: 2\n"},
		{"a: [\n\n1,\n\n2,\n\n]", "a: [\n\t1\n\n\t2\n]\n"},
		{"a: {\n\nb: 1\n\n}", "a: {\n\tb: 1\n}\n"},
		{"a: [1, # One\n2 # Two\n]", "a: [\n\t1 # One\n\t2 # Two\n]\n"},
		{"a: [1 /* One */\n]", "a: [\n\t1 /* One */\n]\n"},
		{"a: [\n\t1,\n\t2,\n]\nb: {\n\tc: 1,\n\td: [3, 4,],\n}", "a: [\n\t1\n\t2\n]\nb: {\n\tc: 1\n\td: [3, 4]\n}\n"},
		{"[1, /* x */ 2\n]", "[\n\t1\n\t/* x */ 2\n]\n"},
		{"a: [1 /* a */, /* b */ 2\n]", "a: [\n\t1 /* a */\n\t/* b */ 2\n]\n"},
		{"[1, /* a */ /* b */ 2\n]", "[\n\t1\n\t/* a */ /* b */ 2\n]\n"},
		{"[1 /* a */, /* b */ 2, /* c */]", "[1 /* a */, /* b */ 2 /* c */]\n"},
		{"/* a */ /* b */ a: 1", "/* a */ /* b */ a: 1\n"},
		{"a: [\n1,\n# Note\n2,\n# End\n]", "a: [\n\t1\n\t# Note\n\t2\n\t# End\n]\n"},
		{"a: [ # c\n1]", "a: [ # c\n\t1\n]\n"},
		{"a: { # only\n}", "a: { # only\n}\n"},
		{"{#\n\n1: []}", "{ #\n\t1: []\n}\n"},
		{"a: [\n# only\n]", "a: [\n\t# only\n]\n"},
		{"a: /* c */ 1", "a: /* c */ 1\n"},
		{"a: # c\n1", "a: # c\n\t1\n"},
		{"a: # c\n{b: 1}", "a: # c\n\t{b: 1}\n"},
		{"a: # c\n{\nb: 1}", "a: # c\n\t{\n\t\tb: 1\n\t}\n"},
		{"a:\n# c\n1", "a:\n\t# c\n\t1\n"},
		{"a: /* x\ny */ {b: 1}", "a: /* x\ny */ {b: 1}\n"},
		{"/* a\n   b */ key: 1", "/* a\n   b */ key: 1\n"},
		{"/* a\n\n\n\n   b   \n*/\nkey: 1", "/* a\n\n   b\n*/\nkey: 1\n"},
		{"# x \u00a0\t\na: 1", "# x \u00a0\na: 1\n"},
		{"a: 1 #\ufeff", "a: 1 #\ufeff\n"},
		{"pool: {\n    description: '''\n        one\n          two\n\n        ''',\n}", "pool: {\n\tdescription:\n\t\t'''\n\t\tone\n\t\t  two\n\n\t\t'''\n}\n"},
		{"a:\n\t'''\n\tx\n\t'''", "a:\n\t'''\n\tx\n\t'''\n"},
		{"a: [\n'''\nx\n'''\n]", "a: [\n\t'''\n\tx\n\t'''\n]\n"},
		{"a: '''\n\tx  \n\t'''", "a:\n\t'''\n\tx  \n\t'''\n"},
		{"a: [\n1,\n# One\n\n# Two\n2,\n]", "a: [\n\t1\n\t# One\n\n\t# Two\n\t2\n]\n"},
		{"# One\n\n# Two\na: 1", "# One\n\n# Two\na: 1\n"},
		{"# Head\n[1, # One\n2] # Tail", "# Head\n[\n\t1 # One\n\t2\n] # Tail\n"},
		{"a: [1,\n2] /* t */\nb: 3", "a: [\n\t1\n\t2\n] /* t */\nb: 3\n"},
		{"a: [1,\n/* one\n  two */\n2]", "a: [\n\t1\n\t/* one\n  two */\n\t2\n]\n"},
		{"a: # c\n\t'''\n\tx\n\t'''", "a: # c\n\t'''\n\tx\n\t'''\n"},
		{"a: '''\n  x\n   \n  y\n  '''", "a:\n\t'''\n\tx\n   \n\ty\n\t'''\n"},
	}

	for _, test := range cases {
		if formatted := mustFormat(t, test[0]); formatted != test[1] {
			t.Errorf("Format(%q):\n got %q\nwant %q", test[0], formatted, test[1])
		}

		checkFormatProperties(t, test[0])
	}
}

// respace replaces every run of spaces and tabs in decor, outside comments, with a random run, and keeps the line breaks.
func respace(random *rand.Rand, decor string) string {
	var builder strings.Builder
	previous := 0

	writeSpaces := func(text string) {
		for index, line := range strings.Split(text, "\n") {
			if index > 0 {
				builder.WriteByte('\n')
			}

			if strings.Trim(line, " \t") != "" {
				builder.WriteString(line)
				continue
			}

			builder.WriteString([]string{"", "", " ", "\t", " \t "}[random.IntN(5)])
		}
	}

	for part := range decorParts(decor) {
		if part.kind == lineFeedPart {
			continue
		}

		writeSpaces(decor[previous:part.start])
		builder.WriteString(decor[part.start:part.end])
		previous = part.end
	}

	writeSpaces(decor[previous:])
	return builder.String()
}

func respaceNode(random *rand.Rand, node Node) {
	switch node := node.(type) {
	case *Object:
		for _, member := range node.Members {
			member.Decor.Leading = respace(random, member.Decor.Leading)
			member.Decor.Trailing = respace(random, member.Decor.Trailing)
			member.afterColon = respace(random, member.afterColon)
			member.separator.beforeComma = respace(random, member.separator.beforeComma)
			respaceNode(random, member.Value)
		}

		node.ClosingDecor = respace(random, node.ClosingDecor)
	case *Array:
		for _, item := range node.Items {
			item.Decor.Leading = respace(random, item.Decor.Leading)
			item.Decor.Trailing = respace(random, item.Decor.Trailing)
			item.separator.beforeComma = respace(random, item.separator.beforeComma)
			respaceNode(random, item.Value)
		}

		node.ClosingDecor = respace(random, node.ClosingDecor)
	}
}

// Other spaces and tabs between the tokens and comments of a document, with the same line breaks, give the same formatted text, as in the JS reference's tests.
func TestFormatIgnoresSpacesAndTabsBetweenTokens(t *testing.T) {
	generator := newGenerator(7)
	random := rand.New(rand.NewPCG(8, 9))
	var documents []string

	for _, name := range conformanceCases(t, "valid") {
		documents = append(documents, string(readCase(t, filepath.Join(conformanceDirectory, "valid", name+".soml"))))
	}

	for range 2000 {
		documents = append(documents, generator.document(map[string]any{"root": generator.value(3)}))
	}

	for _, text := range documents {
		formatted := mustFormat(t, text)
		document := mustParse(t, text)
		document.leading = respace(random, document.leading)
		document.trailing = respace(random, document.trailing)
		respaceNode(random, document.root)
		respaced := document.String()

		if again, err := Format([]byte(respaced)); err != nil || string(again) != formatted {
			t.Fatalf("respacing %q as %q changed the formatted text:\n%q\n%q (%v)", text, respaced, formatted, again, err)
		}
	}
}
