// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// ProviderResolver binds a stable provider name to its resolver.
type ProviderResolver struct {
	Name     string
	Resolver Resolver
}

// MultiResolver compares normalized safe plans from independently configured providers.
type MultiResolver struct {
	providers  []ProviderResolver
	conversion *CurrencyConversion
}

// NewMultiResolver validates and constructs a multi-provider resolver.
// Without quotes, plans in different currencies are never compared.
func NewMultiResolver(providers []ProviderResolver) (*MultiResolver, error) {
	return NewMultiResolverWithFX(providers, nil)
}

// NewMultiResolverWithFX behaves like NewMultiResolver; with a conversion
// it ranks plans by their value in the target currency, so the cheapest
// offer can win even when providers quote different currencies. The
// conversion decides ranking only: a selected plan keeps the currency
// the provider charges in.
func NewMultiResolverWithFX(
	providers []ProviderResolver,
	conversion *CurrencyConversion,
) (*MultiResolver, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("at least one provider resolver is required")
	}
	seen := map[string]struct{}{}
	validated := make([]ProviderResolver, 0, len(providers))
	for _, provider := range providers {
		name := strings.ToLower(strings.TrimSpace(provider.Name))
		if name == "" || provider.Resolver == nil {
			return nil, fmt.Errorf("provider name and resolver are required")
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate provider %s", name)
		}
		seen[name] = struct{}{}
		validated = append(validated, ProviderResolver{
			Name: name, Resolver: provider.Resolver,
		})
	}
	return &MultiResolver{providers: validated, conversion: conversion}, nil
}

// Lookup queries selected providers and chooses only comparable safe plans.
func (resolver *MultiResolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	type providerResult struct {
		name   string
		result procurement.SourcedPart
		err    error
	}
	results := make([]providerResult, 0, len(resolver.providers))
	for _, provider := range resolver.providers {
		result, err := provider.Resolver.Lookup(ctx, demand)
		results = append(results, providerResult{
			name: provider.Name, result: result, err: err,
		})
	}

	output := procurement.SourcedPart{
		Demand: demand,
		Offers: []procurement.Offer{},
	}
	var (
		safeOffers     []procurement.Offer
		providerErrors []string
		fallbacks      []procurement.SourcedPart
	)
	for _, provider := range results {
		if provider.err != nil {
			providerErrors = append(
				providerErrors,
				fmt.Sprintf("%s: %s", provider.name, provider.err.Error()),
			)
			continue
		}
		if provider.result.Status == "priced" &&
			(provider.result.Offer == nil ||
				provider.result.Offer.SelectedPlan == nil ||
				!provider.result.Offer.SelectedPlan.StockVerified ||
				provider.result.Offer.ReviewRequired ||
				provider.result.Offer.SelectedPlan.PurchasedQuantity <
					demand.RequiredQuantity) {
			providerErrors = append(
				providerErrors,
				provider.name+": invalid priced-result contract",
			)
			continue
		}
		output.CandidateCount += provider.result.CandidateCount
		if provider.result.Offer != nil {
			offer := *provider.result.Offer
			output.Offers = append(output.Offers, offer)
			if provider.result.Status == "priced" &&
				offer.SelectedPlan != nil &&
				offer.SelectedPlan.StockVerified &&
				!offer.ReviewRequired {
				safeOffers = append(safeOffers, offer)
			}
		}
		if provider.result.Status != "priced" {
			fallbacks = append(fallbacks, provider.result)
		}
	}

	if len(safeOffers) > 0 {
		ranked, rankErr := resolver.comparablePlans(safeOffers)
		if rankErr != nil {
			clearSelectedPlans(output.Offers)
			output.Status = "unavailable"
			output.IssueCode = rankErr.code
			output.IssueMessage = rankErr.message
			return output, nil
		}
		sort.SliceStable(ranked, func(left, right int) bool {
			a, b := ranked[left], ranked[right]
			if a.price != b.price {
				return a.price < b.price
			}
			if a.offer.SelectedPlan.SurplusQuantity != b.offer.SelectedPlan.SurplusQuantity {
				return a.offer.SelectedPlan.SurplusQuantity < b.offer.SelectedPlan.SurplusQuantity
			}
			return a.offer.Provider < b.offer.Provider
		})
		selected := ranked[0].offer
		clearSelectedPlans(output.Offers)
		for index := range output.Offers {
			if output.Offers[index].Provider == selected.Provider &&
				output.Offers[index].DistributorPartNumber == selected.DistributorPartNumber {
				output.Offers[index].SelectedPlan = selected.SelectedPlan
			}
		}
		output.Offer = &selected
		output.Status = "priced"
		if len(providerErrors) > 0 {
			output.IssueCode = "PROVIDER_DEGRADED"
			output.IssueMessage = strings.Join(providerErrors, "; ")
		}
		return output, nil
	}

	if len(providerErrors) == len(resolver.providers) {
		return output, fmt.Errorf(
			"all selected providers failed: %s",
			strings.Join(providerErrors, "; "),
		)
	}
	if len(fallbacks) > 0 {
		sort.SliceStable(fallbacks, func(left, right int) bool {
			return fallbackRank(fallbacks[left].Status) <
				fallbackRank(fallbacks[right].Status)
		})
		selected := fallbacks[0]
		// No fallback outcome may carry a purchase plan: a misbehaving
		// adapter that attached one to a non-priced result would
		// otherwise leak it into offers, where downstream ranking
		// scans for any SelectedPlan regardless of status.
		clearSelectedPlans(output.Offers)
		if selected.Offer != nil && selected.Offer.SelectedPlan != nil {
			cleared := *selected.Offer
			cleared.SelectedPlan = nil
			selected.Offer = &cleared
		}
		output.Status = selected.Status
		output.Offer = selected.Offer
		output.IssueCode = selected.IssueCode
		output.IssueMessage = selected.IssueMessage
		if len(providerErrors) > 0 {
			if output.IssueMessage != "" {
				output.IssueMessage += "; "
			}
			output.IssueMessage += strings.Join(providerErrors, "; ")
		}
		return output, nil
	}
	output.Status = "not_found"
	output.IssueCode = "PART_NOT_FOUND"
	output.IssueMessage = "selected providers returned no candidate"
	return output, nil
}

