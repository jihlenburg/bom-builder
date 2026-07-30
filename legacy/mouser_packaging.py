"""Packaging detail extraction from Mouser search payloads and product pages.

This module owns the logic that resolves packaging constraints (MOQ, order
multiples, full-reel quantities, reel price breaks) from Mouser data.  It
handles three layers of packaging information, each progressively richer:

1. **Search API payload** — the Mouser search response includes basic
   packaging fields such as ``Packaging``, ``ReelingAvailability``, and
   ``MinimumOrderQuantity``.
2. **Embedded page JSON** — Mouser product pages embed structured JSON
   inside ``<script>`` tags that often expose full-reel price breaks and
   packaging options not available in the search API.
3. **Visible page text** — as a last resort, the module parses visible
   pricing tables and packaging labels from the rendered page text.

The module also owns the ``MouserPackagingDetails`` dataclass and its
serialization helpers used for persistent caching.
"""

from __future__ import annotations

import json
import logging
import re
from dataclasses import asdict, dataclass
from html import unescape
from html.parser import HTMLParser
from typing import Any
from urllib.parse import urljoin

from manufacturer_packaging import (
    ManufacturerPackagingDetails,
    _extract_optional_int,
    is_probably_blocked_page_html,
)

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Type alias — raw Mouser result dictionaries
# ---------------------------------------------------------------------------

type MouserPart = dict[str, Any]

@dataclass(frozen=True)
class MouserPackagingDetails:
    """Packaging and ordering constraints resolved for one Mouser orderable."""

    packaging_mode: str | None = None
    packaging_source: str | None = None
    minimum_order_quantity: int | None = None
    order_multiple: int | None = None
    standard_pack_quantity: int | None = None
    full_reel_quantity: int | None = None
    full_reel_price_breaks: tuple[dict[str, Any], ...] = ()


def _serialize_mouser_packaging_details(
    details: MouserPackagingDetails | None,
) -> dict[str, Any]:
    """Return a JSON-serializable cache payload for Mouser packaging details."""
    if details is None:
        return {"found": False}
    payload = asdict(details)
    payload["full_reel_price_breaks"] = list(details.full_reel_price_breaks)
    return {"found": True, "details": payload}


def _deserialize_mouser_packaging_details(
    payload: Any,
) -> tuple[bool, MouserPackagingDetails | None]:
    """Decode one cached Mouser packaging-details payload."""
    if not isinstance(payload, dict) or "found" not in payload:
        return False, None
    if not payload.get("found"):
        return True, None
    raw_details = payload.get("details")
    if not isinstance(raw_details, dict):
        return True, None
    raw_breaks = raw_details.get("full_reel_price_breaks") or []
    break_rows = tuple(
        row for row in raw_breaks
        if isinstance(row, dict)
    )
    return (
        True,
        MouserPackagingDetails(
            packaging_mode=str(raw_details.get("packaging_mode") or "").strip() or None,
            packaging_source=str(raw_details.get("packaging_source") or "").strip() or None,
            minimum_order_quantity=_extract_optional_int(raw_details.get("minimum_order_quantity")),
            order_multiple=_extract_optional_int(raw_details.get("order_multiple")),
            standard_pack_quantity=_extract_optional_int(raw_details.get("standard_pack_quantity")),
            full_reel_quantity=_extract_optional_int(raw_details.get("full_reel_quantity")),
            full_reel_price_breaks=break_rows,
        ),
    )


def _serialize_manufacturer_packaging_details(
    details: ManufacturerPackagingDetails | None,
) -> dict[str, Any]:
    """Return a JSON-serializable cache payload for manufacturer packaging details."""
    if details is None:
        return {"found": False}
    return {
        "found": True,
        "details": asdict(details),
    }


