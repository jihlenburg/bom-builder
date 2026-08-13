// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package mouser

import "testing"

// 2026-08-05: "Würth Elektronik" (correct German spelling) must match
// Mouser's ASCII catalog name "Wurth Elektronik": the umlaut form
// produced "Found 0 matching manufacturers" and a stocked WE-XHMI
// inductor (74439358220, 323 at Mouser) read as a false shortage.
func TestManufacturersMatchFoldsDiacritics(t *testing.T) {
	t.Parallel()
	cases := [][2]string{
		{"Würth Elektronik", "Wurth Elektronik"},
		{"Würth Elektronik", "Wurth Electronics"},
		{"Würth Elektronik eiSos GmbH & Co. KG", "Wurth Elektronik"},
		{"TDK", "TDK"},
	}
	for _, pair := range cases {
		if !manufacturersMatch(pair[0], pair[1]) {
			t.Fatalf("no match: %q vs %q", pair[0], pair[1])
		}
	}
	if manufacturersMatch("Würth Elektronik", "Panasonic") {
		t.Fatal("folded matching became too permissive")
	}
}

func TestMouserManufacturerNameSendsASCII(t *testing.T) {
	t.Parallel()
	if got := mouserManufacturerName("Würth Elektronik"); got != "Wurth Elektronik" {
		t.Fatalf("API name still carries diacritics: %q", got)
	}
}
