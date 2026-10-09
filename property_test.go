package soml

// Property tests: random values read back from their canonical form, and random legal spellings of a value give its canonical form.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type generator struct {
	random *rand.Rand
}

func newGenerator(seed uint64) *generator {
	return &generator{random: rand.New(rand.NewPCG(seed, seed+1))}
}

func (generator *generator) chance(percent int) bool {
	return generator.random.IntN(100) < percent
}

func (generator *generator) pick(choices ...string) string {
	return choices[generator.random.IntN(len(choices))]
}

// key makes a random key: often bare, sometimes with characters that need quotes.
func (generator *generator) key() string {
	switch generator.random.IntN(6) {
	case 0:
		return generator.pick("a", "b", "port", "404", "-x", "true", "null", "infinity", "2024-01-01", "a_b")
	case 1:
		return generator.text(4)
	}

	return generator.pick("name", "host", "list", "server", "x", "y", "z", "a-b", "7zip")
}

// text makes a random string of up to maximumLength characters, from a set that has every character the quoting rules treat differently.
func (generator *generator) text(maximumLength int) string {
	characters := []string{"a", "b", " ", "'", "\"", "\\", "\n", "\t", "\u0000", "\u001f", "\u007f", "é", "😀", "\u200b", "\u2028", ".", "#", "/*", "{", ":", "\ufeff", "\u0085"}
	var builder strings.Builder

	for range generator.random.IntN(maximumLength + 1) {
		builder.WriteString(characters[generator.random.IntN(len(characters))])
	}

	return builder.String()
}

// value makes a random plain value, as Unmarshal into an `any` gives it, nested at most depth more levels.
func (generator *generator) value(depth int) any {
	kind := generator.random.IntN(10)

	if depth <= 0 && kind >= 8 {
		kind = generator.random.IntN(8)
	}

	switch kind {
	case 0:
		return nil
	case 1:
		return generator.chance(50)
	case 2:
		switch generator.random.IntN(4) {
		case 0:
			return generator.random.Int64()
		case 1:
			return []int64{math.MinInt64, math.MaxInt64, 0, -1}[generator.random.IntN(4)]
		}

		return int64(generator.random.IntN(2000) - 1000)
	case 3:
		switch generator.random.IntN(4) {
		case 0:
			return math.Float64frombits(generator.random.Uint64()&^(0x7FF<<52) | uint64(generator.random.IntN(0x7FE))<<52)
		case 1:
			return []float64{0, 1, -1.5, 0.1, 1e21, 1e-7, 5e-324, math.MaxFloat64, math.Inf(1), math.Inf(-1), 123456789012345680000, 0.000001}[generator.random.IntN(12)]
		}

		return float64(generator.random.IntN(20000)-10000) / 8
	case 4:
		return generator.text(8)
	case 5:
		// From 0001 to 9999, with a random fraction.
		seconds := generator.random.Int64N(253402300800-(-62135596800)) - 62135596800
		return time.Unix(seconds, int64(generator.random.IntN(1_000_000_000))).UTC()
	case 6, 7:
		switch generator.random.IntN(3) {
		case 0:
			return time.Duration(generator.random.Int64())
		case 1:
			return []time.Duration{0, 1, -1, math.MinInt64, math.MaxInt64, time.Hour, 90 * time.Minute, 1500 * time.Millisecond}[generator.random.IntN(8)]
		}

		return time.Duration(generator.random.IntN(100000)) * time.Millisecond
	case 8:
		items := make([]any, generator.random.IntN(4))

		for index := range items {
			items[index] = generator.value(depth - 1)
		}

		return items
	}

	members := map[string]any{}

	for range generator.random.IntN(4) {
		members[generator.key()] = generator.value(depth - 1)
	}

	return members
}

// space writes random whitespace and comments, where the grammar allows them. allowLineBreak is false at the top level of an object without braces, between entries that must stay on separate lines.
func (generator *generator) space(builder *strings.Builder, allowLineBreak bool) {
	for range generator.random.IntN(3) {
		switch generator.random.IntN(5) {
		case 0:
			builder.WriteString(" ")
		case 1:
			builder.WriteString("\t")
		case 2:
			builder.WriteString("/* cé */")
		case 3:
			if allowLineBreak {
				builder.WriteString("\n")
			}
		default:
			if allowLineBreak {
				builder.WriteString("# note\n")
			}
		}
	}
}

