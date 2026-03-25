"""Persistent cache for distributor lookup responses.

The cache stores raw distributor responses instead of final chosen matches so
resolver heuristics and pricing policy can evolve without invalidating all
previously fetched source data. Mouser keeps its historical search-specific API
for compatibility, while Digi-Key and TI use the generic provider/key storage.
"""

import json
import logging
import os
import sqlite3
import sys
import time
from pathlib import Path
from typing import Any

log = logging.getLogger(__name__)


class LookupCache:
    """SQLite-backed TTL cache for raw distributor responses.

    The cache exposes a Mouser-specific search API for backward compatibility
    plus a generic provider/key JSON interface for other distributors.
    """

    def __init__(self, ttl_seconds: int = 24 * 60 * 60, db_path: Path | None = None):
        """Create or open the persistent lookup cache database.

        Parameters
        ----------
        ttl_seconds:
            Time-to-live window for cached entries.
        db_path:
            Optional explicit SQLite database path. When omitted, the module
            uses :func:`default_cache_db_path`.
        """
        self.ttl_seconds = ttl_seconds
        self.db_path = db_path or default_cache_db_path()
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(self.db_path)
        self._conn.execute(
            """
            CREATE TABLE IF NOT EXISTS mouser_search_cache (
                part_number TEXT NOT NULL,
                search_option TEXT NOT NULL,
                fetched_at INTEGER NOT NULL,
                response_json TEXT NOT NULL,
                PRIMARY KEY (part_number, search_option)
            )
            """
        )
        self._conn.execute(
            """
            CREATE TABLE IF NOT EXISTS provider_response_cache (
                provider TEXT NOT NULL,
                cache_key TEXT NOT NULL,
                fetched_at INTEGER NOT NULL,
                response_json TEXT NOT NULL,
                PRIMARY KEY (provider, cache_key)
            )
            """
        )
        self._conn.commit()
        self.purge_expired()

    def close(self) -> None:
        """Close the underlying SQLite connection."""
        self._conn.close()

    def _cutoff(self) -> int:
        """Return the earliest still-valid fetch timestamp."""
        return int(time.time()) - self.ttl_seconds

    def _decode_payload(
        self,
        response_json: str,
        *,
        provider: str,
        cache_key: str,
    ) -> Any | None:
        """Decode one cached JSON payload and handle corruption safely."""
        try:
            return json.loads(response_json)
        except json.JSONDecodeError as e:
            log.warning(
                "Invalid cached %s payload for %s: %s",
                provider,
                cache_key,
                e,
            )
            self.delete_provider_response(provider, cache_key)
            return None

    def get(self, part_number: str, search_option: str) -> list[dict] | None:
        """Return cached search results when present and still fresh.

        Invalid JSON payloads are treated as cache corruption: the bad entry is
        deleted and ``None`` is returned so the caller can refetch from Mouser.
        """
        row = self._conn.execute(
            """
            SELECT response_json
            FROM mouser_search_cache
            WHERE part_number = ? AND search_option = ? AND fetched_at >= ?
            """,
            (part_number, search_option, self._cutoff()),
        ).fetchone()

        if row is None:
            return None

        payload = self._decode_payload(
            row[0],
            provider="mouser_search",
            cache_key=f"{part_number}/{search_option}",
        )
        return payload if isinstance(payload, list) else None

    def has(self, part_number: str, search_option: str) -> bool:
        """Return whether a fresh cache entry exists for the lookup key."""
        row = self._conn.execute(
            """
            SELECT 1
            FROM mouser_search_cache
            WHERE part_number = ? AND search_option = ? AND fetched_at >= ?
            """,
            (part_number, search_option, self._cutoff()),
        ).fetchone()
        return row is not None

    def set(self, part_number: str, search_option: str, results: list[dict]) -> None:
        """Store or replace cached raw distributor search results."""
        self._conn.execute(
            """
            INSERT INTO mouser_search_cache (part_number, search_option, fetched_at, response_json)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(part_number, search_option) DO UPDATE SET
                fetched_at = excluded.fetched_at,
                response_json = excluded.response_json
            """,
            (
                part_number,
                search_option,
                int(time.time()),
                json.dumps(results),
            ),
        )
        self._conn.commit()

    def delete(self, part_number: str, search_option: str) -> None:
        """Delete a cached entry for one lookup key."""
        self._conn.execute(
            """
            DELETE FROM mouser_search_cache
            WHERE part_number = ? AND search_option = ?
            """,
            (part_number, search_option),
        )
        self._conn.commit()

    def purge_expired(self) -> int:
        """Delete expired entries and return the number removed."""
        cutoff = self._cutoff()
        removed = 0
        cursor = self._conn.execute(
            "DELETE FROM mouser_search_cache WHERE fetched_at < ?",
            (cutoff,),
        )
        removed += cursor.rowcount
        cursor = self._conn.execute(
            "DELETE FROM provider_response_cache WHERE fetched_at < ?",
            (cutoff,),
        )
        removed += cursor.rowcount
        self._conn.commit()
        return removed

    def get_provider_response(self, provider: str, cache_key: str) -> Any | None:
        """Return one cached JSON payload for a generic provider/key pair."""
        row = self._conn.execute(
            """
            SELECT response_json
            FROM provider_response_cache
            WHERE provider = ? AND cache_key = ? AND fetched_at >= ?
            """,
            (provider, cache_key, self._cutoff()),
        ).fetchone()
        if row is None:
            return None
        return self._decode_payload(
            row[0],
            provider=provider,
            cache_key=cache_key,
        )

    def has_provider_response(self, provider: str, cache_key: str) -> bool:
        """Return whether a fresh generic provider/key entry exists."""
        row = self._conn.execute(
            """
            SELECT 1
            FROM provider_response_cache
            WHERE provider = ? AND cache_key = ? AND fetched_at >= ?
            """,
            (provider, cache_key, self._cutoff()),
        ).fetchone()
        return row is not None

    def set_provider_response(self, provider: str, cache_key: str, payload: Any) -> None:
        """Store or replace one generic provider/key JSON payload."""
        self._conn.execute(
            """
            INSERT INTO provider_response_cache (provider, cache_key, fetched_at, response_json)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(provider, cache_key) DO UPDATE SET
                fetched_at = excluded.fetched_at,
                response_json = excluded.response_json
            """,
            (
                provider,
                cache_key,
                int(time.time()),
                json.dumps(payload),
            ),
        )
        self._conn.commit()

    def delete_provider_response(self, provider: str, cache_key: str) -> None:
        """Delete one generic provider/key cache entry."""
        self._conn.execute(
            """
            DELETE FROM provider_response_cache
            WHERE provider = ? AND cache_key = ?
            """,
            (provider, cache_key),
        )
        self._conn.commit()


def default_cache_db_path() -> Path:
    """Return the default on-disk cache location for the current platform.

    The path honors ``BOM_BUILDER_CACHE_DB`` when set, otherwise it uses the
    platform's conventional cache directory. The historical filename remains
    ``mouser_cache.sqlite3`` for backward compatibility even though the cache
    now stores responses from multiple distributors.
    """
    override = os.getenv("BOM_BUILDER_CACHE_DB", "").strip()
    if override:
        return Path(override).expanduser()

    if os.name == "nt":
        base = Path(os.getenv("LOCALAPPDATA", Path.home() / "AppData" / "Local"))
    elif os.getenv("XDG_CACHE_HOME"):
        base = Path(os.environ["XDG_CACHE_HOME"])
    elif sys.platform == "darwin":
        base = Path.home() / "Library" / "Caches"
    else:
        base = Path.home() / ".cache"

    return base / "bom-builder" / "mouser_cache.sqlite3"
