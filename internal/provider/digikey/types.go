package digikey

import "encoding/json"

// Locale controls Digi-Key market, language, currency, and ship-to context.
type Locale struct {
	Site          string
	Language      string
	Currency      string
	ShipToCountry string
}

// PricingResult is the subset of PricingOptionsByQuantity used by sourcing.
type PricingResult struct {
	RequestedProduct       string
	RequestedQuantity      int
	ManufacturerName       string
	ManufacturerPartNumber string
	ProductURL             string
	Currency               string
	HeaderMode             string
	RateLimitRemaining     *int
	MyPricingOptions       []PricingOption
	StandardPricingOptions []PricingOption
}

// PricingOption is one exact, MOQ, maximum, or better-value option.
type PricingOption struct {
	Name              string
	TotalQuantity     int
	TotalPrice        json.Number
	QuantityAvailable *int
	Products          []PricingProduct
}

// PricingProduct preserves every concrete SKU leg in an option.
type PricingProduct struct {
	ProductNumber        string
	Quantity             int
	MinimumOrderQuantity int
	UnitPrice            json.Number
	ExtendedPrice        json.Number
	PackageType          string
	Marketplace          bool
}

type pricingResponse struct {
	RequestedProduct       string             `json:"RequestedProduct"`
	RequestedQuantity      int                `json:"RequestedQuantity"`
	ProductURL             string             `json:"ProductUrl"`
	ManufacturerPartNumber string             `json:"ManufacturerPartNumber"`
	Manufacturer           namedValue         `json:"Manufacturer"`
	SettingsUsed           settingsUsed       `json:"SettingsUsed"`
	MyPricingOptions       []rawPricingOption `json:"MyPricingOptions"`
	StandardPricingOptions []rawPricingOption `json:"StandardPricingOptions"`
}

type namedValue struct {
	Name string `json:"Name"`
}

type settingsUsed struct {
	SearchLocaleUsed localeUsed `json:"SearchLocaleUsed"`
}

type localeUsed struct {
	Currency string `json:"Currency"`
}

type rawPricingOption struct {
	Name              string              `json:"PricingOption"`
	TotalQuantity     int                 `json:"TotalQuantityPriced"`
	TotalPrice        json.Number         `json:"TotalPrice"`
	QuantityAvailable *int                `json:"QuantityAvailable"`
	Products          []rawPricingProduct `json:"Products"`
}

type rawPricingProduct struct {
	ProductNumber        string      `json:"DigiKeyProductNumber"`
	Quantity             int         `json:"QuantityPriced"`
	MinimumOrderQuantity int         `json:"MinimumOrderQuantity"`
	UnitPrice            json.Number `json:"UnitPrice"`
	ExtendedPrice        json.Number `json:"ExtendedPrice"`
	PackageType          namedValue  `json:"PackageType"`
	Marketplace          bool        `json:"Marketplace"`
}

type productDetailsResponse struct {
	Product struct {
		DatasheetURL      string `json:"DatasheetUrl"`
		ProductURL        string `json:"ProductUrl"`
		QuantityAvailable *int   `json:"QuantityAvailable"`
		ProductVariations []struct {
			DigiKeyProductNumber            string `json:"DigiKeyProductNumber"`
			QuantityAvailableforPackageType *int   `json:"QuantityAvailableforPackageType"`
		} `json:"ProductVariations"`
	} `json:"Product"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// ProductInfo is the ProductDetails subset used by sourcing: document
// links plus the product and per-variation stock. Stock MUST come from
// this endpoint — pricingbyquantity reports QuantityAvailable 0 even
// for well-stocked parts (observed live 2026-07-30, TCAN1473CDRQ1:
// pricing said 0 while ProductDetails and the website said 2940).
type ProductInfo struct {
	DatasheetURL      string
	ProductURL        string
	QuantityAvailable *int
	// VariationQuantities maps a Digi-Key SKU to its package-type
	// stock. Absent SKUs mean UNKNOWN stock, never zero.
	VariationQuantities map[string]int
}
