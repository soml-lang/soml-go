package soml

// Tests for reading and writing one scalar: numbers, strings, block strings, instants, and durations.

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf16"
)

// decodeMember decodes the document `value: <text>` into an `any` and returns the value of its one member.
func decodeMember(text string) (any, error) {
	var document map[string]any

	if err := Unmarshal([]byte("value: "+text+"\n"), &document); err != nil {
		return nil, err
	}

	return document["value"], nil
}

// encodeMember returns the canonical text of a value when it is the value of a member.
func encodeMember(t *testing.T, value any) string {
	t.Helper()
	data, err := Marshal(map[string]any{"value": value})

	if err != nil {
		t.Fatalf("Marshal(%#v): %v", value, err)
	}

	text, ok := strings.CutPrefix(string(data), "value: ")

	if !ok || !strings.HasSuffix(text, "\n") {
		t.Fatalf("Marshal(%#v) = %q, which is not one member", value, data)
	}

	return strings.TrimSuffix(text, "\n")
}

// checkError checks that err is an *Error at the line and the column, with the message.
func checkError(t *testing.T, err error, line, column int, message string) {
	t.Helper()

	if err == nil {
		t.Fatalf("no error, want %d:%d %q", line, column, message)
	}

	somlError, ok := errors.AsType[*Error](err)

	if !ok {
		t.Fatalf("the error is a %T, not an *Error: %v", err, err)
	}

	if somlError.Line != line || somlError.Column != column || somlError.Message != message {
		t.Errorf("error:\n got %d:%d %q\nwant %d:%d %q", somlError.Line, somlError.Column, somlError.Message, line, column, message)
	}
}

// sameFloat reports whether two floats have the same bits, so that 0.0 and -0.0 differ.
func sameFloat(left, right float64) bool {
	return math.Float64bits(left) == math.Float64bits(right)
}

type invalidScalar struct {
	input   string
	message string
}

// checkInvalidScalars checks that each input, as the value of a member, fails at the start of the value with its message.
func checkInvalidScalars(t *testing.T, cases []invalidScalar) {
	t.Helper()

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err == nil {
				t.Fatalf("accepted as %#v, want %q", value, testCase.message)
			}

			checkError(t, err, 1, len("value: ")+1, testCase.message)
		})
	}
}

func TestIntegers(t *testing.T) {
	cases := []struct {
		input    string
		expected int64
	}{
		{"0", 0},
		{"7", 7},
		{"42", 42},
		{"-42", -42},
		{"1_000_000", 1_000_000},
		{"-1_2_3", -123},
		{"0xFF", 0xFF},
		{"0x00FF", 0xFF},
		{"0xDEAD_BEEF", 0xDEADBEEF},
		{"0x0", 0},
		{"0o644", 0o644},
		{"0o7_7", 0o77},
		{"0b1010", 0b1010},
		{"0b1_0", 0b10},
		{"0b0000", 0},
		{"9223372036854775807", math.MaxInt64},
		{"-9223372036854775808", math.MinInt64},
		{"9_223_372_036_854_775_807", math.MaxInt64},
		{"0x7FFFFFFFFFFFFFFF", math.MaxInt64},
		{"0x00000000000000007FFFFFFFFFFFFFFF", math.MaxInt64},
		{"0o777777777777777777777", math.MaxInt64},
		{"0b" + strings.Repeat("1", 63), math.MaxInt64},
		{"123456789012345678", 123456789012345678},
		{"1234567890123456789", 1234567890123456789},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.expected) {
				t.Fatalf("got %#v, want int64(%d)", value, testCase.expected)
			}
		})
	}
}

// Every suggested uppercase hexadecimal integer is valid, and holds the value the lowercase digits mean.
func TestHexadecimalCaseSuggestionIsValid(t *testing.T) {
	for _, digits := range []string{"a", "f", "0a", "a0", "ab_cd", "a_b_c", "7fffffffffffffff", "dead_beef", "_a", "a_", "a__b", "a_b_", "_", "a_B", "ffffffffffffffff", "8000000000000000a", "0000_7fff_ffff_ffff_ffff", "1" + strings.Repeat("0", 100) + "a"} {
		_, err := decodeMember("0x" + digits)
		somlError, ok := errors.AsType[*Error](err)

		if !ok {
			t.Fatalf("0x%s: got %v", digits, err)
		}

		suggestion, isSuggested := strings.CutPrefix(somlError.Message, "hexadecimal digits are uppercase: ")

		if !isSuggested {
			continue
		}

		value, err := decodeMember(suggestion)
		expected, _ := strconv.ParseInt(strings.ReplaceAll(digits, "_", ""), 16, 64)

		if err != nil || value != expected {
			t.Errorf("0x%s suggests %s, which gives %v, %v", digits, suggestion, value, err)
		}
	}
}

// The instant that a date could be is only shown when the date exists, so that the example is valid.
func TestDateIsOnlyShownAsAnInstantWhenItExists(t *testing.T) {
	asString := func(date string) string {
		return date + " is a date, not an instant. Write a date as a string, as in '" + date + "'"
	}

	asInstant := func(date string) string {
		return asString(date) + ". An instant needs a time and an offset, as in " + date + "T00:00:00Z"
	}

	cases := []invalidScalar{
		{"2026-09-19", asInstant("2026-09-19")},
		{"2024-02-29", asInstant("2024-02-29")},
		{"2000-02-29", asInstant("2000-02-29")},
		{"0001-01-01", asInstant("0001-01-01")},
		{"9999-12-31", asInstant("9999-12-31")},
		{"2026-02-30", asString("2026-02-30")},
		{"2025-02-29", asString("2025-02-29")},
		{"1900-02-29", asString("1900-02-29")},
		{"2026-04-31", asString("2026-04-31")},
		{"2026-13-01", asString("2026-13-01")},
		{"2026-00-10", asString("2026-00-10")},
		{"2026-01-00", asString("2026-01-00")},
		{"2026-01-32", asString("2026-01-32")},
		{"0000-01-01", asString("0000-01-01")},
	}

	checkInvalidScalars(t, cases)

	for _, testCase := range cases {
		if instant, ok := strings.CutPrefix(testCase.message, asString(testCase.input)+". An instant needs a time and an offset, as in "); ok {
			if _, err := decodeMember(instant); err != nil {
				t.Errorf("%s suggests %s, which is invalid: %v", testCase.input, instant, err)
			}
		}
	}
}

func TestIntegersInvalid(t *testing.T) {
	outOfRange := func(text string) string {
		return "the integer " + text + " is outside the 64-bit range (-9223372036854775808 to 9223372036854775807)"
	}

	checkInvalidScalars(t, []invalidScalar{
		{"9223372036854775808", outOfRange("9223372036854775808")},
		{"-9223372036854775809", outOfRange("-9223372036854775809")},
		{"18446744073709551616", outOfRange("18446744073709551616")},
		{"0x8000000000000000", outOfRange("0x8000000000000000")},
		{"0xFFFFFFFFFFFFFFFF", outOfRange("0xFFFFFFFFFFFFFFFF")},
		{"0x1_0000_0000_0000_0000", outOfRange("0x1_0000_0000_0000_0000")},
		{"0o1000000000000000000000", outOfRange("0o1000000000000000000000")},
		{"0b1" + strings.Repeat("0", 63), outOfRange("0b1" + strings.Repeat("0", 37) + "…")},
		{"1" + strings.Repeat("0", 60), outOfRange("1" + strings.Repeat("0", 39) + "…")},
		{"-0", "“-0” is not allowed, because zero has one spelling: 0"},
		{"00", "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o0, and an identifier, such as a ZIP code, as a string: '00'"},
		{"007", "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o7, and an identifier, such as a ZIP code, as a string: '007'"},
		// An octal number that long is outside the 64-bit range, so it is not suggested.
		{"0" + strings.Repeat("7", 60), "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '0" + strings.Repeat("7", 39) + "…'"},
		{"-01", "leading zeros are not allowed in a decimal number"},
		{"0x", "expected hexadecimal digits after “0x”"},
		{"0o", "expected octal digits after “0o”"},
		{"0b", "expected binary digits after “0b”"},
		{"0b2", "invalid binary digit “2”"},
		{"0o8", "invalid octal digit “8”"},
		{"0xG", "invalid hexadecimal digit “G”"},
		// A lowercase hexadecimal digit is a digit in the wrong case, so the one named is a character that is no digit in either case.
		{"0xffg", "invalid hexadecimal digit “g”"},
		{"0xFfz", "invalid hexadecimal digit “z”"},
		{"0xf_g", "invalid hexadecimal digit “g”"},
		{"0xFG", "invalid hexadecimal digit “G”"},
		{"0o78", "invalid octal digit “8”"},
		{"0o7a", "invalid octal digit “a”"},
		{"0b102", "invalid binary digit “2”"},
		{"0x_FF", "an underscore in a number must be between two digits"},
		{"0xFF_", "an underscore in a number must be between two digits"},
		{"1_", "an underscore in a number must be between two digits"},
		{"1__0", "an underscore in a number must be between two digits"},
		{"0xff", "hexadecimal digits are uppercase: 0xFF"},
		{"0xff_ff", "hexadecimal digits are uppercase: 0xFF_FF"},
		{"0xFf", "hexadecimal digits are uppercase: 0xFF"},
		{"0x00ff", "hexadecimal digits are uppercase: 0x00FF"},
		{"0x7fffffffffffffff", "hexadecimal digits are uppercase: 0x7FFFFFFFFFFFFFFF"},
		{"0x0000_7fff_ffff_ffff_ffff", "hexadecimal digits are uppercase: 0x0000_7FFF_FFFF_FFFF_FFFF"},
		// The uppercase spelling would be outside the 64-bit range, so there is no example.
		{"0x8000000000000000a", "hexadecimal digits are uppercase"},
		{"0xffffffffffffffff", "hexadecimal digits are uppercase"},
		{"0x0000_8000_0000_0000_000a", "hexadecimal digits are uppercase"},
		// The uppercase spelling would still be invalid, so the underscore is reported.
		{"0x_ff", "an underscore in a number must be between two digits"},
		{"0xff_", "an underscore in a number must be between two digits"},
		{"0xf__f", "an underscore in a number must be between two digits"},
		{"0x_", "an underscore in a number must be between two digits"},
		// The lowercase digits come after the length that is diagnosed, so the whole number decides the reason.
		{"0x" + strings.Repeat("1", 1100) + "ff", "hexadecimal digits are uppercase"},
		{"0x" + strings.Repeat("1", 1100) + "f_f", "hexadecimal digits are uppercase"},
		{"0x" + strings.Repeat("1", 1100) + "f__f", "an underscore in a number must be between two digits"},
		{"0XFF", "a number prefix is lowercase: 0x, 0o, or 0b"},
		{"0B1", "a number prefix is lowercase: 0x, 0o, or 0b"},
		{"-0xFF", "a hexadecimal integer cannot have a sign, because it states a bit pattern rather than a quantity"},
		{"-0o644", "an octal integer cannot have a sign, because it states a bit pattern rather than a quantity"},
		{"-0b1", "a binary integer cannot have a sign, because it states a bit pattern rather than a quantity"},
		{"+1", "a “+” sign is not allowed. A number without a sign is positive"},
		{"1x", "invalid number “1x”. A string value must be quoted, as in '1x'"},
		{"-", "expected a digit or “infinity” after “-”"},
	})
}

