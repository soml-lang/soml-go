package benchmarks

// Benchmarks for the operations whose speed the readme reports, against the most used JSON and TOML packages. The fixtures in testdata are a copy of the shared benchmark fixtures in the soml repository, each with the same data in SOML, JSON, and TOML. The readme in testdata says what each one measures. The formats are not the same size, so compare the time for each operation, not the throughput.
//
// Run them with:
//
//	go test -bench . -cpu 1 -benchtime 100ms -count 10 | tee results.txt
//	go run golang.org/x/perf/cmd/benchstat@latest -col /library results.txt
//
// To update the fixtures, check out the soml repository next to this one and run:
//
//	rsync --archive --delete ../../soml/benchmark/ testdata/

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	burntsushi "github.com/BurntSushi/toml"
	gotoml "github.com/pelletier/go-toml/v2"
	"github.com/soml-lang/soml-go"
)

type fixture struct {
	name string
	// newValue returns a pointer to a new struct of the shape of the fixture. It is nil for a fixture that is too large to describe as a struct, which is then only read into a map and written from one.
	newValue func() any
}

var fixtures = []fixture{
	{
		name: "service",
		newValue: func() any {
			return new(service)
		},
	},
	{
		name: "platform",
	},
	{
		name: "earthquakes",
		newValue: func() any {
			return new(earthquakes)
		},
	},
}

// value returns the data of the fixture as soml reads it, which every library writes in the benchmark.
func (fixture fixture) value(tb testing.TB) any {
	value := fixture.newValue
	if value == nil {
		value = func() any {
			return new(map[string]any)
		}
	}

	result := value()
	if err := soml.Unmarshal(libraries[0].data(tb, fixture), result); err != nil {
		tb.Fatal(err)
	}

	return result
}

type library struct {
	name      string
	extension string
	unmarshal func([]byte, any) error
	marshal   func(any) ([]byte, error)
}

var libraries = []library{
	{
		name:      "soml",
		extension: "soml",
		unmarshal: func(data []byte, value any) error {
			return soml.Unmarshal(data, value)
		},
		marshal: func(value any) ([]byte, error) {
			return soml.Marshal(value)
		},
	},
	{"encoding-json", "json", json.Unmarshal, json.Marshal},
	{"go-toml", "toml", gotoml.Unmarshal, gotoml.Marshal},
	{"BurntSushi-toml", "toml", burntsushi.Unmarshal, burntsushi.Marshal},
}

func (library library) data(tb testing.TB, fixture fixture) []byte {
	data, err := os.ReadFile(filepath.Join("testdata", fixture.name+"."+library.extension))
	if err != nil {
		tb.Fatal(err)
	}

	return data
}

// The benchmarks are only fair if every format of a fixture holds the same data. The data is compared as JSON, because BurntSushi/toml decodes an array of tables in an `any` into a []map[string]any, where the others give a []any.
func TestFixturesAreTheSame(t *testing.T) {
	asJSON := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}

	for _, fixture := range fixtures {
		// The fixture script of the JavaScript implementation also checks this, for every fixture.
		if fixture.newValue == nil {
			continue
		}

		want := fixture.newValue()
		if err := soml.Unmarshal(libraries[0].data(t, fixture), want, soml.RejectUnknownKeys()); err != nil {
			t.Fatalf("%s: %v", fixture.name, err)
		}

		for _, library := range libraries[1:] {
			got := fixture.newValue()
			if err := library.unmarshal(library.data(t, fixture), got); err != nil {
				t.Fatalf("%s: %s: %v", fixture.name, library.name, err)
			}

			if asJSON(got) != asJSON(want) {
				t.Errorf("%s: %s.%s holds different data than %s.soml", library.name, fixture.name, library.extension, fixture.name)
			}
		}
	}
}

// measure runs the benchmark loop, and also reports the CPU time of each operation where the platform has it. On a busy machine, the CPU time changes little, while the wall time also counts the time that other processes have the CPU.
func measure(b *testing.B, run func() error) {
	b.ReportAllocs()
	start := cpuTime()

	for b.Loop() {
		if err := run(); err != nil {
			b.Fatal(err)
		}
	}

	if start >= 0 {
		b.ReportMetric(float64(cpuTime()-start)/float64(b.N), "cpu-ns/op")
	}
}