def _deserialize_manufacturer_packaging_details(
    payload: Any,
) -> tuple[bool, ManufacturerPackagingDetails | None]:
    """Decode one cached manufacturer packaging-details payload."""
    if not isinstance(payload, dict) or "found" not in payload:
        return False, None
    if not payload.get("found"):
        return True, None
    raw_details = payload.get("details")
    if not isinstance(raw_details, dict):
        return True, None
    return (
        True,
        ManufacturerPackagingDetails(
            packaging_mode=str(raw_details.get("packaging_mode") or "").strip() or None,
            packaging_source=str(raw_details.get("packaging_source") or "").strip() or None,
            minimum_order_quantity=_extract_optional_int(raw_details.get("minimum_order_quantity")),
            order_multiple=_extract_optional_int(raw_details.get("order_multiple")),
            standard_pack_quantity=_extract_optional_int(raw_details.get("standard_pack_quantity")),
            full_reel_quantity=_extract_optional_int(raw_details.get("full_reel_quantity")),
        ),
    )


class _VisibleTextParser(HTMLParser):
    """Small HTML-to-text helper for Mouser product-page fallback parsing."""

    _BLOCK_TAGS = {
        "address",
        "article",
        "aside",
        "br",
        "dd",
        "div",
        "dl",
        "dt",
        "fieldset",
        "figcaption",
        "figure",
        "footer",
        "form",
        "h1",
        "h2",
        "h3",
        "h4",
        "h5",
        "h6",
        "header",
        "hr",
        "li",
        "main",
        "nav",
        "ol",
        "p",
        "section",
        "table",
        "tbody",
        "td",
        "th",
        "thead",
        "tr",
        "ul",
    }

    def __init__(self) -> None:
        """Initialize the parser with an empty text buffer."""
        super().__init__()
        self._parts: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        """Insert line breaks around known block tags."""
        if tag in self._BLOCK_TAGS:
            self._parts.append("\n")

    def handle_endtag(self, tag: str) -> None:
        """Insert line breaks around known block tags."""
        if tag in self._BLOCK_TAGS:
            self._parts.append("\n")

    def handle_data(self, data: str) -> None:
        """Collect visible text nodes as-is for later normalization."""
        self._parts.append(data)

    def text(self) -> str:
        """Return the normalized accumulated text."""
        raw = unescape("".join(self._parts))
        lines = [" ".join(line.split()) for line in raw.splitlines()]
        return "\n".join(line for line in lines if line)


# ---------------------------------------------------------------------------
# Packaging extraction from search payloads and product pages
# ---------------------------------------------------------------------------

def _candidate_product_detail_url(candidate: MouserPart) -> str | None:
    """Return the Mouser product-detail URL when the payload exposes one."""
    for key in ("ProductDetailUrl", "ProductDetailURL", "ProductUrl", "ProductURL"):
        value = candidate.get(key)
        if not value:
            continue
        url = str(value).strip()
        if not url:
            continue
        if url.startswith(("http://", "https://")):
            return url
        return urljoin("https://www.mouser.com", url)
    return None


def _normalized_candidate_fields(candidate: MouserPart) -> dict[str, Any]:
    """Return a key-normalized view of one Mouser result payload."""
    return {
        re.sub(r"[^a-z0-9]", "", str(key).lower()): value
        for key, value in candidate.items()
    }


def _candidate_field_value(candidate: MouserPart, *keys: str) -> Any:
    """Return the first matching raw value among multiple possible field names."""
    normalized = _normalized_candidate_fields(candidate)
    for key in keys:
        value = normalized.get(re.sub(r"[^a-z0-9]", "", key.lower()))
        if value not in (None, ""):
            return value
    return None


def _candidate_field_text(candidate: MouserPart, *keys: str) -> str | None:
    """Return the first matching string field among multiple possible names."""
    value = _candidate_field_value(candidate, *keys)
    if value in (None, ""):
        return None
    text = str(value).strip()
    return text or None


