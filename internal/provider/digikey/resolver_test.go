// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package digikey

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func TestNormalizePlanPreservesEveryCompositeSKU(t *testing.T) {
	t.Parallel()
	plan, err := normalizePlan(PricingOption{
		Name:          "Exact",
		TotalQuantity: 100,
		TotalPrice:    json.Number("25.00"),
		Products: []PricingProduct{
			{
				ProductNumber: "A-CT", Quantity: 60, MinimumOrderQuantity: 1,
				UnitPrice: json.Number("0.25"), ExtendedPrice: json.Number("15.00"),
				PackageType: "Cut Tape",
			},
			{
				ProductNumber: "A-TR", Quantity: 40, MinimumOrderQuantity: 20,
				UnitPrice: json.Number("0.25"), ExtendedPrice: json.Number("10.00"),
				PackageType: "Tape & Reel",
			},
		},
	}, "EUR", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Legs) != 2 ||
		plan.Legs[0].FamilyID != "A-CT" ||
		plan.Legs[1].FamilyID != "A-TR" ||
		plan.PurchasedQuantity != 100 ||
		plan.ExtendedPrice.String() != "25.000000" ||
		plan.StockVerified {
		t.Fatalf("composite plan was collapsed or pre-verified: %#v", plan)
	}
}

func TestNormalizePlanRejectsCompositePriceMismatch(t *testing.T) {
	t.Parallel()
	_, err := normalizePlan(PricingOption{
		Name:          "Exact",
		TotalQuantity: 100,
		TotalPrice:    json.Number("25.01"),
		Products: []PricingProduct{{
			ProductNumber: "A-CT", Quantity: 100, MinimumOrderQuantity: 1,
			UnitPrice: json.Number("0.25"), ExtendedPrice: json.Number("25.00"),
			PackageType: "Cut Tape",
		}},
	}, "EUR", 100)
	if err == nil {
		t.Fatal("mismatched provider total was accepted")
	}
}

func TestApplyStockVerifiesPerVariationAndAggregatesLegs(t *testing.T) {
	t.Parallel()
	plan := &procurement.PurchasePlan{
		Legs: []procurement.PurchaseLeg{
			{FamilyID: "A-CT", PurchasedQuantity: 60},
			{FamilyID: "A-TR", PurchasedQuantity: 40},
			{FamilyID: "A-CT", PurchasedQuantity: 10},
		},
	}
	available, verified := applyStock(plan, ProductInfo{
		VariationQuantities: map[string]int{"A-CT": 70, "A-TR": 40},
	})
	if !verified || available == nil || *available != 40 || !plan.StockVerified {
		t.Fatalf("aggregated legs were not verified: %v %v", available, verified)
	}

	short := &procurement.PurchasePlan{
		Legs: []procurement.PurchaseLeg{
			{FamilyID: "A-CT", PurchasedQuantity: 60},
			{FamilyID: "A-CT", PurchasedQuantity: 20},
		},
	}
	available, verified = applyStock(short, ProductInfo{
		VariationQuantities: map[string]int{"A-CT": 70},
	})
	if verified || available == nil || *available != 70 || short.StockVerified {
		t.Fatalf("aggregated shortage was verified: %v %v", available, verified)
	}

	unknown := &procurement.PurchasePlan{
		Legs: []procurement.PurchaseLeg{{FamilyID: "A-CT", PurchasedQuantity: 10}},
	}
	available, verified = applyStock(unknown, ProductInfo{
		VariationQuantities: map[string]int{"B-CT": 500},
	})
	if verified || available != nil {
		t.Fatalf("missing variation passed as known: %v %v", available, verified)
	}
}

