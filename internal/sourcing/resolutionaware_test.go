// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

type fakeInner struct {
	demands []procurement.Demand
	result  procurement.SourcedPart
	err     error
}

func (inner *fakeInner) Lookup(
	_ context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	inner.demands = append(inner.demands, demand)
	if inner.err != nil {
		return procurement.SourcedPart{}, inner.err
	}
	result := inner.result
	result.Demand = demand
	return result, nil
}

type failingSource struct{}

func (failingSource) ActiveResolution(
	context.Context,
	string, string,
) (resolutions.Record, bool, error) {
	return resolutions.Record{}, false, errors.New("database is corrupt")
}

func testStoreWithResolution(
	t *testing.T,
	replacement resolutions.Replacement,
) *resolutions.Store {
	t.Helper()
	store, err := resolutions.Open(filepath.Join(t.TempDir(), "resolutions.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	_, _, err = store.Approve(context.Background(), resolutions.Request{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421-Q1",
		Replacement:  replacement,
		ApprovedBy:   "J. Ihlenburg",
	}, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return store
}

func demandFor(part string) procurement.Demand {
	return procurement.Demand{
		PartNumber:       part,
		Manufacturer:     "Texas Instruments",
		QuantityPerUnit:  10,
		RequiredQuantity: 10,
	}
}

func TestPassthroughWithoutResolution(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
	})
	inner := &fakeInner{result: procurement.SourcedPart{Status: "not_found"}}
	resolver, err := NewResolutionAware(store, inner)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	sourced, err := resolver.Lookup(context.Background(), demandFor("OTHER-PART"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(inner.demands) != 1 || inner.demands[0].PartNumber != "OTHER-PART" {
		t.Fatalf("inner must see the original demand: %+v", inner.demands)
	}
	if sourced.Resolution != nil {
		t.Fatalf("no resolution may be annotated: %+v", sourced.Resolution)
	}
}

func TestRedirectsToApprovedReplacementAndAnnotates(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
	})
	inner := &fakeInner{result: procurement.SourcedPart{
		Status: "review",
		Offers: []procurement.Offer{{
			Provider:               "mouser",
			ManufacturerPartNumber: "TMP421AQDCNRQ1",
			ReviewRequired:         true,
		}},
	}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(inner.demands) != 1 || inner.demands[0].PartNumber != "TMP421AQDCNRQ1" {
		t.Fatalf("inner must see the replacement demand: %+v", inner.demands)
	}
	if sourced.Demand.PartNumber != "TMP421-Q1" {
		t.Fatalf("the BOM line must keep its original identity: %+v", sourced.Demand)
	}
	resolution := sourced.Resolution
	if resolution == nil ||
		resolution.ApprovedBy != "J. Ihlenburg" ||
		resolution.OriginalPartNumber != "TMP421-Q1" ||
		resolution.ReplacementPartNumber != "TMP421AQDCNRQ1" {
		t.Fatalf("resolution annotation missing or wrong: %+v", resolution)
	}
	// No SKU pin: review must NOT be lifted.
	if resolution.ReviewLifted || sourced.Status != "review" {
		t.Fatalf("unpinned resolution must never lift review: %+v", sourced)
	}
}

func stockVerifiedPlan(quantity int) *procurement.PurchasePlan {
	return &procurement.PurchasePlan{
		RequiredQuantity:  quantity,
		PurchasedQuantity: quantity,
		Currency:          "EUR",
		StockVerified:     true,
	}
}

func TestPinnedSKULiftsReviewWhenStockVerified(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
		Provider:     "mouser",
		ProviderSKU:  "595-TMP421AQDCNRQ1",
	})
	inner := &fakeInner{result: procurement.SourcedPart{
		Status: "review",
		Offers: []procurement.Offer{
			{
				Provider:              "digikey",
				DistributorPartNumber: "296-OTHER-ND",
				ReviewRequired:        true,
				CandidatePlan:         stockVerifiedPlan(10),
			},
			{
				Provider:              "mouser",
				DistributorPartNumber: "595-TMP421AQDCNRQ1",
				ReviewRequired:        true,
				CandidatePlan:         stockVerifiedPlan(10),
			},
		},
	}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if sourced.Status != "priced" || !sourced.Resolution.ReviewLifted {
		t.Fatalf("expected the pinned SKU to lift review: %+v", sourced)
	}
	if sourced.Offer == nil ||
		sourced.Offer.DistributorPartNumber != "595-TMP421AQDCNRQ1" ||
		sourced.Offer.ReviewRequired ||
		sourced.Offer.SelectedPlan == nil {
		t.Fatalf("the lifted offer must be the pinned SKU with a selected plan: %+v", sourced.Offer)
	}
	// The other provider's offer stays review-required.
	for _, offer := range sourced.Offers {
		if offer.Provider == "digikey" && !offer.ReviewRequired {
			t.Fatalf("unpinned offers must keep their review flag: %+v", offer)
		}
	}
}

