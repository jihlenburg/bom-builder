// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package mouser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSearchSurvivesCatalogManufacturerRejection pins the 2026-08-13 defect:
// the partnumberandmanufacturer endpoint answered "Found 0 matching
// manufacturers" for NCJ3310AHN/0J under "NXP Semiconductors" — the very
// spelling its own catalog returns — which surfaced as a false not_found in a
// 250-unit costing run. The plain part-number search must be used instead, so
// a manufacturer-list rejection can no longer drop a stocked part.
func TestSearchSurvivesCatalogManufacturerRejection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if _, legacy := body["SearchByPartMfrNameRequest"]; legacy {
				// Reproduce the live failure the legacy shape provoked.
				fmt.Fprint(writer, `{"Errors":[{"Code":"NoManufacturersFound",`+
					`"Message":"Found 0 matching manufacturers."}],`+
					`"SearchResults":{"NumberOfResult":0,"Parts":[]}}`)
				return
			}
			search := body["SearchByPartRequest"]
			if search["mouserPartNumber"] != "NCJ3310AHN/0J" {
				t.Errorf("unexpected request: %#v", body)
			}
			fmt.Fprint(writer, `{"Errors":[],"SearchResults":{"NumberOfResult":1,`+
				`"Parts":[{"ManufacturerPartNumber":"NCJ3310AHN/0J",`+
				`"Manufacturer":"NXP Semiconductors",`+
				`"MouserPartNumber":"771-NCJ3310AHN/0J",`+
				`"AvailabilityInStock":"5848"}]}}`)
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
	parts, err := client.Search(
		context.Background(), "NCJ3310AHN/0J", "NXP", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("stocked part was dropped: parts = %#v", parts)
	}
	if parts[0].Manufacturer != "NXP Semiconductors" {
		t.Fatalf("manufacturer = %q", parts[0].Manufacturer)
	}
}
