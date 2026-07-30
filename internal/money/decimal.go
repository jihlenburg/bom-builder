// Package money implements exact fixed-point decimal arithmetic for prices.
package money

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	// Scale is the number of integer units used to represent one currency unit.
	Scale  int64 = 1_000_000
	places       = 6
)

// Decimal stores a non-negative decimal value with six fractional places.
// JSON uses a string so consumers never receive a binary floating-point value.
type Decimal int64

// Parse accepts provider price text in common dot- or comma-decimal formats.
func Parse(input string) (Decimal, error) {
	normalized, err := normalize(input)
	if err != nil {
		return 0, err
	}
	whole, fraction, _ := strings.Cut(normalized, ".")
	wholeValue, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse whole price: %w", err)
	}
	if wholeValue < 0 {
		return 0, errors.New("money cannot be negative")
	}

	roundUp := false
	if len(fraction) > places {
		roundUp = fraction[places] >= '5'
		fraction = fraction[:places]
	}
	fraction += strings.Repeat("0", places-len(fraction))
	fractionValue := int64(0)
	if fraction != "" {
		fractionValue, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse fractional price: %w", err)
		}
	}
	if wholeValue > math.MaxInt64/Scale {
		return 0, errors.New("money value is too large")
	}
	scaled := wholeValue*Scale + fractionValue
	if roundUp {
		if scaled == math.MaxInt64 {
			return 0, errors.New("money value is too large")
		}
		scaled++
	}
	return Decimal(scaled), nil
}

// FromMicros constructs a Decimal from its exact scaled representation.
func FromMicros(value int64) (Decimal, error) {
	if value < 0 {
		return 0, errors.New("money cannot be negative")
	}
	return Decimal(value), nil
}

// Micros returns the exact scaled integer representation.
func (decimal Decimal) Micros() int64 {
	return int64(decimal)
}

// String renders exactly six fractional places.
func (decimal Decimal) String() string {
	value := int64(decimal)
	whole := value / Scale
	fraction := value % Scale
	return fmt.Sprintf("%d.%06d", whole, fraction)
}

// MarshalJSON encodes a decimal as a quoted exact value.
func (decimal Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(decimal.String())
}

// UnmarshalJSON accepts a quoted decimal and rejects JSON numbers.
func (decimal *Decimal) UnmarshalJSON(data []byte) error {
	if decimal == nil {
		return errors.New("cannot unmarshal money into nil receiver")
	}
	if len(data) == 0 || data[0] != '"' {
		return errors.New("money must be a decimal string")
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*decimal = parsed
	return nil
}

// Add returns the exact sum.
func (decimal Decimal) Add(other Decimal) (Decimal, error) {
	left, right := int64(decimal), int64(other)
	if right > math.MaxInt64-left {
		return 0, errors.New("money addition overflow")
	}
	return Decimal(left + right), nil
}

// MulInt multiplies a price by a non-negative quantity.
func (decimal Decimal) MulInt(quantity int) (Decimal, error) {
	if quantity < 0 {
		return 0, errors.New("money multiplier cannot be negative")
	}
	if quantity == 0 || decimal == 0 {
		return 0, nil
	}
	value := int64(decimal)
	if int64(quantity) > math.MaxInt64/value {
		return 0, errors.New("money multiplication overflow")
	}
	return Decimal(value * int64(quantity)), nil
}

// DivInt divides by a positive integer and rounds half up.
func (decimal Decimal) DivInt(divisor int) (Decimal, error) {
	if divisor <= 0 {
		return 0, errors.New("money divisor must be positive")
	}
	value := int64(decimal)
	quotient := value / int64(divisor)
	remainder := value % int64(divisor)
	if remainder >= (int64(divisor)+1)/2 {
		quotient++
	}
	return Decimal(quotient), nil
}

func normalize(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", errors.New("money value is empty")
	}

	var numeric bytes.Buffer
	for _, character := range trimmed {
		switch {
		case unicode.IsDigit(character):
			numeric.WriteRune(character)
		case character == '.' || character == ',':
			numeric.WriteRune(character)
		case character == '-' || character == '+':
			if numeric.Len() != 0 {
				return "", errors.New("invalid sign in money value")
			}
			numeric.WriteRune(character)
		case unicode.IsSpace(character), unicode.IsLetter(character):
			continue
		default:
			// Currency symbols and grouping apostrophes are ignored.
			continue
		}
	}
	value := numeric.String()
	if value == "" || value == "+" || value == "-" {
		return "", fmt.Errorf("money value %q contains no number", input)
	}
	if strings.HasPrefix(value, "-") {
		return "", errors.New("money cannot be negative")
	}
	value = strings.TrimPrefix(value, "+")

	lastDot := strings.LastIndexByte(value, '.')
	lastComma := strings.LastIndexByte(value, ',')
	switch {
	case lastDot >= 0 && lastComma >= 0:
		decimalSeparator := byte('.')
		if lastComma > lastDot {
			decimalSeparator = ','
		}
		var result strings.Builder
		for index := range len(value) {
			character := value[index]
			if character == '.' || character == ',' {
				if character == decimalSeparator && index == max(lastDot, lastComma) {
					result.WriteByte('.')
				}
				continue
			}
			result.WriteByte(character)
		}
		value = result.String()
	case lastComma >= 0:
		if strings.Count(value, ",") > 1 {
			return "", fmt.Errorf("invalid money value %q", input)
		}
		value = strings.ReplaceAll(value[:lastComma], ",", "") + "." + value[lastComma+1:]
	case lastDot >= 0:
		if strings.Count(value, ".") > 1 {
			return "", fmt.Errorf("invalid money value %q", input)
		}
		value = strings.ReplaceAll(value[:lastDot], ".", "") + "." + value[lastDot+1:]
	}
	if strings.Count(value, ".") > 1 {
		return "", fmt.Errorf("invalid money value %q", input)
	}
	whole, fraction, found := strings.Cut(value, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || found && !allDigits(fraction) {
		return "", fmt.Errorf("invalid money value %q", input)
	}
	if found {
		return whole + "." + fraction, nil
	}
	return whole, nil
}

func allDigits(value string) bool {
	if value == "" {
		return true
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
