"""Regression tests for cached live Mouser packaging fixtures."""

from __future__ import annotations

import json
import sys
from dataclasses import asdict
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from manufacturer_packaging import is_probably_blocked_page_html
from mouser import (
    _packaging_details_from_candidate,
    _packaging_details_from_product_page_html,
)

FIXTURE_DIR = Path(__file__).parent / "fixtures" / "mouser" / "live"
LIVE_FIXTURE_SLUGS = (
    "aq4020-01ftg",
    "dmth12h007spswq",
    "iautn12s5n018t",
    "smmbt3904l",
)


def _fixture_metadata(slug: str) -> dict:
    return json.loads((FIXTURE_DIR / f"{slug}.metadata.json").read_text())


def _fixture_search(slug: str) -> dict:
    return json.loads((FIXTURE_DIR / f"{slug}.search.json").read_text())


def _fixture_product_html(slug: str) -> str:
    return (FIXTURE_DIR / f"{slug}.product.html").read_text()


def _details_dict(details) -> dict:
    payload = asdict(details)
    payload["full_reel_price_breaks"] = list(payload["full_reel_price_breaks"])
    return payload


@pytest.mark.parametrize("slug", LIVE_FIXTURE_SLUGS)
def test_search_fixture_metadata_matches_parser(slug):
    metadata = _fixture_metadata(slug)
    payload = _fixture_search(slug)
    parts = payload.get("SearchResults", {}).get("Parts", [])
    first_part = parts[0] if parts else {}

    details = _packaging_details_from_candidate(first_part)

    assert len(parts) == metadata["result_count"]
    assert _details_dict(details) == metadata["search_packaging_details"]


@pytest.mark.parametrize("slug", LIVE_FIXTURE_SLUGS)
def test_product_page_fixture_metadata_matches_parser(slug):
    metadata = _fixture_metadata(slug)
    html = _fixture_product_html(slug)

    details = _packaging_details_from_product_page_html(html)

    assert is_probably_blocked_page_html(html) is metadata["product_page_blocked"]
    assert _details_dict(details) == metadata["product_page_packaging_details"]
