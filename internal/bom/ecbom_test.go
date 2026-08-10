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
	data, err := EurocircuitsCSV(contract.Design{
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
