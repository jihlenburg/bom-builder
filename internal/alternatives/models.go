// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package alternatives implements conservative, deterministic passive-part
// compatibility checks. It treats unprovided critical data as unknown.
package alternatives

import "github.com/jihlenburg/bom-builder/internal/procurement"

// Request describes one original part and explicitly proposed candidates.
type Request struct {
	Kind             string     `json:"kind"`
	RequiredQuantity int        `json:"required_quantity"`
	Original         PartSpec   `json:"original"`
	Candidates       []PartSpec `json:"candidates"`
}

// PartSpec is the union of supported passive-component fields. Fields not
// relevant to the selected kind must be omitted.
type PartSpec struct {
	PartNumber   string `json:"part_number"`
	Manufacturer string `json:"manufacturer"`
	Package      string `json:"package,omitempty"`

	ResistanceOhms    *string          `json:"resistance_ohms,omitempty"`
	CapacitanceFarads *string          `json:"capacitance_farads,omitempty"`
	InductanceHenries *string          `json:"inductance_henries,omitempty"`
	TolerancePercent  *string          `json:"tolerance_percent,omitempty"`
	PowerWatts        *string          `json:"power_watts,omitempty"`
	VoltageVolts      *string          `json:"voltage_volts,omitempty"`
	ESROhms           *string          `json:"esr_ohms,omitempty"`
	RatedCurrentAmps  *string          `json:"rated_current_amps,omitempty"`
	SaturationAmps    *string          `json:"saturation_current_amps,omitempty"`
	DCResistanceOhms  *string          `json:"dc_resistance_ohms,omitempty"`
	LengthMM          *string          `json:"length_mm,omitempty"`
	WidthMM           *string          `json:"width_mm,omitempty"`
	HeightMM          *string          `json:"height_mm,omitempty"`
	TemperatureMinC   *int             `json:"temperature_min_c,omitempty"`
	TemperatureMaxC   *int             `json:"temperature_max_c,omitempty"`
	Technology        string           `json:"technology,omitempty"`
	Dielectric        string           `json:"dielectric,omitempty"`
	Polarized         *bool            `json:"polarized,omitempty"`
	Shielded          *bool            `json:"shielded,omitempty"`
	Qualifications    []string         `json:"qualifications,omitempty"`
	SourceDocuments   []SourceDocument `json:"source_documents,omitempty"`
}

// SourceDocument identifies user-supplied evidence for a specification set.
type SourceDocument struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// FieldComparison explains one deterministic compatibility decision.
type FieldComparison struct {
	Field          string  `json:"field"`
	Requirement    string  `json:"requirement"`
	OriginalValue  *string `json:"original_value"`
	CandidateValue *string `json:"candidate_value"`
	Relation       string  `json:"relation"`
}

// Result combines compatibility evidence with optional sourcing evidence.
type Result struct {
	Candidate                 PartSpec          `json:"candidate"`
	Compatibility             string            `json:"compatibility"`
	EngineeringReviewRequired bool              `json:"engineering_review_required"`
	EvidenceDocumentCount     int               `json:"evidence_document_count"`
	Comparisons               []FieldComparison `json:"comparisons"`
	RejectedReasons           []string          `json:"rejected_reasons"`
	Sourcing                  *SourcingResult   `json:"sourcing,omitempty"`
	Rank                      *int              `json:"rank,omitempty"`
	RecommendedForReview      bool              `json:"recommended_for_review"`
}

// SourcingResult retains provider provenance without duplicating the selected
// offer in both offer and offers, which keeps agent-facing output compact.
type SourcingResult struct {
	Demand                        procurement.Demand  `json:"demand"`
	Status                        string              `json:"status"`
	SelectedProvider              string              `json:"selected_provider,omitempty"`
	SelectedDistributorPartNumber string              `json:"selected_distributor_part_number,omitempty"`
	Offers                        []procurement.Offer `json:"offers"`
	CandidateCount                int                 `json:"candidate_count"`
	IssueCode                     string              `json:"issue_code,omitempty"`
	IssueMessage                  string              `json:"issue_message,omitempty"`
}

// CompactSourcing converts the common sourcing contract to its non-duplicating
// alternatives representation.
func CompactSourcing(source procurement.SourcedPart) *SourcingResult {
	offers := append([]procurement.Offer(nil), source.Offers...)
	if len(offers) == 0 && source.Offer != nil {
		offers = append(offers, *source.Offer)
	}
	result := &SourcingResult{
		Demand:         source.Demand,
		Status:         source.Status,
		Offers:         offers,
		CandidateCount: source.CandidateCount,
		IssueCode:      source.IssueCode,
		IssueMessage:   source.IssueMessage,
	}
	if source.Offer != nil {
		result.SelectedProvider = source.Offer.Provider
		result.SelectedDistributorPartNumber =
			source.Offer.DistributorPartNumber
	}
	if result.Offers == nil {
		result.Offers = []procurement.Offer{}
	}
	return result
}
