package soml

// Reading one token: numbers, instants, durations, and strings. Both the parser and the syntax tree use these functions, so the two cannot disagree about a value.

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

/*
A syntax error at a byte offset in the source. The parser turns it into an `*Error` with a line and a column at the end, so that the position is only computed for a document that fails.
*/
type syntaxError struct {
	message string
	offset  int
}

func fail(offset int, format string, arguments ...any) *syntaxError {
	return &syntaxError{message: fmt.Sprintf(format, arguments...), offset: offset}
}

// isBareKeyByte reports whether a byte is a letter, a digit, `_`, or `-`: the characters of a bare key.
func isBareKeyByte(character byte) bool {
	return 'a' <= character && character <= 'z' || 'A' <= character && character <= 'Z' || '0' <= character && character <= '9' || character == '_' || character == '-'
}

// isBareKey reports whether a key can be written without quotes.
func isBareKey(key string) bool {
	if key == "" {
		return false
	}

	for index := range len(key) {
		if !isBareKeyByte(key[index]) {
			return false
		}
	}

	return true
}

// isNumberByte reports whether a byte can be part of a number, an instant, or a duration. Such a token is lexed as one maximal run of these, and then checked as a whole.
func isNumberByte(character byte) bool {
	return isBareKeyByte(character) || character == '.' || character == ':' || character == '+'
}

func isDigit(character byte) bool {
	return '0' <= character && character <= '9'
}

func isDigitOrUnderscore(character byte) bool {
	return isDigit(character) || character == '_'
}

func isOctalDigit(character byte) bool {
	return '0' <= character && character <= '7'
}

func isBinaryDigit(character byte) bool {
	return character == '0' || character == '1'
}

func isHexadecimalDigit(character byte) bool {
	return isDigit(character) || 'A' <= character && character <= 'F'
}

func isLetter(character byte) bool {
	return 'a' <= character && character <= 'z' || 'A' <= character && character <= 'Z'
}

// isSpace reports whether a byte is a space or a tab, the whitespace within a line.
func isSpace(character byte) bool {
	return character == ' ' || character == '\t'
}

// skipSpaces returns the index after the spaces and tabs at index.
// Note: Not using strings.TrimLeft because it is slower for the short indentation runs common in config files.
func skipSpaces(text string, index int) int {
	for isSpace(at(text, index)) {
		index++
	}

	return index
}

// at returns the byte at index, or 0 past the end. No valid token contains a NUL, because a raw control character is refused before parsing.
func at(text string, index int) byte {
	if index < 0 || index >= len(text) {
		return 0
	}

	return text[index]
}

// skipDigits returns the index after a run of digits that may have single underscores between them, or index when there is no digit there.
func skipDigits(text string, index int, isDigit func(byte) bool) int {
	if !isDigit(at(text, index)) {
		return index
	}

	index++

	for {
		switch {
		case isDigit(at(text, index)):
			index++
		case at(text, index) == '_' && isDigit(at(text, index+1)):
			index += 2
		default:
			return index
		}
	}
}

// skipIntegerPart returns the index after an integer part, which is `0` or digits without a leading zero, or index when there is none. Anything after a leading `0`, such as the `5` in `05`, is left for the caller to reject.
func skipIntegerPart(text string, index int) int {
	if at(text, index) == '0' {
		return index + 1
	}

	return skipDigits(text, index, isDigit)
}

type numberKind int

const (
	notANumber numberKind = iota
	decimalNumber
	radixNumber
	floatNumber
)

// classifyNumber reports whether text is a decimal int, a radix int, or a float, following the grammar.
func classifyNumber(text string) numberKind {
	if at(text, 0) == '0' {
		var isRadixDigit func(byte) bool

		switch at(text, 1) {
		case 'x':
			isRadixDigit = isHexadecimalDigit
		case 'o':
			isRadixDigit = isOctalDigit
		case 'b':
			isRadixDigit = isBinaryDigit
		}

		if isRadixDigit != nil {
			end := skipDigits(text, 2, isRadixDigit)

			if end > 2 && end == len(text) {
				return radixNumber
			}

			return notANumber
		}
	}

	index := 0

	if at(text, 0) == '-' {
		index = 1
	}

	integerEnd := skipIntegerPart(text, index)

	if integerEnd == index {
		return notANumber
	}

	index = integerEnd
	kind := decimalNumber

	if at(text, index) == '.' {
		end := skipDigits(text, index+1, isDigit)

		if end == index+1 {
			return notANumber
		}

		index = end
		kind = floatNumber
	}

	if at(text, index) == 'e' {
		isNegative := at(text, index+1) == '-'
		exponentStart := index + 1

		if isNegative {
			exponentStart++
		}

		// The exponent follows the leading-zero rule of the integer part, and its zero has one spelling, `e0`, so `e-0` is not an exponent.
		end := exponentStart

		if !isNegative || at(text, exponentStart) != '0' {
			end = skipIntegerPart(text, exponentStart)
		}

		if end == exponentStart {
			return notANumber
		}

		index = end
		kind = floatNumber
	}

	if index != len(text) {
		return notANumber
	}

	return kind
}

// isDurationLike reports whether a token that is not a number or an instant was meant as a duration: its first letter could begin a unit, or a day or a week, or it is a year unit. A radix prefix, an exponent, and other letters, such as the `T` in `20260919T140000Z` or the `x` in `1.5x`, are left to the number errors.
func isDurationLike(text string) bool {
	index := 0

	if at(text, 0) == '-' {
		index = 1
	}

	if !isDigit(at(text, index)) {
		return false
	}

	for isDigitOrUnderscore(at(text, index)) {
		index++
	}

	if at(text, index) == '.' {
		index++

		for isDigitOrUnderscore(at(text, index)) {
			index++
		}
	}

	// Setting the 0x20 bit lowercases an ASCII letter, so that a unit in the wrong case, such as `5H`, still gets the duration error that names the lowercase unit.
	switch at(text, index) | 0x20 {
	case 'd', 'h', 'm', 'n', 's', 'u', 'w':
		return isLetter(at(text, index))
	}

	// A year unit, as in `1y` or `2years`, is only one when the token or another part follows it, so that a word such as `100yen` stays a string to quote.
	unitEnd := index

	for isLetter(at(text, unitEnd)) {
		unitEnd++
	}

	return isYearUnit(text[index:unitEnd]) && (unitEnd == len(text) || isDigit(text[unitEnd]))
}

// hasDatePrefix reports whether a token begins like a date, `YYYY-MM-DD`, so it is read as an instant.
func hasDatePrefix(text string) bool {
	return len(text) >= 10 && allDigits(text[:4]) && text[4] == '-' && allDigits(text[5:7]) && text[7] == '-' && allDigits(text[8:10])
}

// Note: Not using strings.Trim or strings.ContainsFunc because the direct loop is faster for short ASCII fields.
func allDigits(text string) bool {
	for index := range len(text) {
		if !isDigit(text[index]) {
			return false
		}
	}

	return true
}

// tokenValue reads a number, an instant, or a duration from the whole token between start and end in source, as a node without its offset. The error is the reason the token is invalid, which the caller reports at the token's start.
func tokenValue(source string, start, end int) (node, string) {
	text := source[start:end]
	value := node{}

	if hasDatePrefix(text) {
		instant, reason := parseInstant(source, start, end)
		value.kind = kindInstant
		value.setInstant(instant)
		return value, reason
	}

	switch classifyNumber(text) {
	case decimalNumber, radixNumber:
		if text == "-0" {
			return value, "“-0” is not allowed, because zero has one spelling: 0"
		}

		integer, reason := parseInteger(text)
		value.kind = kindInt
		value.integer = integer
		return value, reason
	case floatNumber:
		float, reason := parseFloat(text)
		value.kind = kindFloat
		value.setFloat(float)
		return value, reason
	}

	if isDurationLike(text) {
		duration, reason := parseDuration(text)
		value.kind = kindDuration
		value.integer = int64(duration)
		return value, reason
	}

	if reason := describeThousandsSeparator(source, start, text); reason != "" {
		return value, reason
	}

	return value, describeBadNumber(text, unquotedText(source, start))
}

