package lookupcache

import "testing"

func TestDigiKeyAdapterVersionExcludesLegacyZeroStockEntries(t *testing.T) {
	t.Parallel()
	if version := AdapterVersion("digikey"); version != "digikey-normalized-v2" {
		t.Fatalf("Digi-Key adapter version = %q, want zero-stock-safe v2", version)
	}
}
