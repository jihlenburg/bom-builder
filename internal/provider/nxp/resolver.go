package nxp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Store is the browser boundary used by the NXP resolver and its tests.
type Store interface {
	Search(context.Context, string) (*SearchResult, error)
	PartDetail(context.Context, string, string) (*PartDetail, error)
	Currency() string
}

// Resolver turns NXP public-store evidence into conservative normalized offers.
type Resolver struct {
	store Store
}

// NewResolver constructs an NXP resolver.
func NewResolver(store Store) (*Resolver, error) {
	if store == nil {
		return nil, fmt.Errorf("NXP store is required")
	}
	return &Resolver{store: store}, nil
}

// Lookup queries NXP only for NXP/Freescale manufacturer lines.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	output := procurement.SourcedPart{Demand: demand}
	if !supportsManufacturer(demand.Manufacturer) {
		output.Status = "not_applicable"
		output.IssueCode = "PROVIDER_NOT_APPLICABLE"
		output.IssueMessage = "NXP direct-store pricing applies only to NXP/Freescale parts"
		return output, nil
	}
	result, err := resolver.store.Search(ctx, demand.PartNumber)
	if err != nil {
		return output, err
	}
	if result == nil {
		output.Status = "not_found"
		output.IssueCode = "PART_NOT_FOUND"
		output.IssueMessage = "NXP Store returned no related product"
		return output, nil
	}
	exact := normalizePartNumber(demand.PartNumber) == normalizePartNumber(result.PartID)
	offer := procurement.Offer{
		Provider:               "nxp",
		DistributorPartNumber:  result.PartID,
		ManufacturerPartNumber: result.PartID,
		Manufacturer:           "NXP Semiconductors",
		Description:            result.Description,
		MatchMethod:            map[bool]string{true: "exact", false: "candidate"}[exact],
		ReviewRequired:         !exact,
		RequiredQuantity:       demand.RequiredQuantity,
		AvailableQuantity:      result.StockQuantity,
		Availability:           availabilityText(result),
		Packaging:              packagingSummary(result),
		ProductURL:             result.ProductURL,
	}
	output.Offer = &offer
	output.CandidateCount = 1
	if !result.BuyDirect {
		output.Status = "unavailable"
		output.IssueCode = "DIRECT_BUY_UNAVAILABLE"
		output.IssueMessage = "NXP lists the part but does not offer direct purchase"
		return output, nil
	}
	if !validCurrency(result.Currency) ||
		!strings.EqualFold(result.Currency, resolver.store.Currency()) {
		output.Status = "unavailable"
		output.IssueCode = "CURRENCY_UNAVAILABLE"
		output.IssueMessage = "NXP Store pricing currency was not proven"
		return output, nil
	}
	priceBreaks := normalizePriceBreaks(result)
	offer.PriceBreaks = priceBreaks
	if len(priceBreaks) == 0 {
		output.Offer = &offer
		output.Status = "unavailable"
		output.IssueCode = "PRICE_UNAVAILABLE"
		output.IssueMessage = "NXP Store returned no valid direct price breaks"
		return output, nil
	}

	detail, detailErr := resolver.store.PartDetail(
		ctx,
		demand.PartNumber,
		result.PartID,
	)
	moqConfirmed := detail != nil && detail.MinimumOrderQuantity != nil
	minimumOrderQuantity := 1
	orderMultiple := 0
	if detail != nil {
		if detail.MinimumOrderQuantity != nil {
			minimumOrderQuantity = *detail.MinimumOrderQuantity
		}
		if detail.MinimumPackageQuantity != nil {
			orderMultiple = *detail.MinimumPackageQuantity
		}
	}
	offer.MinimumOrderQuantity = minimumOrderQuantity
	offer.OrderMultiple = orderMultiple
	family := procurement.PurchaseFamily{
		ID:                   result.PartID,
		PackagingMode:        result.PackingDescription,
		MinimumOrderQuantity: minimumOrderQuantity,
		OrderMultiple:        orderMultiple,
		BasePricingStrategy:  "NXP direct price break",
		StrategyMode:         "static",
		AvailableQuantity:    result.StockQuantity,
		PriceBreaks:          priceBreaks,
	}
	plan, optimizeErr := procurement.OptimizePurchaseFamilies(
		demand.RequiredQuantity,
		[]procurement.PurchaseFamily{family},
	)
	if optimizeErr != nil {
		output.Offer = &offer
		output.Status = "unavailable"
		output.IssueCode = "INVALID_PROVIDER_PRICE"
		output.IssueMessage = "NXP Store pricing could not form a valid purchase plan"
		return output, nil
	}
	offer.CandidatePlan = plan
	output.Offer = &offer
	if plan == nil {
		output.Status = "shortage"
		output.IssueCode = "INSUFFICIENT_STOCK"
		output.IssueMessage = "NXP Store stock cannot cover the required purchase plan"
		return output, nil
	}

	switch {
	case !exact:
		output.Status = "review"
		output.IssueCode = "REVIEW_REQUIRED"
		output.IssueMessage = "NXP resolved a non-exact orderable part number"
		offer.ReviewRequired = true
	case !moqConfirmed:
		output.Status = "review"
		output.IssueCode = "MOQ_REVIEW_REQUIRED"
		output.IssueMessage = "NXP direct price is available but MOQ was not confirmed"
		if detailErr != nil {
			output.IssueMessage += ": " + detailErr.Error()
		}
		offer.ReviewRequired = true
	case !plan.StockVerified:
		output.Status = "stock_unknown"
		output.IssueCode = "STOCK_UNKNOWN"
		output.IssueMessage = "NXP Store did not report stock; the plan was not selected"
	default:
		output.Status = "priced"
		offer.SelectedPlan = plan
	}
	output.Offer = &offer
	return output, nil
}