// describeThousandsSeparator gives the reason for a comma used as a thousands separator, as in `[1,000]`, where text, the token at start, is the group after the comma, which the comma makes a separate item. That fails only when the group has a leading zero. Writing that item in octal, as the leading zero message suggests, would parse to the wrong items. It is empty for any other token.
func describeThousandsSeparator(source string, start int, text string) string {
	if len(text) != 3 || text[0] != '0' || !isDigit(text[1]) || !isDigit(text[2]) || at(source, start-1) != ',' {
		return ""
	}

	digitsStart := func(digitsEnd int) int {
		for isDigit(at(source, digitsEnd-1)) {
			digitsEnd--
		}

		return digitsEnd
	}

	groupEnd := start - 1
	numberStart := digitsStart(groupEnd)

	// Earlier groups of three digits, as in `12,345,000`.
	for groupEnd-numberStart == 3 && at(source, numberStart-1) == ',' && isDigit(at(source, numberStart-2)) {
		groupEnd = numberStart - 1
		numberStart = digitsStart(groupEnd)
	}

	before := at(source, numberStart-1)

	// The first group has one to three digits, which are not the end of a float, of a number with underscores, or of a number with a radix prefix.
	if numberStart == groupEnd || before == '.' || before == '_' || numberStart < groupEnd-3 || isLetter(before) {
		return ""
	}

	// The sign belongs to the number, as in `-1,000`.
	if before == '-' {
		numberStart--
	}

	// The groups of three digits after this one, as in `1,000,000`.
	groupsStart := start + len(text)
	groupsEnd := groupsStart
	limit := groupsStart + maxDiagnosedLength

	for groupsEnd+4 <= limit && at(source, groupsEnd) == ',' && isDigit(at(source, groupsEnd+1)) && isDigit(at(source, groupsEnd+2)) && isDigit(at(source, groupsEnd+3)) && (groupsEnd+4 == limit || !isDigit(at(source, groupsEnd+4))) {
		groupsEnd += 4
	}

	number := source[numberStart:groupsEnd]
	reason := "leading zeros are not allowed in a decimal number. A comma separates items, so " + abbreviate(number, 40) + " is not one number"
	// Both spellings are the same int, so they are suggested only when it is in range.
	withUnderscores := strings.ReplaceAll(number, ",", "_")

	if !isValidValue(withUnderscores) {
		return reason
	}

	return reason + ". Write it as " + abbreviate(withUnderscores, 40) + " or " + abbreviate(strings.ReplaceAll(number, ",", ""), 40)
}

// parseInteger reads an int that matches the grammar.
func parseInteger(text string) (int64, string) {
	// Note: Not using strconv.ParseInt because it repeats syntax checks and is slower for these already validated digits.
	radix := uint64(10)
	digits := text
	isNegative := false

	if len(text) > 1 && text[0] == '0' && (text[1] == 'x' || text[1] == 'o' || text[1] == 'b') {
		switch text[1] {
		case 'x':
			radix = 16
		case 'o':
			radix = 8
		default:
			radix = 2
		}

		digits = text[2:]
	} else if text[0] == '-' {
		isNegative = true
		digits = text[1:]
	}

	outOfRange := func() (int64, string) {
		return 0, "the integer " + abbreviate(text, 40) + " is outside the 64-bit range (-9223372036854775808 to 9223372036854775807)"
	}

	// Accumulated as the magnitude, so that the most negative value, whose magnitude is one more than the largest positive value, is read too.
	var magnitude uint64

	for index := range len(digits) {
		character := digits[index]

		if character == '_' {
			continue
		}

		var digit uint64

		switch {
		case isDigit(character):
			digit = uint64(character - '0')
		default:
			// The grammar only allows uppercase hexadecimal digits, so any other character here is `A` to `F`.
			digit = uint64(character-'A') + 10
		}

		high, low := bits.Mul64(magnitude, radix)
		sum, carry := bits.Add64(low, digit, 0)

		if high != 0 || carry != 0 {
			return outOfRange()
		}

		magnitude = sum
	}

	if isNegative {
		if magnitude > 1<<63 {
			return outOfRange()
		}

		// For a magnitude of 2^63, the conversion and the negation both wrap to the most negative int64, which is the value.
		return -int64(magnitude), ""
	}

	if magnitude > math.MaxInt64 {
		return outOfRange()
	}

	return int64(magnitude), ""
}

/*
parseFloat reads a float that matches the grammar.

`strconv.ParseFloat` is not given a long literal as it is, because it returns wrong values for some literals with more than 800 digits before the decimal point, and it clamps an exponent past 10,000 (Go issue #42436). A long literal is first rewritten as `d.ddd…e±N`, with the exponent computed here, which avoids both.
*/
func parseFloat(text string) (float64, string) {
	value := roundFloat(text, 64)

	if math.IsInf(value, 0) {
		// The sign comes from the text, because a literal far outside the range rounds to an infinity without one.
		infinity := "infinity"

		if text[0] == '-' {
			infinity = "-infinity"
		}

		return 0, abbreviate(text, 40) + " is too large to be a finite float. Use " + infinity + " if you mean it"
	}

	if value == 0 {
		// A nonzero digit before the exponent means the literal is not zero, so it underflowed.
		significand, _, _ := strings.Cut(text, "e")

		if strings.ContainsAny(significand, "123456789") {
			return 0, abbreviate(text, 40) + " is too small to be told apart from zero. Write 0.0 if you mean zero"
		}

		// Negative zero is the same value as zero.
		return 0, ""
	}

	return value, ""
}

// roundFloat rounds a float literal that matches the grammar to the nearest value of bitSize bits, 64 or 32, once, from its digits. It saturates to an infinity or to zero, with no sign for an infinity or a zero it rounds to from far outside the range.
func roundFloat(text string, bitSize int) float64 {
	if len(text) <= 32 && !strings.Contains(text, "_") {
		// Too short for either bug: at most 31 digits, so the clamped exponent still gives the same overflow or underflow.
		value, _ := strconv.ParseFloat(text, bitSize)
		return value
	}

	normalized, isZero, exponent := normalizeFloat(text)

	// The value is at least 10^exponent and below 10^(exponent+1), so past ±400 it is certainly outside the float64 range. Within it, `strconv.ParseFloat` gets an exponent too small to be clamped.
	switch {
	case isZero:
		return 0
	case exponent > 400:
		return math.Inf(1)
	case exponent < -400:
		return 0
	}

	value, _ := strconv.ParseFloat(normalized, bitSize)
	return value
}

