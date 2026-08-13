// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package farnell

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSearchUsesProductSearchContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("callInfo.apiKey") != "top-secret" {
			t.Errorf("unexpected API key")
		}
		if query.Get("callInfo.responseDataFormat") != "JSON" ||
			query.Get("storeInfo.id") != "de.farnell.com" ||
			query.Get("term") != "manuPartNum:RC0402FR-0710KL" ||
			query.Get("resultsSettings.responseGroup") != "medium" ||
			query.Get("resultsSettings.offset") != "0" ||
			query.Get("resultsSettings.numberOfResults") == "" {
			t.Errorf("unexpected query: %v", query)
		}
		fmt.Fprint(writer, `{"manufacturerPartNumberSearchReturn":{`+
			`"numberOfResults":1,"products":[{"sku":"9339060",`+
			`"translatedManufacturerPartNumber":"RC0402FR-0710KL",`+
			`"brandName":"Yageo"}]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "top-secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	products, err := client.Search(context.Background(), "RC0402FR-0710KL", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].BrandName != "Yageo" ||
		products[0].SKU != "9339060" {
		t.Fatalf("products = %#v", products)
	}
	if client.RequestCount() != 1 {
		t.Fatalf("request count = %d", client.RequestCount())
	}
}

func TestSearchBroadUsesKeywordTermAndWrapper(t *testing.T) {
	t.Parallel()
	// The wrapper object name differs by search type; a broad search must
	// send an any: term and decode the keywordSearchReturn envelope.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("term") != "any:RC0402FR-0710KL" {
			t.Errorf("unexpected term %q", request.URL.Query().Get("term"))
		}
		fmt.Fprint(writer, `{"keywordSearchReturn":{"numberOfResults":1,`+
			`"products":[{"sku":"9339060","brandName":"Yageo"}]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	products, err := client.Search(context.Background(), "RC0402FR-0710KL", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].SKU != "9339060" {
		t.Fatalf("products = %#v", products)
	}
}

func TestSearchPreservesPriceTextVerbatim(t *testing.T) {
	t.Parallel()
	// Farnell sends prices as bare JSON numbers. The decoded cost must
	// keep the exact response text so pricing never routes through a
	// binary float on its way into money.Parse.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"manufacturerPartNumberSearchReturn":{`+
			`"numberOfResults":1,"products":[{"sku":"9339060",`+
			`"prices":[{"from":1,"to":9,"cost":0.0123}]}]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	products, err := client.Search(context.Background(), "RC0402FR-0710KL", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || len(products[0].Prices) != 1 {
		t.Fatalf("products = %#v", products)
	}
	if string(products[0].Prices[0].Cost) != "0.0123" {
		t.Fatalf("cost text = %q, want 0.0123", products[0].Prices[0].Cost)
	}
}

func TestErrorsNeverContainAPIKeys(t *testing.T) {
	t.Parallel()
	// The API key travels in the query string, so net/http transport
	// errors would echo it inside the request URL.
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request to %s failed", request.URL)
	})
	client, err := New(Config{
		Endpoint:    "https://example.invalid/catalog/products",
		APIKey:      "do-not-leak",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "ABC123", true)
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestProviderErrorBodyCannotEchoAPIKey(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"Fault":{"Reason":{"Text":"bad key echoed-secret"}}}`)
	}))
	defer server.Close()
	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "echoed-secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "ABC123", true)
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "echoed-secret") ||
		!strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("provider response was not redacted: %v", err)
	}
	var providerError *Error
	if !errors.As(err, &providerError) ||
		providerError.Kind != "authentication" {
		t.Fatalf("unexpected error classification: %#v", err)
	}
}

func TestAuthenticationErrorsSurfaceGatewayDetail(t *testing.T) {
	t.Parallel()
	// The Mashery gateway fronting the API reports the actionable
	// failure cause ("Account Inactive", ERR_403_DEVELOPER_INACTIVE) in
	// response headers while the body is a bare HTML fragment; a generic
	// "Forbidden" would hide the only diagnosable detail.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Error-Detail-Header", "Account Inactive")
		writer.Header().Set("X-Mashery-Error-Code", "ERR_403_DEVELOPER_INACTIVE")
		writer.WriteHeader(http.StatusForbidden)
		fmt.Fprint(writer, "<h1>Developer Inactive</h1>")
	}))
	defer server.Close()
	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "ABC123", true)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "Account Inactive") ||
		!strings.Contains(err.Error(), "ERR_403_DEVELOPER_INACTIVE") {
		t.Fatalf("gateway detail was hidden: %v", err)
	}
}

func TestSearchBacksOffAndRetriesTemporaryRateLimit(t *testing.T) {
	t.Parallel()
	var (
		mu    sync.Mutex
		calls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(writer, `{"manufacturerPartNumberSearchReturn":{`+
			`"numberOfResults":1,"products":[{"sku":"9339060"}]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKey:      "secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 2,
		Backoff:     time.Millisecond,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	products, err := client.Search(context.Background(), "RC0402FR-0710KL", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 {
		t.Fatalf("products = %#v", products)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestSearchHonorsCancellation(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client, err := New(Config{
		Endpoint:    "https://example.invalid/catalog/products",
		APIKey:      "secret",
		StoreID:     "de.farnell.com",
		MaxAttempts: 1,
		HTTPClient:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Search(ctx, "ABC123", true)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	_, err := New(Config{StoreID: "de.farnell.com"})
	var providerError *Error
	if !errors.As(err, &providerError) ||
		providerError.Kind != "configuration" {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestNewDerivesCurrencyFromStore(t *testing.T) {
	t.Parallel()
	// Farnell responses carry no currency field; the price currency is
	// implied by the regional store the client queries.
	for store, currency := range map[string]string{
		"de.farnell.com":    "EUR",
		"uk.farnell.com":    "GBP",
		"www.newark.com":    "USD",
		"canada.newark.com": "CAD",
		"au.element14.com":  "AUD",
	} {
		client, err := New(Config{APIKey: "secret", StoreID: store})
		if err != nil {
			t.Fatalf("New(%q) error = %v", store, err)
		}
		if client.Currency() != currency {
			t.Errorf("Currency(%q) = %q, want %q", store, client.Currency(), currency)
		}
	}
}

func TestNewDefaultsToGermanStore(t *testing.T) {
	t.Parallel()
	client, err := New(Config{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if client.StoreID() != "de.farnell.com" || client.Currency() != "EUR" {
		t.Fatalf("store = %q currency = %q", client.StoreID(), client.Currency())
	}
}

func TestNewRejectsUnknownStoreWithoutExplicitCurrency(t *testing.T) {
	t.Parallel()
	// An unmapped store must fail closed instead of guessing a currency:
	// a wrongly labeled price is worse than no price.
	_, err := New(Config{APIKey: "secret", StoreID: "xx.example.com"})
	var providerError *Error
	if !errors.As(err, &providerError) ||
		providerError.Kind != "configuration" {
		t.Fatalf("unexpected error: %#v", err)
	}

	client, err := New(Config{
		APIKey:   "secret",
		StoreID:  "xx.example.com",
		Currency: "eur",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.Currency() != "EUR" {
		t.Fatalf("currency = %q, want EUR", client.Currency())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
