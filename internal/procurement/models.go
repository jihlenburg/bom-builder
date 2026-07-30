package procurement

import "github.com/jihlenburg/bom-builder/internal/money"

// Demand is one deterministic aggregated sourcing requirement.
type Demand struct {
	PartNumber       string            `json:"part_number"`
	Manufacturer     string            `json:"manufacturer"`
	QuantityPerUnit  int               `json:"quantity_per_unit"`
	RequiredQuantity int               `json:"required_quantity"`
	Description      string            `json:"description,omitempty"`
	Package          string            `json:"package,omitempty"`
	Pins             int               `json:"pins,omitempty"`
	References       []SourceReference `json:"references,omitempty"`
}

// SourceReference preserves the authored design and reference-designator origin.
type SourceReference struct {
	Design    string `json:"design"`
	Reference string `json:"reference"`
}

// Offer is one normalized provider result. A plan is selected only when stock
// is known, sufficient, and the match does not require engineering review.
type Offer struct {
	Provider               string        `json:"provider"`
	DistributorPartNumber  string        `json:"distributor_part_number,omitempty"`
	ManufacturerPartNumber string        `json:"manufacturer_part_number,omitempty"`
	Manufacturer           string        `json:"manufacturer,omitempty"`
	Description            string        `json:"description,omitempty"`
	Category               string        `json:"category,omitempty"`
	MatchMethod            string        `json:"match_method"`
	ReviewRequired         bool          `json:"review_required"`
	RequiredQuantity       int           `json:"required_quantity"`
	AvailableQuantity      *int          `json:"available_quantity,omitempty"`
	Availability           string        `json:"availability,omitempty"`
	LeadTime               string        `json:"lead_time,omitempty"`
	LifecycleStatus        string        `json:"lifecycle_status,omitempty"`
	Packaging              string        `json:"packaging,omitempty"`
	MinimumOrderQuantity   int           `json:"minimum_order_quantity,omitempty"`
	OrderMultiple          int           `json:"order_multiple,omitempty"`
	StandardPackQuantity   int           `json:"standard_pack_quantity,omitempty"`
	OrderLimit             *int          `json:"order_limit,omitempty"`
	DatasheetURL           string        `json:"datasheet_url,omitempty"`
	ProductURL             string        `json:"product_url,omitempty"`
	PriceBreaks            []PriceBreak  `json:"price_breaks,omitempty"`
	CandidatePlan          *PurchasePlan `json:"candidate_plan,omitempty"`
	SelectedPlan           *PurchasePlan `json:"selected_plan,omitempty"`
}

// SourcedPart combines one demand with its provider outcome.
type SourcedPart struct {
	Demand         Demand  `json:"demand"`
	Status         string  `json:"status"`
	Offer          *Offer  `json:"offer,omitempty"`
	Offers         []Offer `json:"offers,omitempty"`
	CandidateCount int     `json:"candidate_count"`
	IssueCode      string  `json:"issue_code,omitempty"`
	IssueMessage   string  `json:"issue_message,omitempty"`
}

// PricingSummary contains only safely selected, common-currency totals.
type PricingSummary struct {
	LineCount          int            `json:"line_count"`
	PricedCount        int            `json:"priced_count"`
	ShortageCount      int            `json:"shortage_count"`
	ReviewCount        int            `json:"review_count"`
	NotFoundCount      int            `json:"not_found_count"`
	NotApplicableCount int            `json:"not_applicable_count"`
	ProviderErrors     int            `json:"provider_error_count"`
	Currency           string         `json:"currency,omitempty"`
	TotalCost          *money.Decimal `json:"total_cost,omitempty"`
	CostPerUnit        *money.Decimal `json:"cost_per_unit,omitempty"`
}
