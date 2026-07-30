// Package provider owns safe provider discovery and provider adapter boundaries.
package provider

import (
	"os"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/provider/nxp"
)

// Discover returns provider configuration facts without contacting a network.
func Discover() contract.ProviderDiscoveryEnvelope {
	providers := []contract.ProviderCapability{
		mouserCapability(),
		digiKeyCapability(),
		tiCapability(),
		nxpCapability(),
		serviceCapability("ecb", true, contract.ProviderDetails{
			Implementation:         "pending",
			AuthenticationRequired: boolPointer(false),
		}),
		serviceCapability("openai", envSet("OPENAI_API_KEY"), contract.ProviderDetails{
			Implementation: "pending",
			Model:          "not_selected",
		}),
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

func nxpCapability() contract.ProviderCapability {
	browser := strings.TrimSpace(os.Getenv("BOM_BUILDER_NXP_BROWSER"))
	if browser == "" {
		browser = nxp.FindSystemBrowser()
	}
	configured := false
	if info, err := os.Stat(browser); err == nil && !info.IsDir() {
		configured = true
	}
	capability := distributorCapability("nxp", configured, contract.ProviderDetails{
		Implementation:         "native_go_cdp",
		Currency:               strings.ToUpper(envDefault("NXP_STORE_CURRENCY", "USD")),
		AuthenticationRequired: boolPointer(false),
		SystemBrowser:          browser,
	})
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