def _packaging_details_from_candidate(candidate: MouserPart) -> MouserPackagingDetails:
    """Extract Mouser packaging constraints directly from search payload fields."""
    packaging_mode = _candidate_field_text(candidate, "Packaging", "PackageType")
    reeling = _candidate_field_text(candidate, "ReelingAvailability", "Reeling Availability")
    minimum_order_quantity = _extract_optional_int(
        _candidate_field_value(candidate, "MinimumOrderQuantity", "Minimum Order Quantity", "Min")
    )
    order_multiple = _extract_optional_int(
        _candidate_field_value(
            candidate,
            "OrderQuantityMultiples",
            "Order Quantity Multiples",
            "OrderMultiple",
            "Multiples",
        )
    )
    standard_pack_quantity = _extract_optional_int(
        _candidate_field_value(
            candidate,
            "StandardPackQuantity",
            "Standard Pack Quantity",
            "FactoryPackQuantity",
            "Factory Pack Quantity",
        )
    )

    full_reel_quantity = _extract_optional_int(reeling)
    packaging_text = " ".join(text for text in [packaging_mode, reeling] if text).lower()
    if full_reel_quantity is None and "full reel" in packaging_text and standard_pack_quantity:
        full_reel_quantity = standard_pack_quantity
    if full_reel_quantity is None and packaging_text and "reel" in packaging_text:
        full_reel_quantity = standard_pack_quantity

    merged_mode = " | ".join(text for text in [packaging_mode, reeling] if text) or None
    return MouserPackagingDetails(
        packaging_mode=merged_mode,
        packaging_source="search_api" if merged_mode or minimum_order_quantity or order_multiple or full_reel_quantity else None,
        minimum_order_quantity=minimum_order_quantity,
        order_multiple=order_multiple,
        standard_pack_quantity=standard_pack_quantity,
        full_reel_quantity=full_reel_quantity,
    )


def _search_details_are_sufficient(details: MouserPackagingDetails) -> bool:
    """Return whether search-payload packaging data is specific enough already."""
    return bool(
        details.minimum_order_quantity
        or details.order_multiple
        or details.full_reel_quantity
        or details.full_reel_price_breaks
    )


def _manufacturer_details_are_sufficient(details: MouserPackagingDetails) -> bool:
    """Return whether later manufacturer fallback is unlikely to add value."""
    return bool(
        details.full_reel_quantity
        or details.full_reel_price_breaks
        or (details.minimum_order_quantity and details.order_multiple)
    )


def _mouser_packaging_details_from_manufacturer_details(
    details: ManufacturerPackagingDetails | None,
) -> MouserPackagingDetails | None:
    """Convert manufacturer-page packaging facts into Mouser detail shape."""
    if details is None or not details.is_useful:
        return None
    return MouserPackagingDetails(
        packaging_mode=details.packaging_mode,
        packaging_source=details.packaging_source,
        minimum_order_quantity=details.minimum_order_quantity,
        order_multiple=details.order_multiple,
        standard_pack_quantity=details.standard_pack_quantity,
        full_reel_quantity=details.full_reel_quantity,
    )


def _should_fetch_product_page_packaging(
    candidate: MouserPart,
    search_details: MouserPackagingDetails,
) -> bool:
    """Return whether a Mouser product page is likely to add pricing detail."""
    if search_details.full_reel_price_breaks:
        return False
    if _candidate_product_detail_url(candidate) is None:
        return False

    reeling = _candidate_field_text(candidate, "ReelingAvailability", "Reeling Availability")
    packaging_text = " ".join(
        text for text in [search_details.packaging_mode, reeling] if text
    ).lower()
    if not _search_details_are_sufficient(search_details):
        return True
    return any(
        token in packaging_text
        for token in ("full reel", "reel", "mousereel", "cut tape")
    )


def _merge_packaging_source(
    primary_source: str | None,
    fallback_source: str | None,
) -> str | None:
    """Return a source label that preserves both API and page enrichment."""
    if not primary_source:
        return fallback_source
    if not fallback_source or fallback_source == primary_source:
        return primary_source
    return f"{primary_source} + {fallback_source}"