/*
normalizeFloat rewrites a float literal that matches the grammar as `-d.ddd…e±N`, without underscores and without leading or trailing zeros. It also returns whether the value is zero, and the exponent of the rewritten form, which saturates far outside the binary64 range.
*/
func normalizeFloat(text string) (normalized string, isZero bool, exponent int64) {
	significand, exponentText, _ := strings.Cut(text, "e")
	significand, isNegative := strings.CutPrefix(significand, "-")
	integerPart, fractionPart, _ := strings.Cut(significand, ".")

	// Note: Not using strings.Map, strings.Replacer, or chained strings.ReplaceAll because they slow short literals or allocate more.
	var digits strings.Builder
	digits.Grow(len(significand))

	for index := range len(significand) {
		character := significand[index]

		if isDigit(character) && (digits.Len() > 0 || character != '0') {
			digits.WriteByte(character)
		}
	}

	significant := strings.TrimRight(digits.String(), "0")

	if significant == "" {
		return "", true, 0
	}

	// The exponent stops growing at the limit, so that a huge exponent cannot overflow an int64. Any exponent that far out gives the same infinity or zero.
	const limit = 1 << 40

	for index := range len(exponentText) {
		character := exponentText[index]

		if isDigit(character) && exponent < limit {
			exponent = exponent*10 + int64(character-'0')
		}
	}

	if strings.HasPrefix(exponentText, "-") {
		exponent = -exponent
	}

	// The value is 0.d₁d₂… × 10^point, where point counts the integer digits after the leading zeros, minus the zeros at the start of the fraction.
	integerDigits := int64(len(strings.TrimLeft(strings.ReplaceAll(integerPart, "_", ""), "0")))
	point := integerDigits

	if integerDigits == 0 {
		fraction := strings.ReplaceAll(fractionPart, "_", "")
		point = -int64(len(fraction) - len(strings.TrimLeft(fraction, "0")))
	}

	exponent += point - 1

	var builder strings.Builder
	builder.Grow(len(significant) + 24)

	if isNegative {
		builder.WriteByte('-')
	}

	builder.WriteByte(significant[0])

	if len(significant) > 1 {
		builder.WriteByte('.')
		builder.WriteString(significant[1:])
	}

	builder.WriteByte('e')
	builder.WriteString(strconv.FormatInt(exponent, 10))
	return builder.String(), false, exponent
}

// maxDiagnosedLength is the longest part of an invalid token that the diagnostics look at, so that a huge token does not cost a huge scan.
const maxDiagnosedLength = 1000

// describeBadNumber gives the reason a token is not a number. unquoted is the unquoted string that the token begins, for the suggestion to quote it.
func describeBadNumber(fullText, unquoted string) string {
	text := fullText[:min(len(fullText), maxDiagnosedLength)]

	digitRunEnd := func(index int) int {
		for index < len(text) && isDigitOrUnderscore(text[index]) {
			index++
		}

		return index
	}

	// The index after `-?\d[\d_]*`, or -1.
	startsWithDigitRun := func() int {
		start := 0

		if at(text, 0) == '-' {
			start = 1
		}

		if !isDigit(at(text, start)) {
			return -1
		}

		return digitRunEnd(start + 1)
	}

	if strings.Contains(text, "+") {
		return "a “+” sign is not allowed in a number, including in an exponent"
	}

	unsigned := strings.TrimPrefix(text, "-")

	if len(unsigned) >= 2 && unsigned[0] == '0' && (unsigned[1] == 'B' || unsigned[1] == 'O' || unsigned[1] == 'X') {
		return "a number prefix is lowercase: 0x, 0o, or 0b"
	}

	if len(unsigned) >= 2 && unsigned[0] == '0' && (unsigned[1] == 'b' || unsigned[1] == 'o' || unsigned[1] == 'x') {
		radix := unsigned[1]
		// The whole number, not the part cut at the diagnosed length, because a cut can end before the digit or the underscore that the message is about. Each check below takes linear time.
		digits := strings.TrimPrefix(fullText, "-")[2:]
		name, article, isRadixDigit := "binary", "a", isBinaryDigit

		// A lowercase hexadecimal digit is a digit in the wrong case, so the invalid digit named below is a character that is no digit in either case.
		switch radix {
		case 'x':
			name, article, isRadixDigit = "hexadecimal", "a", isHexadecimalDigitAnyCase
		case 'o':
			name, article, isRadixDigit = "octal", "an", isOctalDigit
		}

		if text[0] == '-' {
			return article + " " + name + " integer cannot have a sign, because it states a bit pattern rather than a quantity"
		}

		if digits == "" {
			return "expected " + name + " digits after “0" + string(radix) + "”"
		}

		if radix == 'x' && strings.ContainsAny(digits, "abcdef") && strings.Trim(digits, "0123456789abcdefABCDEF_") == "" {
			// The uppercase spelling is only suggested when it is valid, so a misplaced underscore is reported first, and a value outside the 64-bit range gets no example.
			uppercase := strings.ToUpper(digits)

			if skipDigits(uppercase, 0, isHexadecimalDigit) != len(uppercase) {
				return "an underscore in a number must be between two digits"
			}

			if !isValidValue("0x" + uppercase) {
				return "hexadecimal digits are uppercase"
			}

			return "hexadecimal digits are uppercase: " + abbreviate("0x"+uppercase, 40)
		}

		for _, character := range digits {
			if character != '_' && (character >= utf8.RuneSelf || !isRadixDigit(byte(character))) {
				return "invalid " + name + " digit “" + string(character) + "”"
			}
		}

		return "an underscore in a number must be between two digits"
	}

	// `-?\d[\d_]*(\.[\d_]+)?E-?[\d_]*`, the whole token. Only when the “E” is where an exponent marker goes, with digits, a `-`, or nothing after it. In `5EUR` or `10EB`, it is part of a word, so lowercasing it would not help.
	if end := startsWithDigitRun(); end >= 0 {
		mantissaEnd := end

		if at(text, end) == '.' && digitRunEnd(end+1) > end+1 {
			mantissaEnd = digitRunEnd(end + 1)
		}

		if at(text, mantissaEnd) == 'E' && strings.Trim(strings.TrimPrefix(text[mantissaEnd+1:], "-"), "0123456789_") == "" {
			return "an exponent marker is a lowercase “e”"
		}
	}

	// `-?[\d_]+\.` followed by the end or by something other than a digit or `_`.
	integerStart := 0

	if at(text, 0) == '-' {
		integerStart = 1
	}

	integerEnd := digitRunEnd(integerStart)

	if integerEnd > integerStart && at(text, integerEnd) == '.' && !isDigitOrUnderscore(at(text, integerEnd+1)) {
		return "a decimal point must be followed by a digit"
	}

	if strings.HasPrefix(text, "-.") {
		return "a number cannot begin with “.”; write a digit before it, as in -0.5"
	}

	// Digits with colons between them, as in a time of day or a port mapping.
	if isDigit(text[0]) && strings.Contains(text, ":") && !strings.Contains(text, "::") && text[len(text)-1] != ':' && strings.Trim(text, "0123456789:") == "" {
		// `H:MM`, `HH:MM`, or either with `:SS`.
		parts := strings.Split(text, ":")
		isTimeOfDay := len(parts[0]) <= 2 && len(parts) <= 3

		for _, part := range parts[1:] {
			isTimeOfDay = isTimeOfDay && len(part) == 2
		}

		if isTimeOfDay {
			return "a time of day is a string, so it must be quoted"
		}

		return "invalid number " + quote(text) + ". A value that contains “:” must be quoted, as in '" + abbreviate(text, 40) + "'"
	}

	// A value with a `:` that starts with a zero, such as the MAC address `00:1A:2B`, is not a number with a leading zero, because removing the zero would not make it valid.
	if len(unsigned) >= 2 && unsigned[0] == '0' && isDigitOrUnderscore(unsigned[1]) && !strings.Contains(text, ":") {
		// Removing the zero gives a valid number with a different meaning, so the message says what the zero usually meant.
		// A `'...'` string cannot hold a `'`, so then there is no example.
		identifier := "an identifier, such as a ZIP code, as a string"

		if !strings.Contains(unquoted, "'") {
			identifier += ": '" + abbreviate(unquoted, 40) + "'"
		}

		digits := strings.TrimLeft(fullText, "0")

		if digits == "" {
			digits = "0"
		}

		switch {
		// The octal suggestion is only for the number on its own, not for one that more text follows, as in `0412 345 678`, and only when it is in range. It is decided on the whole number, because a cut can end before a digit that is not octal or that changes the value.
		case unquoted == fullText && strings.Trim(fullText, "01234567") == "" && isValidValue("0o"+digits):
			return "leading zeros are not allowed in a decimal number. Write an octal number, such as a file mode, as " + abbreviate("0o"+digits, 40) + ", and " + identifier
		case allDigits(text):
			return "leading zeros are not allowed in a decimal number. Write " + identifier
		}

		return "leading zeros are not allowed in a decimal number"
	}

	// An underscore at either end, or next to something other than a digit. Only in a number: an underscore next to a letter other than the exponent marker is part of a word, such as `4k_video`.
	if strings.Trim(text, "0123456789-._e") == "" {
		for index := range len(text) {
			if text[index] == '_' && (index == 0 || index+1 == len(text) || !isDigit(text[index-1]) || !isDigit(text[index+1])) {
				return "an underscore in a number must be between two digits"
			}
		}
	}

	// `-?\d[\d_]*(\.[\d_]+)?e`, then the exponent.
	if end := startsWithDigitRun(); end >= 0 {
		mantissaEnd := end

		if at(text, end) == '.' && digitRunEnd(end+1) > end+1 {
			mantissaEnd = digitRunEnd(end + 1)
		}

		if at(text, mantissaEnd) == 'e' {
			exponent := text[mantissaEnd+1:]
			exponentDigits := strings.TrimPrefix(exponent, "-")

			if len(exponentDigits) >= 2 && exponentDigits[0] == '0' && (isDigit(exponentDigits[1]) || exponentDigits[1] == '_' && isDigit(at(exponentDigits, 2))) {
				return "leading zeros are not allowed in an exponent"
			}

			if exponent == "-0" {
				return "“e-0” is not allowed, because an exponent of zero has one spelling: e0"
			}

			// Only a number ends with its exponent marker. A word that ends with an “e”, such as `1byte` or `-verbose`, has no exponent.
			if exponent == "" || exponent == "-" {
				return "expected digits after the exponent marker “e”"
			}
		}
	}

	// Note: Not using strings.Cut plus strings.Contains because Count is faster for short tokens.
	if strings.Count(text, ".") > 1 {
		return "invalid number " + quote(fullText) + ". A value with several dots, such as a version number, must be quoted"
	}

	if text[0] == '-' && !isDigit(at(text, 1)) {
		rest := strings.ToLower(text[1:])

		// The whole word, as for one without the “-”, so that a word such as `-nano` or `-info` is not taken for NaN or infinity.
		if rest == "nan" {
			return "NaN is not representable. Use null for a missing value"
		}

		if rest == "inf" || rest == "infinity" {
			return quote(fullText) + " is not a value. Negative infinity is written -infinity"
		}

		return "expected a digit or “infinity” after “-”"
	}

	if strings.ContainsFunc(text, func(character rune) bool {
		return character < utf8.RuneSelf && isLetter(byte(character))
	}) {
		return "invalid number " + quote(fullText) + ". A string value must be quoted" + quotingExample(unquoted)
	}

	return "invalid number " + quote(fullText)
}