// writeString writes a string in a random legal spelling.
func (generator *generator) writeString(builder *strings.Builder, text string, allowBlock bool) {
	canLiteral := !strings.ContainsAny(text, "'\n") && !hasControl(text)

	switch {
	case allowBlock && generator.chance(25) && generator.writeBlockString(builder, text):
		return
	case canLiteral && generator.chance(50):
		builder.WriteString("'" + text + "'")
		return
	}

	builder.WriteByte('"')

	for _, character := range text {
		switch {
		case character == '\\':
			builder.WriteString(`\\`)
		case character == '"':
			builder.WriteString(`\"`)
		case character == '\n' && generator.chance(50):
			builder.WriteString(`\n`)
		case character == '\t' && generator.chance(50):
			builder.WriteString(`\t`)
		case character < 0x20 || character == 0x7F || character == '\n' || generator.chance(20):
			builder.WriteString(`\u{` + strconv.FormatInt(int64(character), 16) + `}`)
		default:
			builder.WriteRune(character)
		}
	}

	builder.WriteByte('"')
}

func hasControl(text string) bool {
	for index := range len(text) {
		if character := text[index]; character < 0x20 && character != '\t' || character == 0x7F {
			return true
		}
	}

	return false
}

// writeBlockString writes a string as a block string when it can be one, and reports whether it did.
func (generator *generator) writeBlockString(builder *strings.Builder, text string) bool {
	lines := strings.Split(text, "\n")

	// The first and last lines must have content, and a line of only spaces and tabs would become empty.
	for index, line := range lines {
		isBlank := strings.Trim(line, " \t") == ""

		if isBlank && (index == 0 || index == len(lines)-1 || line != "") {
			return false
		}
	}

	isLiteral := !hasControl(text) && generator.chance(50)
	quote := `"`

	if isLiteral {
		quote = "'"
	}

	// A delimiter longer than any run of its quote at the start of a line.
	length := 3

	for _, line := range lines {
		content := strings.TrimLeft(line, " \t")
		length = max(length, len(content)-len(strings.TrimLeft(content, quote))+1)
	}

	delimiter := strings.Repeat(quote, length)
	indentation := generator.pick("", "\t", "  ", "\t\t")
	builder.WriteString(delimiter + "\n")

	for _, line := range lines {
		if line != "" {
			builder.WriteString(indentation)
		}

		if isLiteral {
			builder.WriteString(line)
		} else {
			for _, character := range line {
				switch {
				case character == '\\':
					builder.WriteString(`\\`)
				case character < 0x20 && character != '\t' || character == 0x7F:
					builder.WriteString(`\u{` + strconv.FormatInt(int64(character), 16) + `}`)
				default:
					builder.WriteRune(character)
				}
			}
		}

		builder.WriteString("\n")
	}

	builder.WriteString(indentation + delimiter)
	return true
}

// writeKey writes a key: bare when it can be, and otherwise quoted.
func (generator *generator) writeKey(builder *strings.Builder, key string) {
	if isBareKey(key) && generator.chance(80) {
		builder.WriteString(key)
		return
	}

	generator.writeString(builder, key, false)
}

// writeInteger writes an int in a random legal spelling.
func (generator *generator) writeInteger(builder *strings.Builder, value int64) {
	if value >= 0 && generator.chance(40) {
		radix := []int{2, 8, 16}[generator.random.IntN(3)]
		prefix := map[int]string{2: "0b", 8: "0o", 16: "0x"}[radix]
		digits := strings.Repeat("0", generator.random.IntN(3)) + strings.ToUpper(strconv.FormatInt(value, radix))
		builder.WriteString(prefix + generator.underscores(digits))
		return
	}

	text := strconv.FormatInt(value, 10)
	sign := ""

	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}

	builder.WriteString(sign + generator.underscores(text))
}

