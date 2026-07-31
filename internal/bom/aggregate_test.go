package bom

import (
	"strings"
	"testing"

	"github.com/jihlenburg/bom-builder/internal/contract"
	"github.com/jihlenburg/bom-builder/internal/money"
)

func TestAggregateScalesRoundsAndPreservesProvenance(t *testing.T) {
	t.Parallel()
	r1 := "R1,R2"
	r3 := "R3"
	designs := []contract.Design{
		{
			Design: "control",
			Parts: []contract.Part{{
				PartNumber:   "RC0402",
				Manufacturer: "Yageo",
				Quantity:     2,
				Reference:    &r1,
			}},
		},
		{
			Design: "power",
			Parts: []contract.Part{{
				PartNumber:   "rc0402",
				Manufacturer: "YAGEO",
				Quantity:     1,
				Reference:    &r3,
			}},
		},
	}
	attrition, _ := money.Parse("0.02")
	demands, warnings, err := Aggregate(designs, 10, attrition)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(demands) != 1 {
		t.Fatalf("demands = %#v", demands)
	}
	demand := demands[0]
	if demand.QuantityPerUnit != 3 || demand.RequiredQuantity != 31 {
		t.Fatalf("unexpected quantities: %#v", demand)
	}
	if len(demand.References) != 2 ||
		demand.References[0].Design != "control" ||
		demand.References[1].Design != "power" {
		t.Fatalf("unexpected references: %#v", demand.References)
	}
}

func TestAggregateRejectsInvalidScaling(t *testing.T) {
	t.Parallel()
	tooMuch, _ := money.Parse("1.000001")
	if _, _, err := Aggregate(nil, 1, tooMuch); err == nil {
		t.Fatal("attrition above one should fail")
	}
	if _, _, err := Aggregate(nil, 0, 0); err == nil {
		t.Fatal("zero units should fail")
	}
}

func TestAggregateWarnsOnConflictingMetadataAndFillsEmpty(t *testing.T) {
	t.Parallel()
	// Package and pins are compatibility-critical inputs to downstream
	// matching: when two designs disagree, dropping one silently hides an
	// engineering conflict. The first value wins deterministically, a
	// stable warning surfaces the disagreement, and empty fields are
	// filled from whichever design supplies them.
	package0402 := "0402"
	package0603 := "0603"
	description := "10k resistor"
	pins := 2
	designs := []contract.Design{
		{
			Design: "control",
			Parts: []contract.Part{{
				PartNumber:   "RC0402",
				Manufacturer: "Yageo",
				Quantity:     1,
				Package:      &package0402,
			}},
		},
		{
			Design: "power",
			Parts: []contract.Part{{
				PartNumber:   "RC0402",
				Manufacturer: "Yageo",
				Quantity:     1,
				Package:      &package0603,
				Description:  &description,
				Pins:         &pins,
			}},
		},
	}
	demands, warnings, err := Aggregate(designs, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(demands) != 1 {
		t.Fatalf("demands = %#v", demands)
	}
	demand := demands[0]
	if demand.Package != "0402" {
		t.Fatalf("package = %q, want first value 0402", demand.Package)
	}
	if demand.Description != "10k resistor" || demand.Pins != 2 {
		t.Fatalf("empty metadata was not filled: %#v", demand)
	}
	if len(warnings) != 1 || warnings[0].Code != "AGGREGATION_METADATA_CONFLICT" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if !strings.Contains(warnings[0].Message, "RC0402") ||
		!strings.Contains(warnings[0].Message, "0603") {
		t.Fatalf("warning lacks context: %q", warnings[0].Message)
	}
}
