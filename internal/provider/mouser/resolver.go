// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package mouser

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Searcher is the transport boundary used by the resolver and its tests.
type Searcher interface {
	Search(
		ctx context.Context,
		partNumber string,
		manufacturer string,
		exact bool,
	) ([]Part, error)
}

// Resolver turns raw Mouser parts into normalized, stock-aware offers.
type Resolver struct {
	searcher Searcher
}

// NewResolver constructs a resolver around one search transport.
func NewResolver(searcher Searcher) (*Resolver, error) {
	if searcher == nil {
		return nil, errors.New("Mouser searcher is required")
	}
	return &Resolver{searcher: searcher}, nil
}

// Lookup performs exact lookup first and a review-required broad pass only
// when no exact manufacturer part number is available.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	result := procurement.SourcedPart{Demand: demand}
	apiManufacturer := mouserManufacturerName(demand.Manufacturer)
	exactParts, err := resolver.searcher.Search(
		ctx,
		demand.PartNumber,
		apiManufacturer,
		true,
	)
	if err != nil {
		return result, err
	}
	exactCandidates := filterCandidates(exactParts, demand, true)
	if len(exactCandidates) > 0 {
		return sourceCandidates(demand, exactCandidates, "exact", false), nil
	}

	broadParts, err := resolver.searcher.Search(
		ctx,
		demand.PartNumber,
		apiManufacturer,
		false,
	)
	if err != nil {
		return result, err
	}
	broadCandidates := filterCandidates(broadParts, demand, false)
	if len(broadCandidates) == 0 {
		result.Status = "not_found"
		result.IssueCode = "PART_NOT_FOUND"
		result.IssueMessage = "Mouser returned no manufacturer-compatible candidate"
		return result, nil
	}
	return sourceCandidates(demand, broadCandidates, "candidate", true), nil
}

func sourceCandidates(
	demand procurement.Demand,
	parts []Part,
	matchMethod string,
	reviewRequired bool,
) procurement.SourcedPart {
	type normalizedCandidate struct {
		offer   procurement.Offer
		status  string
		code    string
		message string
	}
	candidates := make([]normalizedCandidate, 0, len(parts))
	for _, part := range parts {
		offer, status, code, message := normalizeOffer(
			demand,
			part,
			matchMethod,
			reviewRequired,
		)
		candidates = append(candidates, normalizedCandidate{
			offer: offer, status: status, code: code, message: message,
		})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return compareNormalizedCandidates(candidates[left], candidates[right]) < 0
	})
	selected := candidates[0]
	// No status squash for loose matches: normalizeOffer already returns
	// "review" for a healthy non-exact candidate and never emits "priced"
	// or a SelectedPlan while reviewRequired is set. Overwriting here
	// would collapse blocking states — shortage, stock_unknown,
	// unavailable — into a bare "review" and hide them; the offer's
	// ReviewRequired flag records the loose match in every case.
	return procurement.SourcedPart{
		Demand:         demand,
		Status:         selected.status,
		Offer:          &selected.offer,
		CandidateCount: len(candidates),
		IssueCode:      selected.code,
		IssueMessage:   selected.message,
	}
}

func normalizeOffer(
	demand procurement.Demand,
	part Part,
	matchMethod string,
	reviewRequired bool,
) (procurement.Offer, string, string, string) {
	priceBreaks := normalizePriceBreaks(part.PriceBreaks)
	available := availableQuantity(part)
	minimum := positiveInt(part.Minimum)
	multiple := positiveInt(part.Multiple)
	packaging, standardPack := packagingDetails(part.ProductAttributes)
	offer := procurement.Offer{
		Provider:               "mouser",
		DistributorPartNumber:  cleanOrderable(part.MouserPartNumber),
		ManufacturerPartNumber: strings.TrimSpace(part.ManufacturerPartNumber),
		Manufacturer:           strings.TrimSpace(part.Manufacturer),
		Description:            strings.TrimSpace(part.Description),
		Category:               strings.TrimSpace(part.Category),
		MatchMethod:            matchMethod,
		ReviewRequired:         reviewRequired,
		RequiredQuantity:       demand.RequiredQuantity,
		AvailableQuantity:      available,
		Availability:           strings.TrimSpace(part.Availability),
		LeadTime:               strings.TrimSpace(part.LeadTime),
		LifecycleStatus:        strings.TrimSpace(part.LifecycleStatus),
		Packaging:              packaging,
		MinimumOrderQuantity:   minimum,
		OrderMultiple:          multiple,
		StandardPackQuantity:   standardPack,
		DatasheetURL:           strings.TrimSpace(part.DataSheetURL),
		ProductURL:             strings.TrimSpace(part.ProductDetailURL),
		PriceBreaks:            priceBreaks,
	}
	if offer.Availability == "" && available != nil {
		offer.Availability = strconv.Itoa(*available) + " In Stock"
	}
	if offer.DistributorPartNumber == "" {
		return offer, "unavailable", "NOT_ORDERABLE", "Mouser does not expose an orderable SKU"
	}
	if len(priceBreaks) == 0 {
		return offer, "unavailable", "PRICE_UNAVAILABLE", "Mouser returned no valid price breaks"
	}

	family := procurement.PurchaseFamily{
		ID:                     offer.DistributorPartNumber,
		PackagingMode:          packaging,
		MinimumOrderQuantity:   minimum,
		OrderMultiple:          multiple,
		BasePricingStrategy:    "requested quantity",
		StrategyMode:           "price_break",
		AllowMixingAsRemainder: true,
		AvailableQuantity:      available,
		PriceBreaks:            priceBreaks,
	}
	plan, err := procurement.OptimizePurchaseFamilies(demand.RequiredQuantity, []procurement.PurchaseFamily{family})
	if err != nil {
		return offer, "unavailable", "INVALID_PROVIDER_PRICE", "Mouser pricing could not form a valid plan"
	}
	offer.CandidatePlan = plan
	switch {
	case plan == nil && available != nil:
		return offer, "shortage", "INSUFFICIENT_STOCK", "Mouser stock cannot cover the required quantity"
	case plan == nil:
		return offer, "unavailable", "PRICE_UNAVAILABLE", "Mouser pricing could not form a purchase plan"
	case !plan.StockVerified:
		return offer, "stock_unknown", "STOCK_UNKNOWN", "Mouser stock was not reported; the plan was not selected"
	case reviewRequired:
		return offer, "review", "REVIEW_REQUIRED", "non-exact manufacturer part number requires engineering review"
	default:
		offer.SelectedPlan = plan
		return offer, "priced", "", ""
	}
}