func TestFloats(t *testing.T) {
	cases := []struct {
		input    string
		expected float64
	}{
		{"1.0", 1},
		{"-1.5", -1.5},
		{"0.5", 0.5},
		{"0.0", 0},
		{"-0.0", 0},
		{"0.000", 0},
		{"0e0", 0},
		{"0e5", 0},
		{"-0e-5", 0},
		{"0.0e-99999", 0},
		{"1e5", 100000},
		{"1e0", 1},
		{"1e-10", 1e-10},
		{"-1e-10", -1e-10},
		{"2.5e3", 2500},
		{"1.5e300", 1.5e300},
		{"1_000.000_1", 1000.0001},
		{"1e1_0", 1e10},
		{"3.141592653589793", math.Pi},
		{"0.1", 0.1},
		{"infinity", math.Inf(1)},
		{"-infinity", math.Inf(-1)},
		{"1.7976931348623157e308", math.MaxFloat64},
		{"1.7976931348623158e308", math.MaxFloat64},
		{"2.2250738585072014e-308", 2.2250738585072014e-308},
		{"2.225073858507201e-308", 2.225073858507201e-308},
		{"5e-324", math.SmallestNonzeroFloat64},
		{"4.9406564584124654e-324", math.SmallestNonzeroFloat64},
		{"3e-324", math.SmallestNonzeroFloat64},
		// Just above half of the smallest subnormal, so it rounds up to it.
		{"2.4703282292062328e-324", math.SmallestNonzeroFloat64},
		{"-5e-324", -math.SmallestNonzeroFloat64},
		{"9007199254740993.0", 9007199254740992},
		{"9007199254740995.0", 9007199254740996},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			number, ok := value.(float64)

			if !ok {
				t.Fatalf("got %#v, want a float64", value)
			}

			if !sameFloat(number, testCase.expected) {
				t.Fatalf("got %v (%016x), want %v (%016x)", number, math.Float64bits(number), testCase.expected, math.Float64bits(testCase.expected))
			}
		})
	}
}

func TestFloatsInvalid(t *testing.T) {
	checkInvalidScalars(t, []invalidScalar{
		{"1e-400", "1e-400 is too small to be told apart from zero. Write 0.0 if you mean zero"},
		{"-1e-400", "-1e-400 is too small to be told apart from zero. Write 0.0 if you mean zero"},
		{"2.4703282292062327e-324", "2.4703282292062327e-324 is too small to be told apart from zero. Write 0.0 if you mean zero"},
		{"1e400", "1e400 is too large to be a finite float. Use infinity if you mean it"},
		{"-1e400", "-1e400 is too large to be a finite float. Use -infinity if you mean it"},
		{"1e999", "1e999 is too large to be a finite float. Use infinity if you mean it"},
		{"-1e999", "-1e999 is too large to be a finite float. Use -infinity if you mean it"},
		{"1.7976931348623159e308", "1.7976931348623159e308 is too large to be a finite float. Use infinity if you mean it"},
		{"-1.7976931348623159e308", "-1.7976931348623159e308 is too large to be a finite float. Use -infinity if you mean it"},
		{"-1_000e306", "-1_000e306 is too large to be a finite float. Use -infinity if you mean it"},
		{".5", "a number cannot begin with “.”; write a digit before it, as in 0.5"},
		{"-.5", "a number cannot begin with “.”; write a digit before it, as in -0.5"},
		{"5.", "a decimal point must be followed by a digit"},
		{"5.e3", "a decimal point must be followed by a digit"},
		{"1E10", "an exponent marker is a lowercase “e”"},
		{"1.5E10", "an exponent marker is a lowercase “e”"},
		{"1E-5", "an exponent marker is a lowercase “e”"},
		{"1E", "an exponent marker is a lowercase “e”"},
		// An “E” with more than digits after it is part of a word, so lowercasing it would not help.
		{"5EUR", "invalid number “5EUR”. A string value must be quoted, as in '5EUR'"},
		{"10EB", "invalid number “10EB”. A string value must be quoted, as in '10EB'"},
		{"1.5Ex", "invalid number “1.5Ex”. A string value must be quoted, as in '1.5Ex'"},
		{"1e05", "leading zeros are not allowed in an exponent"},
		{"1e-05", "leading zeros are not allowed in an exponent"},
		{"1e-0", "“e-0” is not allowed, because an exponent of zero has one spelling: e0"},
		{"1e+5", "a “+” sign is not allowed in a number, including in an exponent"},
		{"00.5", "leading zeros are not allowed in a decimal number"},
		{"01e2", "leading zeros are not allowed in a decimal number"},
		{"1e", "expected digits after the exponent marker “e”"},
		{"1e-", "expected digits after the exponent marker “e”"},
		{"1.5e", "expected digits after the exponent marker “e”"},
		// Only a number ends with its exponent marker.
		{"1byte", "invalid number “1byte”. A string value must be quoted, as in '1byte'"},
		{"3-phase", "invalid number “3-phase”. A string value must be quoted, as in '3-phase'"},
		{"2e-e", "invalid number “2e-e”. A string value must be quoted, as in '2e-e'"},
		{"-verbose", "expected a digit or “infinity” after “-”"},
		{"1._5", "an underscore in a number must be between two digits"},
		{"1_.5", "an underscore in a number must be between two digits"},
		// An underscore next to a letter other than the exponent marker is part of a word.
		{"4k_video", "invalid number “4k_video”. A string value must be quoted, as in '4k_video'"},
		{"2x_speed", "invalid number “2x_speed”. A string value must be quoted, as in '2x_speed'"},
		{"1.2.3", "invalid number “1.2.3”. A value with several dots, such as a version number, must be quoted"},
		{"nan", "NaN is not representable. Use null for a missing value"},
		{"NaN", "NaN is not representable. Use null for a missing value"},
		{"-nan", "NaN is not representable. Use null for a missing value"},
		{"inf", "“inf” is not a value. Infinity is written infinity, in lowercase"},
		{"Infinity", "“Infinity” is not a value. Infinity is written infinity, in lowercase"},
		{"-inf", "“-inf” is not a value. Negative infinity is written -infinity"},
		{"-Infinity", "“-Infinity” is not a value. Negative infinity is written -infinity"},
		{"-INF", "“-INF” is not a value. Negative infinity is written -infinity"},
		// Only the whole word after “-” is NaN or infinity.
		{"-nano", "expected a digit or “infinity” after “-”"},
		{"-info", "expected a digit or “infinity” after “-”"},
		{"-infinite", "expected a digit or “infinity” after “-”"},
		{"-infinityx", "expected a digit or “infinity” after “-”"},
	})
}

