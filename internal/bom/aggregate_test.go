package bom

import (
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
	demands, err := Aggregate(designs, 10, attrition)
	if err != nil {
		t.Fatal(err)
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
	if _, err := Aggregate(nil, 1, tooMuch); err == nil {
		t.Fatal("attrition above one should fail")
	}
	if _, err := Aggregate(nil, 0, 0); err == nil {
		t.Fatal("zero units should fail")
	}
}
