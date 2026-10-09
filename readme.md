# soml-go

> [SOML](https://soml.sh) for Go: a strict reader, a writer, a formatter, and a lossless syntax tree

> [!NOTE]
> SOML is a config format for humans. The format is a draft.

## Highlights

- Checks every rule in the spec: duplicate keys, a `.` in a bare key, the int64 range, instant and duration ranges, raw control characters, the nesting limit, and more
- Errors with line and column, also when a valid document does not fit your type
- SOML types map directly to Go: `int64`, `float64`, `time.Time`, and `time.Duration`
- Writes struct fields in declared order, or canonical form for stable bytes
- A formatter, and `somlfmt`, a `gofmt` for SOML
- A lossless syntax tree that keeps comments and spelling when you edit a document
- Passes the spec's [conformance suite](https://github.com/soml-lang/soml/tree/main/conformance) and matches the reference implementation on millions of generated documents
- No dependencies

## Install

Use the latest commit on `main` for now:

```sh
go get github.com/soml-lang/soml-go@main
```

The package is `soml`. Requires Go 1.27 or later.

## Usage

[API documentation](https://pkg.go.dev/github.com/soml-lang/soml-go)

### Read

```go
type Postgres struct {
	Host string `soml:"host"`
	Port uint16 `soml:"port"`
}

type Config struct {
	Name       string        `soml:"name"`
	Replicas   int           `soml:"replicas"`
	Timeout    time.Duration `soml:"timeout"`
	DeployedAt time.Time     `soml:"deployed-at"`
	Labels     []string      `soml:"labels"`
	Postgres   Postgres      `soml:"postgres"`
}

data := []byte(`
/* The edge service. */
name: 'api-gateway'
replicas: 3
timeout: 1m30s # Including retries.
deployed-at: 2026-09-19T14:00:00Z
labels: ['prod', 'eu-west']
postgres: {
	host: 'db.internal'
	port: 5432
}
`)

var config Config
err := soml.Unmarshal(data, &config)

fmt.Println(config.Postgres.Port, config.Timeout)
//=> 5432 1m30s
```

Indentation never matters, so a document can be indented like the code around it.

Fields the document does not set keep their value, so you can set defaults before decoding. Maps merge (also `map[string]any`) and slices are replaced. Unknown keys are ignored, unless you pass `soml.RejectUnknownKeys()`, which catches typos:

```go
err := soml.Unmarshal([]byte("port: 80\nprot: 81"), &server, soml.RejectUnknownKeys())
//=> soml: line 2, column 1: unknown key prot; a Go value of type main.Server has no field for it
```

To read from an `io.Reader`, use `soml.UnmarshalRead`. Limit untrusted input with `http.MaxBytesReader`, not `io.LimitReader`, which cuts the document short without an error:

```go
err := soml.UnmarshalRead(http.MaxBytesReader(writer, request.Body, 1<<20), &config)
```

### Write

```go
type Server struct {
	Port  int      `soml:"port"`
	Hosts []string `soml:"hosts"`
}

data, err := soml.Marshal(Server{Port: 8080, Hosts: []string{"a", "b"}})
//=> "port: 8080\nhosts: [\n\t'a'\n\t'b'\n]\n"
```

The output uses the canonical layout: one member per line, tab indentation, and no commas. Struct fields keep their declared order, as in `encoding/json`, so files stay readable. Map keys are sorted.

`soml.Canonical()` also sorts struct fields, so the same value always gives the same bytes, for hashing, signing, or comparing:

```go
data, err := soml.Marshal(Server{Port: 8080, Hosts: []string{"a", "b"}}, soml.Canonical())
//=> "hosts: [\n\t'a'\n\t'b'\n]\nport: 8080\n"
```

`soml.MarshalWrite` writes to an `io.Writer`.

### Values of unknown shape

```go
var value map[string]any
err := soml.Unmarshal([]byte("port: 8080\nratio: 0.5"), &value)

fmt.Printf("%T %T\n", value["port"], value["ratio"])
//=> int64 float64
```

Ints and floats are different types, as in the spec: `3` is an `int64` and `3.0` is a `float64`.

### Change a document and keep its comments

```go
document, err := soml.Parse([]byte("/* The edge service. */\nport: 0x1F90 # The default.\nserver: {host: 'a'}\n"))

err = document.Set(8080, "port")
err = document.Set(443, "server", "port")
err = document.Set(3, "replicas")

fmt.Print(document)
//=> /* The edge service. */
//   port: 8080 # The default.
//   server: {host: 'a', port: 443}
//   replicas: 3
```

An unchanged document prints its exact source. A changed one keeps the comments, blank lines, indentation, member order, and spelling outside the change. New values are written as `Marshal` writes them, on one line inside a one-line container. A path is a list of keys and array indexes (`int`).

Every parsed node has a position, for linters and editors:

```go
document, err := soml.Parse([]byte("port: 0x1F90\nhosts: ['a', \"b\"]\n"))

port, _ := document.Get("port")
span, _ := port.Span()
line, column := document.Position(span.Start)
//=> 1 7
```

`Set` and `Remove` keep the document valid and follow the [rules for editing](https://github.com/soml-lang/soml/blob/main/spec.md#editing) in the spec, so a formatted document stays formatted, with the same bytes as the JS reference. In short:

- A new member goes after the last member of its object, and missing objects on the path are created. A new item goes at the end of its array, at the index that is its length.
- A removed member or item takes its comma and the comments on its line. Comments on their own line stay.
- Removing something that does not exist changes nothing, and `Remove` reports whether it removed something. A path that does not fit the document is an error.

You can also change the nodes directly through `document.Root()`, and then check the result against the spec with `document.Decode(&value)`.

`document.All()` iterates every node with its path:

```go
for path, node := range document.All() {
	if scalar, ok := node.(*soml.Scalar); ok {
		fmt.Println(path, scalar.Raw())
	}
}
//=> [port] 0x1F90
//   [hosts 0] 'a'
//   [hosts 1] "b"
```

### Format

```go
formatted, err := soml.Format([]byte("pool: {min: 2,   max: 0x10,} # Connections"))
//=> "pool: {min: 2, max: 0x10} # Connections\n"

formatted, err = soml.Format([]byte("pool: {\nmin: 2, max: 0x10}"))
//=> "pool: {\n\tmin: 2\n\tmax: 0x10\n}\n"
```

The formatter changes only the layout: one tab per level, one member or item per line in multiline containers, no commas, and no trailing whitespace or repeated blank lines. A container whose brackets are on one line stays on one line, as in `ports: [80, 443]`. Add a line break inside it to expand it. Comments, order, block strings, and spelling stay, so the value never changes. The output matches the JS reference. To format a changed `Document`, format its `Bytes()`.

#### somlfmt

```sh
go install github.com/soml-lang/soml-go/cmd/somlfmt@main
```

Works like `gofmt`. Without a path, it formats standard input. `-l` lists unformatted files, `-d` shows a diff and exits with 1 if there are changes, and `-w` writes the result back. Folders are searched recursively for `.soml` files, skipping files whose names start with a dot and symbolic links found during the search. Paths given explicitly are followed, including symbolic links and hidden files.

```sh
somlfmt -w .
somlfmt -d . # In CI.
```

### Errors

```go
type Server struct {
	Port uint16 `soml:"port"`
}

var server Server
err := soml.Unmarshal([]byte("port: 70000"), &server)
//=> soml: line 1, column 7: the int 70000 does not fit in a Go value of type uint16

if somlError, ok := errors.AsType[*soml.Error](err); ok {
	fmt.Println(somlError.Line, somlError.Column, somlError.Message)
	//=> 1 7 the int 70000 does not fit in a Go value of type uint16
}
```

`Path` is the path to the value or key, in the form `Document.Get` takes, such as `[]any{"servers", 1, "port"}`. Its keys are as the document wrote them, so do not print them to a terminal as they are. `Error()` and `Message` replace control characters. `Marshal` errors have no position, so they name the path: `soml: metrics[1].ratio: NaN is not a SOML value`.

## Types

| SOML | Go | Notes |
|---|---|---|
| string | `string`, `[]byte` and `[N]byte` (base64), a type with `UnmarshalText` | |
| int | integer types | Range-checked both ways. A `uint64` above `math.MaxInt64` cannot be written. |
| int | `float64`, `float32` | Only when exact. |
| float | `float64`, `float32` | Never decodes into an integer type. NaN cannot be written, and `-0.0` is written `0.0`. |
| bool | `bool` | |
| null | pointer, interface, map, slice | An error for other types, so use a pointer to accept it. |
| instant | `time.Time` | In UTC, with nanoseconds. |
| duration | `time.Duration` | Exact, in nanoseconds. |
| array | slice, array | A Go array must have exactly as many items. |
| object | struct, map | A map key can be a string, an integer (`404: 'x'`), a type with `MarshalText` and `UnmarshalText` (such as `time.Time`), or a pointer to one. `MarshalSOML` and `UnmarshalSOML` are not used for keys, as in `encoding/json`. |
| any value | `any` | `map[string]any`, `[]any`, `string`, `int64`, `float64`, `bool`, `time.Time`, `time.Duration`, or `nil` |

### Struct tags

Like the `json` tag: `soml:"name,omitzero,omitempty"`, or `soml:"-"` to skip a field. `omitzero` skips zero values, or values whose `IsZero()` returns true. `omitempty` skips values written as null, an empty string, `[]`, or `{}`. Without a tag, the key is the field name, matched exactly, without case folding. Embedded structs work as in `encoding/json`.

### Custom types

For a type that is a string in SOML, implement `encoding.TextMarshaler` and `encoding.TextUnmarshaler`. Otherwise, implement:

```go
// MarshalSOML returns a Go value that is written in place of the receiver.
func (level Level) MarshalSOML() (any, error)

// UnmarshalSOML gets the value as decoding into an `any` gives it.
func (level *Level) UnmarshalSOML(value any) error
```

## Format

Documents follow the [SOML specification](https://soml.sh).

- File extension: `.soml`
- Encoding: UTF-8 without a byte order mark, with LF line endings
- Media type: `application/soml`
- Uniform type identifier: `com.sindresorhus.soml`

## Performance

Time per document (lower is faster) for the [shared benchmark fixtures](https://github.com/soml-lang/soml/tree/main/benchmark), with the same data in each format (the sizes are of the SOML files), on an Apple M4 Pro with Go 1.27.1 and `GOMAXPROCS=1`. The machine was busy, so the times are the CPU time of the whole process (median of 10 runs at 100 ms per benchmark). This includes garbage collection, so a library that allocates more is charged for it. `platform` is read into a map and written from one. The others use a struct. Each library writes the same Go value.

| | soml-go | encoding/json | go-toml/v2 | BurntSushi/toml |
|---|---|---|---|---|
| Read `service` (3 KB) | 18 µs | 12 µs | 17 µs | 127 µs |
| Read `platform` (35 KB) | 129 µs | 188 µs | 133 µs | 894 µs |
| Read `earthquakes` (733 KB) | 3.9 ms | 3.0 ms | 4.9 ms | 33 ms |
| Write `service` | 12 µs | 7 µs | 14 µs | 65 µs |
| Write `platform` | 146 µs | 75 µs | 176 µs | 439 µs |
| Write `earthquakes` | 3.7 ms | 1.5 ms | 3.2 ms | 17 ms |

Reading builds the whole document as a tree before decoding. `encoding/json` only validates it first.

To run the [benchmarks](benchmarks) with these settings, use `go test -bench . -cpu 1 -benchtime 100ms -count 10` in that directory.

## Limitations

- Nesting is limited to 100 levels, as the spec requires. Every object and array counts, including the document's own.
- A `float32` is written with the shortest digits that read back as the same `float32`, as in `encoding/json`, so `float32(0.1)` is written `0.1`.
- Objects in an `any` are a `map[string]any`, so member order is lost. Order is not part of a SOML value. A `Document` keeps it.
- Only `time.Time` and `time.Duration` themselves are an instant and a duration. `type Timeout time.Duration` is an int, and a struct that embeds a `time.Time` is a string through its promoted `MarshalText`, as in `encoding/json`.
- A method promoted through a nil embedded pointer, as in `struct{ *time.Time }`, panics when called, as in `encoding/json`.
- Decoded strings share one copy of the input, so keeping a small string keeps the whole input in memory. Use `strings.Clone` when that matters.
- Reading takes memory in proportion to the input: about 5 to 10 times its size for `Unmarshal` and 15 times for `Parse`. Limit the size of untrusted input, such as with `http.MaxBytesReader`.
