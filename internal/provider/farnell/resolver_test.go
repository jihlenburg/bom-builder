// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package farnell

import (
	"context"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type stubSearcher struct {
	exact []Product
	broad []Product
}

func (stub stubSearcher) Search(
	_ context.Context,
	_ string,
	exact bool,
) ([]Product, error) {
	if exact {
		return stub.exact, nil
	}
	return stub.broad, nil
}

func (stub stubSearcher) Currency() string { return "EUR" }

func (stub stubSearcher) StoreID() string { return "de.farnell.com" }

func TestResolverBuildsExactStockVerifiedOffer(t *testing.T) {
	t.Parallel()
	resolver, err := NewResolver(stubSearcher{exact: []Product{pricedProduct(5000)}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil || result.Offer.SelectedPlan == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Offer.Provider != "farnell" ||
		result.Offer.DistributorPartNumber != "9339060" {
		t.Fatalf("unexpected offer identity: %#v", result.Offer)
	}
	if result.Offer.SelectedPlan.PurchasedQuantity != 1000 ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "90.000000" ||
		result.Offer.SelectedPlan.Currency != "EUR" ||
		!result.Offer.SelectedPlan.StockVerified {
		t.Fatalf("unsafe plan: %#v", result.Offer.SelectedPlan)
	}
	if result.Offer.DatasheetURL == "" ||
		!strings.Contains(result.Offer.ProductURL, "9339060") {
		t.Fatalf("document links missing: %#v", result.Offer)
	}
}

func TestResolverUsesStoreCurrencyForPriceBreaks(t *testing.T) {
	t.Parallel()
	// The response carries no currency; every break must carry the
	// store-implied currency, and the exact price text must survive the
	// trip into micros without a float detour.
	product := pricedProduct(5000)
	product.Prices = []RawPrice{{From: 1, To: 0, Cost: "0.001"}}
	resolver, _ := NewResolver(stubSearcher{exact: []Product{product}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Offer.PriceBreaks) != 1 {
		t.Fatalf("price breaks = %#v", result.Offer.PriceBreaks)
	}
	if result.Offer.PriceBreaks[0].Currency != "EUR" ||
		result.Offer.PriceBreaks[0].UnitPrice.String() != "0.001000" {
		t.Fatalf("unexpected break: %#v", result.Offer.PriceBreaks[0])
	}
}

func TestNormalizePriceBreaksDropsZeroPrices(t *testing.T) {
	t.Parallel()
	breaks := normalizePriceBreaks([]RawPrice{
		{From: 1, Cost: "0"},
		{From: 10, Cost: "0.10"},
	}, "EUR")
	if len(breaks) != 1 || breaks[0].Quantity != 10 {
		t.Fatalf("zero price was retained: %#v", breaks)
	}
}

func TestResolverDoesNotSelectInsufficientStock(t *testing.T) {
	t.Parallel()
	resolver, _ := NewResolver(stubSearcher{exact: []Product{pricedProduct(949)}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || result.Offer.SelectedPlan != nil ||
		result.IssueCode != "INSUFFICIENT_STOCK" {
		t.Fatalf("unexpected shortage result: %#v", result)
	}
}

func TestResolverReportsUnknownStockExplicitly(t *testing.T) {
	t.Parallel()
	// A missing stock field is UNKNOWN, never zero; the plan must not be
	// selected and the state must be explicit.
	product := pricedProduct(0)
	product.Stock = nil
	resolver, _ := NewResolver(stubSearcher{exact: []Product{product}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "stock_unknown" || result.IssueCode != "STOCK_UNKNOWN" ||
		result.Offer.SelectedPlan != nil {
		t.Fatalf("unknown stock was not explicit: %#v", result)
	}
}

func TestResolverTreatsDiscontinuedWithoutStockAsNotOrderable(t *testing.T) {
	t.Parallel()
	product := pricedProduct(0)
	product.ProductStatus = "NO_LONGER_MANUFACTURED"
	resolver, _ := NewResolver(stubSearcher{exact: []Product{product}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" || result.IssueCode != "NOT_ORDERABLE" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverKeepsLooseMatchReviewRequired(t *testing.T) {
	t.Parallel()
	candidate := pricedProduct(5000)
	candidate.TranslatedManufacturerPartNumber = "RC0402FR-0710KL-T"
	resolver, _ := NewResolver(stubSearcher{broad: []Product{candidate}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || !result.Offer.ReviewRequired ||
		result.Offer.SelectedPlan != nil || result.Offer.CandidatePlan == nil {
		t.Fatalf("loose match was treated as safe: %#v", result)
	}
	if result.Offer.MatchMethod != "candidate" {
		t.Fatalf("match method = %q", result.Offer.MatchMethod)
	}
}

func TestResolverFiltersWrongManufacturer(t *testing.T) {
	t.Parallel()
	wrong := pricedProduct(5000)
	wrong.BrandName = "NXP Semiconductors"
	resolver, _ := NewResolver(stubSearcher{exact: []Product{wrong}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" || result.IssueCode != "PART_NOT_FOUND" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverMatchesDiacriticManufacturerSpelling(t *testing.T) {
	t.Parallel()
	// The catalog indexes manufacturers in ASCII ("Wurth Elektronik");
	// a correctly spelled BOM ("Würth Elektronik") must still match.
	product := pricedProduct(5000)
	product.BrandName = "WURTH ELEKTRONIK"
	product.TranslatedManufacturerPartNumber = "744773022"
	resolver, _ := NewResolver(stubSearcher{exact: []Product{product}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "744773022",
		Manufacturer:     "Würth Elektronik",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" {
		t.Fatalf("diacritic manufacturer did not match: %#v", result)
	}
}

func TestResolverSkipsDevelopmentKitsInBroadSearch(t *testing.T) {
	t.Parallel()
	kit := pricedProduct(100)
	kit.TranslatedManufacturerPartNumber = "RC0402FR-0710KL-EVM"
	kit.DisplayName = "Evaluation Module for RC0402FR-0710KL"
	resolver, _ := NewResolver(stubSearcher{broad: []Product{kit}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" {
		t.Fatalf("development kit was not filtered: %#v", result)
	}
}

func pricedProduct(stock int) Product {
	level := stock
	return Product{
		SKU:                              "9339060",
		DisplayName:                      "RC0402FR-0710KL - SMD Chip Resistor",
		BrandName:                        "YAGEO",
		TranslatedManufacturerPartNumber: "RC0402FR-0710KL",
		TranslatedMinimumOrderQuality:    "1",
		PackSize:                         "1",
		UnitOfMeasure:                    "EACH",
		ProductStatus:                    "STOCKED",
		Prices: []RawPrice{
			{From: 1, To: 999, Cost: "0.10"},
			{From: 1000, To: 0, Cost: "0.09"},
		},
		Stock: &Stock{Level: &level},
		Datasheets: []Datasheet{{
			Type: "Technical Data Sheet",
			URL:  "https://example.test/datasheet.pdf",
		}},
	}
}
