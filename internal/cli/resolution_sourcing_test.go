// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jihlenburg/bom-builder/internal/resolutions"
)

func seedResolution(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolutions.sqlite3")
	store, err := resolutions.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if _, _, err := store.Approve(context.Background(), resolutions.Request{
		Manufacturer: "Yageo",
		PartNumber:   "OLD-PART-1",
		Replacement: resolutions.Replacement{
			Manufacturer: "Yageo",
			PartNumber:   "RC0402FR-0710KL",
		},
		ApprovedBy: "J. Ihlenburg",
		Note:       "approved replacement for the obsolete original",
	}, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	return path
}

func TestLookupConsumesActiveResolution(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	database := seedResolution(t)

	var stdout bytes.Buffer
	exitCode := Run(
		[]string{
			"lookup",
			"OLD-PART-1",
			"--manufacturer", "Yageo",
			"--quantity", "950",
			"--resolutions-db", database,
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	part := payload["parts"].([]any)[0].(map[string]any)
	if part["demand"].(map[string]any)["part_number"] != "OLD-PART-1" {
		t.Fatalf("the BOM line must keep its original identity: %#v", part["demand"])
	}
	resolution := part["resolution"].(map[string]any)
	if resolution["approved_by"] != "J. Ihlenburg" ||
		resolution["replacement_part_number"] != "RC0402FR-0710KL" ||
		resolution["original_part_number"] != "OLD-PART-1" ||
		resolution["review_lifted"] != false {
		t.Fatalf("unexpected resolution annotation: %#v", resolution)
	}
	offer := part["offer"].(map[string]any)
	if offer["manufacturer_part_number"] != "RC0402FR-0710KL" ||
		offer["selected_plan"].(map[string]any)["stock_verified"] != true {
		t.Fatalf("expected a safe plan for the approved replacement: %#v", offer)
	}
	if part["status"] != "priced" {
		t.Fatalf("expected priced status, got %#v", part["status"])
	}
}

func TestLookupIgnoreResolutionsFlagSkipsTheStore(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	database := seedResolution(t)

	var stdout bytes.Buffer
	exitCode := Run(
		[]string{
			"lookup",
			"OLD-PART-1",
			"--manufacturer", "Yageo",
			"--quantity", "950",
			"--resolutions-db", database,
			"--ignore-resolutions",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	part := payload["parts"].([]any)[0].(map[string]any)
	if _, present := part["resolution"]; present {
		t.Fatalf("--ignore-resolutions must skip the store: %#v", part)
	}
	// Without the redirect the fixture's exact part never matches the
	// obsolete original, so the run cannot be safely priced.
	if exitCode == 0 {
		t.Fatalf("expected a non-priced outcome without the resolution, got %s", stdout.String())
	}
}

func TestPriceConsumesActiveResolutionFromStdinDesign(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	database := seedResolution(t)

	design := `{"design":"legacy build","version":"1.0","parts":[
		{"part_number":"OLD-PART-1","manufacturer":"Yageo","quantity":2}]}`
	var stdout bytes.Buffer
	exitCode := Run(
		[]string{
			"price", "-",
			"--units", "10",
			"--resolutions-db", database,
		},
		strings.NewReader(design),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	part := payload["parts"].([]any)[0].(map[string]any)
	resolution, present := part["resolution"].(map[string]any)
	if !present || resolution["resolution_id"] == "" {
		t.Fatalf("price must annotate the applied resolution: %#v", part)
	}
	if part["demand"].(map[string]any)["part_number"] != "OLD-PART-1" {
		t.Fatalf("aggregated demand must keep the original identity: %#v", part["demand"])
	}
}