def _merge_packaging_details(
    primary: MouserPackagingDetails,
    fallback: MouserPackagingDetails | None,
) -> MouserPackagingDetails:
    """Merge fallback page details into the search-payload packaging details."""
    if fallback is None:
        return primary
    return MouserPackagingDetails(
        packaging_mode=primary.packaging_mode or fallback.packaging_mode,
        packaging_source=_merge_packaging_source(
            primary.packaging_source,
            fallback.packaging_source,
        ),
        minimum_order_quantity=primary.minimum_order_quantity or fallback.minimum_order_quantity,
        order_multiple=primary.order_multiple or fallback.order_multiple,
        standard_pack_quantity=primary.standard_pack_quantity or fallback.standard_pack_quantity,
        full_reel_quantity=primary.full_reel_quantity or fallback.full_reel_quantity,
        full_reel_price_breaks=primary.full_reel_price_breaks or fallback.full_reel_price_breaks,
    )


def _extract_product_page_pricing_currency(lines: list[str]) -> str | None:
    """Return the currency shown in the Mouser pricing section heading."""
    pricing_line = next(
        (line for line in lines if line.startswith("Pricing")),
        None,
    )
    if pricing_line is None:
        return None
    match = re.search(r"Pricing\s*\(([^)]+)\)", pricing_line, re.IGNORECASE)
    if match is None:
        return None
    currency = match.group(1).strip()
    return currency or None


def _extract_product_page_price_sections(
    lines: list[str],
) -> dict[str, list[tuple[str, str]]]:
    """Return pricing subsections keyed by their visible Mouser heading."""
    pricing_index = next(
        (index for index, line in enumerate(lines) if line.startswith("Pricing")),
        None,
    )
    if pricing_index is None:
        return {}

    sections: dict[str, list[tuple[str, str]]] = {}
    current_heading: str | None = None
    row_pattern = re.compile(
        r"^\s*([\d.,]+)\s+([€$]?\s*[\d.,]+(?:\s*(?:€|EUR|\$|USD))?)\s+[€$]?\s*[\d.,]+(?:\s*(?:€|EUR|\$|USD))?\s*$",
        re.IGNORECASE,
    )
    stop_prefixes = (
        "Pricing Choice",
        "Packaging Choice",
        "Alternative Packaging",
        "†",
        "Close",
    )

    for line in lines[pricing_index + 1:]:
        if line.startswith(stop_prefixes):
            break
        if not line or line.startswith("Qty."):
            continue

        match = row_pattern.match(line)
        if match:
            if current_heading is not None:
                sections.setdefault(current_heading, []).append(
                    (match.group(1), match.group(2))
                )
            continue

        current_heading = line
        sections.setdefault(current_heading, [])

    return sections


def _extract_json_fragments(script_text: str) -> list[Any]:
    """Return parseable JSON objects or arrays embedded inside one script body."""
    decoder = json.JSONDecoder()
    payloads: list[Any] = []
    seen: set[str] = set()
    index = 0
    while index < len(script_text):
        if script_text[index] not in "{[":
            index += 1
            continue
        try:
            payload, end_index = decoder.raw_decode(script_text[index:])
        except json.JSONDecodeError:
            index += 1
            continue
        try:
            marker = json.dumps(payload, sort_keys=True)
        except (TypeError, ValueError):
            marker = ""
        if marker not in seen:
            payloads.append(payload)
            if marker:
                seen.add(marker)
        index += max(end_index, 1)
    return payloads


def _extract_json_script_payloads(html: str) -> list[Any]:
    """Return structured payloads embedded in Mouser product-page scripts."""
    payloads: list[Any] = []
    for match in re.finditer(
        r"<script\b([^>]*)>(.*?)</script>",
        html,
        re.IGNORECASE | re.DOTALL,
    ):
        attributes = match.group(1) or ""
        script_text = unescape(match.group(2)).strip()
        if not script_text:
            continue

        is_json_script = bool(
            re.search(
                r"type=[\"']application/(?:ld\+)?json[\"']",
                attributes,
                re.IGNORECASE,
            )
        )
        if is_json_script:
            try:
                payloads.append(json.loads(script_text))
            except json.JSONDecodeError:
                payloads.extend(_extract_json_fragments(script_text))
            continue

        if not any(
            token in script_text
            for token in (
                '"packaging"',
                '"reelingAvailability"',
                '"minimumOrderQuantity"',
                '"orderQuantityMultiples"',
                '"standardPackQuantity"',
                '"fullReelQuantity"',
                '"packagingOptions"',
                '"priceBreaks"',
            )
        ):
            continue
        payloads.extend(_extract_json_fragments(script_text))
    return payloads


