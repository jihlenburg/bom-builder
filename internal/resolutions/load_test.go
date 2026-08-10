// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package resolutions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRequest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return path
}

const validRequestJSON = `{
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

func TestLoadAcceptsValidRequestFromFileAndStdin(t *testing.T) {
	request, err := Load(writeRequest(t, validRequestJSON), nil)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	if request.Replacement.Provider != "mouser" || request.ApprovedBy != "J. Ihlenburg" {
		t.Fatalf("unexpected request: %+v", request)
	}
	fromStdin, err := Load("-", strings.NewReader(validRequestJSON))
	if err != nil {
		t.Fatalf("load stdin: %v", err)
	}
	if fromStdin.PartNumber != "TMP421-Q1" {
		t.Fatalf("unexpected stdin request: %+v", fromStdin)
	}
}

func TestLoadRejectsMalformedRequests(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124"},
			"approved_by":"X","surprise":true}`,
		"trailing document": validRequestJSON + `{"extra":true}`,
		"missing approver": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124"}}`,
		"short part number": `{"manufacturer":"TI","part_number":"AB",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124"},
			"approved_by":"X"}`,
		"sku without provider": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124",
			"provider_sku":"595-ABC"},"approved_by":"X"}`,
		"unknown provider": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124",
			"provider":"amazon"},"approved_by":"X"}`,
		"http evidence": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124"},
			"approved_by":"X",
			"source_documents":[{"url":"http://x.example/a.pdf",
			"sha256":"abababababababababababababababababababababababababababababababab"}]}`,
		"short evidence hash": `{"manufacturer":"TI","part_number":"ABC-123",
			"replacement":{"manufacturer":"TI","part_number":"ABC-124"},
			"approved_by":"X",
			"source_documents":[{"url":"https://x.example/a.pdf","sha256":"abcd"}]}`,
	}
	for name, content := range cases {
		if _, err := Load(writeRequest(t, content), nil); err == nil {
			t.Errorf("%s: expected a validation failure", name)
		}
	}
}

func TestLoadEnforcesSizeLimit(t *testing.T) {
	oversized := `{"manufacturer":"` + strings.Repeat("A", maxRequestBytes) + `"}`
	if _, err := Load(writeRequest(t, oversized), nil); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected the size limit to reject the document, got %v", err)
	}
}

func TestValidateRequestNormalizesFields(t *testing.T) {
	request := Request{
		Manufacturer: "  Texas Instruments  ",
		PartNumber:   " TMP421-Q1 ",
		Replacement: Replacement{
			Manufacturer: " TI ",
			PartNumber:   " TMP421AQDCNRQ1 ",
			Provider:     " MOUSER ",
			ProviderSKU:  " 595-X ",
		},
		ApprovedBy: "  X  ",
		SourceDocuments: []EvidenceDocument{{
			URL:    " https://x.example/a.pdf ",
			SHA256: strings.ToUpper(strings.Repeat("ab", 32)) + " ",
		}},
	}
	if err := ValidateRequest(&request); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if request.Manufacturer != "Texas Instruments" ||
		request.Replacement.Provider != "mouser" ||
		request.SourceDocuments[0].SHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("normalization did not apply: %+v", request)
	}
}

func TestDefaultPathIsUserScoped(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Skipf("no user configuration directory in this environment: %v", err)
	}
	if filepath.Base(path) != "resolutions-v1.sqlite3" ||
		filepath.Base(filepath.Dir(path)) != "bom-builder" {
		t.Fatalf("unexpected default path: %s", path)
	}
}
