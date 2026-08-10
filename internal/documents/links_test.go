// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package documents

import (
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func TestLinksFromOfferAndNormalizePreferManufacturerDatasheet(t *testing.T) {
	links := LinksFromOffer(procurement.Offer{
		Provider:               "digikey",
		ManufacturerPartNumber: "ABC123",
		DatasheetURL:           "https://mm.digikey.com/test.pdf#page=2",
		ProductURL:             "https://www.digikey.com/product",
	})
	links = append(links, contract.DocumentLink{
		Kind:                   "datasheet",
		Provider:               "mouser",
		URL:                    "https://manufacturer.example/abc123.pdf",
		ManufacturerPartNumber: "ABC123",
	})
	normalized := NormalizeLinks(links)
	if len(normalized) != 3 {
		t.Fatalf("links = %#v", normalized)
	}
	if normalized[0].URL != "https://manufacturer.example/abc123.pdf" ||
		!normalized[0].Preferred ||
		!normalized[0].Downloadable {
		t.Fatalf("wrong preferred document: %#v", normalized[0])
	}
	if normalized[1].Preferred {
		t.Fatalf("more than one preferred document: %#v", normalized)
	}
}

func TestNormalizeLinksRejectsCredentialsAndUnsupportedSchemes(t *testing.T) {
	normalized := NormalizeLinks([]contract.DocumentLink{
		{Kind: "datasheet", Provider: "mouser", URL: "file:///tmp/a.pdf"},
		{Kind: "datasheet", Provider: "mouser", URL: "https://user:secret@example.test/a.pdf"},
		{Kind: "datasheet", Provider: "mouser", URL: "https://example.test/a.pdf"},
	})
	if len(normalized) != 1 || normalized[0].URL != "https://example.test/a.pdf" {
		t.Fatalf("unexpected links: %#v", normalized)
	}
}