// rankedOffer pairs one safe offer with the value used to rank it. The
// price is expressed in a single comparison currency, which is the
// shared plan currency when every plan agrees and the conversion target
// otherwise.
type rankedOffer struct {
	offer procurement.Offer
	price money.Decimal
}

// rankingError is a fail-closed reason for refusing to compare plans,
// carrying the stable issue code the line reports.
type rankingError struct {
	code    string
	message string
}

// comparablePlans expresses every safe plan in one currency. Plans that
// already share a currency are compared as they are, so a line that
// never needed converting cannot fail for want of a quote.
func (resolver *MultiResolver) comparablePlans(
	safeOffers []procurement.Offer,
) ([]rankedOffer, *rankingError) {
	ranked := make([]rankedOffer, 0, len(safeOffers))
	currency := safeOffers[0].SelectedPlan.Currency
	mixed := false
	for _, offer := range safeOffers[1:] {
		if !strings.EqualFold(currency, offer.SelectedPlan.Currency) {
			mixed = true
			break
		}
	}
	if !mixed {
		for _, offer := range safeOffers {
			ranked = append(ranked, rankedOffer{
				offer: offer, price: offer.SelectedPlan.ExtendedPrice,
			})
		}
		return ranked, nil
	}
	if resolver.conversion == nil {
		return nil, &rankingError{
			code:    "CURRENCY_CONVERSION_REQUIRED",
			message: "safe provider plans use different currencies and cannot be compared",
		}
	}
	for _, offer := range safeOffers {
		converted, err := resolver.conversion.Table.Convert(
			offer.SelectedPlan.ExtendedPrice,
			offer.SelectedPlan.Currency,
			resolver.conversion.Target,
		)
		if err != nil {
			// One unconvertible plan poisons the comparison: it cannot
			// be ruled out as the cheapest, so selecting among the rest
			// could quietly pick a worse plan.
			return nil, &rankingError{
				code:    "FX_CONVERSION_FAILED",
				message: err.Error(),
			}
		}
		ranked = append(ranked, rankedOffer{offer: offer, price: converted})
	}
	return ranked, nil
}

func clearSelectedPlans(offers []procurement.Offer) {
	for index := range offers {
		offers[index].SelectedPlan = nil
	}
}

func fallbackRank(status string) int {
	switch status {
	case "review":
		return 0
	case "shortage":
		return 1
	case "stock_unknown":
		return 2
	case "unavailable":
		return 3
	case "not_found":
		return 4
	case "not_applicable":
		return 5
	default:
		return 6
	}
}
