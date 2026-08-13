// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package procurement defines normalized distributor offers and safe purchasing plans.
package procurement

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/money"
)

const defaultManufacturingPreferenceBasisPoints = 50 // 0.50%

// PriceBreak is one exact unit-price threshold.
type PriceBreak struct {
	Quantity  int           `json:"quantity"`
	UnitPrice money.Decimal `json:"unit_price"`
	Currency  string        `json:"currency"`
}

// PurchaseFamily describes one independently orderable packaging family.
type PurchaseFamily struct {
	ID                     string
	PackageType            string
	PackagingMode          string
	MinimumOrderQuantity   int
	OrderMultiple          int
	FullReelQuantity       int
	BasePricingStrategy    string
	StrategyMode           string
	AllowMixingAsBulk      bool
	AllowMixingAsRemainder bool
	MixQuantity            int
	AvailableQuantity      *int
	PriceBreaks            []PriceBreak
}

// PurchaseLeg is one distributor SKU/packaging leg in an order plan.
type PurchaseLeg struct {
	FamilyID             string        `json:"family_id"`
	PurchasedQuantity    int           `json:"purchased_quantity"`
	UnitPrice            money.Decimal `json:"unit_price"`
	ExtendedPrice        money.Decimal `json:"extended_price"`
	Currency             string        `json:"currency"`
	PriceBreak           int           `json:"price_break_quantity"`
	PricingStrategy      string        `json:"pricing_strategy"`
	PackageType          string        `json:"package_type,omitempty"`
	PackagingMode        string        `json:"packaging_mode,omitempty"`
	MinimumOrderQuantity int           `json:"minimum_order_quantity,omitempty"`
	OrderMultiple        int           `json:"order_multiple,omitempty"`
	OrderBatchSize       int           `json:"order_batch_quantity,omitempty"`
	OrderBatchCount      int           `json:"order_batch_count,omitempty"`
	Marketplace          bool          `json:"marketplace"`
	StockVerified        bool          `json:"stock_verified"`
}

// PurchasePlan is a stock-aware, exactly priced plan covering a requirement.
type PurchasePlan struct {
	RequiredQuantity  int           `json:"required_quantity"`
	PurchasedQuantity int           `json:"purchased_quantity"`
	SurplusQuantity   int           `json:"surplus_quantity"`
	UnitPrice         money.Decimal `json:"effective_unit_price"`
	ExtendedPrice     money.Decimal `json:"extended_price"`
	Currency          string        `json:"currency"`
	PriceBreak        int           `json:"price_break_quantity,omitempty"`
	PricingStrategy   string        `json:"pricing_strategy"`
	OrderPlan         string        `json:"order_plan"`
	StockVerified     bool          `json:"stock_verified"`
	Legs              []PurchaseLeg `json:"legs"`
}

// OptimizePurchaseFamilies returns the preferred legal single- or mixed-family plan.
func OptimizePurchaseFamilies(requiredQuantity int, families []PurchaseFamily) (*PurchasePlan, error) {
	return OptimizePurchaseFamiliesWithPreference(
		requiredQuantity,
		families,
		defaultManufacturingPreferenceBasisPoints,
	)
}

