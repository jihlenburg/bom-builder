// Package sourcing orchestrates normalized provider resolution and safe totals.
package sourcing

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Resolver is the provider-independent lookup boundary.
type Resolver interface {
	Lookup(context.Context, procurement.Demand) (procurement.SourcedPart, error)
}

// Result is the provider-independent sourcing outcome used by CLI envelopes.
type Result struct {
	Status   string
	ExitCode int
	Summary  procurement.PricingSummary
	Parts    []procurement.SourcedPart
	Warnings []contract.Issue
	Errors   []contract.Issue
}

// Source resolves demands sequentially and computes only proven common-currency totals.
func Source(
	ctx context.Context,
	resolver Resolver,
	demands []procurement.Demand,
	units int,
) Result {
	result := Result{
		Status:   "complete",
		ExitCode: contract.ExitOK,
		Parts:    make([]procurement.SourcedPart, 0, len(demands)),
		Summary: procurement.PricingSummary{
			LineCount: len(demands),
		},
		Warnings: []contract.Issue{},
		Errors:   []contract.Issue{},
	}

	total := money.Decimal(0)
	currency := ""
	mixedCurrency := false
	for _, demand := range demands {
		sourced, err := resolver.Lookup(ctx, demand)
		if err != nil {
			sourced = procurement.SourcedPart{
				Demand:       demand,
				Status:       "provider_error",
				IssueCode:    "PROVIDER_REQUEST_FAILED",
				IssueMessage: err.Error(),
			}
			result.Errors = append(result.Errors, contract.Issue{
				Code:    "PROVIDER_REQUEST_FAILED",
				Message: err.Error(),
				Details: map[string]any{"part_number": demand.PartNumber},
			})
		}
		result.Parts = append(result.Parts, sourced)
		switch sourced.Status {
		case "priced":
			if sourced.Offer == nil ||
				sourced.Offer.SelectedPlan == nil ||
				sourced.Offer.ReviewRequired ||
				!sourced.Offer.SelectedPlan.StockVerified ||
				sourced.Offer.SelectedPlan.PurchasedQuantity < demand.RequiredQuantity {
				result.Errors = append(result.Errors, contract.Issue{
					Code:    "INTERNAL_CONTRACT_ERROR",
					Message: "priced part did not contain a safe selected purchase plan",
				})
				result.Summary.ProviderErrors++
				result.Parts[len(result.Parts)-1].Status = "provider_error"
				continue
			}
			result.Summary.PricedCount++
			plan := sourced.Offer.SelectedPlan
			if currency == "" {
				currency = plan.Currency
			} else if !strings.EqualFold(currency, plan.Currency) {
				mixedCurrency = true
			}
			var addErr error
			total, addErr = total.Add(plan.ExtendedPrice)
			if addErr != nil {
				result.Errors = append(result.Errors, contract.Issue{
					Code:    "MONEY_OVERFLOW",
					Message: addErr.Error(),
				})
				result.Summary.ProviderErrors++
			}
		case "shortage", "stock_unknown", "unavailable":
			result.Summary.ShortageCount++
		case "review":
			result.Summary.ReviewCount++
		case "not_found":
			result.Summary.NotFoundCount++
		case "not_applicable":
			result.Summary.NotApplicableCount++
		case "provider_error":
			result.Summary.ProviderErrors++
		default:
			// A status outside the contract must not fall through
			// uncounted: the summary would stop adding up to the
			// line count and the line would degrade silently.
			result.Errors = append(result.Errors, contract.Issue{
				Code: "INTERNAL_CONTRACT_ERROR",
				Message: fmt.Sprintf(
					"resolver returned unknown status %q for %s",
					sourced.Status, demand.PartNumber,
				),
			})
			result.Summary.ProviderErrors++
			result.Parts[len(result.Parts)-1].Status = "provider_error"
		}
		if sourced.IssueCode != "" && sourced.Status != "provider_error" {
			result.Warnings = append(result.Warnings, contract.Issue{
				Code:    sourced.IssueCode,
				Message: sourced.IssueMessage,
				Details: map[string]any{"part_number": demand.PartNumber},
			})
		}
	}

	if mixedCurrency {
		result.Errors = append(result.Errors, contract.Issue{
			Code:    "MIXED_CURRENCY",
			Message: "safe BOM total omitted because selected plans use different currencies",
		})
		result.Summary.ProviderErrors++
	} else if result.Summary.PricedCount > 0 && result.Summary.ProviderErrors == 0 {
		result.Summary.Currency = strings.ToUpper(currency)
		result.Summary.TotalCost = decimalPointer(total)
		if units > 0 {
			perUnit, err := total.DivInt(units)
			if err == nil {
				result.Summary.CostPerUnit = decimalPointer(perUnit)
			}
		}
	}

	incomplete := result.Summary.PricedCount != result.Summary.LineCount
	switch {
	case result.Summary.ProviderErrors > 0:
		result.Status = "failed"
		if result.Summary.PricedCount > 0 {
			result.Status = "partial"
		}
		result.ExitCode = contract.ExitProvider
	case incomplete:
		result.Status = "incomplete"
		result.ExitCode = contract.ExitIncomplete
	}
	return result
}

func decimalPointer(value money.Decimal) *money.Decimal {
	return &value
}