// describeUnknownWord gives the error for a word where a value should be, such as `yes`, `NULL`, or an unquoted string. text is the unquoted string that the word begins, which the suggestion quotes.
func describeUnknownWord(word, text string) string {
	// A keyword hint is only for the word on its own. In `Yes please` or `Nan Goldin`, following it would leave text behind or change the value.
	lowercase := ""

	if text == word {
		lowercase = strings.ToLower(word)
	}

	switch lowercase {
	case "true", "false", "yes", "no", "on", "off":
		return quote(word) + " is not a value. Booleans are written true and false, in lowercase"
	case "null", "nil", "none", "undefined":
		return quote(word) + " is not a value. Null is written null, in lowercase"
	case "nan":
		return "NaN is not representable. Use null for a missing value"
	case "inf", "infinity":
		return quote(word) + " is not a value. Infinity is written infinity, in lowercase"
	}

	return "unexpected " + quote(word) + ". A string value must be quoted" + quotingExample(text)
}

// quotingExample is the example in a message that a string must be quoted, which quotes text as a `'...'` string. That cannot hold a `'`, so then there is no simple example.
func quotingExample(text string) string {
	if strings.Contains(text, "'") {
		return ""
	}

	return ", as in '" + abbreviate(text, 40) + "'"
}

var characterNames = map[rune]string{
	0x061C: "ARABIC LETTER MARK",
	0x0085: "NEXT LINE",
	0x00A0: "NO-BREAK SPACE",
	0x00AD: "SOFT HYPHEN",
	0x1680: "OGHAM SPACE MARK",
	0x2000: "EN QUAD",
	0x2001: "EM QUAD",
	0x2002: "EN SPACE",
	0x2003: "EM SPACE",
	0x2004: "THREE-PER-EM SPACE",
	0x2005: "FOUR-PER-EM SPACE",
	0x2006: "SIX-PER-EM SPACE",
	0x2007: "FIGURE SPACE",
	0x2008: "PUNCTUATION SPACE",
	0x2009: "THIN SPACE",
	0x200A: "HAIR SPACE",
	0x200B: "ZERO WIDTH SPACE",
	0x200C: "ZERO WIDTH NON-JOINER",
	0x200D: "ZERO WIDTH JOINER",
	0x200E: "LEFT-TO-RIGHT MARK",
	0x200F: "RIGHT-TO-LEFT MARK",
	0x2028: "LINE SEPARATOR",
	0x2029: "PARAGRAPH SEPARATOR",
	0x202A: "LEFT-TO-RIGHT EMBEDDING",
	0x202B: "RIGHT-TO-LEFT EMBEDDING",
	0x202C: "POP DIRECTIONAL FORMATTING",
	0x202D: "LEFT-TO-RIGHT OVERRIDE",
	0x202E: "RIGHT-TO-LEFT OVERRIDE",
	0x202F: "NARROW NO-BREAK SPACE",
	0x205F: "MEDIUM MATHEMATICAL SPACE",
	0x2060: "WORD JOINER",
	0x2066: "LEFT-TO-RIGHT ISOLATE",
	0x2067: "RIGHT-TO-LEFT ISOLATE",
	0x2068: "FIRST STRONG ISOLATE",
	0x2069: "POP DIRECTIONAL ISOLATE",
	0x3000: "IDEOGRAPHIC SPACE",
	0xFEFF: "ZERO WIDTH NO-BREAK SPACE",
}

// describeCharacter describes a character for an error message, such as `“$”` or `U+00A0 (NO-BREAK SPACE; only space, tab, and line feed are whitespace)`.
func describeCharacter(character rune) string {
	if name, ok := characterNames[character]; ok {
		note := ""

		if unicode.IsSpace(character) || character == 0x200B || character == 0xFEFF {
			note = "; only space, tab, and line feed are whitespace"
		}

		return fmt.Sprintf("U+%04X (%s%s)", character, name, note)
	}

	// Default ignorable, control, format, private-use, unassigned, surrogate, and separator characters are invisible or ambiguous, so they are shown by code point.
	if character != ' ' && (isIgnorableOrOther(character) || unicode.In(character, unicode.Space)) {
		return fmt.Sprintf("U+%04X", character)
	}

	return "“" + string(character) + "”"
}

// isIgnorableOrOther reports whether a character is default ignorable or in the Unicode category Other (a control, format, private-use, surrogate, or unassigned character), as `[\p{Default_Ignorable_Code_Point}\p{Other}]` in the JS reference. Go has no table of the derived property Default_Ignorable_Code_Point, but the format characters it is derived from are in Other, so the rest of it is the variation selectors and the other default ignorable characters, such as U+3164 (HANGUL FILLER).
func isIgnorableOrOther(character rune) bool {
	return unicode.In(character, unicode.Other, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) || !unicode.In(character, unicode.Letter, unicode.Mark, unicode.Number, unicode.Punct, unicode.Symbol, unicode.Space)
}

// isBlockString reports whether a block string starts at start: a run of three or more of the same quote.
func isBlockString(source string, start int) bool {
	return at(source, start+1) == source[start] && at(source, start+2) == source[start]
}

