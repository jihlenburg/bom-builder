"""Persistent store for manually confirmed resolver choices.

Interactive resolution is only valuable if the user's work is remembered. This
module provides a small JSON-backed database that maps a manufacturer plus BOM
part number to a confirmed distributor selection, allowing later runs to reuse
manual decisions automatically.
"""

from __future__ import annotations

import json
import os
import sys
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass(frozen=True)
class ResolutionRecord:
    """One saved mapping from BOM input to a confirmed distributor part.

    Attributes
    ----------
    manufacturer:
        Manufacturer name from the BOM line.
    part_number:
        Original part number from the BOM line.
    mouser_part_number:
        Confirmed Mouser orderable part number.
    manufacturer_part_number:
        Confirmed manufacturer part number associated with the saved choice.
    saved_at:
        UTC timestamp recording when the selection was stored.
    """

    manufacturer: str
    part_number: str
    mouser_part_number: str
    manufacturer_part_number: str
    saved_at: str

    def matches(self, candidate: dict) -> bool:
        """Return whether this record matches a distributor candidate record."""
        mouser_pn = str(candidate.get("MouserPartNumber") or "").strip()
        manufacturer_pn = str(candidate.get("ManufacturerPartNumber") or "").strip()
        return bool(
            (self.mouser_part_number and mouser_pn == self.mouser_part_number)
            or (
                self.manufacturer_part_number
                and manufacturer_pn == self.manufacturer_part_number
            )
        )


class ResolutionStore:
    """JSON-backed store for confirmed part selections.

    The store is intentionally simple: it loads the full JSON document into
    memory, updates it transactionally through a temporary file, and preserves
    selections across both interactive and non-interactive runs.
    """

    def __init__(self, path: Path | None = None):
        """Create the resolution store and load any existing saved data.

        Parameters
        ----------
        path:
            Optional explicit JSON file location. When omitted, the store uses
            :func:`default_resolution_store_path`.
        """
        self.path = path or default_resolution_store_path()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._data = self._read()

    def get(self, manufacturer: str, part_number: str) -> ResolutionRecord | None:
        """Return a saved resolution for one BOM key when present."""
        raw = self._data.get(_resolution_key(manufacturer, part_number))
        if not isinstance(raw, dict):
            return None
        try:
            return ResolutionRecord(**raw)
        except TypeError:
            return None

    def set(
        self,
        manufacturer: str,
        part_number: str,
        mouser_part_number: str,
        manufacturer_part_number: str,
    ) -> ResolutionRecord:
        """Persist a new saved resolution and return the stored record."""
        record = ResolutionRecord(
            manufacturer=manufacturer.strip(),
            part_number=part_number.strip(),
            mouser_part_number=mouser_part_number.strip(),
            manufacturer_part_number=manufacturer_part_number.strip(),
            saved_at=datetime.now(timezone.utc).isoformat(),
        )
        self._data[_resolution_key(manufacturer, part_number)] = asdict(record)
        self._write()
        return record

    def _read(self) -> dict[str, dict]:
        """Read the on-disk JSON store into memory.

        Invalid JSON or unreadable files are treated as an empty store so a
        damaged cache does not block the rest of the BOM workflow.
        """
        if not self.path.exists():
            return {}
        try:
            raw = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return {}
        return raw if isinstance(raw, dict) else {}

    def _write(self) -> None:
        """Atomically rewrite the on-disk JSON store from in-memory data."""
        tmp = self.path.with_suffix(".tmp")
        with tmp.open("w", encoding="utf-8") as f:
            json.dump(self._data, f, indent=2, sort_keys=True)
            f.write("\n")

        if os.name != "nt":
            try:
                tmp.chmod(0o600)
            except OSError:
                pass

        tmp.replace(self.path)
        if os.name != "nt":
            try:
                self.path.chmod(0o600)
            except OSError:
                pass


def default_resolution_store_path() -> Path:
    """Return the default on-disk location for saved manual resolutions."""
    override = os.getenv("BOM_BUILDER_RESOLUTIONS_FILE", "").strip()
    if override:
        return Path(override).expanduser()

    if os.name == "nt":
        base = Path(os.getenv("APPDATA", Path.home() / "AppData" / "Roaming"))
    elif os.getenv("XDG_CONFIG_HOME"):
        base = Path(os.environ["XDG_CONFIG_HOME"])
    elif sys.platform == "darwin":
        base = Path.home() / ".config"
    else:
        base = Path.home() / ".config"

    return base / "bom-builder" / "resolutions.json"


def _resolution_key(manufacturer: str, part_number: str) -> str:
    """Build the normalized dictionary key used for saved resolutions."""
    return f"{manufacturer.strip().lower()}::{part_number.strip().upper()}"
