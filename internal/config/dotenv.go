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
		key, value, ok, err := parseLine(scanner.Text())
		if err != nil {
			return fmt.Errorf("dotenv line %d: %w", lineNumber, err)
		}
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
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
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = value[1 : len(value)-1]
	} else if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", "", false, fmt.Errorf("invalid quoted value: %w", err)
		}
		value = unquoted
	}
	return key, value, true, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for index, character := range key {
		if character == '_' || character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
