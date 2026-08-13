// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, value)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func TestLoadDotEnvLoadsValuesWithoutEvaluation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(
		path,
		[]byte("MOUSER_API_KEY=\"hello world\"\nMOUSER_API_KEYS='$(nope)'\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	unsetEnvForTest(t, "MOUSER_API_KEY")
	unsetEnvForTest(t, "MOUSER_API_KEYS")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("MOUSER_API_KEY"); got != "hello world" {
		t.Fatalf("MOUSER_API_KEY = %q", got)
	}
	if got := os.Getenv("MOUSER_API_KEYS"); got != "$(nope)" {
		t.Fatalf("MOUSER_API_KEYS = %q", got)
	}
}

func TestLoadDotEnvDoesNotOverrideInheritedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MOUSER_API_KEY=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOUSER_API_KEY", "process")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("MOUSER_API_KEY"); got != "process" {
		t.Fatalf("precedence value = %q", got)
	}
}

func TestLoadDotEnvRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT AN ASSIGNMENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadDotEnv(path); err == nil {
		t.Fatal("LoadDotEnv() accepted malformed content")
	}
}

func TestLoadDotEnvIgnoresKeysOutsideAllowlist(t *testing.T) {
	// The allowlist must cover ambient settings consumed by Go itself, not
	// merely bom-builder's endpoint and path overrides. Otherwise a hostile
	// checkout could proxy authenticated requests or replace TLS roots.
	keys := []string{
		"HTTPS_PROXY",
		"SSL_CERT_FILE",
		"SSL_CERT_DIR",
		"BOM_BUILDER_MOUSER_API_URL",
		"BOM_BUILDER_DIGIKEY_API_BASE_URL",
		"BOM_BUILDER_DIGIKEY_TOKEN_URL",
		"BOM_BUILDER_TI_PRODUCTS_URL",
		"BOM_BUILDER_TI_TOKEN_URL",
		"BOM_BUILDER_CACHE_DB",
		"bom_builder_unrelated_lowercase",
	}
	var contents []byte
	for _, key := range keys {
		unsetEnvForTest(t, key)
		contents = append(contents, (key + "=https://collector.example/\n")...)
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			t.Fatalf("%s was set to %q despite not being allowlisted", key, value)
		}
	}
}

func TestLoadDotEnvPreservesUnsupportedKeysAlreadyInEnvironment(t *testing.T) {
	// When the operator already exported the override, the .env copy is
	// inert (inherited environment always wins), so loading must succeed.
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(
		path,
		[]byte("BOM_BUILDER_MOUSER_API_URL=https://collector.example/\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOM_BUILDER_MOUSER_API_URL", "https://real.example/")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("BOM_BUILDER_MOUSER_API_URL"); got != "https://real.example/" {
		t.Fatalf("override value = %q", got)
	}
}

func TestLoadDotEnvStripsLeadingByteOrderMark(t *testing.T) {
	// Editors on Windows commonly write a UTF-8 BOM; without stripping it
	// the first key becomes "\uFEFFKEY" and fails with a baffling
	// invalid-key error.
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("\uFEFFBOM_BUILDER_CACHE_TTL=24h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnvForTest(t, "BOM_BUILDER_CACHE_TTL")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("BOM_BUILDER_CACHE_TTL"); got != "24h" {
		t.Fatalf("value after BOM strip = %q", got)
	}
}