func TestPinnedSKUWithoutVerifiedStockDoesNotLift(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
		Provider:     "mouser",
		ProviderSKU:  "595-TMP421AQDCNRQ1",
	})
	unverified := stockVerifiedPlan(10)
	unverified.StockVerified = false
	inner := &fakeInner{result: procurement.SourcedPart{
		Status: "review",
		Offers: []procurement.Offer{{
			Provider:              "mouser",
			DistributorPartNumber: "595-TMP421AQDCNRQ1",
			ReviewRequired:        true,
			CandidatePlan:         unverified,
		}},
	}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if sourced.Status != "review" || sourced.Resolution.ReviewLifted {
		t.Fatalf("unverified stock must never lift review: %+v", sourced)
	}
}

func TestPinnedSKUWithShortPlanDoesNotLift(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
		Provider:     "mouser",
		ProviderSKU:  "595-TMP421AQDCNRQ1",
	})
	inner := &fakeInner{result: procurement.SourcedPart{
		Status: "review",
		Offers: []procurement.Offer{{
			Provider:              "mouser",
			DistributorPartNumber: "595-TMP421AQDCNRQ1",
			ReviewRequired:        true,
			CandidatePlan:         stockVerifiedPlan(5),
		}},
	}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if sourced.Status != "review" || sourced.Resolution.ReviewLifted {
		t.Fatalf("a short plan must never lift review: %+v", sourced)
	}
}

func TestSafeResultIsAnnotatedButNeverModified(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
		Provider:     "mouser",
		ProviderSKU:  "595-TMP421AQDCNRQ1",
	})
	safeOffer := procurement.Offer{
		Provider:              "digikey",
		DistributorPartNumber: "296-SAFE-ND",
		SelectedPlan:          stockVerifiedPlan(10),
	}
	inner := &fakeInner{result: procurement.SourcedPart{
		Status: "priced",
		Offer:  &safeOffer,
		Offers: []procurement.Offer{safeOffer},
	}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if sourced.Status != "priced" ||
		sourced.Offer.DistributorPartNumber != "296-SAFE-ND" ||
		sourced.Resolution == nil ||
		sourced.Resolution.ReviewLifted {
		t.Fatalf("a safe result must keep its selection, annotated only: %+v", sourced)
	}
}

func TestRevokedResolutionIsIgnored(t *testing.T) {
	store := testStoreWithResolution(t, resolutions.Replacement{
		Manufacturer: "Texas Instruments",
		PartNumber:   "TMP421AQDCNRQ1",
	})
	records, err := store.List(context.Background(), "", "", 10, false)
	if err != nil || len(records) != 1 {
		t.Fatalf("list: %v %d", err, len(records))
	}
	preview, err := store.Revoke(
		context.Background(),
		records[0].ResolutionID,
		"J. Ihlenburg",
		"",
		"",
		time.Now(),
	)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if _, err := store.Revoke(
		context.Background(),
		records[0].ResolutionID,
		"J. Ihlenburg",
		"",
		preview.ApplyToken,
		time.Now(),
	); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	inner := &fakeInner{result: procurement.SourcedPart{Status: "not_found"}}
	resolver, _ := NewResolutionAware(store, inner)
	sourced, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1"))
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(inner.demands) != 1 || inner.demands[0].PartNumber != "TMP421-Q1" {
		t.Fatalf("a revoked resolution must not redirect: %+v", inner.demands)
	}
	if sourced.Resolution != nil {
		t.Fatalf("a revoked resolution must not annotate: %+v", sourced.Resolution)
	}
}

func TestBrokenStoreFailsClosed(t *testing.T) {
	inner := &fakeInner{result: procurement.SourcedPart{Status: "not_found"}}
	resolver, _ := NewResolutionAware(failingSource{}, inner)
	if _, err := resolver.Lookup(context.Background(), demandFor("TMP421-Q1")); err == nil {
		t.Fatal("a broken resolutions store must fail the lookup explicitly")
	}
	if len(inner.demands) != 0 {
		t.Fatal("the inner resolver must not run when the store fails")
	}
}
