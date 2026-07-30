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
		search := body["SearchByPartMfrNameRequest"]
		if search["mouserPartNumber"] != "RC0402FR-0710KL" ||
			search["partSearchOptions"] != "Exact" ||
			search["manufacturerName"] != "Yageo" {
			t.Errorf("unexpected request: %#v", body)
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
