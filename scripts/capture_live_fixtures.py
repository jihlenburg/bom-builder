"""Capture cached live fixtures for packaging parsers.

The script records a small set of real distributor/manufacturer responses under
``tests/fixtures`` so regression tests can exercise the parser logic without
network access. It is intentionally deterministic: fixed SKUs, fixed output
paths, and explicit metadata describing what each parser saw at capture time.
"""

from __future__ import annotations

import json
import sys
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path

import httpx

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from manufacturer_packaging import (
    is_probably_blocked_page_html,
    manufacturer_packaging_details_from_html,
    manufacturer_page_url,
)
from mouser import (
    MouserClient,
    _packaging_details_from_candidate,
    _packaging_details_from_product_page_html,
)
from lookup_cache import LookupCache, default_cache_db_path
from secret_store import get_secret, get_secret_values

MOUSER_FIXTURE_DIR = ROOT / "tests" / "fixtures" / "mouser" / "live"
MANUFACTURER_FIXTURE_DIR = ROOT / "tests" / "fixtures" / "manufacturers"


@dataclass(frozen=True)
class MouserFixtureSpec:
    """One cached-live Mouser search fixture definition."""

    slug: str
    query: str
    search_option: str


@dataclass(frozen=True)
class ManufacturerFixtureSpec:
    """One manufacturer-page fixture definition."""

    slug: str
    manufacturer: str
    manufacturer_part_number: str
    bom_part_number: str


MOUSER_SKUS = (
    MouserFixtureSpec("aq4020-01ftg", "AQ4020-01FTG", "Exact"),
    MouserFixtureSpec("dmth12h007spswq", "DMTH12H007SPSWQ", "BeginsWith"),
    MouserFixtureSpec("iautn12s5n018t", "IAUTN12S5N018T", "Exact"),
    MouserFixtureSpec("smmbt3904l", "SMMBT3904L", "BeginsWith"),
)

MANUFACTURER_PAGES = (
    ManufacturerFixtureSpec(
        "ti_tps61160drvt",
        "Texas Instruments",
        "TPS61160DRVT",
        "TPS61160",
    ),
    ManufacturerFixtureSpec(
        "ti_tmp421_q1",
        "Texas Instruments",
        "TMP421AQDCNRQ1",
        "TMP421-Q1",
    ),
    ManufacturerFixtureSpec(
        "infineon_iautn12s5n018t",
        "Infineon Technologies",
        "IAUTN12S5N018T",
        "IAUTN12S5N018T",
    ),
)


def _timestamp() -> str:
    """Return the current UTC timestamp in ISO-8601 format."""
    return datetime.now(UTC).replace(microsecond=0).isoformat()


def _mouser_api_key() -> str:
    """Return one configured Mouser API key for client initialization."""
    keys = get_secret_values("mouser_api_keys")
    if keys:
        return keys[0]
    key = get_secret("mouser_api_key")
    if not key:
        raise SystemExit("Mouser API key not configured")
    return key


def _write_json(path: Path, payload: object) -> None:
    """Write one JSON fixture with stable formatting."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True), encoding="utf-8")


def _write_text(path: Path, text: str) -> None:
    """Write one text fixture."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def _mouser_search_results(
    mouser_client: MouserClient,
    cache: LookupCache,
    fixture: MouserFixtureSpec,
) -> tuple[list[dict], str]:
    """Return Mouser search results from the live API or local cache."""
    try:
        return mouser_client.search(fixture.query, fixture.search_option), "live_api"
    except httpx.HTTPError:
        cached = cache.get(fixture.query, fixture.search_option)
        if cached is None:
            raise
        return cached, "local_cache"


def capture_mouser_live_fixtures(
    http_client: httpx.Client,
    mouser_client: MouserClient,
    cache: LookupCache,
) -> None:
    """Capture live Mouser search payloads and product-page responses."""
    for fixture in MOUSER_SKUS:
        parts, search_source = _mouser_search_results(mouser_client, cache, fixture)
        search_json = {"SearchResults": {"Parts": parts}}
        _write_json(MOUSER_FIXTURE_DIR / f"{fixture.slug}.search.json", search_json)

        first_part = parts[0] if parts else {}
        product_url = str(first_part.get("ProductDetailUrl") or "").strip() or None
        search_details = _packaging_details_from_candidate(first_part) if first_part else None

        product_html = ""
        product_status = None
        product_details = None
        product_blocked = None
        if product_url:
            product_response = http_client.get(product_url, follow_redirects=True)
            product_status = product_response.status_code
            product_html = product_response.text
            product_blocked = is_probably_blocked_page_html(product_html)
            product_details = _packaging_details_from_product_page_html(product_html)
            _write_text(MOUSER_FIXTURE_DIR / f"{fixture.slug}.product.html", product_html)

        metadata = {
            "captured_at": _timestamp(),
            "query": fixture.query,
            "search_option": fixture.search_option,
            "search_source": search_source,
            "result_count": len(parts),
            "product_detail_url": product_url,
            "search_packaging_details": (
                asdict(search_details) if search_details is not None else None
            ),
            "product_page_status": product_status,
            "product_page_blocked": product_blocked,
            "product_page_packaging_details": (
                asdict(product_details) if product_details is not None else None
            ),
            "first_part_manufacturer": first_part.get("Manufacturer"),
            "first_part_manufacturer_part_number": first_part.get("ManufacturerPartNumber"),
        }
        _write_json(MOUSER_FIXTURE_DIR / f"{fixture.slug}.metadata.json", metadata)


def capture_manufacturer_fixtures(client: httpx.Client) -> None:
    """Capture a few manufacturer pages that expose packaging metadata cleanly."""
    for fixture in MANUFACTURER_PAGES:
        url = manufacturer_page_url(
            fixture.manufacturer,
            manufacturer_part_number=fixture.manufacturer_part_number,
            bom_part_number=fixture.bom_part_number,
        )
        if url is None:
            continue
        response = client.get(url, follow_redirects=True)
        if response.status_code >= 400:
            print(
                f"Skipping manufacturer fixture {fixture.manufacturer_part_number}: "
                f"HTTP {response.status_code} from {url}"
            )
            continue
        html = response.text
        _write_text(MANUFACTURER_FIXTURE_DIR / f"{fixture.slug}.html", html)
        details = manufacturer_packaging_details_from_html(
            fixture.manufacturer,
            manufacturer_part_number=fixture.manufacturer_part_number,
            bom_part_number=fixture.bom_part_number,
            html=html,
        )
        metadata = {
            "captured_at": _timestamp(),
            "manufacturer": fixture.manufacturer,
            "manufacturer_part_number": fixture.manufacturer_part_number,
            "bom_part_number": fixture.bom_part_number,
            "url": url,
            "blocked": is_probably_blocked_page_html(html),
            "packaging_details": asdict(details) if details is not None else None,
        }
        _write_json(MANUFACTURER_FIXTURE_DIR / f"{fixture.slug}.metadata.json", metadata)


def main() -> None:
    """Capture all configured live fixtures."""
    cache = LookupCache(
        ttl_seconds=365 * 24 * 60 * 60,
        db_path=default_cache_db_path(),
    )
    try:
        with (
            httpx.Client(
                timeout=30.0,
                headers={"User-Agent": "Mozilla/5.0"},
            ) as http_client,
            MouserClient(
                api_key=_mouser_api_key(),
                cache_enabled=False,
            ) as mouser_client,
        ):
            capture_mouser_live_fixtures(http_client, mouser_client, cache)
            capture_manufacturer_fixtures(http_client)
    finally:
        cache.close()


if __name__ == "__main__":
    main()
