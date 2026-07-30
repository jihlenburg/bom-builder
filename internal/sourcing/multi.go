package sourcing

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// ProviderResolver binds a stable provider name to its resolver.
type ProviderResolver struct {
	Name     string
	Resolver Resolver
}

// MultiResolver compares normalized safe plans from independently configured providers.
type MultiResolver struct {
	providers []ProviderResolver
}

// NewMultiResolver validates and constructs a multi-provider resolver.
func NewMultiResolver(providers []ProviderResolver) (*MultiResolver, error) {
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
	return &MultiResolver{providers: validated}, nil
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
		currency := safeOffers[0].SelectedPlan.Currency
		for _, offer := range safeOffers[1:] {
			if !strings.EqualFold(currency, offer.SelectedPlan.Currency) {
				clearSelectedPlans(output.Offers)
				output.Status = "unavailable"
				output.IssueCode = "CURRENCY_CONVERSION_REQUIRED"
				output.IssueMessage = "safe provider plans use different currencies and cannot be compared"
				return output, nil
			}
		}
		sort.SliceStable(safeOffers, func(left, right int) bool {
			a, b := safeOffers[left].SelectedPlan, safeOffers[right].SelectedPlan
			if a.ExtendedPrice != b.ExtendedPrice {
				return a.ExtendedPrice < b.ExtendedPrice
			}
			if a.SurplusQuantity != b.SurplusQuantity {
				return a.SurplusQuantity < b.SurplusQuantity
			}
			return safeOffers[left].Provider < safeOffers[right].Provider
		})
		selected := safeOffers[0]
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
