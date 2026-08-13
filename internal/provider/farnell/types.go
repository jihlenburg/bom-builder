// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package farnell

import (
	"bytes"
	"encoding/json"
)

// Product is the subset of the element14 Product Search API contract used
// by BOM Builder. Raw provider JSON never leaves this package.
type Product struct {
	SKU                              string `json:"sku"`
	DisplayName                      string `json:"displayName"`
	BrandName                        string `json:"brandName"`
	VendorName                       string `json:"vendorName"`
	TranslatedManufacturerPartNumber string `json:"translatedManufacturerPartNumber"`
	// The API spells this field "Quality"; the sibling tag guards
	// against the spelling being corrected server-side one day.
	TranslatedMinimumOrderQuality  json.Number `json:"translatedMinimumOrderQuality"`
	TranslatedMinimumOrderQuantity json.Number `json:"translatedMinimumOrderQuantity"`
	PackSize                       json.Number `json:"packSize"`
	UnitOfMeasure                  string      `json:"unitOfMeasure"`
	ProductStatus                  string      `json:"productStatus"`
	Prices                         []RawPrice  `json:"prices"`
	Stock                          *Stock      `json:"stock"`
	Datasheets                     []Datasheet `json:"datasheets"`
}

// RawPrice is one quantity-banded price. The cost arrives as a bare JSON
// number with no currency field (the store implies the currency), and is
// decoded as json.Number so the exact response text reaches money.Parse
// without passing through a binary float.
type RawPrice struct {
	From int         `json:"from"`
	To   int         `json:"to"`
	Cost json.Number `json:"cost"`
}

// Stock reports warehouse availability. Level is a pointer because a
// missing level means UNKNOWN stock, which must stay distinct from a
// known zero.
type Stock struct {
	Level *int `json:"level"`
}

// UnmarshalJSON accepts both documented stock shapes: the current object
// form {"level": 1075, ...} and the legacy bare-number form 1075.
func (stock *Stock) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '{' {
		var object struct {
			Level *int `json:"level"`
		}
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		stock.Level = object.Level
		return nil
	}
	var level int
	if err := json.Unmarshal(data, &level); err != nil {
		return err
	}
	stock.Level = &level
	return nil
}

// Datasheet is one technical-document link attached to a product.
type Datasheet struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// searchResponse covers the three wrapper names the API uses; which one
// appears depends on the search-term prefix (any:, manuPartNum:, id:).
type searchResponse struct {
	KeywordSearchReturn                *searchReturn `json:"keywordSearchReturn"`
	ManufacturerPartNumberSearchReturn *searchReturn `json:"manufacturerPartNumberSearchReturn"`
	PremierFarnellPartNumberReturn     *searchReturn `json:"premierFarnellPartNumberReturn"`
}

type searchReturn struct {
	NumberOfResults int       `json:"numberOfResults"`
	Products        []Product `json:"products"`
}

// faultResponse is the API's error envelope. Only human-readable text is
// extracted; it is always sanitized before reaching an error message.
type faultResponse struct {
	Fault struct {
		Reason struct {
			Text string `json:"Text"`
		} `json:"Reason"`
		Detail struct {
			SearchException struct {
				ExceptionCode string `json:"exceptionCode"`
				Description   string `json:"description"`
			} `json:"searchException"`
		} `json:"Detail"`
	} `json:"Fault"`
}
