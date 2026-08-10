// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package lookupcache

import "testing"

func TestDigiKeyAdapterVersionExcludesLegacyZeroStockEntries(t *testing.T) {
	t.Parallel()
	if version := AdapterVersion("digikey"); version != "digikey-normalized-v2" {
		t.Fatalf("Digi-Key adapter version = %q, want zero-stock-safe v2", version)
	}
}
