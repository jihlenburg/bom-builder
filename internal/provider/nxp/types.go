// Package nxp implements conservative browser-backed NXP direct-store sourcing.
package nxp

import "encoding/json"

// SearchResult is one normalized NXP public-store search row.
type SearchResult struct {
	Query              string
	PartID             string
	Description        string
	BuyDirect          bool
	OrderActions       []string
	UnitPrice          json.Number
	Currency           string
	StockQuantity      *int
	Availability       string
	PackingName        string
	PackingDescription string
	StepPrices         []StepPrice
	PackageQualityURL  string
	ProductURL         string
}

// StepPrice is one structured NXP direct-store quantity tier.
type StepPrice struct {
	Quantity int
	Price    json.Number
}

// PartDetail is MOQ/package enrichment from the selected NXP family page.
type PartDetail struct {
	Query                  string
	MatchedPartID          string
	MinimumOrderQuantity   *int
	MinimumPackageQuantity *int
}
