// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package contract defines BOM Builder's public JSON protocol and design types.
package contract

import (
	"encoding/json"
	"time"

	"github.com/jihlenburg/bom-builder/internal/alternatives"
	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

const (
	// SchemaVersion identifies the breaking generation of the Go JSON contract.
	SchemaVersion = "2.0"

	// Stable process exit codes.
	ExitOK         = 0
	ExitInput      = 2
	ExitIncomplete = 3
	ExitProvider   = 4
	ExitInternal   = 5
)

// Issue is one stable machine-readable warning or error.
type Issue struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ErrorEnvelope is emitted when a command cannot produce its normal result.
type ErrorEnvelope struct {
	SchemaVersion string  `json:"schema_version"`
	Status        string  `json:"status"`
	ExitCode      int     `json:"exit_code"`
	Command       string  `json:"command"`
	Errors        []Issue `json:"errors"`
}

// Runtime describes the native executable that emitted a capability document.
type Runtime struct {
	Language  string `json:"language"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

// Features is the truthful feature manifest for the current build.
type Features struct {
	JSONStdout                  bool `json:"json_stdout"`
	StdinDesigns                bool `json:"stdin_designs"`
	StrictInput                 bool `json:"strict_input"`
	ProviderConfiguration       bool `json:"provider_configuration"`
	LiveProviderHealth          bool `json:"live_provider_health"`
	Pricing                     bool `json:"pricing"`
	Lookup                      bool `json:"lookup"`
	AlternativeParts            bool `json:"alternative_parts"`
	DatasheetDownloads          bool `json:"datasheet_downloads"`
	PersistentLookupCache       bool `json:"persistent_lookup_cache"`
	NativeGoBinary              bool `json:"native_go_binary"`
	NXPRequiresSystemBrowser    bool `json:"nxp_requires_system_browser"`
	TITransportImplementation   bool `json:"ti_transport_implemented"`
	ConcurrentProviderExecution bool `json:"concurrent_provider_execution"`
}

// SchemaBundle contains every public JSON Schema in one capability response.
type SchemaBundle struct {
	Input        json.RawMessage `json:"input"`
	Alternatives json.RawMessage `json:"alternatives"`
	Cache        json.RawMessage `json:"cache"`
	Output       json.RawMessage `json:"output"`
	Providers    json.RawMessage `json:"providers"`
	Resolutions  json.RawMessage `json:"resolutions"`
}

// CapabilitiesEnvelope is the authoritative machine discovery document.
type CapabilitiesEnvelope struct {
	SchemaVersion           string                     `json:"schema_version"`
	Status                  string                     `json:"status"`
	ExitCode                int                        `json:"exit_code"`
	Version                 string                     `json:"version"`
	Runtime                 Runtime                    `json:"runtime"`
	Commands                []string                   `json:"commands"`
	PlannedCommands         []string                   `json:"planned_commands"`
	Distributors            []string                   `json:"distributors"`
	ImplementedDistributors []string                   `json:"implemented_distributors"`
	Manufacturers           []string                   `json:"manufacturers"`
	Services                []string                   `json:"services"`
	ArtifactFormats         []string                   `json:"artifact_formats"`
	Features                Features                   `json:"features"`
	ProviderConfiguration   *ProviderDiscoveryEnvelope `json:"provider_configuration,omitempty"`
	Schemas                 *SchemaBundle              `json:"schemas,omitempty"`
}

// Locale describes a distributor market and currency context.
type Locale struct {
	Site          string `json:"site"`
	Language      string `json:"language"`
	Currency      string `json:"currency"`
	ShipToCountry string `json:"ship_to_country"`
}

// ProviderDetails contains non-secret configuration facts.
type ProviderDetails struct {
	Implementation         string  `json:"implementation"`
	CredentialCount        *int    `json:"credential_count,omitempty"`
	AccountIDConfigured    *bool   `json:"account_id_configured,omitempty"`
	AuthenticationRequired *bool   `json:"authentication_required,omitempty"`
	Locale                 *Locale `json:"locale,omitempty"`
	SystemBrowser          string  `json:"system_browser,omitempty"`
	Model                  string  `json:"model,omitempty"`
	ResultCount            *int    `json:"result_count,omitempty"`
	MatchedPartNumber      string  `json:"matched_part_number,omitempty"`
	Currency               string  `json:"currency,omitempty"`
	HeaderMode             string  `json:"header_mode,omitempty"`
	RateLimitRemaining     *int    `json:"rate_limit_remaining,omitempty"`
}

// ProviderCapability describes configuration and implementation readiness.
type ProviderCapability struct {
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Implemented  bool            `json:"implemented"`
	Configured   bool            `json:"configured"`
	Status       string          `json:"status"`
	Live         bool            `json:"live"`
	LatencyMS    *int64          `json:"latency_ms,omitempty"`
	RequestCount int             `json:"request_count"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Details      ProviderDetails `json:"details"`
}

// ProviderDiscoveryEnvelope is returned by providers list.
type ProviderDiscoveryEnvelope struct {
	SchemaVersion string               `json:"schema_version"`
	Status        string               `json:"status"`
	ExitCode      int                  `json:"exit_code"`
	Live          bool                 `json:"live"`
	Providers     []ProviderCapability `json:"providers"`
}

// Part is one authored electrical BOM line.
type Part struct {
	PartNumber   string  `json:"part_number"`
	Manufacturer string  `json:"manufacturer"`
	Quantity     int     `json:"quantity"`
	Reference    *string `json:"reference,omitempty"`
	Description  *string `json:"description,omitempty"`
	Package      *string `json:"package,omitempty"`
	Pins         *int    `json:"pins,omitempty"`
	// Optional assembly-BOM fields used by `export ec-bom`.
	Designators []string `json:"designators,omitempty"`
	Value       *string  `json:"value,omitempty"`
	Mounted     *bool    `json:"mounted,omitempty"`
	Comment     *string  `json:"comment,omitempty"`
}

// Design is one strictly validated BOM design document.
type Design struct {
	Design  string  `json:"design"`
	Version *string `json:"version,omitempty"`
	Parts   []Part  `json:"parts"`
}

// ValidationEnvelope reports design validation without contacting providers.
type ValidationEnvelope struct {
	SchemaVersion string   `json:"schema_version"`
	Status        string   `json:"status"`
	ExitCode      int      `json:"exit_code"`
	DesignCount   int      `json:"design_count"`
	PartCount     int      `json:"part_count"`
	Designs       []string `json:"designs"`
}

// ProviderRunMetadata reports one selected provider's bounded request count.
type ProviderRunMetadata struct {
	Name         string `json:"name"`
	RequestCount int    `json:"request_count"`
}

// CacheRunMetadata reports normalized lookup reuse for one command.
type CacheRunMetadata struct {
	Policy               string `json:"policy"`
	Hits                 int64  `json:"hits"`
	StaleHits            int64  `json:"stale_hits"`
	Misses               int64  `json:"misses"`
	Refreshes            int64  `json:"refreshes"`
	Writes               int64  `json:"writes"`
	Bypasses             int64  `json:"bypasses"`
	ErrorCount           int64  `json:"error_count"`
	ReusedSourceRequests int64  `json:"reused_source_requests"`
}

// RunMetadata identifies one bounded sourcing invocation.
type RunMetadata struct {
	RunID        string                `json:"run_id"`
	StartedAt    time.Time             `json:"started_at"`
	DurationMS   int64                 `json:"duration_ms"`
	Providers    []ProviderRunMetadata `json:"providers"`
	RequestCount int                   `json:"request_count"`
	Cache        *CacheRunMetadata     `json:"cache,omitempty"`
}

// DocumentLink is one provider-derived evidence location.
type DocumentLink struct {
	Kind                   string `json:"kind"`
	Provider               string `json:"provider"`
	URL                    string `json:"url"`
	ManufacturerPartNumber string `json:"manufacturer_part_number,omitempty"`
	Preferred              bool   `json:"preferred"`
	Downloadable           bool   `json:"downloadable"`
}

// DocumentArtifact records a verified, immutable-on-write PDF download.
type DocumentArtifact struct {
	SourceURL  string    `json:"source_url"`
	FinalURL   string    `json:"final_url"`
	OutputPath string    `json:"output_path"`
	MIMEType   string    `json:"mime_type"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// DocumentListEnvelope reports provider evidence links for one part.
type DocumentListEnvelope struct {
	SchemaVersion string             `json:"schema_version"`
	Status        string             `json:"status"`
	ExitCode      int                `json:"exit_code"`
	Command       string             `json:"command"`
	Version       string             `json:"version"`
	Run           RunMetadata        `json:"run"`
	Query         procurement.Demand `json:"query"`
	Documents     []DocumentLink     `json:"documents"`
	Warnings      []Issue            `json:"warnings"`
	Errors        []Issue            `json:"errors"`
}

// DocumentFetchEnvelope reports one verified PDF artifact.
type DocumentFetchEnvelope struct {
	SchemaVersion string           `json:"schema_version"`
	Status        string           `json:"status"`
	ExitCode      int              `json:"exit_code"`
	Command       string           `json:"command"`
	Version       string           `json:"version"`
	Run           RunMetadata      `json:"run"`
	Artifact      DocumentArtifact `json:"artifact"`
	Warnings      []Issue          `json:"warnings"`
	Errors        []Issue          `json:"errors"`
}

// ExportArtifact records an immutable-on-write exported BOM file.
type ExportArtifact struct {
	OutputPath string `json:"output_path"`
	Format     string `json:"format"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	LineCount  int    `json:"line_count"`
}

// ExportEnvelope reports one exported BOM artifact.
type ExportEnvelope struct {
	SchemaVersion string         `json:"schema_version"`
	Status        string         `json:"status"`
	ExitCode      int            `json:"exit_code"`
	Command       string         `json:"command"`
	Version       string         `json:"version"`
	Design        string         `json:"design"`
	Artifact      ExportArtifact `json:"artifact"`
	Warnings      []Issue        `json:"warnings"`
	Errors        []Issue        `json:"errors"`
}

// AlternativeSummary reports deterministic compatibility and sourcing counts.
type AlternativeSummary struct {
	CandidateCount     int `json:"candidate_count"`
	CompatibleCount    int `json:"compatible_count"`
	UnknownCount       int `json:"unknown_count"`
	IncompatibleCount  int `json:"incompatible_count"`
	SourcedCount       int `json:"sourced_count"`
	InStockCount       int `json:"in_stock_count"`
	ProviderErrorCount int `json:"provider_error_count"`
	RecommendedCount   int `json:"recommended_for_review_count"`
}

// AlternativesEnvelope reports conservative compatibility and live sourcing.
type AlternativesEnvelope struct {
	SchemaVersion    string                       `json:"schema_version"`
	Status           string                       `json:"status"`
	ExitCode         int                          `json:"exit_code"`
	Command          string                       `json:"command"`
	Version          string                       `json:"version"`
	Run              RunMetadata                  `json:"run"`
	Kind             string                       `json:"kind"`
	RequiredQuantity int                          `json:"required_quantity"`
	Original         alternatives.PartSpec        `json:"original"`
	OriginalSourcing *alternatives.SourcingResult `json:"original_sourcing,omitempty"`
	Summary          AlternativeSummary           `json:"summary"`
	Candidates       []alternatives.Result        `json:"candidates"`
	Warnings         []Issue                      `json:"warnings"`
	Errors           []Issue                      `json:"errors"`
}

// PricingEnvelope is emitted by both lookup and price.
type PricingEnvelope struct {
	SchemaVersion string                     `json:"schema_version"`
	Status        string                     `json:"status"`
	ExitCode      int                        `json:"exit_code"`
	Command       string                     `json:"command"`
	Version       string                     `json:"version"`
	Run           RunMetadata                `json:"run"`
	Units         int                        `json:"units"`
	Attrition     money.Decimal              `json:"attrition"`
	Summary       procurement.PricingSummary `json:"summary"`
	Parts         []procurement.SourcedPart  `json:"parts"`
	Warnings      []Issue                    `json:"warnings"`
	Errors        []Issue                    `json:"errors"`
}

// CacheStoreStatus summarizes one local normalized-result database.
type CacheStoreStatus struct {
	Path          string     `json:"path"`
	Exists        bool       `json:"exists"`
	SchemaVersion int        `json:"schema_version"`
	EntryCount    int        `json:"entry_count"`
	FreshCount    int        `json:"fresh_count"`
	StaleCount    int        `json:"stale_count"`
	SizeBytes     int64      `json:"size_bytes"`
	OldestEntry   *time.Time `json:"oldest_entry,omitempty"`
	NewestEntry   *time.Time `json:"newest_entry,omitempty"`
}

// CacheEntryMetadata exposes cache identity and provenance without payload data.
type CacheEntryMetadata struct {
	Provider         string    `json:"provider"`
	Key              string    `json:"key"`
	PartNumber       string    `json:"part_number"`
	Manufacturer     string    `json:"manufacturer"`
	RequiredQuantity int       `json:"required_quantity"`
	ResultStatus     string    `json:"result_status"`
	AdapterVersion   string    `json:"adapter_version"`
	FetchedAt        time.Time `json:"fetched_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	SourceRequests   int       `json:"source_requests"`
	Stale            bool      `json:"stale"`
}

// CacheVerifyReport records SQLite and normalized-payload verification.
type CacheVerifyReport struct {
	OK             bool     `json:"ok"`
	IntegrityCheck string   `json:"integrity_check"`
	CheckedEntries int      `json:"checked_entries"`
	InvalidEntries int      `json:"invalid_entries"`
	Issues         []string `json:"issues"`
}

// CachePruneResult is an exact preview or applied deletion operation.
type CachePruneResult struct {
	Scope        string `json:"scope"`
	MatchedCount int    `json:"matched_count"`
	ApplyToken   string `json:"apply_token,omitempty"`
	Applied      bool   `json:"applied"`
	DeletedCount int64  `json:"deleted_count"`
}

// CacheStatusEnvelope is emitted by cache status.
type CacheStatusEnvelope struct {
	SchemaVersion string           `json:"schema_version"`
	Status        string           `json:"status"`
	ExitCode      int              `json:"exit_code"`
	Command       string           `json:"command"`
	Version       string           `json:"version"`
	Cache         CacheStoreStatus `json:"cache"`
	Warnings      []Issue          `json:"warnings"`
	Errors        []Issue          `json:"errors"`
}

// CacheListEnvelope is emitted by cache list.
type CacheListEnvelope struct {
	SchemaVersion string               `json:"schema_version"`
	Status        string               `json:"status"`
	ExitCode      int                  `json:"exit_code"`
	Command       string               `json:"command"`
	Version       string               `json:"version"`
	Cache         CacheStoreStatus     `json:"cache"`
	Entries       []CacheEntryMetadata `json:"entries"`
	Warnings      []Issue              `json:"warnings"`
	Errors        []Issue              `json:"errors"`
}

// CacheVerifyEnvelope is emitted by cache verify.
type CacheVerifyEnvelope struct {
	SchemaVersion string            `json:"schema_version"`
	Status        string            `json:"status"`
	ExitCode      int               `json:"exit_code"`
	Command       string            `json:"command"`
	Version       string            `json:"version"`
	Cache         CacheStoreStatus  `json:"cache"`
	Verification  CacheVerifyReport `json:"verification"`
	Warnings      []Issue           `json:"warnings"`
	Errors        []Issue           `json:"errors"`
}

// CachePruneEnvelope is emitted by cache prune previews and applications.
type CachePruneEnvelope struct {
	SchemaVersion string           `json:"schema_version"`
	Status        string           `json:"status"`
	ExitCode      int              `json:"exit_code"`
	Command       string           `json:"command"`
	Version       string           `json:"version"`
	Cache         CacheStoreStatus `json:"cache"`
	Prune         CachePruneResult `json:"prune"`
	Warnings      []Issue          `json:"warnings"`
	Errors        []Issue          `json:"errors"`
}

// ResolutionReplacement identifies one approved replacement part.
type ResolutionReplacement struct {
	Manufacturer string `json:"manufacturer"`
	PartNumber   string `json:"part_number"`
	Provider     string `json:"provider,omitempty"`
	ProviderSKU  string `json:"provider_sku,omitempty"`
}

// ResolutionEvidenceDocument identifies one document backing an approval.
type ResolutionEvidenceDocument struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// ResolutionRecord is one stored human-approved resolution.
type ResolutionRecord struct {
	ResolutionID    string                       `json:"resolution_id"`
	Manufacturer    string                       `json:"manufacturer"`
	PartNumber      string                       `json:"part_number"`
	Replacement     ResolutionReplacement        `json:"replacement"`
	ApprovedBy      string                       `json:"approved_by"`
	Note            string                       `json:"note,omitempty"`
	SourceDocuments []ResolutionEvidenceDocument `json:"source_documents"`
	Status          string                       `json:"status"`
	ApprovedAt      time.Time                    `json:"approved_at"`
	UpdatedAt       time.Time                    `json:"updated_at"`
}

// ResolutionEvent is one append-only audit history entry.
type ResolutionEvent struct {
	EventID      int64     `json:"event_id"`
	ResolutionID string    `json:"resolution_id"`
	Action       string    `json:"action"`
	Actor        string    `json:"actor"`
	Manufacturer string    `json:"manufacturer"`
	PartNumber   string    `json:"part_number"`
	Details      string    `json:"details,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// ResolutionsStoreStatus summarizes one resolutions database.
type ResolutionsStoreStatus struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	SchemaVersion   int    `json:"schema_version"`
	ActiveCount     int    `json:"active_count"`
	SupersededCount int    `json:"superseded_count"`
	RevokedCount    int    `json:"revoked_count"`
	EventCount      int    `json:"event_count"`
}

// ResolutionRevokeResult is an exact preview or applied revocation.
type ResolutionRevokeResult struct {
	ResolutionID string            `json:"resolution_id"`
	Matched      bool              `json:"matched"`
	Record       *ResolutionRecord `json:"record,omitempty"`
	ApplyToken   string            `json:"apply_token,omitempty"`
	Applied      bool              `json:"applied"`
}

// ResolutionApproveEnvelope is emitted by resolutions approve.
type ResolutionApproveEnvelope struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        string                 `json:"status"`
	ExitCode      int                    `json:"exit_code"`
	Command       string                 `json:"command"`
	Version       string                 `json:"version"`
	Resolutions   ResolutionsStoreStatus `json:"resolutions"`
	Resolution    ResolutionRecord       `json:"resolution"`
	Superseded    *ResolutionRecord      `json:"superseded,omitempty"`
	Warnings      []Issue                `json:"warnings"`
	Errors        []Issue                `json:"errors"`
}

