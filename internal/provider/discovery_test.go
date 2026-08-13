// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

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
	t.Setenv("FARNELL_API_KEY", "farnell-secret")
	t.Setenv("FARNELL_STORE_ID", "de.farnell.com")
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
		"farnell-secret",
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
	farnell := discovery.Providers[3]
	if !farnell.Implemented || !farnell.Configured ||
		farnell.Status != "ready" ||
		farnell.Details.Implementation != "native_go" ||
		farnell.Details.Currency != "EUR" {
		t.Fatalf("Farnell should be advertised as ready with the store currency: %#v", farnell)
	}
	microchip := discovery.Providers[4]
	if !microchip.Implemented || microchip.Kind != "manufacturer" ||
		microchip.Status != "ready" {
		t.Fatalf("Microchip should be advertised as a ready manufacturer source: %#v", microchip)
	}
}

func TestDiscoverReportsFarnellUnconfiguredWithoutKey(t *testing.T) {
	// The store implies the price currency, so an unset key must read as
	// unconfigured rather than silently pricing against a default store.
	t.Setenv("FARNELL_API_KEY", "")
	t.Setenv("FARNELL_STORE_ID", "")
	t.Setenv("FARNELL_PRICE_CURRENCY", "")
	for _, capability := range Discover().Providers {
		if capability.Name != "farnell" {
			continue
		}
		if !capability.Implemented || capability.Configured ||
			capability.Status != "unconfigured" {
			t.Fatalf("unexpected unconfigured Farnell capability: %#v", capability)
		}
		return
	}
	t.Fatal("Farnell capability is missing from discovery")
}
