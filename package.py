"""Package and pin-count inference for distributor part metadata.

The Mouser API does not always expose a clean normalized package field, so the
resolver has to infer package information from several imperfect sources. This
module centralizes that logic and applies it in a stable priority order:

1. parse the Mouser description text
2. inspect the Mouser image URL, which often embeds package names
3. decode manufacturer-specific orderable-package suffixes from the MPN

Package regexes and vendor code tables are loaded from ``packages.yaml`` when
available, with hardcoded defaults acting as a safe fallback.
"""

import logging
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

from config import DATA_DIR

log = logging.getLogger(__name__)


@dataclass(frozen=True)
class PackagePattern:
    """Compiled package-matching rule used by the inference pipeline.

    Attributes
    ----------
    regex:
        Case-insensitive pattern used to search free-form text.
    name:
        Output package name template. Some templates contain ``{0}`` and are
        filled with the captured pin count.
    pins:
        Fixed pin count for the package when known independently of the regex
        capture group.
    """

    regex: re.Pattern[str]
    name: str
    pins: int | None


# ---------------------------------------------------------------------------
# Package pattern loading
# ---------------------------------------------------------------------------


def _load_package_config(yaml_path: Path | None = None) -> dict[str, Any]:
    """Load package patterns and vendor codes from YAML.

    Parameters
    ----------
    yaml_path:
        Optional explicit configuration path. When omitted, the function reads
        ``packages.yaml`` from the project data directory.

    Returns
    -------
    dict[str, Any]
        Configuration dictionary containing pattern and vendor-code sections.

    Notes
    -----
    Invalid or missing configuration falls back to the built-in defaults so
    package extraction never becomes a hard startup dependency.
    """
    yaml_path = yaml_path or DATA_DIR / "packages.yaml"
    if yaml_path.exists():
        try:
            with open(yaml_path) as f:
                data = yaml.safe_load(f)
            if isinstance(data, dict):
                return data
        except yaml.YAMLError as e:
            log.warning("Failed to parse %s, using defaults: %s", yaml_path, e)

    return _default_package_config()