func TestFloatLongLiterals(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected float64
	}{
		// More than 800 digits before the point, which strconv.ParseFloat alone reads as 9.007199254740994e+13.
		{"802 digits before the point", "9007199254740993" + strings.Repeat("0", 785) + "1e-786", 9.007199254740994e+15},
		{"802 digits before the point, exactly halfway", "9007199254740993" + strings.Repeat("0", 786) + "e-786", 9007199254740992},
		{"801 digits before the point", "9007199254740993" + strings.Repeat("0", 785) + ".0e-785", 9007199254740992},
		{"1000 digits before the point", "1" + strings.Repeat("0", 999) + ".0e-999", 1},
		{"2000 digits before the point", "12345" + strings.Repeat("0", 1995) + "e-1995", 12345},
		{"underscores", "1_" + strings.Repeat("000_", 300) + "000.0e-903", 1},
		// An exponent past 10,000, which strconv.ParseFloat clamps.
		{"exponent of 100000", "0." + strings.Repeat("0", 99999) + "9e100000", 9},
		{"100000 zeros after the point", "0." + strings.Repeat("0", 100000) + "9e100000", 0.9},
		{"exponent of -100000", "9" + strings.Repeat("0", 100000) + ".0e-100000", 9},
		{"tiny fraction", "0." + strings.Repeat("0", 400) + "1e401", 1},
		{"halfway after 800 digits", "9007199254740993." + strings.Repeat("0", 800) + "1", 9007199254740994},
		{"halfway with 800 zeros", "9007199254740993." + strings.Repeat("0", 800), 9007199254740992},
		{"subnormal", "0." + strings.Repeat("0", 323) + "5", math.SmallestNonzeroFloat64},
		{"long zero", "0." + strings.Repeat("0", 1000), 0},
		{"long zero with an exponent", strings.Repeat("0", 1) + "." + strings.Repeat("0_0", 500) + "e99999999999999999999", 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			if number, ok := value.(float64); !ok || !sameFloat(number, testCase.expected) {
				t.Fatalf("got %#v, want %v", value, testCase.expected)
			}
		})
	}

	invalid := []struct {
		name    string
		input   string
		message string
	}{
		{"too large", "1" + strings.Repeat("0", 309) + ".0", "1" + strings.Repeat("0", 39) + "… is too large to be a finite float. Use infinity if you mean it"},
		{"huge exponent", "1.0e" + strings.Repeat("9", 40), "1.0e" + strings.Repeat("9", 36) + "… is too large to be a finite float. Use infinity if you mean it"},
		// A literal this far outside the range rounds to an infinity without a sign, so the hint takes the sign from the text.
		{"too large and negative", "-1" + strings.Repeat("0", 309) + ".0", "-1" + strings.Repeat("0", 38) + "… is too large to be a finite float. Use -infinity if you mean it"},
		{"huge exponent and negative", "-1.0e" + strings.Repeat("9", 40), "-1.0e" + strings.Repeat("9", 35) + "… is too large to be a finite float. Use -infinity if you mean it"},
		{"huge exponent with underscores and negative", "-1.0e1_" + strings.Repeat("0", 40), "-1.0e1_" + strings.Repeat("0", 33) + "… is too large to be a finite float. Use -infinity if you mean it"},
		{"tiny", "0." + strings.Repeat("0", 400) + "1", "0." + strings.Repeat("0", 38) + "… is too small to be told apart from zero. Write 0.0 if you mean zero"},
		{"huge negative exponent", "1.0e-" + strings.Repeat("9", 40), "1.0e-" + strings.Repeat("9", 35) + "… is too small to be told apart from zero. Write 0.0 if you mean zero"},
		{"exponent of 100000 with too few zeros", "0." + strings.Repeat("0", 99000) + "9e100000", "0." + strings.Repeat("0", 38) + "… is too large to be a finite float. Use infinity if you mean it"},
	}

	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := decodeMember(testCase.input)
			checkError(t, err, 1, 8, testCase.message)
		})
	}
}

// Random literals of 801 to 2,300 digits, compared with math/big, which rounds a rational exactly.
func TestFloatLongLiteralsRandom(t *testing.T) {
	random := rand.New(rand.NewPCG(800, 2300))

	for range 300 {
		digitCount := 801 + random.IntN(1500)
		digits := make([]byte, digitCount)

		for index := range digits {
			digits[index] = byte('0' + random.IntN(10))
		}

		digits[0] = byte('1' + random.IntN(9))
		integerDigits := random.IntN(digitCount + 1)
		target := random.IntN(600) - 300
		exponent := target - integerDigits
		var literal string

		switch {
		case integerDigits == 0:
			literal = "0." + string(digits)
		case integerDigits == digitCount:
			literal = string(digits) + ".0"
		default:
			literal = string(digits[:integerDigits]) + "." + string(digits[integerDigits:])
		}

		if exponent != 0 {
			literal += "e" + strconv.Itoa(exponent)
		}

		rational, ok := new(big.Rat).SetString(literal)

		if !ok {
			t.Fatalf("math/big cannot read %.60s…", literal)
		}

		expected, _ := rational.Float64()
		value, err := decodeMember(literal)

		if err != nil {
			t.Fatalf("%.60s…: %v", literal, err)
		}

		if number, ok := value.(float64); !ok || !sameFloat(number, expected) {
			t.Fatalf("%.60s… (%d digits, %d before the point): got %#v, want %v", literal, digitCount, integerDigits, value, expected)
		}
	}
}

// Literals exactly at, just above, and just below the midpoint between two floats, with more than 800 digits before the point.
func TestFloatLongLiteralsHalfway(t *testing.T) {
	random := rand.New(rand.NewPCG(53, 1))

	for range 300 {
		value := math.Ldexp(1+random.Float64(), random.IntN(1800)-900)
		next := math.Nextafter(value, math.Inf(1))
		midpoint := new(big.Rat).Add(new(big.Rat).SetFloat64(value), new(big.Rat).SetFloat64(next))
		midpoint.Quo(midpoint, big.NewRat(2, 1))

		// The midpoint is numerator / 2^power, which is numerator × 5^power / 10^power, a finite decimal.
		power := midpoint.Denom().BitLen() - 1
		digits := new(big.Int).Mul(midpoint.Num(), new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(power)), nil))
		const padding = 801
		halfway := digits.String() + strings.Repeat("0", padding)

		// Ties go to the even one.
		even := value

		if math.Float64bits(value)&1 == 1 {
			even = next
		}

		below := new(big.Int).Sub(new(big.Int).Mul(digits, new(big.Int).Exp(big.NewInt(10), big.NewInt(padding), nil)), big.NewInt(1)).String()

		cases := []struct {
			name     string
			literal  string
			expected float64
		}{
			{"halfway", halfway + "e-" + strconv.Itoa(power+padding), even},
			{"above", halfway + "1e-" + strconv.Itoa(power+padding+1), next},
			{"below", below + "e-" + strconv.Itoa(power+padding), value},
		}

		for _, testCase := range cases {
			decoded, err := decodeMember(testCase.literal)

			if err != nil {
				t.Fatalf("%s %v: %v", testCase.name, value, err)
			}

			if number, ok := decoded.(float64); !ok || !sameFloat(number, testCase.expected) {
				t.Fatalf("%s between %v and %v (%d digits): got %#v, want %v", testCase.name, value, next, len(testCase.literal), decoded, testCase.expected)
			}
		}
	}
}

