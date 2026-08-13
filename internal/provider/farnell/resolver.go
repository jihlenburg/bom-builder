// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package farnell

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Searcher is the transport boundary used by the resolver and its tests.
// Currency and StoreID expose the client's store context: the search
// response itself carries neither, so normalization must take both from
// configuration.
type Searcher interface {
	Search(ctx context.Context, partNumber string, exact bool) ([]Product, error)
	Currency() string
	StoreID() string
}

// Resolver turns raw element14 products into normalized, stock-aware offers.
type Resolver struct {
	searcher Searcher
}

// NewResolver constructs a resolver around one search transport.
func NewResolver(searcher Searcher) (*Resolver, error) {
	if searcher == nil {
		return nil, errors.New("Farnell searcher is required")
	}
	return &Resolver{searcher: searcher}, nil
}

// Lookup performs an exact manufacturer-part-number lookup first and a
// review-required broad keyword pass only when no exact match exists.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	result := procurement.SourcedPart{Demand: demand}
	exactProducts, err := resolver.searcher.Search(ctx, demand.PartNumber, true)
	if err != nil {
		return result, err
	}
	exactCandidates := filterCandidates(exactProducts, demand, true)
	if len(exactCandidates) > 0 {
		return resolver.sourceCandidates(demand, exactCandidates, "exact", false), nil
	}

	broadProducts, err := resolver.searcher.Search(ctx, demand.PartNumber, false)
	if err != nil {
		return result, err
	}
	broadCandidates := filterCandidates(broadProducts, demand, false)
	if len(broadCandidates) == 0 {
		result.Status = "not_found"
		result.IssueCode = "PART_NOT_FOUND"
		result.IssueMessage = "Farnell returned no manufacturer-compatible candidate"
		return result, nil
	}
	return resolver.sourceCandidates(demand, broadCandidates, "candidate", true), nil
}

type normalizedCandidate struct {
	offer   procurement.Offer
	status  string
	code    string
	message string
}

