// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads local configuration without evaluating shell syntax.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// allowedDotEnvKeys are the complete capabilities granted to a
// checkout-local .env file. Keeping an allowlist is important because Go's
// default HTTP transport also consumes ambient variables such as HTTPS_PROXY
// and SSL_CERT_FILE: loading arbitrary names from an untrusted checkout could
// otherwise redirect authenticated provider traffic or replace its trust
// roots. Operator-only endpoint and path overrides remain available through
// the inherited process environment, which always wins below.
var allowedDotEnvKeys = map[string]bool{
	"MOUSER_API_KEY":                   true,
	"MOUSER_API_KEYS":                  true,
	"BOM_BUILDER_MOUSER_MAX_ATTEMPTS":  true,
	"DIGIKEY_CLIENT_ID":                true,
	"DIGIKEY_CLIENT_SECRET":            true,
	"DIGIKEY_ACCOUNT_ID":               true,
	"DIGIKEY_LOCALE_SITE":              true,
	"DIGIKEY_LOCALE_LANGUAGE":          true,
	"DIGIKEY_LOCALE_CURRENCY":          true,
	"DIGIKEY_LOCALE_SHIP_TO_COUNTRY":   true,
	"BOM_BUILDER_DIGIKEY_MAX_ATTEMPTS": true,
	"TI_STORE_API_KEY":                 true,
	"TI_STORE_API_SECRET":              true,
	"TI_STORE_PRICE_CURRENCY":          true,
	"BOM_BUILDER_TI_MAX_ATTEMPTS":      true,
	"FARNELL_API_KEY":                  true,
	"FARNELL_STORE_ID":                 true,
	"FARNELL_PRICE_CURRENCY":           true,
	"BOM_BUILDER_FARNELL_MAX_ATTEMPTS": true,
	"OPENAI_API_KEY":                   true,
	"BOM_BUILDER_CACHE_POLICY":         true,
	"BOM_BUILDER_CACHE_TTL":            true,
}

// LoadDotEnv adds values from path to the environment without overriding
// inherited variables. Missing files are intentionally ignored.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dotenv: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			// Editors on Windows commonly prepend a UTF-8 byte
			// order mark; without stripping it the first key would
			// fail as an invalid name.
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		key, value, ok, err := parseLine(line)
		if err != nil {
			return fmt.Errorf("dotenv line %d: %w", lineNumber, err)
		}
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if !allowedDotEnvKeys[key] {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set dotenv value: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read dotenv: %w", err)
	}
	return nil
}

func parseLine(line string) (string, string, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false, nil
	}
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	key, rawValue, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false, errors.New("expected KEY=VALUE")
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return "", "", false, fmt.Errorf("invalid environment key %q", key)
	}
	value := strings.TrimSpace(rawValue)
	switch {
	case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		value = value[1 : len(value)-1]
	case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", "", false, fmt.Errorf("invalid quoted value: %w", err)
		}
		value = unquoted
	case len(value) >= 1 && (value[0] == '"' || value[0] == '\''):
		// A value that opens a quote without closing it is almost
		// certainly a truncated or multiline attempt; keeping the
		// quote literally would silently corrupt the value.
		return "", "", false, errors.New("unbalanced quote in value")
	}
	return key, value, true, nil
}

// validKey accepts POSIX-portable environment names in either case. Unknown
// names are ignored by LoadDotEnv, so shared files can contain unrelated
// settings without expanding bom-builder's checkout-local capabilities.
func validKey(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		if character == '_' ||
			character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
