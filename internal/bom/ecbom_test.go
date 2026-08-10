// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package bom

import (
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
)

func stringPointer(value string) *string { return &value }

func TestEurocircuitsCSVRendersUploadReadyRows(t *testing.T) {
	t.Parallel()
	notMounted := false
	data, warnings, err := EurocircuitsCSV(contract.Design{
		Design: "demo",
		Parts: []contract.Part{
			{
				PartNumber:   "GCM188R71H104KA57J",
				Manufacturer: "Murata",
				Quantity:     4,
				Designators:  []string{"C103", "C104"},
				Description:  stringPointer("100n 50V"),
				Value:        stringPointer("100n"),
				Package:      stringPointer("0603"),
				Comment:      stringPointer("with; semicolon"),
			},
			{
				PartNumber:   "TC2030-MCP-NL",
				Manufacturer: "Tag-Connect",
				Quantity:     1,
				Mounted:      &notMounted,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("benign content must not warn: %+v", warnings)
	}
	text := string(data)
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 CRLF rows, got %q", text)
	}
	if lines[0] != "Item;Quantity;Designators;Manufacturer;MPN;"+
		"Description;Value;Package;Mounted;Comment" {
		t.Fatalf("header = %q", lines[0])
	}
	if lines[1] != `1;4;C103,C104;Murata;GCM188R71H104KA57J;`+
		`100n 50V;100n;0603;Yes;"with; semicolon"` {
		t.Fatalf("row 1 = %q", lines[1])
	}
	if lines[2] != "2;1;;Tag-Connect;TC2030-MCP-NL;;;;No;" {
		t.Fatalf("row 2 = %q", lines[2])
	}
}

func TestEurocircuitsCSVNeutralizesFormulaContent(t *testing.T) {
	t.Parallel()
	data, warnings, err := EurocircuitsCSV(contract.Design{
		Design: "hostile",
		Parts: []contract.Part{
			{
				PartNumber:   "=cmd|' /C calc'!A0",
				Manufacturer: "@SUM(1+9)",
				Quantity:     1,
				Description:  stringPointer("+cmd|' /C calc'!A0"),
				Value:        stringPointer("-cmd|' /C calc'!A0"),
				Comment:      stringPointer("-40..85C range"),
			},
			{
				// Plain signed numbers stay verbatim: they are ordinary
				// engineering values, not formulas.
				PartNumber:   "SAFE-PART-1",
				Manufacturer: "ACME",
				Quantity:     2,
				Value:        stringPointer("-40"),
				Description:  stringPointer("+3.3"),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		`'=cmd|`,
		`'@SUM`,
		`'+cmd|`,
		`'-cmd|`,
		`'-40..85C range`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q to be escaped in output:\n%s", expected, text)
		}
	}
	if strings.Contains(text, ";'-40;") || strings.Contains(text, ";'+3.3;") {
		t.Fatalf("plain signed numbers must not be escaped:\n%s", text)
	}
	if !strings.Contains(text, ";-40;") || !strings.Contains(text, ";+3.3;") {
		t.Fatalf("plain signed numbers must survive verbatim:\n%s", text)
	}
	if len(warnings) != 5 {
		t.Fatalf("expected 5 escape warnings, got %+v", warnings)
	}
	for _, warning := range warnings {
		if warning.Code != "CSV_FORMULA_CONTENT_ESCAPED" {
			t.Fatalf("unexpected warning code: %+v", warning)
		}
	}
}
