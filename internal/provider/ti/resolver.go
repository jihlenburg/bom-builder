package ti

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Resolver turns TI Store inventory and pricing into safe normalized offers.
type Resolver struct {
	client *Client
}

// NewResolver constructs a resolver around a TI Store client.
func NewResolver(client *Client) (*Resolver, error) {
	if client == nil {
		return nil, fmt.Errorf("TI client is required")
	}
	return &Resolver{client: client}, nil
}

// Lookup resolves one Texas Instruments orderable part number.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	result := procurement.SourcedPart{Demand: demand}
	if !supportsManufacturer(demand.Manufacturer) {
		result.Status = "not_applicable"
		result.IssueCode = "PROVIDER_NOT_APPLICABLE"
		result.IssueMessage = "TI direct-store pricing applies only to Texas Instruments parts"
		return result, nil
	}
	product, err := resolver.client.Product(ctx, demand.PartNumber)
	if err != nil {
		var providerError *Error
		if errors.As(err, &providerError) && providerError.Kind == "not_found" {
			result.Status = "not_found"
			result.IssueCode = "PART_NOT_FOUND"
			result.IssueMessage = "TI Store returned no matching orderable part"
			return result, nil
		}
		return result, err
	}

	query := normalizePartNumber(demand.PartNumber)
	orderable := normalizePartNumber(product.TIPartNumber)
	generic := normalizePartNumber(product.GenericPartNumber)
	exact := query != "" && query == orderable
	related := exact || query == generic ||
		plausiblyRelated(query, orderable) ||
		plausiblyRelated(query, generic)
	if !related {
		result.Status = "not_found"
		result.IssueCode = "PART_NUMBER_MISMATCH"
		result.IssueMessage = "TI Store returned an unrelated product identifier"
		return result, nil
	}

	schedule := selectSchedule(product.Pricing, resolver.client.Currency())
	offer := baseOffer(demand, product, exact)
	result.Offer = &offer
	result.CandidateCount = 1
	if schedule == nil {
		result.Status = "unavailable"
		result.IssueCode = "CURRENCY_UNAVAILABLE"
		result.IssueMessage = "TI Store returned no pricing in the requested currency"
		return result, nil
	}
	priceBreaks := normalizePriceBreaks(schedule.PriceBreaks, schedule.Currency)
	offer.PriceBreaks = priceBreaks
	if len(priceBreaks) == 0 {
		result.Offer = &offer
		result.Status = "unavailable"
		result.IssueCode = "PRICE_UNAVAILABLE"
		result.IssueMessage = "TI Store returned no valid price breaks"
		return result, nil
	}
	if product.OrderLimit != nil && demand.RequiredQuantity > *product.OrderLimit {
		result.Offer = &offer
		result.Status = "shortage"
		result.IssueCode = "ORDER_LIMIT"
		result.IssueMessage = fmt.Sprintf(
			"TI Store order limit %d is below required quantity %d",
			*product.OrderLimit,
			demand.RequiredQuantity,
		)
		return result, nil
	}

	availableForPlan := product.QuantityAvailable
	if availableForPlan != nil && product.OrderLimit != nil &&
		*product.OrderLimit < *availableForPlan {
		limited := *product.OrderLimit
		availableForPlan = &limited
	}
	family := procurement.PurchaseFamily{
		ID:                     firstNonEmpty(product.TIPartNumber, product.Query),
		PackageType:            product.PackageType,
		PackagingMode:          packagingMode(product.PackageCarrier),
		MinimumOrderQuantity:   product.MinimumOrderQuantity,
		BasePricingStrategy:    "TI Store price break",
		StrategyMode:           "price_break",
		AllowMixingAsRemainder: true,
		AvailableQuantity:      availableForPlan,
		PriceBreaks:            priceBreaks,
	}
	plan, optimizeErr := procurement.OptimizePurchaseFamilies(
		demand.RequiredQuantity,
		[]procurement.PurchaseFamily{family},
	)
	if optimizeErr != nil {
		result.Offer = &offer
		result.Status = "unavailable"
		result.IssueCode = "INVALID_PROVIDER_PRICE"
		result.IssueMessage = "TI Store pricing could not form a valid purchase plan"
		return result, nil
	}
	offer.CandidatePlan = plan
	result.Offer = &offer
	if plan == nil {
		result.Status = "shortage"
		result.IssueCode = "INSUFFICIENT_STOCK"
		result.IssueMessage = "TI Store stock or order limits cannot cover the required quantity"
		return result, nil
	}

	lifecycleReview := lifecycleRequiresReview(product.LifeCycle)
	switch {
	case !exact:
		result.Status = "review"
		result.IssueCode = "REVIEW_REQUIRED"
		result.IssueMessage = "TI resolved a non-exact orderable part number"
		offer.ReviewRequired = true
	case lifecycleReview:
		result.Status = "review"
		result.IssueCode = "LIFECYCLE_REVIEW_REQUIRED"
		result.IssueMessage = "TI reported a lifecycle state that requires engineering review"
		offer.ReviewRequired = true
	case !plan.StockVerified:
		result.Status = "stock_unknown"
		result.IssueCode = "STOCK_UNKNOWN"
		result.IssueMessage = "TI Store did not report stock; the plan was not selected"
	default:
		result.Status = "priced"
		offer.SelectedPlan = plan
	}
	result.Offer = &offer
	return result, nil
}