// OptimizePurchaseFamiliesWithPreference allows manufacturing preference to be tested explicitly.
func OptimizePurchaseFamiliesWithPreference(
	requiredQuantity int,
	families []PurchaseFamily,
	preferenceBasisPoints int,
) (*PurchasePlan, error) {
	if requiredQuantity < 1 {
		return nil, errors.New("required quantity must be positive")
	}
	if preferenceBasisPoints < 0 {
		return nil, errors.New("manufacturing preference cannot be negative")
	}

	validFamilies := make([]PurchaseFamily, 0, len(families))
	for _, family := range families {
		if len(family.PriceBreaks) == 0 {
			continue
		}
		if err := validateFamily(family); err != nil {
			return nil, fmt.Errorf("purchase family %q: %w", family.ID, err)
		}
		validFamilies = append(validFamilies, family)
	}

	// Every candidate plan is later compared by raw ExtendedPrice micros,
	// which is meaningless across currencies: a silent pick could select
	// the economically more expensive plan. validateFamily guarantees one
	// currency per family; this guarantees one currency per comparison,
	// mirroring the sourcing layer's explicit conversion refusal.
	currencies := make(map[string]bool)
	for _, family := range validFamilies {
		currencies[family.PriceBreaks[0].Currency] = true
	}
	if len(currencies) > 1 {
		names := make([]string, 0, len(currencies))
		for currency := range currencies {
			names = append(names, currency)
		}
		sort.Strings(names)
		return nil, fmt.Errorf(
			"purchase families span multiple currencies (%s): cross-currency comparison requires explicit conversion",
			strings.Join(names, ", "),
		)
	}

	plans := make([]PurchasePlan, 0, len(validFamilies))
	for _, family := range validFamilies {
		leg, err := purchaseLegFromFamily(family, requiredQuantity)
		if err != nil {
			return nil, err
		}
		if leg != nil {
			plan, err := composePurchasePlan(
				requiredQuantity,
				[]PurchaseLeg{*leg},
				leg.PricingStrategy,
			)
			if err != nil {
				return nil, err
			}
			plans = append(plans, *plan)
		}
	}

	for _, bulk := range validFamilies {
		if !bulk.AllowMixingAsBulk {
			continue
		}
		for _, remainder := range validFamilies {
			if !remainder.AllowMixingAsRemainder || remainder.ID == bulk.ID {
				continue
			}
			for _, quantity := range candidateBulkQuantities(requiredQuantity, bulk, remainder) {
				bulkLeg, err := purchaseLegFromFamily(bulk, quantity)
				if err != nil {
					return nil, err
				}
				if bulkLeg == nil || bulkLeg.PurchasedQuantity >= requiredQuantity {
					continue
				}
				remainderLeg, err := purchaseLegFromFamily(
					remainder,
					requiredQuantity-bulkLeg.PurchasedQuantity,
				)
				if err != nil {
					return nil, err
				}
				if remainderLeg == nil {
					continue
				}
				plan, err := composePurchasePlan(
					requiredQuantity,
					[]PurchaseLeg{*bulkLeg, *remainderLeg},
					"mixed packaging",
				)
				if err != nil {
					return nil, err
				}
				plans = append(plans, *plan)
			}
		}
	}

	if len(plans) == 0 {
		return nil, nil
	}
	return selectBestPlan(plans, preferenceBasisPoints), nil
}

func validateFamily(family PurchaseFamily) error {
	if strings.TrimSpace(family.ID) == "" {
		return errors.New("ID is required")
	}
	currency := ""
	for _, value := range []struct {
		name  string
		value int
	}{
		{"minimum order quantity", family.MinimumOrderQuantity},
		{"order multiple", family.OrderMultiple},
		{"full reel quantity", family.FullReelQuantity},
		{"mix quantity", family.MixQuantity},
	} {
		if value.value < 0 {
			return fmt.Errorf("%s cannot be negative", value.name)
		}
	}
	if family.AvailableQuantity != nil && *family.AvailableQuantity < 0 {
		return errors.New("available quantity cannot be negative")
	}
	for _, priceBreak := range family.PriceBreaks {
		if priceBreak.Quantity < 1 {
			return errors.New("price-break quantity must be positive")
		}
		if priceBreak.UnitPrice.Micros() <= 0 {
			return errors.New("unit price must be positive")
		}
		if !validCurrency(priceBreak.Currency) {
			return fmt.Errorf("invalid currency %q", priceBreak.Currency)
		}
		if currency == "" {
			currency = priceBreak.Currency
		} else if priceBreak.Currency != currency {
			return errors.New("price breaks contain mixed currencies")
		}
	}
	return nil
}

func purchaseLegFromFamily(family PurchaseFamily, quantity int) (*PurchaseLeg, error) {
	if quantity <= 0 {
		return nil, nil
	}
	var candidates []PurchaseLeg
	for _, priceBreak := range family.PriceBreaks {
		purchased := max(quantity, priceBreak.Quantity, family.MinimumOrderQuantity)
		multiple := family.OrderMultiple
		if family.FullReelQuantity > 0 {
			multiple = family.FullReelQuantity
		}
		var err error
		purchased, err = roundUpToMultiple(purchased, multiple)
		if err != nil {
			return nil, err
		}
		if family.AvailableQuantity != nil && purchased > *family.AvailableQuantity {
			continue
		}
		extended, err := priceBreak.UnitPrice.MulInt(purchased)
		if err != nil {
			return nil, err
		}
		batchSize := firstDivisor(
			purchased,
			family.FullReelQuantity,
			family.OrderMultiple,
			family.MinimumOrderQuantity,
		)
		batchCount := 0
		if batchSize > 1 {
			batchCount = purchased / batchSize
		}
		candidates = append(candidates, PurchaseLeg{
			FamilyID:          family.ID,
			PurchasedQuantity: purchased,
			UnitPrice:         priceBreak.UnitPrice,
			ExtendedPrice:     extended,
			Currency:          strings.ToUpper(priceBreak.Currency),
			PriceBreak:        priceBreak.Quantity,
			PricingStrategy: familyStrategy(
				family,
				quantity,
				purchased,
				priceBreak.Quantity,
			),
			PackageType:          family.PackageType,
			PackagingMode:        family.PackagingMode,
			MinimumOrderQuantity: family.MinimumOrderQuantity,
			OrderMultiple:        family.OrderMultiple,
			OrderBatchSize:       batchSize,
			OrderBatchCount:      batchCount,
			StockVerified:        family.AvailableQuantity != nil,
		})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return compareLeg(candidates[left], candidates[right]) < 0
	})
	return &candidates[0], nil
}

