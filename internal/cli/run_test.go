// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

func TestMain(testingMain *testing.M) {
	_ = os.Setenv("BOM_BUILDER_CACHE_POLICY", "off")
	// Isolate every test from a developer's real resolutions database:
	// point the default at a path that never exists unless a test
	// explicitly overrides it.
	isolated, err := os.MkdirTemp("", "bom-builder-cli-test-")
	if err == nil {
		defer os.RemoveAll(isolated)
		_ = os.Setenv(
			"BOM_BUILDER_RESOLUTIONS_DB",
			filepath.Join(isolated, "absent-resolutions.sqlite3"),
		)
	}
	os.Exit(testingMain.Run())
}

func TestCapabilitiesFullReturnsOneCompleteDiscoveryDocument(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"capabilities", "--full"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if payload["schema_version"] != "2.0" {
		t.Fatalf("schema version = %#v", payload["schema_version"])
	}
	if payload["provider_configuration"] == nil || payload["schemas"] == nil {
		t.Fatalf("full discovery omitted fields: %s", stdout.String())
	}
	features := payload["features"].(map[string]any)
	if features["native_go_binary"] != true ||
		features["pricing"] != true ||
		features["lookup"] != true ||
		features["datasheet_downloads"] != true ||
		features["live_provider_health"] != true {
		t.Fatalf("unexpected features: %#v", features)
	}
}

