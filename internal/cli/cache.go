// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jihlenburg/bom-builder/internal/app"
	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/lookupcache"
)

func runCache(args []string, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "cache", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) == 0 {
		return emitError(
			stdout,
			"cache",
			"INVALID_ARGUMENT",
			"expected: cache status|list|verify|prune",
			contract.ExitInput,
			pretty,
		)
	}
	switch remaining[0] {
	case "status":
		return runCacheStatus(remaining[1:], stdout, pretty)
	case "list":
		return runCacheList(remaining[1:], stdout, pretty)
	case "verify":
		return runCacheVerify(remaining[1:], stdout, pretty)
	case "prune":
		return runCachePrune(remaining[1:], stdout, pretty)
	default:
		return emitError(
			stdout,
			"cache",
			"INVALID_ARGUMENT",
			"expected: cache status|list|verify|prune",
			contract.ExitInput,
			pretty,
		)
	}
}

func runCacheStatus(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeCacheCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "cache status", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "cache status", args, pretty)
	}
	store, status, err := openExistingCache(path)
	if err != nil {
		return emitCacheCommandError(stdout, "cache status", err, pretty)
	}
	if store != nil {
		defer store.Close()
	}
	envelope := contract.CacheStatusEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "cache status",
		Version:       app.Version,
		Cache:         status,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func runCacheList(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeCacheCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "cache list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, providerName, hasProvider, err := consumeValueFlag(args, "--provider")
	if err != nil {
		return emitError(stdout, "cache list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if hasProvider {
		providerName = strings.ToLower(strings.TrimSpace(providerName))
		if !isNativeProvider(providerName) {
			return emitError(
				stdout,
				"cache list",
				"UNSUPPORTED_PROVIDER",
				"provider "+providerName+" has no native pricing adapter",
				contract.ExitInput,
				pretty,
			)
		}
	}
	args, limitText, hasLimit, err := consumeValueFlag(args, "--limit")
	if err != nil {
		return emitError(stdout, "cache list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	limit := 100
	if hasLimit {
		limit, err = strconv.Atoi(limitText)
		if err != nil || limit < 1 || limit > 1000 {
			return emitError(
				stdout,
				"cache list",
				"INVALID_LIMIT",
				"--limit must be an integer between 1 and 1000",
				contract.ExitInput,
				pretty,
			)
		}
	}
	args, includeStale, err := consumeFlag(args, "--include-stale")
	if err != nil {
		return emitError(stdout, "cache list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "cache list", args, pretty)
	}
	store, status, err := openExistingCache(path)
	if err != nil {
		return emitCacheCommandError(stdout, "cache list", err, pretty)
	}
	entries := []contract.CacheEntryMetadata{}
	if store != nil {
		defer store.Close()
		cachedEntries, listErr := store.List(
			context.Background(),
			providerName,
			limit,
			includeStale,
			time.Now().UTC(),
		)
		if listErr != nil {
			return emitCacheCommandError(stdout, "cache list", listErr, pretty)
		}
		for _, entry := range cachedEntries {
			entries = append(entries, mapCacheEntry(entry))
		}
	}
	envelope := contract.CacheListEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "cache list",
		Version:       app.Version,
		Cache:         status,
		Entries:       entries,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func runCacheVerify(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeCacheCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "cache verify", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "cache verify", args, pretty)
	}
	store, status, err := openExistingCache(path)
	if err != nil {
		return emitCacheCommandError(stdout, "cache verify", err, pretty)
	}
	report := contract.CacheVerifyReport{
		OK:             true,
		IntegrityCheck: "not_created",
		Issues:         []string{},
	}
	if store != nil {
		defer store.Close()
		verified, verifyErr := store.Verify(context.Background(), time.Now().UTC())
		if verifyErr != nil {
			return emitCacheCommandError(stdout, "cache verify", verifyErr, pretty)
		}
		report = mapCacheVerification(verified)
	}
	statusText := "complete"
	exitCode := contract.ExitOK
	errorsOut := []contract.Issue{}
	if !report.OK {
		statusText = "failed"
		exitCode = contract.ExitInternal
		errorsOut = append(errorsOut, contract.Issue{
			Code:    "CACHE_CORRUPT",
			Message: "cache integrity verification found invalid entries",
		})
	}
	envelope := contract.CacheVerifyEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        statusText,
		ExitCode:      exitCode,
		Command:       "cache verify",
		Version:       app.Version,
		Cache:         status,
		Verification:  report,
		Warnings:      []contract.Issue{},
		Errors:        errorsOut,
	}
	return emitJSONWithExit(stdout, envelope, pretty, exitCode)
}

func runCachePrune(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeCacheCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "cache prune", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, all, err := consumeFlag(args, "--all")
	if err != nil {
		return emitError(stdout, "cache prune", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, applyToken, hasApply, err := consumeValueFlag(args, "--apply")
	if err != nil {
		return emitError(stdout, "cache prune", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "cache prune", args, pretty)
	}
	store, status, err := openExistingCache(path)
	if err != nil {
		return emitCacheCommandError(stdout, "cache prune", err, pretty)
	}
	scope := "expired"
	if all {
		scope = "all"
	}
	result := contract.CachePruneResult{Scope: scope}
	if store == nil {
		if hasApply {
			return emitError(
				stdout,
				"cache prune",
				"CACHE_PREVIEW_STALE",
				"cache database does not exist; request a new preview",
				contract.ExitInput,
				pretty,
			)
		}
	} else {
		defer store.Close()
		pruned, pruneErr := store.Prune(
			context.Background(),
			all,
			applyToken,
			time.Now().UTC(),
		)
		if pruneErr != nil {
			var cacheError *lookupcache.Error
			if errors.As(pruneErr, &cacheError) && cacheError.Kind == "stale_preview" {
				return emitError(
					stdout,
					"cache prune",
					"CACHE_PREVIEW_STALE",
					cacheError.Message,
					contract.ExitInput,
					pretty,
				)
			}
			return emitCacheCommandError(stdout, "cache prune", pruneErr, pretty)
		}
		result = mapCachePrune(pruned)
		status, err = cacheStoreStatus(store)
		if err != nil {
			return emitCacheCommandError(stdout, "cache prune", err, pretty)
		}
	}
	envelope := contract.CachePruneEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "cache prune",
		Version:       app.Version,
		Cache:         status,
		Prune:         result,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func consumeCacheCommandCommon(
	args []string,
	pretty bool,
) ([]string, string, bool, error) {
	var err error
	args, duplicatePretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return nil, "", pretty, err
	}
	pretty = pretty || duplicatePretty
	args, configuredPath, hasPath, err := consumeValueFlag(args, "--cache-db")
	if err != nil {
		return nil, "", pretty, err
	}
	if !hasPath {
		configuredPath = strings.TrimSpace(os.Getenv("BOM_BUILDER_CACHE_DB"))
	}
	if configuredPath == "" {
		configuredPath, err = lookupcache.DefaultPath()
		if err != nil {
			return nil, "", pretty, err
		}
	}
	absolute, err := filepath.Abs(configuredPath)
	if err != nil {
		return nil, "", pretty, errors.New("cache database path is invalid")
	}
	return args, absolute, pretty, nil
}

func openExistingCache(
	path string,
) (*lookupcache.Store, contract.CacheStoreStatus, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, contract.CacheStoreStatus{
			Path:          path,
			Exists:        false,
			SchemaVersion: 0,
		}, nil
	}
	if err != nil {
		return nil, contract.CacheStoreStatus{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, contract.CacheStoreStatus{}, &lookupcache.Error{
			Kind:    "permissions",
			Message: "cache database must be a regular non-symlink file",
		}
	}
	store, err := lookupcache.Open(path)
	if err != nil {
		return nil, contract.CacheStoreStatus{}, err
	}
	status, err := cacheStoreStatus(store)
	if err != nil {
		store.Close()
		return nil, contract.CacheStoreStatus{}, err
	}
	return store, status, nil
}

func cacheStoreStatus(store *lookupcache.Store) (contract.CacheStoreStatus, error) {
	status, err := store.CacheStatus(context.Background(), time.Now().UTC())
	if err != nil {
		return contract.CacheStoreStatus{}, err
	}
	return contract.CacheStoreStatus{
		Path:          status.Path,
		Exists:        status.Exists,
		SchemaVersion: status.SchemaVersion,
		EntryCount:    status.EntryCount,
		FreshCount:    status.FreshCount,
		StaleCount:    status.StaleCount,
		SizeBytes:     status.SizeBytes,
		OldestEntry:   status.OldestEntry,
		NewestEntry:   status.NewestEntry,
	}, nil
}

func mapCacheEntry(entry lookupcache.EntryMetadata) contract.CacheEntryMetadata {
	return contract.CacheEntryMetadata{
		Provider:         entry.Provider,
		Key:              entry.Key,
		PartNumber:       entry.PartNumber,
		Manufacturer:     entry.Manufacturer,
		RequiredQuantity: entry.RequiredQuantity,
		ResultStatus:     entry.ResultStatus,
		AdapterVersion:   entry.AdapterVersion,
		FetchedAt:        entry.FetchedAt,
		ExpiresAt:        entry.ExpiresAt,
		SourceRequests:   entry.SourceRequests,
		Stale:            entry.Stale,
	}
}

func mapCacheVerification(report lookupcache.VerifyReport) contract.CacheVerifyReport {
	return contract.CacheVerifyReport{
		OK:             report.OK,
		IntegrityCheck: report.IntegrityCheck,
		CheckedEntries: report.CheckedEntries,
		InvalidEntries: report.InvalidEntries,
		Issues:         report.Issues,
	}
}

func mapCachePrune(result lookupcache.PruneResult) contract.CachePruneResult {
	return contract.CachePruneResult{
		Scope:        result.Scope,
		MatchedCount: result.MatchedCount,
		ApplyToken:   result.ApplyToken,
		Applied:      result.Applied,
		DeletedCount: result.DeletedCount,
	}
}

func emitCacheCommandError(
	stdout io.Writer,
	command string,
	err error,
	pretty bool,
) int {
	return emitError(
		stdout,
		command,
		"CACHE_ERROR",
		err.Error(),
		contract.ExitInternal,
		pretty,
	)
}

func isNativeProvider(name string) bool {
	switch name {
	case "mouser", "digikey", "ti", "nxp":
		return true
	default:
		return false
	}
}
