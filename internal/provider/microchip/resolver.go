// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package microchip

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

// Resolver turns Microchip catalog records into availability and
// lifecycle EVIDENCE. Offers never carry prices or purchase plans and
// are always review-required: this provider can prove that factory
// stock exists, but a human orders it.
type Resolver struct {
	client *Client
}

// NewResolver constructs a resolver around a Microchip client.
func NewResolver(client *Client) (*Resolver, error) {
	if client == nil {
		return nil, fmt.Errorf("Microchip client is required")
	}
	return &Resolver{client: client}, nil
}

// Lookup resolves one Microchip orderable part number to evidence.
func (resolver *Resolver) Lookup(
	ctx context.Context,
	demand procurement.Demand,
) (procurement.SourcedPart, error) {
	result := procurement.SourcedPart{Demand: demand}
	if !supportsManufacturer(demand.Manufacturer) {
		result.Status = "not_applicable"
		result.IssueCode = "PROVIDER_NOT_APPLICABLE"
		result.IssueMessage = "Microchip catalog evidence applies only to Microchip (or Atmel) parts"
		return result, nil
	}
	products, err := resolver.client.Products(ctx, demand.PartNumber)
	if err != nil {
		return result, err
	}
	if len(products) == 0 {
		// The API matches on prefixes; a fully suffixed orderable part
		// number sometimes needs the base-part fallback query.
		fallback := basePartQuery(demand.PartNumber)
		if fallback != "" && !strings.EqualFold(fallback, strings.TrimSpace(demand.PartNumber)) {
			products, err = resolver.client.Products(ctx, fallback)
			if err != nil {
				return result, err
			}
		}
	}

	query := normalizePartNumber(demand.PartNumber)
	var exact *Product
	for index := range products {
		if normalizePartNumber(products[index].PartNumber) == query && query != "" {
			exact = &products[index]
			break
		}
	}
	if exact == nil {
		if len(products) > 0 {
			result.Status = "not_found"
			result.CandidateCount = len(products)
			result.IssueCode = "PART_NUMBER_MISMATCH"
			result.IssueMessage = fmt.Sprintf(
				"Microchip catalog has no exact orderable match; %d related variants exist",
				len(products),
			)
			return result, nil
		}
		result.Status = "not_found"
		result.IssueCode = "PART_NOT_FOUND"
		result.IssueMessage = "Microchip catalog returned no matching part"
		return result, nil
	}

	offer := offerFromProduct(demand, *exact)
	result.Offer = &offer
	result.CandidateCount = 1
	switch {
	case isEndOfLife(exact.LifecycleStatus):
		result.Status = "review"
		result.IssueCode = "LIFECYCLE_WARNING"
		result.IssueMessage = fmt.Sprintf(
			"Microchip lists lifecycle status %s for this part%s",
			exact.LifecycleStatus,
			stockClause(exact.InStockQuantity),
		)
	case exact.InStockQuantity == nil:
		result.Status = "stock_unknown"
		result.IssueCode = "STOCK_UNKNOWN"
		result.IssueMessage = "Microchip catalog did not report factory stock for this part"
	case *exact.InStockQuantity >= demand.RequiredQuantity:
		result.Status = "review"
		result.IssueCode = "MANUFACTURER_EVIDENCE_ONLY"
		result.IssueMessage = "Microchip factory stock covers the demand; the catalog carries no pricing, order via microchipDIRECT or distribution"
	default:
		result.Status = "shortage"
		result.IssueCode = "INSUFFICIENT_STOCK"
		result.IssueMessage = fmt.Sprintf(
			"Microchip factory stock %d is below required quantity %d%s",
			*exact.InStockQuantity,
			demand.RequiredQuantity,
			leadClause(exact.LeadTimeWeeks),
		)
	}
	return result, nil
}

func offerFromProduct(
	demand procurement.Demand,
	product Product,
) procurement.Offer {
	offer := procurement.Offer{
		Provider:               "microchip",
		DistributorPartNumber:  product.PartNumber,
		ManufacturerPartNumber: product.PartNumber,
		Manufacturer:           "Microchip",
		Description:            product.Description,
		Category:               product.Category,
		MatchMethod:            "exact",
		// Evidence without pricing can never clear engineering review.
		ReviewRequired:    true,
		RequiredQuantity:  demand.RequiredQuantity,
		AvailableQuantity: product.InStockQuantity,
		LifecycleStatus:   product.LifecycleStatus,
		Packaging:         product.PackagingType,
		DatasheetURL:      product.DatasheetURL,
		ProductURL:        "https://www.microchipdirect.com/product/" + product.PartNumber,
	}
	if product.InStockQuantity != nil {
		offer.Availability = fmt.Sprintf(
			"%d in stock (Microchip factory direct)",
			*product.InStockQuantity,
		)
	}
	if product.LeadTimeWeeks != nil {
		offer.LeadTime = fmt.Sprintf("%d weeks", *product.LeadTimeWeeks)
	}
	if product.MinimumOrderQuantity != nil {
		offer.MinimumOrderQuantity = *product.MinimumOrderQuantity
	}
	if product.OrderMultiple != nil {
		offer.OrderMultiple = *product.OrderMultiple
	}
	return offer
}

func supportsManufacturer(manufacturer string) bool {
	normalized := strings.ToLower(strings.TrimSpace(manufacturer))
	return strings.Contains(normalized, "microchip") ||
		strings.Contains(normalized, "atmel")
}

func isEndOfLife(lifecycle string) bool {
	switch lifecycle {
	case "EOL", "NRND", "DISCONTINUED", "OBSOLETE":
		return true
	default:
		return false
	}
}

// normalizePartNumber keeps letters and digits only, uppercased, so
// "dsPIC33AK512MPS506-E/PT" and "DSPIC33AK512MPS506-E/PT" compare equal
// while distinct variants (the tape-reel "T" infix) stay distinct.
func normalizePartNumber(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToUpper(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// basePartQuery returns the leading alphanumeric run of a part number
// ("DSPIC33AK512MPS506" from "DSPIC33AK512MPS506-E/PT"), or empty when
// it would be too short to query.
func basePartQuery(partNumber string) string {
	trimmed := strings.TrimSpace(partNumber)
	end := 0
	for _, character := range trimmed {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			break
		}
		end += len(string(character))
	}
	base := trimmed[:end]
	if len(base) < minQueryLength {
		return ""
	}
	return base
}

func stockClause(stock *int) string {
	if stock == nil {
		return ""
	}
	return fmt.Sprintf(" (%d in factory stock)", *stock)
}

func leadClause(leadWeeks *int) string {
	if leadWeeks == nil {
		return ""
	}
	return fmt.Sprintf(" (replenishment lead %d weeks)", *leadWeeks)
}
