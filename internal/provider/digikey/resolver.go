// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package digikey

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

// Resolver turns Digi-Key quantity-pricing responses into safe normalized offers.
type Resolver struct {
	client *Client
}

// NewResolver constructs a resolver around a Digi-Key client.
func NewResolver(client *Client) (*Resolver, error) {
	if client == nil {
		return nil, fmt.Errorf("Digi-Key client is required")
	}
	return &Resolver{client: client}, nil
}

// Lookup prices one manufacturer part number at its required quantity.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	result := procurement.SourcedPart{Demand: demand}
	pricing, err := resolver.client.PricingByQuantity(
		ctx,
		demand.PartNumber,
		demand.RequiredQuantity,
	)
	if err != nil {
		var providerError *Error
		if errors.As(err, &providerError) && providerError.Kind == "not_found" {
			result.Status = "not_found"
			result.IssueCode = "PART_NOT_FOUND"
			result.IssueMessage = "Digi-Key returned no matching product"
			return result, nil
		}
		return result, err
	}
	if !manufacturersMatch(demand.Manufacturer, pricing.ManufacturerName) {
		result.Status = "not_found"
		result.IssueCode = "MANUFACTURER_MISMATCH"
		result.IssueMessage = "Digi-Key returned a different manufacturer"
		return result, nil
	}

	query := normalizePartNumber(demand.PartNumber)
	returned := normalizePartNumber(pricing.ManufacturerPartNumber)
	exact := query != "" && query == returned
	if !exact && !plausiblyRelated(query, returned) {
		result.Status = "not_found"
		result.IssueCode = "PART_NUMBER_MISMATCH"
		result.IssueMessage = "Digi-Key returned an unrelated manufacturer part number"
		return result, nil
	}

	options := append(
		append([]PricingOption(nil), pricing.MyPricingOptions...),
		pricing.StandardPricingOptions...,
	)
	type normalizedOption struct {
		option    PricingOption
		plan      *procurement.PurchasePlan
		available *int
	}
	var normalized []normalizedOption
	for _, option := range options {
		plan, normalizeErr := normalizePlan(
			option,
			pricing.Currency,
			demand.RequiredQuantity,
		)
		if normalizeErr == nil && plan != nil {
			normalized = append(normalized, normalizedOption{option: option, plan: plan})
		}
	}
	if len(normalized) == 0 {
		result.Status = "unavailable"
		result.IssueCode = "PRICE_UNAVAILABLE"
		result.IssueMessage = "Digi-Key returned no complete pricing option"
		return result, nil
	}

	// Stock truth comes from ProductDetails; the quantity-pricing
	// endpoint reports QuantityAvailable 0 regardless of real stock
	// (observed live 2026-07-30). One fetch serves every option of
	// this product and the document links below.
	info := ProductInfo{}
	if len(normalized[0].plan.Legs) > 0 {
		fetched, infoErr := resolver.client.ProductInformation(
			ctx,
			normalized[0].plan.Legs[0].FamilyID,
		)
		if infoErr == nil {
			info = fetched
		}
	}
	for index := range normalized {
		normalized[index].available, _ = applyStock(normalized[index].plan, info)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		a, b := normalized[left].plan, normalized[right].plan
		if a.StockVerified != b.StockVerified {
			return a.StockVerified
		}
		if a.ExtendedPrice != b.ExtendedPrice {
			return a.ExtendedPrice < b.ExtendedPrice
		}
		if a.SurplusQuantity != b.SurplusQuantity {
			return a.SurplusQuantity < b.SurplusQuantity
		}
		return a.OrderPlan < b.OrderPlan
	})
	selected := normalized[0]
	offer := offerFromOption(
		demand,
		pricing,
		selected.available,
		selected.plan,
		exact,
	)
	if info.DatasheetURL != "" {
		offer.DatasheetURL = info.DatasheetURL
	}
	if info.ProductURL != "" {
		offer.ProductURL = info.ProductURL
	}

	result.Offer = &offer
	result.CandidateCount = 1
	switch {
	case !selected.plan.StockVerified:
		if selected.available == nil {
			result.Status = "stock_unknown"
			result.IssueCode = "STOCK_UNKNOWN"
			result.IssueMessage = "Digi-Key did not report stock for the selected pricing option"
		} else {
			result.Status = "shortage"
			result.IssueCode = "INSUFFICIENT_STOCK"
			result.IssueMessage = "Digi-Key stock cannot cover the selected pricing option"
		}
	case !exact:
		result.Status = "review"
		result.IssueCode = "REVIEW_REQUIRED"
		result.IssueMessage = "non-exact Digi-Key manufacturer part number requires engineering review"
	default:
		result.Status = "priced"
		offer.SelectedPlan = selected.plan
		result.Offer = &offer
	}
	return result, nil
}

