// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package provider owns safe provider discovery and provider adapter boundaries.
package provider

import (
	"os"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/provider/farnell"
)

// Discover returns provider configuration facts without contacting a network.
func Discover() contract.ProviderDiscoveryEnvelope {
	providers := []contract.ProviderCapability{}
	for _, definition := range definitions() {
		providers = append(providers, definition.Capability())
	}
	return contract.ProviderDiscoveryEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "ready",
		ExitCode:      contract.ExitOK,
		Live:          false,
		Providers:     providers,
	}
}

func mouserCapability() contract.ProviderCapability {
	count := len(uniqueTokens(os.Getenv("MOUSER_API_KEYS")))
	if count == 0 && envSet("MOUSER_API_KEY") {
		count = 1
	}
	capability := distributorCapability("mouser", count > 0, contract.ProviderDetails{
		Implementation:  "pending",
		CredentialCount: intPointer(count),
	})
	capability.Implemented = true
	capability.Details.Implementation = "native_go"
	if capability.Configured {
		capability.Status = "ready"
	} else {
		capability.Status = "unconfigured"
	}
	return capability
}

func digiKeyCapability() contract.ProviderCapability {
	accountConfigured := envSet("DIGIKEY_ACCOUNT_ID")
	capability := distributorCapability(
		"digikey",
		envSet("DIGIKEY_CLIENT_ID") &&
			envSet("DIGIKEY_CLIENT_SECRET") &&
			accountConfigured,
		contract.ProviderDetails{
			Implementation:      "native_go",
			AccountIDConfigured: &accountConfigured,
			Locale: &contract.Locale{
				Site:          envDefault("DIGIKEY_LOCALE_SITE", "DE"),
				Language:      envDefault("DIGIKEY_LOCALE_LANGUAGE", "en"),
				Currency:      envDefault("DIGIKEY_LOCALE_CURRENCY", "EUR"),
				ShipToCountry: envDefault("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", "de"),
			},
		},
	)
	capability.Implemented = true
	if capability.Configured {
		capability.Status = "ready"
	} else {
		capability.Status = "unconfigured"
	}
	return capability
}

func tiCapability() contract.ProviderCapability {
	configured := envSet("TI_STORE_API_KEY") && envSet("TI_STORE_API_SECRET")
	capability := distributorCapability("ti", configured, contract.ProviderDetails{
		Implementation: "native_go",
		Currency:       strings.ToUpper(envDefault("TI_STORE_PRICE_CURRENCY", "USD")),
	})
	capability.Implemented = true
	if capability.Configured {
		capability.Status = "ready"
	} else {
		capability.Status = "unconfigured"
	}
	return capability
}

func microchipCapability() contract.ProviderCapability {
	// The public Product API needs no credentials, so the provider is
	// always configured. It supplies availability and lifecycle
	// EVIDENCE only (no pricing) and is selected explicitly, never by
	// `--providers auto`.
	capability := contract.ProviderCapability{
		Name:        "microchip",
		Kind:        "manufacturer",
		Implemented: true,
		Configured:  true,
		Status:      "ready",
		Details: contract.ProviderDetails{
			Implementation:         "native_go",
			AuthenticationRequired: boolPointer(false),
		},
	}
	return capability
}

func farnellCapability() contract.ProviderCapability {
	// The element14 API returns prices without a currency field, so the
	// regional store implies it. A store the adapter cannot price in a
	// known currency is not configured until FARNELL_PRICE_CURRENCY says
	// which one applies.
	storeID := strings.ToLower(envDefault("FARNELL_STORE_ID", farnell.DefaultStoreID))
	currency := strings.ToUpper(strings.TrimSpace(os.Getenv("FARNELL_PRICE_CURRENCY")))
	if currency == "" {
		currency = farnell.StoreCurrency(storeID)
	}
	capability := distributorCapability(
		"farnell",
		envSet("FARNELL_API_KEY") && currency != "",
		contract.ProviderDetails{
			Implementation: "native_go",
			Currency:       currency,
		},
	)
	capability.Implemented = true
	if capability.Configured {
		capability.Status = "ready"
	} else {
		capability.Status = "unconfigured"
	}
	return capability
}

func distributorCapability(
	name string,
	configured bool,
	details contract.ProviderDetails,
) contract.ProviderCapability {
	return contract.ProviderCapability{
		Name:         name,
		Kind:         "distributor",
		Implemented:  false,
		Configured:   configured,
		Status:       "pending",
		Live:         false,
		RequestCount: 0,
		Details:      details,
	}
}

func serviceCapability(
	name string,
	configured bool,
	details contract.ProviderDetails,
) contract.ProviderCapability {
	return contract.ProviderCapability{
		Name:        name,
		Kind:        "service",
		Implemented: false,
		Configured:  configured,
		Status:      "pending",
		Details:     details,
	}
}

func envSet(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

func envDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func uniqueTokens(raw string) []string {
	seen := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(raw, func(character rune) bool {
		return character == ',' || character == ';' || character == '\n'
	}) {
		trimmed := strings.TrimSpace(token)
		if trimmed != "" {
			seen[trimmed] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func intPointer(value int) *int {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}
