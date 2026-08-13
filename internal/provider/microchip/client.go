// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package microchip

import (
	"context"
	"encoding/json"
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
	maxResponseBytes = 8 * 1024 * 1024
	requestPageSize  = 1000
	maxPages         = 3
	minQueryLength   = 3
)

// Config controls one Microchip Product API client.
type Config struct {
	ProductsURL string
	HTTPClient  *http.Client
	MaxAttempts int
	Backoff     time.Duration
}

// Client queries the public Microchip Product API. No credentials exist,
// so the client carries only endpoint and transport configuration.
type Client struct {
	productsURL  string
	httpClient   *http.Client
	maxAttempts  int
	backoff      time.Duration
	requestCount atomic.Int64
}

// New validates configuration and constructs a client.
func New(configuration Config) (*Client, error) {
	productsURL := strings.TrimSpace(configuration.ProductsURL)
	if productsURL == "" {
		productsURL = DefaultProductsURL
	}
	parsed, err := url.Parse(productsURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" {
		return nil, &Error{Kind: "configuration", Message: "products URL must be an absolute HTTP(S) URL"}
	}
	httpClient := configuration.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	maxAttempts := configuration.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	backoff := configuration.Backoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	return &Client{
		productsURL: productsURL,
		httpClient:  httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}, nil
}

// NewFromEnvironment builds a client from process configuration. The
// endpoint override is process-environment only; it is outside the central
// checkout-local dotenv allowlist.
func NewFromEnvironment() (*Client, error) {
	return New(Config{
		ProductsURL: strings.TrimSpace(os.Getenv("BOM_BUILDER_MICROCHIP_PRODUCTS_URL")),
	})
}

// RequestCount reports how many HTTP requests this client has sent.
func (client *Client) RequestCount() int {
	return int(client.requestCount.Load())
}

// Products returns every catalog record matching the part query, which
// the API treats as a prefix/substring of at least three characters.
func (client *Client) Products(
	ctx context.Context,
	partQuery string,
) ([]Product, error) {
	partQuery = strings.TrimSpace(partQuery)
	if len(partQuery) < minQueryLength {
		return nil, &Error{
			Kind:    "input",
			Message: "part query must be at least three characters",
		}
	}
	var products []Product
	for page := 1; page <= maxPages; page++ {
		response, err := client.fetchPage(ctx, partQuery, page)
		if err != nil {
			return nil, err
		}
		for _, record := range response.Data {
			products = append(products, normalizeProduct(record))
		}
		if response.TotalPages <= page || len(response.Data) == 0 {
			return products, nil
		}
	}
	// More pages exist than the bounded budget allows; the caller sees
	// every record fetched so far, which always includes exact matches
	// for queries as specific as an orderable part number.
	return products, nil
}

func (client *Client) fetchPage(
	ctx context.Context,
	partQuery string,
	page int,
) (productResponse, error) {
	query := url.Values{}
	query.Set("part", partQuery)
	query.Set("pagesize", strconv.Itoa(requestPageSize))
	query.Set("pagenumber", strconv.Itoa(page))
	requestURL := client.productsURL + "?" + query.Encode()
	for attempt := 1; attempt <= client.maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return productResponse{}, &Error{Kind: "internal", Message: "could not create request"}
		}
		request.Header.Set("Accept", "application/json")
		client.requestCount.Add(1)
		response, transportErr := client.httpClient.Do(request)
		if transportErr != nil {
			if ctx.Err() != nil {
				return productResponse{}, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			if attempt == client.maxAttempts {
				return productResponse{}, &Error{Kind: "transport", Message: "request failed after retries"}
			}
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return productResponse{}, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		status := response.StatusCode
		response.Body.Close()
		if readErr != nil {
			return productResponse{}, &Error{Kind: "response", Message: "could not read response"}
		}
		if len(data) > maxResponseBytes {
			return productResponse{}, &Error{Kind: "response", Message: "response exceeded size limit"}
		}
		if status == http.StatusNotFound {
			return productResponse{}, nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			if attempt == client.maxAttempts {
				return productResponse{}, &Error{
					Kind:    "provider",
					Message: "Microchip product API returned status " + strconv.Itoa(status),
				}
			}
			if waitErr := waitForRetry(ctx, client.backoff, attempt); waitErr != nil {
				return productResponse{}, &Error{Kind: "timeout", Message: "request deadline exceeded"}
			}
			continue
		}
		if status < 200 || status >= 300 {
			return productResponse{}, &Error{
				Kind:    "provider",
				Message: "Microchip product API returned status " + strconv.Itoa(status),
			}
		}
		var decoded productResponse
		if err := json.Unmarshal(data, &decoded); err != nil {
			return productResponse{}, &Error{Kind: "response", Message: "product response was not valid JSON"}
		}
		return decoded, nil
	}
	return productResponse{}, &Error{Kind: "internal", Message: "retry loop exhausted"}
}

func normalizeProduct(record rawProduct) Product {
	return Product{
		PartNumber:           strings.TrimSpace(record.PartNumber),
		Description:          strings.TrimSpace(record.Description),
		Category:             strings.TrimSpace(record.ComponentType),
		InStockQuantity:      parseQuantity(record.InStockQuantity),
		LeadTimeWeeks:        parseQuantity(record.LeadTimeWeeks),
		LifecycleStatus:      strings.ToUpper(strings.TrimSpace(record.LifecycleStatus)),
		MinimumOrderQuantity: parseQuantity(record.MinimumOrderQuantity),
		OrderMultiple:        parseQuantity(record.OrderMultiple),
		PackagingType:        strings.TrimSpace(record.PackagingType),
		DatasheetURL:         strings.TrimSpace(record.DatasheetURL),
	}
}

// parseQuantity converts the API's stringly-typed non-negative integers.
// Anything unparseable is UNKNOWN (nil), never zero.
func parseQuantity(value string) *int {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func waitForRetry(ctx context.Context, backoff time.Duration, attempt int) error {
	timer := time.NewTimer(backoff * time.Duration(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
