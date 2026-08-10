// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package provider

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/provider/digikey"
	"github.com/jihlenburg/bom-builder/internal/provider/microchip"
	"github.com/jihlenburg/bom-builder/internal/provider/mouser"
	"github.com/jihlenburg/bom-builder/internal/provider/ti"
)

// Check returns configuration status and optionally performs bounded live checks.
func Check(
	ctx context.Context,
	names []string,
	live bool,
) (contract.ProviderDiscoveryEnvelope, error) {
	discovery := Discover()
	selected, err := selectedNames(names)
	if err != nil {
		return contract.ProviderDiscoveryEnvelope{}, err
	}
	filtered := make([]contract.ProviderCapability, 0, len(selected))
	exitCode := contract.ExitOK
	status := "ok"
	for _, capability := range discovery.Providers {
		if _, wanted := selected[capability.Name]; !wanted {
			continue
		}
		if live {
			switch capability.Name {
			case "mouser":
				capability = checkMouser(ctx, capability)
			case "digikey":
				capability = checkDigiKey(ctx, capability)
			case "ti":
				capability = checkTI(ctx, capability)
			case "microchip":
				capability = checkMicrochip(ctx, capability)
			}
			if capability.Implemented && capability.Status == "failed" {
				exitCode = contract.ExitProvider
				status = "failed"
			}
		}
		filtered = append(filtered, capability)
	}
	return contract.ProviderDiscoveryEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        status,
		ExitCode:      exitCode,
		Live:          live,
		Providers:     filtered,
	}, nil
}

func checkMicrochip(
	ctx context.Context,
	capability contract.ProviderCapability,
) contract.ProviderCapability {
	capability.Live = true
	client, err := microchip.NewFromEnvironment()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = "CONFIGURATION_ERROR"
		capability.ErrorMessage = err.Error()
		return capability
	}
	started := time.Now()
	products, err := client.Products(ctx, "DSPIC33AK512MPS506")
	latency := time.Since(started).Milliseconds()
	capability.LatencyMS = &latency
	capability.RequestCount = client.RequestCount()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = providerErrorCode(err)
		capability.ErrorMessage = err.Error()
		return capability
	}
	if len(products) == 0 {
		capability.Status = "failed"
		capability.ErrorCode = "EMPTY_RESULT"
		capability.ErrorMessage = "representative Microchip catalog query returned no products"
		return capability
	}
	if !strings.HasPrefix(
		strings.ToUpper(products[0].PartNumber),
		"DSPIC33AK512MPS506",
	) {
		capability.Status = "failed"
		capability.ErrorCode = "UNEXPECTED_RESULT"
		capability.ErrorMessage = "representative Microchip catalog query returned an unexpected part"
		return capability
	}
	capability.Status = "ok"
	resultCount := len(products)
	capability.Details.ResultCount = &resultCount
	capability.Details.MatchedPartNumber = products[0].PartNumber
	return capability
}

func checkMouser(
	ctx context.Context,
	capability contract.ProviderCapability,
) contract.ProviderCapability {
	capability.Live = true
	if !capability.Configured {
		capability.Status = "failed"
		capability.ErrorCode = "NOT_CONFIGURED"
		capability.ErrorMessage = "Mouser API credentials are not configured"
		return capability
	}
	client, err := mouser.NewFromEnvironment()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = "CONFIGURATION_ERROR"
		capability.ErrorMessage = err.Error()
		return capability
	}
	started := time.Now()
	parts, err := client.Search(ctx, "RC0402FR-0710KL", "Yageo", true)
	latency := time.Since(started).Milliseconds()
	capability.LatencyMS = &latency
	capability.RequestCount = client.RequestCount()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = providerErrorCode(err)
		capability.ErrorMessage = err.Error()
		return capability
	}
	if len(parts) == 0 {
		capability.Status = "failed"
		capability.ErrorCode = "EMPTY_RESULT"
		capability.ErrorMessage = "representative Mouser search returned no parts"
		return capability
	}
	if !strings.EqualFold(parts[0].ManufacturerPartNumber, "RC0402FR-0710KL") {
		capability.Status = "failed"
		capability.ErrorCode = "UNEXPECTED_RESULT"
		capability.ErrorMessage = "representative Mouser search returned an unexpected part"
		return capability
	}
	capability.Status = "ok"
	resultCount := len(parts)
	capability.Details.ResultCount = &resultCount
	capability.Details.MatchedPartNumber = parts[0].ManufacturerPartNumber
	return capability
}