func BenchmarkUnmarshalIntoStruct(b *testing.B) {
	for _, fixture := range fixtures {
		if fixture.newValue == nil {
			continue
		}

		b.Run("fixture="+fixture.name, func(b *testing.B) {
			for _, library := range libraries {
				b.Run("library="+library.name, func(b *testing.B) {
					data := library.data(b, fixture)

					measure(b, func() error {
						return library.unmarshal(data, fixture.newValue())
					})
				})
			}
		})
	}
}

func BenchmarkUnmarshalIntoAny(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run("fixture="+fixture.name, func(b *testing.B) {
			for _, library := range libraries {
				b.Run("library="+library.name, func(b *testing.B) {
					data := library.data(b, fixture)

					measure(b, func() error {
						var value map[string]any
						return library.unmarshal(data, &value)
					})
				})
			}
		})
	}
}

func BenchmarkMarshal(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run("fixture="+fixture.name, func(b *testing.B) {
			value := fixture.value(b)

			for _, library := range libraries {
				b.Run("library="+library.name, func(b *testing.B) {
					measure(b, func() error {
						_, err := library.marshal(value)
						return err
					})
				})
			}
		})
	}
}

func BenchmarkMarshalBytes(b *testing.B) {
	for _, testCase := range []struct {
		name  string
		value any
	}{
		{"slice", []byte("hello world")},
		{"array", [11]byte([]byte("hello world"))},
		{"addressable-array", new([11]byte([]byte("hello world")))},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			value := map[string]any{"value": testCase.value}

			measure(b, func() error {
				_, err := soml.Marshal(value)
				return err
			})
		})
	}
}

func BenchmarkFormatBlockComment(b *testing.B) {
	for _, testCase := range []struct {
		name  string
		lines int
	}{
		{"short", 4},
		{"long", 400},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			data := []byte("/*\n" + strings.Repeat("comment line  \n", testCase.lines) + "*/\nvalue: 1\n")

			measure(b, func() error {
				_, err := soml.Format(data)
				return err
			})
		})
	}
}

func BenchmarkMarshalInstants(b *testing.B) {
	for _, nanoseconds := range []int{0, 123_456_789} {
		b.Run(fmt.Sprintf("nanoseconds=%d", nanoseconds), func(b *testing.B) {
			values := make([]time.Time, 100)
			start := time.Date(2026, 10, 16, 12, 0, 0, nanoseconds, time.UTC)

			for index := range values {
				values[index] = start.Add(time.Duration(index) * time.Second)
			}

			measure(b, func() error {
				_, err := soml.Marshal(values)
				return err
			})
		})
	}
}

func BenchmarkUnmarshalLargeSlice(b *testing.B) {
	data := []byte("[" + strings.Repeat("{Value: 1},", 300) + "]")

	measure(b, func() error {
		var values []struct {
			Value   int
			Padding [4096]byte
		}

		return soml.Unmarshal(data, &values)
	})
}

func BenchmarkFormatBlockString(b *testing.B) {
	for _, lines := range []int{4, 400} {
		b.Run(fmt.Sprintf("lines=%d", lines), func(b *testing.B) {
			data := []byte("outer: {\n    value: '''\n" + strings.Repeat("        content\n   \n", lines) + "        '''\n}\n")

			measure(b, func() error {
				_, err := soml.Format(data)
				return err
			})
		})
	}
}

func BenchmarkSetAppend(b *testing.B) {
	for _, items := range []int{2, 100} {
		b.Run(fmt.Sprintf("items=%d", items), func(b *testing.B) {
			data := []byte("values: [" + strings.Repeat("1, ", items) + "]\n")

			measure(b, func() error {
				document, err := soml.Parse(data)

				if err != nil {
					return err
				}

				return document.Set(3, "values", items)
			})
		})
	}
}

func BenchmarkParse(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run("fixture="+fixture.name, func(b *testing.B) {
			data := libraries[0].data(b, fixture)

			measure(b, func() error {
				_, err := soml.Parse(data)
				return err
			})
		})
	}
}

