package microchip

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func resolverForRecords(t *testing.T, records string) *Resolver {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, testCatalogJSON(records))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{ProductsURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(client)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func microchipDemand(required int) procurement.Demand {
	return procurement.Demand{
		PartNumber:       "dsPIC33AK512MPS506-E/PT",
		Manufacturer:     "Microchip",
		RequiredQuantity: required,
	}
}

func TestResolverSkipsNonMicrochipManufacturers(t *testing.T) {
	t.Parallel()
	resolver := resolverForRecords(t, catalogRecord)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "GCM188R71H104KA57J",
		Manufacturer:     "Murata",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_applicable" ||
		result.IssueCode != "PROVIDER_NOT_APPLICABLE" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverReturnsReviewEvidenceWithStock(t *testing.T) {
	t.Parallel()
	resolver := resolverForRecords(t, catalogRecord)
	result, err := resolver.Lookup(context.Background(), microchipDemand(10))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" ||
		result.IssueCode != "MANUFACTURER_EVIDENCE_ONLY" ||
		result.Offer == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	offer := result.Offer
	if !offer.ReviewRequired ||
		offer.SelectedPlan != nil ||
		offer.CandidatePlan != nil ||
		len(offer.PriceBreaks) != 0 {
		t.Fatalf("evidence offer must never carry pricing or plans: %#v", offer)
	}
	if offer.AvailableQuantity == nil || *offer.AvailableQuantity != 960 ||
		offer.LifecycleStatus != "REL" ||
		offer.LeadTime != "6 weeks" ||
		offer.OrderMultiple != 160 ||
		!strings.Contains(offer.ProductURL, "microchipdirect.com/product/") {
		t.Fatalf("evidence fields missing: %#v", offer)
	}
}

func TestResolverReportsShortageWithLeadTime(t *testing.T) {
	t.Parallel()
	resolver := resolverForRecords(t, catalogRecord)
	result, err := resolver.Lookup(context.Background(), microchipDemand(5000))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" ||
		result.IssueCode != "INSUFFICIENT_STOCK" ||
		!strings.Contains(result.IssueMessage, "960") ||
		!strings.Contains(result.IssueMessage, "lead 6 weeks") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverFlagsEndOfLifeBeforeStock(t *testing.T) {
	t.Parallel()
	record := strings.Replace(catalogRecord, `"lifecycle_status":"REL"`, `"lifecycle_status":"EOL"`, 1)
	resolver := resolverForRecords(t, record)
	result, err := resolver.Lookup(context.Background(), microchipDemand(10))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" ||
		result.IssueCode != "LIFECYCLE_WARNING" ||
		!strings.Contains(result.IssueMessage, "EOL") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverUnknownStockIsStockUnknown(t *testing.T) {
	t.Parallel()
	record := strings.Replace(catalogRecord, `"instock_quantity":"960"`, `"instock_quantity":"n/a"`, 1)
	resolver := resolverForRecords(t, record)
	result, err := resolver.Lookup(context.Background(), microchipDemand(10))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "stock_unknown" || result.IssueCode != "STOCK_UNKNOWN" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolverBasePartQueryWithoutExactMatchIsNotFound(t *testing.T) {
	t.Parallel()
	variants := catalogRecord + "," +
		strings.Replace(catalogRecord,
			`"part_number":"DSPIC33AK512MPS506-E/PT"`,
			`"part_number":"DSPIC33AK512MPS506-I/PT"`, 1)
	resolver := resolverForRecords(t, variants)
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "DSPIC33AK512MPS506",
		Manufacturer:     "Microchip Technology",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" ||
		result.IssueCode != "PART_NUMBER_MISMATCH" ||
		result.CandidateCount != 2 {
		t.Fatalf("base-part query must not silently pick a variant: %#v", result)
	}
}

func TestResolverFallsBackToBasePartQuery(t *testing.T) {
	t.Parallel()
	// The API answers only base-part queries in this scenario; the full
	// orderable query returns nothing.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.EqualFold(request.URL.Query().Get("part"), "DSPIC33AK512MPS506") {
			fmt.Fprint(writer, testCatalogJSON(catalogRecord))
			return
		}
		fmt.Fprint(writer, `{"data":[],"pagenumber":1,"pagesize":1000,"totalPages":0,"totalRecords":0}`)
	}))
	defer server.Close()
	client, _ := New(Config{ProductsURL: server.URL, HTTPClient: server.Client()})
	resolver, _ := NewResolver(client)
	result, err := resolver.Lookup(context.Background(), microchipDemand(10))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || result.Offer == nil ||
		result.Offer.ManufacturerPartNumber != "DSPIC33AK512MPS506-E/PT" {
		t.Fatalf("base-part fallback failed: %#v", result)
	}
}
