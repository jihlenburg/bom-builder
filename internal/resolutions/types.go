// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package resolutions persists human-approved part resolutions. A resolution
// records that a named person cleared engineering review for one replacement
// of one original demand, with document evidence. The store never makes that
// decision itself: it only remembers decisions a human explicitly approved.
package resolutions

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SchemaVersion is the resolutions SQLite schema this build reads and writes.
const SchemaVersion = 1

// Record statuses. Exactly one record per demand key may be active; approving
// a new resolution supersedes the previous one, and revoking retires a
// resolution without deleting its audit trail.
const (
	StatusActive     = "active"
	StatusSuperseded = "superseded"
	StatusRevoked    = "revoked"
)

// Audit actions recorded in the append-only event history.
const (
	ActionApproved   = "approved"
	ActionSuperseded = "superseded"
	ActionRevoked    = "revoked"
)

// Replacement identifies the approved part, optionally pinned to one
// provider-orderable SKU.
type Replacement struct {
	Manufacturer string `json:"manufacturer"`
	PartNumber   string `json:"part_number"`
	Provider     string `json:"provider,omitempty"`
	ProviderSKU  string `json:"provider_sku,omitempty"`
}

// EvidenceDocument identifies one fetched document backing an approval.
type EvidenceDocument struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Request is one strict approval document supplied by the operator.
type Request struct {
	Manufacturer    string             `json:"manufacturer"`
	PartNumber      string             `json:"part_number"`
	Replacement     Replacement        `json:"replacement"`
	ApprovedBy      string             `json:"approved_by"`
	Note            string             `json:"note,omitempty"`
	SourceDocuments []EvidenceDocument `json:"source_documents,omitempty"`
}

// Record is one stored resolution with its full approval identity.
type Record struct {
	ResolutionID    string             `json:"resolution_id"`
	Manufacturer    string             `json:"manufacturer"`
	PartNumber      string             `json:"part_number"`
	Replacement     Replacement        `json:"replacement"`
	ApprovedBy      string             `json:"approved_by"`
	Note            string             `json:"note,omitempty"`
	SourceDocuments []EvidenceDocument `json:"source_documents"`
	Status          string             `json:"status"`
	ApprovedAt      time.Time          `json:"approved_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// Event is one append-only audit history entry.
type Event struct {
	EventID      int64     `json:"event_id"`
	ResolutionID string    `json:"resolution_id"`
	Action       string    `json:"action"`
	Actor        string    `json:"actor"`
	Manufacturer string    `json:"manufacturer"`
	PartNumber   string    `json:"part_number"`
	Details      string    `json:"details,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// Status summarizes one resolutions database.
type Status struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	SchemaVersion   int    `json:"schema_version"`
	ActiveCount     int    `json:"active_count"`
	SupersededCount int    `json:"superseded_count"`
	RevokedCount    int    `json:"revoked_count"`
	EventCount      int    `json:"event_count"`
}

// RevokeResult is an exact preview or applied revocation.
type RevokeResult struct {
	ResolutionID string  `json:"resolution_id"`
	Matched      bool    `json:"matched"`
	Record       *Record `json:"record,omitempty"`
	ApplyToken   string  `json:"apply_token,omitempty"`
	Applied      bool    `json:"applied"`
}

// Error is a stable resolutions-layer failure.
type Error struct {
	Kind    string
	Message string
}

func (storeError *Error) Error() string {
	if storeError == nil {
		return ""
	}
	return "resolutions " + storeError.Kind + ": " + storeError.Message
}

// DefaultPath returns the platform-native per-user resolutions database path.
// Resolutions are durable engineering decisions, not disposable cache state,
// so they live under the user configuration directory rather than the cache
// directory an operating system may reclaim.
func DefaultPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", &Error{
			Kind:    "configuration",
			Message: "user configuration directory is unavailable",
		}
	}
	return filepath.Join(root, "bom-builder", "resolutions-v1.sqlite3"), nil
}

// demandKey normalizes one original-demand identity for uniqueness checks.
// Manufacturer and part number match case-insensitively: a resolution is a
// human decision about one physical part, not about one spelling of it.
func demandKey(manufacturer, partNumber string) string {
	return strings.ToLower(strings.TrimSpace(manufacturer)) +
		"\x00" +
		strings.ToLower(strings.TrimSpace(partNumber))
}
