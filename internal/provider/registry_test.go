// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package provider

import (
	"fmt"
	"testing"
)

func TestRegistryDefinitionsAreCompleteAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, definition := range definitions() {
		if definition.Name == "" || definition.Capability == nil {
			t.Fatalf("definition missing name or capability: %+v", definition)
		}
		if seen[definition.Name] {
			t.Fatalf("duplicate provider %s", definition.Name)
		}
		seen[definition.Name] = true
		switch definition.Kind {
		case "distributor", "manufacturer", "service":
		default:
			t.Fatalf("provider %s has unknown kind %q", definition.Name, definition.Kind)
		}
		capability := definition.Capability()
		if capability.Name != definition.Name || capability.Kind != definition.Kind {
			t.Fatalf(
				"capability identity mismatch for %s: %+v",
				definition.Name, capability,
			)
		}
	}
}

func TestRegistryGroupings(t *testing.T) {
	if fmt.Sprint(DistributorNames()) != "[mouser digikey ti farnell]" {
		t.Fatalf("distributors = %v", DistributorNames())
	}
	if fmt.Sprint(ManufacturerNames()) != "[microchip]" {
		t.Fatalf("manufacturers = %v", ManufacturerNames())
	}
	if fmt.Sprint(AutoSelectableNames()) != "[mouser digikey ti farnell]" {
		t.Fatalf("auto-selectable = %v", AutoSelectableNames())
	}
	if fmt.Sprint(PricingProviderNames()) != "[mouser digikey ti farnell microchip]" {
		t.Fatalf("pricing providers = %v", PricingProviderNames())
	}
	if fmt.Sprint(AllNames()) != "[digikey ecb farnell microchip mouser openai ti]" {
		t.Fatalf("all names = %v", AllNames())
	}
}

func TestRegistryLookupNormalizesNames(t *testing.T) {
	definition, exists := ByName("  MOUSER ")
	if !exists || definition.Name != "mouser" {
		t.Fatalf("normalized lookup failed: %+v %v", definition, exists)
	}
	if _, exists := ByName("nxp"); exists {
		t.Fatal("removed providers must not resolve")
	}
	if _, exists := ByName(""); exists {
		t.Fatal("the empty name must not resolve")
	}
}

func TestRegistryServiceDefinitionsHaveNoRuntime(t *testing.T) {
	for _, definition := range definitions() {
		if definition.Kind == "service" && definition.NewRuntime != nil {
			t.Fatalf("service %s must not offer a pricing runtime", definition.Name)
		}
		if definition.Kind == "service" && definition.AutoSelect {
			t.Fatalf("service %s must not be auto-selectable", definition.Name)
		}
	}
}

func TestEvidenceOnlyProvidersAreNeverAutoSelected(t *testing.T) {
	definition, exists := ByName("microchip")
	if !exists {
		t.Fatal("microchip must be registered")
	}
	if definition.AutoSelect {
		t.Fatal("evidence-only providers must require explicit selection")
	}
	if definition.NewRuntime == nil {
		t.Fatal("microchip must offer a pricing-pipeline runtime")
	}
}
