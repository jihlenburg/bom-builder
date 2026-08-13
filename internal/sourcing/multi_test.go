// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/fx"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type fixedResolver struct {
	part procurement.SourcedPart
	err  error
}

func (resolver fixedResolver) Lookup(
	_ context.Context,
	_ procurement.Demand,
) (procurement.SourcedPart, error) {
	return resolver.part, resolver.err
}

func TestMultiResolverSelectsCheapestComparableSafePlan(t *testing.T) {
	t.Parallel()
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	mouser := pricedProviderPart(t, demand, "mouser", "10.00", "EUR")
	digikey := pricedProviderPart(t, demand, "digikey", "9.50", "EUR")
	resolver, err := NewMultiResolver([]ProviderResolver{
		{Name: "mouser", Resolver: fixedResolver{part: mouser}},
		{Name: "digikey", Resolver: fixedResolver{part: digikey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil ||
		result.Offer.Provider != "digikey" || len(result.Offers) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, offer := range result.Offers {
		if offer.Provider == "mouser" && offer.SelectedPlan != nil {
			t.Fatal("losing provider retained selected plan")
		}
	}
}

func TestMultiResolverFallbackClearsSelectedPlans(t *testing.T) {
	t.Parallel()
	// A misbehaving adapter can attach a SelectedPlan to a non-priced
	// result; the fallback path must strip it, or downstream consumers
	// (alternatives ranking scans offers for any SelectedPlan) would use
	// an unsafe plan from a shortage line.
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	part := pricedProviderPart(t, demand, "mouser", "10.00", "EUR")
	part.Status = "shortage"
	part.IssueCode = "INSUFFICIENT_STOCK"
	resolver, err := NewMultiResolver([]ProviderResolver{
		{Name: "mouser", Resolver: fixedResolver{part: part}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" {
		t.Fatalf("unexpected status: %#v", result)
	}
	if result.Offer != nil && result.Offer.SelectedPlan != nil {
		t.Fatal("fallback offer retained a selected plan")
	}
	for _, offer := range result.Offers {
		if offer.SelectedPlan != nil {
			t.Fatal("fallback offers retained a selected plan")
		}
	}
}

func TestMultiResolverRefusesCrossCurrencyComparison(t *testing.T) {
	t.Parallel()
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	resolver, _ := NewMultiResolver([]ProviderResolver{
		{
			Name: "mouser",
			Resolver: fixedResolver{
				part: pricedProviderPart(t, demand, "mouser", "10", "EUR"),
			},
		},
		{
			Name: "digikey",
			Resolver: fixedResolver{
				part: pricedProviderPart(t, demand, "digikey", "9", "USD"),
			},
		},
	})
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" ||
		result.IssueCode != "CURRENCY_CONVERSION_REQUIRED" ||
		result.Offer != nil {
		t.Fatalf("mixed currencies were compared: %#v", result)
	}
}

func TestMultiResolverConvertsPlansBeforeComparing(t *testing.T) {
	t.Parallel()
	// With dated quotes the cheapest plan is decided on converted value,
	// not on the raw number: 8.50 USD at 1.10 USD per EUR is 7.727272 EUR
	// and beats 8.00 EUR, which a currency-blind numeric comparison would
	// have picked instead.
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	resolver, err := NewMultiResolverWithFX(
		[]ProviderResolver{
			{
				Name: "mouser",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "mouser", "8.00", "EUR"),
				},
			},
			{
				Name: "digikey",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "digikey", "8.50", "USD"),
				},
			},
		},
		&CurrencyConversion{Target: "EUR", Table: testQuoteTable(t)},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil ||
		result.Offer.Provider != "digikey" {
		t.Fatalf("conversion did not decide the comparison: %#v", result)
	}
	// Conversion decides the ranking only. The selected plan keeps the
	// currency the provider actually charges in, so the order stays
	// payable as quoted.
	if result.Offer.SelectedPlan.Currency != "USD" ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "8.500000" {
		t.Fatalf("selected plan was rewritten: %#v", result.Offer.SelectedPlan)
	}
	for _, offer := range result.Offers {
		if offer.Provider == "mouser" && offer.SelectedPlan != nil {
			t.Fatal("losing provider retained selected plan")
		}
	}
}

func TestMultiResolverFailsExplicitlyWhenAPlanCurrencyIsNotQuoted(t *testing.T) {
	t.Parallel()
	// An unconvertible plan cannot be proven more expensive than the
	// others, so comparing the remainder could select a worse plan. Fail
	// closed instead, the same way converted totals do.
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	resolver, _ := NewMultiResolverWithFX(
		[]ProviderResolver{
			{
				Name: "mouser",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "mouser", "8.00", "EUR"),
				},
			},
			{
				Name: "farnell",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "farnell", "7.00", "GBP"),
				},
			},
		},
		&CurrencyConversion{Target: "EUR", Table: testQuoteTable(t)},
	)
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" ||
		result.IssueCode != "FX_CONVERSION_FAILED" ||
		result.Offer != nil {
		t.Fatalf("unquoted currency was compared anyway: %#v", result)
	}
	for _, offer := range result.Offers {
		if offer.SelectedPlan != nil {
			t.Fatalf("failed comparison left a selected plan: %#v", offer)
		}
	}
}