// applyStock verifies a purchase plan against per-variation stock from
// ProductDetails and stamps the verdict onto the plan and its legs.
// It returns the plan's limiting availability, nil when any purchased
// SKU has no reported quantity — unknown stock is never zero stock.
func applyStock(
	plan *procurement.PurchasePlan,
	info ProductInfo,
) (*int, bool) {
	purchasedBySKU := make(map[string]int, len(plan.Legs))
	for _, leg := range plan.Legs {
		purchasedBySKU[leg.FamilyID] += leg.PurchasedQuantity
	}
	verified := len(purchasedBySKU) > 0
	known := true
	limiting := 0
	first := true
	for sku, purchased := range purchasedBySKU {
		quantity, reported := info.VariationQuantities[sku]
		if !reported {
			known = false
			verified = false
			break
		}
		if quantity < purchased {
			verified = false
		}
		if first || quantity < limiting {
			limiting = quantity
			first = false
		}
	}
	var available *int
	if known && len(purchasedBySKU) > 0 {
		available = &limiting
	}
	plan.StockVerified = verified
	for index := range plan.Legs {
		if available == nil {
			plan.Legs[index].StockVerified = false
			continue
		}
		quantity := info.VariationQuantities[plan.Legs[index].FamilyID]
		plan.Legs[index].StockVerified = purchasedBySKU[plan.Legs[index].FamilyID] <= quantity
	}
	return available, verified
}

func normalizePlan(
	option PricingOption,
	currency string,
	required int,
) (*procurement.PurchasePlan, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if !validCurrency(currency) ||
		option.TotalQuantity < required ||
		option.TotalQuantity < 1 ||
		len(option.Products) == 0 {
		return nil, fmt.Errorf("invalid pricing option")
	}
	total, err := money.Parse(option.TotalPrice.String())
	if err != nil || total.Micros() <= 0 {
		return nil, fmt.Errorf("invalid total price")
	}
	// Stock is verified later by applyStock from ProductDetails data;
	// the pricing endpoint's QuantityAvailable is not trustworthy
	// (reports 0 for stocked parts — observed live 2026-07-30).
	stockVerified := false
	legs := make([]procurement.PurchaseLeg, 0, len(option.Products))
	quantitySum := 0
	legTotal := money.Decimal(0)
	for _, product := range option.Products {
		if product.ProductNumber == "" ||
			product.Quantity < 1 ||
			product.MinimumOrderQuantity < 1 {
			return nil, fmt.Errorf("invalid product leg")
		}
		unitPrice, unitErr := money.Parse(product.UnitPrice.String())
		extendedPrice, extendedErr := money.Parse(product.ExtendedPrice.String())
		if unitErr != nil || extendedErr != nil || extendedPrice.Micros() <= 0 {
			return nil, fmt.Errorf("invalid product price")
		}
		legTotal, err = legTotal.Add(extendedPrice)
		if err != nil {
			return nil, fmt.Errorf("invalid product total")
		}
		quantitySum += product.Quantity
		batchSize := 0
		batchCount := 0
		if product.MinimumOrderQuantity > 1 &&
			product.Quantity%product.MinimumOrderQuantity == 0 {
			batchSize = product.MinimumOrderQuantity
			batchCount = product.Quantity / product.MinimumOrderQuantity
		}
		legs = append(legs, procurement.PurchaseLeg{
			FamilyID:             product.ProductNumber,
			PurchasedQuantity:    product.Quantity,
			UnitPrice:            unitPrice,
			ExtendedPrice:        extendedPrice,
			Currency:             currency,
			PriceBreak:           product.MinimumOrderQuantity,
			PricingStrategy:      option.Name,
			PackageType:          product.PackageType,
			PackagingMode:        product.PackageType,
			MinimumOrderQuantity: product.MinimumOrderQuantity,
			OrderMultiple:        max(product.MinimumOrderQuantity, 1),
			OrderBatchSize:       batchSize,
			OrderBatchCount:      batchCount,
			Marketplace:          product.Marketplace,
			StockVerified:        stockVerified,
		})
	}
	if quantitySum != option.TotalQuantity {
		return nil, fmt.Errorf("product legs do not preserve total quantity")
	}
	if legTotal != total {
		return nil, fmt.Errorf("product legs do not preserve total price")
	}
	effective, err := total.DivInt(option.TotalQuantity)
	if err != nil {
		return nil, err
	}
	return &procurement.PurchasePlan{
		RequiredQuantity:  required,
		PurchasedQuantity: option.TotalQuantity,
		SurplusQuantity:   option.TotalQuantity - required,
		UnitPrice:         effective,
		ExtendedPrice:     total,
		Currency:          currency,
		PriceBreak:        option.TotalQuantity,
		PricingStrategy:   option.Name,
		OrderPlan:         formatLegs(legs),
		StockVerified:     stockVerified,
		Legs:              legs,
	}, nil
}

