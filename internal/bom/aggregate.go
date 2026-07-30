// Package bom aggregates validated designs into deterministic sourcing demand.
package bom

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type partKey struct {
	partNumber   string
	manufacturer string
}

type accumulator struct {
	demand procurement.Demand
}

// Aggregate merges duplicate parts and scales demand for units and attrition.
func Aggregate(
	designs []contract.Design,
	units int,
	attrition money.Decimal,
) ([]procurement.Demand, error) {
	if units < 1 {
		return nil, errors.New("units must be positive")
	}
	if attrition.Micros() < 0 || attrition.Micros() > money.Scale {
		return nil, errors.New("attrition must be between 0 and 1")
	}

	parts := make(map[partKey]*accumulator)
	for _, design := range designs {
		for _, part := range design.Parts {
			key := partKey{
				partNumber:   strings.ToUpper(strings.TrimSpace(part.PartNumber)),
				manufacturer: strings.ToUpper(strings.TrimSpace(part.Manufacturer)),
			}
			item, exists := parts[key]
			if !exists {
				item = &accumulator{demand: procurement.Demand{
					PartNumber:   part.PartNumber,
					Manufacturer: part.Manufacturer,
					Description:  pointerValue(part.Description),
					Package:      pointerValue(part.Package),
					Pins:         intPointerValue(part.Pins),
				}}
				parts[key] = item
			}
			if part.Quantity > math.MaxInt-item.demand.QuantityPerUnit {
				return nil, errors.New("quantity per unit overflow")
			}
			item.demand.QuantityPerUnit += part.Quantity
			if part.Reference != nil && strings.TrimSpace(*part.Reference) != "" {
				item.demand.References = append(
					item.demand.References,
					procurement.SourceReference{
						Design:    design.Design,
						Reference: strings.TrimSpace(*part.Reference),
					},
				)
			}
		}
	}

	results := make([]procurement.Demand, 0, len(parts))
	for _, item := range parts {
		required, err := scaledQuantity(
			item.demand.QuantityPerUnit,
			units,
			attrition.Micros(),
		)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.demand.PartNumber, err)
		}
		item.demand.RequiredQuantity = required
		sort.SliceStable(item.demand.References, func(left, right int) bool {
			a, b := item.demand.References[left], item.demand.References[right]
			if a.Design != b.Design {
				return a.Design < b.Design
			}
			return a.Reference < b.Reference
		})
		results = append(results, item.demand)
	}
	sort.SliceStable(results, func(left, right int) bool {
		a, b := results[left], results[right]
		if strings.ToUpper(a.Manufacturer) != strings.ToUpper(b.Manufacturer) {
			return strings.ToUpper(a.Manufacturer) < strings.ToUpper(b.Manufacturer)
		}
		return strings.ToUpper(a.PartNumber) < strings.ToUpper(b.PartNumber)
	})
	return results, nil
}

func scaledQuantity(perUnit, units int, attritionMicros int64) (int, error) {
	if perUnit > math.MaxInt/units {
		return 0, errors.New("required quantity overflow")
	}
	base := perUnit * units
	if base == 0 || attritionMicros == 0 {
		return base, nil
	}
	if int64(base) > math.MaxInt64/attritionMicros {
		return 0, errors.New("attrition calculation overflow")
	}
	extraNumerator := int64(base) * attritionMicros
	if extraNumerator > math.MaxInt64-(money.Scale-1) {
		return 0, errors.New("attrition calculation overflow")
	}
	extra := (extraNumerator + money.Scale - 1) / money.Scale
	if extra > int64(math.MaxInt-base) {
		return 0, errors.New("required quantity overflow")
	}
	return base + int(extra), nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func intPointerValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
