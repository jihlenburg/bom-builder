"""Persistent cache for raw Mouser search responses.

The cache stores the unmodified Mouser API search payload keyed by
``(part_number, search_option)``. Caching the raw distributor response instead
of the final chosen match is important: lookup heuristics can evolve over time
without invalidating all previously fetched distributor data.
"""

import json
import logging
import os
import sqlite3
import sys
import time
from pathlib import Path

log = logging.getLogger(__name__)


class LookupCache:
    """SQLite-backed TTL cache for Mouser search responses.

    The cache is intentionally tiny in scope. It only knows how to store and
    retrieve search payloads, while resolver policy remains in :mod:`mouser`.
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
        self._conn.commit()
        self.purge_expired()

    def close(self) -> None:
        """Close the underlying SQLite connection."""
        self._conn.close()

    def get(self, part_number: str, search_option: str) -> list[dict] | None:
        """Return cached search results when present and still fresh.

        Invalid JSON payloads are treated as cache corruption: the bad entry is
        deleted and ``None`` is returned so the caller can refetch from Mouser.
        """
        cutoff = int(time.time()) - self.ttl_seconds
        row = self._conn.execute(
            """
            SELECT response_json
            FROM mouser_search_cache
            WHERE part_number = ? AND search_option = ? AND fetched_at >= ?
            """,
            (part_number, search_option, cutoff),
        ).fetchone()

        if row is None:
            return None

        try:
            return json.loads(row[0])
        except json.JSONDecodeError as e:
            log.warning(
                "Invalid cached Mouser payload for %s/%s: %s",
                part_number,
                search_option,
                e,
            )
            self.delete(part_number, search_option)
            return None

    def has(self, part_number: str, search_option: str) -> bool:
        """Return whether a fresh cache entry exists for the lookup key."""
        cutoff = int(time.time()) - self.ttl_seconds
        row = self._conn.execute(
            """
            SELECT 1
            FROM mouser_search_cache
            WHERE part_number = ? AND search_option = ? AND fetched_at >= ?
            """,
            (part_number, search_option, cutoff),
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
        cutoff = int(time.time()) - self.ttl_seconds
        cursor = self._conn.execute(
            "DELETE FROM mouser_search_cache WHERE fetched_at < ?",
            (cutoff,),
        )
        self._conn.commit()
        return cursor.rowcount


def default_cache_db_path() -> Path:
    """Return the default on-disk cache location for the current platform.

    The path honors ``BOM_BUILDER_CACHE_DB`` when set, otherwise it uses the
    platform's conventional cache directory.
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
