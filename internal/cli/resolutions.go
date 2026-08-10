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
	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

func runResolutions(args []string, stdin io.Reader, stdout io.Writer) int {
	remaining, pretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return emitError(stdout, "resolutions", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, false)
	}
	if len(remaining) == 0 {
		return emitError(
			stdout,
			"resolutions",
			"INVALID_ARGUMENT",
			"expected: resolutions approve|list|history|revoke",
			contract.ExitInput,
			pretty,
		)
	}
	switch remaining[0] {
	case "approve":
		return runResolutionsApprove(remaining[1:], stdin, stdout, pretty)
	case "list":
		return runResolutionsList(remaining[1:], stdout, pretty)
	case "history":
		return runResolutionsHistory(remaining[1:], stdout, pretty)
	case "revoke":
		return runResolutionsRevoke(remaining[1:], stdout, pretty)
	default:
		return emitError(
			stdout,
			"resolutions",
			"INVALID_ARGUMENT",
			"expected: resolutions approve|list|history|revoke",
			contract.ExitInput,
			pretty,
		)
	}
}

func runResolutionsApprove(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	pretty bool,
) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "resolutions approve", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 1 {
		return emitError(
			stdout,
			"resolutions approve",
			"INVALID_ARGUMENT",
			"expected exactly one approval request source (file or -)",
			contract.ExitInput,
			pretty,
		)
	}
	if args[0] != "-" && strings.HasPrefix(args[0], "--") {
		return emitUnexpected(stdout, "resolutions approve", args, pretty)
	}
	request, err := resolutions.Load(args[0], stdin)
	if err != nil {
		return emitError(stdout, "resolutions approve", "INVALID_INPUT", err.Error(), contract.ExitInput, pretty)
	}
	// Approving creates the database when it does not exist yet: recording
	// the first human decision must not require a separate setup step.
	store, err := resolutions.Open(path)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions approve", err, pretty)
	}
	defer store.Close()
	record, superseded, err := store.Approve(context.Background(), request, time.Now().UTC())
	if err != nil {
		return emitResolutionsError(stdout, "resolutions approve", err, pretty)
	}
	status, err := store.StoreStatus(context.Background())
	if err != nil {
		return emitResolutionsError(stdout, "resolutions approve", err, pretty)
	}
	envelope := contract.ResolutionApproveEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "resolutions approve",
		Version:       app.Version,
		Resolutions:   mapResolutionsStatus(status),
		Resolution:    mapResolutionRecord(record),
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	if superseded != nil {
		mapped := mapResolutionRecord(*superseded)
		envelope.Superseded = &mapped
	}
	return emitJSON(stdout, envelope, pretty)
}

