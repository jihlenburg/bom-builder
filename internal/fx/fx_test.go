// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package fx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/money"
)

func mustParse(t *testing.T, value string) money.Decimal {
	t.Helper()
	parsed, err := money.Parse(value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func testTable(t *testing.T) Table {
	t.Helper()
	table, err := NewTable("ecb", "2026-08-07", "EUR", map[string]money.Decimal{
		"USD": mustParse(t, "1.25"),
		"JPY": mustParse(t, "160.00"),
	})
	if err != nil {
		t.Fatalf("new table: %v", err)
	}
	return table
}

func TestConvertBaseAndCrossRates(t *testing.T) {
	table := testTable(t)
	cases := []struct {
		amount, from, to, expected string
	}{
		{"10.00", "EUR", "EUR", "10.000000"},
		{"10.00", "EUR", "USD", "12.500000"},
		{"12.50", "USD", "EUR", "10.000000"},
		// Cross rate through the base: USD -> JPY = 160 / 1.25 = 128.
		{"1.00", "USD", "JPY", "128.000000"},
		{"128.00", "JPY", "USD", "1.000000"},
	}
	for _, testCase := range cases {
		converted, err := table.Convert(
			mustParse(t, testCase.amount),
			testCase.from,
			testCase.to,
		)
		if err != nil {
			t.Fatalf("%s %s->%s: %v", testCase.amount, testCase.from, testCase.to, err)
		}
		if converted.String() != testCase.expected {
			t.Fatalf(
				"%s %s->%s = %s, expected %s",
				testCase.amount, testCase.from, testCase.to,
				converted.String(), testCase.expected,
			)
		}
	}
}

func TestConvertRoundsHalfToEven(t *testing.T) {
	table, err := NewTable("ecb", "2026-08-07", "EUR", map[string]money.Decimal{
		"USD": mustParse(t, "2"),
	})
	if err != nil {
		t.Fatalf("new table: %v", err)
	}
	// 0.000001 USD -> EUR = 0.0000005: exactly half, rounds to even (0).
	converted, err := table.Convert(mustParse(t, "0.000001"), "USD", "EUR")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted.String() != "0.000000" {
		t.Fatalf("half-to-even must round 0.0000005 down, got %s", converted.String())
	}
	// 0.000003 USD -> EUR = 0.0000015: exactly half, rounds to even (2).
	converted, err = table.Convert(mustParse(t, "0.000003"), "USD", "EUR")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if converted.String() != "0.000002" {
		t.Fatalf("half-to-even must round 0.0000015 up, got %s", converted.String())
	}
}

func TestConvertFailsExplicitly(t *testing.T) {
	table := testTable(t)
	if _, err := table.Convert(mustParse(t, "1.00"), "EUR", "GBP"); err == nil ||
		!strings.Contains(err.Error(), "GBP") {
		t.Fatalf("unknown currency must name itself: %v", err)
	}
	if _, err := table.Convert(mustParse(t, "1.00"), "XXX", "EUR"); err == nil {
		t.Fatal("unknown source currency must fail")
	}
	// 9.2 quadrillion micros * 160 overflows the exact range.
	huge, err := money.FromMicros(1<<62 + 1)
	if err != nil {
		t.Fatalf("from micros: %v", err)
	}
	if _, err := table.Convert(huge, "EUR", "JPY"); err == nil {
		t.Fatal("overflow must fail explicitly")
	}
}

func TestNewTableRejectsInvalidInput(t *testing.T) {
	if _, err := NewTable("", "2026-08-07", "EUR", nil); err == nil {
		t.Fatal("missing source must fail")
	}
	if _, err := NewTable("ecb", "", "EUR", nil); err == nil {
		t.Fatal("missing date must fail")
	}
	if _, err := NewTable("ecb", "2026-08-07", "EURO", nil); err == nil {
		t.Fatal("invalid base must fail")
	}
	if _, err := NewTable("ecb", "2026-08-07", "EUR", map[string]money.Decimal{
		"USD": money.Decimal(0),
	}); err == nil {
		t.Fatal("zero rate must fail")
	}
}

const ecbFixture = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01"
    xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
  <gesmes:subject>Reference rates</gesmes:subject>
  <Cube>
    <Cube time="2026-08-07">
      <Cube currency="USD" rate="1.0876"/>
      <Cube currency="JPY" rate="162.53"/>
      <Cube currency="GBP" rate="0.8451"/>
    </Cube>
  </Cube>
</gesmes:Envelope>`

func TestECBClientParsesDailyDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/xml")
			writer.Write([]byte(ecbFixture))
		},
	))
	defer server.Close()
	t.Setenv("BOM_BUILDER_ECB_URL", server.URL)
	client, err := NewECBClientFromEnvironment()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	table, err := client.FetchDaily(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if table.Source() != "ecb" || table.Date() != "2026-08-07" || table.Base() != "EUR" {
		t.Fatalf("unexpected table identity: %s %s %s", table.Source(), table.Date(), table.Base())
	}
	converted, err := table.Convert(mustParse(t, "1.0876"), "USD", "EUR")
	if err != nil || converted.String() != "1.000000" {
		t.Fatalf("fixture rate must round-trip: %s %v", converted.String(), err)
	}
}

func TestECBClientFailsOnBrokenDocuments(t *testing.T) {
	cases := map[string]string{
		"empty":     `<?xml version="1.0"?><gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01"><Cube></Cube></gesmes:Envelope>`,
		"not xml":   `{"rates": []}`,
		"bad rate":  strings.Replace(ecbFixture, "1.0876", "not-a-rate", 1),
		"no date":   strings.Replace(ecbFixture, ` time="2026-08-07"`, "", 1),
		"zero rate": strings.Replace(ecbFixture, "1.0876", "0", 1),
	}
	for name, document := range cases {
		server := httptest.NewServer(http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				writer.Write([]byte(document))
			},
		))
		t.Setenv("BOM_BUILDER_ECB_URL", server.URL)
		client, err := NewECBClientFromEnvironment()
		if err != nil {
			t.Fatalf("%s: new client: %v", name, err)
		}
		if _, err := client.FetchDaily(context.Background()); err == nil {
			t.Errorf("%s: expected a fetch failure", name)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
		},
	))
	defer server.Close()
	t.Setenv("BOM_BUILDER_ECB_URL", server.URL)
	client, err := NewECBClientFromEnvironment()
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.FetchDaily(context.Background()); err == nil {
		t.Error("HTTP failure must propagate")
	}
}