func checkTI(
	ctx context.Context,
	capability contract.ProviderCapability,
) contract.ProviderCapability {
	capability.Live = true
	if !capability.Configured {
		capability.Status = "failed"
		capability.ErrorCode = "NOT_CONFIGURED"
		capability.ErrorMessage = "TI Store API credentials are not configured"
		return capability
	}
	client, err := ti.NewFromEnvironment()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = "CONFIGURATION_ERROR"
		capability.ErrorMessage = err.Error()
		return capability
	}
	started := time.Now()
	product, err := client.Product(ctx, "TMP421AQDCNRQ1")
	latency := time.Since(started).Milliseconds()
	capability.LatencyMS = &latency
	capability.RequestCount = client.RequestCount()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = providerErrorCode(err)
		capability.ErrorMessage = err.Error()
		return capability
	}
	if !strings.EqualFold(product.TIPartNumber, "TMP421AQDCNRQ1") {
		capability.Status = "failed"
		capability.ErrorCode = "UNEXPECTED_RESULT"
		capability.ErrorMessage = "representative TI Store request returned an unexpected product"
		return capability
	}
	resultCount := 0
	for _, schedule := range product.Pricing {
		if strings.EqualFold(schedule.Currency, client.Currency()) {
			resultCount += len(schedule.PriceBreaks)
		}
	}
	if resultCount == 0 {
		capability.Status = "failed"
		capability.ErrorCode = "EMPTY_RESULT"
		capability.ErrorMessage = "representative TI Store request returned no requested-currency pricing"
		return capability
	}
	capability.Status = "ok"
	capability.Details.ResultCount = &resultCount
	capability.Details.MatchedPartNumber = product.TIPartNumber
	capability.Details.Currency = client.Currency()
	return capability
}

func checkDigiKey(
	ctx context.Context,
	capability contract.ProviderCapability,
) contract.ProviderCapability {
	capability.Live = true
	if !capability.Configured {
		capability.Status = "failed"
		capability.ErrorCode = "NOT_CONFIGURED"
		capability.ErrorMessage = "Digi-Key API credentials and account ID are not configured"
		return capability
	}
	client, err := digikey.NewFromEnvironment()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = "CONFIGURATION_ERROR"
		capability.ErrorMessage = err.Error()
		return capability
	}
	started := time.Now()
	pricing, err := client.PricingByQuantity(ctx, "P5555-ND", 100)
	latency := time.Since(started).Milliseconds()
	capability.LatencyMS = &latency
	capability.RequestCount = client.RequestCount()
	if err != nil {
		capability.Status = "failed"
		capability.ErrorCode = providerErrorCode(err)
		capability.ErrorMessage = err.Error()
		return capability
	}
	resultCount := len(pricing.MyPricingOptions) + len(pricing.StandardPricingOptions)
	if !strings.EqualFold(pricing.ManufacturerPartNumber, "ECA-1VHG102") ||
		resultCount == 0 {
		capability.Status = "failed"
		capability.ErrorCode = "UNEXPECTED_RESULT"
		capability.ErrorMessage = "representative Digi-Key pricing request returned an unexpected product"
		return capability
	}
	capability.Status = "ok"
	capability.Details.ResultCount = &resultCount
	capability.Details.MatchedPartNumber = pricing.ManufacturerPartNumber
	capability.Details.Currency = pricing.Currency
	capability.Details.HeaderMode = pricing.HeaderMode
	capability.Details.RateLimitRemaining = pricing.RateLimitRemaining
	return capability
}

func selectedNames(names []string) (map[string]struct{}, error) {
	if len(names) == 0 {
		names = []string{"all"}
	}
	known := map[string]struct{}{
		"mouser": {}, "digikey": {}, "ti": {},
		"microchip": {}, "ecb": {}, "openai": {},
	}
	selected := map[string]struct{}{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "all" {
			return known, nil
		}
		if _, exists := known[name]; !exists {
			return nil, errors.New("unknown provider " + name)
		}
		selected[name] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, errors.New("at least one provider is required")
	}
	return selected, nil
}

func providerErrorCode(err error) string {
	var mouserError *mouser.Error
	if errors.As(err, &mouserError) {
		return strings.ToUpper(mouserError.Kind)
	}
	var digiKeyError *digikey.Error
	if errors.As(err, &digiKeyError) {
		return strings.ToUpper(digiKeyError.Kind)
	}
	var tiError *ti.Error
	if errors.As(err, &tiError) {
		return strings.ToUpper(tiError.Kind)
	}
	var microchipError *microchip.Error
	if errors.As(err, &microchipError) {
		return strings.ToUpper(microchipError.Kind)
	}
	return "PROVIDER_ERROR"
}
