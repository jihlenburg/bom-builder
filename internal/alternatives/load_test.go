// Copyright (C) 2026 Joern Ihlenburg
// SPDX-License-Identifier: GPL-3.0-or-later

package alternatives

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoadStrictValidResistorRequest(t *testing.T) {
	request, err := Load("-", strings.NewReader(`{
		"kind":"RESISTOR",
		"required_quantity":1000,
		"original":{
			"part_number":"RC0402FR-0710KL",
			"manufacturer":"Yageo",
			"package":"0402",
			"resistance_ohms":"10000",
			"tolerance_percent":"1",
			"power_watts":"0.0625",
			"voltage_volts":"50",
			"temperature_min_c":-55,
			"temperature_max_c":155,
			"technology":"thick film",
			"qualifications":["AEC-Q200"]
		},
		"candidates":[{
			"part_number":"CRCW040210K0FKED",
			"manufacturer":"Vishay",
			"package":"0402",
			"resistance_ohms":"10000",
			"tolerance_percent":"1",
			"power_watts":"0.063",
			"voltage_volts":"50",
			"temperature_min_c":-55,
			"temperature_max_c":155,
			"technology":"thick film"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Kind != "resistor" ||
		request.RequiredQuantity != 1000 ||
		request.Original.ResistanceOhms == nil ||
		*request.Original.ResistanceOhms != "10000" {
		t.Fatalf("request = %#v", request)
	}
}

func TestLoadRejectsUnknownIrrelevantAndMissingCriticalFields(t *testing.T) {
	base := `{
		"kind":"resistor",
		"required_quantity":10,
		"original":{
			"part_number":"ORIGINAL","manufacturer":"Maker","package":"0402",
			"resistance_ohms":"1000","tolerance_percent":"1",
			"power_watts":"0.063","voltage_volts":"50",
			"temperature_min_c":-55,"temperature_max_c":125,
			"technology":"thick film"%s
		},
		"candidates":[{
			"part_number":"CANDIDATE","manufacturer":"Other"%s
		}]
	}`
	for _, testCase := range []struct {
		name              string
		originalAddition  string
		candidateAddition string
	}{
		{name: "unknown", candidateAddition: `,"mystery":true`},
		{name: "irrelevant", candidateAddition: `,"capacitance_farads":"0.1"`},
		{name: "zero physical value", candidateAddition: `,"power_watts":"0"`},
		{name: "missing original", originalAddition: ``, candidateAddition: ``},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := base
			if testCase.name == "missing original" {
				input = strings.Replace(input, `"voltage_volts":"50",`, "", 1)
			}
			input = fmt.Sprintf(input, testCase.originalAddition, testCase.candidateAddition)
			if _, err := Load("-", strings.NewReader(input)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadRejectsDuplicateCandidate(t *testing.T) {
	input := `{
		"kind":"capacitor",
		"required_quantity":1,
		"original":{
			"part_number":"ABC","manufacturer":"Maker","package":"0603",
			"capacitance_farads":"0.0000001","tolerance_percent":"10",
			"voltage_volts":"50","dielectric":"X7R","polarized":false,
			"temperature_min_c":-55,"temperature_max_c":125
		},
		"candidates":[{
			"part_number":"abc","manufacturer":"maker"
		}]
	}`
	if _, err := Load("-", strings.NewReader(input)); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestValidatePartReportsDeterministicFieldOnMultipleErrors(t *testing.T) {
	t.Parallel()
	// With two invalid numeric fields, the reported field must not depend
	// on map iteration order: nondeterministic error text breaks golden
	// tests and the project's deterministic-output rule.
	first := ""
	for run := 0; run < 30; run++ {
		bad := "not-a-number"
		part := PartSpec{
			PartNumber:       "ABC123",
			Manufacturer:     "Maker",
			ResistanceOhms:   &bad,
			TolerancePercent: &bad,
		}
		err := validatePart(&part, "original", "resistor", true)
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "resistance_ohms") {
			t.Fatalf("run %d: expected the declaration-first field, got %v", run, err)
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("run %d: error changed from %q to %q", run, first, err.Error())
		}
	}
}
