"""Tests for manufacturer-page fallback packaging adapters."""

from __future__ import annotations

import json
import sys
from dataclasses import asdict
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from manufacturer_packaging import (
    is_probably_blocked_page_html,
    manufacturer_packaging_details_from_html,
    manufacturer_page_url,
)

FIXTURE_DIR = Path(__file__).parent / "fixtures" / "manufacturers"


def _fixture_metadata(slug: str) -> dict:
    return json.loads((FIXTURE_DIR / f"{slug}.metadata.json").read_text())


def _fixture_html(slug: str) -> str:
    return (FIXTURE_DIR / f"{slug}.html").read_text()


def _details_dict_or_none(details) -> dict | None:
    return None if details is None else asdict(details)


class TestManufacturerPageUrl:
    def test_ti_uses_bom_family_and_opn(self):
        assert manufacturer_page_url(
            "TI",
            manufacturer_part_number="TMP421AQDCNRQ1",
            bom_part_number="TMP421-Q1",
        ) == "https://www.ti.com/product/TMP421-Q1/part-details/TMP421AQDCNRQ1"

    def test_infineon_uses_direct_part_path(self):
        assert manufacturer_page_url(
            "Infineon",
            manufacturer_part_number="IAUTN12S5N018T",
            bom_part_number="IAUTN12S5N018T",
        ) == "https://www.infineon.com/part/IAUTN12S5N018T"

    def test_onsemi_uses_inventory_page(self):
        assert manufacturer_page_url(
            "onsemi",
            manufacturer_part_number="SMMBT3904LT1G",
            bom_part_number="SMMBT3904L",
        ) == (
            "https://www.onsemi.com/PowerSolutions/availability.do"
            "?lctn=homeRight&part=SMMBT3904LT1G"
        )


class TestManufacturerPageParsing:
    def test_parses_onsemi_availability_row(self):
        html = """
        <html><body>
        <div>SMMBT3904LT1G</div>
        <div>Active SOT-23 (TO-236) 2.90x1.30x1.00, 1.90P 3 REEL 3000 $0.0123</div>
        </body></html>
        """

        details = manufacturer_packaging_details_from_html(
            "onsemi",
            manufacturer_part_number="SMMBT3904LT1G",
            bom_part_number="SMMBT3904L",
            html=html,
        )

        assert details is not None
        assert details.packaging_mode == "REEL"
        assert details.standard_pack_quantity == 3000
        assert details.full_reel_quantity == 3000

    @pytest.mark.parametrize(
        "slug",
        [
            "ti_tps61160drvt",
            "ti_tmp421_q1",
            "infineon_iautn12s5n018t",
        ],
    )
    def test_matches_cached_live_fixture_metadata(self, slug):
        metadata = _fixture_metadata(slug)
        html = _fixture_html(slug)

        details = manufacturer_packaging_details_from_html(
            metadata["manufacturer"],
            manufacturer_part_number=metadata["manufacturer_part_number"],
            bom_part_number=metadata["bom_part_number"],
            html=html,
        )

        assert is_probably_blocked_page_html(html) is metadata["blocked"]
        assert _details_dict_or_none(details) == metadata["packaging_details"]
