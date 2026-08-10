// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package cli implements BOM Builder's machine-first command protocol.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/bom"
	"github.com/jihlenburg/bom-builder/internal/config"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/design"
	"github.com/jihlenburg/bom-builder/internal/lookupcache"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
	"github.com/jihlenburg/bom-builder/internal/provider"
	"github.com/jihlenburg/bom-builder/internal/provider/digikey"
	"github.com/jihlenburg/bom-builder/internal/provider/microchip"
	"github.com/jihlenburg/bom-builder/internal/provider/mouser"
	"github.com/jihlenburg/bom-builder/internal/provider/nxp"
	"github.com/jihlenburg/bom-builder/internal/provider/ti"
	"github.com/jihlenburg/bom-builder/internal/sourcing"
	"github.com/jihlenburg/bom-builder/schemas"
)

// Run dispatches one CLI invocation and returns a stable process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(stdout, "bom-builder %s\n", app.Version)
		return contract.ExitOK
	}
	if topic, requested := helpRequest(args); requested {
		return runHelp(topic, stdout)
	}
	if len(args) == 0 {
		return emitError(stdout, "", "COMMAND_REQUIRED", "a command is required", contract.ExitInput, false)
	}
	command := args[0]
	// A broken checkout-local .env is user-authored input, not an internal
	// failure: report it under the invoked command with the input exit
	// code so agents can tell "fix your .env" from "the tool broke".
	if err := config.LoadDotEnv(".env"); err != nil {
		return emitError(stdout, command, "CONFIG_ERROR", err.Error(), contract.ExitInput, false)
	}
	switch command {
	case "capabilities":
		return runCapabilities(args[1:], stdout)
	case "providers":
		return runProviders(args[1:], stdout)
	case "schema":
		return runSchema(args[1:], stdout)
	case "validate":
		return runValidate(args[1:], stdin, stdout)
	case "lookup":
		return runLookup(args[1:], stdout)
	case "price":
		return runPrice(args[1:], stdin, stdout)
	case "alternatives":
		return runAlternatives(args[1:], stdin, stdout)
	case "documents":
		return runDocuments(args[1:], stdout)
	case "export":
		return runExport(args[1:], stdin, stdout)
	case "cache":
		return runCache(args[1:], stdout)
	case "resolutions":
		return runResolutions(args[1:], stdin, stdout)
	case "interactive":
		return runInteractive(args[1:], stdin, stdout)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	default:
		return emitError(
			stdout,
			command,
			"UNKNOWN_COMMAND",
			fmt.Sprintf("unknown command %q", command),
			contract.ExitInput,
			false,
		)
	}
}

func runCapabilities(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "capabilities", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	remaining, full, err := consumeFlag(remaining, "--full")
	if err != nil {
		return emitError(stdout, "capabilities", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(remaining) != 0 {
		return emitUnexpected(stdout, "capabilities", remaining, pretty)
	}

	envelope := contract.CapabilitiesEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "ok",
		ExitCode:      contract.ExitOK,
		Version:       app.Version,
		Runtime: contract.Runtime{
			Language:  "go",
			GoVersion: runtime.Version(),
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
		},
		Commands: []string{
			"alternatives",
			"cache list",
			"cache prune",
			"cache status",
			"cache verify",
			"capabilities",
			"documents fetch",
			"documents list",
			"export ec-bom",
			"help",
			"interactive",
			"lookup",
			"price",
			"providers list",
			"providers check",
			"resolutions approve",
			"resolutions history",
			"resolutions list",
			"resolutions revoke",
			"serve",
			"schema input",
			"schema alternatives",
			"schema cache",
			"schema output",
			"schema providers",
			"schema resolutions",
			"validate",
		},
		PlannedCommands:         []string{},
		Distributors:            []string{"mouser", "digikey", "ti", "nxp"},
		ImplementedDistributors: []string{"mouser", "digikey", "ti", "nxp"},
		Manufacturers:           []string{"microchip"},
		Services:                []string{"ecb", "openai"},
		ArtifactFormats:         []string{"json", "pdf", "ec-bom-csv"},
		Features: contract.Features{
			JSONStdout:                  true,
			StdinDesigns:                true,
			StrictInput:                 true,
			ProviderConfiguration:       true,
			LiveProviderHealth:          true,
			Pricing:                     true,
			Lookup:                      true,
			AlternativeParts:            true,
			DatasheetDownloads:          true,
			PersistentLookupCache:       true,
			NativeGoBinary:              true,
			NXPRequiresSystemBrowser:    true,
			TITransportImplementation:   true,
			ConcurrentProviderExecution: false,
		},
	}
	if full {
		discovery := provider.Discover()
		bundle, bundleErr := schemas.Bundle()
		if bundleErr != nil {
			return emitError(stdout, "capabilities", "SCHEMA_ERROR", bundleErr.Error(), contract.ExitInternal, pretty)
		}
		envelope.ProviderConfiguration = &discovery
		envelope.Schemas = &bundle
	}
	return emitJSON(stdout, envelope, pretty)
}

func runProviders(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "providers", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) == 1 && remaining[0] == "list" {
		return emitJSON(stdout, provider.Discover(), pretty)
	}
	if len(remaining) > 0 && remaining[0] == "check" {
		return runProviderCheck(remaining[1:], stdout, pretty)
	}
	return emitError(
		stdout,
		"providers",
		"INVALID_ARGUMENT",
		"expected: providers list|check",
		contract.ExitInput,
		pretty,
	)
}

