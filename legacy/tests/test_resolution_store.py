"""Tests for the persistent manual resolution store."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from resolution_store import ResolutionStore


class TestResolutionStore:
    def test_round_trip_saved_resolution(self, tmp_path):
        store = ResolutionStore(tmp_path / "resolutions.json")
        record = store.set(
            "TI",
            "TMP421-Q1",
            "595-TMP421AQDCNRQ1",
            "TMP421AQDCNRQ1",
        )

        loaded = store.get("TI", "TMP421-Q1")

        assert loaded is not None
        assert loaded.mouser_part_number == record.mouser_part_number
        assert loaded.matches(
            {
                "MouserPartNumber": "595-TMP421AQDCNRQ1",
                "ManufacturerPartNumber": "TMP421AQDCNRQ1",
            }
        )

    def test_lists_and_removes_records_deterministically(self, tmp_path):
        store = ResolutionStore(tmp_path / "resolutions.json")
        store.set("TI", "Z-PART", "595-Z", "Z-PART")
        store.set("NXP", "A-PART", "771-A", "A-PART")

        records = store.list_records()
        removed = store.remove("TI", "Z-PART")

        assert [record.part_number for record in records] == ["A-PART", "Z-PART"]
        assert removed is True
        assert store.get("TI", "Z-PART") is None
        assert store.remove("TI", "Z-PART") is False