func BenchmarkFormat(b *testing.B) {
	for _, fixture := range fixtures {
		b.Run("fixture="+fixture.name, func(b *testing.B) {
			data := libraries[0].data(b, fixture)

			measure(b, func() error {
				_, err := soml.Format(data)
				return err
			})
		})
	}
}

// isoDuration is a duration that SOML holds as a duration, and JSON and TOML as an ISO 8601 string, such as "PT55S", as the fixtures write it. It reads only the time part of an ISO 8601 duration, which is all the fixtures use.
type isoDuration time.Duration

func (duration isoDuration) MarshalSOML() (any, error) {
	return time.Duration(duration), nil
}

func (duration *isoDuration) UnmarshalSOML(value any) error {
	result, ok := value.(time.Duration)
	if !ok {
		return fmt.Errorf("expected a duration, got %T", value)
	}

	*duration = isoDuration(result)
	return nil
}

func (duration isoDuration) MarshalText() ([]byte, error) {
	text := strconv.AppendFloat([]byte("PT"), time.Duration(duration).Seconds(), 'f', -1, 64)
	return append(text, 'S'), nil
}

func (duration *isoDuration) UnmarshalText(text []byte) error {
	result, err := time.ParseDuration(strings.ToLower(strings.TrimPrefix(string(text), "PT")))
	if err != nil {
		return err
	}

	*duration = isoDuration(result)
	return nil
}

// service is the shape of the service fixture: the config of a checkout service.
type service struct {
	Name        string          `soml:"name" json:"name" toml:"name"`
	Version     string          `soml:"version" json:"version" toml:"version"`
	Environment string          `soml:"environment" json:"environment" toml:"environment"`
	Region      string          `soml:"region" json:"region" toml:"region"`
	DeployedAt  time.Time       `soml:"deployed-at" json:"deployed-at" toml:"deployed-at"`
	Server      server          `soml:"server" json:"server" toml:"server"`
	Database    database        `soml:"database" json:"database" toml:"database"`
	Cache       cache           `soml:"cache" json:"cache" toml:"cache"`
	Queue       queue           `soml:"queue" json:"queue" toml:"queue"`
	Payments    payments        `soml:"payments" json:"payments" toml:"payments"`
	Limits      limits          `soml:"limits" json:"limits" toml:"limits"`
	Features    map[string]bool `soml:"features" json:"features" toml:"features"`
	Logging     logging         `soml:"logging" json:"logging" toml:"logging"`
	Telemetry   telemetry       `soml:"telemetry" json:"telemetry" toml:"telemetry"`
	Maintenance maintenance     `soml:"maintenance" json:"maintenance" toml:"maintenance"`
	Regions     []region        `soml:"regions" json:"regions" toml:"regions"`
}

type server struct {
	Host          string      `soml:"host" json:"host" toml:"host"`
	Port          int         `soml:"port" json:"port" toml:"port"`
	KeepAlive     isoDuration `soml:"keep-alive" json:"keep-alive" toml:"keep-alive"`
	ReadTimeout   isoDuration `soml:"read-timeout" json:"read-timeout" toml:"read-timeout"`
	WriteTimeout  isoDuration `soml:"write-timeout" json:"write-timeout" toml:"write-timeout"`
	MaxHeaderSize int         `soml:"max-header-size" json:"max-header-size" toml:"max-header-size"`
	TLS           tls         `soml:"tls" json:"tls" toml:"tls"`
}

type tls struct {
	Certificate string   `soml:"certificate" json:"certificate" toml:"certificate"`
	Key         string   `soml:"key" json:"key" toml:"key"`
	MinVersion  string   `soml:"min-version" json:"min-version" toml:"min-version"`
	Ciphers     []string `soml:"ciphers" json:"ciphers" toml:"ciphers"`
}

type database struct {
	Primary  string   `soml:"primary" json:"primary" toml:"primary"`
	Replicas []string `soml:"replicas" json:"replicas" toml:"replicas"`
	Pool     pool     `soml:"pool" json:"pool" toml:"pool"`
	Setup    string   `soml:"setup" json:"setup" toml:"setup"`
}

type pool struct {
	Min            int         `soml:"min" json:"min" toml:"min"`
	Max            int         `soml:"max" json:"max" toml:"max"`
	IdleTimeout    isoDuration `soml:"idle-timeout" json:"idle-timeout" toml:"idle-timeout"`
	AcquireTimeout isoDuration `soml:"acquire-timeout" json:"acquire-timeout" toml:"acquire-timeout"`
}