// readString reads any string that starts at start, which is a quote: a block string, `'...'`, or `"..."`. It returns the value and the index after it.
// Note: Not using strconv.Unquote because Go string syntax has different Unicode escapes and single-quote rules.
func readString(source string, start int) (string, int, *syntaxError) {
	if isBlockString(source, start) {
		return readBlockString(source, start)
	}

	if source[start] == '\'' {
		return readLiteralString(source, start)
	}

	return readEscapedString(source, start)
}

// readLiteralString reads a `'...'` string that starts at start. The value is a substring of the source.
func readLiteralString(source string, start int) (string, int, *syntaxError) {
	// Note: Not using strings.Cut because direct IndexByte searches are faster here.
	// Stops at the first quote or line break, so that a one-line document with many strings stays linear. The quote is found first and then the line break before it, because IndexByte is much faster than IndexAny. A scan past a line break is an error, which ends the parse.
	rest := source[start+1:]
	length := strings.IndexByte(rest, '\'')

	if length < 0 || strings.IndexByte(rest[:length], '\n') >= 0 {
		return "", 0, fail(start, "unterminated string. A '...' string must end on the line it starts on; use a block string (''') for multiple lines")
	}

	return source[start+1 : start+1+length], start + 2 + length, nil
}

// readEscapedString reads a `"..."` string that starts at start. The value is a substring of the source when it has no escapes.
func readEscapedString(source string, start int) (string, int, *syntaxError) {
	chunkStart := start + 1
	var value []byte
	hasEscape := false

	for {
		length := strings.IndexAny(source[chunkStart:], "\"\\\n")

		if length < 0 {
			return "", 0, unterminatedEscapedString(start)
		}

		index := chunkStart + length
		chunk := source[chunkStart:index]

		switch source[index] {
		case '"':
			if !hasEscape {
				return chunk, index + 1, nil
			}

			value = append(value, chunk...)
			return string(value), index + 1, nil
		case '\\':
			hasEscape = true
			value = append(value, chunk...)
			var err *syntaxError
			value, chunkStart, err = appendEscape(source, index, value)

			if err != nil {
				return "", 0, err
			}
		default:
			return "", 0, unterminatedEscapedString(start)
		}
	}
}

func unterminatedEscapedString(start int) *syntaxError {
	return fail(start, "unterminated string. A \"...\" string must end on the line it starts on; use a block string (\"\"\") for multiple lines")
}

// appendEscape decodes the escape at offset, which is a backslash, and appends it to output. It returns the index after it.
func appendEscape(source string, offset int, output []byte) ([]byte, int, *syntaxError) {
	switch at(source, offset+1) {
	case '\\':
		output = append(output, '\\')
	case '"':
		output = append(output, '"')
	case 'n':
		output = append(output, '\n')
	case 't':
		output = append(output, '\t')
	case 'u':
		return appendUnicodeEscape(source, offset, output)
	case 'r':
		return nil, 0, fail(offset, "there is no \\r escape, because a carriage return cannot be represented")
	case '\'':
		return nil, 0, fail(offset, "a ' needs no escape inside \"...\"")
	case 0, '\n':
		return nil, 0, fail(offset, "a backslash must be followed by an escape character. Use \\\\ for a literal backslash, or a '...' string")
	default:
		character, _ := utf8.DecodeRuneInString(source[offset+1:])
		return nil, 0, fail(offset, "unknown escape “\\%s”. The escapes are \\\\, \\\", \\n, \\t, and \\u{…}; use a '...' string for literal backslashes", string(character))
	}

	return output, offset + 2, nil
}

func isLowerHexadecimalDigit(character byte) bool {
	return isDigit(character) || 'a' <= character && character <= 'f'
}

// appendUnicodeEscape decodes a `\u{…}` escape at offset, which is the backslash. It returns the index after it.
func appendUnicodeEscape(source string, offset int, output []byte) ([]byte, int, *syntaxError) {
	digitsStart := offset + 3
	length := 0

	// Uppercase digits too, so that the value of such an escape is checked before its spelling.
	for length < 7 && isHexadecimalDigitAnyCase(at(source, digitsStart+length)) {
		length++
	}

	if at(source, offset+2) != '{' || length == 0 || length > 6 || at(source, digitsStart+length) != '}' {
		return nil, 0, fail(offset, "%s", describeBadUnicodeEscape(source, offset+1))
	}

	hexadecimal := source[digitsStart : digitsStart+length]
	// At most six hexadecimal digits always fit in 32 bits, so there is no error.
	code, _ := strconv.ParseUint(hexadecimal, 16, 32)

	// The value is checked before the spelling, so that the uppercase and leading zeros errors never point to an escape that is not allowed either.
	switch {
	case code == 0x0D:
		return nil, 0, fail(offset, "a carriage return (U+000D) cannot be represented, so \\u{%s} is not allowed", hexadecimal)
	case 0xD800 <= code && code <= 0xDFFF:
		return nil, 0, fail(offset, "\\u{%s} is a surrogate, which is not a Unicode scalar value", hexadecimal)
	case code > unicode.MaxRune:
		return nil, 0, fail(offset, "\\u{%s} is above U+10FFFF, the largest Unicode scalar value", hexadecimal)
	case strings.ContainsAny(hexadecimal, "ABCDEF"):
		return nil, 0, fail(offset, "a Unicode escape uses lowercase hexadecimal digits")
	case length > 1 && hexadecimal[0] == '0':
		return nil, 0, fail(offset, "a Unicode escape may not have leading zeros; write \\u{%x}", code)
	}

	return utf8.AppendRune(output, rune(code)), digitsStart + length + 1, nil
}

func isHexadecimalDigitAnyCase(character byte) bool {
	return isLowerHexadecimalDigit(character) || 'A' <= character && character <= 'F'
}

// fourDigitEscape reads the code of the JSON escape `\uXXXX` from its `u`, in either case.
func fourDigitEscape(text string) (uint64, bool) {
	// Note: Not using strconv.ParseUint for validation because malformed escape digits would allocate an error.
	if len(text) < 5 || text[0] != 'u' || strings.ContainsFunc(text[1:5], func(character rune) bool {
		return character >= utf8.RuneSelf || !isHexadecimalDigitAnyCase(byte(character))
	}) {
		return 0, false
	}

	code, _ := strconv.ParseUint(text[1:5], 16, 32)
	return code, true
}

// describeBadUnicodeEscape describes a malformed `\u` escape. offset is the `u`.
func describeBadUnicodeEscape(source string, offset int) string {
	rest := source[offset:min(len(source), offset+12)]

	if code, ok := fourDigitEscape(rest); ok {
		form := "the four-digit \\" + rest[:5]

		// JSON writes a character above U+FFFF as two such escapes, a surrogate pair, which is one `\u{…}` escape here. DecodeRune gives U+FFFD for two escapes that are not a pair.
		if after, ok := strings.CutPrefix(rest[5:], "\\"); ok {
			if low, ok := fourDigitEscape(after); ok {
				if character := utf16.DecodeRune(rune(code), rune(low)); character != unicode.ReplacementChar {
					return fmt.Sprintf("%s\\%s form is not an escape. Write \\u{%x}", form, after[:5], character)
				}
			}
		}

		switch {
		case utf16.IsSurrogate(rune(code)):
			return form + " form is not an escape, and a lone surrogate is not a Unicode scalar value. Write the character it is half of as one \\u{…} escape"
		case code == 0x0D:
			return form + " form is not an escape, and a carriage return (U+000D) cannot be represented"
		}

		return fmt.Sprintf("%s form is not an escape. Write \\u{%x}", form, code)
	}

	if digits, ok := strings.CutPrefix(rest, "u{"); ok {
		length := 0

		for length < len(digits) && isHexadecimalDigitAnyCase(digits[length]) {
			length++
		}

		digits = digits[:length]

		if strings.ContainsAny(digits, "ABCDEF") {
			return "a Unicode escape uses lowercase hexadecimal digits"
		}

		if strings.HasPrefix(rest, "u{}") {
			return "a Unicode escape needs one to six hexadecimal digits"
		}

		if len(digits) >= 7 {
			return "a Unicode escape has at most six hexadecimal digits"
		}
	}

	return "a Unicode escape is written \\u{…} with one to six lowercase hexadecimal digits"
}