func runProviderCheck(args []string, stdout io.Writer, pretty bool) int {
	var err error
	args, duplicatePretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "providers check", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	pretty = pretty || duplicatePretty
	args, live, err := consumeFlag(args, "--live")
	if err != nil {
		return emitError(stdout, "providers check", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, providersValue, foundProviders, err := consumeValueFlag(args, "--providers")
	if err != nil {
		return emitError(stdout, "providers check", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if !foundProviders {
		providersValue = "all"
	}
	args, deadlineValue, foundDeadline, err := consumeValueFlag(args, "--deadline")
	if err != nil {
		return emitError(stdout, "providers check", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "providers check", args, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, foundDeadline)
	if err != nil {
		return emitError(stdout, "providers check", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	envelope, err := provider.Check(ctx, commaList(providersValue), live)
	if err != nil {
		return emitError(
			stdout,
			"providers check",
			"INVALID_ARGUMENT",
			err.Error(),
			contract.ExitInput,
			pretty,
		)
	}
	return emitJSONWithExit(stdout, envelope, pretty, envelope.ExitCode)
}

func runSchema(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "schema", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) != 1 {
		return emitError(stdout, "schema", "INVALID_ARGUMENT", "expected one schema target", contract.ExitInput, pretty)
	}
	document, err := schemas.Get(remaining[0])
	if err != nil {
		return emitError(stdout, "schema", "UNKNOWN_SCHEMA", err.Error(), contract.ExitInput, pretty)
	}
	var payload any
	if err := json.Unmarshal(document, &payload); err != nil {
		return emitError(stdout, "schema", "SCHEMA_ERROR", err.Error(), contract.ExitInternal, pretty)
	}
	return emitJSON(stdout, payload, pretty)
}

func runValidate(args []string, stdin io.Reader, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "validate", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) == 0 {
		return emitError(stdout, "validate", "DESIGN_REQUIRED", "at least one design source is required", contract.ExitInput, pretty)
	}
	for _, source := range remaining {
		// Same guard as price: a flag-shaped leftover must surface as
		// a usage error, not be opened as a file named "--bogus".
		if strings.HasPrefix(source, "--") {
			return emitUnexpected(stdout, "validate", []string{source}, pretty)
		}
	}
	designs, err := design.LoadSources(remaining, stdin)
	if err != nil {
		return emitError(stdout, "validate", "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	names := make([]string, 0, len(designs))
	partCount := 0
	for _, loaded := range designs {
		names = append(names, loaded.Design)
		partCount += len(loaded.Parts)
	}
	envelope := contract.ValidationEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "valid",
		ExitCode:      contract.ExitOK,
		DesignCount:   len(designs),
		PartCount:     partCount,
		Designs:       names,
	}
	return emitJSON(stdout, envelope, pretty)
}

func runLookup(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	remaining, manufacturer, hasManufacturer, err := consumeValueFlag(remaining, "--manufacturer")
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, quantityText, hasQuantity, err := consumeValueFlag(remaining, "--quantity")
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, providersValue, hasProviders, err := consumeValueFlag(remaining, "--providers")
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, deadlineValue, hasDeadline, err := consumeValueFlag(remaining, "--deadline")
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, cacheConfig, err := consumeCacheFlags(remaining)
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_CACHE_CONFIGURATION", err.Error(), contract.ExitInput, pretty)
	}
	if len(remaining) != 1 {
		return emitError(
			stdout,
			"lookup",
			"INVALID_ARGUMENT",
			"expected exactly one part number",
			contract.ExitInput,
			pretty,
		)
	}
	if strings.HasPrefix(remaining[0], "--") {
		// A flag-shaped leftover is a mistyped or unknown option, never
		// a part number; querying providers for it would burn a
		// request and report a misleading not_found.
		return emitUnexpected(stdout, "lookup", []string{remaining[0]}, pretty)
	}
	if length := len(strings.TrimSpace(remaining[0])); length < 3 || length > 40 {
		return emitError(
			stdout,
			"lookup",
			"INVALID_PART_NUMBER",
			"part number must contain between 3 and 40 characters",
			contract.ExitInput,
			pretty,
		)
	}
	if !hasManufacturer || strings.TrimSpace(manufacturer) == "" {
		return emitError(
			stdout,
			"lookup",
			"MANUFACTURER_REQUIRED",
			"--manufacturer is required",
			contract.ExitInput,
			pretty,
		)
	}
	quantity := 1
	if hasQuantity {
		quantity, err = parsePositiveInt(quantityText, "quantity")
		if err != nil {
			return emitError(stdout, "lookup", "INVALID_QUANTITY", err.Error(), contract.ExitInput, pretty)
		}
	}
	selectedProviders, err := resolveProviderSelection(
		providersValue,
		hasProviders,
		cacheConfig.Policy,
	)
	if err != nil {
		return emitProviderSelectionError(stdout, "lookup", err, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, hasDeadline)
	if err != nil {
		return emitError(stdout, "lookup", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	demand := procurement.Demand{
		PartNumber:       strings.TrimSpace(remaining[0]),
		Manufacturer:     strings.TrimSpace(manufacturer),
		QuantityPerUnit:  quantity,
		RequiredQuantity: quantity,
	}
	return executePricing(
		"lookup",
		[]procurement.Demand{demand},
		nil,
		1,
		0,
		deadline,
		selectedProviders,
		cacheConfig,
		stdout,
		pretty,
	)
}

func runPrice(args []string, stdin io.Reader, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "price", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	remaining, unitsText, hasUnits, err := consumeValueFlag(remaining, "--units")
	if err != nil {
		return emitError(stdout, "price", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, attritionText, hasAttrition, err := consumeValueFlag(remaining, "--attrition")
	if err != nil {
		return emitError(stdout, "price", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, providersValue, hasProviders, err := consumeValueFlag(remaining, "--providers")
	if err != nil {
		return emitError(stdout, "price", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, deadlineValue, hasDeadline, err := consumeValueFlag(remaining, "--deadline")
	if err != nil {
		return emitError(stdout, "price", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	remaining, cacheConfig, err := consumeCacheFlags(remaining)
	if err != nil {
		return emitError(stdout, "price", "INVALID_CACHE_CONFIGURATION", err.Error(), contract.ExitInput, pretty)
	}
	if !hasUnits {
		return emitError(stdout, "price", "UNITS_REQUIRED", "--units is required", contract.ExitInput, pretty)
	}
	units, err := parsePositiveInt(unitsText, "units")
	if err != nil {
		return emitError(stdout, "price", "INVALID_UNITS", err.Error(), contract.ExitInput, pretty)
	}
	attrition := money.Decimal(0)
	if hasAttrition {
		attrition, err = money.Parse(attritionText)
		if err != nil || attrition.Micros() > money.Scale {
			return emitError(
				stdout,
				"price",
				"INVALID_ATTRITION",
				"attrition must be a finite decimal between 0 and 1",
				contract.ExitInput,
				pretty,
			)
		}
	}
	selectedProviders, err := resolveProviderSelection(
		providersValue,
		hasProviders,
		cacheConfig.Policy,
	)
	if err != nil {
		return emitProviderSelectionError(stdout, "price", err, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, hasDeadline)
	if err != nil {
		return emitError(stdout, "price", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	if len(remaining) == 0 {
		return emitError(
			stdout,
			"price",
			"DESIGN_REQUIRED",
			"at least one design source is required",
			contract.ExitInput,
			pretty,
		)
	}
	for _, source := range remaining {
		if strings.HasPrefix(source, "--") {
			return emitUnexpected(stdout, "price", []string{source}, pretty)
		}
	}
	designs, err := design.LoadSources(remaining, stdin)
	if err != nil {
		return emitError(stdout, "price", "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	demands, aggregationWarnings, err := bom.Aggregate(designs, units, attrition)
	if err != nil {
		return emitError(stdout, "price", "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	return executePricing(
		"price",
		demands,
		aggregationWarnings,
		units,
		attrition,
		deadline,
		selectedProviders,
		cacheConfig,
		stdout,
		pretty,
	)
}

type providerRuntime struct {
	name         string
	resolver     sourcing.Resolver
	requestCount func() int
	close        func()
}

type providerRuntimeSetupError struct {
	provider string
	kind     string
	cause    error
}

func (setupError *providerRuntimeSetupError) Error() string {
	return setupError.cause.Error()
}

func executePricing(
	command string,
	demands []procurement.Demand,
	aggregationWarnings []contract.Issue,
	units int,
	attrition money.Decimal,
	deadline time.Duration,
	providerNames []string,
	cacheConfig lookupcache.Config,
	stdout io.Writer,
	pretty bool,
) int {
	runtimes, cacheSession, err := newProviderRuntimes(providerNames, cacheConfig)
	if err != nil {
		return emitProviderRuntimeSetupError(stdout, command, err, pretty)
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
		return emitError(stdout, command, "INTERNAL_ERROR", err.Error(), contract.ExitInternal, pretty)
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	result := sourcing.Source(ctx, resolver, demands, units)
	providerRuns, totalRequests := providerRunMetadata(runtimes)
	envelope := contract.PricingEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        result.Status,
		ExitCode:      result.ExitCode,
		Command:       command,
		Version:       app.Version,
		Run: contract.RunMetadata{
			RunID:        newRunID(),
			StartedAt:    started,
			DurationMS:   time.Since(started).Milliseconds(),
			Providers:    providerRuns,
			RequestCount: totalRequests,
			Cache:        cacheRunMetadata(cacheSession),
		},
		Units:     units,
		Attrition: attrition,
		Summary:   result.Summary,
		Parts:     result.Parts,
		Warnings:  append(aggregationWarnings, result.Warnings...),
		Errors:    result.Errors,
	}
	return emitJSONWithExit(stdout, envelope, pretty, result.ExitCode)
}

func newProviderRuntimes(
	providerNames []string,
	cacheConfig lookupcache.Config,
) ([]providerRuntime, *lookupcache.Session, error) {
	cacheSession, err := lookupcache.NewSession(cacheConfig)
	if err != nil {
		return nil, nil, &providerRuntimeSetupError{
			provider: "cache", kind: "cache", cause: err,
		}
	}
	runtimes := make([]providerRuntime, 0, len(providerNames))
	for _, name := range providerNames {
		var runtime providerRuntime
		cacheOnly := cacheConfig.Policy == lookupcache.PolicyOnly ||
			cacheConfig.Policy == lookupcache.PolicyOffline
		if cacheOnly {
			switch name {
			case "mouser", "digikey", "ti", "nxp", "microchip":
				runtime = providerRuntime{
					name:         name,
					requestCount: func() int { return 0 },
				}
			default:
				closeProviderRuntimeResources(runtimes, cacheSession)
				return nil, nil, &providerRuntimeSetupError{
					provider: name,
					kind:     "unsupported",
					cause:    errors.New("provider " + name + " has no native pricing adapter"),
				}
			}
		} else {
			switch name {
			case "mouser":
				client, err := mouser.NewFromEnvironment()
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "configuration", cause: err,
					}
				}
				resolver, err := mouser.NewResolver(client)
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "internal", cause: err,
					}
				}
				runtime = providerRuntime{
					name:         name,
					resolver:     resolver,
					requestCount: client.RequestCount,
				}
			case "digikey":
				client, err := digikey.NewFromEnvironment()
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "configuration", cause: err,
					}
				}
				resolver, err := digikey.NewResolver(client)
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "internal", cause: err,
					}
				}
				runtime = providerRuntime{
					name:         name,
					resolver:     resolver,
					requestCount: client.RequestCount,
				}
			case "ti":
				client, err := ti.NewFromEnvironment()
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "configuration", cause: err,
					}
				}
				resolver, err := ti.NewResolver(client)
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "internal", cause: err,
					}
				}
				runtime = providerRuntime{
					name:         name,
					resolver:     resolver,
					requestCount: client.RequestCount,
				}
			case "microchip":
				client, err := microchip.NewFromEnvironment()
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "configuration", cause: err,
					}
				}
				resolver, err := microchip.NewResolver(client)
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "internal", cause: err,
					}
				}
				runtime = providerRuntime{
					name:         name,
					resolver:     resolver,
					requestCount: client.RequestCount,
				}
			case "nxp":
				client, err := nxp.NewFromEnvironment()
				if err != nil {
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "configuration", cause: err,
					}
				}
				resolver, err := nxp.NewResolver(client)
				if err != nil {
					client.Close()
					closeProviderRuntimeResources(runtimes, cacheSession)
					return nil, nil, &providerRuntimeSetupError{
						provider: name, kind: "internal", cause: err,
					}
				}
				runtime = providerRuntime{
					name:         name,
					resolver:     resolver,
					requestCount: client.RequestCount,
					close:        client.Close,
				}
			default:
				closeProviderRuntimeResources(runtimes, cacheSession)
				return nil, nil, &providerRuntimeSetupError{
					provider: name,
					kind:     "unsupported",
					cause:    errors.New("provider " + name + " has no native pricing adapter"),
				}
			}
		}
		cachedResolver, err := cacheSession.Resolver(
			name,
			runtime.resolver,
			runtime.requestCount,
		)
		if err != nil {
			if runtime.close != nil {
				runtime.close()
			}
			closeProviderRuntimeResources(runtimes, cacheSession)
			return nil, nil, &providerRuntimeSetupError{
				provider: name, kind: "cache", cause: err,
			}
		}
		runtime.resolver = cachedResolver
		runtimes = append(runtimes, runtime)
	}
	return runtimes, cacheSession, nil
}

func closeProviderRuntimes(runtimes []providerRuntime) {
	for index := len(runtimes) - 1; index >= 0; index-- {
		if runtimes[index].close != nil {
			runtimes[index].close()
		}
	}
}

func closeProviderRuntimeResources(
	runtimes []providerRuntime,
	cacheSession *lookupcache.Session,
) {
	closeProviderRuntimes(runtimes)
	if cacheSession != nil {
		_ = cacheSession.Close()
	}
}

func providerRunMetadata(
	runtimes []providerRuntime,
) ([]contract.ProviderRunMetadata, int) {
	providerRuns := make([]contract.ProviderRunMetadata, 0, len(runtimes))
	totalRequests := 0
	for _, runtime := range runtimes {
		requestCount := runtime.requestCount()
		totalRequests += requestCount
		providerRuns = append(providerRuns, contract.ProviderRunMetadata{
			Name:         runtime.name,
			RequestCount: requestCount,
		})
	}
	return providerRuns, totalRequests
}

func emitProviderRuntimeSetupError(
	stdout io.Writer,
	command string,
	err error,
	pretty bool,
) int {
	var setupError *providerRuntimeSetupError
	if !errors.As(err, &setupError) {
		return emitError(stdout, command, "INTERNAL_ERROR", err.Error(), contract.ExitInternal, pretty)
	}
	switch setupError.kind {
	case "configuration":
		return emitProviderConfigurationError(
			stdout,
			command,
			setupError.provider,
			setupError.cause,
			pretty,
		)
	case "unsupported":
		return emitError(
			stdout,
			command,
			"UNSUPPORTED_PROVIDER",
			setupError.cause.Error(),
			contract.ExitInput,
			pretty,
		)
	case "cache":
		return emitError(
			stdout,
			command,
			"CACHE_ERROR",
			setupError.cause.Error(),
			contract.ExitInternal,
			pretty,
		)
	default:
		return emitError(
			stdout,
			command,
			"INTERNAL_ERROR",
			setupError.cause.Error(),
			contract.ExitInternal,
			pretty,
		)
	}
}

func emitProviderConfigurationError(
	stdout io.Writer,
	command, providerName string,
	err error,
	pretty bool,
) int {
	return emitError(
		stdout,
		command,
		"PROVIDER_CONFIGURATION_ERROR",
		providerName+": "+err.Error(),
		contract.ExitProvider,
		pretty,
	)
}

func consumeFlag(args []string, flag string) ([]string, bool, error) {
	count := 0
	remaining := make([]string, 0, len(args))
	for _, argument := range args {
		if argument == flag {
			count++
			continue
		}
		remaining = append(remaining, argument)
	}
	if count > 1 {
		return nil, false, fmt.Errorf("%s may only be provided once", flag)
	}
	return remaining, count == 1, nil
}

func consumeValueFlag(
	args []string,
	flag string,
) ([]string, string, bool, error) {
	remaining := make([]string, 0, len(args))
	value := ""
	count := 0
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if strings.HasPrefix(argument, flag+"=") {
			count++
			value = strings.TrimPrefix(argument, flag+"=")
			continue
		}
		if argument != flag {
			remaining = append(remaining, argument)
			continue
		}
		count++
		if index+1 >= len(args) {
			return nil, "", false, fmt.Errorf("%s requires a value", flag)
		}
		index++
		value = args[index]
	}
	if count > 1 {
		return nil, "", false, fmt.Errorf("%s may only be provided once", flag)
	}
	return remaining, value, count == 1, nil
}

func emitUnexpected(stdout io.Writer, command string, args []string, pretty bool) int {
	sorted := slices.Clone(args)
	slices.Sort(sorted)
	return emitError(
		stdout,
		command,
		"INVALID_ARGUMENT",
		fmt.Sprintf("unexpected argument(s): %s", strings.Join(sorted, ", ")),
		contract.ExitInput,
		pretty,
	)
}

func emitError(
	stdout io.Writer,
	command, code, message string,
	exitCode int,
	pretty bool,
) int {
	envelope := contract.ErrorEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "failed",
		ExitCode:      exitCode,
		Command:       command,
		Errors: []contract.Issue{{
			Code:    code,
			Message: message,
		}},
	}
	emitJSON(stdout, envelope, pretty)
	return exitCode
}

func emitJSON(stdout io.Writer, payload any, pretty bool) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(payload); err != nil {
		return contract.ExitInternal
	}
	return contract.ExitOK
}

func emitJSONWithExit(stdout io.Writer, payload any, pretty bool, exitCode int) int {
	if emitted := emitJSON(stdout, payload, pretty); emitted != contract.ExitOK {
		return emitted
	}
	return exitCode
}

func parsePositiveInt(value, name string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func parseDeadline(value string, configured bool) (time.Duration, error) {
	if !configured {
		return 2 * time.Minute, nil
	}
	deadline, err := time.ParseDuration(value)
	if err != nil || deadline < time.Second || deadline > 30*time.Minute {
		return 0, fmt.Errorf("deadline must be a duration between 1s and 30m")
	}
	return deadline, nil
}

var errNoConfiguredProviders = errors.New("no configured native pricing provider")

func resolveProviderSelection(
	value string,
	configured bool,
	cachePolicy lookupcache.Policy,
) ([]string, error) {
	if !configured ||
		strings.EqualFold(strings.TrimSpace(value), "auto") ||
		strings.EqualFold(strings.TrimSpace(value), "all") {
		if cachePolicy == lookupcache.PolicyOnly ||
			cachePolicy == lookupcache.PolicyOffline {
			return []string{"mouser", "digikey", "ti", "nxp"}, nil
		}
		var selected []string
		for _, capability := range provider.Discover().Providers {
			if capability.Implemented && capability.Configured &&
				capability.Kind == "distributor" {
				selected = append(selected, capability.Name)
			}
		}
		if len(selected) == 0 {
			return nil, errNoConfiguredProviders
		}
		return selected, nil
	}
	providers := commaList(value)
	if len(providers) == 0 {
		return nil, fmt.Errorf("at least one provider is required")
	}
	for _, name := range providers {
		if name != "mouser" && name != "digikey" && name != "ti" &&
			name != "nxp" && name != "microchip" {
			return nil, fmt.Errorf("provider %s has no native pricing adapter", name)
		}
	}
	return providers, nil
}

func emitProviderSelectionError(
	stdout io.Writer,
	command string,
	err error,
	pretty bool,
) int {
	if errors.Is(err, errNoConfiguredProviders) {
		return emitError(
			stdout,
			command,
			"PROVIDER_CONFIGURATION_ERROR",
			"no configured native pricing provider; configure a distributor API or install Chrome/Edge for NXP",
			contract.ExitProvider,
			pretty,
		)
	}
	return emitError(
		stdout,
		command,
		"UNSUPPORTED_PROVIDER",
		err.Error(),
		contract.ExitInput,
		pretty,
	)
}

func commaList(value string) []string {
	var result []string
	seen := map[string]struct{}{}
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func newRunID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 16)
}
