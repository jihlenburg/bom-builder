// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

const approvalRequestJSON = `{
  "manufacturer": "Texas Instruments",
  "part_number": "TMP421-Q1",
  "replacement": {
    "manufacturer": "Texas Instruments",
    "part_number": "TMP421AQDCNRQ1",
    "provider": "mouser",
    "provider_sku": "595-TMP421AQDCNRQ1"
  },
  "approved_by": "J. Ihlenburg",
  "note": "packaging variant cleared for Rev A",
  "source_documents": [
    {
      "url": "https://www.ti.com/lit/ds/symlink/tmp421.pdf",
      "sha256": "abababababababababababababababababababababababababababababababab"
    }
  ]
}`

func runResolutionsCommand(
	t *testing.T,
	stdin string,
	args ...string,
) (int, map[string]any) {
	t.Helper()
	var stdout bytes.Buffer
	exitCode := Run(args, strings.NewReader(stdin), &stdout, &bytes.Buffer{})
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v (%s)", err, stdout.String())
	}
	return exitCode, payload
}

func TestResolutionsApproveListHistoryRevokeLifecycle(t *testing.T) {
	t.Chdir(t.TempDir())
	database := filepath.Join(t.TempDir(), "resolutions.sqlite3")

	// Approve from stdin creates the database and the first active record.
	exitCode, payload := runResolutionsCommand(
		t,
		approvalRequestJSON,
		"resolutions", "approve", "-", "--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("approve exit = %d, payload = %#v", exitCode, payload)
	}
	resolution := payload["resolution"].(map[string]any)
	resolutionID := resolution["resolution_id"].(string)
	if resolution["status"] != "active" || resolution["approved_by"] != "J. Ihlenburg" {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
	if payload["superseded"] != nil {
		t.Fatalf("first approval must not supersede: %#v", payload)
	}
	status := payload["resolutions"].(map[string]any)
	if status["active_count"] != float64(1) || status["event_count"] != float64(1) {
		t.Fatalf("unexpected store status: %#v", status)
	}

	// A second approval for the same demand supersedes the first.
	exitCode, payload = runResolutionsCommand(
		t,
		strings.Replace(approvalRequestJSON, "595-TMP421AQDCNRQ1", "595-TMP421-ALT", 1),
		"resolutions", "approve", "-", "--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("second approve exit = %d", exitCode)
	}
	superseded := payload["superseded"].(map[string]any)
	if superseded["resolution_id"] != resolutionID || superseded["status"] != "superseded" {
		t.Fatalf("expected the first resolution superseded: %#v", superseded)
	}
	newResolutionID := payload["resolution"].(map[string]any)["resolution_id"].(string)

	// Active-only list returns one record; filters are case-insensitive.
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "list",
		"--manufacturer", "texas instruments",
		"--part", "tmp421-q1",
		"--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("list exit = %d", exitCode)
	}
	records := payload["records"].([]any)
	if len(records) != 1 ||
		records[0].(map[string]any)["resolution_id"] != newResolutionID {
		t.Fatalf("expected one active record, got %#v", records)
	}
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "list", "--include-inactive", "--resolutions-db", database,
	)
	if exitCode != contract.ExitOK || len(payload["records"].([]any)) != 2 {
		t.Fatalf("expected two records with --include-inactive: %#v", payload)
	}

	// History returns the audit trail, newest first.
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "history", "--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("history exit = %d", exitCode)
	}
	events := payload["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("expected three audit events, got %#v", events)
	}
	if events[0].(map[string]any)["action"] != "approved" ||
		events[1].(map[string]any)["action"] != "superseded" {
		t.Fatalf("unexpected event order: %#v", events)
	}

	// Revoke needs a preview token; the preview itself changes nothing.
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "revoke",
		"--id", newResolutionID,
		"--revoked-by", "J. Ihlenburg",
		"--reason", "design change",
		"--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("revoke preview exit = %d", exitCode)
	}
	revoke := payload["revoke"].(map[string]any)
	token, _ := revoke["apply_token"].(string)
	if revoke["applied"] != false || token == "" {
		t.Fatalf("expected unapplied preview with token: %#v", revoke)
	}

	// A wrong token is a stale preview, an exact token applies.
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "revoke",
		"--id", newResolutionID,
		"--revoked-by", "J. Ihlenburg",
		"--apply", "sha256:wrong",
		"--resolutions-db", database,
	)
	if exitCode != contract.ExitInput ||
		payload["errors"].([]any)[0].(map[string]any)["code"] != "RESOLUTION_PREVIEW_STALE" {
		t.Fatalf("expected RESOLUTION_PREVIEW_STALE, got %#v", payload)
	}
	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "revoke",
		"--id", newResolutionID,
		"--revoked-by", "J. Ihlenburg",
		"--reason", "design change",
		"--apply", token,
		"--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("revoke apply exit = %d, payload = %#v", exitCode, payload)
	}
	revoke = payload["revoke"].(map[string]any)
	if revoke["applied"] != true ||
		revoke["record"].(map[string]any)["status"] != "revoked" {
		t.Fatalf("expected applied revocation: %#v", revoke)
	}
	status = payload["resolutions"].(map[string]any)
	if status["active_count"] != float64(0) ||
		status["revoked_count"] != float64(1) ||
		status["superseded_count"] != float64(1) {
		t.Fatalf("unexpected final status: %#v", status)
	}
}