def _iter_embedded_packaging_records(node: Any) -> Any:
    """Yield nested dict nodes that look like packaging metadata carriers."""
    if isinstance(node, dict):
        normalized_keys = {
            re.sub(r"[^a-z0-9]", "", str(key).lower())
            for key in node
        }
        if normalized_keys & {
            "packaging",
            "reelingavailability",
            "minimumorderquantity",
            "orderquantitymultiples",
            "standardpackquantity",
            "fullreelquantity",
            "packagingoptions",
            "pricebreaks",
        }:
            yield node
        for value in node.values():
            yield from _iter_embedded_packaging_records(value)
    elif isinstance(node, list):
        for value in node:
            yield from _iter_embedded_packaging_records(value)


def _embedded_record_to_candidate(record: dict[str, Any]) -> dict[str, Any]:
    """Map one embedded JSON record into candidate-like Mouser fields."""
    candidate: dict[str, Any] = {}
    for key, value in record.items():
        normalized = re.sub(r"[^a-z0-9]", "", str(key).lower())
        if normalized == "packaging":
            candidate["Packaging"] = value
        elif normalized == "reelingavailability":
            candidate["ReelingAvailability"] = value
        elif normalized == "minimumorderquantity":
            candidate["MinimumOrderQuantity"] = value
        elif normalized in {"orderquantitymultiples", "ordermultiple"}:
            candidate["OrderQuantityMultiples"] = value
        elif normalized in {"standardpackquantity", "fullreelquantity"}:
            candidate["StandardPackQuantity"] = value
    return candidate


def _embedded_full_reel_price_breaks(record: dict[str, Any]) -> tuple[dict[str, Any], ...]:
    """Extract explicit full-reel price breaks from embedded JSON records."""
    options = record.get("packagingOptions")
    if isinstance(options, list):
        for option in options:
            if not isinstance(option, dict):
                continue
            packaging_text = " ".join(
                str(option.get(key) or "")
                for key in ("label", "packaging", "reelingAvailability", "name")
            ).lower()
            if "full reel" not in packaging_text and "reel" not in packaging_text:
                continue
            price_breaks = option.get("priceBreaks")
            if isinstance(price_breaks, list):
                return tuple(
                    price_break
                    for price_break in price_breaks
                    if isinstance(price_break, dict)
                )
    price_breaks = record.get("fullReelPriceBreaks")
    if isinstance(price_breaks, list):
        return tuple(
            price_break
            for price_break in price_breaks
            if isinstance(price_break, dict)
        )
    return ()


