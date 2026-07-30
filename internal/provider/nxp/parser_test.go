package nxp

import (
	"encoding/json"
	"testing"
)

func TestSelectBestResultPrefersDirectPricedVariant(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"results":[
		{
			"summary":"part_id::<b>KW47B42ZB7AFTBR</b>|~~~|description_s::Test",
			"metaData":{
				"Order":["Buy Through Distributor"],
				"stock_quantity":0
			}
		},
		{
			"summary":"part_id::<b>KW47B42ZB7AFTBT</b>|~~~|description_s::Test",
			"metaData":{
				"Description":"KW47",
				"Order":["Buy Direct","Buy Through Distributor"],
				"Availability":"In Stock",
				"packing_name":"TRAY",
				"packing_desc":"TRAY-Tray, Bakeable",
				"stock_quantity":4310,
				"stepPrice":["1::130::6.60","26::120::6.10","100::110::5.59"],
				"unitPrice":6.60
			},
			"url":"/webapp/salesItem.jsp?partId=KW47B42ZB7AFTBT"
		}
	]}`)
	result, err := selectBestResult("KW47B42ZB7AFTB", payload, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil ||
		result.PartID != "KW47B42ZB7AFTBT" ||
		!result.BuyDirect ||
		result.Currency != "USD" ||
		result.StockQuantity == nil ||
		*result.StockQuantity != 4310 ||
		len(result.StepPrices) != 3 ||
		result.StepPrices[2].Quantity != 100 ||
		result.StepPrices[2].Price.String() != "5.59" ||
		result.ProductURL == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSelectBestResultFailsClosedOnSchemaChange(t *testing.T) {
	t.Parallel()
	for _, payload := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"results":[{"metaData":{"Order":["Buy Direct"]}}]}`),
	} {
		if _, err := selectBestResult("ABC123", payload, "USD"); err == nil {
			t.Fatalf("schema change accepted: %s", payload)
		}
	}
}

func TestParsePartDetailExtractsMOQAndPackageQuantity(t *testing.T) {
	t.Parallel()
	body := `
	KW47B42ZB7AFTB
	ACTIVE
	KW47B42ZB7AFTBT
	ACTIVE
	Packing: TRAY
	Min. Package Quantity: 260
	Min. Order Quantity: 1,300
	Lead Time: 26 weeks
	`
	detail := parsePartDetail("KW47B42ZB7AFTB", "KW47B42ZB7AFTBT", body)
	if detail == nil ||
		detail.MinimumOrderQuantity == nil ||
		*detail.MinimumOrderQuantity != 1300 ||
		detail.MinimumPackageQuantity == nil ||
		*detail.MinimumPackageQuantity != 260 {
		t.Fatalf("unexpected detail: %#v", detail)
	}
}

func TestStepPricesPreserveJSONDecimals(t *testing.T) {
	t.Parallel()
	result, err := selectBestResult("ABC123", []byte(`{"results":[{
		"metaData":{
			"part_id":"ABC123",
			"Order":["Buy Direct"],
			"stepPrice":["1::x::0.123456"]
		}
	}]}`), "USD")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.StepPrices[0].Price)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "0.123456" {
		t.Fatalf("price = %s", encoded)
	}
}