func (resolver *Resolver) sourceCandidates(
	demand procurement.Demand,
	products []Product,
	matchMethod string,
	reviewRequired bool,
) procurement.SourcedPart {
	candidates := make([]normalizedCandidate, 0, len(products))
	for _, product := range products {
		offer, status, code, message := resolver.normalizeOffer(
			demand,
			product,
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
	// unavailable — into a bare "review" and hide them.
	return procurement.SourcedPart{
		Demand:         demand,
		Status:         selected.status,
		Offer:          &selected.offer,
		CandidateCount: len(candidates),
		IssueCode:      selected.code,
		IssueMessage:   selected.message,
	}
}

func (resolver *Resolver) normalizeOffer(
	demand procurement.Demand,
	product Product,
	matchMethod string,
	reviewRequired bool,
) (procurement.Offer, string, string, string) {
	priceBreaks := normalizePriceBreaks(product.Prices, resolver.searcher.Currency())
	available := availableQuantity(product)
	minimum := numberToPositiveInt(product.TranslatedMinimumOrderQuality)
	if minimum == 0 {
		minimum = numberToPositiveInt(product.TranslatedMinimumOrderQuantity)
	}
	standardPack := numberToPositiveInt(product.PackSize)
	offer := procurement.Offer{
		Provider:               "farnell",
		DistributorPartNumber:  strings.TrimSpace(product.SKU),
		ManufacturerPartNumber: strings.TrimSpace(product.TranslatedManufacturerPartNumber),
		Manufacturer:           productManufacturer(product),
		Description:            strings.TrimSpace(product.DisplayName),
		MatchMethod:            matchMethod,
		ReviewRequired:         reviewRequired,
		RequiredQuantity:       demand.RequiredQuantity,
		AvailableQuantity:      available,
		LifecycleStatus:        strings.TrimSpace(product.ProductStatus),
		MinimumOrderQuantity:   minimum,
		StandardPackQuantity:   standardPack,
		DatasheetURL:           firstDatasheetURL(product.Datasheets),
		ProductURL:             productURL(resolver.searcher.StoreID(), product.SKU),
		PriceBreaks:            priceBreaks,
	}
	if available != nil {
		offer.Availability = strconv.Itoa(*available) + " In Stock"
	}
	if offer.DistributorPartNumber == "" {
		return offer, "unavailable", "NOT_ORDERABLE", "Farnell does not expose an orderable SKU"
	}
	if discontinued(product.ProductStatus) && (available == nil || *available == 0) {
		return offer, "unavailable", "NOT_ORDERABLE",
			"Farnell lists the part as " + offer.LifecycleStatus + " without stock"
	}
	if len(priceBreaks) == 0 {
		return offer, "unavailable", "PRICE_UNAVAILABLE", "Farnell returned no valid price breaks"
	}

	family := procurement.PurchaseFamily{
		ID:                     offer.DistributorPartNumber,
		MinimumOrderQuantity:   minimum,
		BasePricingStrategy:    "requested quantity",
		StrategyMode:           "price_break",
		AllowMixingAsRemainder: true,
		AvailableQuantity:      available,
		PriceBreaks:            priceBreaks,
	}
	plan, err := procurement.OptimizePurchaseFamilies(
		demand.RequiredQuantity,
		[]procurement.PurchaseFamily{family},
	)
	if err != nil {
		return offer, "unavailable", "INVALID_PROVIDER_PRICE", "Farnell pricing could not form a valid plan"
	}
	offer.CandidatePlan = plan
	switch {
	case plan == nil && available != nil:
		return offer, "shortage", "INSUFFICIENT_STOCK", "Farnell stock cannot cover the required quantity"
	case plan == nil:
		return offer, "unavailable", "PRICE_UNAVAILABLE", "Farnell pricing could not form a purchase plan"
	case !plan.StockVerified:
		return offer, "stock_unknown", "STOCK_UNKNOWN", "Farnell stock was not reported; the plan was not selected"
	case reviewRequired:
		return offer, "review", "REVIEW_REQUIRED", "non-exact manufacturer part number requires engineering review"
	default:
		offer.SelectedPlan = plan
		return offer, "priced", "", ""
	}
}

func filterCandidates(products []Product, demand procurement.Demand, exact bool) []Product {
	query := normalizePartNumber(demand.PartNumber)
	candidates := make([]Product, 0, len(products))
	for _, product := range products {
		candidate := normalizePartNumber(product.TranslatedManufacturerPartNumber)
		if query == "" || candidate == "" ||
			!manufacturersMatch(demand.Manufacturer, productManufacturer(product)) ||
			isNonComponent(product) {
			continue
		}
		if exact {
			if query != candidate {
				continue
			}
		} else if !plausiblyRelated(query, candidate) {
			continue
		}
		candidates = append(candidates, product)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftStock := integerValue(availableQuantity(candidates[left]))
		rightStock := integerValue(availableQuantity(candidates[right]))
		leftOrderable := strings.TrimSpace(candidates[left].SKU) != ""
		rightOrderable := strings.TrimSpace(candidates[right].SKU) != ""
		if leftOrderable != rightOrderable {
			return leftOrderable
		}
		if len(candidates[left].Prices) != len(candidates[right].Prices) {
			return len(candidates[left].Prices) > len(candidates[right].Prices)
		}
		if leftStock != rightStock {
			return leftStock > rightStock
		}
		return candidates[left].SKU < candidates[right].SKU
	})
	return candidates
}

func compareNormalizedCandidates(left, right normalizedCandidate) int {
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

// normalizePriceBreaks converts quantity-banded prices. The cost text
// reaches money.Parse exactly as the provider sent it; unparseable rows
// are dropped rather than guessed at.
func normalizePriceBreaks(raw []RawPrice, currency string) []procurement.PriceBreak {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	normalized := make([]procurement.PriceBreak, 0, len(raw))
	for _, priceBreak := range raw {
		price, err := money.Parse(priceBreak.Cost.String())
		if err != nil || priceBreak.From < 1 || !validCurrency(currency) {
			continue
		}
		normalized = append(normalized, procurement.PriceBreak{
			Quantity:  priceBreak.From,
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

// availableQuantity reports known stock. A missing stock object or level
// is UNKNOWN (nil), never zero.
func availableQuantity(product Product) *int {
	if product.Stock == nil || product.Stock.Level == nil || *product.Stock.Level < 0 {
		return nil
	}
	level := *product.Stock.Level
	return &level
}

func discontinued(productStatus string) bool {
	switch strings.ToUpper(strings.TrimSpace(productStatus)) {
	case "NO_LONGER_MANUFACTURED", "NO_LONGER_STOCKED":
		return true
	default:
		return false
	}
}

func productManufacturer(product Product) string {
	if brand := strings.TrimSpace(product.BrandName); brand != "" {
		return brand
	}
	return strings.TrimSpace(product.VendorName)
}

func firstDatasheetURL(datasheets []Datasheet) string {
	for _, datasheet := range datasheets {
		link := strings.TrimSpace(datasheet.URL)
		if strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") {
			return link
		}
	}
	return ""
}

// productURL builds the store's canonical short product path. /dp/<sku>
// is the storefront's stable product redirect.
func productURL(storeID, sku string) string {
	storeID = strings.TrimSpace(storeID)
	sku = strings.TrimSpace(sku)
	if storeID == "" || sku == "" {
		return ""
	}
	return "https://" + storeID + "/dp/" + url.PathEscape(sku)
}

func numberToPositiveInt(value json.Number) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value.String()))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
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
	if query == "" || candidate == "" {
		return false
	}
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
	return len(left) >= 4 && strings.Contains(right, left) ||
		len(right) >= 4 && strings.Contains(left, right)
}

// foldDiacritics maps common Latin letters with diacritics to their
// ASCII base form. The element14 catalog indexes manufacturers in ASCII
// ("Wurth Elektronik"); a correctly spelled BOM ("Würth Elektronik")
// must still match.
func foldDiacritics(text string) string {
	fold := map[rune]string{
		'ä': "a", 'ö': "o", 'ü': "u", 'Ä': "A", 'Ö': "O", 'Ü': "U",
		'ß': "ss",
		'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'å': "a",
		'À': "A", 'Á': "A", 'Â': "A", 'Ã': "A", 'Å': "A",
		'è': "e", 'é': "e", 'ê': "e", 'ë': "e",
		'È': "E", 'É': "E", 'Ê': "E", 'Ë': "E",
		'ì': "i", 'í': "i", 'î': "i", 'ï': "i",
		'Ì': "I", 'Í': "I", 'Î': "I", 'Ï': "I",
		'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ø': "o",
		'Ò': "O", 'Ó': "O", 'Ô': "O", 'Õ': "O", 'Ø': "O",
		'ù': "u", 'ú': "u", 'û': "u",
		'Ù': "U", 'Ú': "U", 'Û': "U",
		'ñ': "n", 'Ñ': "N", 'ç': "c", 'Ç': "C",
		'ý': "y", 'Ý': "Y", 'æ': "ae", 'Æ': "AE",
		'š': "s", 'Š': "S", 'ž': "z", 'Ž': "Z",
		'č': "c", 'Č': "C", 'ć': "c", 'Ć': "C",
		'ł': "l", 'Ł': "L", 'đ': "d", 'Đ': "D",
	}
	var out strings.Builder
	for _, character := range text {
		if replacement, exists := fold[character]; exists {
			out.WriteString(replacement)
			continue
		}
		out.WriteRune(character)
	}
	return out.String()
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
			"semiconductors", "electronics", "electronic", "elektronik":
			return
		}
		words = append(words, word)
	}
	for _, character := range strings.ToLower(foldDiacritics(manufacturer)) {
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
		"adi":                    "analog devices",
		"maxim integrated":       "analog devices",
		"st":                     "stmicroelectronics",
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

func isNonComponent(product Product) bool {
	text := strings.ToUpper(
		product.TranslatedManufacturerPartNumber + " " + product.DisplayName,
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