func composePurchasePlan(
	requiredQuantity int,
	legs []PurchaseLeg,
	strategy string,
) (*PurchasePlan, error) {
	if len(legs) == 0 {
		return nil, nil
	}
	currency := legs[0].Currency
	purchased := 0
	total := money.Decimal(0)
	stockVerified := true
	for _, leg := range legs {
		if leg.Currency != currency {
			return nil, errors.New("purchase plan contains mixed currencies")
		}
		var err error
		total, err = total.Add(leg.ExtendedPrice)
		if err != nil {
			return nil, err
		}
		if leg.PurchasedQuantity > math.MaxInt-purchased {
			return nil, errors.New("purchase quantity overflow")
		}
		purchased += leg.PurchasedQuantity
		stockVerified = stockVerified && leg.StockVerified
	}
	if purchased < requiredQuantity {
		return nil, errors.New("purchase plan does not cover required quantity")
	}
	effective, err := total.DivInt(purchased)
	if err != nil {
		return nil, err
	}
	priceBreak := 0
	if len(legs) == 1 {
		priceBreak = legs[0].PriceBreak
	}
	return &PurchasePlan{
		RequiredQuantity:  requiredQuantity,
		PurchasedQuantity: purchased,
		SurplusQuantity:   purchased - requiredQuantity,
		UnitPrice:         effective,
		ExtendedPrice:     total,
		Currency:          currency,
		PriceBreak:        priceBreak,
		PricingStrategy:   strategy,
		OrderPlan:         formatOrderPlan(legs),
		StockVerified:     stockVerified,
		Legs:              legs,
	}, nil
}

func selectBestPlan(plans []PurchasePlan, preferenceBasisPoints int) *PurchasePlan {
	sort.SliceStable(plans, func(left, right int) bool {
		return comparePlanCost(plans[left], plans[right]) < 0
	})
	cheapest := plans[0]
	preferred := make([]PurchasePlan, 0, len(plans))
	for _, plan := range plans {
		if withinBasisPoints(plan.ExtendedPrice, cheapest.ExtendedPrice, preferenceBasisPoints) {
			preferred = append(preferred, plan)
		}
	}
	sort.SliceStable(preferred, func(left, right int) bool {
		return compareManufacturingPreference(preferred[left], preferred[right]) < 0
	})
	selected := preferred[0]
	return &selected
}

func compareLeg(left, right PurchaseLeg) int {
	if result := cmpDecimal(left.ExtendedPrice, right.ExtendedPrice); result != 0 {
		return result
	}
	if left.PurchasedQuantity != right.PurchasedQuantity {
		return cmpInt(left.PurchasedQuantity, right.PurchasedQuantity)
	}
	return cmpInt(left.PriceBreak, right.PriceBreak)
}

func comparePlanCost(left, right PurchasePlan) int {
	if result := cmpDecimal(left.ExtendedPrice, right.ExtendedPrice); result != 0 {
		return result
	}
	if left.SurplusQuantity != right.SurplusQuantity {
		return cmpInt(left.SurplusQuantity, right.SurplusQuantity)
	}
	if left.PurchasedQuantity != right.PurchasedQuantity {
		return cmpInt(left.PurchasedQuantity, right.PurchasedQuantity)
	}
	return cmpInt(len(left.Legs), len(right.Legs))
}

func compareManufacturingPreference(left, right PurchasePlan) int {
	leftReel, leftStable, leftCut := packagingQuantities(left)
	rightReel, rightStable, rightCut := packagingQuantities(right)
	for _, comparison := range []int{
		cmpInt(rightReel, leftReel),
		cmpInt(rightStable, leftStable),
		cmpInt(leftCut, rightCut),
		comparePlanCost(left, right),
	} {
		if comparison != 0 {
			return comparison
		}
	}
	return strings.Compare(left.OrderPlan, right.OrderPlan)
}

