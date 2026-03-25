"""Tests for persistent distributor response caching."""

import sqlite3
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from lookup_cache import LookupCache


class TestLookupCache:
    def test_round_trip_cache_entry(self, tmp_path):
        cache = LookupCache(ttl_seconds=3600, db_path=tmp_path / "cache.sqlite3")
        payload = [{"ManufacturerPartNumber": "PART1"}]

        cache.set("PART1", "Exact", payload)

        assert cache.get("PART1", "Exact") == payload
        cache.close()

    def test_expired_entry_is_not_returned(self, tmp_path):
        db_path = tmp_path / "cache.sqlite3"
        cache = LookupCache(ttl_seconds=1, db_path=db_path)
        cache.set("PART1", "Exact", [{"ManufacturerPartNumber": "PART1"}])
        cache.close()

        conn = sqlite3.connect(db_path)
        conn.execute(
            "UPDATE mouser_search_cache SET fetched_at = fetched_at - 3600"
        )
        conn.commit()
        conn.close()

        cache = LookupCache(ttl_seconds=1, db_path=db_path)
        assert cache.get("PART1", "Exact") is None
        cache.close()

    def test_delete_removes_entry(self, tmp_path):
        cache = LookupCache(ttl_seconds=3600, db_path=tmp_path / "cache.sqlite3")
        cache.set("PART1", "Exact", [{"ManufacturerPartNumber": "PART1"}])

        cache.delete("PART1", "Exact")

        assert cache.get("PART1", "Exact") is None
        cache.close()

    def test_has_reports_fresh_entry(self, tmp_path):
        cache = LookupCache(ttl_seconds=3600, db_path=tmp_path / "cache.sqlite3")
        cache.set("PART1", "Exact", [{"ManufacturerPartNumber": "PART1"}])

        assert cache.has("PART1", "Exact") is True
        assert cache.has("PART1", "BeginsWith") is False
        cache.close()

    def test_round_trip_generic_provider_entry(self, tmp_path):
        cache = LookupCache(ttl_seconds=3600, db_path=tmp_path / "cache.sqlite3")
        payload = {"RequestedProduct": "P5555-ND", "RequestedQuantity": 100}

        cache.set_provider_response("digikey_response", "pricing:P5555-ND:100", payload)

        assert (
            cache.get_provider_response("digikey_response", "pricing:P5555-ND:100")
            == payload
        )
        assert cache.has_provider_response("digikey_response", "pricing:P5555-ND:100") is True
        cache.close()
