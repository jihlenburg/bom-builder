// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnvLoadsValuesWithoutEvaluation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(
		path,
		[]byte("BOM_BUILDER_TEST_VALUE=\"hello world\"\nBOM_BUILDER_LITERAL='$(nope)'\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Unsetenv("BOM_BUILDER_TEST_VALUE")
		os.Unsetenv("BOM_BUILDER_LITERAL")
	})
	os.Unsetenv("BOM_BUILDER_TEST_VALUE")
	os.Unsetenv("BOM_BUILDER_LITERAL")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("BOM_BUILDER_TEST_VALUE"); got != "hello world" {
		t.Fatalf("BOM_BUILDER_TEST_VALUE = %q", got)
	}
	if got := os.Getenv("BOM_BUILDER_LITERAL"); got != "$(nope)" {
		t.Fatalf("BOM_BUILDER_LITERAL = %q", got)
	}
}

func TestLoadDotEnvDoesNotOverrideInheritedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("BOM_BUILDER_TEST_PRECEDENCE=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOM_BUILDER_TEST_PRECEDENCE", "process")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("BOM_BUILDER_TEST_PRECEDENCE"); got != "process" {
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

func TestLoadDotEnvRefusesIntroducingRestrictedKeys(t *testing.T) {
	// A checkout-local .env may supply credentials and preferences, but it
	// must never introduce keys that redirect authenticated traffic to
	// another host, name an executable to launch, or relocate trusted
	// cache state: running the tool inside an untrusted checkout would
	// otherwise send real keys to an attacker's endpoint.
	for _, key := range []string{
		"BOM_BUILDER_MOUSER_API_URL",
		"BOM_BUILDER_DIGIKEY_API_BASE_URL",
		"BOM_BUILDER_DIGIKEY_TOKEN_URL",
		"BOM_BUILDER_TI_PRODUCTS_URL",
		"BOM_BUILDER_TI_TOKEN_URL",
		"BOM_BUILDER_CACHE_DB",
	} {
		path := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(path, []byte(key+"=https://collector.example/\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Unsetenv(key)
		err := LoadDotEnv(path)
		if err == nil {
			t.Fatalf("LoadDotEnv() introduced restricted key %s from .env", key)
		}
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("refusal for %s does not name the key: %v", key, err)
		}
		if value := os.Getenv(key); value != "" {
			t.Fatalf("%s was set to %q despite the refusal", key, value)
		}
	}
}

func TestLoadDotEnvIgnoresRestrictedKeysAlreadyInEnvironment(t *testing.T) {
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

func TestLoadDotEnvAcceptsPortableLowercaseKeys(t *testing.T) {
	// Lowercase keys are legal POSIX environment names and common in
	// real-world .env files; one unrelated `flag=1` line must not brick
	// every bom-builder command in that directory.
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("bom_builder_test_lowercase=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("bom_builder_test_lowercase") })
	os.Unsetenv("bom_builder_test_lowercase")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("bom_builder_test_lowercase"); got != "1" {
		t.Fatalf("lowercase value = %q", got)
	}
}

func TestLoadDotEnvStripsLeadingByteOrderMark(t *testing.T) {
	// Editors on Windows commonly write a UTF-8 BOM; without stripping it
	// the first key becomes "\uFEFFKEY" and fails with a baffling
	// invalid-key error.
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("\uFEFFBOM_BUILDER_TEST_BOM=ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("BOM_BUILDER_TEST_BOM") })
	os.Unsetenv("BOM_BUILDER_TEST_BOM")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv("BOM_BUILDER_TEST_BOM"); got != "ok" {
		t.Fatalf("value after BOM strip = %q", got)
	}
}
