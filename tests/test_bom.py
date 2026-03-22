"""Tests for BOM aggregation engine."""

import json
import tempfile
from pathlib import Path

import pytest

import sys
sys.path.insert(0, str(Path(__file__).parent.parent))

from bom import aggregate_parts, load_design
from models import Design, Part


class TestLoadDesign:
    def test_load_valid_json(self, tmp_path):
        data = {
            "design": "Test",
            "parts": [
                {"part_number": "X", "manufacturer": "Y", "quantity": 1}
            ],
        }
        p = tmp_path / "test.json"
        p.write_text(json.dumps(data))

        design = load_design(p)
        assert design.design == "Test"
        assert len(design.parts) == 1

    def test_load_invalid_json(self, tmp_path):
        p = tmp_path / "bad.json"
        p.write_text("not json {{{")

        with pytest.raises(SystemExit, match="Invalid JSON"):
            load_design(p)

    def test_load_missing_file(self, tmp_path):
        with pytest.raises(SystemExit, match="Cannot read"):
            load_design(tmp_path / "nonexistent.json")


class TestAggregateParts:
    def _make_design(self, name, parts_data):
        parts = [Part(**p) for p in parts_data]
        return Design(design=name, parts=parts)

    def test_single_design_scaling(self):
        d = self._make_design("D1", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 4},
        ])
        result = aggregate_parts([d], units=1000)
        assert len(result) == 1
        assert result[0].quantity_per_unit == 4
        assert result[0].total_quantity == 4000

    def test_attrition_rounds_up(self):
        d = self._make_design("D1", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 1},
        ])
        result = aggregate_parts([d], units=1000, attrition=0.02)
        assert result[0].total_quantity == 1020  # ceil(1000 * 1.02)

    def test_aggregate_across_designs(self):
        d1 = self._make_design("D1", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 2},
        ])
        d2 = self._make_design("D2", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 3},
        ])
        result = aggregate_parts([d1, d2], units=100)
        assert len(result) == 1
        assert result[0].quantity_per_unit == 5
        assert result[0].total_quantity == 500

    def test_different_manufacturers_not_merged(self):
        d = self._make_design("D1", [
            {"part_number": "R1", "manufacturer": "Yageo", "quantity": 1},
            {"part_number": "R1", "manufacturer": "Vishay", "quantity": 1},
        ])
        result = aggregate_parts([d], units=100)
        assert len(result) == 2

    def test_references_merged(self):
        d1 = self._make_design("Board A", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 2, "reference": "R1,R2"},
        ])
        d2 = self._make_design("Board B", [
            {"part_number": "R1", "manufacturer": "Y", "quantity": 1, "reference": "R3"},
        ])
        result = aggregate_parts([d1, d2], units=1)
        assert "Board A: R1,R2" in result[0].reference
        assert "Board B: R3" in result[0].reference

    def test_empty_designs(self):
        result = aggregate_parts([], units=1000)
        assert result == []