func runResolutionsList(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "resolutions list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, manufacturer, _, err := consumeValueFlag(args, "--manufacturer")
	if err != nil {
		return emitError(stdout, "resolutions list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, partNumber, _, err := consumeValueFlag(args, "--part")
	if err != nil {
		return emitError(stdout, "resolutions list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, limit, err := consumeResolutionsLimit(args)
	if err != nil {
		return emitError(stdout, "resolutions list", "INVALID_LIMIT", err.Error(), contract.ExitInput, pretty)
	}
	args, includeInactive, err := consumeFlag(args, "--include-inactive")
	if err != nil {
		return emitError(stdout, "resolutions list", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "resolutions list", args, pretty)
	}
	store, status, err := openExistingResolutions(path)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions list", err, pretty)
	}
	records := []contract.ResolutionRecord{}
	if store != nil {
		defer store.Close()
		listed, listErr := store.List(
			context.Background(),
			manufacturer,
			partNumber,
			limit,
			includeInactive,
		)
		if listErr != nil {
			return emitResolutionsError(stdout, "resolutions list", listErr, pretty)
		}
		for _, record := range listed {
			records = append(records, mapResolutionRecord(record))
		}
	}
	envelope := contract.ResolutionListEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "resolutions list",
		Version:       app.Version,
		Resolutions:   status,
		Records:       records,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func runResolutionsHistory(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "resolutions history", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, manufacturer, _, err := consumeValueFlag(args, "--manufacturer")
	if err != nil {
		return emitError(stdout, "resolutions history", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, partNumber, _, err := consumeValueFlag(args, "--part")
	if err != nil {
		return emitError(stdout, "resolutions history", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, limit, err := consumeResolutionsLimit(args)
	if err != nil {
		return emitError(stdout, "resolutions history", "INVALID_LIMIT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "resolutions history", args, pretty)
	}
	store, status, err := openExistingResolutions(path)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions history", err, pretty)
	}
	events := []contract.ResolutionEvent{}
	if store != nil {
		defer store.Close()
		history, historyErr := store.History(
			context.Background(),
			manufacturer,
			partNumber,
			limit,
		)
		if historyErr != nil {
			return emitResolutionsError(stdout, "resolutions history", historyErr, pretty)
		}
		for _, event := range history {
			events = append(events, contract.ResolutionEvent{
				EventID:      event.EventID,
				ResolutionID: event.ResolutionID,
				Action:       event.Action,
				Actor:        event.Actor,
				Manufacturer: event.Manufacturer,
				PartNumber:   event.PartNumber,
				Details:      event.Details,
				OccurredAt:   event.OccurredAt,
			})
		}
	}
	envelope := contract.ResolutionHistoryEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "resolutions history",
		Version:       app.Version,
		Resolutions:   status,
		Events:        events,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func runResolutionsRevoke(args []string, stdout io.Writer, pretty bool) int {
	args, path, pretty, err := consumeResolutionsCommandCommon(args, pretty)
	if err != nil {
		return emitError(stdout, "resolutions revoke", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, resolutionID, hasID, err := consumeValueFlag(args, "--id")
	if err != nil {
		return emitError(stdout, "resolutions revoke", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, revokedBy, hasRevokedBy, err := consumeValueFlag(args, "--revoked-by")
	if err != nil {
		return emitError(stdout, "resolutions revoke", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, reason, _, err := consumeValueFlag(args, "--reason")
	if err != nil {
		return emitError(stdout, "resolutions revoke", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	args, applyToken, _, err := consumeValueFlag(args, "--apply")
	if err != nil {
		return emitError(stdout, "resolutions revoke", "INVALID_ARGUMENT", err.Error(), contract.ExitInput, pretty)
	}
	if len(args) != 0 {
		return emitUnexpected(stdout, "resolutions revoke", args, pretty)
	}
	if !hasID || strings.TrimSpace(resolutionID) == "" {
		return emitError(
			stdout,
			"resolutions revoke",
			"RESOLUTION_ID_REQUIRED",
			"--id is required",
			contract.ExitInput,
			pretty,
		)
	}
	if !hasRevokedBy || strings.TrimSpace(revokedBy) == "" {
		return emitError(
			stdout,
			"resolutions revoke",
			"REVOKED_BY_REQUIRED",
			"--revoked-by is required: a revocation records a human decision",
			contract.ExitInput,
			pretty,
		)
	}
	store, status, err := openExistingResolutions(path)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions revoke", err, pretty)
	}
	if store == nil {
		return emitError(
			stdout,
			"resolutions revoke",
			"RESOLUTION_NOT_FOUND",
			"resolutions database does not exist",
			contract.ExitInput,
			pretty,
		)
	}
	defer store.Close()
	result, err := store.Revoke(
		context.Background(),
		resolutionID,
		revokedBy,
		reason,
		applyToken,
		time.Now().UTC(),
	)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions revoke", err, pretty)
	}
	status, err = resolutionsStoreStatus(store)
	if err != nil {
		return emitResolutionsError(stdout, "resolutions revoke", err, pretty)
	}
	revoke := contract.ResolutionRevokeResult{
		ResolutionID: result.ResolutionID,
		Matched:      result.Matched,
		ApplyToken:   result.ApplyToken,
		Applied:      result.Applied,
	}
	if result.Record != nil {
		mapped := mapResolutionRecord(*result.Record)
		revoke.Record = &mapped
	}
	envelope := contract.ResolutionRevokeEnvelope{
		SchemaVersion: contract.SchemaVersion,
		Status:        "complete",
		ExitCode:      contract.ExitOK,
		Command:       "resolutions revoke",
		Version:       app.Version,
		Resolutions:   status,
		Revoke:        revoke,
		Warnings:      []contract.Issue{},
		Errors:        []contract.Issue{},
	}
	return emitJSON(stdout, envelope, pretty)
}

func consumeResolutionsCommandCommon(
	args []string,
	pretty bool,
) ([]string, string, bool, error) {
	var err error
	args, duplicatePretty, err := consumeFlag(args, "--pretty")
	if err != nil {
		return nil, "", pretty, err
	}
	pretty = pretty || duplicatePretty
	args, configuredPath, hasPath, err := consumeValueFlag(args, "--resolutions-db")
	if err != nil {
		return nil, "", pretty, err
	}
	if !hasPath {
		configuredPath = strings.TrimSpace(os.Getenv("BOM_BUILDER_RESOLUTIONS_DB"))
	}
	if configuredPath == "" {
		configuredPath, err = resolutions.DefaultPath()
		if err != nil {
			return nil, "", pretty, err
		}
	}
	absolute, err := filepath.Abs(configuredPath)
	if err != nil {
		return nil, "", pretty, errors.New("resolutions database path is invalid")
	}
	return args, absolute, pretty, nil
}

func consumeResolutionsLimit(args []string) ([]string, int, error) {
	args, limitText, hasLimit, err := consumeValueFlag(args, "--limit")
	if err != nil {
		return nil, 0, err
	}
	limit := 100
	if hasLimit {
		limit, err = strconv.Atoi(limitText)
		if err != nil || limit < 1 || limit > 1000 {
			return nil, 0, errors.New("--limit must be an integer between 1 and 1000")
		}
	}
	return args, limit, nil
}

func openExistingResolutions(
	path string,
) (*resolutions.Store, contract.ResolutionsStoreStatus, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, contract.ResolutionsStoreStatus{
			Path:   path,
			Exists: false,
		}, nil
	}
	if err != nil {
		return nil, contract.ResolutionsStoreStatus{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, contract.ResolutionsStoreStatus{}, &resolutions.Error{
			Kind:    "permissions",
			Message: "resolutions database must be a regular non-symlink file",
		}
	}
	store, err := resolutions.Open(path)
	if err != nil {
		return nil, contract.ResolutionsStoreStatus{}, err
	}
	status, err := resolutionsStoreStatus(store)
	if err != nil {
		store.Close()
		return nil, contract.ResolutionsStoreStatus{}, err
	}
	return store, status, nil
}

func resolutionsStoreStatus(
	store *resolutions.Store,
) (contract.ResolutionsStoreStatus, error) {
	status, err := store.StoreStatus(context.Background())
	if err != nil {
		return contract.ResolutionsStoreStatus{}, err
	}
	return mapResolutionsStatus(status), nil
}

func mapResolutionsStatus(status resolutions.Status) contract.ResolutionsStoreStatus {
	return contract.ResolutionsStoreStatus{
		Path:            status.Path,
		Exists:          status.Exists,
		SchemaVersion:   status.SchemaVersion,
		ActiveCount:     status.ActiveCount,
		SupersededCount: status.SupersededCount,
		RevokedCount:    status.RevokedCount,
		EventCount:      status.EventCount,
	}
}

func mapResolutionRecord(record resolutions.Record) contract.ResolutionRecord {
	documents := make([]contract.ResolutionEvidenceDocument, 0, len(record.SourceDocuments))
	for _, document := range record.SourceDocuments {
		documents = append(documents, contract.ResolutionEvidenceDocument{
			URL:    document.URL,
			SHA256: document.SHA256,
		})
	}
	return contract.ResolutionRecord{
		ResolutionID: record.ResolutionID,
		Manufacturer: record.Manufacturer,
		PartNumber:   record.PartNumber,
		Replacement: contract.ResolutionReplacement{
			Manufacturer: record.Replacement.Manufacturer,
			PartNumber:   record.Replacement.PartNumber,
			Provider:     record.Replacement.Provider,
			ProviderSKU:  record.Replacement.ProviderSKU,
		},
		ApprovedBy:      record.ApprovedBy,
		Note:            record.Note,
		SourceDocuments: documents,
		Status:          record.Status,
		ApprovedAt:      record.ApprovedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

func emitResolutionsError(
	stdout io.Writer,
	command string,
	err error,
	pretty bool,
) int {
	var storeErr *resolutions.Error
	if errors.As(err, &storeErr) {
		switch storeErr.Kind {
		case "input", "configuration":
			return emitError(stdout, command, "INVALID_INPUT", storeErr.Message, contract.ExitInput, pretty)
		case "not_found":
			return emitError(stdout, command, "RESOLUTION_NOT_FOUND", storeErr.Message, contract.ExitInput, pretty)
		case "stale_preview":
			return emitError(stdout, command, "RESOLUTION_PREVIEW_STALE", storeErr.Message, contract.ExitInput, pretty)
		}
	}
	return emitError(stdout, command, "RESOLUTIONS_ERROR", err.Error(), contract.ExitInternal, pretty)
}
