package soml_test

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/soml-lang/soml-go"
)

func ExampleUnmarshal() {
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

	if err := soml.Unmarshal(data, &config); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(config.Postgres.Port, config.Timeout, config.DeployedAt.Year())
	// Output: 5432 1m30s 2026
}

func ExampleMarshal() {
	type Server struct {
		Port  int      `soml:"port"`
		Hosts []string `soml:"hosts"`
	}

	data, err := soml.Marshal(Server{Port: 8080, Hosts: []string{"a", "b"}})

	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Print(string(data))
	// Output:
	// port: 8080
	// hosts: [
	// 	'a'
	// 	'b'
	// ]
}

func ExampleCanonical() {
	type Server struct {
		Port  int      `soml:"port"`
		Hosts []string `soml:"hosts"`
	}

	data, err := soml.Marshal(Server{Port: 8080, Hosts: []string{"a", "b"}}, soml.Canonical())

	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Print(string(data))
	// Output:
	// hosts: [
	// 	'a'
	// 	'b'
	// ]
	// port: 8080
}

func ExampleUnmarshal_any() {
	var value map[string]any

	if err := soml.Unmarshal([]byte("port: 8080\nratio: 0.5\nhosts: ['a', 'b']"), &value); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("%T %T %v\n", value["port"], value["ratio"], value["hosts"])
	// Output: int64 float64 [a b]
}

func ExampleError() {
	type Server struct {
		Port uint16 `soml:"port"`
	}

	var server Server
	err := soml.Unmarshal([]byte("port: 70000"), &server)
	fmt.Println(err)

	if somlError, ok := errors.AsType[*soml.Error](err); ok {
		fmt.Println(somlError.Line, somlError.Column)
	}

	// Output:
	// soml: line 1, column 7: the int 70000 does not fit in a Go value of type uint16
	// 1 7
}

func ExampleRejectUnknownKeys() {
	type Server struct {
		Port int `soml:"port"`
	}

	var server Server
	err := soml.Unmarshal([]byte("port: 80\nprot: 81"), &server, soml.RejectUnknownKeys())
	fmt.Println(err)
	// Output: soml: line 2, column 1: unknown key prot; a Go value of type soml_test.Server has no field for it
}

func ExampleParse() {
	document, err := soml.Parse([]byte("/* The edge service. */\nport: 0x1F90 # The default.\nserver: {host: 'a'}\n"))

	if err != nil {
		fmt.Println(err)
		return
	}

	if err := document.Set(8080, "port"); err != nil {
		fmt.Println(err)
		return
	}

	if err := document.Set(443, "server", "port"); err != nil {
		fmt.Println(err)
		return
	}

	if err := document.Set(3, "replicas"); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Print(document)
	// Output:
	// /* The edge service. */
	// port: 8080 # The default.
	// server: {host: 'a', port: 443}
	// replicas: 3
}

func ExampleDocument_Get() {
	document, err := soml.Parse([]byte("port: 0x1F90\nhosts: ['a', \"b\"]\n"))

	if err != nil {
		fmt.Println(err)
		return
	}

	port, _ := document.Get("port")
	host, _ := document.Get("hosts", 1)
	span, _ := port.Span()
	line, column := document.Position(span.Start)

	fmt.Println(port.(*soml.Scalar).Value(), port.(*soml.Scalar).Raw(), host.(*soml.Scalar).Style() == soml.StyleEscaped, line, column)
	// Output: 8080 0x1F90 true 1 7
}

func ExampleFormat() {
	formatted, err := soml.Format([]byte("pool: {min: 2,   max: 0x10} # Connections\nhosts: ['a',\n'b']"))

	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Print(string(formatted))
	// Output:
	// pool: {min: 2, max: 0x10} # Connections
	// hosts: [
	// 	'a'
	// 	'b'
	// ]
}

func ExampleUnmarshalRead() {
	type Server struct {
		Port int `soml:"port"`
	}

	// A request body or a file, for example. Limit the size of input from an untrusted source, such as with http.MaxBytesReader.
	reader := strings.NewReader("port: 8080\n")
	var server Server

	if err := soml.UnmarshalRead(reader, &server); err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(server.Port)
	// Output: 8080
}

func ExampleDocument_All() {
	document, err := soml.Parse([]byte("port: 0x1F90\nhosts: ['a', \"b\"]\n"))

	if err != nil {
		fmt.Println(err)
		return
	}

	for path, node := range document.All() {
		if scalar, ok := node.(*soml.Scalar); ok {
			fmt.Println(path, scalar.Raw())
		}
	}

	// Output:
	// [port] 0x1F90
	// [hosts 0] 'a'
	// [hosts 1] "b"
}