def _packaging_details_from_embedded_product_page_html(
    html: str,
) -> MouserPackagingDetails | None:
    """Parse packaging facts from structured JSON embedded in a product page."""
    payloads = _extract_json_script_payloads(html)
    details: MouserPackagingDetails | None = None
    for payload in payloads:
        for record in _iter_embedded_packaging_records(payload):
            if not isinstance(record, dict):
                continue
            candidate = _embedded_record_to_candidate(record)
            if not candidate:
                continue
            parsed = _packaging_details_from_candidate(candidate)
            if not parsed.packaging_source:
                continue
            parsed = MouserPackagingDetails(
                packaging_mode=parsed.packaging_mode,
                packaging_source="product_page_embedded",
                minimum_order_quantity=parsed.minimum_order_quantity,
                order_multiple=parsed.order_multiple,
                standard_pack_quantity=parsed.standard_pack_quantity,
                full_reel_quantity=(
                    parsed.full_reel_quantity
                    or _extract_optional_int(record.get("fullReelQuantity"))
                ),
                full_reel_price_breaks=_embedded_full_reel_price_breaks(record),
            )
            details = parsed if details is None else _merge_packaging_details(details, parsed)

    attribute_candidate: dict[str, Any] = {}
    attribute_map = {
        "packaging": "Packaging",
        "reeling-availability": "ReelingAvailability",
        "minimum-order-quantity": "MinimumOrderQuantity",
        "order-multiple": "OrderQuantityMultiples",
        "standard-pack-quantity": "StandardPackQuantity",
        "full-reel-quantity": "StandardPackQuantity",
    }
    for attr_name, candidate_key in attribute_map.items():
        match = re.search(
            rf'data-{attr_name}="([^"]+)"',
            html,
            re.IGNORECASE,
        )
        if match:
            attribute_candidate[candidate_key] = unescape(match.group(1))
    if attribute_candidate:
        parsed = _packaging_details_from_candidate(attribute_candidate)
        parsed = MouserPackagingDetails(
            packaging_mode=parsed.packaging_mode,
            packaging_source="product_page_embedded",
            minimum_order_quantity=parsed.minimum_order_quantity,
            order_multiple=parsed.order_multiple,
            standard_pack_quantity=parsed.standard_pack_quantity,
            full_reel_quantity=parsed.full_reel_quantity,
            full_reel_price_breaks=(),
        )
        details = parsed if details is None else _merge_packaging_details(details, parsed)

    return details if details and _search_details_are_sufficient(details) else None


def _packaging_details_from_product_page_html(html: str) -> MouserPackagingDetails:
    """Parse full-reel constraints and price breaks from a Mouser product page."""
    if is_probably_blocked_page_html(html):
        return MouserPackagingDetails()
    embedded_details = _packaging_details_from_embedded_product_page_html(html)

    parser = _VisibleTextParser()
    parser.feed(html)
    text = parser.text()
    lines = text.splitlines()

    minimum_order_quantity: int | None = None
    order_multiple: int | None = None
    packaging_mode: str | None = None
    full_reel_quantity: int | None = None

    min_match = re.search(
        r"Minimum:\s*([\d,.]+)\s+Multiples:\s*([\d,.]+)",
        text,
        re.IGNORECASE,
    )
    if min_match:
        minimum_order_quantity = _extract_optional_int(min_match.group(1))
        order_multiple = _extract_optional_int(min_match.group(2))

    full_reel_match = re.search(
        r"Full Reel\s*\(Order in multiples of\s*([\d,.]+)\)",
        text,
        re.IGNORECASE,
    )
    if full_reel_match:
        full_reel_quantity = _extract_optional_int(full_reel_match.group(1))

    packaging_lines: list[str] = []
    for index, line in enumerate(lines):
        if line == "Packaging:":
            for next_line in lines[index + 1:index + 5]:
                if next_line.startswith("Pricing"):
                    break
                packaging_lines.append(next_line)
            break
    if packaging_lines:
        packaging_mode = " | ".join(packaging_lines)

    pricing_currency = _extract_product_page_pricing_currency(lines)
    full_reel_price_breaks = tuple(
        {
            "Quantity": quantity,
            "Price": price,
            **({"Currency": pricing_currency} if pricing_currency else {}),
        }
        for quantity, price in _extract_product_page_price_section(lines, "Full Reel")
    )

    visible_details = MouserPackagingDetails(
        packaging_mode=packaging_mode or ("Full Reel" if full_reel_quantity else None),
        packaging_source="product_page",
        minimum_order_quantity=minimum_order_quantity,
        order_multiple=order_multiple,
        standard_pack_quantity=full_reel_quantity,
        full_reel_quantity=full_reel_quantity,
        full_reel_price_breaks=full_reel_price_breaks,
    )
    if embedded_details is None:
        return visible_details
    return _merge_packaging_details(embedded_details, visible_details)


def _extract_product_page_price_section(
    lines: list[str],
    heading_prefix: str,
) -> list[tuple[str, str]]:
    """Extract quantity/unit-price pairs from one Mouser pricing subsection."""
    heading_prefix = heading_prefix.lower()
    for heading, rows in _extract_product_page_price_sections(lines).items():
        if heading.lower().startswith(heading_prefix):
            return rows
    return []
