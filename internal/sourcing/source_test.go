// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"errors"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type stubResolver struct {
	results map[string]procurement.SourcedPart
	err     error
}

func (stub stubResolver) Lookup(
	_ context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	if stub.err != nil {
		return procurement.SourcedPart{Demand: demand}, stub.err
	}
	return stub.results[demand.PartNumber], nil
}

func TestSourceTotalsOnlySelectedCommonCurrencyPlans(t *testing.T) {
	t.Parallel()
	first := demandWithPlan(t, "A", "EUR", "10")
	second := demandWithPlan(t, "B", "EUR", "5.50")
	result := Source(context.Background(), stubResolver{results: map[string]procurement.SourcedPart{
		"A": first,
		"B": second,
	}}, []procurement.Demand{first.Demand, second.Demand}, 10)
	if result.ExitCode != 0 || result.Summary.TotalCost == nil ||
		result.Summary.TotalCost.String() != "15.500000" ||
		result.Summary.CostPerUnit.String() != "1.550000" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSourceFailsClosedForProviderError(t *testing.T) {
	t.Parallel()
	demand := procurement.Demand{PartNumber: "A", RequiredQuantity: 1}
	result := Source(
		context.Background(),
		stubResolver{err: errors.New("mouser authentication: rejected")},
		[]procurement.Demand{demand},
		1,
	)
	if result.ExitCode != 4 || result.Summary.TotalCost != nil ||
		result.Summary.ProviderErrors != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSourceOmitsMixedCurrencyTotal(t *testing.T) {
	t.Parallel()
	first := demandWithPlan(t, "A", "EUR", "10")
	second := demandWithPlan(t, "B", "USD", "5")
	result := Source(context.Background(), stubResolver{results: map[string]procurement.SourcedPart{
		"A": first,
		"B": second,
	}}, []procurement.Demand{first.Demand, second.Demand}, 1)
	if result.ExitCode != 4 || result.Summary.TotalCost != nil ||
		len(result.Errors) != 1 || result.Errors[0].Code != "MIXED_CURRENCY" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSourceRejectsUnverifiedSelectedPlan(t *testing.T) {
	t.Parallel()
	part := demandWithPlan(t, "A", "EUR", "10")
	part.Offer.SelectedPlan.StockVerified = false
	result := Source(
		context.Background(),
		stubResolver{results: map[string]procurement.SourcedPart{"A": part}},
		[]procurement.Demand{part.Demand},
		1,
	)
	if result.ExitCode != 4 || result.Summary.PricedCount != 0 ||
		result.Summary.TotalCost != nil ||
		result.Parts[0].Status != "provider_error" {
		t.Fatalf("unsafe plan was accepted: %#v", result)
	}
}

func TestSourceCountsUnknownStatusAsContractError(t *testing.T) {
	t.Parallel()
	// A resolver returning a status outside the contract must not fall
	// through the summary uncounted: the counts would stop adding up to
	// the line count and the line would degrade silently. Everywhere else
	// contract violations map to INTERNAL_CONTRACT_ERROR; this must too.
	part := procurement.SourcedPart{
		Demand: procurement.Demand{PartNumber: "A", RequiredQuantity: 1},
		Status: "backordered",
	}
	result := Source(
		context.Background(),
		stubResolver{results: map[string]procurement.SourcedPart{"A": part}},
		[]procurement.Demand{part.Demand},
		1,
	)
	if result.Summary.ProviderErrors != 1 || result.Parts[0].Status != "provider_error" {
		t.Fatalf("unknown status fell through: %#v", result)
	}
	found := false
	for _, issue := range result.Errors {
		if issue.Code == "INTERNAL_CONTRACT_ERROR" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing INTERNAL_CONTRACT_ERROR issue: %#v", result.Errors)
	}
}

func demandWithPlan(
	t *testing.T,
	partNumber, currency, total string,
) procurement.SourcedPart {
	t.Helper()
	amount, err := money.Parse(total)
	if err != nil {
		t.Fatal(err)
	}
	demand := procurement.Demand{PartNumber: partNumber, RequiredQuantity: 1}
	return procurement.SourcedPart{
		Demand: demand,
		Status: "priced",
		Offer: &procurement.Offer{
			SelectedPlan: &procurement.PurchasePlan{
				RequiredQuantity:  1,
				PurchasedQuantity: 1,
				ExtendedPrice:     amount,
				Currency:          currency,
				StockVerified:     true,
			},
		},
	}
}
