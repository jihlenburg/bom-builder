package mouser

import (
	"context"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/procurement"
)

type stubSearcher struct {
	exact []Part
	broad []Part
}

func (stub stubSearcher) Search(
	_ context.Context,
	_ string,
	_ string,
	exact bool,
) ([]Part, error) {
	if exact {
		return stub.exact, nil
	}
	return stub.broad, nil
}

func TestResolverBuildsExactStockVerifiedOffer(t *testing.T) {
	t.Parallel()
	resolver, err := NewResolver(stubSearcher{exact: []Part{pricedPart("5000")}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "priced" || result.Offer == nil || result.Offer.SelectedPlan == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Offer.SelectedPlan.PurchasedQuantity != 1000 ||
		result.Offer.SelectedPlan.ExtendedPrice.String() != "90.000000" ||
		!result.Offer.SelectedPlan.StockVerified {
		t.Fatalf("unsafe plan: %#v", result.Offer.SelectedPlan)
	}
	if result.Offer.DatasheetURL == "" || result.Offer.ProductURL == "" {
		t.Fatalf("document links missing: %#v", result.Offer)
	}
}

func TestResolverDoesNotSelectInsufficientStock(t *testing.T) {
	t.Parallel()
	resolver, _ := NewResolver(stubSearcher{exact: []Part{pricedPart("949")}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || result.Offer.SelectedPlan != nil ||
		result.IssueCode != "INSUFFICIENT_STOCK" {
		t.Fatalf("unexpected shortage result: %#v", result)
	}
}

func TestResolverKeepsLooseMatchReviewRequired(t *testing.T) {
	t.Parallel()
	candidate := pricedPart("5000")
	candidate.ManufacturerPartNumber = "RC0402FR-0710KL-T"
	resolver, _ := NewResolver(stubSearcher{broad: []Part{candidate}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "review" || !result.Offer.ReviewRequired ||
		result.Offer.SelectedPlan != nil || result.Offer.CandidatePlan == nil {
		t.Fatalf("loose match was treated as safe: %#v", result)
	}
}

func TestResolverLooseMatchKeepsShortageExplicit(t *testing.T) {
	t.Parallel()
	// A loose match that also has insufficient stock must surface the
	// blocking shortage, not collapse it into a bare "review": hiding a
	// stock state behind the review flag is exactly the implicit-state
	// collapse the engineering rules forbid. The offer's ReviewRequired
	// flag still records that the match was loose.
	candidate := pricedPart("949")
	candidate.ManufacturerPartNumber = "RC0402FR-0710KL-T"
	resolver, _ := NewResolver(stubSearcher{broad: []Part{candidate}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 950,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "shortage" || result.IssueCode != "INSUFFICIENT_STOCK" {
		t.Fatalf("shortage was masked by review status: %#v", result)
	}
	if result.Offer == nil || !result.Offer.ReviewRequired || result.Offer.SelectedPlan != nil {
		t.Fatalf("review flag or plan safety lost: %#v", result.Offer)
	}
}

func TestResolverFiltersWrongManufacturer(t *testing.T) {
	t.Parallel()
	wrong := pricedPart("5000")
	wrong.Manufacturer = "NXP Semiconductors"
	resolver, _ := NewResolver(stubSearcher{exact: []Part{wrong}})
	result, err := resolver.Lookup(context.Background(), procurement.Demand{
		PartNumber:       "RC0402FR-0710KL",
		Manufacturer:     "Yageo",
		RequiredQuantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "not_found" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func pricedPart(stock string) Part {
	return Part{
		AvailabilityInStock:    stock,
		Manufacturer:           "Yageo Corporation",
		ManufacturerPartNumber: "RC0402FR-0710KL",
		MouserPartNumber:       "603-RC0402FR-0710KL",
		DataSheetURL:           "https://example.test/datasheet.pdf",
		ProductDetailURL:       "https://example.test/product",
		Minimum:                "1",
		Multiple:               "1",
		PriceBreaks: []RawPriceBreak{
			{Quantity: 1, Price: "0,10 €", Currency: "EUR"},
			{Quantity: 1000, Price: "0,09 €", Currency: "EUR"},
		},
		ProductAttributes: []ProductAttribute{
			{Name: "Packaging", Value: "Cut Tape"},
			{Name: "Standard Pack Qty", Value: "5000"},
		},
	}
}
