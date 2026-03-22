"""Tests for report writers."""

import csv
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from models import BomSummary, PricedPart
from report import EXTENDED_PRICE_INDEX, write_csv, write_json


def _make_parts() -> list[PricedPart]:
    return [
        PricedPart(
            part_number="R1",
            manufacturer="Yageo",
            quantity_per_unit=2,
            total_quantity=200,
            unit_price=0.05,
            extended_price=10.0,
            currency="EUR",
        )
    ]


class TestWriteCsv:
    def test_creates_parent_directory_and_writes_summary(self, tmp_path):
        parts = _make_parts()
        summary = BomSummary.from_parts(parts, units=100)
        output = tmp_path / "nested" / "reports" / "bom.csv"

        write_csv(parts, output, summary)

        assert output.exists()
        with output.open(newline="", encoding="utf-8") as f:
            rows = list(csv.reader(f))

        total_cost_row = next(row for row in rows if row and row[0] == "TOTAL COST")
        assert total_cost_row[EXTENDED_PRICE_INDEX] == "10.00"


class TestWriteJson:
    def test_creates_parent_directory_and_serializes_summary(self, tmp_path):
        parts = _make_parts()
        summary = BomSummary.from_parts(parts, units=100)
        output = tmp_path / "nested" / "reports" / "bom.json"

        write_json(parts, output, summary)

        assert output.exists()
        payload = json.loads(output.read_text(encoding="utf-8"))
        assert payload["total_cost"] == 10.0
        assert payload["parts"][0]["part_number"] == "R1"