// Every branch of the layout in canonical form rule 11.
func TestFloatCanonical(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		// 11.1
		{"zero", 0.0, "0.0"},
		{"negative zero", math.Copysign(0, -1), "0.0"},
		{"infinity", math.Inf(1), "infinity"},
		{"negative infinity", math.Inf(-1), "-infinity"},
		{"negative", -2.5, "-2.5"},
		// 11.3: k ≤ n ≤ 21
		{"one", 1.0, "1.0"},
		{"hundred", 100.0, "100.0"},
		{"negative hundred", -100.0, "-100.0"},
		{"1e20", 1e20, "100000000000000000000.0"},
		{"21 digits", 123456789012345680000.0, "123456789012345680000.0"},
		{"2^53", 9007199254740992.0, "9007199254740992.0"},
		// 11.4: 0 < n < k
		{"one and a half", 1.5, "1.5"},
		{"pi", 3.14, "3.14"},
		{"123.456", 123.456, "123.456"},
		{"0.1 + 0.2 + 1", 1.3000000000000003, "1.3000000000000003"},
		{"tie of 2^50", 1125899906842624.25, "1125899906842624.2"},
		{"tie from the Rust plan", 1658206780088562.2, "1658206780088562.2"},
		{"17 digits", 12345678.901234567, "12345678.901234567"},
		// 11.5: −6 < n ≤ 0
		{"half", 0.5, "0.5"},
		{"tenth", 0.1, "0.1"},
		{"0.1 + 0.2", 0.30000000000000004, "0.30000000000000004"},
		{"1e-6", 0.000001, "0.000001"},
		{"0.00000123", 0.00000123, "0.00000123"},
		{"negative 1e-6", -0.000001, "-0.000001"},
		// 11.6: everything else
		{"1e21", 1e21, "1e21"},
		{"1.5e21", 1.5e21, "1.5e21"},
		{"22 digits", 1.2345678901234568e21, "1.2345678901234568e21"},
		{"1e-7", 1e-7, "1e-7"},
		{"1.5e-7", 1.5e-7, "1.5e-7"},
		{"negative 1e-7", -1e-7, "-1e-7"},
		{"1.5e300", 1.5e300, "1.5e300"},
		{"largest", math.MaxFloat64, "1.7976931348623157e308"},
		{"smallest normal", 2.2250738585072014e-308, "2.2250738585072014e-308"},
		{"smallest subnormal", math.SmallestNonzeroFloat64, "5e-324"},
		{"largest subnormal", 2.225073858507201e-308, "2.225073858507201e-308"},
		{"123e-20", 123e-20, "1.23e-18"},
		// A float32 is written with its own shortest digits.
		{"float32 tenth", float32(0.1), "0.1"},
		{"float32 third", float32(1.0) / 3, "0.33333334"},
		{"float32 2^24", float32(16777216), "16777216.0"},
		{"float32 largest", float32(math.MaxFloat32), "3.4028235e38"},
		{"float32 smallest subnormal", float32(math.SmallestNonzeroFloat32), "1e-45"},
		{"float32 negative zero", float32(math.Copysign(0, -1)), "0.0"},
		{"float32 infinity", float32(math.Inf(1)), "infinity"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if text := encodeMember(t, testCase.value); text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}
		})
	}
}

func TestFloatNaN(t *testing.T) {
	for _, value := range []any{math.NaN(), float32(math.NaN()), []float64{1, math.NaN()}} {
		_, err := Marshal(map[string]any{"value": value})
		checkError(t, err, 0, 0, "NaN is not a SOML value")
	}
}

// Random bit patterns read back exactly from their canonical form.
func TestFloatRandomRoundTrip(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))

	for range 100 {
		values := make([]float64, 0, 1000)

		for len(values) < cap(values) {
			if value := math.Float64frombits(random.Uint64()); !math.IsNaN(value) {
				values = append(values, value)
			}
		}

		data, err := Marshal(values)

		if err != nil {
			t.Fatal(err)
		}

		var decoded []float64

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		for index, value := range values {
			// Zero has one value whatever its sign.
			if value == 0 {
				value = 0
			}

			if !sameFloat(decoded[index], value) {
				t.Fatalf("%v (%016x) read back as %v (%016x)", value, math.Float64bits(value), decoded[index], math.Float64bits(decoded[index]))
			}
		}
	}

	for range 100 {
		values := make([]float32, 0, 1000)

		for len(values) < cap(values) {
			if value := math.Float32frombits(random.Uint32()); !math.IsNaN(float64(value)) {
				values = append(values, value)
			}
		}

		data, err := Marshal(values)

		if err != nil {
			t.Fatal(err)
		}

		var decoded []float32

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		for index, value := range values {
			if value == 0 {
				value = 0
			}

			if math.Float32bits(decoded[index]) != math.Float32bits(value) {
				t.Fatalf("float32 %v (%08x) read back as %v (%08x)", value, math.Float32bits(value), decoded[index], math.Float32bits(decoded[index]))
			}
		}
	}
}

// bigPowerOfTen is 10^exponent as a rational.
func bigPowerOfTen(exponent int) *big.Rat {
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(exponent, -exponent))), nil)

	if exponent < 0 {
		return new(big.Rat).SetFrac(big.NewInt(1), power)
	}

	return new(big.Rat).SetInt(power)
}

/*
referenceShortest computes the digits of a positive finite float with math/big, as spec rule 11.2 states them: the smallest k for which a k-digit s and an n exist such that s × 10^(n−k) reads back as the value, then the closest to the exact value, then the even s. The value is 0.digits × 10^point.
*/
func referenceShortest(value float64) (digits string, point int) {
	exact := new(big.Rat).SetFloat64(value)
	below := new(big.Rat).SetFloat64(math.Nextafter(value, 0))
	var above *big.Rat

	if next := math.Nextafter(value, math.Inf(1)); math.IsInf(next, 1) {
		above = new(big.Rat).Sub(new(big.Rat).Add(exact, exact), below)
	} else {
		above = new(big.Rat).SetFloat64(next)
	}

	half := big.NewRat(1, 2)
	lower := new(big.Rat).Mul(new(big.Rat).Add(exact, below), half)
	upper := new(big.Rat).Mul(new(big.Rat).Add(exact, above), half)
	isEven := math.Float64bits(value)&1 == 0

	// A decimal reads back as the value when it is inside the interval of values that round to it. Its ends round to the even neighbor.
	readsBack := func(candidate *big.Rat) bool {
		lowerComparison := candidate.Cmp(lower)
		upperComparison := candidate.Cmp(upper)
		return lowerComparison > 0 && upperComparison < 0 || isEven && (lowerComparison == 0 || upperComparison == 0)
	}

	// 10^(n−1) ≤ value < 10^n.
	n := int(math.Floor(math.Log10(value))) + 1

	for bigPowerOfTen(n-1).Cmp(exact) > 0 {
		n--
	}

	for bigPowerOfTen(n).Cmp(exact) <= 0 {
		n++
	}

	for k := 1; k <= 17; k++ {
		scale := bigPowerOfTen(n - k)
		quotient := new(big.Rat).Quo(exact, scale)
		floor := new(big.Int).Quo(quotient.Num(), quotient.Denom())
		var best *big.Int
		var bestDistance *big.Rat

		for _, candidate := range []*big.Int{floor, new(big.Int).Add(floor, big.NewInt(1))} {
			if candidate.Sign() == 0 {
				continue
			}

			decimal := new(big.Rat).Mul(new(big.Rat).SetInt(candidate), scale)

			if !readsBack(decimal) {
				continue
			}

			distance := new(big.Rat).Abs(new(big.Rat).Sub(decimal, exact))

			if best == nil {
				best, bestDistance = candidate, distance
				continue
			}

			comparison := distance.Cmp(bestDistance)

			if comparison < 0 || comparison == 0 && candidate.Bit(0) == 0 {
				best, bestDistance = candidate, distance
			}
		}

		if best != nil {
			text := best.String()
			return strings.TrimRight(text, "0"), n - k + len(text)
		}
	}

	panic(fmt.Sprintf("no shortest digits for %v", value))
}

// referenceLayout writes 0.digits × 10^point as spec rule 11 steps 3 to 6 say.
func referenceLayout(digits string, point int) string {
	count := len(digits)

	switch {
	case count <= point && point <= 21:
		return digits + strings.Repeat("0", point-count) + ".0"
	case 0 < point && point < count:
		return digits[:point] + "." + digits[point:]
	case -6 < point && point <= 0:
		return "0." + strings.Repeat("0", -point) + digits
	}

	text := digits[:1]

	if count > 1 {
		text += "." + digits[1:]
	}

	return text + "e" + strconv.Itoa(point-1)
}

// The canonical digits of random floats, compared with a math/big reference, in case a later Go version changes how strconv picks them.
func TestFloatShortestDigitsReference(t *testing.T) {
	random := rand.New(rand.NewPCG(11, 2))

	values := []float64{
		1125899906842624.25,
		1658206780088562.2,
		math.MaxFloat64,
		math.SmallestNonzeroFloat64,
		2.2250738585072014e-308,
		2.225073858507201e-308,
		1e21,
		1e-7,
		0.1,
		5e-324 * 3,
		9007199254740993,
	}

	for exponent := -1074; exponent <= 1023; exponent += 7 {
		values = append(values, math.Ldexp(1, exponent))
	}

	for len(values) < 4000 {
		var value float64

		switch random.IntN(3) {
		case 0:
			value = math.Abs(math.Float64frombits(random.Uint64()))
		case 1:
			// The range of the layouts with a point.
			value = math.Pow(10, random.Float64()*29-7)
		default:
			// Between 2^50 and 2^54, where ties between two shortest candidates are common.
			value = math.Ldexp(1+random.Float64(), 50+random.IntN(4))
		}

		if math.IsNaN(value) || math.IsInf(value, 0) || value == 0 {
			continue
		}

		values = append(values, value)
	}

	for _, value := range values {
		expected := referenceLayout(referenceShortest(value))

		if text := encodeMember(t, value); text != expected {
			t.Fatalf("%v (%016x): got %s, want %s", value, math.Float64bits(value), text, expected)
		}

		if text := encodeMember(t, -value); text != "-"+expected {
			t.Fatalf("%v: got %s, want -%s", -value, text, expected)
		}
	}
}

