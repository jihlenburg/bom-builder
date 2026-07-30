package ti

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestClientUsesOAuthCurrencyAndReusesToken(t *testing.T) {
	t.Parallel()
	var (
		mu         sync.Mutex
		tokenCalls int
		apiCalls   int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/oauth/accesstoken":
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			if request.Form.Get("grant_type") != "client_credentials" ||
				request.Form.Get("client_id") != "client-id" ||
				request.Form.Get("client_secret") != "client-secret" {
				t.Errorf("unexpected token form: %v", request.Form)
			}
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":3599}`)
		default:
			mu.Lock()
			apiCalls++
			mu.Unlock()
			if request.Header.Get("Authorization") != "Bearer access-secret" ||
				request.URL.Query().Get("currency") != "EUR" {
				t.Errorf("unexpected product request: %#v %s", request.Header, request.URL)
			}
			writeProductResponse(writer, productFixture(
				"LP2982AIM5-3.3/NOPB",
				"LP2982",
				5000,
				`""`,
				"ACTIVE",
				"EUR",
			))
		}
	}))
	defer server.Close()

	client := testClient(t, server, "EUR")
	for range 2 {
		product, err := client.Product(
			context.Background(),
			"LP2982AIM5-3.3/NOPB",
		)
		if err != nil {
			t.Fatal(err)
		}
		if product.TIPartNumber != "LP2982AIM5-3.3/NOPB" ||
			product.QuantityAvailable == nil ||
			*product.QuantityAvailable != 5000 ||
			product.OrderLimit != nil ||
			len(product.Pricing) != 1 {
			t.Fatalf("unexpected product: %#v", product)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 1 || apiCalls != 2 || client.RequestCount() != 3 {
		t.Fatalf(
			"token=%d API=%d requests=%d",
			tokenCalls,
			apiCalls,
			client.RequestCount(),
		)
	}
}

func TestClientPreservesZeroStockAndNumericStringLimit(t *testing.T) {
	t.Parallel()
	server := tiServer(productFixture(
		"TMP421AQDCNRQ1",
		"TMP421-Q1",
		0,
		`"50"`,
		"ACTIVE",
		"USD",
	))
	defer server.Close()
	client := testClient(t, server, "USD")
	product, err := client.Product(context.Background(), "TMP421AQDCNRQ1")
	if err != nil {
		t.Fatal(err)
	}
	if product.QuantityAvailable == nil || *product.QuantityAvailable != 0 ||
		product.OrderLimit == nil || *product.OrderLimit != 50 {
		t.Fatalf("stock/limit lost: %#v", product)
	}
}

func TestClientErrorsRedactCredentialsAndToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth/accesstoken" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":3599}`)
			return
		}
		writer.WriteHeader(http.StatusForbidden)
		fmt.Fprint(
			writer,
			`{"message":"client-id client-secret access-secret"}`,
		)
	}))
	defer server.Close()
	client := testClient(t, server, "USD")
	_, err := client.Product(context.Background(), "TMP421AQDCNRQ1")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"client-id", "client-secret", "access-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked in %v", secret, err)
		}
	}
}

func TestClientClassifiesNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/oauth/accesstoken" {
			fmt.Fprint(writer, `{"access_token":"token","expires_in":3599}`)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
		fmt.Fprint(writer, `{"message":"not found"}`)
	}))
	defer server.Close()
	client := testClient(t, server, "USD")
	_, err := client.Product(context.Background(), "MISSING")
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.Kind != "not_found" {
		t.Fatalf("error = %#v", err)
	}
}

func testClient(t *testing.T, server *httptest.Server, currency string) *Client {
	t.Helper()
	client, err := New(Config{
		HTTPClient:   server.Client(),
		ProductsURL:  server.URL + "/v2/store/products",
		TokenURL:     server.URL + "/v1/oauth/accesstoken",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Currency:     currency,
		MaxAttempts:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func tiServer(payload string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/oauth/accesstoken" {
			fmt.Fprint(writer, `{"access_token":"access-secret","expires_in":3599}`)
			return
		}
		writeProductResponse(writer, payload)
	}))
}

func writeProductResponse(writer http.ResponseWriter, payload string) {
	fmt.Fprint(writer, payload)
}

func productFixture(
	tiPartNumber, genericPartNumber string,
	stock int,
	limitJSON, lifecycle, currency string,
) string {
	return fmt.Sprintf(`{
		"tiPartNumber":%q,
		"genericPartNumber":%q,
		"buyNowURL":"https://www.ti.com/product/test",
		"quantity":%d,
		"limit":%s,
		"description":"Test product",
		"minimumOrderQuantity":1,
		"standardPackQuantity":3000,
		"pinCount":8,
		"packageType":"SOT-23 (DBV)",
		"packageCarrier":"Large T&R",
		"customReel":true,
		"lifeCycle":%q,
		"pricing":[{
			"currency":%q,
			"priceBreaks":[
				{"priceBreakQuantity":1,"price":0.10},
				{"priceBreakQuantity":1000,"price":0.09}
			]
		}]
	}`, tiPartNumber, genericPartNumber, stock, limitJSON, lifecycle, currency)
}