def _default_package_config() -> dict[str, Any]:
    """Return the built-in fallback package configuration."""
    return {
        "patterns": [
            # QFP variants
            {"regex": r"\bLQFP[- ]?(\d+)\b", "name": "LQFP-{0}", "pins": None},
            {"regex": r"\bTQFP[- ]?(\d+)\b", "name": "TQFP-{0}", "pins": None},
            {"regex": r"\bQFP[- ]?(\d+)\b", "name": "QFP-{0}", "pins": None},
            {"regex": r"\bQFN[- ]?(\d+)\b", "name": "QFN-{0}", "pins": None},
            {"regex": r"\bWQFN[- ]?(\d+)\b", "name": "WQFN-{0}", "pins": None},
            {"regex": r"\bDFN[- ]?(\d+)\b", "name": "DFN-{0}", "pins": None},
            {"regex": r"\bBGA[- ]?(\d+)\b", "name": "BGA-{0}", "pins": None},
            {"regex": r"\bWLCSP[- ]?(\d+)\b", "name": "WLCSP-{0}", "pins": None},
            # SOIC / SOP variants
            {"regex": r"\b(\d+)[- ]?SOIC\b", "name": "SOIC-{0}", "pins": None},
            {"regex": r"\bSOIC[- ]?(\d+)\b", "name": "SOIC-{0}", "pins": None},
            {"regex": r"\bHSOIC[- ]?(\d+)\b", "name": "HSOIC-{0}", "pins": None},
            {"regex": r"\bSOP[- ]?(\d+)\b", "name": "SOP-{0}", "pins": None},
            {"regex": r"\bSSOP[- ]?(\d+)\b", "name": "SSOP-{0}", "pins": None},
            {"regex": r"\bTSSOP[- ]?(\d+)\b", "name": "TSSOP-{0}", "pins": None},
            {"regex": r"\b(\d+)[- ]?MSOP\b", "name": "MSOP-{0}", "pins": None},
            {"regex": r"\bMSOP[- ]?(\d+)\b", "name": "MSOP-{0}", "pins": None},
            {"regex": r"\bVSSOP[- ]?(\d+)\b", "name": "VSSOP-{0}", "pins": None},
            {"regex": r"\bVSON[- ]?(\d+)\b", "name": "VSON-{0}", "pins": None},
            {"regex": r"\bSON[- ]?(\d+)\b", "name": "SON-{0}", "pins": None},
            {"regex": r"\bWSON[- ]?(\d+)\b", "name": "WSON-{0}", "pins": None},
            {"regex": r"\bX2SON[- ]?(\d+)\b", "name": "X2SON-{0}", "pins": None},
            {"regex": r"\bSO[- ]?(\d+)\b", "name": "SO-{0}", "pins": None},
            # SOT variants (specific before generic)
            {"regex": r"\b(\d+)[- ]?SOT[- ]?23\b", "name": "SOT-23-{0}", "pins": None},
            {"regex": r"\bSOT[- ]?23[- ]?(\d+)\b", "name": "SOT-23-{0}", "pins": None},
            {"regex": r"\bSOT[- ]?23\b", "name": "SOT-23", "pins": 3},
            {"regex": r"\bSOT[- ]?223\b", "name": "SOT-223", "pins": 4},
            {"regex": r"\bSOT[- ]?363\b", "name": "SOT-363", "pins": 6},
            {"regex": r"\bSOT[- ]?563\b", "name": "SOT-563", "pins": 6},
            {"regex": r"\bSOT[- ]?89\b", "name": "SOT-89", "pins": 3},
            {"regex": r"\bSC[- ]?70[- ]?(\d+)\b", "name": "SC-70-{0}", "pins": None},
            {"regex": r"\bSC[- ]?70\b", "name": "SC-70", "pins": 5},
            # Chip passives (imperial sizes)
            {"regex": r"\b01005\b", "name": "01005", "pins": 2},
            {"regex": r"\b0201\b", "name": "0201", "pins": 2},
            {"regex": r"\b0402\b", "name": "0402", "pins": 2},
            {"regex": r"\b0603\b", "name": "0603", "pins": 2},
            {"regex": r"\b0805\b", "name": "0805", "pins": 2},
            {"regex": r"\b1206\b", "name": "1206", "pins": 2},
            {"regex": r"\b1210\b", "name": "1210", "pins": 2},
            {"regex": r"\b1812\b", "name": "1812", "pins": 2},
            {"regex": r"\b2010\b", "name": "2010", "pins": 2},
            {"regex": r"\b2512\b", "name": "2512", "pins": 2},
            # DIP
            {"regex": r"\bDIP[- ]?(\d+)\b", "name": "DIP-{0}", "pins": None},
            {"regex": r"\bPDIP[- ]?(\d+)\b", "name": "PDIP-{0}", "pins": None},
            # TO packages
            {"regex": r"\b(\d+)[- ]?TO[- ]?92\b", "name": "TO-92", "pins": None},
            {"regex": r"\bTO[- ]?220\b", "name": "TO-220", "pins": 3},
            {"regex": r"\bTO[- ]?252\b", "name": "TO-252 (DPAK)", "pins": 3},
            {"regex": r"\bTO[- ]?263\b", "name": "TO-263 (D2PAK)", "pins": 3},
            {"regex": r"\bTO[- ]?92\b", "name": "TO-92", "pins": 3},
            {"regex": r"\bD2PAK\b", "name": "TO-263 (D2PAK)", "pins": 3},
            {"regex": r"\bDPAK\b", "name": "TO-252 (DPAK)", "pins": 3},
        ],
        "ti_codes": {
            "DCN": ["SOT-23-8", 8],
            "DCT": ["SOT-23-8", 8],
            "DCK": ["SC-70-5", 5],
            "DCR": ["SOT-6X", 6],
            "DR": ["SOIC-8", 8],
            "DRC": ["VSON-10", 10],
            "DRB": ["VSON-8", 8],
            "DBV": ["SOT-23-5", 5],
            "DDF": ["SOT-23-8", 8],
            "DGK": ["MSOP-8", 8],
            "DGS": ["MSOP-10", 10],
            "DGQ": ["MSOP-10", 10],
            "DGX": ["VSSOP-19", 19],
            "DDA": ["HSOIC-8", 8],
            "DQN": ["X2SON-4", 4],
            "DQX": ["WSON-2", 2],
            "PW": ["TSSOP", None],
            "RGE": ["QFN-24", 24],
            "RGY": ["QFN-14", 14],
            "RHA": ["QFN-40", 40],
            "RSA": ["QFN-40", 40],
            "RHB": ["QFN-32", 32],
            "RTE": ["WQFN-16", 16],
            "RTJ": ["QFN-20", 20],
            "ZXK": ["DSBGA-9", 9],
            "MF": ["SOT-23-5", 5],
            "MFX": ["SOT-23-5", 5],
        },
        "stm_codes": {
            "T": ["LQFP", None],
            "U": ["UFQFPN", None],
            "Y": ["WLCSP", None],
            "H": ["UFBGA", None],
            "C": ["UFQFPN-48", 48],
            "R": ["LQFP-64", 64],
            "V": ["LQFP-100", 100],
            "Z": ["LQFP-144", 144],
            "I": ["LQFP-176", 176],
            "A": ["UFBGA-169", 169],
        },
    }


# Load config once at module level
_PKG_CONFIG = _load_package_config()


def _compile_patterns(raw_patterns: list[dict[str, Any]]) -> tuple[PackagePattern, ...]:
    """Compile raw YAML pattern entries into immutable regex rules."""
    compiled: list[PackagePattern] = []
    for entry in raw_patterns:
        regex = entry.get("regex")
        name = entry.get("name")
        if not regex or not name:
            continue
        compiled.append(
            PackagePattern(
                regex=re.compile(regex, re.IGNORECASE),
                name=name,
                pins=entry.get("pins"),
            )
        )
    return tuple(compiled)