/*
readBlockString reads a block string, whose opening run of quotes starts at start. It returns the value and the index after the closing delimiter.

The block closes at the first line whose first non-whitespace content is a run of exactly as many quotes as the opening delimiter. The closing line's indentation is removed from every content line, except a blank one, which holds only spaces and tabs and becomes an empty line. Blank lines directly after the opening delimiter and directly before the closing one are not content.
*/
func readBlockString(source string, start int) (string, int, *syntaxError) {
	quoteCharacter := source[start]
	delimiterLength := 0

	for at(source, start+delimiterLength) == quoteCharacter {
		delimiterLength++
	}

	index := start + delimiterLength

	if index >= len(source) {
		return "", 0, fail(start, "unterminated block string")
	}

	// As in Swift, the opening delimiter is followed directly by a line break, not even by trailing whitespace.
	if source[index] != '\n' {
		return "", 0, fail(index, "a block string's opening delimiter must be followed directly by a line break, and its content starts on the next line")
	}

	index++
	contentStart := index

	// Each content line, as the offset of its start and its end.
	type line struct {
		start, end int
	}

	// A constant capacity keeps the lines of most block strings on the stack.
	lines := make([]line, 0, 16)
	var indentation string

	for {
		if index >= len(source) {
			return "", 0, fail(start, "unterminated block string%s", describeInlineClosingDelimiter(source, contentStart, source[start:start+delimiterLength]))
		}

		lineStart := index
		lineEnd := lineEnd(source, lineStart)
		contentStart := lineStart

		for contentStart < lineEnd && (source[contentStart] == ' ' || source[contentStart] == '\t') {
			contentStart++
		}

		runLength := 0

		for at(source, contentStart+runLength) == quoteCharacter {
			runLength++
		}

		if runLength == delimiterLength {
			index = contentStart + delimiterLength
			indentation = source[lineStart:contentStart]
			break
		}

		lines = append(lines, line{lineStart, lineEnd})
		index = lineEnd + 1
	}

	// Each line's content and its offset, with blank lines empty. The content is a part of the line, so each line is changed to its content in place.
	for position := range lines {
		line := &lines[position]
		text := source[line.start:line.end]

		// A blank line, which is empty or holds only spaces and tabs, may leave out the indentation, and it becomes an empty line. Swift is stricter here: there only a completely empty line may leave it out.
		if strings.Trim(text, " \t") == "" {
			line.end = line.start
		} else if strings.HasPrefix(text, indentation) {
			line.start += len(indentation)
		} else {
			return "", 0, fail(line.start, "this line does not start with the indentation of its block string's closing delimiter. Every line except a blank one must start with exactly the same spaces and tabs")
		}
	}

	first := 0

	for first < len(lines) && lines[first].start == lines[first].end {
		first++
	}

	last := len(lines)

	for last > first && lines[last-1].start == lines[last-1].end {
		last--
	}

	contents := lines[first:last]

	// An escape is never shorter than the character it makes, so the content and the line breaks between the lines are the most that the value can need.
	size := max(0, len(contents)-1)

	for _, content := range contents {
		size += content.end - content.start
	}

	value := make([]byte, 0, size)

	for position, content := range contents {
		text := source[content.start:content.end]

		if position > 0 {
			value = append(value, '\n')
		}

		if quoteCharacter == '\'' {
			value = append(value, text...)
			continue
		}

		var err *syntaxError
		value, err = appendUnescapedLine(source, text, content.start, value)

		if err != nil {
			return "", 0, err
		}
	}

	return string(value), index, nil
}

// describeInlineClosingDelimiter is the hint for an unterminated block string with a content line that ends with the delimiter, as TOML allows. Adding a closing line after it would keep the delimiter as content.
func describeInlineClosingDelimiter(source string, contentStart int, delimiter string) string {
	line := strings.Count(source[:contentStart], "\n") + 1

	for lineStart := contentStart; lineStart < len(source); lineStart = lineEnd(source, lineStart) + 1 {
		end := lineEnd(source, lineStart)
		text := strings.TrimRight(source[skipSpaces(source, lineStart):end], " \t")

		// A line of only quotes, longer than the delimiter, is content.
		if strings.HasSuffix(text, delimiter) && strings.Trim(text, delimiter[:1]) != "" {
			return ". Its closing delimiter must start a line, so move the " + delimiter + " at the end of line " + strconv.Itoa(line) + " to a new line"
		}

		line++
	}

	return ""
}

// appendUnescapedLine decodes the escapes in one line of a `"""` block, which starts at offset in the source.
func appendUnescapedLine(source, text string, offset int, output []byte) ([]byte, *syntaxError) {
	chunkStart := 0

	for {
		length := strings.IndexByte(text[chunkStart:], '\\')

		if length < 0 {
			break
		}

		index := chunkStart + length
		output = append(output, text[chunkStart:index]...)
		var end int
		var err *syntaxError
		output, end, err = appendEscape(source, offset+index, output)

		if err != nil {
			return nil, err
		}

		chunkStart = end - offset
	}

	return append(output, text[chunkStart:]...), nil
}

// parseInstant reads an instant whose text, the token between start and end in source, begins like a date, with the reason it is invalid.
// Note: Not using time.Parse because it accepts excess fractional digits, year 0000, and -00:00, which SOML rejects.
func parseInstant(source string, start, end int) (time.Time, string) {
	text := source[start:end]
	parts, ok := scanInstant(text)

	if !ok {
		// A date, a space, and a time, as TOML and Python write a date and time.
		timeEnd := end

		if at(source, end) == ' ' && isDigit(at(source, end+1)) {
			timeEnd = end + 2

			for isNumberByte(at(source, timeEnd)) {
				timeEnd++
			}
		}

		timeOfDay := ""

		if timeEnd > end {
			timeOfDay = source[end+1 : timeEnd]
		}

		// More of the instant may follow, as in `14:00:00,5Z` or `14:00:00[Europe/Oslo]`, so it may have an offset.
		isWholeValue := isValueEndAt(source, timeEnd) && !(at(source, timeEnd) == ',' && isDigit(at(source, timeEnd+1)))
		return time.Time{}, describeBadInstant(text, timeOfDay, isWholeValue, offsetAfterSpace(source, end))
	}

	reason := func(reason string) (time.Time, string) {
		return time.Time{}, "invalid instant " + abbreviate(text, 40) + ": " + reason
	}

	if len(parts.fraction) > 9 {
		return reason("a fractional second has at most nine digits")
	}

	if parts.year == 0 {
		return reason("the year must be 0001 to 9999")
	}

	if parts.month < 1 || parts.month > 12 {
		return reason("the month must be 01 to 12")
	}

	lastDay := daysInMonth(parts.year, parts.month)

	if parts.day < 1 || parts.day > lastDay {
		return reason(fmt.Sprintf("the day must be 01 to %d in that month", lastDay))
	}

	if parts.hour > 23 {
		return reason("the hour must be 00 to 23")
	}

	if parts.minute > 59 {
		return reason("the minute must be 00 to 59")
	}

	if parts.second > 59 {
		return reason("the second must be 00 to 59, and a leap second is not representable")
	}

	if parts.offsetHour > 23 {
		return reason("the offset hour must be 00 to 23")
	}

	if parts.offsetMinute > 59 {
		return reason("the offset minute must be 00 to 59")
	}

	if parts.offsetSign < 0 && parts.offsetHour == 0 && parts.offsetMinute == 0 {
		return reason("-00:00 means “offset unknown” in RFC 3339, which is not representable; use Z or +00:00")
	}

	nanosecond := 0

	for index := range 9 {
		nanosecond *= 10

		if index < len(parts.fraction) {
			nanosecond += int(parts.fraction[index] - '0')
		}
	}

	// An instant is a point in time, so the offset only moves it to UTC and is not kept.
	offset := time.Duration(parts.offsetSign) * (time.Duration(parts.offsetHour)*time.Hour + time.Duration(parts.offsetMinute)*time.Minute)
	instant := time.Date(parts.year, time.Month(parts.month), parts.day, parts.hour, parts.minute, parts.second, nanosecond, time.UTC).Add(-offset)

	if !isInstantInRange(instant) {
		return reason("in UTC it falls outside the years 0001 to 9999")
	}

	return instant, ""
}

