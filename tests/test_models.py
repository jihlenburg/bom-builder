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
    MatchMethod,
    Part,
    PricedPart,
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
        assert priced.package == "0402"
        assert priced.mouser_part_number is None

    def test_price_must_be_non_negative(self):
        with pytest.raises(ValidationError):
            PricedPart(
                part_number="X", manufacturer="Y",
                quantity_per_unit=1, total_quantity=1000,
                unit_price=-1.0,
            )


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
