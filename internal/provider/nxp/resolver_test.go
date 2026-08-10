// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package nxp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type fakeStore struct {
	result      *SearchResult
	searchErr   error
	detail      *PartDetail
	detailErr   error
	currency    string
	searchCalls int
}

func (store *fakeStore) Search(
	_ context.Context,
	_ string,
) (*SearchResult, error) {
	store.searchCalls++
	return store.result, store.searchErr
}

func (store *fakeStore) PartDetail(
	_ context.Context,
	_, _ string,
) (*PartDetail, error) {
	return store.detail, store.detailErr
}

func (store *fakeStore) Currency() string {
	return store.currency
}

func TestResolverProducesSafeExactNXPPlan(t *testing.T) {
	t.Parallel()
	stock, moq, multiple := 4310, 1300, 260
	store := &fakeStore{
		currency: "USD",
		result: &SearchResult{
			Query: "KW47B42ZB7AFTBT", PartID: "KW47B42ZB7AFTBT",
			BuyDirect: true, Currency: "USD", StockQuantity: &stock,
			PackingDescription: "TRAY-Tray, Bakeable",
			StepPrices: []StepPrice{
				{Quantity: 1, Price: json.Number("6.60")},
				{Quantity: 100, Price: json.Number("5.59")},
			},
		},
		detail: &PartDetail{
			MinimumOrderQuantity: &moq, MinimumPackageQuantity: &multiple,
		},
	}
	resolver, _ := NewResolver(store)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "KW47B42ZB7AFTBT", Manufacturer: "NXP", RequiredQuantity: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" ||
		result.Offer == nil ||
		result.Offer.SelectedPlan == nil ||
		result.Offer.SelectedPlan.PurchasedQuantity != 1300 ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "7267.000000" ||
		!result.Offer.SelectedPlan.StockVerified {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverRequiresReviewForPackagingVariantOrMissingMOQ(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		query  string
		detail *PartDetail
		code   string
	}{
		{
			name: "variant", query: "KW47B42ZB7AFTB",
			detail: &PartDetail{MinimumOrderQuantity: intPointer(1)},
			code:   "REVIEW_REQUIRED",
		},
		{
			name: "moq", query: "KW47B42ZB7AFTBT",
			detail: nil,
			code:   "MOQ_REVIEW_REQUIRED",
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stock := 4310
			store := &fakeStore{
				currency: "USD",
				result: &SearchResult{
					PartID: "KW47B42ZB7AFTBT", BuyDirect: true, Currency: "USD",
					StockQuantity: &stock,
					StepPrices: []StepPrice{{
						Quantity: 1, Price: json.Number("5.59"),
					}},
				},
				detail: testCase.detail,
			}
			resolver, _ := NewResolver(store)
			result, err := resolver.Lookup(context.Background(), procurement.Demand{
				PartNumber: testCase.query, Manufacturer: "NXP", RequiredQuantity: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "review" ||
				result.IssueCode != testCase.code ||
				result.Offer.SelectedPlan != nil ||
				result.Offer.CandidatePlan == nil {
				t.Fatalf("unsafe result: %#v", result)
			}
		})
	}
}

func TestResolverSkipsNonNXPManufacturersWithoutBrowser(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		currency:  "USD",
		searchErr: errors.New("browser should not be called"),
	}
	resolver, _ := NewResolver(store)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "ABC", Manufacturer: "Yageo", RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_applicable" || store.searchCalls != 0 {
		t.Fatalf("unexpected result=%#v calls=%d", result, store.searchCalls)
	}
}

func TestSupportsManufacturerAcceptsCommonNXPSpellings(t *testing.T) {
	t.Parallel()
	// Distributor catalogs and BOM lines routinely carry NXP's regional
	// and legal entity names; rejecting them silently downgrades those
	// lines to not_applicable and loses direct pricing.
	for _, name := range []string{
		"NXP",
		"NXP Semiconductors",
		"NXP USA Inc.",
		"NXP Semiconductors N.V.",
		"NXP B.V.",
		"Freescale Semiconductor, Inc.",
	} {
		if !supportsManufacturer(name) {
			t.Errorf("supportsManufacturer(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Yageo", "Texas Instruments", "STMicroelectronics"} {
		if supportsManufacturer(name) {
			t.Errorf("supportsManufacturer(%q) = true, want false", name)
		}
	}
}

func TestResolverDoesNotSelectWhenDirectBuyUnavailable(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		currency: "USD",
		result:   &SearchResult{PartID: "ABC123", BuyDirect: false},
	}
	resolver, _ := NewResolver(store)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "ABC123", Manufacturer: "NXP", RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "unavailable" ||
		result.IssueCode != "DIRECT_BUY_UNAVAILABLE" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func intPointer(value int) *int {
	return &value
}
