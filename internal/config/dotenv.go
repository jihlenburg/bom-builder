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

// restrictedDotEnvKeys are configuration keys a checkout-local .env file may
// never introduce. They redirect authenticated provider traffic to another
// host, name an executable to launch, or relocate trusted cache state — so a
// hostile .env inside an untrusted checkout could exfiltrate real API keys
// from the inherited environment or execute arbitrary code. These overrides
// must come from the process environment, where only the operator sets them.
// A .env value for a key that is already exported is inert (the inherited
// environment always wins) and therefore tolerated.
var restrictedDotEnvKeys = map[string]bool{
	"BOM_BUILDER_MOUSER_API_URL":         true,
	"BOM_BUILDER_DIGIKEY_API_BASE_URL":   true,
	"BOM_BUILDER_DIGIKEY_TOKEN_URL":      true,
	"BOM_BUILDER_TI_PRODUCTS_URL":        true,
	"BOM_BUILDER_TI_TOKEN_URL":           true,
	"BOM_BUILDER_MICROCHIP_PRODUCTS_URL": true,
	"BOM_BUILDER_ECB_URL":                true,
	"BOM_BUILDER_CACHE_DB":               true,
	"BOM_BUILDER_RESOLUTIONS_DB":         true,
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
		if restrictedDotEnvKeys[key] {
			return fmt.Errorf(
				"dotenv line %d: %s must be set in the process environment, not %s"+
					" (a checkout-local file could redirect authenticated traffic,"+
					" choose the launched browser, or relocate the lookup cache)",
				lineNumber, key, path,
			)
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

// validKey accepts POSIX-portable environment names in either case: lowercase
// keys are legal and common in real-world .env files, and rejecting them
// would make one unrelated `flag=1` line brick every command run from that
// directory.
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
