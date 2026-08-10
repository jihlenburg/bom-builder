// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ti implements the Texas Instruments Store Inventory and Pricing API.
package ti

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// PriceBreak is one TI direct-store quantity tier.
type PriceBreak struct {
	Quantity int
	Price    json.Number
}

// PricingSchedule is one ISO-currency pricing schedule.
type PricingSchedule struct {
	Currency    string
	PriceBreaks []PriceBreak
}

// Product is the normalized subset of a TI Store product response used by sourcing.
type Product struct {
	Query                string
	TIPartNumber         string
	GenericPartNumber    string
	BuyNowURL            string
	QuantityAvailable    *int
	OrderLimit           *int
	Description          string
	MinimumOrderQuantity int
	StandardPackQuantity int
	PinCount             int
	PackageType          string
	PackageCarrier       string
	CustomReel           *bool
	LifeCycle            string
	Pricing              []PricingSchedule
}

type rawProduct struct {
	TIPartNumber         string             `json:"tiPartNumber"`
	GenericPartNumber    string             `json:"genericPartNumber"`
	BuyNowURL            string             `json:"buyNowURL"`
	Quantity             flexibleInt        `json:"quantity"`
	Limit                flexibleInt        `json:"limit"`
	Description          string             `json:"description"`
	MinimumOrderQuantity flexibleInt        `json:"minimumOrderQuantity"`
	StandardPackQuantity flexibleInt        `json:"standardPackQuantity"`
	PinCount             flexibleInt        `json:"pinCount"`
	PackageType          string             `json:"packageType"`
	PackageCarrier       string             `json:"packageCarrier"`
	CustomReel           *bool              `json:"customReel"`
	LifeCycle            string             `json:"lifeCycle"`
	Pricing              []rawPriceSchedule `json:"pricing"`
}

type rawPriceSchedule struct {
	Currency    string          `json:"currency"`
	PriceBreaks []rawPriceBreak `json:"priceBreaks"`
}

type rawPriceBreak struct {
	Quantity flexibleInt `json:"priceBreakQuantity"`
	Price    json.Number `json:"price"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// flexibleInt accepts TI's documented integer values and its blank limit value.
type flexibleInt struct {
	Value   int
	Present bool
}

func (value *flexibleInt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || bytes.Equal(data, []byte(`""`)) {
		*value = flexibleInt{}
		return nil
	}
	var text string
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return errors.New("invalid integer string")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			*value = flexibleInt{}
			return nil
		}
	} else {
		text = string(data)
	}
	parsed, err := strconv.ParseInt(text, 10, 32)
	if err != nil {
		return errors.New("invalid integer value")
	}
	value.Value = int(parsed)
	value.Present = true
	return nil
}
