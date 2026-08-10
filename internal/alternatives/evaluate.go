// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package alternatives

import (
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Evaluate returns a field-by-field fail-closed comparison for every candidate.
func Evaluate(request Request) []Result {
	results := make([]Result, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		comparisons := commonComparisons(request.Original, candidate)
		switch request.Kind {
		case "resistor":
			comparisons = append(comparisons, resistorComparisons(request.Original, candidate)...)
		case "capacitor":
			comparisons = append(comparisons, capacitorComparisons(request.Original, candidate)...)
		case "inductor":
			comparisons = append(comparisons, inductorComparisons(request.Original, candidate)...)
		}
		compatibility := "compatible"
		reasons := []string{}
		for _, comparison := range comparisons {
			switch comparison.Relation {
			case "worse":
				compatibility = "incompatible"
				reasons = append(reasons, comparison.Field+" does not meet the original requirement")
			case "unknown":
				if compatibility != "incompatible" {
					compatibility = "unknown"
				}
				reasons = append(reasons, comparison.Field+" is unknown")
			}
		}
		results = append(results, Result{
			Candidate:                 candidate,
			Compatibility:             compatibility,
			EngineeringReviewRequired: true,
			EvidenceDocumentCount:     len(candidate.SourceDocuments),
			Comparisons:               comparisons,
			RejectedReasons:           reasons,
			RecommendedForReview:      false,
		})
	}
	return results
}

// Rank assigns ranks to viable candidates and marks one safe-stock result for
// review only when all safe candidates have a comparable currency. It returns
// the distinct safe-plan currencies.
func Rank(results []Result) []string {
	for index := range results {
		results[index].RecommendedForReview = false
	}
	sort.SliceStable(results, func(left, right int) bool {
		return compareResults(results[left], results[right]) < 0
	})
	rank := 0
	for index := range results {
		if results[index].Compatibility == "incompatible" {
			results[index].Rank = nil
			continue
		}
		rank++
		results[index].Rank = intPointer(rank)
	}
	currencySet := map[string]struct{}{}
	for index := range results {
		if results[index].Compatibility != "compatible" {
			continue
		}
		if plan := selectedPlan(results[index].Sourcing); plan != nil {
			currencySet[strings.ToUpper(plan.Currency)] = struct{}{}
		}
	}
	currencies := make([]string, 0, len(currencySet))
	for currency := range currencySet {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	if len(currencies) > 1 {
		return currencies
	}
	for index := range results {
		if results[index].Compatibility == "compatible" &&
			results[index].Sourcing != nil &&
			results[index].Sourcing.Status == "priced" &&
			selectedPlan(results[index].Sourcing) != nil {
			results[index].RecommendedForReview = true
			break
		}
	}
	return currencies
}

func commonComparisons(original, candidate PartSpec) []FieldComparison {
	comparisons := []FieldComparison{
		compareNormalizedString("package", "equal", original.Package, candidate.Package, normalizeToken),
		compareIntegers("temperature_min_c", "candidate_lte", original.TemperatureMinC, candidate.TemperatureMinC, -1),
		compareIntegers("temperature_max_c", "candidate_gte", original.TemperatureMaxC, candidate.TemperatureMaxC, 1),
	}
	for _, dimension := range []struct {
		name      string
		original  *string
		candidate *string
	}{
		{"length_mm", original.LengthMM, candidate.LengthMM},
		{"width_mm", original.WidthMM, candidate.WidthMM},
		{"height_mm", original.HeightMM, candidate.HeightMM},
	} {
		if dimension.original != nil {
			comparisons = append(comparisons, compareDecimals(
				dimension.name,
				"candidate_lte",
				dimension.original,
				dimension.candidate,
				-1,
			))
		}
	}
	if len(original.Qualifications) > 0 {
		comparisons = append(
			comparisons,
			compareQualifications(original.Qualifications, candidate.Qualifications),
		)
	}
	return comparisons
}

func resistorComparisons(original, candidate PartSpec) []FieldComparison {
	return []FieldComparison{
		compareDecimals("resistance_ohms", "equal", original.ResistanceOhms, candidate.ResistanceOhms, 0),
		compareDecimals("tolerance_percent", "candidate_lte", original.TolerancePercent, candidate.TolerancePercent, -1),
		compareDecimals("power_watts", "candidate_gte", original.PowerWatts, candidate.PowerWatts, 1),
		compareDecimals("voltage_volts", "candidate_gte", original.VoltageVolts, candidate.VoltageVolts, 1),
		compareNormalizedString("technology", "equal", original.Technology, candidate.Technology, normalizeToken),
	}
}

func capacitorComparisons(original, candidate PartSpec) []FieldComparison {
	comparisons := []FieldComparison{
		compareDecimals("capacitance_farads", "equal", original.CapacitanceFarads, candidate.CapacitanceFarads, 0),
		compareDecimals("tolerance_percent", "candidate_lte", original.TolerancePercent, candidate.TolerancePercent, -1),
		compareDecimals("voltage_volts", "candidate_gte", original.VoltageVolts, candidate.VoltageVolts, 1),
		compareNormalizedString("dielectric", "equal", original.Dielectric, candidate.Dielectric, normalizeDielectric),
		compareBooleans("polarized", "equal", original.Polarized, candidate.Polarized),
	}
	if original.ESROhms != nil {
		comparisons = append(
			comparisons,
			compareDecimals("esr_ohms", "candidate_lte", original.ESROhms, candidate.ESROhms, -1),
		)
	}
	return comparisons
}

func inductorComparisons(original, candidate PartSpec) []FieldComparison {
	return []FieldComparison{
		compareDecimals("inductance_henries", "equal", original.InductanceHenries, candidate.InductanceHenries, 0),
		compareDecimals("tolerance_percent", "candidate_lte", original.TolerancePercent, candidate.TolerancePercent, -1),
		compareDecimals("rated_current_amps", "candidate_gte", original.RatedCurrentAmps, candidate.RatedCurrentAmps, 1),
		compareDecimals("saturation_current_amps", "candidate_gte", original.SaturationAmps, candidate.SaturationAmps, 1),
		compareDecimals("dc_resistance_ohms", "candidate_lte", original.DCResistanceOhms, candidate.DCResistanceOhms, -1),
		compareBooleans("shielded", "equal", original.Shielded, candidate.Shielded),
	}
}

func compareDecimals(
	field, requirement string,
	original, candidate *string,
	betterDirection int,
) FieldComparison {
	comparison := FieldComparison{
		Field:          field,
		Requirement:    requirement,
		OriginalValue:  cloneString(original),
		CandidateValue: cloneString(candidate),
		Relation:       "unknown",
	}
	if original == nil || candidate == nil {
		return comparison
	}
	left, leftOK := new(big.Rat).SetString(*original)
	right, rightOK := new(big.Rat).SetString(*candidate)
	if !leftOK || !rightOK {
		return comparison
	}
	order := right.Cmp(left)
	switch {
	case order == 0:
		comparison.Relation = "equal"
	case betterDirection < 0 && order < 0:
		comparison.Relation = "better"
	case betterDirection > 0 && order > 0:
		comparison.Relation = "better"
	default:
		comparison.Relation = "worse"
	}
	return comparison
}

func compareNormalizedString(
	field, requirement, original, candidate string,
	normalize func(string) string,
) FieldComparison {
	originalValue, candidateValue := stringPointer(original), stringPointer(candidate)
	comparison := FieldComparison{
		Field:          field,
		Requirement:    requirement,
		OriginalValue:  originalValue,
		CandidateValue: candidateValue,
		Relation:       "unknown",
	}
	if originalValue == nil || candidateValue == nil {
		return comparison
	}
	if normalize(original) == normalize(candidate) {
		comparison.Relation = "equal"
	} else {
		comparison.Relation = "worse"
	}
	return comparison
}

func compareIntegers(
	field, requirement string,
	original, candidate *int,
	betterDirection int,
) FieldComparison {
	comparison := FieldComparison{
		Field:          field,
		Requirement:    requirement,
		OriginalValue:  integerString(original),
		CandidateValue: integerString(candidate),
		Relation:       "unknown",
	}
	if original == nil || candidate == nil {
		return comparison
	}
	switch {
	case *candidate == *original:
		comparison.Relation = "equal"
	case betterDirection < 0 && *candidate < *original:
		comparison.Relation = "better"
	case betterDirection > 0 && *candidate > *original:
		comparison.Relation = "better"
	default:
		comparison.Relation = "worse"
	}
	return comparison
}

func compareBooleans(
	field, requirement string,
	original, candidate *bool,
) FieldComparison {
	comparison := FieldComparison{
		Field:          field,
		Requirement:    requirement,
		OriginalValue:  boolString(original),
		CandidateValue: boolString(candidate),
		Relation:       "unknown",
	}
	if original == nil || candidate == nil {
		return comparison
	}
	if *original == *candidate {
		comparison.Relation = "equal"
	} else {
		comparison.Relation = "worse"
	}
	return comparison
}

func compareQualifications(
	original, candidate []string,
) FieldComparison {
	comparison := FieldComparison{
		Field:          "qualifications",
		Requirement:    "candidate_superset",
		OriginalValue:  sliceString(original),
		CandidateValue: sliceString(candidate),
		Relation:       "unknown",
	}
	if candidate == nil {
		return comparison
	}
	required := normalizedSet(original)
	available := normalizedSet(candidate)
	for qualification := range required {
		if _, exists := available[qualification]; !exists {
			comparison.Relation = "worse"
			return comparison
		}
	}
	if len(available) > len(required) {
		comparison.Relation = "better"
	} else {
		comparison.Relation = "equal"
	}
	return comparison
}

func compareResults(left, right Result) int {
	if rank := compatibilityRank(left.Compatibility) - compatibilityRank(right.Compatibility); rank != 0 {
		return rank
	}
	if rank := sourcingRank(left.Sourcing) - sourcingRank(right.Sourcing); rank != 0 {
		return rank
	}
	leftPlan, rightPlan := selectedPlan(left.Sourcing), selectedPlan(right.Sourcing)
	if leftPlan != nil && rightPlan != nil &&
		strings.EqualFold(leftPlan.Currency, rightPlan.Currency) &&
		leftPlan.ExtendedPrice != rightPlan.ExtendedPrice {
		if leftPlan.ExtendedPrice < rightPlan.ExtendedPrice {
			return -1
		}
		return 1
	}
	leftStock, rightStock := availableStock(left.Sourcing), availableStock(right.Sourcing)
	if leftStock != rightStock {
		if leftStock > rightStock {
			return -1
		}
		return 1
	}
	leftKey, rightKey := partKey(left.Candidate), partKey(right.Candidate)
	return strings.Compare(leftKey, rightKey)
}

func selectedPlan(result *SourcingResult) *procurement.PurchasePlan {
	if result == nil {
		return nil
	}
	for index := range result.Offers {
		if result.Offers[index].SelectedPlan != nil {
			return result.Offers[index].SelectedPlan
		}
	}
	return nil
}

func availableStock(result *SourcingResult) int {
	if result == nil {
		return -1
	}
	for index := range result.Offers {
		if result.Offers[index].Provider == result.SelectedProvider &&
			result.Offers[index].DistributorPartNumber ==
				result.SelectedDistributorPartNumber &&
			result.Offers[index].AvailableQuantity != nil {
			return *result.Offers[index].AvailableQuantity
		}
	}
	return -1
}

func compatibilityRank(value string) int {
	switch value {
	case "compatible":
		return 0
	case "unknown":
		return 1
	default:
		return 2
	}
}

func sourcingRank(result *SourcingResult) int {
	if result == nil {
		return 9
	}
	switch result.Status {
	case "priced":
		return 0
	case "review":
		return 1
	case "shortage":
		return 2
	case "stock_unknown":
		return 3
	case "unavailable":
		return 4
	case "not_found":
		return 5
	case "provider_error":
		return 6
	default:
		return 7
	}
}

func normalizeToken(value string) string {
	var output strings.Builder
	for _, character := range strings.ToUpper(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func normalizeDielectric(value string) string {
	normalized := normalizeToken(value)
	if normalized == "NP0" {
		return "C0G"
	}
	return normalized
}

func normalizedSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalizeToken(value); normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func integerString(value *int) *string {
	if value == nil {
		return nil
	}
	text := strconv.Itoa(*value)
	return &text
}

func boolString(value *bool) *string {
	if value == nil {
		return nil
	}
	text := strconv.FormatBool(*value)
	return &text
}

func sliceString(values []string) *string {
	if values == nil {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for value := range normalizedSet(values) {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	text := strings.Join(normalized, ",")
	return &text
}

func intPointer(value int) *int {
	return &value
}
