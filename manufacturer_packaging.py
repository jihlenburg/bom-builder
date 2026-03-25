"""Manufacturer-page fallback parsing for packaging and ordering metadata.

The Mouser search API is the primary source of packaging constraints, but some
distributors do not expose reel size or pack quantity consistently for every
orderable. This module provides a narrow, opt-in fallback layer that can fetch
and parse selected manufacturer pages when those pages expose stable packaging
metadata.

The implementation is intentionally conservative:

1. only use deterministic manufacturer URL patterns or BOM-family URLs
2. prefer structured embedded data and explicit HTML attributes over free text
3. detect common bot walls / access-denied pages and treat them as no-data
4. return only packaging facts that appear explicit on the manufacturer page
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from html import unescape
from typing import Iterable
from urllib.parse import quote

TI_MANUFACTURERS = {"texas instruments", "ti"}
INFINEON_MANUFACTURERS = {"infineon", "infineon technologies"}
ONSEMI_MANUFACTURERS = {"onsemi", "on semiconductor", "on semi"}
NXP_MANUFACTURERS = {"nxp semiconductors", "nxp", "nxp usa"}
DIODES_MANUFACTURERS = {"diodes incorporated", "diodes inc", "diodes inc."}


@dataclass(frozen=True)
class ManufacturerPackagingDetails:
    """Normalized packaging facts resolved from a manufacturer page."""

    packaging_mode: str | None = None
    packaging_source: str | None = None
    minimum_order_quantity: int | None = None
    order_multiple: int | None = None
    standard_pack_quantity: int | None = None
    full_reel_quantity: int | None = None

    @property
    def is_useful(self) -> bool:
        """Return whether the parsed details contain actionable constraints."""
        return bool(
            self.packaging_mode
            or self.minimum_order_quantity
            or self.order_multiple
            or self.standard_pack_quantity
            or self.full_reel_quantity
        )


def _normalize_manufacturer_name(name: str) -> str:
    """Normalize manufacturer names for adapter matching."""
    return " ".join(name.lower().strip().split())


def _extract_optional_int(value: object) -> int | None:
    """Extract the first positive integer embedded in a loose value."""
    if value in (None, "", False):
        return None
    if isinstance(value, int):
        return value if value > 0 else None
    match = re.search(r"\d[\d,.]*", str(value))
    if not match:
        return None
    digits = re.sub(r"[^\d]", "", match.group())
    if not digits:
        return None
    number = int(digits)
    return number if number > 0 else None


def _merge_mode(parts: Iterable[str | None]) -> str | None:
    """Join distinct non-empty packaging-mode fragments."""
    seen: list[str] = []
    for part in parts:
        if not part:
            continue
        text = " ".join(str(part).split())
        if text and text not in seen:
            seen.append(text)
    return " | ".join(seen) or None


def is_probably_blocked_page_html(html: str) -> bool:
    """Return whether the response looks like a bot wall or error page."""
    lowered = html.lower()
    return any(
        marker in lowered
        for marker in (
            "access to this page has been denied",
            "access denied",
            "enable javascript and cookies to continue",
            "just a moment...",
            "page not available",
            "sorry! this page is not available",
            "__cf_chl_opt",
            "feedback_redirect?refcode=accessdenied",
        )
    )


def packaging_kind_from_text(packaging_text: str) -> str | None:
    """Infer a packaging noun (reel, tray, tube, batch) from free-form text.

    Returns ``None`` when the input is empty or contains no recognizable
    packaging keyword.  The function is intentionally conservative: it only
    returns ``"reel"`` when the text mentions a reel *without* also mentioning
    ``"cut tape"`` or ``"mousereel"`` — both of which indicate cut-tape
    variants that should not be classified as full-reel packaging.
    """
    normalized = packaging_text.lower()
    if "reel" in normalized and "cut tape" not in normalized and "mousereel" not in normalized:
        return "reel"
    if "tray" in normalized:
        return "tray"
    if "tube" in normalized:
        return "tube"
    if normalized:
        return "batch"
    return None


def manufacturer_page_url(
    manufacturer: str,
    *,
    manufacturer_part_number: str | None = None,
    bom_part_number: str | None = None,
) -> str | None:
    """Return a deterministic manufacturer product URL when one is known."""
    normalized = _normalize_manufacturer_name(manufacturer)
    mpn = (manufacturer_part_number or "").strip()
    bom_pn = (bom_part_number or "").strip()
    if not mpn:
        return None

    if normalized in TI_MANUFACTURERS and bom_pn:
        return (
            f"https://www.ti.com/product/{quote(bom_pn)}/part-details/{quote(mpn)}"
        )
    if normalized in INFINEON_MANUFACTURERS:
        return f"https://www.infineon.com/part/{quote(mpn)}"
    if normalized in ONSEMI_MANUFACTURERS:
        return (
            "https://www.onsemi.com/PowerSolutions/availability.do"
            f"?lctn=homeRight&part={quote(mpn)}"
        )
    if normalized in NXP_MANUFACTURERS:
        return f"https://www.nxp.com/part/{quote(mpn)}"
    if normalized in DIODES_MANUFACTURERS:
        return f"https://www.diodes.com/part/view/{quote(mpn)}"
    return None


def manufacturer_packaging_details_from_html(
    manufacturer: str,
    *,
    manufacturer_part_number: str | None = None,
    bom_part_number: str | None = None,
    html: str,
) -> ManufacturerPackagingDetails | None:
    """Parse packaging details from one manufacturer page HTML response."""
    if not html or is_probably_blocked_page_html(html):
        return None

    normalized = _normalize_manufacturer_name(manufacturer)
    for parser in (
        _ti_packaging_details_from_html if normalized in TI_MANUFACTURERS else None,
        _infineon_packaging_details_from_html if normalized in INFINEON_MANUFACTURERS else None,
        _generic_manufacturer_packaging_details_from_html,
    ):
        if parser is None:
            continue
        details = parser(
            manufacturer_part_number=manufacturer_part_number,
            bom_part_number=bom_part_number,
            html=html,
        )
        if details is not None and details.is_useful:
            return details
    return None


def _ti_packaging_details_from_html(
    *,
    manufacturer_part_number: str | None,
    bom_part_number: str | None,
    html: str,
) -> ManufacturerPackagingDetails | None:
    """Parse TI orderable-part pages using explicit attributes and rows."""
    if not manufacturer_part_number:
        return None

    target_opn = manufacturer_part_number.strip().upper()
    unescaped_html = unescape(html)

    add_to_cart_match = re.search(
        rf"<ti-add-to-cart\b[^>]*\bopn=\"{re.escape(target_opn)}\"[^>]*\bpackage-quantity=\"([^\"]+)\"[^>]*>",
        unescaped_html,
        re.IGNORECASE,
    )
    package_quantity = (
        _extract_optional_int(add_to_cart_match.group(1))
        if add_to_cart_match is not None
        else None
    )

    carrier_match = re.search(
        rf"Package qty \| Carrier</span>\s*<a[^>]*\bOPN={re.escape(target_opn)}[^>]*>([^<]+)</a>",
        unescaped_html,
        re.IGNORECASE,
    )
    carrier_text = " ".join(carrier_match.group(1).split()) if carrier_match else None
    carrier_kind = None
    if carrier_text and "|" in carrier_text:
        _, carrier_kind = [part.strip() for part in carrier_text.split("|", 1)]

    package_hint = package_quantity
    if carrier_text and package_hint is None:
        package_hint = _extract_optional_int(carrier_text)

    carrier_lower = (carrier_kind or "").lower()
    packaging_mode = _merge_mode([carrier_kind])
    full_reel_quantity = (
        package_hint
        if any(token in carrier_lower for token in ("large t&r", "full reel", "reel"))
        else None
    )

    details = ManufacturerPackagingDetails(
        packaging_mode=packaging_mode,
        packaging_source="manufacturer_ti_page",
        minimum_order_quantity=None,
        order_multiple=None,
        standard_pack_quantity=package_hint,
        full_reel_quantity=full_reel_quantity,
    )
    return details if details.is_useful else None


def _infineon_target_opn(
    manufacturer_part_number: str | None,
    unescaped_html: str,
) -> str | None:
    """Return the best Infineon orderable-part number visible in the page."""
    if not manufacturer_part_number:
        return None
    target = manufacturer_part_number.strip().upper()
    opn_names = re.findall(r'"opnName"\s*:\s*"([^"]+)"', unescaped_html, re.IGNORECASE)
    exact = next((name for name in opn_names if name.upper() == target), None)
    if exact is not None:
        return exact
    prefix = [
        name for name in opn_names if name.upper().startswith(target)
    ]
    if prefix:
        return sorted(prefix, key=len)[0]
    return None


def _infineon_packaging_details_from_html(
    *,
    manufacturer_part_number: str | None,
    bom_part_number: str | None,
    html: str,
) -> ManufacturerPackagingDetails | None:
    """Parse Infineon product pages from embedded orderable JSON fragments."""
    unescaped_html = unescape(html)
    target_opn = _infineon_target_opn(manufacturer_part_number, unescaped_html)
    if target_opn is None:
        return None

    opn_index = unescaped_html.upper().find(f'"OPNNAME": "{target_opn.upper()}"')
    if opn_index < 0:
        return None
    window = unescaped_html[max(opn_index - 3500, 0): opn_index + 3500]

    functional_packing = _search_window_text(window, "functionalPacking")
    large_packing_unit = _search_window_int(window, "largePackingUnit")
    minimum_order_quantity = _search_window_int(window, "minimumOrderQty")
    order_multiple = _search_window_int(window, "multipleQty")

    full_reel_quantity = (
        large_packing_unit
        if functional_packing and "reel" in functional_packing.lower()
        else None
    )
    details = ManufacturerPackagingDetails(
        packaging_mode=_merge_mode([functional_packing]),
        packaging_source="manufacturer_infineon_page",
        minimum_order_quantity=minimum_order_quantity,
        order_multiple=order_multiple,
        standard_pack_quantity=large_packing_unit,
        full_reel_quantity=full_reel_quantity,
    )
    return details if details.is_useful else None


def _search_window_text(window: str, key: str) -> str | None:
    """Return the nearby JSON string value for one key."""
    match = re.search(
        rf'"{re.escape(key)}"\s*:\s*"([^"]+)"',
        window,
        re.IGNORECASE,
    )
    return match.group(1).strip() if match else None


def _search_window_int(window: str, key: str) -> int | None:
    """Return the nearby integer value for one key."""
    match = re.search(
        rf'"{re.escape(key)}"\s*:\s*([0-9][0-9,]*)',
        window,
        re.IGNORECASE,
    )
    return _extract_optional_int(match.group(1)) if match else None


def _strip_html(html: str) -> str:
    """Return a compact visible-text approximation of one HTML fragment."""
    text = re.sub(r"<[^>]+>", " ", unescape(html))
    return " ".join(text.split())


def _manufacturer_window(
    html: str,
    manufacturer_part_number: str | None,
    *,
    before: int = 2500,
    after: int = 2500,
) -> str:
    """Return a raw HTML window around the target orderable when available."""
    if not manufacturer_part_number:
        return html
    target = manufacturer_part_number.strip()
    if not target:
        return html
    match = re.search(re.escape(target), html, re.IGNORECASE)
    if match is None:
        return html
    start = max(match.start() - before, 0)
    end = min(match.end() + after, len(html))
    return html[start:end]


def _generic_manufacturer_packaging_details_from_html(
    *,
    manufacturer_part_number: str | None,
    bom_part_number: str | None,
    html: str,
) -> ManufacturerPackagingDetails | None:
    """Parse common packaging fields from embedded JSON/attributes on vendor pages."""
    unescaped_html = unescape(html)
    raw_window = _manufacturer_window(unescaped_html, manufacturer_part_number)
    visible_window = _strip_html(raw_window)
    packaging_mode = _merge_mode(
        [
            _search_window_text(raw_window, "functionalPacking"),
            _search_window_text(raw_window, "packingType"),
            _search_window_text(raw_window, "carrier"),
        ]
    )
    minimum_order_quantity = _search_window_int(raw_window, "minimumOrderQty")
    order_multiple = _search_window_int(raw_window, "multipleQty")
    standard_pack_quantity = _search_window_int(raw_window, "largePackingUnit")
    if standard_pack_quantity is None:
        package_quantity_match = re.search(
            r'package-quantity="([^"]+)"',
            raw_window,
            re.IGNORECASE,
        )
        if package_quantity_match:
            standard_pack_quantity = _extract_optional_int(package_quantity_match.group(1))

    carrier_row = re.search(
        r"Package qty \| Carrier(?:</span>)?\s*(?:<a[^>]*>)?([^<\n]+)",
        raw_window,
        re.IGNORECASE,
    )
    if carrier_row and standard_pack_quantity is None:
        standard_pack_quantity = _extract_optional_int(carrier_row.group(1))
    if carrier_row and packaging_mode is None and "|" in carrier_row.group(1):
        packaging_mode = _merge_mode([carrier_row.group(1).split("|", 1)[1].strip()])

    shipping_match = re.search(
        r"\b(?:shipping|qty\.?)\s*([0-9][0-9,]*)\s*(?:\|\s*)?"
        r"(tape\s*&\s*reel|reel|tube|tray|bulk)\b",
        visible_window,
        re.IGNORECASE,
    )
    if shipping_match:
        if standard_pack_quantity is None:
            standard_pack_quantity = _extract_optional_int(shipping_match.group(1))
        if packaging_mode is None:
            packaging_mode = _merge_mode([shipping_match.group(2)])

    container_match = re.search(
        r"\b(tape\s*&\s*reel|reel|tube|tray|bulk)\b\s+([0-9][0-9,]*)\b",
        visible_window,
        re.IGNORECASE,
    )
    if container_match:
        if packaging_mode is None:
            packaging_mode = _merge_mode([container_match.group(1)])
        if standard_pack_quantity is None:
            standard_pack_quantity = _extract_optional_int(container_match.group(2))

    full_reel_quantity = (
        standard_pack_quantity
        if packaging_mode and any(token in packaging_mode.lower() for token in ("reel", "t&r"))
        else None
    )
    details = ManufacturerPackagingDetails(
        packaging_mode=packaging_mode,
        packaging_source="manufacturer_generic_page",
        minimum_order_quantity=minimum_order_quantity,
        order_multiple=order_multiple,
        standard_pack_quantity=standard_pack_quantity,
        full_reel_quantity=full_reel_quantity,
    )
    return details if details.is_useful else None