_PACKAGE_PATTERNS = _compile_patterns(_PKG_CONFIG.get("patterns", []))


# ---------------------------------------------------------------------------
# Pattern matching helpers
# ---------------------------------------------------------------------------


def _match_patterns(text: str) -> tuple[str | None, int | None]:
    """Match free-form text against the configured package patterns.

    Parameters
    ----------
    text:
        Description text, image URL fragment, or other normalized string to
        inspect for package cues.

    Returns
    -------
    tuple[str | None, int | None]
        Inferred package name and pin count, or ``(None, None)`` when no rule
        matches.
    """
    if not text:
        return None, None

    for entry in _PACKAGE_PATTERNS:
        m = entry.regex.search(text)
        if m:
            name_template = entry.name
            fixed_pins = entry.pins
            if m.lastindex and "{0}" in name_template:
                pin_str = m.group(1)
                try:
                    pins = int(pin_str) if fixed_pins is None else fixed_pins
                except ValueError:
                    continue
                return name_template.format(pin_str), pins
            else:
                return name_template, fixed_pins

    return None, None


def _extract_from_description(description: str) -> tuple[str | None, int | None]:
    """Extract package information from Mouser description text."""
    return _match_patterns(description)


def _extract_from_image_url(image_url: str) -> tuple[str | None, int | None]:
    """Try to extract package from Mouser's image URL path.

    Mouser image URLs often embed the package name, for example
    ``.../ITP_TI_SOT-23-8_DCN_t.jpg`` or ``.../LQFP_64_t.jpg``.
    """
    if not image_url:
        return None, None
    # Normalize underscores to spaces for matching
    return _match_patterns(image_url.replace("_", " "))


def _extract_from_mpn(mpn: str, manufacturer: str) -> tuple[str | None, int | None]:
    """Extract package information from the manufacturer part number.

    This is the lowest-priority inference source because it relies on
    manufacturer-specific encoding rules, but it is still extremely valuable
    for families such as TI automotive parts where orderable suffixes carry
    package information.
    """
    mfr = manufacturer.lower()

    # TI package codes (e.g. "DCN" in TMP423AQDCNRQ1)
    if "texas instruments" in mfr or mfr == "ti":
        ti_codes = _PKG_CONFIG.get("ti_codes", {})
        for code in sorted(ti_codes, key=len, reverse=True):
            if code.upper() in mpn.upper():
                pkg, pins = ti_codes[code]
                return pkg, pins

    # STM32 package codes: STM32F405RGT6
    #   STM32 = family, F405 = subfamily, R = pin count (64), G = flash size,
    #   T = package type (LQFP), 6 = temperature range.
    # We extract the package letter (second-to-last char) and pin count letter.
    if "stmicroelectronics" in mfr or mfr == "st":
        # Match: STM32 + subfamily + pin_letter + flash_letter + pkg_letter + temp_digit
        m = re.match(r"STM32\w+?([A-Z])([A-Z])([A-Z])(\d)$", mpn.upper())
        if m:
            pkg_letter = m.group(3)  # T=LQFP, U=UFQFPN, etc.
            pin_letter = m.group(1)  # R=64, V=100, Z=144, etc.
            stm_codes = _PKG_CONFIG.get("stm_codes", {})
            # STM pin count encoding
            stm_pin_counts = _PKG_CONFIG.get("stm_pin_counts", {
                "C": 48, "R": 64, "V": 100, "Z": 144, "I": 176, "A": 169, "B": 208,
            })
            if pkg_letter in stm_codes:
                pkg, _ = stm_codes[pkg_letter]
                pin_count = stm_pin_counts.get(pin_letter)
                if pin_count:
                    return f"{pkg}-{pin_count}", pin_count
                return pkg, None

    return None, None


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def extract_package_info(
    mouser_part: dict[str, Any], manufacturer: str
) -> tuple[str | None, int | None]:
    """Extract package and pin count from Mouser part data.

    Parameters
    ----------
    mouser_part:
        Raw Mouser API part dictionary.
    manufacturer:
        Manufacturer name from the BOM or distributor result, used for
        vendor-specific MPN decoding.

    Returns
    -------
    tuple[str | None, int | None]
        Inferred package name and pin count, or ``(None, None)`` when the
        available metadata is insufficient.

    Notes
    -----
    Source priority is:

    1. Mouser description text
    2. Mouser product image URL
    3. Manufacturer part number decoding
    """
    description = mouser_part.get("Description", "")
    image_url = mouser_part.get("ImagePath", "")
    mpn = mouser_part.get("ManufacturerPartNumber", "")

    for extractor, source in [
        (_extract_from_description, description),
        (_extract_from_image_url, image_url),
    ]:
        package, pins = extractor(source)
        if package:
            return package, pins

    return _extract_from_mpn(mpn, manufacturer)
