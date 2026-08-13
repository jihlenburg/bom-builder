// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/fx"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type plannedResolver struct {
	parts map[string]procurement.SourcedPart
}

func (resolver plannedResolver) Lookup(
	_ context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	part := resolver.parts[demand.PartNumber]
	part.Demand = demand
	return part, nil
}

func pricedPart(currency, extended string, quantity int) procurement.SourcedPart {
	price, err := money.Parse(extended)
	if err != nil {
		panic(err)
	}
	plan := &procurement.PurchasePlan{
		RequiredQuantity:  quantity,
		PurchasedQuantity: quantity,
		ExtendedPrice:     price,
		Currency:          currency,
		StockVerified:     true,
	}
	return procurement.SourcedPart{
		Status: "priced",
		Offer:  &procurement.Offer{Provider: "mouser", SelectedPlan: plan},
	}
}

func fxDemand(part string, quantity int) procurement.Demand {
	return procurement.Demand{
		PartNumber:       part,
		Manufacturer:     "ACME",
		QuantityPerUnit:  quantity,
		RequiredQuantity: quantity,
	}
}

func fxTestTable(t *testing.T) fx.Table {
	t.Helper()
	usd, _ := money.Parse("1.25")
	table, err := fx.NewTable("ecb", "2026-08-07", "EUR", map[string]money.Decimal{
		"USD": usd,
	})
	if err != nil {
		t.Fatalf("new table: %v", err)
	}
	return table
}

func TestSourceWithFXConvertsMixedCurrenciesToTarget(t *testing.T) {
	resolver := plannedResolver{parts: map[string]procurement.SourcedPart{
		"EUR-PART": pricedPart("EUR", "10.00", 5),
		"USD-PART": pricedPart("USD", "12.50", 5),
	}}
	result := SourceWithFX(
		context.Background(),
		resolver,
		[]procurement.Demand{fxDemand("EUR-PART", 5), fxDemand("USD-PART", 5)},
		1,
		&CurrencyConversion{Target: "EUR", Table: fxTestTable(t)},
	)
	if result.ExitCode != 0 {
		t.Fatalf("exit = %d, errors = %+v", result.ExitCode, result.Errors)
	}
	summary := result.Summary
	// 10.00 EUR + (12.50 USD / 1.25) = 20.00 EUR.
	if summary.Currency != "EUR" ||
		summary.TotalCost == nil ||
		summary.TotalCost.String() != "20.000000" {
		t.Fatalf("unexpected converted total: %+v", summary)
	}
	if summary.Conversion == nil ||
		summary.Conversion.QuoteSource != "ecb" ||
		summary.Conversion.QuoteDate != "2026-08-07" {
		t.Fatalf("converted totals must carry quote provenance: %+v", summary.Conversion)
	}
}

func TestSourceWithoutFXStillFailsClosedOnMixedCurrencies(t *testing.T) {
	resolver := plannedResolver{parts: map[string]procurement.SourcedPart{
		"EUR-PART": pricedPart("EUR", "10.00", 5),
		"USD-PART": pricedPart("USD", "12.50", 5),
	}}
	result := Source(
		context.Background(),
		resolver,
		[]procurement.Demand{fxDemand("EUR-PART", 5), fxDemand("USD-PART", 5)},
		1,
	)
	if result.Summary.TotalCost != nil {
		t.Fatalf("mixed currencies without FX must omit the total: %+v", result.Summary)
	}
	found := false
	for _, issue := range result.Errors {
		if issue.Code == "MIXED_CURRENCY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected MIXED_CURRENCY, got %+v", result.Errors)
	}
}

func TestSourceWithFXFailsClosedOnUnquotedCurrency(t *testing.T) {
	resolver := plannedResolver{parts: map[string]procurement.SourcedPart{
		"EUR-PART": pricedPart("EUR", "10.00", 5),
		"GBP-PART": pricedPart("GBP", "8.00", 5),
	}}
	result := SourceWithFX(
		context.Background(),
		resolver,
		[]procurement.Demand{fxDemand("EUR-PART", 5), fxDemand("GBP-PART", 5)},
		1,
		&CurrencyConversion{Target: "EUR", Table: fxTestTable(t)},
	)
	if result.Summary.TotalCost != nil {
		t.Fatalf("an unquoted currency must omit the total: %+v", result.Summary)
	}
	found := false
	for _, issue := range result.Errors {
		if issue.Code == "FX_CONVERSION_FAILED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected FX_CONVERSION_FAILED, got %+v", result.Errors)
	}
	if result.ExitCode == 0 {
		t.Fatal("a failed conversion must not exit 0")
	}
	if result.Summary.PricedCount != 1 || result.Summary.ProviderErrors != 1 {
		t.Fatalf("failed conversion was counted as priced: %+v", result.Summary)
	}
	failed := result.Parts[1]
	if failed.Status != "provider_error" || failed.IssueCode != "FX_CONVERSION_FAILED" {
		t.Fatalf("failed conversion left a priced line: %#v", failed)
	}
}

func TestSourceMarksMoneyOverflowAsLineFailure(t *testing.T) {
	resolver := plannedResolver{parts: map[string]procurement.SourcedPart{
		"MAX-PART": pricedPart("EUR", "9223372036854.775807", 1),
		"ONE-PART": pricedPart("EUR", "0.000001", 1),
	}}
	result := Source(
		context.Background(),
		resolver,
		[]procurement.Demand{fxDemand("MAX-PART", 1), fxDemand("ONE-PART", 1)},
		1,
	)
	if result.Summary.PricedCount != 1 || result.Summary.ProviderErrors != 1 {
		t.Fatalf("overflow was counted as priced: %+v", result.Summary)
	}
	failed := result.Parts[1]
	if failed.Status != "provider_error" || failed.IssueCode != "MONEY_OVERFLOW" {
		t.Fatalf("overflow left a priced line: %#v", failed)
	}
}
