// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package farnell implements the element14 Product Search API adapter
// serving the Farnell (Europe), Newark (Americas), and element14 (APAC)
// storefronts.
package farnell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultEndpoint = "https://api.element14.com/catalog/products"
	// DefaultStoreID is the regional storefront used when none is
	// configured. It quotes EUR, matching the default target currency.
	DefaultStoreID  = "de.farnell.com"
	maxResponseSize = 8 * 1024 * 1024
	// maxResults bounds broad keyword searches; manuPartNum lookups
	// rarely produce more than a handful of packaging variants.
	maxResults = 25
)

// storeCurrencies maps a regional storefront to the currency its prices
// are quoted in. The Product Search API returns bare numbers with no
// currency field, so this mapping is the only currency source. Only
// stores whose currency is unambiguous are listed; any other store must
// be configured with an explicit FARNELL_PRICE_CURRENCY or construction
// fails closed — a wrongly labeled price is worse than no price.
var storeCurrencies = map[string]string{
	// Farnell Europe: euro-area storefronts.
	"at.farnell.com":         "EUR",
	"be.farnell.com":         "EUR",
	"cpcireland.farnell.com": "EUR",
	"de.farnell.com":         "EUR",
	"ee.farnell.com":         "EUR",
	"es.farnell.com":         "EUR",
	"fi.farnell.com":         "EUR",
	"fr.farnell.com":         "EUR",
	"ie.farnell.com":         "EUR",
	"it.farnell.com":         "EUR",
	"lt.farnell.com":         "EUR",
	"lv.farnell.com":         "EUR",
	"nl.farnell.com":         "EUR",
	"pt.farnell.com":         "EUR",
	"si.farnell.com":         "EUR",
	"sk.farnell.com":         "EUR",
	// Farnell Europe: national-currency storefronts.
	"uk.farnell.com":      "GBP",
	"cpc.farnell.com":     "GBP",
	"onecall.farnell.com": "GBP",
	"ch.farnell.com":      "CHF",
	"cz.farnell.com":      "CZK",
	"dk.farnell.com":      "DKK",
	"hu.farnell.com":      "HUF",
	"no.farnell.com":      "NOK",
	"pl.farnell.com":      "PLN",
	"se.farnell.com":      "SEK",
	// Newark Americas.
	"www.newark.com":    "USD",
	"canada.newark.com": "CAD",
	// element14 Asia-Pacific.
	"au.element14.com": "AUD",
	"cn.element14.com": "CNY",
	"hk.element14.com": "HKD",
	"in.element14.com": "INR",
	"kr.element14.com": "KRW",
	"my.element14.com": "MYR",
	"nz.element14.com": "NZD",
	"ph.element14.com": "PHP",
	"sg.element14.com": "SGD",
	"th.element14.com": "THB",
	"tw.element14.com": "TWD",
}

// StoreCurrency reports the currency a known storefront quotes its
// prices in, or "" when the store is not in the built-in table and an
// explicit currency override is required.
func StoreCurrency(storeID string) string {
	return storeCurrencies[strings.ToLower(strings.TrimSpace(storeID))]
}

// Error is a sanitized provider failure with a stable kind.
type Error struct {
	Kind       string
	StatusCode int
	Message    string
}

func (providerError *Error) Error() string {
	if providerError == nil {
		return ""
	}
	if providerError.StatusCode != 0 {
		return "farnell " + providerError.Kind +
			" (HTTP " + strconv.Itoa(providerError.StatusCode) + "): " +
			providerError.Message
	}
	return "farnell " + providerError.Kind + ": " + providerError.Message
}

// Client is a bounded element14 Product Search API client for one
// regional store.
type Client struct {
	httpClient   *http.Client
	endpoint     string
	apiKey       string
	storeID      string
	currency     string
	maxAttempts  int
	backoff      time.Duration
	requestCount atomic.Int64
}

// Config allows tests and callers to provide transport policy explicitly.
type Config struct {
	HTTPClient  *http.Client
	Endpoint    string
	APIKey      string
	StoreID     string
	Currency    string
	MaxAttempts int
	Backoff     time.Duration
}