func TestResolutionsListWithoutDatabaseReportsAbsentStore(t *testing.T) {
	t.Chdir(t.TempDir())
	database := filepath.Join(t.TempDir(), "missing.sqlite3")
	exitCode, payload := runResolutionsCommand(
		t,
		"",
		"resolutions", "list", "--resolutions-db", database,
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("list exit = %d", exitCode)
	}
	status := payload["resolutions"].(map[string]any)
	if status["exists"] != false || len(payload["records"].([]any)) != 0 {
		t.Fatalf("expected an absent store with no records: %#v", payload)
	}
	if _, err := os.Lstat(database); !os.IsNotExist(err) {
		t.Fatal("a read-only command must not create the database")
	}
}

func TestResolutionsRevokeWithoutDatabaseIsInputError(t *testing.T) {
	t.Chdir(t.TempDir())
	exitCode, payload := runResolutionsCommand(
		t,
		"",
		"resolutions", "revoke",
		"--id", "abc123",
		"--revoked-by", "X",
		"--resolutions-db", filepath.Join(t.TempDir(), "missing.sqlite3"),
	)
	if exitCode != contract.ExitInput ||
		payload["errors"].([]any)[0].(map[string]any)["code"] != "RESOLUTION_NOT_FOUND" {
		t.Fatalf("expected RESOLUTION_NOT_FOUND, got %#v", payload)
	}
}

func TestResolutionsRejectsInvalidArgumentsAndInput(t *testing.T) {
	t.Chdir(t.TempDir())
	database := filepath.Join(t.TempDir(), "resolutions.sqlite3")

	exitCode, payload := runResolutionsCommand(t, "", "resolutions")
	if exitCode != contract.ExitInput || payload["status"] != "failed" {
		t.Fatalf("expected usage failure: %#v", payload)
	}

	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "list", "--bogus", "--resolutions-db", database,
	)
	if exitCode != contract.ExitInput ||
		!strings.Contains(
			payload["errors"].([]any)[0].(map[string]any)["message"].(string),
			"--bogus",
		) {
		t.Fatalf("expected unknown flags to be rejected: %#v", payload)
	}

	exitCode, payload = runResolutionsCommand(
		t,
		`{"manufacturer":"TI"}`,
		"resolutions", "approve", "-", "--resolutions-db", database,
	)
	if exitCode != contract.ExitInput ||
		payload["errors"].([]any)[0].(map[string]any)["code"] != "INVALID_INPUT" {
		t.Fatalf("expected INVALID_INPUT for an incomplete request: %#v", payload)
	}
	if _, err := os.Lstat(database); !os.IsNotExist(err) {
		t.Fatal("a rejected approval must not create the database")
	}

	exitCode, payload = runResolutionsCommand(
		t,
		"",
		"resolutions", "revoke", "--id", "abc123", "--resolutions-db", database,
	)
	if exitCode != contract.ExitInput ||
		payload["errors"].([]any)[0].(map[string]any)["code"] != "REVOKED_BY_REQUIRED" {
		t.Fatalf("expected REVOKED_BY_REQUIRED: %#v", payload)
	}
}

func TestSchemaResolutionsIsServed(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	exitCode := Run(
		[]string{"schema", "resolutions"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != contract.ExitOK {
		t.Fatalf("schema exit = %d", exitCode)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if payload["title"] != "BOM Builder resolution approval request" {
		t.Fatalf("unexpected schema: %#v", payload["title"])
	}
}