// isInstantInRange reports whether a time is between 0001-01-01T00:00:00Z and 9999-12-31T23:59:59.999999999Z, the range of an instant.
func isInstantInRange(instant time.Time) bool {
	year := instant.UTC().Year()
	return 1 <= year && year <= 9999
}

type instantParts struct {
	year, month, day, hour, minute, second int
	fraction                               string
	offsetSign                             int
	offsetHour, offsetMinute               int
}

// scanInstant splits an instant's text into its parts, before any range check. The text must match `YYYY-MM-DDTHH:MM:SS[.fraction](Z|±HH:MM)` exactly.
func scanInstant(text string) (instantParts, bool) {
	if len(text) < 20 || text[4] != '-' || text[7] != '-' || text[10] != 'T' || text[13] != ':' || text[16] != ':' {
		return instantParts{}, false
	}

	index := 19
	parts := instantParts{}

	if text[index] == '.' {
		start := index + 1
		index = start

		for index < len(text) && isDigit(text[index]) {
			index++
		}

		if index == start {
			return instantParts{}, false
		}

		parts.fraction = text[start:index]
	}

	rest := text[index:]

	switch {
	case rest == "Z":
	case len(rest) == 6 && (rest[0] == '+' || rest[0] == '-') && rest[3] == ':' && allDigits(rest[1:3]) && allDigits(rest[4:6]):
		parts.offsetSign = 1

		if rest[0] == '-' {
			parts.offsetSign = -1
		}

		parts.offsetHour = number(rest[1:3])
		parts.offsetMinute = number(rest[4:6])
	default:
		return instantParts{}, false
	}

	for _, field := range []string{text[0:4], text[5:7], text[8:10], text[11:13], text[14:16], text[17:19]} {
		if !allDigits(field) {
			return instantParts{}, false
		}
	}

	parts.year = number(text[0:4])
	parts.month = number(text[5:7])
	parts.day = number(text[8:10])
	parts.hour = number(text[11:13])
	parts.minute = number(text[14:16])
	parts.second = number(text[17:19])
	return parts, true
}

// number is the value of a short run of ASCII digits.
// Note: Not using strconv.Atoi because it repeats validation and is slower for these two- and four-digit fields.
func number(digits string) int {
	value := 0

	for index := range len(digits) {
		value = value*10 + int(digits[index]-'0')
	}

	return value
}

// instantForm is how an instant is written, for the messages about an invalid one.
const instantForm = "An instant is written as 2026-09-19T14:00:00Z, with an optional fraction of up to nine digits and an offset of Z or ±HH:MM"

// offsetAfterSpace gives an offset on its own after spaces at index in source, as in `2026-09-19T14:00:00 Z`, which belongs directly after the time, or an empty string.
func offsetAfterSpace(source string, index int) string {
	start := skipSpaces(source, index)

	if start == index {
		return ""
	}

	length := 0

	switch sign := at(source, start); {
	case sign == 'Z':
		length = 1
	case (sign == '+' || sign == '-') && isDigit(at(source, start+1)) && isDigit(at(source, start+2)) && at(source, start+3) == ':' && isDigit(at(source, start+4)) && isDigit(at(source, start+5)):
		length = 6
	}

	if length == 0 || !isValueEndAt(source, start+length) {
		return ""
	}

	return source[start : start+length]
}

// describeBadInstant gives the reason that text, which begins like a date, is not an instant. timeOfDay is the token after a space that follows text, or an empty string. isWholeValue is whether nothing that may be part of the instant follows text. offset is an offset after a space that follows text, or an empty string.
func describeBadInstant(text, timeOfDay string, isWholeValue bool, offset string) string {
	if len(text) > maxDiagnosedLength {
		return "invalid instant " + quote(text) + ". " + instantForm
	}

	if len(text) == 10 {
		if len(timeOfDay) >= 5 && allDigits(timeOfDay[:2]) && timeOfDay[2] == ':' && allDigits(timeOfDay[3:5]) {
			return describeSpaceSeparatedInstant(text, timeOfDay, isWholeValue)
		}

		message := text + " is a date, not an instant. Write a date as a string, as in '" + text + "'"

		// The instant it could be is only shown when the date exists, so that the example is valid.
		if year, month, day := number(text[0:4]), number(text[5:7]), number(text[8:10]); year >= 1 && 1 <= month && month <= 12 && 1 <= day && day <= daysInMonth(year, month) {
			message += ". An instant needs a time and an offset, as in " + text + "T00:00:00Z"
		}

		return message
	}

	if len(text) > 10 && text[10] == 't' {
		return "the date and time separator in an instant is an uppercase “T”"
	}

	if strings.HasSuffix(text, "z") {
		return "the UTC offset in an instant is an uppercase “Z”"
	}

	if length := len(text); length >= 5 && (text[length-5] == '+' || text[length-5] == '-') && allDigits(text[length-4:]) {
		return "an instant's offset is written with a colon, as in +07:00"
	}

	if !isLocalDateTime(text) {
		return "invalid instant " + quote(text) + ". " + instantForm
	}

	if offset != "" {
		// The instant has the offset it was meant in, so it is not a local time to write as a string.
		reason := "an instant's offset follows its time directly, without a space"

		if !isValidValue(text + offset) {
			return reason
		}

		return reason + ", as in " + abbreviate(text+offset, 40)
	}

	if !isWholeValue {
		return "an instant needs an offset: Z or ±HH:MM"
	}

	return "an instant needs an offset: Z or ±HH:MM. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '" + abbreviate(text, 40) + "', or add the offset it was meant in"
}

// isLocalDateTime reports whether text is a date and a time with an optional fraction and nothing after it, as an instant without its offset.
func isLocalDateTime(text string) bool {
	if len(text) < 19 || !hasDatePrefix(text) || text[10] != 'T' || !allDigits(text[11:13]) || text[13] != ':' || !allDigits(text[14:16]) || text[16] != ':' || !allDigits(text[17:19]) {
		return false
	}

	rest := text[19:]
	return rest == "" || len(rest) > 1 && rest[0] == '.' && allDigits(rest[1:])
}

