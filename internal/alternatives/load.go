// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package alternatives

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const (
	maxRequestBytes = 4 * 1024 * 1024
	maxCandidates   = 25
)

var (
	decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

type fieldRequirement struct {
	field string
	ok    bool
}

// Load reads and strictly validates one alternatives request from a file/stdin.
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
		return Request{}, fmt.Errorf("read alternatives JSON: %w", err)
	}
	if closeErr != nil {
		return Request{}, closeErr
	}
	if len(data) > maxRequestBytes {
		return Request{}, fmt.Errorf("alternatives JSON exceeds %d-byte limit", maxRequestBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Request{}, errors.New("alternatives JSON is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode alternatives JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("decode alternatives JSON: trailing JSON value")
		}
		return Request{}, fmt.Errorf("decode alternatives JSON: %w", err)
	}
	if err := validateRequest(&request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func validateRequest(request *Request) error {
	request.Kind = strings.ToLower(strings.TrimSpace(request.Kind))
	switch request.Kind {
	case "resistor", "capacitor", "inductor":
	default:
		return errors.New("kind must be resistor, capacitor, or inductor")
	}
	if request.RequiredQuantity < 1 || request.RequiredQuantity > 1_000_000_000 {
		return errors.New("required_quantity must be between 1 and 1000000000")
	}
	if len(request.Candidates) < 1 || len(request.Candidates) > maxCandidates {
		return fmt.Errorf("candidates must contain between 1 and %d items", maxCandidates)
	}
	if err := validatePart(&request.Original, "original", request.Kind, true); err != nil {
		return err
	}
	seen := map[string]struct{}{
		partKey(request.Original): {},
	}
	for index := range request.Candidates {
		candidate := &request.Candidates[index]
		prefix := fmt.Sprintf("candidates[%d]", index)
		if err := validatePart(candidate, prefix, request.Kind, false); err != nil {
			return err
		}
		key := partKey(*candidate)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s duplicates the original or another candidate", prefix)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validatePart(
	part *PartSpec,
	prefix, kind string,
	original bool,
) error {
	part.PartNumber = strings.TrimSpace(part.PartNumber)
	part.Manufacturer = strings.TrimSpace(part.Manufacturer)
	part.Package = strings.TrimSpace(part.Package)
	part.Technology = strings.TrimSpace(part.Technology)
	part.Dielectric = strings.TrimSpace(part.Dielectric)
	if part.PartNumber == "" || len(part.PartNumber) > 80 {
		return fmt.Errorf("%s.part_number is required and must be at most 80 characters", prefix)
	}
	if part.Manufacturer == "" || len(part.Manufacturer) > 120 {
		return fmt.Errorf("%s.manufacturer is required and must be at most 120 characters", prefix)
	}
	for _, numeric := range numericFields(part) {
		field, value := numeric.name, numeric.value
		if value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if !decimalPattern.MatchString(trimmed) {
			return fmt.Errorf("%s.%s must be a non-negative decimal string", prefix, field)
		}
		number, ok := new(big.Rat).SetString(trimmed)
		if !ok || number.Sign() < 0 {
			return fmt.Errorf("%s.%s must be a non-negative decimal string", prefix, field)
		}
		if number.Sign() == 0 && requiresPositiveValue(field) {
			return fmt.Errorf("%s.%s must be greater than zero", prefix, field)
		}
		*value = trimmed
	}
	for _, temperature := range []struct {
		field string
		value *int
	}{
		{"temperature_min_c", part.TemperatureMinC},
		{"temperature_max_c", part.TemperatureMaxC},
	} {
		if temperature.value != nil &&
			(*temperature.value < -273 || *temperature.value > 1000) {
			return fmt.Errorf("%s.%s must be between -273 and 1000", prefix, temperature.field)
		}
	}
	if part.TemperatureMinC != nil && part.TemperatureMaxC != nil &&
		*part.TemperatureMinC > *part.TemperatureMaxC {
		return fmt.Errorf("%s temperature range is inverted", prefix)
	}
	for documentIndex := range part.SourceDocuments {
		document := &part.SourceDocuments[documentIndex]
		document.URL = strings.TrimSpace(document.URL)
		document.SHA256 = strings.ToLower(strings.TrimSpace(document.SHA256))
		parsed, err := url.Parse(document.URL)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" ||
			parsed.User != nil {
			return fmt.Errorf("%s.source_documents[%d].url must be HTTPS", prefix, documentIndex)
		}
		if !sha256Pattern.MatchString(document.SHA256) {
			return fmt.Errorf("%s.source_documents[%d].sha256 must contain 64 hex characters", prefix, documentIndex)
		}
	}
	if err := normalizeQualifications(part, prefix); err != nil {
		return err
	}
	if err := validateRelevantFields(part, prefix, kind); err != nil {
		return err
	}
	if !original {
		return nil
	}
	required := []fieldRequirement{
		{"package", part.Package != ""},
		{"tolerance_percent", part.TolerancePercent != nil},
		{"temperature_min_c", part.TemperatureMinC != nil},
		{"temperature_max_c", part.TemperatureMaxC != nil},
	}
	switch kind {
	case "resistor":
		required = append(required,
			fieldRequirement{"resistance_ohms", part.ResistanceOhms != nil},
			fieldRequirement{"power_watts", part.PowerWatts != nil},
			fieldRequirement{"voltage_volts", part.VoltageVolts != nil},
			fieldRequirement{"technology", part.Technology != ""},
		)
	case "capacitor":
		required = append(required,
			fieldRequirement{"capacitance_farads", part.CapacitanceFarads != nil},
			fieldRequirement{"voltage_volts", part.VoltageVolts != nil},
			fieldRequirement{"dielectric", part.Dielectric != ""},
			fieldRequirement{"polarized", part.Polarized != nil},
		)
	case "inductor":
		required = append(required,
			fieldRequirement{"inductance_henries", part.InductanceHenries != nil},
			fieldRequirement{"rated_current_amps", part.RatedCurrentAmps != nil},
			fieldRequirement{"saturation_current_amps", part.SaturationAmps != nil},
			fieldRequirement{"dc_resistance_ohms", part.DCResistanceOhms != nil},
			fieldRequirement{"shielded", part.Shielded != nil},
		)
	}
	for _, requirement := range required {
		if !requirement.ok {
			return fmt.Errorf("original.%s is required for %s compatibility", requirement.field, kind)
		}
	}
	return nil
}

func validateRelevantFields(
	part *PartSpec,
	prefix, kind string,
) error {
	var irrelevant []fieldRequirement
	switch kind {
	case "resistor":
		irrelevant = []fieldRequirement{
			{"capacitance_farads", part.CapacitanceFarads != nil},
			{"inductance_henries", part.InductanceHenries != nil},
			{"esr_ohms", part.ESROhms != nil},
			{"rated_current_amps", part.RatedCurrentAmps != nil},
			{"saturation_current_amps", part.SaturationAmps != nil},
			{"dc_resistance_ohms", part.DCResistanceOhms != nil},
			{"dielectric", part.Dielectric != ""},
			{"polarized", part.Polarized != nil},
			{"shielded", part.Shielded != nil},
		}
	case "capacitor":
		irrelevant = []fieldRequirement{
			{"resistance_ohms", part.ResistanceOhms != nil},
			{"inductance_henries", part.InductanceHenries != nil},
			{"power_watts", part.PowerWatts != nil},
			{"rated_current_amps", part.RatedCurrentAmps != nil},
			{"saturation_current_amps", part.SaturationAmps != nil},
			{"dc_resistance_ohms", part.DCResistanceOhms != nil},
			{"technology", part.Technology != ""},
			{"shielded", part.Shielded != nil},
		}
	case "inductor":
		irrelevant = []fieldRequirement{
			{"resistance_ohms", part.ResistanceOhms != nil},
			{"capacitance_farads", part.CapacitanceFarads != nil},
			{"power_watts", part.PowerWatts != nil},
			{"voltage_volts", part.VoltageVolts != nil},
			{"esr_ohms", part.ESROhms != nil},
			{"technology", part.Technology != ""},
			{"dielectric", part.Dielectric != ""},
			{"polarized", part.Polarized != nil},
		}
	}
	for _, field := range irrelevant {
		if field.ok {
			return fmt.Errorf("%s.%s is not used for %s compatibility", prefix, field.field, kind)
		}
	}
	return nil
}

func normalizeQualifications(part *PartSpec, prefix string) error {
	if part.Qualifications == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for index, qualification := range part.Qualifications {
		qualification = strings.TrimSpace(qualification)
		if qualification == "" {
			return fmt.Errorf("%s.qualifications[%d] cannot be empty", prefix, index)
		}
		key := normalizeToken(qualification)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s.qualifications contains duplicate %q", prefix, qualification)
		}
		seen[key] = struct{}{}
		part.Qualifications[index] = qualification
	}
	return nil
}

func requiresPositiveValue(field string) bool {
	switch field {
	case "tolerance_percent", "esr_ohms", "dc_resistance_ohms":
		return false
	default:
		return true
	}
}

type numericField struct {
	name  string
	value *string
}

// numericFields returns the validated fields in a fixed declaration order so
// error selection is deterministic: with a map, the field named in a
// multi-error request would vary run to run, violating the deterministic
// output rule and breaking golden tests of failure envelopes.
func numericFields(part *PartSpec) []numericField {
	return []numericField{
		{"resistance_ohms", part.ResistanceOhms},
		{"capacitance_farads", part.CapacitanceFarads},
		{"inductance_henries", part.InductanceHenries},
		{"tolerance_percent", part.TolerancePercent},
		{"power_watts", part.PowerWatts},
		{"voltage_volts", part.VoltageVolts},
		{"esr_ohms", part.ESROhms},
		{"rated_current_amps", part.RatedCurrentAmps},
		{"saturation_current_amps", part.SaturationAmps},
		{"dc_resistance_ohms", part.DCResistanceOhms},
		{"length_mm", part.LengthMM},
		{"width_mm", part.WidthMM},
		{"height_mm", part.HeightMM},
	}
}

func partKey(part PartSpec) string {
	return strings.ToUpper(strings.TrimSpace(part.Manufacturer)) + "\x00" +
		strings.ToUpper(strings.TrimSpace(part.PartNumber))
}