func TestMultiResolverComparesOneCurrencyWithoutQuotes(t *testing.T) {
	t.Parallel()
	// Plans that already share a currency need no quote at all, even
	// when a conversion is configured and the table cannot price that
	// currency. Requiring a quote here would fail lines that never
	// needed converting.
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	resolver, _ := NewMultiResolverWithFX(
		[]ProviderResolver{
			{
				Name: "mouser",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "mouser", "9.00", "GBP"),
				},
			},
			{
				Name: "farnell",
				Resolver: fixedResolver{
					part: pricedProviderPart(t, demand, "farnell", "7.00", "GBP"),
				},
			},
		},
		&CurrencyConversion{Target: "EUR", Table: testQuoteTable(t)},
	)
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil ||
		result.Offer.Provider != "farnell" {
		t.Fatalf("same-currency comparison required a quote: %#v", result)
	}
}

// testQuoteTable is a dated EUR-based table quoting only USD, so tests
// can exercise both a convertible and an unquoted currency.
func testQuoteTable(t *testing.T) fx.Table {
	t.Helper()
	rate, err := money.Parse("1.10")
	if err != nil {
		t.Fatal(err)
	}
	table, err := fx.NewTable("ecb", "2026-08-13", "EUR", map[string]money.Decimal{
		"USD": rate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func TestMultiResolverKeepsHealthyProviderWhenAnotherDegrades(t *testing.T) {
	t.Parallel()
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 100}
	healthy := pricedProviderPart(t, demand, "mouser", "10", "EUR")
	resolver, _ := NewMultiResolver([]ProviderResolver{
		{Name: "mouser", Resolver: fixedResolver{part: healthy}},
		{Name: "digikey", Resolver: fixedResolver{err: errors.New("authentication failed")}},
	})
	result, err := resolver.Lookup(context.Background(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer.Provider != "mouser" ||
		result.IssueCode != "PROVIDER_DEGRADED" {
		t.Fatalf("unexpected degraded result: %#v", result)
	}
}

func TestMultiResolverRejectsInvalidPricedProviderContract(t *testing.T) {
	t.Parallel()
	resolver, err := NewMultiResolver([]ProviderResolver{{
		Name: "broken",
		Resolver: fixedResolver{part: procurement.SourcedPart{
			Status: "priced",
			Offer:  &procurement.Offer{Provider: "broken"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "ABC", Manufacturer: "Acme", RequiredQuantity: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid priced-result contract") {
		t.Fatalf("invalid provider contract was accepted: %v", err)
	}
}

func pricedProviderPart(
	t *testing.T,
	demand procurement.Demand,
	provider, total, currency string,
) procurement.SourcedPart {
	t.Helper()
	amount, err := money.Parse(total)
	if err != nil {
		t.Fatal(err)
	}
	plan := &procurement.PurchasePlan{
		RequiredQuantity:  demand.RequiredQuantity,
		PurchasedQuantity: demand.RequiredQuantity,
		ExtendedPrice:     amount,
		Currency:          currency,
		StockVerified:     true,
	}
	offer := &procurement.Offer{
		Provider:              provider,
		DistributorPartNumber: provider + "-sku",
		MatchMethod:           "exact",
		CandidatePlan:         plan,
		SelectedPlan:          plan,
	}
	return procurement.SourcedPart{
		Demand: demand, Status: "priced", Offer: offer, CandidateCount: 1,
	}
}
