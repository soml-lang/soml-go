package soml

// Edge cases that mutation testing found untested: each test fails on a small change to the code that the rest of the suite does not notice.

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestFloatThatUnderflowsWithANineIsAnError(t *testing.T) {
	var value any
	err := Unmarshal([]byte("a: 9e-400\n"), &value)

	if err == nil || !strings.Contains(err.Error(), "too small to be told apart from zero") {
		t.Fatalf("got %v", err)
	}
}

func TestDecimalPointBeforeAnUppercaseExponentNeedsADigit(t *testing.T) {
	var value any
	err := Unmarshal([]byte("a: 1.E5\n"), &value)

	if err == nil || !strings.Contains(err.Error(), "a decimal point must be followed by a digit") {
		t.Fatalf("got %v", err)
	}
}

func TestDurationWithAZeroBeforeAnUnderscoreIsALeadingZero(t *testing.T) {
	var value any
	err := Unmarshal([]byte("a: 0_5s\n"), &value)

	if err == nil || !strings.Contains(err.Error(), "leading zeros are not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestDuplicateKeyInALargeObject(t *testing.T) {
	var builder strings.Builder

	for index := range 20 {
		fmt.Fprintf(&builder, "k%d: %d\n", index, index)
	}

	// The first key is in the index when it is built, and the last one is added to it afterwards.
	for _, duplicate := range []string{"k0", "k19"} {
		var value any
		err := Unmarshal([]byte(builder.String()+duplicate+": 1\n"), &value)

		if err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Errorf("%s: got %v", duplicate, err)
		}
	}
}

func TestDuplicateKeyInLargeObjectsAtOneDepth(t *testing.T) {
	object := func(count int) string {
		var builder strings.Builder
		builder.WriteString("{\n")

		for index := range count {
			fmt.Fprintf(&builder, "k%d: %d\n", index, index)
		}

		return builder.String()
	}

	// Each object reuses the key index of the one before it at its depth, which must not keep its keys. The first object is too large for its index to be reused.
	for _, count := range []int{20, 300} {
		valid := "[\n" + object(count) + "}\n" + object(20) + "}\n]\n"
		var value any

		if err := Unmarshal([]byte(valid), &value); err != nil {
			t.Errorf("%d: got %v", count, err)
		}

		duplicate := "[\n" + object(count) + "}\n" + object(20) + "k3: 1\n}\n]\n"

		if err := Unmarshal([]byte(duplicate), &value); err == nil || !strings.Contains(err.Error(), "duplicate key k3") {
			t.Errorf("%d: got %v", count, err)
		}
	}
}

func TestDecodeIntoASliceOfZeroSizeElements(t *testing.T) {
	var value struct {
		A []struct{} `soml:"a"`
	}

	if err := Unmarshal([]byte("a: [{}, {}]\n"), &value); err != nil || len(value.A) != 2 {
		t.Fatalf("got %v, %v", value.A, err)
	}
}

func TestDecodeIntoAnArrayResetsEachElement(t *testing.T) {
	type pair struct {
		X int `soml:"x"`
		Y int `soml:"y"`
	}

	var value struct {
		A [1]pair `soml:"a"`
	}

	value.A[0].Y = 5

	if err := Unmarshal([]byte("a: [{x: 1}]\n"), &value); err != nil || value.A[0] != (pair{X: 1}) {
		t.Fatalf("got %+v, %v", value.A, err)
	}
}

func TestDecodeZeroIntoFloat32(t *testing.T) {
	value, err := decodeMemberInto[float32]("0.0")

	if err != nil || value != 0 {
		t.Fatalf("got %v, %v", value, err)
	}
}

// Negative zero is the same value as zero, also when it is rounded from its digits for a float32.
func TestDecodeNegativeZeroIntoFloat32(t *testing.T) {
	for _, text := range []string{"-0.0", "-0.0e5", "-0.000_0", "-0." + strings.Repeat("0", 40)} {
		value, err := decodeMemberInto[float32](text)

		if err != nil || value != 0 || math.Signbit(float64(value)) {
			t.Errorf("%s: got %v, %v", text, value, err)
		}
	}
}

