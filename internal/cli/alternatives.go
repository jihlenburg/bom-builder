package cli

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/alternatives"
	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/sourcing"
)

func runAlternatives(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	remaining, onlyIfShortage, err := consumeFlag(remaining, "--only-if-shortage")
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, providersValue, hasProviders, err := consumeValueFlag(remaining, "--providers")
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, deadlineValue, hasDeadline, err := consumeValueFlag(remaining, "--deadline")
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, cacheConfig, err := consumeCacheFlags(remaining)
	if err != nil {
		return emitError(
			stdout,
			"alternatives",
			"INVALID_CACHE_CONFIGURATION",
			err.Error(),
			contract.ExitInput,
			pretty,
		)
	}
	if len(remaining) != 1 {
		return emitError(
			stdout,
			"alternatives",
			"INVALID_ARGUMENT",
			"expected exactly one alternatives request file or '-'",
			contract.ExitInput,
			pretty,
		)
	}
	if remaining[0] != "-" && strings.HasPrefix(remaining[0], "-") {
		// A flag-shaped leftover ("--bogus") must surface as a usage
		// error, not be opened as a file; the bare "-" stdin marker
		// stays valid.
		return emitUnexpected(stdout, "alternatives", []string{remaining[0]}, pretty)
	}
	request, err := alternatives.Load(remaining[0], stdin)
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	providerNames, err := resolveProviderSelection(
		providersValue,
		hasProviders,
		cacheConfig.Policy,
	)
	if err != nil {
		return emitProviderSelectionError(stdout, "alternatives", err, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, hasDeadline)
	if err != nil {
		return emitError(stdout, "alternatives", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	runtimes, cacheSession, err := newProviderRuntimes(providerNames, cacheConfig)
	if err != nil {
		return emitProviderRuntimeSetupError(stdout, "alternatives", err, pretty)
	}
	defer closeProviderRuntimeResources(runtimes, cacheSession)
	bindings := make([]sourcing.ProviderResolver, 0, len(runtimes))
	for _, runtime := range runtimes {
		bindings = append(bindings, sourcing.ProviderResolver{
			Name: runtime.name, Resolver: runtime.resolver,
		})
	}
	resolver, err := sourcing.NewMultiResolver(bindings)
	if err != nil {
		return emitError(stdout, "alternatives", "INTERNAL_ERROR", err.Error(), contract.ExitInternal, pretty)
	}

	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	results := alternatives.Evaluate(request)
	var (
		warnings           []contract.Issue
		attemptCount       int
		providerErrorCount int
	)
	originalSourcing, sourceErr := sourceAlternative(
		ctx,
		resolver,
		request.Original,
		request.RequiredQuantity,
	)
	attemptCount++
	if sourceErr != nil {
		providerErrorCount++
		warnings = append(warnings, alternativeProviderIssue(
			request.Original.PartNumber,
			sourceErr,
		))
	}
	originalSufficient := onlyIfShortage &&
		sourceErr == nil &&
		originalSourcing.Status == "priced"
	if !originalSufficient {
		for index := range results {
			if results[index].Compatibility == "incompatible" {
				continue
			}
			sourced, lookupErr := sourceAlternative(
				ctx,
				resolver,
				results[index].Candidate,
				request.RequiredQuantity,
			)
			attemptCount++
			results[index].Sourcing = sourced
			if lookupErr != nil {
				providerErrorCount++
				warnings = append(warnings, alternativeProviderIssue(
					results[index].Candidate.PartNumber,
					lookupErr,
				))
			}
		}
	}
	currencies := alternatives.Rank(results)
	if len(currencies) > 1 {
		warnings = append(warnings, contract.Issue{
			Code:    "CURRENCY_COMPARISON_REQUIRED",
			Message: "stocked alternative plans use different currencies; no preferred candidate was selected",
			Details: map[string]any{"currencies": currencies},
		})
	}
	summary := summarizeAlternatives(results, providerErrorCount)
	status := "incomplete"
	exitCode := contract.ExitIncomplete
	var outputErrors []contract.Issue
	switch {
	case originalSufficient:
		status = "not_needed"
		exitCode = contract.ExitOK
		warnings = append(warnings, contract.Issue{
			Code:    "ORIGINAL_STOCK_SUFFICIENT",
			Message: "the original part has a safe in-stock plan; candidate sourcing was skipped",
		})
	case attemptCount > 0 && providerErrorCount == attemptCount:
		status = "failed"
		exitCode = contract.ExitProvider
		outputErrors = warnings
		warnings = []contract.Issue{}
	case summary.RecommendedCount > 0:
		status = "review_required"
		warnings = append(warnings, contract.Issue{
			Code:    "ENGINEERING_REVIEW_REQUIRED",
			Message: "the top stocked candidate passed supplied-data checks but still requires engineering approval",
			Details: map[string]any{
				"part_number": results[0].Candidate.PartNumber,
			},
		})
	case len(currencies) > 1 && summary.InStockCount > 0:
		status = "incomplete"
	default:
		warnings = append(warnings, contract.Issue{
			Code:    "NO_STOCKED_COMPATIBLE_ALTERNATIVE",
			Message: "no deterministically compatible candidate has a safe in-stock purchase plan",
		})
	}
	providerRuns, totalRequests := providerRunMetadata(runtimes)
	envelope := contract.AlternativesEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        status,
		ExitCode:      exitCode,
		Command:       "alternatives",
		Version:       app.Version,
		Run: contract.RunMetadata{
			RunID:        newRunID(),
			StartedAt:    started,
			DurationMS:   time.Since(started).Milliseconds(),
			Providers:    providerRuns,
			RequestCount: totalRequests,
			Cache:        cacheRunMetadata(cacheSession),
		},
		Kind:             request.Kind,
		RequiredQuantity: request.RequiredQuantity,
		Original:         request.Original,
		OriginalSourcing: originalSourcing,
		Summary:          summary,
		Candidates:       results,
		Warnings:         warnings,
		Errors:           outputErrors,
	}
	if envelope.Warnings == nil {
		envelope.Warnings = []contract.Issue{}
	}
	if envelope.Errors == nil {
		envelope.Errors = []contract.Issue{}
	}
	return emitJSONWithExit(stdout, envelope, pretty, exitCode)
}

func sourceAlternative(
	ctx context.Context,
	resolver sourcing.Resolver,
	spec alternatives.PartSpec,
	requiredQuantity int,
) (*alternatives.SourcingResult, error) {
	demand := procurement.Demand{
		PartNumber:       spec.PartNumber,
		Manufacturer:     spec.Manufacturer,
		QuantityPerUnit:  requiredQuantity,
		RequiredQuantity: requiredQuantity,
		Package:          spec.Package,
	}
	result, err := resolver.Lookup(ctx, demand)
	if err != nil {
		return alternatives.CompactSourcing(procurement.SourcedPart{
			Demand:       demand,
			Status:       "provider_error",
			IssueCode:    "PROVIDER_ERROR",
			IssueMessage: err.Error(),
		}), err
	}
	return alternatives.CompactSourcing(result), nil
}

func alternativeProviderIssue(
	partNumber string,
	err error,
) contract.Issue {
	return contract.Issue{
		Code:    "ALTERNATIVE_PROVIDER_ERROR",
		Message: err.Error(),
		Details: map[string]any{"part_number": partNumber},
	}
}

func summarizeAlternatives(
	results []alternatives.Result,
	providerErrorCount int,
) contract.AlternativeSummary {
	summary := contract.AlternativeSummary{
		CandidateCount:     len(results),
		ProviderErrorCount: providerErrorCount,
	}
	for _, result := range results {
		switch result.Compatibility {
		case "compatible":
			summary.CompatibleCount++
		case "unknown":
			summary.UnknownCount++
		case "incompatible":
			summary.IncompatibleCount++
		}
		if result.Sourcing != nil && result.Sourcing.Status != "provider_error" {
			summary.SourcedCount++
		}
		if result.Sourcing != nil && result.Sourcing.Status == "priced" {
			summary.InStockCount++
		}
		if result.RecommendedForReview {
			summary.RecommendedCount++
		}
	}
	return summary
}
