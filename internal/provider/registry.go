// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package provider

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/provider/digikey"
	"github.com/jihlenburg/bom-builder/internal/provider/microchip"
	"github.com/jihlenburg/bom-builder/internal/provider/mouser"
	"github.com/jihlenburg/bom-builder/internal/provider/ti"
	"github.com/jihlenburg/bom-builder/internal/sourcing"
)

// Runtime is one constructed pricing adapter with its lifecycle hooks.
type Runtime struct {
	Name         string
	Resolver     sourcing.Resolver
	RequestCount func() int
	// Close releases adapter resources; nil when nothing is held.
	Close func()
}

// Definition is the single in-code description of one provider.
//
// ADDING OR REMOVING A PROVIDER — the complete checklist:
//  1. The adapter package under internal/provider/<name>/ and its entry in
//     definitions() below (construction, capability, live check).
//  2. providerErrorCode in health.go (typed error kind mapping).
//  3. lookupcache.AdapterVersion and lookupcache.ProviderContextHash
//     (cache identity: version string and non-secret context values).
//  4. Restricted endpoint-override keys in internal/config/dotenv.go and
//     the .env.example entries.
//  5. Schema enums: providers/output/cache (and resolutions, where REMOVED
//     providers stay listed so durable stored data keeps decoding).
//  6. Help text, README, and the interactive/web provider placeholders.
type Definition struct {
	Name string
	// Kind is distributor, manufacturer, or service.
	Kind string
	// AutoSelect marks providers eligible for `--providers auto` when
	// configured. Evidence-only and service providers stay explicit.
	AutoSelect bool
	// NewRuntime constructs the live pricing adapter; nil when the
	// provider has no native pricing runtime (services).
	NewRuntime func() (*Runtime, error)
	// Capability reports configuration facts without network access.
	Capability func() contract.ProviderCapability
	// LiveCheck performs one bounded health probe; nil when none exists.
	LiveCheck func(context.Context, contract.ProviderCapability) contract.ProviderCapability
}

// definitions is the deterministic provider order used across discovery,
// capabilities, and health output.
func definitions() []Definition {
	return []Definition{
		{
			Name:       "mouser",
			Kind:       "distributor",
			AutoSelect: true,
			NewRuntime: func() (*Runtime, error) {
				client, err := mouser.NewFromEnvironment()
				if err != nil {
					return nil, err
				}
				resolver, err := mouser.NewResolver(client)
				if err != nil {
					return nil, &internalRuntimeError{cause: err}
				}
				return &Runtime{
					Name:         "mouser",
					Resolver:     resolver,
					RequestCount: client.RequestCount,
				}, nil
			},
			Capability: mouserCapability,
			LiveCheck:  checkMouser,
		},
		{
			Name:       "digikey",
			Kind:       "distributor",
			AutoSelect: true,
			NewRuntime: func() (*Runtime, error) {
				client, err := digikey.NewFromEnvironment()
				if err != nil {
					return nil, err
				}
				resolver, err := digikey.NewResolver(client)
				if err != nil {
					return nil, &internalRuntimeError{cause: err}
				}
				return &Runtime{
					Name:         "digikey",
					Resolver:     resolver,
					RequestCount: client.RequestCount,
				}, nil
			},
			Capability: digiKeyCapability,
			LiveCheck:  checkDigiKey,
		},
		{
			Name:       "ti",
			Kind:       "distributor",
			AutoSelect: true,
			NewRuntime: func() (*Runtime, error) {
				client, err := ti.NewFromEnvironment()
				if err != nil {
					return nil, err
				}
				resolver, err := ti.NewResolver(client)
				if err != nil {
					return nil, &internalRuntimeError{cause: err}
				}
				return &Runtime{
					Name:         "ti",
					Resolver:     resolver,
					RequestCount: client.RequestCount,
				}, nil
			},
			Capability: tiCapability,
			LiveCheck:  checkTI,
		},
		{
			Name: "microchip",
			Kind: "manufacturer",
			// Evidence-only (no pricing): explicit selection keeps
			// review-required factory data out of default runs.
			AutoSelect: false,
			NewRuntime: func() (*Runtime, error) {
				client, err := microchip.NewFromEnvironment()
				if err != nil {
					return nil, err
				}
				resolver, err := microchip.NewResolver(client)
				if err != nil {
					return nil, &internalRuntimeError{cause: err}
				}
				return &Runtime{
					Name:         "microchip",
					Resolver:     resolver,
					RequestCount: client.RequestCount,
				}, nil
			},
			Capability: microchipCapability,
			LiveCheck:  checkMicrochip,
		},
		{
			Name: "ecb",
			Kind: "service",
			Capability: func() contract.ProviderCapability {
				capability := serviceCapability("ecb", true, contract.ProviderDetails{
					Implementation:         "native_go",
					AuthenticationRequired: boolPointer(false),
				})
				// The FX layer is implemented (--currency conversion);
				// the discovery document must say so truthfully.
				capability.Implemented = true
				capability.Status = "ready"
				return capability
			},
		},
		{
			Name: "openai",
			Kind: "service",
			Capability: func() contract.ProviderCapability {
				return serviceCapability("openai", envSet("OPENAI_API_KEY"), contract.ProviderDetails{
					Implementation: "pending",
					Model:          "not_selected",
				})
			},
		},
	}
}

// internalRuntimeError marks a construction failure that is not a
// configuration problem, so the CLI can map it to an internal error.
type internalRuntimeError struct{ cause error }

func (runtimeError *internalRuntimeError) Error() string {
	return runtimeError.cause.Error()
}

// IsInternalRuntimeError reports whether a NewRuntime failure is an
// internal defect rather than missing or invalid configuration.
func IsInternalRuntimeError(err error) bool {
	var runtimeError *internalRuntimeError
	return errors.As(err, &runtimeError)
}

// ByName returns the definition for one normalized provider name.
func ByName(name string) (Definition, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, definition := range definitions() {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}

// PricingProviderNames returns every provider with a native pricing
// runtime, in deterministic registry order.
func PricingProviderNames() []string {
	names := []string{}
	for _, definition := range definitions() {
		if definition.NewRuntime != nil {
			names = append(names, definition.Name)
		}
	}
	return names
}

// DistributorNames returns the distributor providers in registry order.
func DistributorNames() []string {
	names := []string{}
	for _, definition := range definitions() {
		if definition.Kind == "distributor" {
			names = append(names, definition.Name)
		}
	}
	return names
}

// ManufacturerNames returns the manufacturer providers in registry order.
func ManufacturerNames() []string {
	names := []string{}
	for _, definition := range definitions() {
		if definition.Kind == "manufacturer" {
			names = append(names, definition.Name)
		}
	}
	return names
}

// AutoSelectableNames returns providers eligible for `--providers auto`.
func AutoSelectableNames() []string {
	names := []string{}
	for _, definition := range definitions() {
		if definition.AutoSelect && definition.NewRuntime != nil {
			names = append(names, definition.Name)
		}
	}
	return names
}

// AllNames returns every registered provider name, sorted, including
// services without pricing runtimes.
func AllNames() []string {
	names := []string{}
	for _, definition := range definitions() {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	return names
}
