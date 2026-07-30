package money

import (
	"encoding/json"
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
