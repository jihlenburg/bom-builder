// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package procurement

import (
	"testing"

	"github.com/jihlenburg/bom-builder/internal/money"
)

func decimal(t *testing.T, value string) money.Decimal {
	t.Helper()
	parsed, err := money.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestOptimizerBuysUpWhenNextBreakCostsLess(t *testing.T) {
	t.Parallel()
	stock := 5000
	plan, err := OptimizePurchaseFamilies(950, []PurchaseFamily{{
		ID:                     "cut_tape",
		PackagingMode:          "Cut Tape",
		StrategyMode:           "price_break",
		BasePricingStrategy:    "requested quantity",
		AllowMixingAsRemainder: true,
		AvailableQuantity:      &stock,
		PriceBreaks: []PriceBreak{
			{Quantity: 1, UnitPrice: decimal(t, "0.10"), Currency: "EUR"},
			{Quantity: 1000, UnitPrice: decimal(t, "0.09"), Currency: "EUR"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.PurchasedQuantity != 1000 || plan.ExtendedPrice.String() != "90.000000" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if !plan.StockVerified || plan.PricingStrategy != "next price break" {
		t.Fatalf("unsafe or unexpected plan: %#v", plan)
	}
}

func TestOptimizerRejectsPlanBeyondKnownStock(t *testing.T) {
	t.Parallel()
	stock := 949
	plan, err := OptimizePurchaseFamilies(950, []PurchaseFamily{{
		ID:                "cut_tape",
		AvailableQuantity: &stock,
		PriceBreaks: []PriceBreak{{
			Quantity: 1, UnitPrice: decimal(t, "0.10"), Currency: "EUR",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("plan should not exceed stock: %#v", plan)
	}
}

func TestOptimizerBuildsCheaperMixedPlan(t *testing.T) {
	t.Parallel()
	stock := 20_000
	plan, err := OptimizePurchaseFamiliesWithPreference(6000, []PurchaseFamily{
		{
			ID:                     "cut_tape",
			PackagingMode:          "Cut Tape",
			StrategyMode:           "price_break",
			BasePricingStrategy:    "requested quantity",
			AllowMixingAsRemainder: true,
			AvailableQuantity:      &stock,
			PriceBreaks: []PriceBreak{
				{Quantity: 1, UnitPrice: decimal(t, "1.00"), Currency: "EUR"},
				{Quantity: 500, UnitPrice: decimal(t, "0.60"), Currency: "EUR"},
				{Quantity: 1000, UnitPrice: decimal(t, "0.55"), Currency: "EUR"},
			},
		},
		{
			ID:                   "full_reel",
			PackagingMode:        "Full Reel",
			MinimumOrderQuantity: 1800,
			OrderMultiple:        1800,
			FullReelQuantity:     1800,
			StrategyMode:         "full_reel",
			AllowMixingAsBulk:    true,
			MixQuantity:          1800,
			AvailableQuantity:    &stock,
			PriceBreaks: []PriceBreak{{
				Quantity: 1800, UnitPrice: decimal(t, "0.40"), Currency: "EUR",
			}},
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.ExtendedPrice.String() != "2520.000000" ||
		plan.OrderPlan != "3 reels x 1800 + 600 cut tape" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if len(plan.Legs) != 2 || !plan.StockVerified {
		t.Fatalf("unexpected legs: %#v", plan.Legs)
	}
}

func TestOptimizerRefusesCrossCurrencyPlanComparison(t *testing.T) {
	t.Parallel()
	// Two viable single-currency families in different currencies produce
	// plans whose raw micro-values are not commensurable: silently picking
	// the "cheaper" one can select the economically more expensive plan.
	// Failed currency normalization must be an explicit state, mirroring
	// the sourcing layer's CURRENCY_CONVERSION_REQUIRED refusal.
	stock := 10_000
	plan, err := OptimizePurchaseFamilies(100, []PurchaseFamily{
		{
			ID:                "eur_tape",
			AvailableQuantity: &stock,
			PriceBreaks: []PriceBreak{{
				Quantity: 1, UnitPrice: decimal(t, "0.10"), Currency: "EUR",
			}},
		},
		{
			ID:                "usd_tape",
			AvailableQuantity: &stock,
			PriceBreaks: []PriceBreak{{
				Quantity: 1, UnitPrice: decimal(t, "0.09"), Currency: "USD",
			}},
		},
	})
	if err == nil {
		t.Fatalf("cross-currency comparison should fail, got plan %#v", plan)
	}
}

func TestOptimizerNeverMixesCurrencies(t *testing.T) {
	t.Parallel()
	_, err := OptimizePurchaseFamilies(100, []PurchaseFamily{{
		ID: "bad",
		PriceBreaks: []PriceBreak{
			{Quantity: 1, UnitPrice: decimal(t, "1"), Currency: "EUR"},
			{Quantity: 10, UnitPrice: decimal(t, "1"), Currency: "USD"},
		},
	}})
	if err == nil {
		t.Fatal("mixed-currency family should fail")
	}
}
