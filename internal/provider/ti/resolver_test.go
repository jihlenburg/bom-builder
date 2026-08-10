// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package ti

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func TestResolverProducesExactStockVerifiedOffer(t *testing.T) {
	t.Parallel()
	server := tiServer(productFixture(
		"TPS61160DRVR",
		"TPS61160",
		5000,
		"10000",
		"ACTIVE",
		"EUR",
	))
	defer server.Close()
	resolver := testResolver(t, server, "EUR")
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "TPS61160DRVR",
		Manufacturer:     "Texas Instruments",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" ||
		result.Offer == nil ||
		result.Offer.SelectedPlan == nil ||
		result.Offer.SelectedPlan.PurchasedQuantity != 1000 ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "90.000000" ||
		!result.Offer.SelectedPlan.StockVerified ||
		result.Offer.OrderLimit == nil ||
		*result.Offer.OrderLimit != 10000 {
		t.Fatalf("unexpected TI result: %#v", result)
	}
}

func TestResolverDoesNotContactTIForAnotherManufacturer(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	resolver := testResolver(t, server, "USD")
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "RC0402FR-0710KL", Manufacturer: "Yageo", RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_applicable" || requests.Load() != 0 {
		t.Fatalf("unexpected result=%#v requests=%d", result, requests.Load())
	}
}

func TestSupportsCommonTIManufacturerNames(t *testing.T) {
	t.Parallel()
	for _, manufacturer := range []string{
		"TI",
		"Texas Instruments",
		"Texas Instruments Incorporated",
		"Texas Instruments, Inc.",
	} {
		if !supportsManufacturer(manufacturer) {
			t.Errorf("manufacturer %q was not recognized", manufacturer)
		}
	}
	if supportsManufacturer("Analog Devices") {
		t.Fatal("unrelated manufacturer was recognized as TI")
	}
}

func TestResolverRequiresReviewForGenericPartResolution(t *testing.T) {
	t.Parallel()
	server := tiServer(productFixture(
		"TPS61160DRVR",
		"TPS61160",
		5000,
		"10000",
		"ACTIVE",
		"USD",
	))
	defer server.Close()
	resolver := testResolver(t, server, "USD")
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "TPS61160", Manufacturer: "TI", RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" ||
		result.Offer == nil ||
		!result.Offer.ReviewRequired ||
		result.Offer.SelectedPlan != nil ||
		result.Offer.CandidatePlan == nil {
		t.Fatalf("generic match was selected: %#v", result)
	}
}

func TestResolverRejectsInsufficientStockAndOrderLimit(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		stock     int
		limit     string
		issueCode string
	}{
		{name: "stock", stock: 10, limit: "1000", issueCode: "INSUFFICIENT_STOCK"},
		{name: "limit", stock: 1000, limit: "50", issueCode: "ORDER_LIMIT"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := tiServer(productFixture(
				"TMP421AQDCNRQ1",
				"TMP421-Q1",
				testCase.stock,
				testCase.limit,
				"ACTIVE",
				"USD",
			))
			defer server.Close()
			resolver := testResolver(t, server, "USD")
			result, err := resolver.Lookup(context.Background(), procurement.Demand{
				PartNumber:       "TMP421AQDCNRQ1",
				Manufacturer:     "TI",
				RequiredQuantity: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "shortage" ||
				result.IssueCode != testCase.issueCode ||
				result.Offer.SelectedPlan != nil {
				t.Fatalf("unsafe result: %#v", result)
			}
		})
	}
}

func TestResolverRequiresReviewForNonActiveLifecycle(t *testing.T) {
	t.Parallel()
	server := tiServer(productFixture(
		"TMP421AQDCNRQ1",
		"TMP421-Q1",
		1000,
		"1000",
		"NRND",
		"USD",
	))
	defer server.Close()
	resolver := testResolver(t, server, "USD")
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber: "TMP421AQDCNRQ1", Manufacturer: "TI", RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" ||
		result.IssueCode != "LIFECYCLE_REVIEW_REQUIRED" ||
		result.Offer.SelectedPlan != nil {
		t.Fatalf("lifecycle risk was selected: %#v", result)
	}
}

func testResolver(
	t *testing.T,
	server *httptest.Server,
	currency string,
) *Resolver {
	t.Helper()
	client := testClient(t, server, currency)
	resolver, err := NewResolver(client)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}