// underscores puts random single underscores between digits.
func (generator *generator) underscores(digits string) string {
	var builder strings.Builder

	for index := range len(digits) {
		if index > 0 && generator.chance(15) {
			builder.WriteByte('_')
		}

		builder.WriteByte(digits[index])
	}

	return builder.String()
}

// writeFloat writes a float in a random legal spelling: its shortest digits with the decimal point moved into an exponent, or extra zeros.
func (generator *generator) writeFloat(builder *strings.Builder, value float64) {
	if math.IsInf(value, 0) || value == 0 || generator.chance(30) {
		text, _ := appendFloat(nil, value, 64)
		builder.Write(text)
		return
	}

	if value < 0 {
		builder.WriteByte('-')
		value = -value
	}

	text := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentText, _ := strings.Cut(text, "e")
	exponent, _ := strconv.Atoi(exponentText)
	digits := strings.Replace(mantissa, ".", "", 1)

	// The value is digits × 10^(exponent - len(digits) + 1). Write it as digits, an optional fraction of zeros, and an exponent.
	scale := exponent - len(digits) + 1
	fraction := ""

	if generator.chance(50) {
		fraction = "." + strings.Repeat("0", 1+generator.random.IntN(3))
	}

	builder.WriteString(generator.underscores(digits) + fraction)

	if scale != 0 || fraction == "" {
		builder.WriteString("e" + strconv.Itoa(scale))
	}
}

// writeInstant writes an instant with a random offset, and a fraction with trailing zeros.
func (generator *generator) writeInstant(builder *strings.Builder, value time.Time) {
	offset := (generator.random.IntN(48) - 24) * 30 * 60
	local := value.In(time.FixedZone("", offset))

	// The offset must keep the date in the years 0001 to 9999.
	if local.Year() < 1 || local.Year() > 9999 {
		local = value
	}

	text := local.Format("2006-01-02T15:04:05")

	if nanoseconds := local.Nanosecond(); nanoseconds != 0 || generator.chance(20) {
		fraction := fmt.Sprintf("%09d", nanoseconds)
		trimmed := strings.TrimRight(fraction, "0")
		fraction = fraction[:max(len(trimmed), 1)+generator.random.IntN(9-max(len(trimmed), 1)+1)]
		text += "." + fraction
	}

	_, offset = local.Zone()

	if offset == 0 && generator.chance(50) {
		builder.WriteString(text + "Z")
		return
	}

	sign := "+"

	if offset < 0 {
		sign, offset = "-", -offset
	}

	builder.WriteString(fmt.Sprintf("%s%s%02d:%02d", text, sign, offset/3600, offset/60%60))
}

// writeDuration writes a duration in other units than canonical form uses.
func (generator *generator) writeDuration(builder *strings.Builder, value time.Duration) {
	if value == 0 {
		builder.WriteString(generator.pick("0s", "0ns", "0h", "0m0s", "0.0s"))
		return
	}

	magnitude := uint64(value)

	if value < 0 {
		builder.WriteByte('-')
		magnitude = -magnitude
	}

	// The magnitude of the most negative duration has no positive duration, so it is written in nanoseconds.
	switch choice := generator.random.IntN(3); {
	case choice == 0 || magnitude > math.MaxInt64:
		builder.WriteString(generator.underscores(strconv.FormatUint(magnitude, 10)) + "ns")
	case choice == 1:
		// Milliseconds with a fraction of nanoseconds.
		whole, rest := magnitude/1_000_000, magnitude%1_000_000
		builder.WriteString(strconv.FormatUint(whole, 10))

		if rest != 0 {
			builder.WriteString("." + strings.TrimRight(fmt.Sprintf("%06d", rest), "0"))
		}

		builder.WriteString("ms")
	default:
		builder.WriteString(string(appendDuration(nil, time.Duration(magnitude))))
	}
}

