"""Tests for data models."""

import pytest
from pydantic import ValidationError

import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent.parent))

from models import (
    AggregatedPart,
    BomSummary,
    Design,
    DistributorOffer,
    MatchMethod,
    Part,
    PricedPart,
    PurchaseLeg,
)


class TestPart:
    def test_valid_part(self):
        p = Part(part_number="RC0402FR-0710KL", manufacturer="Yageo", quantity=4)
        assert p.part_number == "RC0402FR-0710KL"
        assert p.quantity == 4
        assert p.description is None

    def test_quantity_must_be_positive(self):
        with pytest.raises(ValidationError):
            Part(part_number="X", manufacturer="Y", quantity=0)

    def test_quantity_negative_rejected(self):
        with pytest.raises(ValidationError):
            Part(part_number="X", manufacturer="Y", quantity=-1)

    def test_optional_fields(self):
        p = Part(
            part_number="X", manufacturer="Y", quantity=1,
            description="test", package="0402", pins=2, reference="R1",
        )
        assert p.package == "0402"
        assert p.pins == 2


class TestDesign:
    def test_valid_design(self):
        d = Design(
            design="Test",
            parts=[Part(part_number="X", manufacturer="Y", quantity=1)],
        )
        assert d.design == "Test"
        assert len(d.parts) == 1

    def test_empty_parts_list(self):
        d = Design(design="Empty", parts=[])
        assert len(d.parts) == 0


class TestMatchMethod:
    def test_enum_values(self):
        assert MatchMethod.EXACT.value == "exact"
        assert MatchMethod.FUZZY.value == "fuzzy"
        assert MatchMethod.NOT_FOUND.value == "not_found"

    def test_display_names(self):
        assert "Exact" in MatchMethod.EXACT.display_name
        assert "review" in MatchMethod.FUZZY.display_name.lower()


class TestPricedPart:
    def test_from_aggregated(self):
        agg = AggregatedPart(
            part_number="X", manufacturer="Y",
            quantity_per_unit=3, total_quantity=3000,
            description="test", package="0402",
        )
        priced = PricedPart.from_aggregated(agg)
        assert priced.part_number == "X"
        assert priced.total_quantity == 3000
        assert priced.required_quantity == 3000
        assert priced.package == "0402"
        assert priced.mouser_part_number is None
        assert priced.distributor is None

    def test_price_must_be_non_negative(self):
        with pytest.raises(ValidationError):
            PricedPart(
                part_number="X", manufacturer="Y",
                quantity_per_unit=1, total_quantity=1000,
                unit_price=-1.0,
            )

    def test_apply_selected_offer_sets_generic_fields(self):
        priced = PricedPart(
            part_number="X",
            manufacturer="Y",
            quantity_per_unit=1,
            total_quantity=100,
        )
        offer = DistributorOffer(
            distributor="Digi-Key",
            distributor_part_number="P5555-ND",
            manufacturer_part_number="ECA-1VHG102",
            unit_price=0.5,
            extended_price=50.0,
            currency="EUR",
            required_quantity=100,
            purchased_quantity=110,
            surplus_quantity=10,
            package_type="Tape & Reel",
            packaging_mode="Full Reel",
            packaging_source="digikey_api",
            minimum_order_quantity=100,
            order_multiple=100,
            full_reel_quantity=100,
            pricing_strategy="Better Value",
            order_plan="1 reel x 100",
            purchase_legs=[
                PurchaseLeg(
                    purchased_quantity=100,
                    unit_price=0.5,
                    extended_price=50.0,
                    currency="EUR",
                    price_break_quantity=100,
                    pricing_strategy="Better Value",
                    packaging_mode="Full Reel",
                    order_batch_quantity=100,
                    order_batch_count=1,
                )
            ],
        )

        priced.apply_selected_offer(offer)

        assert priced.distributor == "Digi-Key"
        assert priced.distributor_part_number == "P5555-ND"
        assert priced.manufacturer_part_number == "ECA-1VHG102"
        assert priced.unit_price == 0.5
        assert priced.extended_price == 50.0
        assert priced.required_quantity == 100
        assert priced.purchased_quantity == 110
        assert priced.surplus_quantity == 10
        assert priced.package_type == "Tape & Reel"
        assert priced.packaging_mode == "Full Reel"
        assert priced.packaging_source == "digikey_api"
        assert priced.minimum_order_quantity == 100
        assert priced.order_multiple == 100
        assert priced.full_reel_quantity == 100
        assert priced.pricing_strategy == "Better Value"
        assert priced.order_plan == "1 reel x 100"
        assert len(priced.purchase_legs) == 1
        assert priced.purchase_legs[0].order_batch_count == 1
        assert priced.has_surplus_purchase is True
        assert priced.mouser_part_number is None


class TestBomSummary:
    def test_from_parts(self):
        parts = [
            PricedPart(
                part_number="A", manufacturer="M",
                quantity_per_unit=2, total_quantity=2000,
                extended_price=100.0, currency="EUR",
            ),
            PricedPart(
                part_number="B", manufacturer="M",
                quantity_per_unit=1, total_quantity=1000,
                extended_price=50.0, currency="EUR",
            ),
        ]
        s = BomSummary.from_parts(parts, units=1000)
        assert s.total_parts == 2
        assert s.total_cost == 150.0
        assert s.cost_per_unit == 0.15
        assert s.total_components_per_unit == 3
        assert s.error_count == 0

    def test_from_parts_with_errors(self):
        parts = [
            PricedPart(
                part_number="A", manufacturer="M",
                quantity_per_unit=1, total_quantity=1000,
                lookup_error="Not found",
            ),
        ]
        s = BomSummary.from_parts(parts, units=1000)
        assert s.error_count == 1
        assert s.total_cost == 0
        assert s.priced_count == 0

    def test_zero_units(self):
        parts = [
            PricedPart(
                part_number="A", manufacturer="M",
                quantity_per_unit=1, total_quantity=0,
                extended_price=100.0,
            ),
        ]
        s = BomSummary.from_parts(parts, units=0)
        assert s.cost_per_unit == 0
