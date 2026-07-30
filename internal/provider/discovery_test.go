package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiscoverReportsPresenceWithoutCredentialValues(t *testing.T) {
	t.Setenv("MOUSER_API_KEYS", "secret-primary,secret-backup")
	t.Setenv("MOUSER_API_KEY", "")
	t.Setenv("DIGIKEY_CLIENT_ID", "client-id-secret")
	t.Setenv("DIGIKEY_CLIENT_SECRET", "client-secret")
	t.Setenv("DIGIKEY_ACCOUNT_ID", "account-id-secret")
	t.Setenv("TI_STORE_API_KEY", "ti-key-secret")
	t.Setenv("TI_STORE_API_SECRET", "ti-api-secret")
	t.Setenv("TI_STORE_PRICE_CURRENCY", "EUR")
	t.Setenv("OPENAI_API_KEY", "openai-secret")

	discovery := Discover()
	data, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{
		"secret-primary",
		"secret-backup",
		"client-id-secret",
		"client-secret",
		"account-id-secret",
		"ti-key-secret",
		"ti-api-secret",
		"openai-secret",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("provider discovery leaked %q", secret)
		}
	}

	mouser := discovery.Providers[0]
	if mouser.Details.CredentialCount == nil || *mouser.Details.CredentialCount != 2 {
		t.Fatalf("Mouser credential count = %#v", mouser.Details.CredentialCount)
	}
	if !mouser.Implemented || mouser.Status != "ready" {
		t.Fatalf("Mouser should be advertised as ready: %#v", mouser)
	}
	digiKey := discovery.Providers[1]
	if !digiKey.Implemented || !digiKey.Configured ||
		digiKey.Status != "ready" ||
		digiKey.Details.Implementation != "native_go" {
		t.Fatalf("Digi-Key should be advertised as ready: %#v", digiKey)
	}
	ti := discovery.Providers[2]
	if !ti.Implemented || !ti.Configured ||
		ti.Status != "ready" ||
		ti.Details.Implementation != "native_go" ||
		ti.Details.Currency != "EUR" {
		t.Fatalf("TI should be advertised as ready: %#v", ti)
	}
	nxp := discovery.Providers[3]
	if !nxp.Implemented ||
		nxp.Details.Implementation != "native_go_cdp" ||
		nxp.Details.AuthenticationRequired == nil ||
		*nxp.Details.AuthenticationRequired {
		t.Fatalf("NXP should be advertised as a native browser adapter: %#v", nxp)
	}
}