func TestStrings(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`'hello'`, "hello"},
		{`''`, ""},
		{`""`, ""},
		{`'C:\Users\name'`, `C:\Users\name`},
		{`'say "hi"'`, `say "hi"`},
		{`'\n'`, `\n`},
		{`"it's"`, "it's"},
		{`"a\\b"`, `a\b`},
		{`"\""`, `"`},
		{`"line\nbreak"`, "line\nbreak"},
		{`"tab\there"`, "tab\there"},
		{`"\u{0}"`, "\x00"},
		{`"\u{7f}"`, "\x7f"},
		{`"\u{41}"`, "A"},
		{`"\u{e9}"`, "é"},
		{`"\u{2028}"`, "\u2028"},
		{`"\u{fffe}"`, "\uFFFE"},
		{`"\u{1f600}"`, "😀"},
		{`"\u{10ffff}"`, "\U0010FFFF"},
		{`"\\u{41}"`, `\u{41}`},
		{"'a\tb'", "a\tb"},
		{"\"a\tb\"", "a\tb"},
		{`'é😀'`, "é😀"},
		{"'\u2028\u2029'", "\u2028\u2029"},
		{"'\u200B'", "\u200B"},
		{"'\uFEFF'", "\uFEFF"},
		{"'\u0085'", "\u0085"},
		{`'#not a comment'`, "#not a comment"},
		{`'/* not a comment */'`, "/* not a comment */"},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.expected) {
				t.Fatalf("got %#v, want %q", value, testCase.expected)
			}
		})
	}
}

// suggestedEscape returns the escape that an error message tells the user to write, if any.
func suggestedEscape(message string) (string, bool) {
	_, after, ok := strings.Cut(message, "rite \\u{")

	if !ok {
		return "", false
	}

	digits, _, ok := strings.Cut(after, "}")
	return "\\u{" + digits + "}", ok && digits != "…"
}

