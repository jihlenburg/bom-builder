// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/lookupcache"
)

func TestProviderSelectionExplicitAndExcludeSyntax(t *testing.T) {
	// Only Mouser and TI are configured in this environment.
	t.Setenv("MOUSER_API_KEYS", "secret")
	t.Setenv("MOUSER_API_KEY", "")
	t.Setenv("DIGIKEY_CLIENT_ID", "")
	t.Setenv("DIGIKEY_CLIENT_SECRET", "")
	t.Setenv("DIGIKEY_ACCOUNT_ID", "")
	t.Setenv("TI_STORE_API_KEY", "ti-key")
	t.Setenv("TI_STORE_API_SECRET", "ti-secret")

	cases := []struct {
		name       string
		value      string
		configured bool
		policy     lookupcache.Policy
		expected   string
		wantErr    string
	}{
		{
			name:     "default is the configured automatic set",
			expected: "[mouser ti]",
		},
		{
			name:  "explicit list is honored verbatim",
			value: "digikey,mouser", configured: true,
			expected: "[digikey mouser]",
		},
		{
			name:  "exclusion narrows the automatic set",
			value: "-ti", configured: true,
			expected: "[mouser]",
		},
		{
			name:  "auto with exclusion",
			value: "auto,-mouser", configured: true,
			expected: "[ti]",
		},
		{
			name:     "cache-only automatic set needs no credentials",
			policy:   lookupcache.PolicyOnly,
			expected: "[mouser digikey ti]",
		},
		{
			name:  "cache-only with exclusion",
			value: "-digikey", configured: true,
			policy:   lookupcache.PolicyOnly,
			expected: "[mouser ti]",
		},
		{
			name:  "exclusions with explicit names are rejected",
			value: "mouser,-ti", configured: true,
			wantErr: "leave the unwanted one out",
		},
		{
			name:  "auto mixed with explicit names is rejected",
			value: "auto,mouser", configured: true,
			wantErr: "auto cannot be combined",
		},
		{
			name:  "unknown provider is rejected",
			value: "farnell", configured: true,
			wantErr: "no native pricing adapter",
		},
		{
			name:  "unknown exclusion is rejected",
			value: "-nxp", configured: true,
			wantErr: "no native pricing adapter",
		},
		{
			name:  "excluding everything is rejected",
			value: "-mouser,-ti", configured: true,
			wantErr: "every automatic provider was excluded",
		},
	}
	for _, testCase := range cases {
		selected, err := resolveProviderSelection(
			testCase.value,
			testCase.configured,
			testCase.policy,
		)
		if testCase.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Errorf("%s: expected error containing %q, got %v",
					testCase.name, testCase.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", testCase.name, err)
			continue
		}
		if fmt.Sprint(selected) != testCase.expected {
			t.Errorf("%s: selected %v, expected %s",
				testCase.name, selected, testCase.expected)
		}
	}
}
