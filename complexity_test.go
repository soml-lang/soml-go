package soml

// Every input shape takes linear time: 8 times the input must not take much more than 8 times as long. A quadratic step would take 64 times as long.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// measure returns the shortest time of a few runs, which is the least noisy.
func measure(run func()) time.Duration {
	shortest := time.Duration(1<<63 - 1)

	for range 5 {
		start := time.Now()
		run()
		shortest = min(shortest, time.Since(start))
	}

	return shortest
}

func TestComplexityIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}

	shapes := []struct {
		name     string
		document func(size int) string
	}{
		{"a long literal string", func(size int) string { return "a: '" + strings.Repeat("x", size*20) + "'" }},
		{"a long escaped string", func(size int) string { return `a: "` + strings.Repeat(`x\n\u{1f600}`, size*2) + `"` }},
		{"a long block string", func(size int) string { return "a: '''\n" + strings.Repeat("\tline\n\n", size) + "\t'''" }},
		{"a long comment", func(size int) string { return "# " + strings.Repeat("x", size*20) + "\na: 1" }},
		{"a long block comment", func(size int) string { return "/* " + strings.Repeat("x*", size*10) + " */ a: 1" }},
		{"many keys", func(size int) string {
			var builder strings.Builder

			for index := range size {
				builder.WriteString("key" + strings.Repeat("x", index%7) + string(rune('a'+index%26)) + strconv.Itoa(index) + ": 1\n")
			}

			return builder.String()
		}},
		{"many array items on one line", func(size int) string {
			return "a: [" + strings.Repeat("'x', 1.5, 2026-01-01T00:00:00Z, 1h30m, ", size) + "]"
		}},
		{"many small objects", func(size int) string { return "a: [" + strings.Repeat("{b: 1, c: [true]}, ", size) + "]" }},
		{"a long number", func(size int) string { return "a: 1" + strings.Repeat("0", size*20) + ".5" }},
		{"a long invalid number", func(size int) string { return "a: 1" + strings.Repeat("x", size*20) }},
		{"a long hexadecimal number with lowercase digits", func(size int) string { return "a: 0x" + strings.Repeat("1", size*20) + "ff" }},
		{"a long unterminated string", func(size int) string { return "a: '" + strings.Repeat("x", size*20) }},
		{"a long key without a colon", func(size int) string { return strings.Repeat("ab ", size*10) }},
		{"a long key with dots", func(size int) string { return strings.Repeat("ab.", size*10) + ": 1" }},
		{"many strings on one line", func(size int) string { return "a: [" + strings.Repeat(`'x', "y", `, size) + "]" }},
		{"many block comments after a value", func(size int) string { return "a: 1" + strings.Repeat(" /* c */", size) + "\nb: 2" }},
		{"many block comments before an item", func(size int) string { return "a: [1, " + strings.Repeat("/* c */ ", size) + "\n2]" }},
	}

	const size = 4000

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			for _, operation := range []struct {
				name string
				run  func([]byte)
			}{
				{"Unmarshal", func(data []byte) {
					var value any
					_ = Unmarshal(data, &value)
				}},
				{"Parse and print", func(data []byte) {
					if document, err := Parse(data); err == nil {
						_ = document.Bytes()
						_ = document.Comments()
					}
				}},
				{"Format", func(data []byte) {
					_, _ = Format(data)
				}},
				{"Set", func(data []byte) {
					if document, err := Parse(data); err == nil {
						_ = document.Set(1, "added")
					}
				}},
				{"Remove", func(data []byte) {
					if document, err := Parse(data); err == nil {
						_, _ = document.Remove("a")
					}
				}},
			} {
				small := []byte(shape.document(size))
				large := []byte(shape.document(size * 8))
				var ratio float64
				smallTime, largeTime := time.Duration(1<<63-1), time.Duration(1<<63-1)

				// Measured up to five times, keeping the shortest time of each size, because other processes can slow down any measurement.
				for range 5 {
					smallTime = min(smallTime, measure(func() { operation.run(small) }))
					largeTime = min(largeTime, measure(func() { operation.run(large) }))
					ratio = float64(largeTime) / float64(max(smallTime, time.Microsecond))

					// Linear is about 8, and quadratic about 64. The limit leaves room for noise and for the cache effects of a larger input.
					if ratio <= 32 {
						break
					}
				}

				if ratio > 32 {
					t.Errorf("%s: 8 times the input took %.1f times as long", operation.name, ratio)
				}
			}
		})
	}
}
