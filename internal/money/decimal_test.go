// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package money

import (
	"encoding/json"
	"math"
	"testing"
)

func TestParseProviderPriceFormatsExactly(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"0,045 €":    "0.045000",
		"1.234,56 €": "1234.560000",
		"$0.045":     "0.045000",
		"$1,234.56":  "1234.560000",
		"12,34 EUR":  "12.340000",
		"0.1234567":  "0.123457",
	}
	for input, expected := range tests {
		parsed, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", input, err)
		}
		if parsed.String() != expected {
			t.Fatalf("Parse(%q) = %s, want %s", input, parsed, expected)
		}
	}
}

func TestDecimalJSONIsAString(t *testing.T) {
	t.Parallel()
	decimal, err := Parse("90")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(decimal)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"90.000000"` {
		t.Fatalf("encoded = %s", encoded)
	}

	var decoded Decimal
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != decimal {
		t.Fatalf("decoded = %s, want %s", decoded, decimal)
	}
	if err := json.Unmarshal([]byte(`90.0`), &decoded); err == nil {
		t.Fatal("JSON number should be rejected")
	}
}

func TestDecimalArithmetic(t *testing.T) {
	t.Parallel()
	unit, _ := Parse("0.09")
	extended, err := unit.MulInt(1000)
	if err != nil {
		t.Fatal(err)
	}
	if extended.String() != "90.000000" {
		t.Fatalf("extended = %s", extended)
	}
	average, err := extended.DivInt(1000)
	if err != nil {
		t.Fatal(err)
	}
	if average != unit {
		t.Fatalf("average = %s, want %s", average, unit)
	}
}

func TestParseRejectsInvalidAndNegativeValues(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "no price", "-1.00", "1.2.3"} {
		if _, err := Parse(input); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", input)
		}
	}
}

func TestParseRejectsFractionOverflow(t *testing.T) {
	t.Parallel()
	// The scaled value must never wrap past MaxInt64 into a negative
	// Decimal: the fractional micros count toward the bound too.
	for _, input := range []string{
		"9223372036854.999999",
		"9223372036854.775808",
		"9223372036854.7758075", // round-up lands exactly one past MaxInt64
		"9223372036855",
	} {
		parsed, err := Parse(input)
		if err == nil {
			t.Fatalf("Parse(%q) = %s, want overflow error", input, parsed)
		}
	}
	// The exact top of the range must still parse.
	parsed, err := Parse("9223372036854.775807")
	if err != nil {
		t.Fatalf("Parse(max): %v", err)
	}
	if parsed.Micros() != math.MaxInt64 {
		t.Fatalf("Parse(max) = %d micros, want MaxInt64", parsed.Micros())
	}
}

func TestParseRejectsDigitFreeInput(t *testing.T) {
	t.Parallel()
	// A separator with no digits must be a loud failure, never a zero
	// price: a zero unit price silently understates the whole BOM.
	for _, input := range []string{".", ",", "€.", "$,", "EUR"} {
		parsed, err := Parse(input)
		if err == nil {
			t.Fatalf("Parse(%q) = %s, want error", input, parsed)
		}
	}
}

func TestParseRejectsTextEmbeddedBetweenDigits(t *testing.T) {
	t.Parallel()
	// Letters between digit runs mean the input is not a single price
	// ("1e5", "2 for 1.50"); silently concatenating the digits produces a
	// plausible but wrong number. Leading or trailing currency words stay
	// accepted.
	for _, input := range []string{"1e5", "1E5", "2 for 1.50", "1 pc 2.50"} {
		parsed, err := Parse(input)
		if err == nil {
			t.Fatalf("Parse(%q) = %s, want error", input, parsed)
		}
	}
	accepted := map[string]string{
		"EUR 12.34": "12.340000",
		"12,34 EUR": "12.340000",
		"USD 5":     "5.000000",
		"1.50 USD":  "1.500000",
	}
	for input, expected := range accepted {
		parsed, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", input, err)
		}
		if parsed.String() != expected {
			t.Fatalf("Parse(%q) = %s, want %s", input, parsed, expected)
		}
	}
}

func TestParseRejectsAmbiguousThousandsComma(t *testing.T) {
	t.Parallel()
	// "<digits>,ddd" with a lone comma reads as US thousands ("$1,234" =
	// 1234) or EU decimals ("1,234" EUR = 1.234) with nothing to break the
	// tie. Failed normalization must be explicit, not a silent 1000x pick.
	for _, input := range []string{"$1,234", "1,234", "12,345 €"} {
		parsed, err := Parse(input)
		if err == nil {
			t.Fatalf("Parse(%q) = %s, want ambiguity error", input, parsed)
		}
	}
	// Unambiguous comma forms keep parsing: a "0," whole part cannot be
	// grouping, a second separator disambiguates, and 1-2 or 4+ fraction
	// digits rule out grouping.
	accepted := map[string]string{
		"0,045 €":  "0.045000",
		"1.234,56": "1234.560000",
		"1,234.56": "1234.560000",
		"12,34":    "12.340000",
		"1,2345":   "1.234500",
	}
	for input, expected := range accepted {
		parsed, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", input, err)
		}
		if parsed.String() != expected {
			t.Fatalf("Parse(%q) = %s, want %s", input, parsed, expected)
		}
	}
}

func TestStringRendersNegativeValuesDefensively(t *testing.T) {
	t.Parallel()
	// Decimal is documented non-negative, but the type is an int64 alias
	// anyone can construct directly; String must not emit garbage like
	// "-9223372036854.-551617" into JSON if that invariant is broken.
	tests := map[Decimal]string{
		Decimal(-5):            "-0.000005",
		Decimal(-1_500_000):    "-1.500000",
		Decimal(math.MinInt64): "-9223372036854.775808",
		Decimal(math.MaxInt64): "9223372036854.775807",
		Decimal(0):             "0.000000",
	}
	for value, expected := range tests {
		if rendered := value.String(); rendered != expected {
			t.Fatalf("Decimal(%d).String() = %q, want %q", int64(value), rendered, expected)
		}
	}
}

func TestFromMicrosRejectsNegative(t *testing.T) {
	t.Parallel()
	if _, err := FromMicros(-1); err == nil {
		t.Fatal("FromMicros(-1) unexpectedly succeeded")
	}
}

func TestArithmeticOverflowIsAnError(t *testing.T) {
	t.Parallel()
	top := Decimal(math.MaxInt64)
	if _, err := top.Add(Decimal(1)); err == nil {
		t.Fatal("Add past MaxInt64 unexpectedly succeeded")
	}
	if _, err := Decimal(math.MaxInt64/2 + 1).MulInt(2); err == nil {
		t.Fatal("MulInt past MaxInt64 unexpectedly succeeded")
	}
}

func TestDivIntRoundsHalfUp(t *testing.T) {
	t.Parallel()
	value, err := Parse("2.000001")
	if err != nil {
		t.Fatal(err)
	}
	half, err := value.DivInt(2)
	if err != nil {
		t.Fatal(err)
	}
	if half.String() != "1.000001" {
		t.Fatalf("2.000001 / 2 = %s, want 1.000001 (half up)", half)
	}
	third, err := Decimal(1).DivInt(3)
	if err != nil {
		t.Fatal(err)
	}
	if third != 0 {
		t.Fatalf("0.000001 / 3 = %s, want 0.000000", third)
	}
}
