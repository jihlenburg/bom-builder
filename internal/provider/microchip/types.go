// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package microchip sources availability and lifecycle evidence from
// Microchip's public Product API (Partner Data Exchange). The API is
// credential-free and returns factory-direct stock, lead times, and
// lifecycle status, but NO pricing — results are therefore evidence
// for engineering review and can never become a selected purchase plan.
package microchip

import "fmt"

// DefaultProductsURL is Microchip's documented public Product API endpoint.
const DefaultProductsURL = "https://www.microchip.com/designresources/product-catalog/api/productInfo"

// Error is a categorized Microchip Product API failure.
type Error struct {
	Kind    string
	Message string
}

func (providerError *Error) Error() string {
	return fmt.Sprintf("microchip %s: %s", providerError.Kind, providerError.Message)
}

// Product is one normalized catalog record. Every numeric field arrives
// as a string from the API; unparseable values stay nil (unknown),
// never zero.
type Product struct {
	PartNumber           string
	Description          string
	Category             string
	InStockQuantity      *int
	LeadTimeWeeks        *int
	LifecycleStatus      string
	MinimumOrderQuantity *int
	OrderMultiple        *int
	PackagingType        string
	DatasheetURL         string
}

type productResponse struct {
	Data         []rawProduct `json:"data"`
	PageNumber   int          `json:"pagenumber"`
	PageSize     int          `json:"pagesize"`
	TotalPages   int          `json:"totalPages"`
	TotalRecords int          `json:"totalRecords"`
}

type rawProduct struct {
	PartNumber           string `json:"part_number"`
	Description          string `json:"description"`
	ComponentType        string `json:"component_type"`
	InStockQuantity      string `json:"instock_quantity"`
	LeadTimeWeeks        string `json:"lead_time_weeks"`
	LifecycleStatus      string `json:"lifecycle_status"`
	MinimumOrderQuantity string `json:"minimum_order_quantity"`
	OrderMultiple        string `json:"order_multiple"`
	PackagingType        string `json:"packaging_type"`
	DatasheetURL         string `json:"datasheet_url"`
}