// describeSpaceSeparatedInstant gives the reason for a date and a time with a space between them, where an instant has a “T”. Following the example must not turn a local time into UTC silently, so a time without an offset is described as what it is. An instant is only shown when it is valid, so a date that does not exist, a time out of range, or an instant outside the years 0001 to 9999 in UTC gets the general form instead.
func describeSpaceSeparatedInstant(date, timeOfDay string, isWholeValue bool) string {
	const separator = "the date and time separator in an instant is an uppercase “T”, not a space"
	instant := date + "T" + timeOfDay

	switch {
	case len(instant) > maxDiagnosedLength:
		return separator + ". " + instantForm
	case isValidValue(instant):
		return separator + ", as in " + abbreviate(instant, 40)
	case isWholeValue && isLocalDateTime(instant) && isValidValue(instant+"Z"):
		return separator + ", and an instant needs the offset it was meant in, as in " + abbreviate(instant, 40) + "Z for UTC. A date and time without an offset is a local time, which is not an instant, so write it as a string, as in '" + abbreviate(date+" "+timeOfDay, 40) + "'"
	}

	return separator + ". " + instantForm
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func daysInMonth(year, month int) int {
	switch month {
	case 2:
		if isLeapYear(year) {
			return 29
		}

		return 28
	case 4, 6, 9, 11:
		return 30
	}

	return 31
}

// durationUnits are the units of a duration, from largest to smallest, with their length.
var durationUnits = []struct {
	name   string
	length uint64
}{
	{"h", uint64(time.Hour)},
	{"m", uint64(time.Minute)},
	{"s", uint64(time.Second)},
	{"ms", uint64(time.Millisecond)},
	{"us", uint64(time.Microsecond)},
	{"ns", uint64(time.Nanosecond)},
}

// isDurationUnit reports whether a word is one of the units of a duration, in its exact case.
func isDurationUnit(word string) bool {
	for _, durationUnit := range durationUnits {
		if durationUnit.name == word {
			return true
		}
	}

	return false
}

/*
parseDuration reads a duration in SOML syntax, with the reason it is invalid.

Every part is read before the value is checked, so that an error names the part it is about.

Note: Not using time.ParseDuration because it accepts unordered and repeated units and truncates fractional nanoseconds, which SOML rejects.
*/
func parseDuration(text string) (time.Duration, string) {
	failure := func(reason string) (time.Duration, string) {
		return 0, "invalid duration " + abbreviate(text, 40) + ": " + reason
	}

	isNegative := at(text, 0) == '-'
	index := 0

	if isNegative {
		index = 1
	}

	if index == len(text) {
		return failure("a duration needs at least one part, as in 30s")
	}

	previousRank := -1

	// The magnitude, which saturates, because a value past the int64 range is only reported as out of range.
	var total uint64

	// The fraction of the last part, and that part's unit length. Only the last part may have one, so the value checks wait until every part is read.
	var fraction string
	var unitLength uint64

	for index < len(text) {
		digitsEnd := skipIntegerPart(text, index)
		next := at(text, digitsEnd)

		if digitsEnd == index {
			if text[index] == '-' && isDigit(at(text, index+1)) {
				return failure("only the whole duration takes a sign, as in -1h30m")
			}

			if text[index] == '+' {
				return failure("a “+” sign is not allowed")
			}

			return failure("expected a number at “" + abbreviate(text[index:], 10) + "”")
		}

		if text[index] == '0' && (isDigit(next) || next == '_') {
			return failure("leading zeros are not allowed")
		}

		if next == '_' {
			return failure("an underscore must be between two digits")
		}

		fraction = ""
		unitStart := digitsEnd

		if next == '.' {
			unitStart = skipDigits(text, digitsEnd+1, isDigit)

			if unitStart == digitsEnd+1 {
				return failure("a “.” must be followed by a digit")
			}

			if at(text, unitStart) == '_' {
				return failure("an underscore must be between two digits")
			}

			fraction = text[digitsEnd+1 : unitStart]
		}

		unitEnd := unitStart

		for isLetter(at(text, unitEnd)) {
			unitEnd++
		}

		unit := text[unitStart:unitEnd]
		rank := -1

		for candidate, durationUnit := range durationUnits {
			if durationUnit.name == unit {
				rank = candidate
			}
		}

		if rank < 0 {
			return failure(describeBadUnit(unit, text))
		}

		if rank <= previousRank {
			return failure("the units are in the order h, m, s, ms, us, ns, and each appears at most once")
		}

		if next == '.' && isDigit(at(text, unitEnd)) {
			return failure("only the last part may have a fraction")
		}

		unitLength = durationUnits[rank].length
		total = saturatingAdd(total, saturatingMultiply(saturatingDigits(text[index:digitsEnd]), unitLength))
		index = unitEnd
		previousRank = rank
	}

	// Trailing zeros change nothing, and leaving them out keeps the arithmetic small however many there are.
	fraction = strings.TrimRight(strings.ReplaceAll(fraction, "_", ""), "0")

	// No unit has more than 13 factors of 2 or of 5, so more than 13 significant fraction digits is never a whole number of nanoseconds.
	if len(fraction) > 13 {
		return failure("it is not a whole number of nanoseconds")
	}

	if fraction != "" {
		scale := uint64(1)

		for range len(fraction) {
			scale *= 10
		}

		// The product can pass 2^64, but its high word is always below the scale, so the division cannot overflow.
		high, low := bits.Mul64(saturatingDigits(fraction), unitLength)
		quotient, remainder := bits.Div64(high, low, scale)

		if remainder != 0 {
			return failure("it is not a whole number of nanoseconds")
		}

		total = saturatingAdd(total, quotient)
	}

	if isNegative && total == 0 {
		return failure("“-” is not allowed before zero, because zero has one spelling: 0s")
	}

	limit := uint64(math.MaxInt64)

	if isNegative {
		limit++
	}

	if total > limit {
		return failure("it is outside the 64-bit range of nanoseconds, about 292 years either way")
	}

	if isNegative {
		// For a magnitude of 2^63, the negation wraps to 2^63, which converts to the most negative duration.
		return time.Duration(-total), ""
	}

	return time.Duration(total), ""
}

// saturatingDigits is the value of decimal digits with optional underscores, saturating at the largest uint64.
func saturatingDigits(digits string) uint64 {
	var value uint64

	for index := range len(digits) {
		if digits[index] != '_' {
			value = saturatingAdd(saturatingMultiply(value, 10), uint64(digits[index]-'0'))
		}
	}

	return value
}

func saturatingAdd(left, right uint64) uint64 {
	sum, carry := bits.Add64(left, right, 0)

	if carry != 0 {
		return math.MaxUint64
	}

	return sum
}

func saturatingMultiply(left, right uint64) uint64 {
	high, low := bits.Mul64(left, right)

	if high != 0 {
		return math.MaxUint64
	}

	return low
}

// isYearUnit reports whether a word is a year unit, as in `1y` or `2years`, in any case.
// Note: Not using strings.EqualFold because checking every spelling is slower for lowercase input.
func isYearUnit(word string) bool {
	switch strings.ToLower(word) {
	case "y", "yr", "yrs", "year", "years":
		return true
	}

	return false
}

// describeBadUnit explains a unit that is not one, in the duration text.
func describeBadUnit(unit, text string) string {
	if isYearUnit(unit) {
		return "there is no year unit, because a year is not a fixed length. Write 8760h for a fixed 365 days"
	}

	switch unit {
	case "":
		return "every number needs a unit: h, m, s, ms, us, or ns"
	case "d", "day", "days":
		return "there is no day unit, because a day is not a fixed length. Write 24h for a fixed 24 hours"
	case "w", "week", "weeks":
		return "there is no week unit, because a day is not a fixed length. Write 168h for a fixed 168 hours"
	case "M":
		// A number with `M` alone is more often a size, as in `memory: 512M`, and `512m` would read as minutes.
		if digits := strings.TrimSuffix(text, "M"); isDigit(at(digits, 0)) && strings.Trim(digits, "0123456789_") == "" {
			return "there is no month unit, because a month is not a fixed length. A size, such as 512M, is a string: '" + abbreviate(text, 40) + "'"
		}

		return "there is no month unit, because a month is not a fixed length"
	}

	for _, durationUnit := range durationUnits {
		if strings.EqualFold(durationUnit.name, unit) {
			return "the units are lowercase: " + strings.ToLower(unit)
		}
	}

	return "“" + abbreviate(unit, 10) + "” is not a unit. The units are h, m, s, ms, us, and ns, and a string must be quoted"
}
