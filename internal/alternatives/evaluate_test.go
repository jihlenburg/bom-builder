package alternatives

import (
	"testing"

	"github.com/jihlenburg/bom-builder/internal/money"
	"github.com/jihlenburg/bom-builder/internal/procurement"
)

func TestEvaluateResistorsClassifiesBetterUnknownAndIncompatible(t *testing.T) {
	original := resistorSpec("ORIGINAL")
	compatible := resistorSpec("COMPATIBLE")
	*compatible.TolerancePercent = "0.5"
	*compatible.PowerWatts = "0.1"
	*compatible.VoltageVolts = "75"
	compatible.Qualifications = []string{"AEC-Q200", "Anti-Sulfur"}
	unknown := resistorSpec("UNKNOWN")
	unknown.VoltageVolts = nil
	incompatible := resistorSpec("INCOMPATIBLE")
	incompatible.Package = "0603"

	results := Evaluate(Request{
		Kind:       "resistor",
		Original:   original,
		Candidates: []PartSpec{compatible, unknown, incompatible},
	})
	if results[0].Compatibility != "compatible" ||
		relation(results[0], "tolerance_percent") != "better" ||
		relation(results[0], "qualifications") != "better" {
		t.Fatalf("compatible result = %#v", results[0])
	}
	if results[1].Compatibility != "unknown" ||
		relation(results[1], "voltage_volts") != "unknown" {
		t.Fatalf("unknown result = %#v", results[1])
	}
	if results[2].Compatibility != "incompatible" ||
		relation(results[2], "package") != "worse" {
		t.Fatalf("incompatible result = %#v", results[2])
	}
	for _, result := range results {
		if !result.EngineeringReviewRequired {
			t.Fatal("alternatives must retain engineering review")
		}
	}
}

func TestEvaluateCapacitorTreatsNP0AndC0GAsEquivalent(t *testing.T) {
	original := capacitorSpec("ORIGINAL")
	candidate := capacitorSpec("CANDIDATE")
	original.Dielectric = "NP0"
	candidate.Dielectric = "C0G"
	results := Evaluate(Request{
		Kind: "capacitor", Original: original, Candidates: []PartSpec{candidate},
	})
	if results[0].Compatibility != "compatible" ||
		relation(results[0], "dielectric") != "equal" {
		t.Fatalf("result = %#v", results[0])
	}
}

func TestEvaluateInductorRejectsHigherDCR(t *testing.T) {
	original := inductorSpec("ORIGINAL")
	candidate := inductorSpec("CANDIDATE")
	*candidate.DCResistanceOhms = "0.2"
	results := Evaluate(Request{
		Kind: "inductor", Original: original, Candidates: []PartSpec{candidate},
	})
	if results[0].Compatibility != "incompatible" ||
		relation(results[0], "dc_resistance_ohms") != "worse" {
		t.Fatalf("result = %#v", results[0])
	}
}

func TestRankPrioritizesCompatibilityThenSafeStock(t *testing.T) {
	price, err := money.Parse("1.25")
	if err != nil {
		t.Fatal(err)
	}
	stock := 1000
	results := []Result{
		{
			Candidate:     resistorSpec("SHORT"),
			Compatibility: "compatible",
			Sourcing: CompactSourcing(procurement.SourcedPart{
				Status: "shortage",
				Offer:  &procurement.Offer{AvailableQuantity: &stock},
			}),
		},
		{
			Candidate:     resistorSpec("SAFE"),
			Compatibility: "compatible",
			Sourcing: CompactSourcing(procurement.SourcedPart{
				Status: "priced",
				Offer: &procurement.Offer{
					AvailableQuantity: &stock,
					SelectedPlan: &procurement.PurchasePlan{
						ExtendedPrice: price,
						Currency:      "EUR",
					},
				},
			}),
		},
		{
			Candidate:     resistorSpec("UNKNOWN"),
			Compatibility: "unknown",
			Sourcing: CompactSourcing(procurement.SourcedPart{
				Status: "priced",
				Offer: &procurement.Offer{
					AvailableQuantity: &stock,
					SelectedPlan: &procurement.PurchasePlan{
						ExtendedPrice: price,
						Currency:      "EUR",
					},
				},
			}),
		},
		{
			Candidate:     resistorSpec("BAD"),
			Compatibility: "incompatible",
		},
	}
	Rank(results)
	if results[0].Candidate.PartNumber != "SAFE" ||
		results[0].Rank == nil ||
		*results[0].Rank != 1 ||
		!results[0].RecommendedForReview ||
		results[3].Rank != nil {
		t.Fatalf("ranked results = %#v", results)
	}
}

func TestRankDoesNotPreferPricesAcrossCurrencies(t *testing.T) {
	eur, _ := money.Parse("1")
	usd, _ := money.Parse("0.5")
	results := []Result{
		{
			Candidate:     resistorSpec("EUR"),
			Compatibility: "compatible",
			Sourcing:      pricedSourcing(eur, "EUR"),
		},
		{
			Candidate:     resistorSpec("USD"),
			Compatibility: "compatible",
			Sourcing:      pricedSourcing(usd, "USD"),
		},
	}
	currencies := Rank(results)
	if len(currencies) != 2 ||
		results[0].RecommendedForReview ||
		results[1].RecommendedForReview {
		t.Fatalf("currencies = %v, results = %#v", currencies, results)
	}
}

func resistorSpec(partNumber string) PartSpec {
	return PartSpec{
		PartNumber: partNumber, Manufacturer: "Maker", Package: "0402",
		ResistanceOhms: strptr("10000"), TolerancePercent: strptr("1"),
		PowerWatts: strptr("0.063"), VoltageVolts: strptr("50"),
		TemperatureMinC: intptr(-55), TemperatureMaxC: intptr(155),
		Technology: "thick film", Qualifications: []string{"AEC-Q200"},
	}
}

func capacitorSpec(partNumber string) PartSpec {
	polarized := false
	return PartSpec{
		PartNumber: partNumber, Manufacturer: "Maker", Package: "0603",
		CapacitanceFarads: strptr("0.0000001"), TolerancePercent: strptr("10"),
		VoltageVolts: strptr("50"), Dielectric: "X7R", Polarized: &polarized,
		TemperatureMinC: intptr(-55), TemperatureMaxC: intptr(125),
	}
}

func inductorSpec(partNumber string) PartSpec {
	shielded := true
	return PartSpec{
		PartNumber: partNumber, Manufacturer: "Maker", Package: "1210",
		InductanceHenries: strptr("0.00001"), TolerancePercent: strptr("20"),
		RatedCurrentAmps: strptr("2"), SaturationAmps: strptr("2.5"),
		DCResistanceOhms: strptr("0.1"), Shielded: &shielded,
		TemperatureMinC: intptr(-40), TemperatureMaxC: intptr(125),
	}
}

func relation(result Result, field string) string {
	for _, comparison := range result.Comparisons {
		if comparison.Field == field {
			return comparison.Relation
		}
	}
	return ""
}

func pricedSourcing(
	price money.Decimal,
	currency string,
) *SourcingResult {
	stock := 1000
	return CompactSourcing(procurement.SourcedPart{
		Status: "priced",
		Offer: &procurement.Offer{
			AvailableQuantity: &stock,
			SelectedPlan: &procurement.PurchasePlan{
				ExtendedPrice: price,
				Currency:      currency,
			},
		},
	})
}

func strptr(value string) *string {
	return &value
}

func intptr(value int) *int {
	return &value
}