type cache struct {
	URL        string      `soml:"url" json:"url" toml:"url"`
	TTL        isoDuration `soml:"ttl" json:"ttl" toml:"ttl"`
	MaxEntries int         `soml:"max-entries" json:"max-entries" toml:"max-entries"`
	Eviction   string      `soml:"eviction" json:"eviction" toml:"eviction"`
}

type queue struct {
	Broker    string     `soml:"broker" json:"broker" toml:"broker"`
	Exchanges []exchange `soml:"exchanges" json:"exchanges" toml:"exchanges"`
	Retry     retry      `soml:"retry" json:"retry" toml:"retry"`
}

type exchange struct {
	Name    string `soml:"name" json:"name" toml:"name"`
	Type    string `soml:"type" json:"type" toml:"type"`
	Durable bool   `soml:"durable" json:"durable" toml:"durable"`
}

type retry struct {
	Attempts     int         `soml:"attempts" json:"attempts" toml:"attempts"`
	InitialDelay isoDuration `soml:"initial-delay" json:"initial-delay" toml:"initial-delay"`
	MaxDelay     isoDuration `soml:"max-delay" json:"max-delay" toml:"max-delay"`
	Multiplier   float64     `soml:"multiplier" json:"multiplier" toml:"multiplier"`
}

type payments struct {
	Provider        string   `soml:"provider" json:"provider" toml:"provider"`
	Currency        string   `soml:"currency" json:"currency" toml:"currency"`
	Capture         string   `soml:"capture" json:"capture" toml:"capture"`
	ReviewThreshold int      `soml:"review-threshold" json:"review-threshold" toml:"review-threshold"`
	Fees            fees     `soml:"fees" json:"fees" toml:"fees"`
	Webhooks        webhooks `soml:"webhooks" json:"webhooks" toml:"webhooks"`
}

type fees struct {
	Percentage float64 `soml:"percentage" json:"percentage" toml:"percentage"`
	Fixed      int     `soml:"fixed" json:"fixed" toml:"fixed"`
}

type webhooks struct {
	Path      string      `soml:"path" json:"path" toml:"path"`
	Tolerance isoDuration `soml:"tolerance" json:"tolerance" toml:"tolerance"`
	Events    []string    `soml:"events" json:"events" toml:"events"`
}

type limits struct {
	RequestsPerMinute int     `soml:"requests-per-minute" json:"requests-per-minute" toml:"requests-per-minute"`
	Burst             int     `soml:"burst" json:"burst" toml:"burst"`
	MaxBodySize       int     `soml:"max-body-size" json:"max-body-size" toml:"max-body-size"`
	MaxItemsPerOrder  int     `soml:"max-items-per-order" json:"max-items-per-order" toml:"max-items-per-order"`
	CPU               float64 `soml:"cpu" json:"cpu" toml:"cpu"`
	MemoryMB          int     `soml:"memory-mb" json:"memory-mb" toml:"memory-mb"`
}

type logging struct {
	Level      string   `soml:"level" json:"level" toml:"level"`
	Format     string   `soml:"format" json:"format" toml:"format"`
	SampleRate float64  `soml:"sample-rate" json:"sample-rate" toml:"sample-rate"`
	Redact     []string `soml:"redact" json:"redact" toml:"redact"`
	File       logFile  `soml:"file" json:"file" toml:"file"`
}

type logFile struct {
	Path      string `soml:"path" json:"path" toml:"path"`
	Mode      int    `soml:"mode" json:"mode" toml:"mode"`
	MaxSizeMB int    `soml:"max-size-mb" json:"max-size-mb" toml:"max-size-mb"`
	MaxFiles  int    `soml:"max-files" json:"max-files" toml:"max-files"`
}

type telemetry struct {
	Endpoint   string               `soml:"endpoint" json:"endpoint" toml:"endpoint"`
	Interval   isoDuration          `soml:"interval" json:"interval" toml:"interval"`
	Attributes map[string]string    `soml:"attributes" json:"attributes" toml:"attributes"`
	Histograms map[string][]float64 `soml:"histograms" json:"histograms" toml:"histograms"`
}