func packagingQuantities(plan PurchasePlan) (reel, stable, cut int) {
	for _, leg := range plan.Legs {
		switch packagingKind(leg) {
		case "reel":
			reel += leg.PurchasedQuantity
			stable += leg.PurchasedQuantity
		case "stable":
			stable += leg.PurchasedQuantity
		case "cut":
			cut += leg.PurchasedQuantity
		}
	}
	return reel, stable, cut
}

func packagingKind(leg PurchaseLeg) string {
	text := strings.ToLower(strings.TrimSpace(leg.PackagingMode + " " + leg.PackageType))
	switch {
	case strings.Contains(text, "cut tape"):
		return "cut"
	case strings.Contains(text, "mousereel"), strings.Contains(text, "mouse reel"):
		return "stable"
	case strings.Contains(text, "reel"), strings.Contains(text, "t&r"):
		return "reel"
	case strings.Contains(text, "tray"), strings.Contains(text, "tube"), strings.Contains(text, "bulk"):
		return "stable"
	default:
		return "unknown"
	}
}

func candidateBulkQuantities(
	required int,
	bulk PurchaseFamily,
	remainder PurchaseFamily,
) []int {
	mix := firstPositive(bulk.MixQuantity, bulk.FullReelQuantity, bulk.OrderMultiple)
	if mix <= 1 {
		return nil
	}
	counts := map[int]struct{}{}
	addAround := func(count int) {
		for _, candidate := range []int{count - 1, count, count + 1} {
			if candidate > 0 {
				counts[candidate] = struct{}{}
			}
		}
	}
	addAround(required / mix)
	for _, priceBreak := range bulk.PriceBreaks {
		addAround(priceBreak.Quantity / mix)
	}
	for _, priceBreak := range remainder.PriceBreaks {
		if required > priceBreak.Quantity {
			addAround((required - priceBreak.Quantity) / mix)
		}
	}
	if bulk.AvailableQuantity != nil {
		addAround(*bulk.AvailableQuantity / mix)
	}
	quantities := make([]int, 0, len(counts))
	for count := range counts {
		if count > math.MaxInt/mix {
			continue
		}
		quantity := count * mix
		if quantity > 0 && quantity < required {
			quantities = append(quantities, quantity)
		}
	}
	sort.Ints(quantities)
	return quantities
}

func familyStrategy(family PurchaseFamily, required, purchased, priceBreak int) string {
	switch family.StrategyMode {
	case "static":
		return family.BasePricingStrategy
	case "full_reel":
		return "full reel"
	case "price_break":
		if purchased > required {
			if priceBreak > required {
				return "next price break"
			}
			if family.OrderMultiple > 1 {
				return "order multiple"
			}
			return "next price break"
		}
		if family.BasePricingStrategy != "" {
			return family.BasePricingStrategy
		}
		return "requested quantity"
	default:
		return family.BasePricingStrategy
	}
}

func formatOrderPlan(legs []PurchaseLeg) string {
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
				"%d %s x %d",
				leg.OrderBatchCount,
				noun,
				leg.OrderBatchSize,
			))
			continue
		}
		if leg.PackagingMode != "" {
			rendered = append(
				rendered,
				fmt.Sprintf("%d %s", leg.PurchasedQuantity, strings.ToLower(leg.PackagingMode)),
			)
			continue
		}
		rendered = append(rendered, fmt.Sprintf("%d", leg.PurchasedQuantity))
	}
	return strings.Join(rendered, " + ")
}

func roundUpToMultiple(quantity, multiple int) (int, error) {
	if multiple <= 1 {
		return quantity, nil
	}
	remainder := quantity % multiple
	if remainder == 0 {
		return quantity, nil
	}
	addition := multiple - remainder
	if quantity > math.MaxInt-addition {
		return 0, errors.New("purchase quantity overflow")
	}
	return quantity + addition, nil
}

func firstDivisor(quantity int, candidates ...int) int {
	for _, candidate := range candidates {
		if candidate > 1 && quantity%candidate == 0 {
			return candidate
		}
	}
	return 0
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
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

func withinBasisPoints(candidate, cheapest money.Decimal, basisPoints int) bool {
	left := new(big.Int).Mul(big.NewInt(candidate.Micros()), big.NewInt(10_000))
	right := new(big.Int).Mul(
		big.NewInt(cheapest.Micros()),
		big.NewInt(int64(10_000+basisPoints)),
	)
	return left.Cmp(right) <= 0
}

func cmpDecimal(left, right money.Decimal) int {
	return cmpInt64(left.Micros(), right.Micros())
}

func cmpInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func cmpInt64(left, right int64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
