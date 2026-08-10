// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package mouser

// Part is the subset of Mouser's v2 product contract used by BOM Builder.
type Part struct {
	Availability           string                `json:"Availability"`
	AvailabilityInStock    string                `json:"AvailabilityInStock"`
	Category               string                `json:"Category"`
	DataSheetURL           string                `json:"DataSheetUrl"`
	Description            string                `json:"Description"`
	LeadTime               string                `json:"LeadTime"`
	LifecycleStatus        string                `json:"LifecycleStatus"`
	Manufacturer           string                `json:"Manufacturer"`
	ManufacturerPartNumber string                `json:"ManufacturerPartNumber"`
	Minimum                string                `json:"Min"`
	Multiple               string                `json:"Mult"`
	MouserPartNumber       string                `json:"MouserPartNumber"`
	PriceBreaks            []RawPriceBreak       `json:"PriceBreaks"`
	ProductAttributes      []ProductAttribute    `json:"ProductAttributes"`
	ProductDetailURL       string                `json:"ProductDetailUrl"`
	Reeling                bool                  `json:"Reeling"`
	ROHSStatus             string                `json:"ROHSStatus"`
	SuggestedReplacement   string                `json:"SuggestedReplacement"`
	RestrictionMessage     string                `json:"RestrictionMessage"`
	AvailableOnOrder       string                `json:"AvailableOnOrder"`
	AvailabilityOnOrder    []AvailabilityOnOrder `json:"AvailabilityOnOrder"`
}

// RawPriceBreak mirrors Mouser's localized price payload.
type RawPriceBreak struct {
	Quantity int    `json:"Quantity"`
	Price    string `json:"Price"`
	Currency string `json:"Currency"`
}

// ProductAttribute is one name/value item from Mouser.
type ProductAttribute struct {
	Name  string `json:"AttributeName"`
	Value string `json:"AttributeValue"`
	Cost  string `json:"AttributeCost"`
}

// AvailabilityOnOrder is one expected-stock event.
type AvailabilityOnOrder struct {
	Quantity int    `json:"Quantity"`
	Date     string `json:"Date"`
}
