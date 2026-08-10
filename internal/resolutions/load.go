// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package resolutions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	maxRequestBytes      = 1024 * 1024
	maxEvidenceDocuments = 10
	maxPartNumberLength  = 40
	minPartNumberLength  = 3
	maxNameLength        = 80
	maxApproverLength    = 120
	maxNoteLength        = 500
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Load reads and strictly validates one approval request from a file/stdin.
func Load(source string, stdin io.Reader) (Request, error) {
	var (
		reader io.Reader
		close  func() error
	)
	if source == "-" {
		reader = stdin
		close = func() error { return nil }
	} else {
		file, err := os.Open(source)
		if err != nil {
			return Request{}, err
		}
		reader = file
		close = file.Close
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	closeErr := close()
	if err != nil {
		return Request{}, fmt.Errorf("read resolution JSON: %w", err)
	}
	if closeErr != nil {
		return Request{}, closeErr
	}
	if len(data) > maxRequestBytes {
		return Request{}, fmt.Errorf("resolution JSON exceeds %d-byte limit", maxRequestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("invalid resolution JSON: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Request{}, fmt.Errorf("resolution JSON must contain exactly one document")
	}
	if err := ValidateRequest(&request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// ValidateRequest normalizes and checks one approval request in place.
func ValidateRequest(request *Request) error {
	request.Manufacturer = strings.TrimSpace(request.Manufacturer)
	request.PartNumber = strings.TrimSpace(request.PartNumber)
	request.Replacement.Manufacturer = strings.TrimSpace(request.Replacement.Manufacturer)
	request.Replacement.PartNumber = strings.TrimSpace(request.Replacement.PartNumber)
	request.Replacement.Provider = strings.ToLower(strings.TrimSpace(request.Replacement.Provider))
	request.Replacement.ProviderSKU = strings.TrimSpace(request.Replacement.ProviderSKU)
	request.ApprovedBy = strings.TrimSpace(request.ApprovedBy)
	request.Note = strings.TrimSpace(request.Note)

	if err := validateName("manufacturer", request.Manufacturer); err != nil {
		return err
	}
	if err := validatePartNumber("part_number", request.PartNumber); err != nil {
		return err
	}
	if err := validateName(
		"replacement.manufacturer",
		request.Replacement.Manufacturer,
	); err != nil {
		return err
	}
	if err := validatePartNumber(
		"replacement.part_number",
		request.Replacement.PartNumber,
	); err != nil {
		return err
	}
	if err := validateReplacementProvider(request.Replacement); err != nil {
		return err
	}
	if request.ApprovedBy == "" {
		return fmt.Errorf("approved_by is required: a resolution records a human decision")
	}
	if len(request.ApprovedBy) > maxApproverLength {
		return fmt.Errorf("approved_by must contain at most %d characters", maxApproverLength)
	}
	if len(request.Note) > maxNoteLength {
		return fmt.Errorf("note must contain at most %d characters", maxNoteLength)
	}
	if len(request.SourceDocuments) > maxEvidenceDocuments {
		return fmt.Errorf(
			"source_documents may contain at most %d entries",
			maxEvidenceDocuments,
		)
	}
	for index := range request.SourceDocuments {
		document := &request.SourceDocuments[index]
		document.URL = strings.TrimSpace(document.URL)
		document.SHA256 = strings.ToLower(strings.TrimSpace(document.SHA256))
		if err := validateEvidenceDocument(index, *document); err != nil {
			return err
		}
	}
	return nil
}

func validateName(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxNameLength {
		return fmt.Errorf("%s must contain at most %d characters", field, maxNameLength)
	}
	return nil
}

func validatePartNumber(field, value string) error {
	if length := len(value); length < minPartNumberLength || length > maxPartNumberLength {
		return fmt.Errorf(
			"%s must contain between %d and %d characters",
			field,
			minPartNumberLength,
			maxPartNumberLength,
		)
	}
	return nil
}

func validateReplacementProvider(replacement Replacement) error {
	switch replacement.Provider {
	case "", "mouser", "digikey", "ti", "nxp", "microchip":
	default:
		return fmt.Errorf(
			"replacement.provider %q has no native adapter",
			replacement.Provider,
		)
	}
	if replacement.ProviderSKU != "" && replacement.Provider == "" {
		return fmt.Errorf("replacement.provider is required when replacement.provider_sku is set")
	}
	if len(replacement.ProviderSKU) > maxPartNumberLength {
		return fmt.Errorf(
			"replacement.provider_sku must contain at most %d characters",
			maxPartNumberLength,
		)
	}
	return nil
}

func validateEvidenceDocument(index int, document EvidenceDocument) error {
	parsed, err := url.Parse(document.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("source_documents[%d].url must be an absolute https URL", index)
	}
	if !sha256Pattern.MatchString(document.SHA256) {
		return fmt.Errorf(
			"source_documents[%d].sha256 must be a 64-character hex digest",
			index,
		)
	}
	return nil
}