func supportsManufacturer(manufacturer string) bool {
	words := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(manufacturer)), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
	filtered := words[:0]
	for _, word := range words {
		switch word {
		case "inc", "incorporated", "corp", "corporation", "company", "co",
			"ltd", "limited", "semiconductor", "semiconductors":
			continue
		default:
			filtered = append(filtered, word)
		}
	}
	normalized := strings.Join(filtered, " ")
	return normalized == "nxp" || normalized == "freescale"
}

func normalizePriceBreaks(result *SearchResult) []procurement.PriceBreak {
	raw := result.StepPrices
	if len(raw) == 0 && result.UnitPrice != "" {
		raw = []StepPrice{{Quantity: 1, Price: result.UnitPrice}}
	}
	normalized := make([]procurement.PriceBreak, 0, len(raw))
	for _, priceBreak := range raw {
		price, err := money.Parse(priceBreak.Price.String())
		if err != nil || price.Micros() <= 0 || priceBreak.Quantity < 1 {
			continue
		}
		normalized = append(normalized, procurement.PriceBreak{
			Quantity:  priceBreak.Quantity,
			UnitPrice: price,
			Currency:  strings.ToUpper(result.Currency),
		})
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].Quantity < normalized[right].Quantity
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].Quantity == normalized[index].Quantity {
			return nil
		}
	}
	return normalized
}

func availabilityText(result *SearchResult) string {
	values := make([]string, 0, 2)
	if result.StockQuantity != nil {
		values = append(values, strconv.Itoa(*result.StockQuantity)+" in stock")
	}
	if value := strings.TrimSpace(result.Availability); value != "" &&
		!containsFold(values, value) {
		values = append(values, value)
	}
	return strings.Join(values, "; ")
}

func packagingSummary(result *SearchResult) string {
	values := make([]string, 0, 2)
	for _, value := range []string{result.PackingName, result.PackingDescription} {
		if value = strings.TrimSpace(value); value != "" &&
			!containsFold(values, value) {
			values = append(values, value)
		}
	}
	return strings.Join(values, " | ")
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}
