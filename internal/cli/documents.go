// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/documents"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func runDocuments(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "documents", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) == 0 {
		return emitError(
			stdout,
			"documents",
			"INVALID_ARGUMENT",
			"expected: documents list|fetch",
			contract.ExitInput,
			pretty,
		)
	}
	switch remaining[0] {
	case "list":
		return runDocumentsList(remaining[1:], stdout, pretty)
	case "fetch":
		return runDocumentsFetch(remaining[1:], stdout, pretty)
	default:
		return emitError(
			stdout,
			"documents",
			"INVALID_ARGUMENT",
			"expected: documents list|fetch",
			contract.ExitInput,
			pretty,
		)
	}
}

func runDocumentsList(
	args []string,
	stdout io.Writer,
	pretty bool,
) int {
	var err error
	args, duplicatePretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	pretty = pretty || duplicatePretty
	args, manufacturer, hasManufacturer, err := consumeValueFlag(args, "--manufacturer")
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, quantityText, hasQuantity, err := consumeValueFlag(args, "--quantity")
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, providersValue, hasProviders, err := consumeValueFlag(args, "--providers")
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, deadlineValue, hasDeadline, err := consumeValueFlag(args, "--deadline")
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, cacheConfig, err := consumeCacheFlags(args)
	if err != nil {
		return emitError(
			stdout,
			"documents list",
			"INVALID_CACHE_CONFIGURATION",
			err.Error(),
			contract.ExitInput,
			pretty,
		)
	}
	if len(args) != 1 {
		return emitError(
			stdout,
			"documents list",
			"INVALID_ARGUMENT",
			"expected exactly one part number",
			contract.ExitInput,
			pretty,
		)
	}
	if strings.HasPrefix(args[0], "--") {
		// A flag-shaped leftover is a mistyped or unknown option, never
		// a part number.
		return emitUnexpected(stdout, "documents list", []string{args[0]}, pretty)
	}
	partNumber := strings.TrimSpace(args[0])
	if len(partNumber) < 3 || len(partNumber) > 40 {
		return emitError(
			stdout,
			"documents list",
			"INVALID_PART_NUMBER",
			"part number must contain between 3 and 40 characters",
			contract.ExitInput,
			pretty,
		)
	}
	if !hasManufacturer || strings.TrimSpace(manufacturer) == "" {
		return emitError(
			stdout,
			"documents list",
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
			return emitError(stdout, "documents list", "INVALID_QUANTITY", err.Error(), contract.ExitInput, pretty)
		}
	}
	providerNames, err := resolveProviderSelection(
		providersValue,
		hasProviders,
		cacheConfig.Policy,
	)
	if err != nil {
		return emitProviderSelectionError(stdout, "documents list", err, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, hasDeadline)
	if err != nil {
		return emitError(stdout, "documents list", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	runtimes, cacheSession, err := newProviderRuntimes(providerNames, cacheConfig)
	if err != nil {
		return emitProviderRuntimeSetupError(stdout, "documents list", err, pretty)
	}
	defer closeProviderRuntimeResources(runtimes, cacheSession)

	demand := procurement.Demand{
		PartNumber:       partNumber,
		Manufacturer:     strings.TrimSpace(manufacturer),
		QuantityPerUnit:  quantity,
		RequiredQuantity: quantity,
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var (
		links          []contract.DocumentLink
		providerIssues []contract.Issue
		failureCount   int
	)
	for _, runtime := range runtimes {
		result, lookupErr := runtime.resolver.Lookup(ctx, demand)
		if lookupErr != nil {
			failureCount++
			providerIssues = append(providerIssues, contract.Issue{
				Code:    "DOCUMENT_PROVIDER_ERROR",
				Message: runtime.name + ": " + lookupErr.Error(),
				Details: map[string]any{"provider": runtime.name},
			})
			continue
		}
		if result.Offer != nil {
			links = append(links, documents.LinksFromOffer(*result.Offer)...)
		}
	}
	links = documents.NormalizeLinks(links)
	providerRuns, totalRequests := providerRunMetadata(runtimes)
	status := "complete"
	exitCode := contract.ExitOK
	warnings := providerIssues
	var outputErrors []contract.Issue
	if failureCount == len(runtimes) {
		status = "failed"
		exitCode = contract.ExitProvider
		outputErrors = providerIssues
		warnings = []contract.Issue{}
	} else if len(links) == 0 {
		status = "incomplete"
		exitCode = contract.ExitIncomplete
		warnings = append(warnings, contract.Issue{
			Code:    "DOCUMENT_NOT_FOUND",
			Message: "selected providers returned no document links",
			Details: map[string]any{"part_number": partNumber},
		})
	}
	envelope := contract.DocumentListEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        status,
		ExitCode:      exitCode,
		Command:       "documents list",
		Version:       app.Version,
		Run: contract.RunMetadata{
			RunID:        newRunID(),
			StartedAt:    started,
			DurationMS:   time.Since(started).Milliseconds(),
			Providers:    providerRuns,
			RequestCount: totalRequests,
			Cache:        cacheRunMetadata(cacheSession),
		},
		Query:     demand,
		Documents: links,
		Warnings:  warnings,
		Errors:    outputErrors,
	}
	if envelope.Documents == nil {
		envelope.Documents = []contract.DocumentLink{}
	}
	if envelope.Warnings == nil {
		envelope.Warnings = []contract.Issue{}
	}
	if envelope.Errors == nil {
		envelope.Errors = []contract.Issue{}
	}
	return emitJSONWithExit(stdout, envelope, pretty, exitCode)
}

func runDocumentsFetch(
	args []string,
	stdout io.Writer,
	pretty bool,
) int {
	var err error
	args, duplicatePretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "documents fetch", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	pretty = pretty || duplicatePretty
	args, outputPath, hasOutput, err := consumeValueFlag(args, "--output")
	if err != nil {
		return emitError(stdout, "documents fetch", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, maxBytesText, hasMaxBytes, err := consumeValueFlag(args, "--max-bytes")
	if err != nil {
		return emitError(stdout, "documents fetch", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, deadlineValue, hasDeadline, err := consumeValueFlag(args, "--deadline")
	if err != nil {
		return emitError(stdout, "documents fetch", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 1 {
		return emitError(
			stdout,
			"documents fetch",
			"INVALID_ARGUMENT",
			"expected exactly one document URL",
			contract.ExitInput,
			pretty,
		)
	}
	if !hasOutput || strings.TrimSpace(outputPath) == "" {
		return emitError(
			stdout,
			"documents fetch",
			"OUTPUT_REQUIRED",
			"--output is required",
			contract.ExitInput,
			pretty,
		)
	}
	maxBytes := documents.DefaultMaxBytes
	if hasMaxBytes {
		maxBytes, err = strconv.ParseInt(maxBytesText, 10, 64)
		if err != nil {
			return emitError(
				stdout,
				"documents fetch",
				"INVALID_MAX_BYTES",
				"--max-bytes must be a positive integer",
				contract.ExitInput,
				pretty,
			)
		}
	}
	fetcher, err := documents.NewFetcher(maxBytes)
	if err != nil {
		return emitDocumentFetchError(stdout, err, pretty)
	}
	deadline, err := parseDeadline(deadlineValue, hasDeadline)
	if err != nil {
		return emitError(stdout, "documents fetch", "INVALID_DEADLINE", err.Error(), contract.ExitInput, pretty)
	}
	started := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	artifact, err := fetcher.Fetch(ctx, args[0], outputPath)
	if err != nil {
		return emitDocumentFetchError(stdout, err, pretty)
	}
	envelope := contract.DocumentFetchEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "documents fetch",
		Version:       app.Version,
		Run: contract.RunMetadata{
			RunID:        newRunID(),
			StartedAt:    started,
			DurationMS:   time.Since(started).Milliseconds(),
			Providers:    []contract.ProviderRunMetadata{},
			RequestCount: 1,
		},
		Artifact: *artifact,
		Warnings: []contract.Issue{},
		Errors:   []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func emitDocumentFetchError(
	stdout io.Writer,
	err error,
	pretty bool,
) int {
	var fetchError *documents.FetchError
	if !errors.As(err, &fetchError) {
		return emitError(
			stdout,
			"documents fetch",
			"INTERNAL_ERROR",
			"unexpected document failure",
			contract.ExitInternal,
			pretty,
		)
	}
	code := "DOCUMENT_ERROR"
	exitCode := contract.ExitProvider
	switch fetchError.Kind {
	case "input":
		code = "INVALID_DOCUMENT_INPUT"
		exitCode = contract.ExitInput
	case "exists":
		code = "OUTPUT_EXISTS"
		exitCode = contract.ExitInput
	case "timeout":
		code = "DOCUMENT_TIMEOUT"
	case "network":
		code = "DOCUMENT_NETWORK_ERROR"
	case "response":
		code = "DOCUMENT_HTTP_ERROR"
	case "validation":
		code = "DOCUMENT_NOT_PDF"
	case "limit":
		code = "DOCUMENT_TOO_LARGE"
	case "filesystem":
		code = "FILESYSTEM_ERROR"
		exitCode = contract.ExitInternal
	}
	return emitError(
		stdout,
		"documents fetch",
		code,
		fetchError.Message,
		exitCode,
		pretty,
	)
}