// write writes a value in a random legal spelling.
func (generator *generator) write(builder *strings.Builder, value any) {
	switch value := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		builder.WriteString(strconv.FormatBool(value))
	case int64:
		generator.writeInteger(builder, value)
	case float64:
		generator.writeFloat(builder, value)
	case string:
		generator.writeString(builder, value, true)
	case time.Time:
		generator.writeInstant(builder, value)
	case time.Duration:
		generator.writeDuration(builder, value)
	case []any:
		builder.WriteByte('[')
		generator.space(builder, true)

		for index, item := range value {
			generator.write(builder, item)

			// A comma goes on the line of the item before it.
			if index+1 < len(value) || generator.chance(50) {
				generator.space(builder, false)
				builder.WriteByte(',')
			}

			generator.space(builder, true)
		}

		builder.WriteByte(']')
	case map[string]any:
		builder.WriteByte('{')
		generator.space(builder, true)
		entries := generator.entries(value)

		for index, entry := range entries {
			builder.WriteString(entry)

			// A comma goes on the line of the item before it.
			if index+1 < len(entries) || generator.chance(50) {
				generator.space(builder, false)
				builder.WriteByte(',')
			}

			generator.space(builder, true)
		}

		builder.WriteByte('}')
	}
}

// entries writes the members of an object in a random order, each as `key: value`.
func (generator *generator) entries(object map[string]any) []string {
	keys := make([]string, 0, len(object))

	for key := range object {
		keys = append(keys, key)
	}

	slices.Sort(keys)
	generator.random.Shuffle(len(keys), func(left, right int) {
		keys[left], keys[right] = keys[right], keys[left]
	})

	var entries []string

	for _, key := range keys {
		var entry strings.Builder
		generator.writeKey(&entry, key)
		entry.WriteString(":")
		generator.space(&entry, true)
		generator.write(&entry, object[key])
		entries = append(entries, entry.String())
	}

	return entries
}

// document writes a top-level value in a random legal spelling, with braces or without them.
func (generator *generator) document(value any) string {
	var builder strings.Builder
	generator.space(&builder, true)

	if object, ok := value.(map[string]any); ok && len(object) > 0 && generator.chance(60) {
		for _, entry := range generator.entries(object) {
			builder.WriteString(entry)
			generator.space(&builder, false)
			builder.WriteString("\n")
			generator.space(&builder, true)
		}

		return builder.String()
	}

	generator.write(&builder, value)
	generator.space(&builder, true)
	return builder.String()
}

func TestPropertyRandomValuesRoundTripAndRespellingsGiveTheSameCanonicalForm(t *testing.T) {
	generator := newGenerator(3)

	for range 20000 {
		var value any = generator.value(4)

		switch value.(type) {
		case map[string]any, []any:
		default:
			// A document is a collection.
			value = []any{value}
		}

		if generator.chance(50) {
			value = map[string]any{"root": value, "other": generator.value(2)}
		}

		canonical, err := Marshal(value, Canonical())

		if err != nil {
			// A key from the random text can hold nothing that Marshal refuses, so every value is representable.
			t.Fatalf("Marshal(%#v): %v", value, err)
		}

		var decoded any

		if err := Unmarshal(canonical, &decoded); err != nil {
			t.Fatalf("Unmarshal of %q: %v", canonical, err)
		}

		if rewritten, _ := Marshal(decoded, Canonical()); string(rewritten) != string(canonical) {
			t.Fatalf("canonical form is not a fixed point:\n%q\n%q", canonical, rewritten)
		}

		text := generator.document(value)

		if !utf8.ValidString(text) {
			t.Fatalf("the generator wrote invalid UTF-8: %q", text)
		}

		var respelled any

		if err := Unmarshal([]byte(text), &respelled); err != nil {
			t.Fatalf("a respelling of\n%s\nis not valid:\n%s\n%v", canonical, text, err)
		}

		if respelledCanonical, _ := Marshal(respelled, Canonical()); string(respelledCanonical) != string(canonical) {
			t.Fatalf("a respelling gives another value:\n%s\nrespelled:\n%s\ncanonical form of the respelling:\n%s", canonical, text, respelledCanonical)
		}

		// The tree prints the respelling back unchanged.
		if printed := mustParse(t, text).String(); printed != text {
			t.Fatalf("the tree printed %q, want %q", printed, text)
		}
	}
}