type maintenance struct {
	Enabled bool   `soml:"enabled" json:"enabled" toml:"enabled"`
	Message string `soml:"message" json:"message" toml:"message"`
}

type region struct {
	Name    string `soml:"name" json:"name" toml:"name"`
	Weight  int    `soml:"weight" json:"weight" toml:"weight"`
	Primary bool   `soml:"primary" json:"primary" toml:"primary"`
}

// earthquakes is the shape of the earthquakes fixture: a week of earthquakes from the USGS feed, as GeoJSON.
type earthquakes struct {
	Type     string              `soml:"type" json:"type" toml:"type"`
	Metadata earthquakesMetadata `soml:"metadata" json:"metadata" toml:"metadata"`
	BBox     []float64           `soml:"bbox" json:"bbox" toml:"bbox"`
	Features []earthquakeFeature `soml:"features" json:"features" toml:"features"`
}

type earthquakesMetadata struct {
	Generated time.Time `soml:"generated" json:"generated" toml:"generated"`
	URL       string    `soml:"url" json:"url" toml:"url"`
	Title     string    `soml:"title" json:"title" toml:"title"`
	Status    int       `soml:"status" json:"status" toml:"status"`
	API       string    `soml:"api" json:"api" toml:"api"`
	Count     int       `soml:"count" json:"count" toml:"count"`
}

type earthquakeFeature struct {
	Type       string               `soml:"type" json:"type" toml:"type"`
	Properties earthquakeProperties `soml:"properties" json:"properties" toml:"properties"`
	Geometry   earthquakeGeometry   `soml:"geometry" json:"geometry" toml:"geometry"`
	ID         string               `soml:"id" json:"id" toml:"id"`
}

// earthquakeProperties has a nil pointer for each measurement that an earthquake does not have, which is left out when it is written, as in the feed. An explicit zero is kept, so the fixture check sees it.
type earthquakeProperties struct {
	Magnitude     float64   `soml:"mag" json:"mag" toml:"mag"`
	Place         string    `soml:"place" json:"place" toml:"place"`
	Time          time.Time `soml:"time" json:"time" toml:"time"`
	Updated       time.Time `soml:"updated" json:"updated" toml:"updated"`
	URL           string    `soml:"url" json:"url" toml:"url"`
	Detail        string    `soml:"detail" json:"detail" toml:"detail"`
	Felt          *int      `soml:"felt,omitzero" json:"felt,omitzero" toml:"felt,omitempty"`
	CDI           *float64  `soml:"cdi,omitzero" json:"cdi,omitzero" toml:"cdi,omitempty"`
	MMI           *float64  `soml:"mmi,omitzero" json:"mmi,omitzero" toml:"mmi,omitempty"`
	Alert         *string   `soml:"alert,omitzero" json:"alert,omitzero" toml:"alert,omitempty"`
	Status        string    `soml:"status" json:"status" toml:"status"`
	Tsunami       int       `soml:"tsunami" json:"tsunami" toml:"tsunami"`
	Significance  int       `soml:"sig" json:"sig" toml:"sig"`
	Network       string    `soml:"net" json:"net" toml:"net"`
	Code          string    `soml:"code" json:"code" toml:"code"`
	IDs           string    `soml:"ids" json:"ids" toml:"ids"`
	Sources       string    `soml:"sources" json:"sources" toml:"sources"`
	Types         string    `soml:"types" json:"types" toml:"types"`
	Stations      int       `soml:"nst" json:"nst" toml:"nst"`
	MinDistance   *float64  `soml:"dmin,omitzero" json:"dmin,omitzero" toml:"dmin,omitempty"`
	RMS           float64   `soml:"rms" json:"rms" toml:"rms"`
	Gap           float64   `soml:"gap" json:"gap" toml:"gap"`
	MagnitudeType string    `soml:"magType" json:"magType" toml:"magType"`
	EventType     string    `soml:"type" json:"type" toml:"type"`
	Title         string    `soml:"title" json:"title" toml:"title"`
}

type earthquakeGeometry struct {
	Type        string    `soml:"type" json:"type" toml:"type"`
	Coordinates []float64 `soml:"coordinates" json:"coordinates" toml:"coordinates"`
}