func TestHelpIsConciseAndDoesNotDependOnConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.WriteFile(
		filepath.Join(directory, ".env"),
		[]byte("not valid dotenv syntax\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"documents", "list", "--help"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 ||
		!strings.Contains(stdout.String(), "bom-builder documents list <mpn>") ||
		!strings.Contains(stdout.String(), "--providers <auto|list>") {
		t.Fatalf("exit code = %d, help = %q", exitCode, stdout.String())
	}
}

func TestUnknownFlagsAreRejectedNotTreatedAsPositionals(t *testing.T) {
	// `lookup --stock-verify --manufacturer Yageo` used to pass the length
	// gate and query providers for the literal MPN "--stock-verify",
	// burning a provider request and reporting not_found instead of a
	// usage error. Every positional consumer must reject flag-shaped
	// leftovers the way price and export already do.
	t.Chdir(t.TempDir())
	cases := map[string][]string{
		"lookup":         {"lookup", "--stock-verify", "--manufacturer", "Yageo"},
		"validate":       {"validate", "--bogus"},
		"documents list": {"documents", "list", "--bogus-flag"},
		"alternatives":   {"alternatives", "--bogus"},
	}
	for name, args := range cases {
		var stdout bytes.Buffer
		exitCode := Run(args, strings.NewReader(""), &stdout, &bytes.Buffer{})
		if exitCode != contract.ExitInput ||
			!strings.Contains(stdout.String(), "unexpected argument") {
			t.Errorf("%s: exit = %d, output = %s", name, exitCode, stdout.String())
		}
	}
}

func TestDotEnvProblemsAreUserErrorsWithTheRealCommand(t *testing.T) {
	// A broken checkout-local .env is user-authored input, not an internal
	// failure: the envelope must carry the invoked command and exit 2 so
	// agents can distinguish "fix your .env" from "the tool broke".
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.WriteFile(
		filepath.Join(directory, ".env"),
		[]byte("BOM_BUILDER_BROKEN=\"unclosed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer

	exitCode := Run([]string{"capabilities"}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if exitCode != contract.ExitInput {
		t.Fatalf("exit code = %d, want %d; output = %s", exitCode, contract.ExitInput, stdout.String())
	}
	var payload struct {
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
		Errors   []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON envelope: %v: %s", err, stdout.String())
	}
	if payload.Command != "capabilities" || payload.ExitCode != contract.ExitInput ||
		len(payload.Errors) == 0 || payload.Errors[0].Code != "CONFIG_ERROR" {
		t.Fatalf("unexpected envelope: %s", stdout.String())
	}
}

func TestValidateReadsStdinAndEmitsJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	input := `{"design":"stdin","parts":[{"part_number":"R1","manufacturer":"Yageo","quantity":3}]}`

	exitCode := Run(
		[]string{"validate", "-"},
		strings.NewReader(input),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}

	var payload struct {
		Status      string `json:"status"`
		DesignCount int    `json:"design_count"`
		PartCount   int    `json:"part_count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "valid" || payload.DesignCount != 1 || payload.PartCount != 1 {
		t.Fatalf("unexpected validation payload: %#v", payload)
	}
}

func TestLookupMicrochipReturnsReviewEvidenceWithoutPricing(t *testing.T) {
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fmt.Fprint(writer, `{"data":[{
			"part_number":"DSPIC33AK512MPS506-E/PT",
			"description":"200MHz DSC",
			"component_type":"16-bit DSC",
			"instock_quantity":"960",
			"lead_time_weeks":"6",
			"lifecycle_status":"REL",
			"minimum_order_quantity":"1",
			"order_multiple":"160",
			"packaging_type":"TRAY",
			"datasheet_url":"https://ww1.microchip.com/ds.pdf"
		}],"pagenumber":1,"pagesize":1000,"totalPages":1,"totalRecords":1}`)
	}))
	defer server.Close()
	t.Setenv("BOM_BUILDER_MICROCHIP_PRODUCTS_URL", server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup", "DSPIC33AK512MPS506-E/PT",
			"--manufacturer", "Microchip",
			"--quantity", "10",
			"--providers", "microchip",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 3 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Parts []struct {
			Status string `json:"status"`
			Offer  struct {
				Provider          string          `json:"provider"`
				ReviewRequired    bool            `json:"review_required"`
				AvailableQuantity *int            `json:"available_quantity"`
				LifecycleStatus   string          `json:"lifecycle_status"`
				SelectedPlan      json.RawMessage `json:"selected_plan"`
				PriceBreaks       json.RawMessage `json:"price_breaks"`
			} `json:"offer"`
			IssueCode string `json:"issue_code"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, stdout.String())
	}
	if len(payload.Parts) != 1 {
		t.Fatalf("parts = %s", stdout.String())
	}
	part := payload.Parts[0]
	if part.Status != "review" ||
		part.IssueCode != "MANUFACTURER_EVIDENCE_ONLY" ||
		part.Offer.Provider != "microchip" ||
		!part.Offer.ReviewRequired ||
		part.Offer.AvailableQuantity == nil ||
		*part.Offer.AvailableQuantity != 960 ||
		part.Offer.LifecycleStatus != "REL" ||
		len(part.Offer.SelectedPlan) != 0 ||
		len(part.Offer.PriceBreaks) != 0 {
		t.Fatalf("unexpected evidence lookup: %s", stdout.String())
	}
}

func TestExportECBOMWritesUploadReadyFile(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	var stdout bytes.Buffer
	input := `{"design":"NINAcamp","parts":[` +
		`{"part_number":"GCM188R71H104KA57J","manufacturer":"Murata",` +
		`"quantity":4,"designators":["C103","C104"],"value":"100n",` +
		`"package":"0603","description":"100n 50V"},` +
		`{"part_number":"TC2030-MCP-NL","manufacturer":"Tag-Connect",` +
		`"quantity":1,"mounted":false,"comment":"tooling, not fitted"}]}`

	exitCode := Run(
		[]string{"export", "ec-bom", "-", "--output", "bom.csv"},
		strings.NewReader(input),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Status   string `json:"status"`
		Command  string `json:"command"`
		Design   string `json:"design"`
		Artifact struct {
			OutputPath string `json:"output_path"`
			Format     string `json:"format"`
			SizeBytes  int64  `json:"size_bytes"`
			SHA256     string `json:"sha256"`
			LineCount  int    `json:"line_count"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "written" || payload.Command != "export ec-bom" ||
		payload.Design != "NINAcamp" || payload.Artifact.Format != "ec-bom-csv" ||
		payload.Artifact.LineCount != 2 || payload.Artifact.SizeBytes < 1 ||
		len(payload.Artifact.SHA256) != 64 {
		t.Fatalf("unexpected export payload: %s", stdout.String())
	}
	written, err := os.ReadFile(payload.Artifact.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(written), "\r\n"), "\r\n")
	if len(lines) != 3 ||
		lines[0] != "Item;Quantity;Designators;Manufacturer;MPN;"+
			"Description;Value;Package;Mounted;Comment" ||
		lines[1] != "1;4;C103,C104;Murata;GCM188R71H104KA57J;100n 50V;100n;0603;Yes;" ||
		lines[2] != "2;1;;Tag-Connect;TC2030-MCP-NL;;;;No;tooling, not fitted" {
		t.Fatalf("unexpected eC-BOM content: %q", string(written))
	}

	// A second run must refuse to overwrite the existing artifact.
	var second bytes.Buffer
	exitCode = Run(
		[]string{"export", "ec-bom", "-", "--output", "bom.csv"},
		strings.NewReader(input),
		&second,
		&bytes.Buffer{},
	)
	if exitCode != 2 || !strings.Contains(second.String(), "OUTPUT_EXISTS") {
		t.Fatalf("overwrite was not refused: exit=%d output=%s", exitCode, second.String())
	}
}

func TestInvalidInputUsesStableExitAndIssueCode(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"validate", "-"},
		strings.NewReader(`{"design":"broken","parts":[]}`),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d", exitCode)
	}

	var payload struct {
		ExitCode int `json:"exit_code"`
		Errors   []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExitCode != 2 || len(payload.Errors) != 1 || payload.Errors[0].Code != "INVALID_INPUT" {
		t.Fatalf("unexpected error payload: %s", stdout.String())
	}
}

func TestUnknownCommandStillEmitsJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"mystery"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 2 || !json.Valid(stdout.Bytes()) {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
}

func TestLookupReturnsStockVerifiedMouserPlan(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup",
			"RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "950",
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
	if payload["status"] != "complete" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	parts := payload["parts"].([]any)
	offer := parts[0].(map[string]any)["offer"].(map[string]any)
	plan := offer["selected_plan"].(map[string]any)
	if plan["extended_price"] != "90.000000" || plan["stock_verified"] != true {
		t.Fatalf("unsafe plan: %#v", plan)
	}
}

func TestLookupCacheReusesNormalizedResultWithoutCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	cachePath := filepath.Join(t.TempDir(), "lookups.sqlite3")
	t.Setenv("BOM_BUILDER_CACHE_POLICY", "prefer")
	t.Setenv("BOM_BUILDER_CACHE_DB", cachePath)

	var first bytes.Buffer
	exitCode := Run(
		[]string{
			"lookup", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "100",
			"--providers", "mouser",
		},
		strings.NewReader(""),
		&first,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("first exit code = %d, output = %s", exitCode, first.String())
	}
	var firstPayload struct {
		Run struct {
			RequestCount int `json:"request_count"`
			Cache        struct {
				Misses    int `json:"misses"`
				Refreshes int `json:"refreshes"`
				Writes    int `json:"writes"`
			} `json:"cache"`
		} `json:"run"`
	}
	if err := json.Unmarshal(first.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if firstPayload.Run.RequestCount != 1 ||
		firstPayload.Run.Cache.Misses != 1 ||
		firstPayload.Run.Cache.Refreshes != 1 ||
		firstPayload.Run.Cache.Writes != 1 {
		t.Fatalf("unexpected first cache metadata: %s", first.String())
	}

	t.Setenv("MOUSER_API_KEYS", "")
	t.Setenv("MOUSER_API_KEY", "")
	var second bytes.Buffer
	exitCode = Run(
		[]string{
			"lookup", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "100",
			"--cache-policy", "only",
		},
		strings.NewReader(""),
		&second,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("cached exit code = %d, output = %s", exitCode, second.String())
	}
	var secondPayload struct {
		Status string `json:"status"`
		Run    struct {
			RequestCount int `json:"request_count"`
			Cache        struct {
				Policy               string `json:"policy"`
				Hits                 int    `json:"hits"`
				Misses               int    `json:"misses"`
				ReusedSourceRequests int    `json:"reused_source_requests"`
			} `json:"cache"`
		} `json:"run"`
		Parts []struct {
			Status string `json:"status"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(second.Bytes(), &secondPayload); err != nil {
		t.Fatal(err)
	}
	if secondPayload.Status != "complete" ||
		secondPayload.Run.RequestCount != 0 ||
		secondPayload.Run.Cache.Policy != "only" ||
		secondPayload.Run.Cache.Hits != 1 ||
		// One hit from the cached Mouser row; every other cache-only
		// automatic provider (digikey, ti, farnell) misses.
		secondPayload.Run.Cache.Misses != 3 ||
		secondPayload.Run.Cache.ReusedSourceRequests != 1 ||
		len(secondPayload.Parts) != 1 ||
		secondPayload.Parts[0].Status != "priced" {
		t.Fatalf("unexpected cached output: %s", second.String())
	}
}

func TestCacheCommandsInspectVerifyPreviewAndApply(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	cachePath := filepath.Join(t.TempDir(), "lookups.sqlite3")
	t.Setenv("BOM_BUILDER_CACHE_POLICY", "prefer")
	t.Setenv("BOM_BUILDER_CACHE_DB", cachePath)
	var seeded bytes.Buffer
	exitCode := Run(
		[]string{
			"lookup", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "100",
			"--providers", "mouser",
		},
		strings.NewReader(""),
		&seeded,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("seed exit code = %d, output = %s", exitCode, seeded.String())
	}

	for _, command := range [][]string{
		{"cache", "status"},
		{"cache", "list", "--provider", "mouser", "--include-stale"},
		{"cache", "verify"},
	} {
		var stdout bytes.Buffer
		exitCode = Run(command, strings.NewReader(""), &stdout, &bytes.Buffer{})
		if exitCode != 0 {
			t.Fatalf("%v exit code = %d, output = %s", command, exitCode, stdout.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		cacheStatus := payload["cache"].(map[string]any)
		if cacheStatus["exists"] != true || cacheStatus["entry_count"] != float64(1) {
			t.Fatalf("unexpected cache status for %v: %s", command, stdout.String())
		}
	}

	var previewOutput bytes.Buffer
	exitCode = Run(
		[]string{"cache", "prune", "--all"},
		strings.NewReader(""),
		&previewOutput,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("preview exit code = %d, output = %s", exitCode, previewOutput.String())
	}
	var preview struct {
		Cache struct {
			EntryCount int `json:"entry_count"`
		} `json:"cache"`
		Prune struct {
			MatchedCount int    `json:"matched_count"`
			ApplyToken   string `json:"apply_token"`
			Applied      bool   `json:"applied"`
		} `json:"prune"`
	}
	if err := json.Unmarshal(previewOutput.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Cache.EntryCount != 1 ||
		preview.Prune.MatchedCount != 1 ||
		preview.Prune.ApplyToken == "" ||
		preview.Prune.Applied {
		t.Fatalf("unexpected preview: %s", previewOutput.String())
	}

	var appliedOutput bytes.Buffer
	exitCode = Run(
		[]string{
			"cache", "prune", "--all",
			"--apply", preview.Prune.ApplyToken,
		},
		strings.NewReader(""),
		&appliedOutput,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("apply exit code = %d, output = %s", exitCode, appliedOutput.String())
	}
	var applied struct {
		Cache struct {
			EntryCount int `json:"entry_count"`
		} `json:"cache"`
		Prune struct {
			Applied      bool `json:"applied"`
			DeletedCount int  `json:"deleted_count"`
		} `json:"prune"`
	}
	if err := json.Unmarshal(appliedOutput.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Cache.EntryCount != 0 ||
		!applied.Prune.Applied ||
		applied.Prune.DeletedCount != 1 {
		t.Fatalf("unexpected applied prune: %s", appliedOutput.String())
	}
}

func TestPriceReadsStdinAggregatesAndPrices(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer
	input := `{"design":"board","parts":[` +
		`{"part_number":"RC0402FR-0710KL","manufacturer":"Yageo","quantity":2},` +
		`{"part_number":"rc0402fr-0710kl","manufacturer":"YAGEO","quantity":1}` +
		`]}`

	exitCode := Run(
		[]string{"price", "-", "--units", "10", "--attrition", "0.02"},
		strings.NewReader(input),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Status  string `json:"status"`
		Summary struct {
			LineCount int    `json:"line_count"`
			TotalCost string `json:"total_cost"`
		} `json:"summary"`
		Parts []struct {
			Demand struct {
				RequiredQuantity int `json:"required_quantity"`
			} `json:"demand"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "complete" || payload.Summary.LineCount != 1 ||
		payload.Parts[0].Demand.RequiredQuantity != 31 ||
		payload.Summary.TotalCost != "3.100000" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestLookupShortageUsesIncompleteExitCode(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "10")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "100",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 3 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "incomplete" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	parts := payload["parts"].([]any)
	if parts[0].(map[string]any)["status"] != "shortage" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestProviderCheckCanRunLiveMouserProbe(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"providers", "check", "--providers", "mouser", "--live"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Live      bool `json:"live"`
		Providers []struct {
			Status       string `json:"status"`
			RequestCount int    `json:"request_count"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Live || len(payload.Providers) != 1 ||
		payload.Providers[0].Status != "ok" ||
		payload.Providers[0].RequestCount != 1 {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestDocumentsListReturnsProviderEvidenceLinks(t *testing.T) {
	t.Chdir(t.TempDir())
	server := mouserServer(t, "5000")
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"documents", "list", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--providers", "mouser",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Status string `json:"status"`
		Run    struct {
			RequestCount int `json:"request_count"`
		} `json:"run"`
		Documents []struct {
			Kind         string `json:"kind"`
			Provider     string `json:"provider"`
			URL          string `json:"url"`
			Preferred    bool   `json:"preferred"`
			Downloadable bool   `json:"downloadable"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "complete" ||
		payload.Run.RequestCount != 1 ||
		len(payload.Documents) != 2 ||
		payload.Documents[0].Kind != "datasheet" ||
		payload.Documents[0].Provider != "mouser" ||
		payload.Documents[0].URL != "https://example.test/datasheet.pdf" ||
		!payload.Documents[0].Preferred ||
		!payload.Documents[0].Downloadable {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestDocumentsFetchRejectsNonHTTPSURLBeforeWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	output := filepath.Join(t.TempDir(), "datasheet.pdf")
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"documents", "fetch", "http://127.0.0.1/private.pdf",
			"--output", output,
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Errors) != 1 ||
		payload.Errors[0].Code != "INVALID_DOCUMENT_INPUT" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output should not exist: %v", err)
	}
}

func TestAlternativesSourcesOnlyViableCandidatesAndKeepsReviewRequired(t *testing.T) {
	t.Chdir(t.TempDir())
	server := alternativesMouserServer(t)
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer
	input := `{
		"kind":"resistor",
		"required_quantity":100,
		"original":{
			"part_number":"ORIGINAL10K","manufacturer":"Yageo","package":"0402",
			"resistance_ohms":"10000","tolerance_percent":"1",
			"power_watts":"0.063","voltage_volts":"50",
			"temperature_min_c":-55,"temperature_max_c":125,
			"technology":"thick film"
		},
		"candidates":[
			{
				"part_number":"ALT10K","manufacturer":"Vishay","package":"0402",
				"resistance_ohms":"10000","tolerance_percent":"1",
				"power_watts":"0.1","voltage_volts":"75",
				"temperature_min_c":-55,"temperature_max_c":155,
				"technology":"thick film"
			},
			{
				"part_number":"BAD10K","manufacturer":"Vishay","package":"0603",
				"resistance_ohms":"10000","tolerance_percent":"1",
				"power_watts":"0.1","voltage_volts":"75",
				"temperature_min_c":-55,"temperature_max_c":155,
				"technology":"thick film"
			}
		]
	}`

	exitCode := Run(
		[]string{"alternatives", "-", "--providers", "mouser"},
		strings.NewReader(input),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 3 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Status string `json:"status"`
		Run    struct {
			RequestCount int `json:"request_count"`
		} `json:"run"`
		Summary struct {
			CompatibleCount   int `json:"compatible_count"`
			IncompatibleCount int `json:"incompatible_count"`
			InStockCount      int `json:"in_stock_count"`
			RecommendedCount  int `json:"recommended_for_review_count"`
		} `json:"summary"`
		OriginalSourcing struct {
			Status string `json:"status"`
		} `json:"original_sourcing"`
		Candidates []struct {
			Candidate struct {
				PartNumber string `json:"part_number"`
			} `json:"candidate"`
			Compatibility        string `json:"compatibility"`
			ReviewRequired       bool   `json:"engineering_review_required"`
			RecommendedForReview bool   `json:"recommended_for_review"`
			Sourcing             *struct {
				Status string `json:"status"`
			} `json:"sourcing"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "review_required" ||
		payload.Run.RequestCount != 2 ||
		payload.OriginalSourcing.Status != "shortage" ||
		payload.Summary.CompatibleCount != 1 ||
		payload.Summary.IncompatibleCount != 1 ||
		payload.Summary.InStockCount != 1 ||
		payload.Summary.RecommendedCount != 1 ||
		len(payload.Candidates) != 2 ||
		payload.Candidates[0].Candidate.PartNumber != "ALT10K" ||
		!payload.Candidates[0].ReviewRequired ||
		!payload.Candidates[0].RecommendedForReview ||
		payload.Candidates[0].Sourcing == nil ||
		payload.Candidates[0].Sourcing.Status != "priced" ||
		payload.Candidates[1].Compatibility != "incompatible" ||
		payload.Candidates[1].Sourcing != nil {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestAlternativesCanSkipCandidatesWhenOriginalStockIsSufficient(t *testing.T) {
	t.Chdir(t.TempDir())
	server := alternativesMouserServer(t)
	defer server.Close()
	configureMouserTestEnvironment(t, server.URL)
	var stdout bytes.Buffer
	input := `{
		"kind":"resistor",
		"required_quantity":100,
		"original":{
			"part_number":"AVAILABLE10K","manufacturer":"Yageo","package":"0402",
			"resistance_ohms":"10000","tolerance_percent":"1",
			"power_watts":"0.063","voltage_volts":"50",
			"temperature_min_c":-55,"temperature_max_c":125,
			"technology":"thick film"
		},
		"candidates":[{
			"part_number":"ALT10K","manufacturer":"Vishay","package":"0402",
			"resistance_ohms":"10000","tolerance_percent":"1",
			"power_watts":"0.1","voltage_volts":"75",
			"temperature_min_c":-55,"temperature_max_c":155,
			"technology":"thick film"
		}]
	}`

	exitCode := Run(
		[]string{
			"alternatives", "-", "--providers", "mouser",
			"--only-if-shortage",
		},
		strings.NewReader(input),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Status string `json:"status"`
		Run    struct {
			RequestCount int `json:"request_count"`
		} `json:"run"`
		Candidates []struct {
			Sourcing any `json:"sourcing"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "not_needed" ||
		payload.Run.RequestCount != 1 ||
		len(payload.Candidates) != 1 ||
		payload.Candidates[0].Sourcing != nil {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestLookupReturnsDigiKeyPlanAndDocuments(t *testing.T) {
	t.Chdir(t.TempDir())
	server := digiKeyServer(t, "Panasonic Electronic Components", "ECA-1VHG102", "69.8")
	defer server.Close()
	configureDigiKeyTestEnvironment(t, server.URL)
	t.Setenv("MOUSER_API_KEYS", "")
	t.Setenv("MOUSER_API_KEY", "")
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup", "ECA-1VHG102",
			"--manufacturer", "Panasonic",
			"--quantity", "100",
			"--providers", "digikey",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Run struct {
			Providers []struct {
				Name         string `json:"name"`
				RequestCount int    `json:"request_count"`
			} `json:"providers"`
			RequestCount int `json:"request_count"`
		} `json:"run"`
		Parts []struct {
			Offer struct {
				Provider     string `json:"provider"`
				DatasheetURL string `json:"datasheet_url"`
				SelectedPlan *struct {
					ExtendedPrice string `json:"extended_price"`
				} `json:"selected_plan"`
			} `json:"offer"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Run.Providers) != 1 ||
		payload.Run.Providers[0].Name != "digikey" ||
		payload.Run.Providers[0].RequestCount != 3 ||
		payload.Run.RequestCount != 3 ||
		payload.Parts[0].Offer.Provider != "digikey" ||
		payload.Parts[0].Offer.DatasheetURL == "" ||
		payload.Parts[0].Offer.SelectedPlan == nil ||
		payload.Parts[0].Offer.SelectedPlan.ExtendedPrice != "69.800000" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestLookupComparesMouserAndDigiKeyOffers(t *testing.T) {
	t.Chdir(t.TempDir())
	mouser := mouserServer(t, "5000")
	defer mouser.Close()
	digiKey := digiKeyServer(t, "Yageo Corporation", "RC0402FR-0710KL", "8.0")
	defer digiKey.Close()
	configureMouserTestEnvironment(t, mouser.URL)
	configureDigiKeyTestEnvironment(t, digiKey.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup", "RC0402FR-0710KL",
			"--manufacturer", "Yageo",
			"--quantity", "100",
			"--providers", "mouser,digikey",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Run struct {
			Providers    []any `json:"providers"`
			RequestCount int   `json:"request_count"`
		} `json:"run"`
		Parts []struct {
			Offer struct {
				Provider string `json:"provider"`
			} `json:"offer"`
			Offers []struct {
				Provider     string `json:"provider"`
				SelectedPlan any    `json:"selected_plan"`
			} `json:"offers"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Run.Providers) != 2 ||
		payload.Run.RequestCount != 4 ||
		payload.Parts[0].Offer.Provider != "digikey" ||
		len(payload.Parts[0].Offers) != 2 {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
	selectedCount := 0
	for _, offer := range payload.Parts[0].Offers {
		if offer.SelectedPlan != nil {
			selectedCount++
			if offer.Provider != "digikey" {
				t.Fatalf("wrong provider selected: %s", stdout.String())
			}
		}
	}
	if selectedCount != 1 {
		t.Fatalf("selected plan count = %d, output = %s", selectedCount, stdout.String())
	}
}

func TestProviderCheckCanRunLiveDigiKeyProbe(t *testing.T) {
	t.Chdir(t.TempDir())
	server := digiKeyServer(t, "Panasonic Electronic Components", "ECA-1VHG102", "69.8")
	defer server.Close()
	configureDigiKeyTestEnvironment(t, server.URL)
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"providers", "check", "--providers", "digikey", "--live"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Providers []struct {
			Status       string `json:"status"`
			RequestCount int    `json:"request_count"`
			Details      struct {
				Currency   string `json:"currency"`
				HeaderMode string `json:"header_mode"`
			} `json:"details"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Providers) != 1 ||
		payload.Providers[0].Status != "ok" ||
		payload.Providers[0].RequestCount != 2 ||
		payload.Providers[0].Details.Currency != "EUR" ||
		payload.Providers[0].Details.HeaderMode != "account_id" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestLookupReturnsTIStorePlan(t *testing.T) {
	t.Chdir(t.TempDir())
	server := tiStoreServer(t, "TPS61160DRVR", "TPS61160", "EUR", 5000)
	defer server.Close()
	configureTITestEnvironment(t, server.URL, "EUR")
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{
			"lookup", "TPS61160DRVR",
			"--manufacturer", "Texas Instruments",
			"--quantity", "950",
			"--providers", "ti",
		},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Run struct {
			Providers []struct {
				Name         string `json:"name"`
				RequestCount int    `json:"request_count"`
			} `json:"providers"`
		} `json:"run"`
		Parts []struct {
			Offer struct {
				Provider     string `json:"provider"`
				OrderLimit   *int   `json:"order_limit"`
				SelectedPlan *struct {
					PurchasedQuantity int    `json:"purchased_quantity"`
					ExtendedPrice     string `json:"extended_price"`
				} `json:"selected_plan"`
			} `json:"offer"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Run.Providers) != 1 ||
		payload.Run.Providers[0].Name != "ti" ||
		payload.Run.Providers[0].RequestCount != 2 ||
		payload.Parts[0].Offer.Provider != "ti" ||
		payload.Parts[0].Offer.OrderLimit == nil ||
		*payload.Parts[0].Offer.OrderLimit != 10000 ||
		payload.Parts[0].Offer.SelectedPlan == nil ||
		payload.Parts[0].Offer.SelectedPlan.PurchasedQuantity != 1000 ||
		payload.Parts[0].Offer.SelectedPlan.ExtendedPrice != "90.000000" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestProviderCheckCanRunLiveTIProbe(t *testing.T) {
	t.Chdir(t.TempDir())
	server := tiStoreServer(t, "TMP421AQDCNRQ1", "TMP421-Q1", "USD", 5000)
	defer server.Close()
	configureTITestEnvironment(t, server.URL, "USD")
	var stdout bytes.Buffer

	exitCode := Run(
		[]string{"providers", "check", "--providers", "ti", "--live"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %s", exitCode, stdout.String())
	}
	var payload struct {
		Providers []struct {
			Status       string `json:"status"`
			RequestCount int    `json:"request_count"`
			Details      struct {
				Currency          string `json:"currency"`
				MatchedPartNumber string `json:"matched_part_number"`
			} `json:"details"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Providers) != 1 ||
		payload.Providers[0].Status != "ok" ||
		payload.Providers[0].RequestCount != 2 ||
		payload.Providers[0].Details.Currency != "USD" ||
		payload.Providers[0].Details.MatchedPartNumber != "TMP421AQDCNRQ1" {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func configureMouserTestEnvironment(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("MOUSER_API_KEYS", "test-secret")
	t.Setenv("MOUSER_API_KEY", "")
	t.Setenv("DIGIKEY_CLIENT_ID", "")
	t.Setenv("DIGIKEY_CLIENT_SECRET", "")
	t.Setenv("DIGIKEY_ACCOUNT_ID", "")
	t.Setenv("TI_STORE_API_KEY", "")
	t.Setenv("TI_STORE_API_SECRET", "")
	t.Setenv("BOM_BUILDER_MOUSER_API_URL", endpoint)
	t.Setenv("BOM_BUILDER_MOUSER_MAX_ATTEMPTS", "1")
}

func configureDigiKeyTestEnvironment(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("DIGIKEY_CLIENT_ID", "client-id")
	t.Setenv("DIGIKEY_CLIENT_SECRET", "client-secret")
	t.Setenv("DIGIKEY_ACCOUNT_ID", "account-id")
	t.Setenv("DIGIKEY_LOCALE_SITE", "DE")
	t.Setenv("DIGIKEY_LOCALE_LANGUAGE", "en")
	t.Setenv("DIGIKEY_LOCALE_CURRENCY", "EUR")
	t.Setenv("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", "de")
	t.Setenv("BOM_BUILDER_DIGIKEY_API_BASE_URL", endpoint)
	t.Setenv("BOM_BUILDER_DIGIKEY_TOKEN_URL", endpoint+"/v1/oauth2/token")
	t.Setenv("BOM_BUILDER_DIGIKEY_MAX_ATTEMPTS", "1")
}

func configureTITestEnvironment(
	t *testing.T,
	endpoint, currency string,
) {
	t.Helper()
	t.Setenv("TI_STORE_API_KEY", "ti-client-id")
	t.Setenv("TI_STORE_API_SECRET", "ti-client-secret")
	t.Setenv("TI_STORE_PRICE_CURRENCY", currency)
	t.Setenv("BOM_BUILDER_TI_PRODUCTS_URL", endpoint+"/v2/store/products")
	t.Setenv("BOM_BUILDER_TI_TOKEN_URL", endpoint+"/v1/oauth/accesstoken")
	t.Setenv("BOM_BUILDER_TI_MAX_ATTEMPTS", "1")
}

func mouserServer(t *testing.T, stock string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("apiKey") != "test-secret" {
			t.Error("Mouser test request omitted API key")
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(
			writer,
			`{"Errors":[],"SearchResults":{"NumberOfResult":1,"Parts":[{`+
				`"AvailabilityInStock":%q,`+
				`"Manufacturer":"Yageo Corporation",`+
				`"ManufacturerPartNumber":"RC0402FR-0710KL",`+
				`"MouserPartNumber":"603-RC0402FR-0710KL",`+
				`"Min":"1","Mult":"1",`+
				`"DataSheetUrl":"https://example.test/datasheet.pdf",`+
				`"ProductDetailUrl":"https://example.test/product",`+
				`"PriceBreaks":[`+
				`{"Quantity":1,"Price":"0,10 €","Currency":"EUR"},`+
				`{"Quantity":1000,"Price":"0,09 €","Currency":"EUR"}]`+
				`}]}}`,
			stock,
		)
	}))
}

func alternativesMouserServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Search struct {
				Manufacturer string `json:"manufacturerName"`
				PartNumber   string `json:"mouserPartNumber"`
			} `json:"SearchByPartMfrNameRequest"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		stock := "5000"
		if input.Search.PartNumber == "ORIGINAL10K" {
			stock = "0"
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(
			writer,
			`{"Errors":[],"SearchResults":{"NumberOfResult":1,"Parts":[{`+
				`"AvailabilityInStock":%q,`+
				`"Manufacturer":%q,`+
				`"ManufacturerPartNumber":%q,`+
				`"MouserPartNumber":%q,`+
				`"Min":"1","Mult":"1",`+
				`"DataSheetUrl":"https://manufacturer.test/part.pdf",`+
				`"ProductDetailUrl":"https://mouser.test/product",`+
				`"PriceBreaks":[{"Quantity":1,"Price":"0.10","Currency":"EUR"}]`+
				`}]}}`,
			stock,
			input.Search.Manufacturer,
			input.Search.PartNumber,
			"TEST-"+input.Search.PartNumber,
		)
	}))
}

func digiKeyServer(
	t *testing.T,
	manufacturer, manufacturerPartNumber, total string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/v1/oauth2/token":
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			if request.Form.Get("client_id") != "client-id" ||
				request.Form.Get("client_secret") != "client-secret" ||
				request.Form.Get("grant_type") != "client_credentials" {
				t.Errorf("unexpected Digi-Key token form: %v", request.Form)
			}
			fmt.Fprint(writer, `{"access_token":"access-token","expires_in":600}`)
		case strings.HasSuffix(request.URL.Path, "/productdetails"):
			assertCLIKeyDigiKeyHeaders(t, request)
			// Mirrors live behavior: ProductDetails carries the real
			// per-variation stock (2026-07-30 fix).
			fmt.Fprint(writer, `{"Product":{`+
				`"DatasheetUrl":"https://manufacturer.test/part.pdf",`+
				`"ProductUrl":"https://digikey.test/product",`+
				`"QuantityAvailable":5000,`+
				`"ProductVariations":[{`+
				`"DigiKeyProductNumber":"P5555-ND",`+
				`"QuantityAvailableforPackageType":5000}]}}`)
		default:
			assertCLIKeyDigiKeyHeaders(t, request)
			fmt.Fprintf(writer, `{
				"RequestedProduct":%q,
				"RequestedQuantity":100,
				"ManufacturerPartNumber":%q,
				"Manufacturer":{"Name":%q},
				"ProductUrl":"https://digikey.test/base-product",
				"SettingsUsed":{"SearchLocaleUsed":{"Currency":"EUR"}},
				"MyPricingOptions":[],
				"StandardPricingOptions":[{
					"PricingOption":"Exact",
					"TotalQuantityPriced":100,
					"TotalPrice":%s,
					"QuantityAvailable":0,
					"Products":[{
						"DigiKeyProductNumber":"P5555-ND",
						"QuantityPriced":100,
						"MinimumOrderQuantity":1,
						"UnitPrice":0.08,
						"ExtendedPrice":%s,
						"PackageType":{"Name":"Cut Tape"}
					}]
				}]
			}`, manufacturerPartNumber, manufacturerPartNumber, manufacturer, total, total)
		}
	}))
}

func assertCLIKeyDigiKeyHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("Authorization") != "Bearer access-token" ||
		request.Header.Get("X-DIGIKEY-Client-Id") != "client-id" ||
		request.Header.Get("X-DIGIKEY-Account-Id") != "account-id" ||
		request.Header.Get("X-DIGIKEY-Locale-Currency") != "EUR" {
		t.Errorf("unexpected Digi-Key headers: %#v", request.Header)
	}
}

func tiStoreServer(
	t *testing.T,
	tiPartNumber, genericPartNumber, currency string,
	stock int,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/oauth/accesstoken" {
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			if request.Form.Get("grant_type") != "client_credentials" ||
				request.Form.Get("client_id") != "ti-client-id" ||
				request.Form.Get("client_secret") != "ti-client-secret" {
				t.Errorf("unexpected TI token form: %v", request.Form)
			}
			fmt.Fprint(writer, `{"access_token":"ti-access-token","expires_in":3599}`)
			return
		}
		if request.Header.Get("Authorization") != "Bearer ti-access-token" ||
			request.URL.Query().Get("currency") != currency {
			t.Errorf("unexpected TI request: %#v %s", request.Header, request.URL)
		}
		fmt.Fprintf(writer, `{
			"tiPartNumber":%q,
			"genericPartNumber":%q,
			"buyNowURL":"https://www.ti.com/product/test",
			"quantity":%d,
			"limit":10000,
			"description":"Test TI product",
			"minimumOrderQuantity":1,
			"standardPackQuantity":3000,
			"packageType":"SOT-23 (DBV)",
			"packageCarrier":"Large T&R",
			"customReel":true,
			"lifeCycle":"ACTIVE",
			"pricing":[{
				"currency":%q,
				"priceBreaks":[
					{"priceBreakQuantity":1,"price":0.10},
					{"priceBreakQuantity":1000,"price":0.09}
				]
			}]
		}`, tiPartNumber, genericPartNumber, stock, currency)
	}))
}
