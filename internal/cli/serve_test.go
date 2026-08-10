// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

func TestServeRefusesNonLoopbackListenAddresses(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, address := range []string{"0.0.0.0:8080", "192.168.1.10:8080", "bad"} {
		var stdout bytes.Buffer
		exitCode := Run(
			[]string{
				"serve",
				"--listen", address,
				"--resolutions-db", filepath.Join(t.TempDir(), "r.sqlite3"),
			},
			strings.NewReader(""),
			&stdout,
			&bytes.Buffer{},
		)
		if exitCode != contract.ExitInput {
			t.Fatalf("address %q: exit = %d, output = %s", address, exitCode, stdout.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
			t.Fatalf("address %q: invalid JSON: %v", address, err)
		}
		code := payload["errors"].([]any)[0].(map[string]any)["code"]
		if code != "LISTEN_NOT_LOOPBACK" && code != "INVALID_ARGUMENT" {
			t.Fatalf("address %q: unexpected code %v", address, code)
		}
	}
}

func TestServeRejectsUnknownFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	exitCode := Run(
		[]string{"serve", "--bogus"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != contract.ExitInput || !strings.Contains(stdout.String(), "--bogus") {
		t.Fatalf("expected unknown flags to be rejected: %s", stdout.String())
	}
}

func TestValidateLoopbackListenAcceptsLocalForms(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "localhost:8080", "[::1]:9000"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Fatalf("address %q must be accepted: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:80", "10.0.0.5:80", "example.com:80", ":8080"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Fatalf("address %q must be rejected", address)
		}
	}
}