func TestResolverProducesExactStockVerifiedOfferWithDocuments(t *testing.T) {
	t.Parallel()
	server := resolverServer("5000", "ECA-1VHG102")
	defer server.Close()
	client := testClient(t, server)
	resolver, err := NewResolver(client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic Electronic Components",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil ||
		result.Offer.SelectedPlan == nil ||
		!result.Offer.SelectedPlan.StockVerified ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "69.800000" ||
		result.Offer.DatasheetURL == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverPrefersCheaperStockedOverbuyAcrossPricingGroups(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/oauth2/token":
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
		case request.URL.Path == "/products/v4/search/P5555-CT-ND/productdetails":
			fmt.Fprint(writer, `{"Product":{`+
				`"DatasheetUrl":"https://manufacturer.test/part.pdf",`+
				`"ProductUrl":"https://digikey.test/product",`+
				`"ProductVariations":[`+
				`{"DigiKeyProductNumber":"P5555-CT-ND",`+
				`"QuantityAvailableforPackageType":5000},`+
				`{"DigiKeyProductNumber":"P5555-TR-ND",`+
				`"QuantityAvailableforPackageType":5000}]}}`)
		default:
			fmt.Fprint(writer, `{
				"RequestedProduct":"ECA-1VHG102",
				"RequestedQuantity":950,
				"ManufacturerPartNumber":"ECA-1VHG102",
				"Manufacturer":{"Name":"Panasonic Electronic Components"},
				"SettingsUsed":{"SearchLocaleUsed":{"Currency":"EUR"}},
				"MyPricingOptions":[{
					"PricingOption":"Exact",
					"TotalQuantityPriced":950,
					"TotalPrice":95,
					"QuantityAvailable":0,
					"Products":[{
						"DigiKeyProductNumber":"P5555-CT-ND",
						"QuantityPriced":950,
						"MinimumOrderQuantity":1,
						"UnitPrice":0.1,
						"ExtendedPrice":95,
						"PackageType":{"Name":"Cut Tape"}
					}]
				}],
				"StandardPricingOptions":[{
					"PricingOption":"Better Value",
					"TotalQuantityPriced":1000,
					"TotalPrice":90,
					"QuantityAvailable":0,
					"Products":[{
						"DigiKeyProductNumber":"P5555-TR-ND",
						"QuantityPriced":1000,
						"MinimumOrderQuantity":1000,
						"UnitPrice":0.09,
						"ExtendedPrice":90,
						"PackageType":{"Name":"Tape & Reel"}
					}]
				}]
			}`)
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil ||
		result.Offer.SelectedPlan == nil ||
		result.Offer.DistributorPartNumber != "P5555-TR-ND" ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "90.000000" ||
		result.Offer.SelectedPlan.PurchasedQuantity != 1000 ||
		result.Offer.SelectedPlan.SurplusQuantity != 50 ||
		result.Offer.SelectedPlan.PricingStrategy != "Better Value" ||
		result.Offer.SelectedPlan.OrderPlan != "1 reel x 1000 (P5555-TR-ND)" {
		t.Fatalf("cheaper overbuy plan was not selected: %#v", result)
	}
}

func TestResolverRejectsManufacturerMismatchBeforeStockLookup(t *testing.T) {
	t.Parallel()
	server := resolverServer("5000", "ECA-1VHG102")
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Texas Instruments",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" ||
		result.IssueCode != "MANUFACTURER_MISMATCH" ||
		result.Offer != nil ||
		client.RequestCount() != 2 {
		t.Fatalf("manufacturer mismatch was not rejected early: %#v", result)
	}
}

func TestResolverDoesNotSelectInsufficientStock(t *testing.T) {
	t.Parallel()
	server := resolverServer("10", "ECA-1VHG102")
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || result.Offer.SelectedPlan != nil {
		t.Fatalf("short stock was selected: %#v", result)
	}
}

func TestResolverReportsStockUnknownWithoutVariationData(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/oauth2/token":
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
		case request.URL.Path == "/products/v4/search/P5555-ND/productdetails":
			fmt.Fprint(writer, `{"Product":{`+
				`"DatasheetUrl":"https://manufacturer.test/part.pdf",`+
				`"ProductUrl":"https://digikey.test/product"}}`)
		default:
			writePricingResponse(writer, "ECA-1VHG102")
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "stock_unknown" || result.Offer == nil ||
		result.Offer.SelectedPlan != nil ||
		result.Offer.AvailableQuantity != nil {
		t.Fatalf("missing variation data was not treated as unknown: %#v", result)
	}
}

func TestResolverKeepsDifferentMPNReviewRequired(t *testing.T) {
	t.Parallel()
	server := resolverServer("5000", "ECA-1VHG102-T")
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || result.Offer == nil ||
		!result.Offer.ReviewRequired ||
		result.Offer.SelectedPlan != nil ||
		result.Offer.CandidatePlan == nil {
		t.Fatalf("different MPN was marked exact: %#v", result)
	}
}

func TestResolverKeepsKnownShortageAheadOfReview(t *testing.T) {
	t.Parallel()
	server := resolverServer("10", "ECA-1VHG102-T")
	defer server.Close()
	client := testClient(t, server)
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "ECA-1VHG102",
		Manufacturer:     "Panasonic",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || result.IssueCode != "INSUFFICIENT_STOCK" ||
		result.Offer == nil || !result.Offer.ReviewRequired ||
		result.Offer.SelectedPlan != nil || result.Offer.CandidatePlan == nil {
		t.Fatalf("review state masked known shortage: %#v", result)
	}
}

// resolverServer mirrors live Digi-Key behavior observed 2026-07-30:
// pricingbyquantity always reports QuantityAvailable 0 (the field is
// not populated there), while productdetails carries the real product
// and per-variation quantities. The stock parameter therefore feeds
// the productdetails variation for the priced SKU, never the pricing
// response.
func resolverServer(stock, manufacturerPartNumber string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/v1/oauth2/token":
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
		case request.URL.Path == "/products/v4/search/P5555-ND/productdetails":
			fmt.Fprintf(writer, `{"Product":{`+
				`"DatasheetUrl":"https://manufacturer.test/part.pdf",`+
				`"ProductUrl":"https://digikey.test/product",`+
				`"QuantityAvailable":%s,`+
				`"ProductVariations":[`+
				`{"DigiKeyProductNumber":"P5555-ND",`+
				`"QuantityAvailableforPackageType":%s,`+
				`"MinimumOrderQuantity":1},`+
				`{"DigiKeyProductNumber":"P5555-TR-ND",`+
				`"QuantityAvailableforPackageType":0,`+
				`"MinimumOrderQuantity":3000}`+
				`]}}`, stock, stock)
		default:
			writePricingResponse(writer, manufacturerPartNumber)
		}
	}))
}
