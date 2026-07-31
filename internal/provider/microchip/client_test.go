package microchip

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testCatalogJSON(records string) string {
	count := strings.Count(records, "part_number")
	return fmt.Sprintf(
		`{"data":[%s],"pagenumber":1,"pagesize":1000,"totalPages":1,"totalRecords":%d}`,
		records,
		count,
	)
}

const catalogRecord = `{
	"part_number":"DSPIC33AK512MPS506-E/PT",
	"description":"200MHz, 512KB Flash",
	"component_type":"16-bit DSC",
	"instock_quantity":"960",
	"lead_time_weeks":"6",
	"lifecycle_status":"REL",
	"minimum_order_quantity":"1",
	"order_multiple":"160",
	"packaging_type":"TRAY",
	"datasheet_url":"https://ww1.microchip.com/datasheet.pdf"
}`

func TestProductsParsesStringlyTypedRecords(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("part") != "DSPIC33AK512MPS506-E/PT" {
			t.Errorf("unexpected part query %q", request.URL.Query().Get("part"))
		}
		fmt.Fprint(writer, testCatalogJSON(catalogRecord))
	}))
	defer server.Close()
	client, err := New(Config{ProductsURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	products, err := client.Products(context.Background(), "DSPIC33AK512MPS506-E/PT")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 {
		t.Fatalf("products = %#v", products)
	}
	product := products[0]
	if product.PartNumber != "DSPIC33AK512MPS506-E/PT" ||
		product.InStockQuantity == nil || *product.InStockQuantity != 960 ||
		product.LeadTimeWeeks == nil || *product.LeadTimeWeeks != 6 ||
		product.LifecycleStatus != "REL" ||
		product.MinimumOrderQuantity == nil || *product.MinimumOrderQuantity != 1 ||
		product.OrderMultiple == nil || *product.OrderMultiple != 160 ||
		product.PackagingType != "TRAY" {
		t.Fatalf("normalized product = %#v", product)
	}
}

func TestProductsUnparseableQuantityIsUnknownNeverZero(t *testing.T) {
	t.Parallel()
	record := strings.Replace(catalogRecord, `"instock_quantity":"960"`, `"instock_quantity":""`, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, testCatalogJSON(record))
	}))
	defer server.Close()
	client, _ := New(Config{ProductsURL: server.URL, HTTPClient: server.Client()})
	products, err := client.Products(context.Background(), "DSPIC33AK512MPS506")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || products[0].InStockQuantity != nil {
		t.Fatalf("empty stock string must stay unknown: %#v", products)
	}
}

func TestProductsRejectsShortQueries(t *testing.T) {
	t.Parallel()
	client, _ := New(Config{ProductsURL: "https://example.test"})
	_, err := client.Products(context.Background(), "ab")
	providerError, ok := err.(*Error)
	if !ok || providerError.Kind != "input" {
		t.Fatalf("short query error = %v", err)
	}
}

func TestProductsRetriesServerErrorsThenReportsProviderFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, _ := New(Config{
		ProductsURL: server.URL,
		HTTPClient:  server.Client(),
		MaxAttempts: 2,
		Backoff:     1,
	})
	_, err := client.Products(context.Background(), "DSPIC33AK512MPS506")
	providerError, ok := err.(*Error)
	if !ok || providerError.Kind != "provider" || calls.Load() != 2 {
		t.Fatalf("error = %v, calls = %d", err, calls.Load())
	}
	if client.RequestCount() != 2 {
		t.Fatalf("request count = %d", client.RequestCount())
	}
}

func TestProductsBoundsPagination(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("pagenumber")
		calls.Add(1)
		fmt.Fprintf(writer,
			`{"data":[%s],"pagenumber":%s,"pagesize":1000,"totalPages":99,"totalRecords":99000}`,
			catalogRecord, page)
	}))
	defer server.Close()
	client, _ := New(Config{ProductsURL: server.URL, HTTPClient: server.Client()})
	products, err := client.Products(context.Background(), "DSPIC33AK")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != maxPages || len(products) != maxPages {
		t.Fatalf("calls = %d, products = %d", calls.Load(), len(products))
	}
}
