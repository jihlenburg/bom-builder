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
