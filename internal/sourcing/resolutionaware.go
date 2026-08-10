// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package sourcing

import (
	"context"
	"fmt"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

// ResolutionSource yields the active human-approved resolution for one
// demand identity. *resolutions.Store satisfies it.
type ResolutionSource interface {
	ActiveResolution(
		ctx context.Context,
		manufacturer, partNumber string,
	) (resolutions.Record, bool, error)
}

// ResolutionAwareResolver redirects demands with an active human-approved
// resolution to the approved replacement before provider lookup, and
// annotates the result with the approval's identity. Demands without a
// resolution pass through untouched.
type ResolutionAwareResolver struct {
	source ResolutionSource
	inner  Resolver
}

// NewResolutionAware wraps a resolver with resolution consumption.
func NewResolutionAware(
	source ResolutionSource,
	inner Resolver,
) (*ResolutionAwareResolver, error) {
	if source == nil || inner == nil {
		return nil, fmt.Errorf("a resolution source and an inner resolver are required")
	}
	return &ResolutionAwareResolver{source: source, inner: inner}, nil
}

// Lookup implements Resolver.
func (resolver *ResolutionAwareResolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	record, found, err := resolver.source.ActiveResolution(
		ctx,
		demand.Manufacturer,
		demand.PartNumber,
	)
	if err != nil {
		// Fail closed: the operator asked for resolution-aware sourcing,
		// so a broken store is an explicit failure, not a silent skip.
		return procurement.SourcedPart{}, fmt.Errorf("resolutions store: %w", err)
	}
	if !found {
		return resolver.inner.Lookup(ctx, demand)
	}
	replacementDemand := demand
	replacementDemand.Manufacturer = record.Replacement.Manufacturer
	replacementDemand.PartNumber = record.Replacement.PartNumber
	sourced, err := resolver.inner.Lookup(ctx, replacementDemand)
	if err != nil {
		return sourced, err
	}
	// The BOM line keeps its original identity; the resolution block
	// explains why the offers name a different part.
	sourced.Demand = demand
	applied := &procurement.AppliedResolution{
		ResolutionID:            record.ResolutionID,
		ApprovedBy:              record.ApprovedBy,
		ApprovedAt:              record.ApprovedAt,
		OriginalManufacturer:    demand.Manufacturer,
		OriginalPartNumber:      demand.PartNumber,
		ReplacementManufacturer: record.Replacement.Manufacturer,
		ReplacementPartNumber:   record.Replacement.PartNumber,
		Provider:                record.Replacement.Provider,
		ProviderSKU:             record.Replacement.ProviderSKU,
	}
	sourced.Resolution = applied
	liftPinnedReview(&sourced, record, demand.RequiredQuantity)
	return sourced, nil
}

// liftPinnedReview clears the review requirement for exactly one case: the
// human approved a specific provider-orderable SKU, that very SKU came
// back as a review-required offer (typically a packaging-variant match),
// and its stock-verified plan covers the demand. The stored approval IS
// the completed engineering review of that SKU. Anything looser — a
// different SKU, a different provider, unverified stock, or an already
// safe result — leaves the outcome untouched.
func liftPinnedReview(
	sourced *procurement.SourcedPart,
	record resolutions.Record,
	requiredQuantity int,
) {
	if record.Replacement.Provider == "" || record.Replacement.ProviderSKU == "" {
		return
	}
	if sourced.Status != "review" {
		return
	}
	if sourced.Offer != nil && sourced.Offer.SelectedPlan != nil &&
		!sourced.Offer.ReviewRequired {
		return
	}
	for index := range sourced.Offers {
		offer := &sourced.Offers[index]
		if !offer.ReviewRequired {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(offer.Provider), record.Replacement.Provider) {
			continue
		}
		if !strings.EqualFold(
			strings.TrimSpace(offer.DistributorPartNumber),
			record.Replacement.ProviderSKU,
		) {
			continue
		}
		plan := offer.CandidatePlan
		if plan == nil || !plan.StockVerified || plan.PurchasedQuantity < requiredQuantity {
			return
		}
		offer.ReviewRequired = false
		offer.SelectedPlan = plan
		selected := *offer
		sourced.Offer = &selected
		sourced.Status = "priced"
		sourced.IssueCode = ""
		sourced.IssueMessage = ""
		sourced.Resolution.ReviewLifted = true
		return
	}
}