func filterCandidates(parts []Part, demand procurement.Demand, exact bool) []Part {
	query := normalizePartNumber(demand.PartNumber)
	candidates := make([]Part, 0, len(parts))
	for _, part := range parts {
		candidate := normalizePartNumber(part.ManufacturerPartNumber)
		if query == "" || candidate == "" ||
			!manufacturersMatch(demand.Manufacturer, part.Manufacturer) ||
			isNonComponent(part) {
			continue
		}
		if exact {
			if query != candidate {
				continue
			}
		} else if !plausiblyRelated(query, candidate) {
			continue
		}
		candidates = append(candidates, part)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftStock := integerValue(availableQuantity(candidates[left]))
		rightStock := integerValue(availableQuantity(candidates[right]))
		leftOrderable := cleanOrderable(candidates[left].MouserPartNumber) != ""
		rightOrderable := cleanOrderable(candidates[right].MouserPartNumber) != ""
		if leftOrderable != rightOrderable {
			return leftOrderable
		}
		if len(candidates[left].PriceBreaks) != len(candidates[right].PriceBreaks) {
			return len(candidates[left].PriceBreaks) > len(candidates[right].PriceBreaks)
		}
		if leftStock != rightStock {
			return leftStock > rightStock
		}
		return candidates[left].MouserPartNumber < candidates[right].MouserPartNumber
	})
	return candidates
}

func compareNormalizedCandidates(left, right struct {
	offer   procurement.Offer
	status  string
	code    string
	message string
}) int {
	leftRank, rightRank := statusRank(left.status), statusRank(right.status)
	if leftRank != rightRank {
		return leftRank - rightRank
	}
	leftPlan, rightPlan := left.offer.CandidatePlan, right.offer.CandidatePlan
	if leftPlan != nil && rightPlan != nil &&
		leftPlan.Currency == rightPlan.Currency &&
		leftPlan.ExtendedPrice != rightPlan.ExtendedPrice {
		if leftPlan.ExtendedPrice < rightPlan.ExtendedPrice {
			return -1
		}
		return 1
	}
	leftStock := integerValue(left.offer.AvailableQuantity)
	rightStock := integerValue(right.offer.AvailableQuantity)
	if leftStock != rightStock {
		if leftStock > rightStock {
			return -1
		}
		return 1
	}
	return strings.Compare(left.offer.DistributorPartNumber, right.offer.DistributorPartNumber)
}

func statusRank(status string) int {
	switch status {
	case "priced":
		return 0
	case "review":
		return 1
	case "shortage":
		return 2
	case "stock_unknown":
		return 3
	default:
		return 4
	}
}

func normalizePriceBreaks(raw []RawPriceBreak) []procurement.PriceBreak {
	normalized := make([]procurement.PriceBreak, 0, len(raw))
	for _, priceBreak := range raw {
		currency := strings.ToUpper(strings.TrimSpace(priceBreak.Currency))
		price, err := money.Parse(priceBreak.Price)
		if err != nil || priceBreak.Quantity < 1 || len(currency) != 3 {
			continue
		}
		normalized = append(normalized, procurement.PriceBreak{
			Quantity:  priceBreak.Quantity,
			UnitPrice: price,
			Currency:  currency,
		})
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		if normalized[left].Quantity != normalized[right].Quantity {
			return normalized[left].Quantity < normalized[right].Quantity
		}
		return normalized[left].UnitPrice < normalized[right].UnitPrice
	})
	return normalized
}