func TestErrorTextShortensAPathOfTwentyOneSteps(t *testing.T) {
	path := make([]any, 21)

	for index := range path {
		path[index] = "k"
	}

	somlError := &Error{Message: "problem", Path: path}

	if text := somlError.Error(); text != "soml: k.k.k.k.k.k.k.k.k.k.….k.k.k.k.k: problem" {
		t.Fatalf("got %q", text)
	}
}

func TestPositionOfANegativeOffset(t *testing.T) {
	document := mustParse(t, "a: 1\n")

	if line, column := document.Position(-3); line != 1 || column != 1 {
		t.Fatalf("got %d:%d", line, column)
	}
}

type embeddedCycleA struct {
	*embeddedCycleB
	A int
}

type embeddedCycleB struct {
	*embeddedCycleA
	B int
}

func TestEmbeddedPointerCycle(t *testing.T) {
	done := make(chan string, 1)

	go func() {
		text, err := Marshal(embeddedCycleA{A: 1})
		done <- fmt.Sprintf("%q %v", text, err)
	}()

	select {
	case result := <-done:
		if result != `"A: 1\n" <nil>` {
			t.Fatalf("got %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Marshal did not finish")
	}
}

func TestStyleOfAddedKeysAndStrings(t *testing.T) {
	if style := NewKey("name").Style(); style != StyleBare {
		t.Errorf("bare key: got %v", style)
	}

	node, err := NewNode("it's")

	if err != nil {
		t.Fatal(err)
	}

	if style := node.(*Scalar).Style(); style != StyleEscaped {
		t.Errorf("string with a quote: got %v", style)
	}
}

func TestSetEmptyCollectionsInAnIndentedObject(t *testing.T) {
	checkChange(t, "a: {\n\tb: 1,\n}\n", func(document *Document) error {
		if err := document.Set(map[string]any{}, "a", "c"); err != nil {
			return err
		}

		return document.Set([]int{}, "a", "d")
	}, "a: {\n\tb: 1,\n\tc: {}\n\td: []\n}\n")
}

func TestGetANegativeIndex(t *testing.T) {
	document := mustParse(t, "a: [1]\n")

	if node, ok := document.Get("a", -1); ok {
		t.Fatalf("got %v", node)
	}
}

func TestSetThroughTheIndexPastTheEndOfAnArray(t *testing.T) {
	document := mustParse(t, "a: [1]\n")
	err := document.Set(5, "a", 2, "b")

	if err == nil || err.Error() != "soml: cannot edit a[2].b, because the array at a has 1 item. Add an item at index 1" || document.String() != "a: [1]\n" {
		t.Fatalf("got %v, %q", err, document.String())
	}

	// The index equal to the length makes a new item, but an index below it names an item of an array that is not there.
	err = document.Set(5, "a", 1, "b", 0)

	if err == nil || err.Error() != "soml: cannot edit a[1].b[0], because a[1].b does not exist, so it has no index 0" || document.String() != "a: [1]\n" {
		t.Fatalf("got %v, %q", err, document.String())
	}

	checkChange(t, "a: [1]\n", set(5, "a", 1, "b"), "a: [1, {b: 5}]\n")
}

func TestFormatRejectsACommaAtTheStartOfALine(t *testing.T) {
	for _, text := range []string{"[1\n,\n2]", "{a: 1\n, b: 2}", "[\n\t1\n\t/* a */ /* c */ , 2\n]\n"} {
		if _, err := Format([]byte(text)); err == nil {
			t.Errorf("Format(%q) gave no error", text)
		}
	}
}

func TestFormatRemovesABlankLineBeforeTheFirstCommentInBrackets(t *testing.T) {
	if text := mustFormat(t, "a: [\n\n\t# c\n\t1,\n]\n"); text != "a: [\n\t# c\n\t1\n]\n" {
		t.Fatalf("got %q", text)
	}
}

func TestFormatBlockCommentBeforeTheFirstItemStaysInFrontOfIt(t *testing.T) {
	if text := mustFormat(t, "a: [/* a */ 1\n]\n"); text != "a: [\n\t/* a */ 1\n]\n" {
		t.Fatalf("got %q", text)
	}
}
