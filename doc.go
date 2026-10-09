/*
Package soml reads and writes SOML, a config format for humans.

SOML keeps JSON's tree of objects and arrays and its strictness, and adds comments, bare keys, literal strings, block strings, instants, durations, trailing commas, and line breaks between the items of an array or object. A value's type comes from its syntax, never from its content: '123' is a string and 123 is an int. See https://github.com/soml-lang/soml for the specification.

# Reading

Unmarshal checks every rule of the specification, and decodes a document into a Go value, such as a struct, a map, or an `any`:

	var config Config
	err := soml.Unmarshal(data, &config)

SOML's types map directly to Go: an int is an int64, a float a float64, an instant a time.Time, and a duration a time.Duration. They stay apart when a document is decoded into an `any`.

UnmarshalRead reads the document from an io.Reader, such as a request body.

# Writing

Marshal writes a Go value in the layout and spelling of canonical form: one member per line, tab indentation, and no commas. Struct fields keep the order they are declared in, as in encoding/json, so a file for people reads in a sensible order, and map keys are sorted. With the Canonical option, it writes canonical form itself, where struct fields are sorted by key too, so equal values give equal bytes, and the output can be hashed, signed, or compared. MarshalWrite writes it to an io.Writer.

# Formatting

Format changes only the layout of a document, as the formatter in the specification says, and keeps its comments, member order, and the spelling of every value. An object or an array whose brackets are on one line stays on one line, and the others get one member or item per line. The somlfmt command, in cmd/somlfmt, formats files as gofmt does, with the same -l, -d, and -w flags.

# Changing a document

Parse reads a document into a lossless syntax tree, a Document. An unchanged document prints the exact text it was parsed from. A changed one keeps the comments, blank lines, indentation, member order, and the author's spelling of every key and value outside the change, and every node has its position, for linters and editors. Document.All goes through every node with its path.

# Errors

Every returned error is an *Error. Errors about a place in a document have a line and column, including type mismatches. Errors about a value from decoding or Marshal also have its path, such as ["servers", 1, "port"].

# Limitations

  - Nesting is limited to exactly 100 levels, as the specification requires. Every object and array counts, including the document's own.
  - A struct field matches a key exactly. Without a tag, the key is the Go field name, so most fields need a tag such as `soml:"release-date"`.
  - A float32 is written with the shortest digits that read back as the same float32, as in encoding/json, so `float32(0.1)` is written 0.1, and not as the float64 it converts to.
  - Object members in an `any` are a map[string]any, so their order is not kept. Member order is not part of a value in SOML. A Document keeps the written order.
  - Only time.Time and time.Duration themselves are an instant and a duration. A type defined from them, such as `type Timeout time.Duration`, is an int, as its kind is, and a struct that embeds a time.Time gets its MarshalText method and is written as a string, as in encoding/json.
  - Unmarshal makes one copy of the input, and the strings it decodes share that copy, so a decoded string keeps the whole input in memory.
  - Reading takes memory in proportion to the input: for a typical config file, about 5 to 10 times its size for Unmarshal and about 15 times for Parse. A service that reads documents from untrusted sources should limit the size of the input.
  - A method promoted through a nil embedded pointer, as in `struct{ *time.Time }`, panics when Marshal or Unmarshal calls it, as in encoding/json.
*/
package soml