// New validates and constructs a client.
func New(configuration Config) (*Client, error) {
	apiKey := strings.TrimSpace(configuration.APIKey)
	if apiKey == "" {
		return nil, &Error{Kind: "configuration", Message: "no API key is configured"}
	}
	endpoint := strings.TrimSpace(configuration.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return nil, &Error{Kind: "configuration", Message: "invalid API endpoint"}
	}
	storeID := strings.ToLower(strings.TrimSpace(configuration.StoreID))
	if storeID == "" {
		storeID = DefaultStoreID
	}
	currency := strings.ToUpper(strings.TrimSpace(configuration.Currency))
	if currency == "" {
		currency = storeCurrencies[storeID]
	}
	if !validCurrency(currency) {
		return nil, &Error{
			Kind: "configuration",
			Message: "store " + storeID + " has no known price currency; " +
				"set FARNELL_PRICE_CURRENCY to the store's ISO 4217 code",
		}
	}
	httpClient := configuration.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	maxAttempts := configuration.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 3
	}
	if maxAttempts < 1 || maxAttempts > 8 {
		return nil, &Error{Kind: "configuration", Message: "max attempts must be between 1 and 8"}
	}
	backoff := configuration.Backoff
	if backoff == 0 {
		backoff = 250 * time.Millisecond
	}
	if backoff < 0 || backoff > 30*time.Second {
		return nil, &Error{Kind: "configuration", Message: "invalid retry backoff"}
	}
	return &Client{
		httpClient:  httpClient,
		endpoint:    endpoint,
		apiKey:      apiKey,
		storeID:     storeID,
		currency:    currency,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}, nil
}

// NewFromEnvironment reads the credential and non-secret overrides. The
// endpoint override is process-environment only; `.env` files may not
// introduce it (enforced centrally by the dotenv loader).
func NewFromEnvironment() (*Client, error) {
	maxAttempts := 3
	if configured := strings.TrimSpace(os.Getenv("BOM_BUILDER_FARNELL_MAX_ATTEMPTS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil {
			return nil, &Error{
				Kind:    "configuration",
				Message: "BOM_BUILDER_FARNELL_MAX_ATTEMPTS must be an integer",
			}
		}
		maxAttempts = parsed
	}
	return New(Config{
		Endpoint:    strings.TrimSpace(os.Getenv("BOM_BUILDER_FARNELL_API_URL")),
		APIKey:      os.Getenv("FARNELL_API_KEY"),
		StoreID:     os.Getenv("FARNELL_STORE_ID"),
		Currency:    os.Getenv("FARNELL_PRICE_CURRENCY"),
		MaxAttempts: maxAttempts,
	})
}

// RequestCount returns the number of live HTTP requests made by this client.
func (client *Client) RequestCount() int {
	return int(client.requestCount.Load())
}

// Currency reports the store-implied (or explicitly configured) currency
// every price in this client's responses is quoted in.
func (client *Client) Currency() string {
	return client.currency
}

// StoreID reports the regional storefront this client queries.
func (client *Client) StoreID() string {
	return client.storeID
}

// Search queries the catalog. An exact search uses the manuPartNum: term
// (manufacturer-part-number index); a broad search uses the any: keyword
// index and is expected to feed a review-gated candidate pass only.
func (client *Client) Search(
	ctx context.Context,
	partNumber string,
	exact bool,
) ([]Product, error) {
	partNumber = strings.TrimSpace(partNumber)
	if len(partNumber) < 3 || len(partNumber) > 40 {
		return nil, &Error{
			Kind:    "input",
			Message: "part number must contain between 3 and 40 characters",
		}
	}
	term := "any:" + partNumber
	if exact {
		term = "manuPartNum:" + partNumber
	}

	attempt := 0
	for {
		attempt++
		response, requestErr := client.do(ctx, term)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			if attempt >= client.maxAttempts {
				return nil, &Error{Kind: "transport", Message: "request failed after retries"}
			}
			if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
				return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		data, readErr := readBoundedBody(response)
		status := response.StatusCode
		header := response.Header
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		switch {
		case status >= 200 && status < 300:
			return decodeSearchResponse(data)
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return nil, &Error{
				Kind:       "authentication",
				StatusCode: status,
				Message:    client.sanitizeMessage(faultMessage(data, header, status)),
			}
		case status == http.StatusTooManyRequests:
			if attempt < client.maxAttempts {
				if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
					return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
				}
				continue
			}
			return nil, &Error{
				Kind:       "rate_limit",
				StatusCode: status,
				Message:    client.sanitizeMessage(faultMessage(data, header, status)),
			}
		case status >= 500:
			if attempt < client.maxAttempts {
				if err := waitForRetry(ctx, client.backoff, attempt); err != nil {
					return nil, &Error{Kind: "timeout", Message: "request deadline exceeded"}
				}
				continue
			}
			return nil, &Error{
				Kind:       "response",
				StatusCode: status,
				Message:    client.sanitizeMessage(faultMessage(data, header, status)),
			}
		default:
			return nil, &Error{
				Kind:       "response",
				StatusCode: status,
				Message:    client.sanitizeMessage(faultMessage(data, header, status)),
			}
		}
	}
}

