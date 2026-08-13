// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package mouser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSearchUsesOfficialV2Contract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("apiKey") != "top-secret" {
			t.Errorf("unexpected API key")
		}
		var body map[string]map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		search := body["SearchByPartRequest"]
		if search["mouserPartNumber"] != "RC0402FR-0710KL" ||
			search["partSearchOptions"] != "Exact" {
			t.Errorf("unexpected request: %#v", body)
		}
		if _, sent := search["manufacturerName"]; sent {
			t.Errorf("manufacturerName must not reach the API: %#v", body)
		}
		fmt.Fprint(writer, `{"Errors":[],"SearchResults":{"NumberOfResult":1,"Parts":[`+
			`{"ManufacturerPartNumber":"RC0402FR-0710KL","Manufacturer":"Yageo"}`+
			`]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKeys:     []string{"top-secret"},
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := client.Search(context.Background(), "RC0402FR-0710KL", "Yageo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Manufacturer != "Yageo" {
		t.Fatalf("parts = %#v", parts)
	}
	if client.RequestCount() != 1 {
		t.Fatalf("request count = %d", client.RequestCount())
	}
}

func TestSearchRotatesKeyAfterDailyQuota(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		keys []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		key := request.URL.Query().Get("apiKey")
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
		if key == "primary-secret" {
			writer.WriteHeader(http.StatusForbidden)
			fmt.Fprint(writer, `{"Errors":[{"Code":"TooManyRequests",`+
				`"Message":"Exceeded requests per day"}]}`)
			return
		}
		fmt.Fprint(writer, `{"Errors":[],"SearchResults":{"Parts":[]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKeys:     []string{"primary-secret", "backup-secret"},
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Search(context.Background(), "ABC123", "Example", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(keys, ",") != "primary-secret,backup-secret" {
		t.Fatalf("keys = %#v", keys)
	}
}

func TestErrorsNeverContainAPIKeys(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request to %s failed", request.URL)
	})
	client, err := New(Config{
		Endpoint:    "https://example.invalid/search",
		APIKeys:     []string{"do-not-leak"},
		MaxAttempts: 1,
		HTTPClient:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "ABC123", "Example", true)
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
		fmt.Fprint(writer, `{"Errors":[{"Code":"Unauthorized",`+
			`"Message":"bad key echoed-secret"}]}`)
	}))
	defer server.Close()
	client, err := New(Config{
		Endpoint:    server.URL,
		APIKeys:     []string{"echoed-secret"},
		MaxAttempts: 1,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Search(context.Background(), "ABC123", "Example", true)
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), "echoed-secret") ||
		!strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("provider response was not redacted: %v", err)
	}
}

func TestSearchHonorsCancellation(t *testing.T) {
	t.Parallel()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client, err := New(Config{
		Endpoint:    "https://example.invalid/search",
		APIKeys:     []string{"secret"},
		MaxAttempts: 1,
		HTTPClient:  &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Search(ctx, "ABC123", "Example", true)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestSearchBacksOffAndRetriesTemporaryRateLimit(t *testing.T) {
	t.Parallel()
	// A 429 with no spare key must back off and retry within the attempt
	// budget instead of failing the lookup outright.
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
			fmt.Fprint(writer, `{"Errors":[{"Code":"TooManyRequests","Message":"slow down"}]}`)
			return
		}
		fmt.Fprint(writer, `{"Errors":[],"SearchResults":{"NumberOfResult":1,"Parts":[`+
			`{"ManufacturerPartNumber":"RC0402FR-0710KL","Manufacturer":"Yageo"}`+
			`]}}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Endpoint:    server.URL,
		APIKeys:     []string{"only-key"},
		MaxAttempts: 2,
		Backoff:     time.Millisecond,
		HTTPClient:  server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts, err := client.Search(context.Background(), "RC0402FR-0710KL", "Yageo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %#v", parts)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestSanitizeMessageTruncatesAtRuneBoundary(t *testing.T) {
	t.Parallel()
	// Truncating at a byte offset can split a multi-byte rune and emit
	// invalid UTF-8 into JSON error fields.
	client := &Client{}
	long := strings.Repeat("a", 299) + "€ tail"
	sanitized := client.sanitizeMessage(long)
	if !utf8.ValidString(sanitized) {
		t.Fatalf("sanitized message is not valid UTF-8: %q", sanitized)
	}
	if len(sanitized) > 300 {
		t.Fatalf("sanitized length = %d", len(sanitized))
	}
}
