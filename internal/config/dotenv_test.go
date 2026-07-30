package config

import (
	"os"
	"path/filepath"
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