func (client *Client) do(ctx context.Context, term string) (*http.Response, error) {
	endpoint, err := url.Parse(client.endpoint)
	if err != nil {
		return nil, errors.New("invalid endpoint")
	}
	query := endpoint.Query()
	query.Set("callInfo.responseDataFormat", "JSON")
	query.Set("callInfo.apiKey", client.apiKey)
	query.Set("storeInfo.id", client.storeID)
	query.Set("term", term)
	query.Set("resultsSettings.offset", "0")
	query.Set("resultsSettings.numberOfResults", strconv.Itoa(maxResults))
	// The medium response group carries price breaks and stock detail
	// without the large group's image and related-product payload.
	query.Set("resultsSettings.responseGroup", "medium")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("could not create request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "bom-builder-go/3")
	client.requestCount.Add(1)
	response, err := client.httpClient.Do(request)
	if err != nil {
		// net/http errors commonly include the full request URL. Do not
		// return their text because the element14 API requires the API
		// key in the query string.
		return nil, errors.New("HTTP transport failed")
	}
	return response, nil
}

func readBoundedBody(response *http.Response) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return nil, &Error{Kind: "response", Message: "could not read response"}
	}
	if len(data) > maxResponseSize {
		return nil, &Error{Kind: "response", Message: "response exceeded size limit"}
	}
	return data, nil
}

func decodeSearchResponse(data []byte) ([]Product, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, &Error{Kind: "response", Message: "response was empty"}
	}
	var payload searchResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, &Error{Kind: "response", Message: "response was not valid JSON"}
	}
	for _, wrapper := range []*searchReturn{
		payload.ManufacturerPartNumberSearchReturn,
		payload.KeywordSearchReturn,
		payload.PremierFarnellPartNumberReturn,
	} {
		if wrapper != nil {
			return wrapper.Products, nil
		}
	}
	return nil, &Error{Kind: "response", Message: "search response had no recognized payload"}
}

// faultMessage extracts the API's human-readable failure text; callers
// must sanitize it before it reaches an error message.
func faultMessage(data []byte, header http.Header, status int) string {
	var fault faultResponse
	if err := json.Unmarshal(data, &fault); err == nil {
		if text := strings.TrimSpace(fault.Fault.Detail.SearchException.Description); text != "" {
			return text
		}
		if text := strings.TrimSpace(fault.Fault.Reason.Text); text != "" {
			return text
		}
	}
	// The Mashery gateway fronting the API reports the actionable cause
	// ("Account Inactive", ERR_403_DEVELOPER_INACTIVE) in headers while
	// the body is a bare HTML fragment.
	detail := strings.TrimSpace(header.Get("X-Error-Detail-Header"))
	code := strings.TrimSpace(header.Get("X-Mashery-Error-Code"))
	switch {
	case detail != "" && code != "":
		return detail + " (" + code + ")"
	case detail != "":
		return detail
	case code != "":
		return code
	}
	if text := http.StatusText(status); text != "" {
		return text
	}
	return "unexpected provider response"
}

func (client *Client) sanitizeMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "provider rejected the request"
	}
	message = strings.ReplaceAll(message, client.apiKey, "[REDACTED]")
	if len(message) > 300 {
		// The byte cut may split a multi-byte rune; scrub the torn
		// tail so JSON error fields never carry invalid UTF-8.
		message = strings.ToValidUTF8(message[:300], "")
	}
	return message
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, character := range currency {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func waitForRetry(ctx context.Context, base time.Duration, attempt int) error {
	delay := base
	for range max(attempt-1, 0) {
		if delay > 15*time.Second {
			delay = 30 * time.Second
			break
		}
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
