// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package fx provides dated foreign-exchange reference quotes and exact
// conversion. Conversions are computed on integer micro-units with one
// half-to-even rounding at the sixth decimal place, and every failure —
// unknown currency, invalid rate, overflow — is explicit. Nothing in this
// package guesses: a conversion either succeeds against a dated quote
// table or returns an error.
package fx

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/money"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// Table is one dated set of reference quotes against a single base
// currency (ECB publishes EUR-based rates).
type Table struct {
	source string
	date   string
	base   string
	rates  map[string]money.Decimal
}

// NewTable validates and constructs a quote table. Rates express one base
// unit in the quoted currency and must be positive.
func NewTable(
	source, date, base string,
	rates map[string]money.Decimal,
) (Table, error) {
	source = strings.TrimSpace(source)
	date = strings.TrimSpace(date)
	base = strings.ToUpper(strings.TrimSpace(base))
	if source == "" || date == "" {
		return Table{}, fmt.Errorf("quote source and date are required")
	}
	if !currencyPattern.MatchString(base) {
		return Table{}, fmt.Errorf("base currency %q is invalid", base)
	}
	validated := make(map[string]money.Decimal, len(rates))
	for currency, rate := range rates {
		currency = strings.ToUpper(strings.TrimSpace(currency))
		if !currencyPattern.MatchString(currency) {
			return Table{}, fmt.Errorf("quoted currency %q is invalid", currency)
		}
		if rate.Micros() <= 0 {
			return Table{}, fmt.Errorf("rate for %s must be positive", currency)
		}
		validated[currency] = rate
	}
	return Table{source: source, date: date, base: base, rates: validated}, nil
}

// Source names the quote publisher (for example "ecb").
func (table Table) Source() string { return table.source }

// Date is the publication date of the quotes, as published.
func (table Table) Date() string { return table.date }

// Base is the table's base currency.
func (table Table) Base() string { return table.base }

// rateMicros returns micro-units of currency per one base unit.
func (table Table) rateMicros(currency string) (int64, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == table.base {
		return money.Scale, nil
	}
	rate, exists := table.rates[currency]
	if !exists {
		return 0, fmt.Errorf(
			"no %s quote for %s on %s",
			table.source, currency, table.date,
		)
	}
	return rate.Micros(), nil
}

// Convert converts an amount between two quoted currencies with exactly
// one rounding: amount × rate(to) / rate(from), half to even at the sixth
// decimal place.
func (table Table) Convert(
	amount money.Decimal,
	from, to string,
) (money.Decimal, error) {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return amount, nil
	}
	if amount.Micros() < 0 {
		return money.Decimal(0), fmt.Errorf("negative amounts are not convertible")
	}
	fromRate, err := table.rateMicros(from)
	if err != nil {
		return money.Decimal(0), err
	}
	toRate, err := table.rateMicros(to)
	if err != nil {
		return money.Decimal(0), err
	}
	numerator := new(big.Int).Mul(
		big.NewInt(amount.Micros()),
		big.NewInt(toRate),
	)
	converted, err := divideHalfEven(numerator, big.NewInt(fromRate))
	if err != nil {
		return money.Decimal(0), fmt.Errorf("convert %s to %s: %w", from, to, err)
	}
	result, err := money.FromMicros(converted)
	if err != nil {
		return money.Decimal(0), fmt.Errorf("convert %s to %s: %w", from, to, err)
	}
	return result, nil
}

// divideHalfEven divides two non-negative integers rounding half to even
// and fails on results outside the exact int64 micro range.
func divideHalfEven(numerator, denominator *big.Int) (int64, error) {
	quotient, remainder := new(big.Int).QuoRem(
		numerator,
		denominator,
		new(big.Int),
	)
	doubled := new(big.Int).Lsh(remainder, 1)
	switch doubled.Cmp(denominator) {
	case 1:
		quotient.Add(quotient, big.NewInt(1))
	case 0:
		if quotient.Bit(0) == 1 {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("result exceeds the exact decimal range")
	}
	return quotient.Int64(), nil
}