// ResolutionListEnvelope is emitted by resolutions list.
type ResolutionListEnvelope struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        string                 `json:"status"`
	ExitCode      int                    `json:"exit_code"`
	Command       string                 `json:"command"`
	Version       string                 `json:"version"`
	Resolutions   ResolutionsStoreStatus `json:"resolutions"`
	Records       []ResolutionRecord     `json:"records"`
	Warnings      []Issue                `json:"warnings"`
	Errors        []Issue                `json:"errors"`
}

// ResolutionHistoryEnvelope is emitted by resolutions history.
type ResolutionHistoryEnvelope struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        string                 `json:"status"`
	ExitCode      int                    `json:"exit_code"`
	Command       string                 `json:"command"`
	Version       string                 `json:"version"`
	Resolutions   ResolutionsStoreStatus `json:"resolutions"`
	Events        []ResolutionEvent      `json:"events"`
	Warnings      []Issue                `json:"warnings"`
	Errors        []Issue                `json:"errors"`
}

// ResolutionRevokeEnvelope is emitted by resolutions revoke previews and
// applications.
type ResolutionRevokeEnvelope struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        string                 `json:"status"`
	ExitCode      int                    `json:"exit_code"`
	Command       string                 `json:"command"`
	Version       string                 `json:"version"`
	Resolutions   ResolutionsStoreStatus `json:"resolutions"`
	Revoke        ResolutionRevokeResult `json:"revoke"`
	Warnings      []Issue                `json:"warnings"`
	Errors        []Issue                `json:"errors"`
}
