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

func TestInteractiveRefusesToStartWithoutTerminal(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	exitCode := Run(
		[]string{
			"interactive",
			"--resolutions-db", filepath.Join(t.TempDir(), "resolutions.sqlite3"),
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != contract.ExitInput {
		t.Fatalf("exit = %d, output = %s", exitCode, stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("the refusal must be machine-readable JSON: %v (%s)", err, stdout.String())
	}
	if payload["errors"].([]any)[0].(map[string]any)["code"] != "INTERACTIVE_TTY_REQUIRED" {
		t.Fatalf("expected INTERACTIVE_TTY_REQUIRED, got %#v", payload)
	}
}

func TestInteractiveRejectsUnknownFlags(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	exitCode := Run(
		[]string{"interactive", "--bogus"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != contract.ExitInput ||
		!strings.Contains(stdout.String(), "--bogus") {
		t.Fatalf("expected unknown flags to be rejected: %s", stdout.String())
	}
}
