package sourcing

import (
	"context"
	"errors"
	"strings"
	"testing"

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