func availableQuantity(part Part) *int {
	if stock := positiveIntPointer(part.AvailabilityInStock); stock != nil {
		return stock
	}
	fields := strings.Fields(part.Availability)
	if len(fields) > 0 {
		if stock := positiveIntPointer(fields[0]); stock != nil {
			return stock
		}
	}
	return nil
}

func packagingDetails(attributes []ProductAttribute) (string, int) {
	var (
		packaging []string
		standard  int
	)
	seen := map[string]struct{}{}
	for _, attribute := range attributes {
		name := strings.ToLower(strings.TrimSpace(attribute.Name))
		value := strings.TrimSpace(attribute.Value)
		switch {
		case name == "packaging" && value != "":
			key := strings.ToLower(value)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				packaging = append(packaging, value)
			}
		case strings.Contains(name, "standard pack"):
			standard = positiveInt(value)
		}
	}
	return strings.Join(packaging, " | "), standard
}

func positiveInt(value string) int {
	pointer := positiveIntPointer(value)
	if pointer == nil {
		return 0
	}
	return *pointer
}

func positiveIntPointer(value string) *int {
	var digits strings.Builder
	started := false
	for _, character := range value {
		if unicode.IsDigit(character) {
			digits.WriteRune(character)
			started = true
		} else if started && character != ',' && character != '.' && !unicode.IsSpace(character) {
			break
		}
	}
	if digits.Len() == 0 {
		return nil
	}
	parsed, err := strconv.Atoi(digits.String())
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func cleanOrderable(partNumber string) string {
	partNumber = strings.TrimSpace(partNumber)
	if strings.EqualFold(partNumber, "N/A") {
		return ""
	}
	return partNumber
}

func normalizePartNumber(partNumber string) string {
	var normalized strings.Builder
	for _, character := range strings.ToUpper(partNumber) {
		if character >= 'A' && character <= 'Z' || unicode.IsDigit(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func plausiblyRelated(query, candidate string) bool {
	shared := 0
	for shared < len(query) && shared < len(candidate) && query[shared] == candidate[shared] {
		shared++
	}
	minimum := min(6, len(query))
	return shared >= minimum && shared*10 >= min(len(query), len(candidate))*6
}

func manufacturersMatch(input, candidate string) bool {
	left := canonicalManufacturer(input)
	right := canonicalManufacturer(candidate)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if len(left) >= 4 && strings.Contains(right, left) ||
		len(right) >= 4 && strings.Contains(left, right) {
		return true
	}
	return false
}

func canonicalManufacturer(manufacturer string) string {
	var words []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		word := current.String()
		current.Reset()
		switch word {
		case "inc", "incorporated", "corp", "corporation", "company", "co",
			"ltd", "limited", "technologies", "technology", "semiconductor",
			"semiconductors", "electronics", "electronic":
			return
		}
		words = append(words, word)
	}
	for _, character := range strings.ToLower(manufacturer) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			current.WriteRune(character)
		} else {
			flush()
		}
	}
	flush()
	normalized := strings.Join(words, " ")
	aliases := map[string]string{
		"ti":                     "texas instruments",
		"nxp usa":                "nxp",
		"onsemi":                 "on",
		"on":                     "on",
		"diodes":                 "diodes",
		"adi":                    "analog devices",
		"maxim integrated":       "analog devices",
		"st":                     "stmicroelectronics",
		"wurth":                  "würth",
		"yageo":                  "yageo",
		"vishay general":         "vishay",
		"vishay intertechnology": "vishay",
		"vishay dale":            "vishay",
		"vishay beyschlag":       "vishay",
	}
	if alias, exists := aliases[normalized]; exists {
		return alias
	}
	return normalized
}

func mouserManufacturerName(manufacturer string) string {
	canonical := canonicalManufacturer(manufacturer)
	names := map[string]string{
		"texas instruments":  "Texas Instruments",
		"nxp":                "NXP Semiconductors",
		"infineon":           "Infineon Technologies",
		"stmicroelectronics": "STMicroelectronics",
		"on":                 "onsemi",
		"analog devices":     "Analog Devices",
		"yageo":              "Yageo",
		"vishay":             "Vishay",
		"diodes":             "Diodes Incorporated",
	}
	if name, exists := names[canonical]; exists {
		return name
	}
	return strings.TrimSpace(manufacturer)
}

func isNonComponent(part Part) bool {
	text := strings.ToUpper(
		part.ManufacturerPartNumber + " " + part.Description + " " + part.Category,
	)
	for _, marker := range []string{
		"EVALUATION MODULE", "DEVELOPMENT TOOL", "DEVELOPMENT KIT",
		"EVAL BOARD", "-EVM", "EVM-", "-KIT", "KIT-",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func integerValue(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}
