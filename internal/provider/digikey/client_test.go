package digikey

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

func TestClientUsesOAuthLocaleAndAccountHeadersAndReusesToken(t *testing.T) {
	t.Parallel()
	var (
		mu         sync.Mutex
		tokenCalls int
		apiCalls   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/oauth2/token":
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			if request.Form.Get("client_id") != "client-id" ||
				request.Form.Get("client_secret") != "client-secret" ||
				request.Form.Get("grant_type") != "client_credentials" {
				t.Errorf("unexpected token form: %v", request.Form)
			}
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600,"token_type":"Bearer"}`)
		default:
			mu.Lock()
			apiCalls++
			mu.Unlock()
			assertDigiKeyHeaders(t, request)
			writer.Header().Set("X-RateLimit-Remaining", "997")
			writePricingResponse(writer, "ECA-1VHG102")
		}
	}))
	defer server.Close()

	client := testClient(t, server)
	for _, quantity := range []int{100, 200} {
		result, err := client.PricingByQuantity(
			context.Background(),
			"ECA-1VHG102",
			quantity,
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Currency != "EUR" || result.HeaderMode != "account_id" ||
			result.RateLimitRemaining == nil || *result.RateLimitRemaining != 997 {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 1 || apiCalls != 2 || client.RequestCount() != 3 {
		t.Fatalf(
			"token calls=%d API calls=%d request count=%d",
			tokenCalls,
			apiCalls,
			client.RequestCount(),
		)
	}
}

func TestClientNormalizesLocaleAndRequiresCurrentAccountHeader(t *testing.T) {
	t.Parallel()
	client, err := New(Config{
		APIBaseURL:   "https://api.example.test",
		TokenURL:     "https://api.example.test/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AccountID:    "account-id",
		Locale: Locale{
			Site:          " fr ",
			Language:      "EN",
			Currency:      "eur",
			ShipToCountry: "FR",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.Locale() != (Locale{
		Site: "FR", Language: "en", Currency: "EUR", ShipToCountry: "fr",
	}) {
		t.Fatalf("locale = %#v", client.Locale())
	}

	_, err = New(Config{
		APIBaseURL:   "https://api.example.test",
		TokenURL:     "https://api.example.test/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Locale: Locale{
			Site: "DE", Language: "en", Currency: "EUR", ShipToCountry: "de",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "account ID is required") {
		t.Fatalf("missing two-legged account ID error = %v", err)
	}
}

func TestClientErrorsRedactCredentialsAndToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth2/token" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
			return
		}
		writer.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(writer, `{"detail":"client-id client-secret account-id access-secret"}`)
	}))
	defer server.Close()
	client := testClient(t, server)
	_, err := client.PricingByQuantity(context.Background(), "ABC123", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"client-id", "client-secret", "account-id", "access-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked in %v", secret, err)
		}
	}
}

func TestProductInformation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth2/token" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
			return
		}
		assertDigiKeyHeaders(t, request)
		fmt.Fprint(writer, `{"Product":{`+
			`"DatasheetUrl":"https://manufacturer.test/part.pdf",`+
			`"ProductUrl":"https://digikey.test/product",`+
			`"QuantityAvailable":2940,`+
			`"ProductVariations":[`+
			`{"DigiKeyProductNumber":"296-CT-ND",`+
			`"QuantityAvailableforPackageType":2940},`+
			`{"DigiKeyProductNumber":"296-TR-ND",`+
			`"QuantityAvailableforPackageType":0},`+
			`{"DigiKeyProductNumber":"296-NR-ND"}`+
			`]}}`)
	}))
	defer server.Close()
	client := testClient(t, server)
	info, err := client.ProductInformation(context.Background(), "P5555-ND")
	if err != nil {
		t.Fatal(err)
	}
	if info.DatasheetURL != "https://manufacturer.test/part.pdf" ||
		info.ProductURL != "https://digikey.test/product" ||
		info.QuantityAvailable == nil || *info.QuantityAvailable != 2940 {
		t.Fatalf("info = %#v", info)
	}
	if len(info.VariationQuantities) != 2 ||
		info.VariationQuantities["296-CT-ND"] != 2940 ||
		info.VariationQuantities["296-TR-ND"] != 0 {
		t.Fatalf("variation quantities = %#v", info.VariationQuantities)
	}
	if _, present := info.VariationQuantities["296-NR-ND"]; present {
		t.Fatal("variation without reported quantity must stay unknown")
	}
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(Config{
		HTTPClient:   server.Client(),
		APIBaseURL:   server.URL,
		TokenURL:     server.URL + "/v1/oauth2/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AccountID:    "account-id",
		Locale: Locale{
			Site: "DE", Language: "en", Currency: "EUR", ShipToCountry: "de",
		},
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertDigiKeyHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	expected := map[string]string{
		"Authorization":                  "Bearer access-secret",
		"X-Digikey-Client-Id":            "client-id",
		"X-Digikey-Account-Id":           "account-id",
		"X-Digikey-Locale-Site":          "DE",
		"X-Digikey-Locale-Language":      "en",
		"X-Digikey-Locale-Currency":      "EUR",
		"X-Digikey-Locale-Shiptocountry": "de",
	}
	for name, expectedValue := range expected {
		if request.Header.Get(name) != expectedValue {
			t.Errorf("%s = %q, want %q", name, request.Header.Get(name), expectedValue)
		}
	}
	if value := request.Header.Get("X-DIGIKEY-Customer-Id"); value != "" {
		t.Errorf("obsolete X-DIGIKEY-Customer-Id header = %q", value)
	}
}

// writePricingResponse mirrors the live endpoint, which reports
// QuantityAvailable 0 regardless of real stock (observed 2026-07-30);
// stock truth lives in the productdetails fixture instead.
func writePricingResponse(
	writer http.ResponseWriter,
	manufacturerPartNumber string,
) {
	fmt.Fprintf(writer, `{
		"RequestedProduct":"P5555-ND",
		"RequestedQuantity":100,
		"ManufacturerPartNumber":%q,
		"Manufacturer":{"Name":"Panasonic Electronic Components"},
		"ProductUrl":"https://digikey.test/base-product",
		"SettingsUsed":{"SearchLocaleUsed":{"Currency":"EUR"}},
		"MyPricingOptions":[],
		"StandardPricingOptions":[{
			"PricingOption":"Exact",
			"TotalQuantityPriced":100,
			"TotalPrice":69.8,
			"QuantityAvailable":0,
			"Products":[{
				"DigiKeyProductNumber":"P5555-ND",
				"QuantityPriced":100,
				"MinimumOrderQuantity":1,
				"UnitPrice":0.698,
				"ExtendedPrice":69.8,
				"PackageType":{"Name":"Bulk"}
			}]
		}]
	}`, manufacturerPartNumber)
}

func TestPricingResponseUsesJSONNumbers(t *testing.T) {
	t.Parallel()
	var response pricingResponse
	if err := decodeJSON([]byte(`{
		"StandardPricingOptions":[{
			"TotalPrice":69.8,
			"Products":[{"UnitPrice":0.698,"ExtendedPrice":69.8}]
		}]
	}`), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response.StandardPricingOptions[0].TotalPrice)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "69.8" {
		t.Fatalf("number = %s", encoded)
	}
}

func TestClientRefreshesTokenOnceAfterUnauthorized(t *testing.T) {
	t.Parallel()
	// A 401 on a pricing call means the token expired server-side: the
	// client must clear it, fetch a fresh one, and retry exactly once —
	// without burning a regular retry attempt.
	var (
		mu         sync.Mutex
		tokenCalls int
		apiCalls   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth2/token" {
			mu.Lock()
			tokenCalls++
			issued := tokenCalls
			mu.Unlock()
			fmt.Fprintf(writer, `{"access_token":"token-%d","expires_in":600}`, issued)
			return
		}
		mu.Lock()
		apiCalls++
		mu.Unlock()
		if request.Header.Get("Authorization") == "Bearer token-1" {
			writer.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(writer, `{}`)
			return
		}
		writePricingResponse(writer, "ECA-1VHG102")
	}))
	defer server.Close()

	client := testClient(t, server)
	result, err := client.PricingByQuantity(context.Background(), "ECA-1VHG102", 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Currency != "EUR" {
		t.Fatalf("unexpected result: %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 2 || apiCalls != 2 {
		t.Fatalf("token calls = %d, api calls = %d; want 2 and 2", tokenCalls, apiCalls)
	}
}

func TestClientRetriesRateLimitAndServerErrors(t *testing.T) {
	t.Parallel()
	// 429 and 5xx are transient: with attempts remaining the client backs
	// off and retries rather than failing the lookup.
	var (
		mu       sync.Mutex
		apiCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth2/token" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
			return
		}
		mu.Lock()
		apiCalls++
		call := apiCalls
		mu.Unlock()
		switch call {
		case 1:
			writer.WriteHeader(http.StatusTooManyRequests)
		case 2:
			writer.WriteHeader(http.StatusBadGateway)
		default:
			writePricingResponse(writer, "ECA-1VHG102")
		}
	}))
	defer server.Close()

	client, err := New(Config{
		HTTPClient:   server.Client(),
		APIBaseURL:   server.URL,
		TokenURL:     server.URL + "/v1/oauth2/token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AccountID:    "account-id",
		Locale: Locale{
			Site: "DE", Language: "en", Currency: "EUR", ShipToCountry: "de",
		},
		MaxAttempts: 3,
		Backoff:     time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PricingByQuantity(context.Background(), "ECA-1VHG102", 100); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if apiCalls != 3 {
		t.Fatalf("api calls = %d, want 3", apiCalls)
	}
}

func TestClientRejectsOversizedResponseBody(t *testing.T) {
	t.Parallel()
	// A hostile or broken endpoint returning an unbounded body must hit
	// the read cap with an explicit error, not exhaust memory or be
	// half-parsed.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth2/token" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":600}`)
			return
		}
		writer.Write([]byte(strings.Repeat("a", 8*1024*1024+2)))
	}))
	defer server.Close()

	client := testClient(t, server)
	_, err := client.PricingByQuantity(context.Background(), "ECA-1VHG102", 100)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected size-limit error, got %v", err)
	}
}

func TestSanitizeTruncatesAtRuneBoundary(t *testing.T) {
	t.Parallel()
	// Truncating at a byte offset can split a multi-byte rune and emit
	// invalid UTF-8 into JSON error fields.
	client := &Client{}
	long := strings.Repeat("a", 299) + "€ tail"
	sanitized := client.sanitize(long)
	if !utf8.ValidString(sanitized) {
		t.Fatalf("sanitized message is not valid UTF-8: %q", sanitized)
	}
	if len(sanitized) > 300 {
		t.Fatalf("sanitized length = %d", len(sanitized))
	}
}