func offerFromOption(
	demand procurement.Demand,
	pricing PricingResult,
	available *int,
	plan *procurement.PurchasePlan,
	exact bool,
) procurement.Offer {
	productNumbers := make([]string, 0, len(plan.Legs))
	packages := make([]string, 0, len(plan.Legs))
	minimum := 0
	for _, leg := range plan.Legs {
		productNumbers = append(productNumbers, leg.FamilyID)
		if leg.PackagingMode != "" && !slicesContains(packages, leg.PackagingMode) {
			packages = append(packages, leg.PackagingMode)
		}
		if minimum == 0 || leg.MinimumOrderQuantity < minimum {
			minimum = leg.MinimumOrderQuantity
		}
	}
	matchMethod := "candidate"
	if exact {
		matchMethod = "exact"
	}
	priceBreaks := make([]procurement.PriceBreak, 0, 1)
	priceBreaks = append(priceBreaks, procurement.PriceBreak{
		Quantity:  plan.PurchasedQuantity,
		UnitPrice: plan.UnitPrice,
		Currency:  plan.Currency,
	})
	offer := procurement.Offer{
		Provider:               "digikey",
		DistributorPartNumber:  strings.Join(productNumbers, " + "),
		ManufacturerPartNumber: pricing.ManufacturerPartNumber,
		Manufacturer:           pricing.ManufacturerName,
		MatchMethod:            matchMethod,
		ReviewRequired:         !exact,
		RequiredQuantity:       demand.RequiredQuantity,
		AvailableQuantity:      available,
		Availability:           availabilityText(available),
		Packaging:              strings.Join(packages, " + "),
		MinimumOrderQuantity:   minimum,
		OrderMultiple:          max(minimum, 1),
		ProductURL:             pricing.ProductURL,
		PriceBreaks:            priceBreaks,
		CandidatePlan:          plan,
	}
	return offer
}

func formatLegs(legs []procurement.PurchaseLeg) string {
	rendered := make([]string, 0, len(legs))
	for _, leg := range legs {
		if leg.OrderBatchSize > 1 && leg.OrderBatchCount > 0 {
			noun := "batches"
			if strings.Contains(strings.ToLower(leg.PackagingMode), "reel") {
				noun = "reels"
			}
			if leg.OrderBatchCount == 1 {
				noun = strings.TrimSuffix(noun, "s")
			}
			rendered = append(rendered, fmt.Sprintf(
				"%d %s x %d (%s)",
				leg.OrderBatchCount,
				noun,
				leg.OrderBatchSize,
				leg.FamilyID,
			))
			continue
		}
		label := strings.ToLower(strings.TrimSpace(leg.PackagingMode))
		if label == "" {
			label = "units"
		}
		rendered = append(rendered, fmt.Sprintf(
			"%d %s (%s)",
			leg.PurchasedQuantity,
			label,
			leg.FamilyID,
		))
	}
	return strings.Join(rendered, " + ")
}

func availabilityText(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value) + " available"
}

func manufacturersMatch(input, candidate string) bool {
	left, right := normalizeManufacturer(input), normalizeManufacturer(candidate)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	return len(left) >= 4 && strings.Contains(right, left) ||
		len(right) >= 4 && strings.Contains(left, right)
}

func normalizeManufacturer(value string) string {
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
			"semiconductors", "electronics", "electronic", "components":
			return
		}
		words = append(words, word)
	}
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			current.WriteRune(character)
		} else {
			flush()
		}
	}
	flush()
	normalized := strings.Join(words, " ")
	aliases := map[string]string{
		"ti":               "texas instruments",
		"onsemi":           "on",
		"on":               "on",
		"adi":              "analog devices",
		"maxim integrated": "analog devices",
		"st":               "stmicroelectronics",
	}
	if alias, exists := aliases[normalized]; exists {
		return alias
	}
	return normalized
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

func plausiblyRelated(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	shared := 0
	for shared < len(left) && shared < len(right) && left[shared] == right[shared] {
		shared++
	}
	return shared >= min(6, len(left)) &&
		shared*10 >= min(len(left), len(right))*6
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, character := range currency {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