// Every escape that an error suggests is valid, and reads as the character that was meant.
func TestSuggestedEscapeIsValid(t *testing.T) {
	const lone = `is not an escape, and a lone surrogate is not a Unicode scalar value. Write the character it is half of as one \u{…} escape`

	cases := []struct {
		input   string
		message string
	}{
		// A JSON surrogate pair is one escape for the character it encodes.
		{`"\uD83D\uDE00"`, `the four-digit \uD83D\uDE00 form is not an escape. Write \u{1f600}`},
		{`"\ud83d\ude00"`, `the four-digit \ud83d\ude00 form is not an escape. Write \u{1f600}`},
		{`"\uD800\uDC00"`, `the four-digit \uD800\uDC00 form is not an escape. Write \u{10000}`},
		{`"\uDBFF\uDFFF"`, `the four-digit \uDBFF\uDFFF form is not an escape. Write \u{10ffff}`},
		// A lone JSON surrogate has no escape to suggest. A low surrogate first, or a high surrogate before something that is not a low one, is not a pair.
		{`"\uD83D"`, `the four-digit \uD83D form ` + lone},
		{`"\uDE00"`, `the four-digit \uDE00 form ` + lone},
		{`"\uDE00\uD83D"`, `the four-digit \uDE00 form ` + lone},
		{`"\uD83D\uD83D"`, `the four-digit \uD83D form ` + lone},
		{`"\uD83D\u0041"`, `the four-digit \uD83D form ` + lone},
		{`"\uD83D\u{de00}"`, `the four-digit \uD83D form ` + lone},
		{`"\uD83Dx"`, `the four-digit \uD83D form ` + lone},
		{`"\uD83D\uDE0"`, `the four-digit \uD83D form ` + lone},
		// A JSON carriage return has no escape to suggest.
		{`"\u000D"`, `the four-digit \u000D form is not an escape, and a carriage return (U+000D) cannot be represented`},
		{`"\u000d"`, `the four-digit \u000d form is not an escape, and a carriage return (U+000D) cannot be represented`},
		// A braced escape with leading zeros is reported for its value first.
		{`"\u{000d}"`, `a carriage return (U+000D) cannot be represented, so \u{000d} is not allowed`},
		{`"\u{00d800}"`, `\u{00d800} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{0dfff}"`, `\u{0dfff} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{0110000}"`, `a Unicode escape has at most six hexadecimal digits`},
		{`"\u{011000}"`, `a Unicode escape may not have leading zeros; write \u{11000}`},
		{`"\u{00}"`, `a Unicode escape may not have leading zeros; write \u{0}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			_, err := decodeMember(testCase.input)
			checkError(t, err, 1, 9, testCase.message)
		})
	}

	// checkSuggestion checks that an invalid escape suggests a valid one with the expected value, or none when expected is empty.
	checkSuggestion := func(t *testing.T, escape, expected string) {
		t.Helper()
		_, err := decodeMember(`"` + escape + `"`)
		somlError, ok := errors.AsType[*Error](err)

		if !ok {
			t.Fatalf("%s: got %v", escape, err)
		}

		suggestion, isSuggested := suggestedEscape(somlError.Message)

		if isSuggested != (expected != "") {
			t.Fatalf("%s: %q", escape, somlError.Message)
		}

		if !isSuggested {
			return
		}

		if value, err := decodeMember(`"` + suggestion + `"`); err != nil || value != expected {
			t.Fatalf("%s suggests %s, which gives %q, %v", escape, suggestion, value, err)
		}
	}

	t.Run("every four-digit form", func(t *testing.T) {
		for code := range 0x10000 {
			expected := string(rune(code))

			if code == 0x0D || 0xD800 <= code && code <= 0xDFFF {
				expected = ""
			}

			checkSuggestion(t, fmt.Sprintf(`\u%04X`, code), expected)
		}
	})

	t.Run("every surrogate pair", func(t *testing.T) {
		for high := 0xD800; high <= 0xDBFF; high += 0x31 {
			for low := 0xDC00; low <= 0xDFFF; low += 0x29 {
				checkSuggestion(t, fmt.Sprintf(`\u%x\u%x`, high, low), string(utf16.DecodeRune(rune(high), rune(low))))
			}
		}
	})

	t.Run("every braced form with leading zeros", func(t *testing.T) {
		for _, hexadecimal := range []string{"00", "000", "0041", "00e9", "000d", "0d800", "00dbff", "0dc00", "0dfff", "0e000", "010ffff", "0110000", "01f600", "000010"} {
			code, _ := strconv.ParseUint(hexadecimal, 16, 32)
			expected := string(rune(code))

			if code == 0x0D || 0xD800 <= code && code <= 0xDFFF || code > unicode.MaxRune || len(hexadecimal) > 6 {
				expected = ""
			}

			checkSuggestion(t, `\u{`+hexadecimal+`}`, expected)
		}
	})
}

func TestStringsInvalid(t *testing.T) {
	cases := []struct {
		input   string
		column  int
		message string
	}{
		{`"ab\q"`, 11, `unknown escape “\q”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{`"é\q"`, 10, `unknown escape “\q”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{`"\é"`, 9, `unknown escape “\é”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{`"\r"`, 9, `there is no \r escape, because a carriage return cannot be represented`},
		{`"\'"`, 9, `a ' needs no escape inside "..."`},
		{`"\0"`, 9, `unknown escape “\0”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{`"\x41"`, 9, `unknown escape “\x”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{`"\u{E9}"`, 9, `a Unicode escape uses lowercase hexadecimal digits`},
		{`"\u{0041}"`, 9, `a Unicode escape may not have leading zeros; write \u{41}`},
		{`"\u{00}"`, 9, `a Unicode escape may not have leading zeros; write \u{0}`},
		{`"\u0041"`, 9, `the four-digit \u0041 form is not an escape. Write \u{41}`},
		{`"\ud83d\ude00"`, 9, `the four-digit \ud83d\ude00 form is not an escape. Write \u{1f600}`},
		{`"\u{d800}"`, 9, `\u{d800} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{dfff}"`, 9, `\u{dfff} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{d}"`, 9, `a carriage return (U+000D) cannot be represented, so \u{d} is not allowed`},
		{`"\u{110000}"`, 9, `\u{110000} is above U+10FFFF, the largest Unicode scalar value`},
		// The value of an escape with uppercase digits is checked before its spelling, so that lowercasing the digits never gives an escape that is not allowed either.
		{`"\u{D}"`, 9, `a carriage return (U+000D) cannot be represented, so \u{D} is not allowed`},
		{`"\u{00D}"`, 9, `a carriage return (U+000D) cannot be represented, so \u{00D} is not allowed`},
		{`"\u{D800}"`, 9, `\u{D800} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{dFfF}"`, 9, `\u{dFfF} is a surrogate, which is not a Unicode scalar value`},
		{`"\u{11000F}"`, 9, `\u{11000F} is above U+10FFFF, the largest Unicode scalar value`},
		{`"\u{0E9}"`, 9, `a Unicode escape uses lowercase hexadecimal digits`},
		{`"\u{}"`, 9, `a Unicode escape needs one to six hexadecimal digits`},
		{`"\u{1234567}"`, 9, `a Unicode escape has at most six hexadecimal digits`},
		{`"\u{41"`, 9, `a Unicode escape is written \u{…} with one to six lowercase hexadecimal digits`},
		{`"\`, 9, `a backslash must be followed by an escape character. Use \\ for a literal backslash, or a '...' string`},
		{`'abc`, 8, `unterminated string. A '...' string must end on the line it starts on; use a block string (''') for multiple lines`},
		{"'a\nb'", 8, `unterminated string. A '...' string must end on the line it starts on; use a block string (''') for multiple lines`},
		{`"abc`, 8, `unterminated string. A "..." string must end on the line it starts on; use a block string (""") for multiple lines`},
		{"\"a\nb\"", 8, `unterminated string. A "..." string must end on the line it starts on; use a block string (""") for multiple lines`},
		{`'a'b`, 11, `unexpected “b” after a value`},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			_, err := decodeMember(testCase.input)
			checkError(t, err, 1, testCase.column, testCase.message)
		})
	}
}

// The characters that are errors anywhere, also in a string.
func TestStringCharacters(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		column  int
		message string
	}{
		{"NUL", "'a\x00'", 10, `a raw control character (U+0000) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \u{0} inside "..."`},
		{"escape character", "\"\x1b[31m\"", 9, `a raw control character (U+001B) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \u{1b} inside "..."`},
		{"DEL", "'\x7f'", 9, `a raw control character (U+007F) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \u{7f} inside "..."`},
		{"carriage return", "'a\rb'", 10, "a carriage return (U+000D) is not allowed anywhere. Use LF line endings"},
		{"invalid UTF-8", "'a\xffb'", 10, "invalid UTF-8 byte 0xFF"},
		{"a surrogate in UTF-8", "'\xed\xa0\x80'", 9, "invalid UTF-8 byte 0xED"},
		{"incomplete UTF-8", "'\xe2\x82", 9, "incomplete UTF-8 sequence at the end of the input"},
		{"after a multibyte character", "'éé\x01'", 11, `a raw control character (U+0001) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \u{1} inside "..."`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var value any
			err := Unmarshal([]byte("value: "+testCase.input), &value)
			checkError(t, err, 1, testCase.column, testCase.message)
		})
	}

	var value any
	err := Unmarshal([]byte("\uFEFFvalue: 1\n"), &value)
	checkError(t, err, 1, 1, "a byte order mark (BOM) is not allowed")
}

func TestCharacterErrorsPrecedeSyntaxErrors(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		line    int
		column  int
		message string
	}{
		{"UTF-8 before BOM and controls", "\uFEFF?\x00\n\xff\xfe", 2, 1, "invalid UTF-8 byte 0xFF"},
		{"incomplete UTF-8 before BOM and controls", "\uFEFF?\x00\n\xe2\x82", 2, 1, "incomplete UTF-8 sequence at the end of the input"},
		{"BOM before controls", "\uFEFF?\x00", 1, 1, "a byte order mark (BOM) is not allowed"},
		{"first control before syntax", "?\n\r\x00", 2, 1, "a carriage return (U+000D) is not allowed anywhere. Use LF line endings"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var value any
			err := Unmarshal([]byte(testCase.input), &value)
			checkError(t, err, testCase.line, testCase.column, testCase.message)
		})
	}
}

func TestBlockStrings(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic", "'''\n\tConnections are reused.\n\tSecond line.\n\t'''", "Connections are reused.\nSecond line."},
		{"no indentation", "'''\nfirst\nsecond\n'''", "first\nsecond"},
		{"one line", "'''\n  only\n  '''", "only"},
		{"empty", "'''\n'''", ""},
		{"only blank lines", "'''\n\n  \n\t\n'''", ""},
		{"deeper lines keep their extra indentation", "'''\n\t\tA\n\t\t\tB\n\t\t  C\n\t\t'''", "A\n\tB\n  C"},
		{"blank lines at the ends are dropped", "'''\n\n\t\n\tA\n\n\tB\n  \n\n\t'''", "A\n\nB"},
		{"a blank line needs no indentation", "'''\n\t\tA\n\n\t\tB\n\t\t'''", "A\n\nB"},
		{"a blank line with other whitespace is empty", "'''\n\tA\n \t \n\tB\n\t'''", "A\n\nB"},
		{"trailing spaces are content", "'''\n\tA  \n\t'''", "A  "},
		{"literal keeps backslashes", "'''\n\tC:\\path\\n\n\t'''", `C:\path\n`},
		{"literal quotes", "'''\n\tit's \"quoted\" and ''two''\n\t'''", `it's "quoted" and ''two''`},
		{"escaped", "\"\"\"\n\ta\\tb\\n\\u{41}\\\\\n\t\"\"\"", "a\tb\n" + `A\`},
		{"escaped tab line", "\"\"\"\n\tA\n\t\\t\n\tB\n\t\"\"\"", "A\n\t\nB"},
		{"escaped keeps a literal quote", "\"\"\"\n\tsay \"hi\" and \"\"two\"\"\n\t\"\"\"", `say "hi" and ""two""`},
		{"longer delimiter", "''''\n\t'''\n\tinside\n\t''''", "'''\ninside"},
		{"a longer run is content", "'''\n\t''''\n\t'''", "''''"},
		{"the other quote is content", "'''\n\t\"\"\"\n\t'''", `"""`},
		{"a closing run with text before it is content", "'''\n\tx '''\n\t'''", "x '''"},
		{"tabs and spaces", "'''\n \tA\n \t'''", "A"},
		{"raw unicode", "'''\n\té😀\u2028\n\t'''", "é😀\u2028"},
		{"a comment marker is content", "'''\n\t# not a comment\n\t'''", "# not a comment"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.expected) {
				t.Fatalf("got %#v, want %q", value, testCase.expected)
			}
		})
	}

	t.Run("followed by a comma and a comment", func(t *testing.T) {
		var value any
		err := Unmarshal([]byte("pool: {\n\tdescription: '''\n\t\tConnections are reused.\n\t\t''', # the end\n\tsize: 1,\n}\n"), &value)

		if err != nil {
			t.Fatal(err)
		}

		expected := map[string]any{"pool": map[string]any{"description": "Connections are reused.", "size": int64(1)}}

		if !reflect.DeepEqual(value, expected) {
			t.Fatalf("got %#v", value)
		}
	})

	t.Run("in an array", func(t *testing.T) {
		var value any

		if err := Unmarshal([]byte("[\n\t'''\n\ta\n\t''', \"\"\"\n\tb\n\t\"\"\"]\n"), &value); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(value, []any{"a", "b"}) {
			t.Fatalf("got %#v", value)
		}
	})
}

func TestBlockStringsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		line    int
		column  int
		message string
	}{
		{"text after the opening", "'''text\n'''", 1, 11, "a block string's opening delimiter must be followed directly by a line break, and its content starts on the next line"},
		{"a space after the opening", "''' \n'''", 1, 11, "a block string's opening delimiter must be followed directly by a line break, and its content starts on the next line"},
		{"a comment after the opening", "\"\"\" # comment\n\"\"\"", 1, 11, "a block string's opening delimiter must be followed directly by a line break, and its content starts on the next line"},
		{"less indentation", "'''\n\t\tA\n\tB\n\t\t'''", 3, 1, "this line does not start with the indentation of its block string's closing delimiter. Every line except a blank one must start with exactly the same spaces and tabs"},
		{"spaces for a tab", "'''\n    A\n\t'''", 2, 1, "this line does not start with the indentation of its block string's closing delimiter. Every line except a blank one must start with exactly the same spaces and tabs"},
		{"unterminated", "'''\n\tA\n", 1, 8, "unterminated block string"},
		{"unterminated at the opening", "'''", 1, 8, "unterminated block string"},
		{"unterminated with a longer run", "'''\n\tA\n\t''''", 1, 8, "unterminated block string"},
		{"text after the closing", "'''\n\tA\n\t'''x", 3, 5, "unexpected “x” after a value"},
		{"a bad escape", "\"\"\"\n\tok\n\tbad \\q\n\t\"\"\"", 3, 6, `unknown escape “\q”. The escapes are \\, \", \n, \t, and \u{…}; use a '...' string for literal backslashes`},
		{"a raw control character", "'''\n\t\x01\n\t'''", 2, 2, `a raw control character (U+0001) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \u{1} inside "..."`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var value any
			err := Unmarshal([]byte("value: "+testCase.input), &value)
			checkError(t, err, testCase.line, testCase.column, testCase.message)
		})
	}

	t.Run("as a key", func(t *testing.T) {
		var value any
		err := Unmarshal([]byte("'''\na\n''': 1\n"), &value)
		checkError(t, err, 1, 1, "a block string cannot be a key")
	})
}

func TestInstants(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Time
	}{
		{"2026-09-19T14:00:00Z", time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)},
		{"2026-09-19T14:00:00+02:00", time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)},
		{"2026-09-19T14:00:00-07:30", time.Date(2026, 9, 19, 21, 30, 0, 0, time.UTC)},
		{"2026-09-19T14:00:00+00:00", time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)},
		{"2026-09-19T23:30:00-01:00", time.Date(2026, 9, 20, 0, 30, 0, 0, time.UTC)},
		{"2026-01-01T00:00:00+23:59", time.Date(2025, 12, 31, 0, 1, 0, 0, time.UTC)},
		{"2026-09-19T14:00:00.5Z", time.Date(2026, 9, 19, 14, 0, 0, 500_000_000, time.UTC)},
		{"2026-09-19T14:00:00.000000001Z", time.Date(2026, 9, 19, 14, 0, 0, 1, time.UTC)},
		{"2026-09-19T14:00:00.123456789+01:00", time.Date(2026, 9, 19, 13, 0, 0, 123_456_789, time.UTC)},
		{"2026-09-19T14:00:00.100Z", time.Date(2026, 9, 19, 14, 0, 0, 100_000_000, time.UTC)},
		{"0001-01-01T00:00:00Z", time.Time{}},
		{"0001-01-01T00:59:00+00:59", time.Time{}},
		{"0001-01-01T00:00:00-23:59", time.Date(1, 1, 1, 23, 59, 0, 0, time.UTC)},
		{"9999-12-31T23:59:59.999999999Z", time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)},
		{"9999-12-31T22:00:00-01:59", time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC)},
		{"2024-02-29T00:00:00Z", time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"2000-02-29T00:00:00Z", time.Date(2000, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"2026-04-30T23:59:59Z", time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC)},
		{"2026-12-31T00:00:00Z", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			instant, ok := value.(time.Time)

			if !ok {
				t.Fatalf("got %#v, want a time.Time", value)
			}

			if !instant.Equal(testCase.expected) || instant.Location() != time.UTC {
				t.Fatalf("got %v in %v, want %v in UTC", instant, instant.Location(), testCase.expected)
			}
		})
	}
}

func TestInstantsInvalid(t *testing.T) {
	invalid := func(text, reason string) invalidScalar {
		return invalidScalar{text, "invalid instant " + text + ": " + reason}
	}

	const form = ". An instant is written as 2026-09-19T14:00:00Z, with an optional fraction of up to nine digits and an offset of Z or ±HH:MM"

	checkInvalidScalars(t, []invalidScalar{
		invalid("0000-01-01T00:00:00Z", "the year must be 0001 to 9999"),
		invalid("2026-00-01T00:00:00Z", "the month must be 01 to 12"),
		invalid("2026-13-01T00:00:00Z", "the month must be 01 to 12"),
		invalid("2026-01-00T00:00:00Z", "the day must be 01 to 31 in that month"),
		invalid("2026-01-32T00:00:00Z", "the day must be 01 to 31 in that month"),
		invalid("2026-04-31T00:00:00Z", "the day must be 01 to 30 in that month"),
		invalid("2026-06-31T00:00:00Z", "the day must be 01 to 30 in that month"),
		invalid("2026-09-31T00:00:00Z", "the day must be 01 to 30 in that month"),
		invalid("2026-11-31T00:00:00Z", "the day must be 01 to 30 in that month"),
		invalid("2023-02-29T00:00:00Z", "the day must be 01 to 28 in that month"),
		invalid("1900-02-29T00:00:00Z", "the day must be 01 to 28 in that month"),
		invalid("2024-02-30T00:00:00Z", "the day must be 01 to 29 in that month"),
		invalid("2026-09-19T24:00:00Z", "the hour must be 00 to 23"),
		invalid("2026-09-19T14:60:00Z", "the minute must be 00 to 59"),
		invalid("2026-09-19T14:00:60Z", "the second must be 00 to 59, and a leap second is not representable"),
		invalid("2026-09-19T14:00:00+24:00", "the offset hour must be 00 to 23"),
		invalid("2026-09-19T14:00:00+01:60", "the offset minute must be 00 to 59"),
		invalid("2026-09-19T14:00:00-00:00", "-00:00 means “offset unknown” in RFC 3339, which is not representable; use Z or +00:00"),
		invalid("2026-09-19T14:00:00.1234567890Z", "a fractional second has at most nine digits"),
		// In range in every component, but not in UTC.
		invalid("0001-01-01T00:00:00+00:01", "in UTC it falls outside the years 0001 to 9999"),
		invalid("0001-01-01T23:58:59+23:59", "in UTC it falls outside the years 0001 to 9999"),
		invalid("9999-12-31T23:59:59-00:01", "in UTC it falls outside the years 0001 to 9999"),
		invalid("9999-12-31T23:00:00-01:00", "in UTC it falls outside the years 0001 to 9999"),
		{"2026-09-19t14:00:00Z", "the date and time separator in an instant is an uppercase “T”"},
		{"2026-09-19T14:00:00z", "the UTC offset in an instant is an uppercase “Z”"},
		{"2026-09-19T14:00:00+0200", "an instant's offset is written with a colon, as in +07:00"},
		{"2026-09-19", "2026-09-19 is a date, not an instant. Write a date as a string, as in '2026-09-19'. An instant needs a time and an offset, as in 2026-09-19T00:00:00Z"},
		{"2026-09-19T14:00:00", "an instant needs an offset: Z or ±HH:MM. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '2026-09-19T14:00:00', or add the offset it was meant in"},
		{"2026-09-19T14:00:00.5", "an instant needs an offset: Z or ±HH:MM. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '2026-09-19T14:00:00.5', or add the offset it was meant in"},
		{"2026-09-19T14:00Z", "invalid instant “2026-09-19T14:00Z”" + form},
		{"2026-09-19T14:00:00.Z", "invalid instant “2026-09-19T14:00:00.Z”" + form},
		{"2026-9-19T14:00:00Z", "invalid number “2026-9-19T14:00:00Z”. A string value must be quoted, as in '2026-9-19T14:00:00Z'"},
		{"2026-09-19T14:00:00+2:00", "invalid instant “2026-09-19T14:00:00+2:00”" + form},
	})

	t.Run("a bare instant is not a document", func(t *testing.T) {
		var value any
		err := Unmarshal([]byte("2026-09-19T14:00:00Z\n"), &value)
		checkError(t, err, 1, 1, "a bare value is not a document. A document is an object or an array, so write it as `key: value` or `[value]`")
	})

	t.Run("an instant as a key is a string", func(t *testing.T) {
		var value map[string]any

		if err := Unmarshal([]byte("2024-01-01: 'new year'\n"), &value); err != nil {
			t.Fatal(err)
		}

		if value["2024-01-01"] != "new year" {
			t.Fatalf("got %#v", value)
		}
	})
}

func TestInstantCanonical(t *testing.T) {
	cases := []struct {
		value    time.Time
		expected string
	}{
		{time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC), "2026-09-19T14:00:00Z"},
		{time.Date(2026, 9, 19, 14, 0, 0, 500_000_000, time.UTC), "2026-09-19T14:00:00.5Z"},
		{time.Date(2026, 9, 19, 14, 0, 0, 120_000_000, time.UTC), "2026-09-19T14:00:00.12Z"},
		{time.Date(2026, 9, 19, 14, 0, 0, 123_456_789, time.UTC), "2026-09-19T14:00:00.123456789Z"},
		{time.Date(2026, 9, 19, 14, 0, 0, 100, time.UTC), "2026-09-19T14:00:00.0000001Z"},
		{time.Date(2026, 9, 19, 14, 0, 0, 0, time.FixedZone("", 2*60*60)), "2026-09-19T12:00:00Z"},
		{time.Date(2026, 9, 19, 23, 30, 0, 0, time.FixedZone("", -90*60)), "2026-09-20T01:00:00Z"},
		{time.Time{}, "0001-01-01T00:00:00Z"},
		{time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), "9999-12-31T23:59:59.999999999Z"},
		{time.Date(10000, 1, 1, 0, 30, 0, 0, time.FixedZone("", 60*60)), "9999-12-31T23:30:00Z"},
		{time.Date(5, 3, 4, 5, 6, 7, 0, time.UTC), "0005-03-04T05:06:07Z"},
	}

	for _, testCase := range cases {
		t.Run(testCase.expected, func(t *testing.T) {
			text := encodeMember(t, testCase.value)

			if text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}

			value, err := decodeMember(text)

			if err != nil {
				t.Fatal(err)
			}

			if instant, ok := value.(time.Time); !ok || !instant.Equal(testCase.value) {
				t.Fatalf("read back as %#v", value)
			}
		})
	}

	t.Run("a monotonic clock reading is ignored", func(t *testing.T) {
		now := time.Now()

		if text, expected := encodeMember(t, now), now.UTC().Format("2006-01-02T15:04:05.999999999Z"); text != expected {
			t.Fatalf("got %s, want %s", text, expected)
		}
	})
}

func TestDurations(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"90m", 90 * time.Minute},
		{"1.5h", 90 * time.Minute},
		{"1h1.5m", time.Hour + 90*time.Second},
		{"1.5s", 1500 * time.Millisecond},
		{"1500ms", 1500 * time.Millisecond},
		{"250us", 250 * time.Microsecond},
		{"0.5us", 500 * time.Nanosecond},
		{"1.25ms", 1250 * time.Microsecond},
		{"1ns", 1},
		{"-5m", -5 * time.Minute},
		{"-1h30m", -90 * time.Minute},
		{"0s", 0},
		{"0ns", 0},
		{"0h", 0},
		{"0h0m0s", 0},
		{"1h0m0s", time.Hour},
		{"1_000ms", time.Second},
		{"1h2m3s4ms5us6ns", time.Hour + 2*time.Minute + 3*time.Second + 4*time.Millisecond + 5*time.Microsecond + 6},
		{"24h", 24 * time.Hour},
		{"0.0000000001h", 360},
		{"1.000000001s", time.Second + 1},
		{"1.500000000000000000000000000s", 1500 * time.Millisecond},
		{"1.0ns", 1},
		{"1_0.2_5s", 10250 * time.Millisecond},
		{"9223372036854775807ns", math.MaxInt64},
		{"-9223372036854775808ns", math.MinInt64},
		{"2562047h47m16.854775807s", math.MaxInt64},
		{"-2562047h47m16.854775808s", math.MinInt64},
		{"0.0000000000025h", 9},
		{"153722867m16.854775807s", math.MaxInt64},
		{"9223372036.854775807s", math.MaxInt64},
		{"9223372036854775.807us", math.MaxInt64},
	}

	for _, testCase := range cases {
		t.Run(testCase.input, func(t *testing.T) {
			value, err := decodeMember(testCase.input)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.expected) {
				t.Fatalf("got %#v, want %v (%d)", value, testCase.expected, int64(testCase.expected))
			}
		})
	}
}

func TestDurationsInvalid(t *testing.T) {
	invalid := func(text, reason string) invalidScalar {
		return invalidScalar{text, "invalid duration " + text + ": " + reason}
	}

	const outOfRange = "it is outside the 64-bit range of nanoseconds, about 292 years either way"
	const notWhole = "it is not a whole number of nanoseconds"
	const order = "the units are in the order h, m, s, ms, us, ns, and each appears at most once"
	const year = "there is no year unit, because a year is not a fixed length. Write 8760h for a fixed 365 days"

	checkInvalidScalars(t, []invalidScalar{
		invalid("9223372036854775808ns", outOfRange),
		invalid("-9223372036854775809ns", outOfRange),
		invalid("2562048h", outOfRange),
		invalid("2562047h47m16.854775808s", outOfRange),
		invalid("99999999999999999999999h", outOfRange),
		invalid("18446744073709551616ns", outOfRange),
		invalid("2562047h47m16s854ms775us808ns", outOfRange),
		invalid("0.5ns", notWhole),
		invalid("1.0000000001s", notWhole),
		invalid("0.0000000000001h", notWhole),
		invalid("1.00000000000001ms", notWhole),
		invalid("30m1h", order),
		invalid("1m1m", order),
		invalid("1s1m", order),
		invalid("1ns1us", order),
		invalid("1.5h30m", "only the last part may have a fraction"),
		invalid("-0s", "“-” is not allowed before zero, because zero has one spelling: 0s"),
		invalid("-0h0m", "“-” is not allowed before zero, because zero has one spelling: 0s"),
		invalid("1d", "there is no day unit, because a day is not a fixed length. Write 24h for a fixed 24 hours"),
		invalid("1w", "there is no week unit, because a day is not a fixed length. Write 168h for a fixed 168 hours"),
		invalid("1y", year),
		invalid("1.5y", year),
		invalid("-1y", year),
		invalid("2years", year),
		invalid("3yrs", year),
		invalid("1YEAR", year),
		invalid("1y6m", year),
		invalid("1h1y", year),
		// A word that only starts like a year unit is a string to quote.
		{"100yen", "invalid number “100yen”. A string value must be quoted, as in '100yen'"},
		invalid("1H", "the units are lowercase: h"),
		invalid("1Ms", "the units are lowercase: ms"),
		invalid("1US", "the units are lowercase: us"),
		invalid("01s", "leading zeros are not allowed"),
		invalid("1h05m", "leading zeros are not allowed"),
		invalid("1_s", "an underscore must be between two digits"),
		invalid("1._5s", "a “.” must be followed by a digit"),
		invalid("1.5_s", "an underscore must be between two digits"),
		invalid("1.s", "a “.” must be followed by a digit"),
		invalid("1h-30m", "only the whole duration takes a sign, as in -1h30m"),
		invalid("1s5", "every number needs a unit: h, m, s, ms, us, or ns"),
		invalid("1sec", "“sec” is not a unit. The units are h, m, s, ms, us, and ns, and a string must be quoted"),
		invalid("1hm", "“hm” is not a unit. The units are h, m, s, ms, us, and ns, and a string must be quoted"),
		{"+1s", "a “+” sign is not allowed. A number without a sign is positive"},
		invalid("1s+1m", "a “+” sign is not allowed"),
	})

	t.Run("a unit that is not ASCII", func(t *testing.T) {
		_, err := decodeMember("1µs")
		checkError(t, err, 1, 9, "the unit for microseconds is written us, as in 1us")
	})
}

func TestDurationCanonical(t *testing.T) {
	cases := []struct {
		value    time.Duration
		expected string
	}{
		{90 * time.Minute, "1h30m"},
		{1500 * time.Millisecond, "1.5s"},
		{250 * time.Microsecond, "0.00025s"},
		{0, "0s"},
		{1, "0.000000001s"},
		{100 * time.Millisecond, "0.1s"},
		{30 * time.Second, "30s"},
		{time.Hour, "1h"},
		{time.Minute, "1m"},
		{61 * time.Second, "1m1s"},
		{time.Hour + time.Second, "1h1s"},
		{time.Hour + 500*time.Millisecond, "1h0.5s"},
		{25 * time.Hour, "25h"},
		{-5 * time.Minute, "-5m"},
		{-1, "-0.000000001s"},
		{-(time.Hour + 1), "-1h0.000000001s"},
		{1234567*time.Hour + 53*time.Minute + 24*time.Second, "1234567h53m24s"},
		{math.MaxInt64, "2562047h47m16.854775807s"},
		{math.MinInt64, "-2562047h47m16.854775808s"},
	}

	for _, testCase := range cases {
		t.Run(testCase.expected, func(t *testing.T) {
			text := encodeMember(t, testCase.value)

			if text != testCase.expected {
				t.Fatalf("got %s, want %s", text, testCase.expected)
			}

			value, err := decodeMember(text)

			if err != nil {
				t.Fatal(err)
			}

			if value != any(testCase.value) {
				t.Fatalf("read back as %#v", value)
			}
		})
	}

	t.Run("random durations read back", func(t *testing.T) {
		random := rand.New(rand.NewPCG(3, 4))
		values := make([]time.Duration, 1000)

		for index := range values {
			values[index] = time.Duration(random.Uint64() >> random.IntN(64))

			if random.IntN(2) == 0 {
				values[index] = -values[index]
			}
		}

		data, err := Marshal(values)

		if err != nil {
			t.Fatal(err)
		}

		var decoded []time.Duration

		if err := Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(decoded, values) {
			t.Fatal("random durations did not read back")
		}
	})
}

func TestScalarHints(t *testing.T) {
	tests := []struct {
		document, message string
	}{
		{"memory: 512M", "invalid duration 512M: there is no month unit, because a month is not a fixed length. A size, such as 512M, is a string: '512M'"},
		{"a: 1h2M", "invalid duration 1h2M: there is no month unit, because a month is not a fixed length"},
		{"zip: 0089", "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '0089'"},
		// The digit that is not octal comes after the length that is diagnosed, so the whole number decides that there is no octal suggestion.
		{"a: 0" + strings.Repeat("0", 1100) + "8", "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '" + strings.Repeat("0", 40) + "…'"},
		{"a: 0" + strings.Repeat("0", 1100) + "7", "leading zeros are not allowed in a decimal number. Write an identifier, such as a ZIP code, as a string: '" + strings.Repeat("0", 40) + "…'"},
		{"mode: 0644", "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as 0o644, and an identifier, such as a ZIP code, as a string: '0644'"},
	}

	for _, test := range tests {
		var value any
		err := Unmarshal([]byte(test.document), &value)

		if somlError, ok := errors.AsType[*Error](err); !ok || somlError.Message != test.message {
			t.Errorf("%q gave %v, want %q", test.document, err, test.message)
		}
	}
}

// A comment is not content, so it may hold any Unicode scalar value except the raw control characters that are refused everywhere, also the invisible and bidirectional characters that are errors between tokens.
func TestCommentsHoldAnyCharacterExceptTheRefusedControls(t *testing.T) {
	characters := "\u00a0\u0085\u200b\u2028\u2029\u202e\u2066\ufeff\U0010FFFF"
	var value any

	if err := Unmarshal([]byte("# "+characters+"\na: 1 /* "+characters+" */\n"), &value); err != nil {
		t.Fatal(err)
	}

	err := Unmarshal([]byte("a: 1 # \x01\n"), &value)
	checkError(t, err, 1, 8, "a raw control character (U+0001) is not allowed anywhere, including in strings and comments. In a string, write it as the escape \\u{1} inside \"...\"")
}