func baseOffer(
	demand procurement.Demand,
	product Product,
	exact bool,
) procurement.Offer {
	matchMethod := "candidate"
	if exact {
		matchMethod = "exact"
	}
	return procurement.Offer{
		Provider:               "ti",
		DistributorPartNumber:  product.TIPartNumber,
		ManufacturerPartNumber: firstNonEmpty(product.TIPartNumber, product.GenericPartNumber),
		Manufacturer:           "Texas Instruments",
		Description:            product.Description,
		MatchMethod:            matchMethod,
		ReviewRequired:         !exact,
		RequiredQuantity:       demand.RequiredQuantity,
		AvailableQuantity:      product.QuantityAvailable,
		Availability:           availabilityText(product),
		LifecycleStatus:        product.LifeCycle,
		Packaging:              packagingSummary(product),
		MinimumOrderQuantity:   product.MinimumOrderQuantity,
		StandardPackQuantity:   product.StandardPackQuantity,
		OrderLimit:             product.OrderLimit,
		ProductURL:             product.BuyNowURL,
	}
}

func selectSchedule(
	schedules []PricingSchedule,
	currency string,
) *PricingSchedule {
	for index := range schedules {
		if strings.EqualFold(schedules[index].Currency, currency) {
			return &schedules[index]
		}
	}
	return nil
}

func normalizePriceBreaks(
	raw []PriceBreak,
	currency string,
) []procurement.PriceBreak {
	normalized := make([]procurement.PriceBreak, 0, len(raw))
	for _, priceBreak := range raw {
		price, err := money.Parse(priceBreak.Price.String())
		if err != nil || price.Micros() <= 0 || priceBreak.Quantity < 1 {
			continue
		}
		normalized = append(normalized, procurement.PriceBreak{
			Quantity:  priceBreak.Quantity,
			UnitPrice: price,
			Currency:  strings.ToUpper(currency),
		})
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		if normalized[left].Quantity != normalized[right].Quantity {
			return normalized[left].Quantity < normalized[right].Quantity
		}
		return normalized[left].UnitPrice < normalized[right].UnitPrice
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].Quantity == normalized[index].Quantity {
			return nil
		}
	}
	return normalized
}

func supportsManufacturer(manufacturer string) bool {
	normalized := normalizeManufacturer(manufacturer)
	return normalized == "ti" ||
		normalized == "texas instruments" ||
		normalized == "texas instruments incorporated"
}

func normalizeManufacturer(value string) string {
	words := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(value)), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
	filtered := words[:0]
	for _, word := range words {
		switch word {
		case "inc", "incorporated", "corp", "corporation", "company", "co",
			"ltd", "limited":
			continue
		default:
			filtered = append(filtered, word)
		}
	}
	return strings.Join(filtered, " ")
}

func normalizePartNumber(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToUpper(value) {
		if character >= 'A' && character <= 'Z' || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func plausiblyRelated(query, candidate string) bool {
	if query == "" || candidate == "" {
		return false
	}
	shared := 0
	for shared < len(query) && shared < len(candidate) &&
		query[shared] == candidate[shared] {
		shared++
	}
	minimum := min(6, len(query))
	return shared >= minimum &&
		shared*10 >= min(len(query), len(candidate))*6
}

func packagingMode(carrier string) string {
	carrier = strings.TrimSpace(carrier)
	lower := strings.ToLower(carrier)
	switch {
	case strings.Contains(lower, "cut tape"):
		return "Cut Tape"
	case strings.Contains(lower, "reel"), strings.Contains(lower, "t&r"):
		return "Full Reel"
	case strings.Contains(lower, "tray"):
		return "Tray"
	case strings.Contains(lower, "tube"):
		return "Tube"
	case strings.Contains(lower, "bulk"):
		return "Bulk"
	default:
		return carrier
	}
}

func packagingSummary(product Product) string {
	values := make([]string, 0, 2)
	for _, value := range []string{product.PackageType, product.PackageCarrier} {
		value = strings.TrimSpace(value)
		if value != "" && !containsFold(values, value) {
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

func availabilityText(product Product) string {
	parts := make([]string, 0, 3)
	if product.QuantityAvailable != nil {
		parts = append(parts, strconv.Itoa(*product.QuantityAvailable)+" in stock")
	}
	if product.OrderLimit != nil {
		parts = append(parts, "order limit "+strconv.Itoa(*product.OrderLimit))
	}
	if strings.TrimSpace(product.LifeCycle) != "" {
		parts = append(parts, strings.TrimSpace(product.LifeCycle))
	}
	return strings.Join(parts, "; ")
}

func lifecycleRequiresReview(lifecycle string) bool {
	lifecycle = strings.ToUpper(strings.TrimSpace(lifecycle))
	if lifecycle == "" || lifecycle == "ACTIVE" {
		return false
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
