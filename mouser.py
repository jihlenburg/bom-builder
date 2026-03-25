"""Mouser integration, search heuristics, and pricing workflow.

This module is the heart of BOM Builder. It owns the distributor-facing lookup
pipeline and the heuristics used to turn messy BOM part numbers into buyable
Mouser orderables. The resolver flow is intentionally layered:

1. deterministic Mouser searches using exact and prefix-based passes
2. candidate scoring using manufacturer, qualifiers, availability, and
   packaging-aware heuristics
3. ambiguity handling through saved resolutions, optional AI reranking, and
   optional interactive human choice
4. price-break selection and enrichment of the final :class:`PricedPart`

The logic is kept in one module on purpose because the scoring, ambiguity, and
pricing decisions are tightly coupled and easier to reason about together.
"""

import logging
import json
import os
import re
import sys
import time
from dataclasses import dataclass, replace
from html import unescape
from html.parser import HTMLParser
from pathlib import Path
from typing import Any
from urllib.parse import urljoin

import httpx
import yaml

from config import (
    DATA_DIR,
    MOUSER_API_URL,
    MOUSER_DEFAULT_MAX_ATTEMPTS,
    MOUSER_DEFAULT_RATE_LIMIT_BACKOFF,
    MOUSER_SEARCH_API_LIMITS,
)
from lookup_cache import LookupCache
from manufacturer_packaging import (
    ManufacturerPackagingDetails,
    is_probably_blocked_page_html,
    manufacturer_packaging_details_from_html,
    manufacturer_page_url,
)
from models import AggregatedPart, DistributorOffer, MatchMethod, PricedPart
from optimizer import (
    FamilyPriceBreak,
    OptimizedPurchasePlan as PurchasePlan,
    PurchaseFamily,
    optimize_purchase_families,
)
from package import extract_package_info
from secret_store import get_secret, get_secret_values

log = logging.getLogger(__name__)

type MouserPart = dict[str, Any]
_PACKAGING_SUFFIX_TOKENS = {
    "R",
    "T",
    "TR",
    "M",
    "RE",
    "TE",
}


@dataclass(frozen=True)
class ScoredCandidate:
    """One Mouser result paired with its computed relevance score.

    The resolver keeps the original raw Mouser part payload intact and stores
    the heuristic score alongside it, allowing later stages to inspect both the
    score and the underlying distributor metadata.
    """

    part: MouserPart
    score: float


@dataclass(frozen=True)
class LookupPass:
    """One explicit search attempt in the multi-pass resolver pipeline.

    Attributes
    ----------
    search_term:
        Part number or normalized base part number sent to Mouser.
    search_option:
        Mouser search mode, typically ``"Exact"`` or ``"BeginsWith"``.
    method:
        Match classification associated with this pass.
    """

    search_term: str
    search_option: str
    method: MatchMethod


@dataclass(frozen=True)
class LookupResult:
    """Best result currently known from the lookup pipeline.

    This object captures both the chosen candidate and the uncertainty state
    around that choice, which allows later stages to distinguish between
    confident deterministic matches, review-required fuzzy matches, and
    externally resolved matches coming from saved/AI/interactive flows.
    """

    part: MouserPart | None
    method: MatchMethod
    candidate_count: int = 0
    review_required: bool = False
    candidates: tuple[ScoredCandidate, ...] = ()
    resolution_source: str | None = None


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


def _normalize_manufacturer_name(name: str) -> str:
    """Normalize manufacturer names for alias and substring comparisons."""
    return " ".join(name.lower().strip().split())

# ---------------------------------------------------------------------------
# Manufacturer alias loading
# ---------------------------------------------------------------------------


def load_manufacturer_aliases(yaml_path: Path | None = None) -> dict[str, set[str]]:
    """Load manufacturer aliases from YAML and build a bidirectional lookup map.

    Parameters
    ----------
    yaml_path:
        Optional explicit path to the alias configuration. When omitted, the
        function uses ``manufacturers.yaml`` in the repository data directory.

    Returns
    -------
    dict[str, set[str]]
        Mapping from normalized manufacturer names to the other normalized
        names considered equivalent.
    """
    yaml_path = yaml_path or DATA_DIR / "manufacturers.yaml"
    if not yaml_path.exists():
        log.warning("Manufacturer aliases file not found: %s", yaml_path)
        return {}

    try:
        with yaml_path.open(encoding="utf-8") as f:
            raw = yaml.safe_load(f)
    except yaml.YAMLError as e:
        log.warning("Failed to parse %s: %s", yaml_path, e)
        return {}

    if not isinstance(raw, dict):
        log.warning("Manufacturer aliases in %s must be a mapping", yaml_path)
        return {}

    aliases: dict[str, set[str]] = {}
    for canonical, alias_list in raw.items():
        if not isinstance(canonical, str):
            continue

        related_names = {_normalize_manufacturer_name(canonical)}
        if isinstance(alias_list, (list, tuple, set)):
            related_names.update(
                _normalize_manufacturer_name(str(alias))
                for alias in alias_list
                if str(alias).strip()
            )
        elif isinstance(alias_list, str) and alias_list.strip():
            related_names.add(_normalize_manufacturer_name(alias_list))

        related_names.discard("")
        for name in related_names:
            aliases.setdefault(name, set()).update(related_names - {name})

    return aliases


# Module-level alias table (loaded once at import)
MANUFACTURER_ALIASES = load_manufacturer_aliases()

# ---------------------------------------------------------------------------
# Qualifier / suffix rules
# ---------------------------------------------------------------------------

STRIP_SUFFIXES = [
    r"[-/]NOPB$",
    r"-Q1$",
    r"-EP$",
    r"-ND$",
    r"#PBF$",
    r"-TR$",
]

QUALIFIER_RULES = {
    "automotive": {
        "input_pattern": re.compile(r"-Q1$|[-_]Q1\b", re.IGNORECASE),
        "candidate_pattern": re.compile(
            r"Q1$|Q1\b|AEC[-\s]?Q\d{3}|automotive", re.IGNORECASE
        ),
        "weight": 40,
    },
    "lead_free": {
        "input_pattern": re.compile(r"[-/]NOPB$|#PBF$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"NOPB|PBF|lead.?free|RoHS", re.IGNORECASE),
        "weight": 10,
    },
    "exposed_pad": {
        "input_pattern": re.compile(r"-EP$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"-EP\b|exposed.?pad", re.IGNORECASE),
        "weight": 20,
    },
    "tape_reel": {
        "input_pattern": re.compile(r"-TR$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"TR$|tape.?reel", re.IGNORECASE),
        "weight": 5,
    },
}

# ---------------------------------------------------------------------------
# Non-component filter (EVMs, dev kits, etc.)
# ---------------------------------------------------------------------------

_NON_COMPONENT_MPN = re.compile(
    r"EVM\b|EVAL\b|DEMO\b|DEV\b|-EK\b|-DK\b|-KIT\b|BOOST-",
    re.IGNORECASE,
)
_NON_COMPONENT_DESC = re.compile(
    r"evaluation\s+module|evaluation\s+board|development\s+tool|"
    r"development\s+kit|demo\s+board|starter\s+kit|reference\s+design",
    re.IGNORECASE,
)
_NON_COMPONENT_CAT = re.compile(
    r"development\s+tool|evaluation|demo\s+board|starter\s+kit",
    re.IGNORECASE,
)

# ---------------------------------------------------------------------------
# Price parsing
# ---------------------------------------------------------------------------

# Matches price strings like "1.234,56", "0,045", "1,234.56", "0.045"
_PRICE_RE = re.compile(r"[\d.,]+")


def parse_price(price_str: str) -> float | None:
    """Parse a Mouser price string, handling both EU and US locale formats.

    Parameters
    ----------
    price_str:
        Raw price string returned by Mouser.

    Returns
    -------
    float | None
        Parsed numeric price, or ``None`` when no valid numeric representation
        can be extracted.

    Examples
    --------
    ``"0,045 €"`` -> ``0.045``
    ``"1.234,56 €"`` -> ``1234.56``
    ``"$1,234.56"`` -> ``1234.56``
    """
    m = _PRICE_RE.search(price_str)
    if not m:
        return None

    num = m.group()

    # Determine format by looking at the last separator
    last_comma = num.rfind(",")
    last_dot = num.rfind(".")

    if last_comma > last_dot:
        # EU format: 1.234,56 → comma is decimal separator
        num = num.replace(".", "").replace(",", ".")
    elif last_dot > last_comma:
        # US format: 1,234.56 → dot is decimal separator
        num = num.replace(",", "")
    else:
        # Only one type or none — replace comma with dot as fallback
        num = num.replace(",", ".")

    try:
        return float(num)
    except ValueError:
        log.warning("Failed to parse price: %r", price_str)
        return None


# ---------------------------------------------------------------------------
# Mouser API client
# ---------------------------------------------------------------------------


class MouserClient:
    """HTTP client for the Mouser part-number search API.

    The client wraps HTTP transport concerns such as retries, backoff, and
    caching. Resolver policy is intentionally left to the surrounding helper
    functions so tests can stub either the full client or just the search
    method as needed.
    """

    def __init__(
        self,
        api_key: str = "",
        rate_limit_backoff: float = MOUSER_DEFAULT_RATE_LIMIT_BACKOFF,
        max_attempts: int = MOUSER_DEFAULT_MAX_ATTEMPTS,
        cache_enabled: bool = True,
        cache_ttl_seconds: int = 24 * 60 * 60,
        allow_product_page_fallback: bool | None = None,
        allow_manufacturer_page_fallback: bool | None = None,
    ):
        """Initialize the Mouser API client.

        Parameters
        ----------
        api_key:
            Explicit Mouser API key override. When omitted, the client first
            reads ``MOUSER_API_KEYS`` as a priority-ordered fallback list and
            then falls back to ``MOUSER_API_KEY``.
        rate_limit_backoff:
            Base backoff in seconds used for throttling and transient errors.
        max_attempts:
            Maximum number of HTTP attempts for one search request.
        cache_enabled:
            Whether the persistent lookup cache should be used.
        cache_ttl_seconds:
            Freshness window for cached search results.
        allow_product_page_fallback:
            Whether Mouser product pages may be fetched as an explicit
            non-API fallback for packaging/reel metadata. Defaults to disabled
            unless ``BOM_BUILDER_ENABLE_MOUSER_PAGE_FALLBACK=1`` is set.
        allow_manufacturer_page_fallback:
            Whether known manufacturer pages may be fetched as a second-order
            fallback for packaging/reel metadata. Defaults to the same opt-in
            state as product-page fallback unless explicitly overridden or
            ``BOM_BUILDER_ENABLE_MANUFACTURER_PAGE_FALLBACK=1`` is set.

        Raises
        ------
        ValueError
            If no Mouser API key can be resolved.
        """
        self.api_keys = _resolve_mouser_api_keys(api_key)
        if not self.api_keys:
            raise ValueError(
                (
                    "Mouser API key not set. Use --api-key or set MOUSER_API_KEYS "
                    "or MOUSER_API_KEY in the environment or .env."
                )
            )
        self._current_api_key_index = 0
        self.api_key = self.api_keys[self._current_api_key_index]
        self.backoff = rate_limit_backoff
        self.max_attempts = max_attempts
        self._client = httpx.Client(timeout=30.0)
        self._cache = LookupCache(ttl_seconds=cache_ttl_seconds) if cache_enabled else None
        self._product_page_cache: dict[str, MouserPackagingDetails | None] = {}
        self._manufacturer_page_cache: dict[str, ManufacturerPackagingDetails | None] = {}
        product_page_fallback = (
            allow_product_page_fallback
            if allow_product_page_fallback is not None
            else os.getenv("BOM_BUILDER_ENABLE_MOUSER_PAGE_FALLBACK", "").strip() == "1"
        )
        self.allow_product_page_fallback = product_page_fallback
        self.allow_manufacturer_page_fallback = (
            allow_manufacturer_page_fallback
            if allow_manufacturer_page_fallback is not None
            else (
                os.getenv("BOM_BUILDER_ENABLE_MANUFACTURER_PAGE_FALLBACK", "").strip()
                == "1"
                or product_page_fallback
            )
        )
        self.network_requests = 0

    def close(self) -> None:
        """Close any open cache/database and HTTP client resources."""
        if self._cache is not None:
            self._cache.close()
        self._client.close()

    def __enter__(self) -> "MouserClient":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Release network and cache resources at the end of a ``with`` block."""
        self.close()

    def has_cached_search(self, part_number: str, search_option: str = "Exact") -> bool:
        """Return whether a fresh cached response exists for this lookup key."""
        return self._cache.has(part_number, search_option) if self._cache is not None else False

    def search(self, part_number: str, search_option: str = "Exact") -> list[dict]:
        """Execute one Mouser part-number search with retries and caching.

        Parameters
        ----------
        part_number:
            Search term sent to Mouser.
        search_option:
            Mouser part-number search mode.

        Returns
        -------
        list[dict]
            Raw Mouser ``Parts`` list from the search response.
        """
        if self._cache is not None:
            cached = self._cache.get(part_number, search_option)
            if cached is not None:
                log.debug("  cache hit for %s '%s'", search_option, part_number)
                return cached

        payload = {
            "SearchByPartRequest": {
                "mouserPartNumber": part_number,
                "partSearchOptions": search_option,
            }
        }

        for attempt in range(self.max_attempts):
            try:
                url = f"{MOUSER_API_URL}?apiKey={self.api_key}"
                self.network_requests += 1
                resp = self._client.post(url, json=payload)
                if _is_mouser_daily_limit_error(resp):
                    if self._switch_to_next_api_key("daily quota exhausted"):
                        continue
                    log.warning(
                        (
                            "Mouser daily quota exhausted for %s '%s'. "
                            "All configured keys are exhausted. "
                            "Configured public limit is %d calls/day (%s)."
                        ),
                        search_option,
                        part_number,
                        MOUSER_SEARCH_API_LIMITS.calls_per_day,
                        MOUSER_SEARCH_API_LIMITS.source_url,
                    )
                    resp.raise_for_status()

                if _is_retryable_rate_limit(resp):
                    if self._switch_to_next_api_key("rate limit hit"):
                        continue
                    if attempt >= self.max_attempts - 1:
                        resp.raise_for_status()
                    backoff = self.backoff * (2 ** attempt)
                    log.debug(
                        "Mouser throttled %s/%s '%s' with HTTP %s, backing off %.1fs",
                        search_option,
                        part_number,
                        part_number,
                        resp.status_code,
                        backoff,
                    )
                    time.sleep(backoff)
                    continue

                resp.raise_for_status()
                parts = resp.json().get("SearchResults", {}).get("Parts", [])
                if self._cache is not None:
                    self._cache.set(part_number, search_option, parts)
                return parts
            except (httpx.ConnectError, httpx.ReadTimeout, httpx.RemoteProtocolError) as e:
                if attempt >= self.max_attempts - 1:
                    raise
                backoff = self.backoff * (2 ** attempt)
                log.debug(
                    "Transient Mouser error for %s/%s '%s': %s; retrying in %.1fs",
                    search_option,
                    part_number,
                    part_number,
                    e,
                    backoff,
                )
                time.sleep(backoff)

        return []

    def _switch_to_next_api_key(self, reason: str) -> bool:
        """Advance to the next configured Mouser API key when available."""
        if self._current_api_key_index >= len(self.api_keys) - 1:
            return False
        self._current_api_key_index += 1
        self.api_key = self.api_keys[self._current_api_key_index]
        log.warning(
            "Switching to backup Mouser API key %d of %d after %s",
            self._current_api_key_index + 1,
            len(self.api_keys),
            reason,
        )
        return True

    def packaging_details(
        self,
        candidate: dict[str, Any],
        *,
        bom_part_number: str | None = None,
    ) -> MouserPackagingDetails:
        """Return packaging constraints from search data plus page fallback."""
        search_details = _packaging_details_from_candidate(candidate)
        details = search_details
        if self.allow_product_page_fallback and _should_fetch_product_page_packaging(candidate, search_details):
            product_url = _candidate_product_detail_url(candidate)
            if product_url:
                if product_url not in self._product_page_cache:
                    page_details: MouserPackagingDetails | None = None
                    try:
                        self.network_requests += 1
                        response = self._client.get(product_url, follow_redirects=True)
                        response.raise_for_status()
                        page_details = _packaging_details_from_product_page_html(response.text)
                    except Exception as e:
                        log.debug("Failed to load Mouser product page %s: %s", product_url, e)
                        page_details = None
                    self._product_page_cache[product_url] = page_details
                details = _merge_packaging_details(
                    details,
                    self._product_page_cache[product_url],
                )

        if not self.allow_manufacturer_page_fallback:
            return details
        if _manufacturer_details_are_sufficient(details):
            return details

        manufacturer_url = manufacturer_page_url(
            str(candidate.get("Manufacturer") or ""),
            manufacturer_part_number=str(candidate.get("ManufacturerPartNumber") or "") or None,
            bom_part_number=bom_part_number,
        )
        if not manufacturer_url:
            return details

        if manufacturer_url not in self._manufacturer_page_cache:
            manufacturer_details: ManufacturerPackagingDetails | None = None
            try:
                self.network_requests += 1
                response = self._client.get(manufacturer_url, follow_redirects=True)
                response.raise_for_status()
                manufacturer_details = manufacturer_packaging_details_from_html(
                    str(candidate.get("Manufacturer") or ""),
                    manufacturer_part_number=str(candidate.get("ManufacturerPartNumber") or "") or None,
                    bom_part_number=bom_part_number,
                    html=response.text,
                )
            except Exception as e:
                log.debug("Failed to load manufacturer page %s: %s", manufacturer_url, e)
                manufacturer_details = None
            self._manufacturer_page_cache[manufacturer_url] = manufacturer_details

        return _merge_packaging_details(
            details,
            _mouser_packaging_details_from_manufacturer_details(
                self._manufacturer_page_cache[manufacturer_url]
            ),
        )


# ---------------------------------------------------------------------------
# Packaging helpers
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


def _extract_optional_int(value: Any) -> int | None:
    """Extract the first positive integer visible inside a loose payload value."""
    if value in (None, "", False):
        return None
    if isinstance(value, int):
        return value if value > 0 else None
    match = re.search(r"\d[\d,\.]*", str(value))
    if not match:
        return None
    digits = re.sub(r"[^\d]", "", match.group())
    if not digits:
        return None
    return int(digits)


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


# ---------------------------------------------------------------------------
# Matching helpers
# ---------------------------------------------------------------------------


def _is_word_boundary_match(needle: str, haystack: str) -> bool:
    """Check if needle appears in haystack at a word boundary.

    For short strings (fewer than four characters), the function requires a
    whole-word match to avoid false positives such as ``"ti"`` matching
    ``"Quantic"``. Longer strings can safely use plain substring matching.
    """
    if len(needle) < 4:
        # Require whole-word match for short strings
        return bool(re.search(r"\b" + re.escape(needle) + r"\b", haystack))
    return needle in haystack


def manufacturers_match(
    input_mfr: str,
    candidate_mfr: str,
    aliases: dict[str, set[str]] | None = None,
) -> bool:
    """Return whether two manufacturer names likely refer to the same company.

    Matching uses normalization, short-name boundary checks, and the alias
    table loaded from ``manufacturers.yaml``.
    """
    a = _normalize_manufacturer_name(input_mfr)
    b = _normalize_manufacturer_name(candidate_mfr)

    if a == b:
        return True

    # Substring/word-boundary match between the two names
    if _is_word_boundary_match(a, b) or _is_word_boundary_match(b, a):
        return True

    alias_table = aliases if aliases is not None else MANUFACTURER_ALIASES

    for src, tgt in [(a, b), (b, a)]:
        src_aliases = alias_table.get(src, set())
        if tgt in src_aliases:
            return True
        if any(
            _is_word_boundary_match(alias, tgt)
            or _is_word_boundary_match(tgt, alias)
            for alias in src_aliases
        ):
            return True

    return False


def is_non_component(mpn: str, description: str, category: str) -> bool:
    """Check if a Mouser result is an EVM, dev kit, or other non-component.

    Any of the three fields may be ``None`` from the Mouser API, so the helper
    first guards against missing values before applying regex searches.
    """
    return bool(
        (mpn and _NON_COMPONENT_MPN.search(mpn))
        or (description and _NON_COMPONENT_DESC.search(description))
        or (category and _NON_COMPONENT_CAT.search(category))
    )


def strip_qualifiers(part_number: str) -> str:
    """Strip known marketing or ordering qualifiers to derive a base part number.

    This is used for the fuzzy fallback pass where the exact orderable suffix
    is not expected to be present in the input BOM.
    """
    result = part_number
    for pattern in STRIP_SUFFIXES:
        result = re.sub(pattern, "", result)
    return result.rstrip("-")


def detect_input_qualifiers(part_number: str) -> dict[str, int]:
    """Detect weighted qualifier hints embedded in the input part number."""
    return {
        name: rule["weight"]
        for name, rule in QUALIFIER_RULES.items()
        if rule["input_pattern"].search(part_number)
    }


def score_candidate(
    candidate: dict[str, Any],
    original_pn: str,
    manufacturer: str,
) -> float:
    """Score a Mouser result for relevance to the original BOM line.

    Parameters
    ----------
    candidate:
        Raw Mouser part dictionary.
    original_pn:
        Original BOM part number before normalization.
    manufacturer:
        BOM manufacturer hint used for candidate filtering.

    Returns
    -------
    float
        Relevance score, or ``-1`` when the candidate should be discarded
        outright.

    Notes
    -----
    The scoring policy prefers manufacturer agreement, explicit part-number
    containment, real orderable Mouser part numbers, price/availability
    presence, and qualifier compatibility. It penalizes spurious automotive
    matches when the BOM did not request an automotive variant.
    """
    score = 0.0

    cand_mfr = candidate.get("Manufacturer", "")
    if manufacturers_match(manufacturer, cand_mfr):
        score += 100
    else:
        return -1

    cand_pn = candidate.get("ManufacturerPartNumber") or ""
    cand_desc = candidate.get("Description") or ""
    cand_cat = candidate.get("Category") or ""
    cand_text = f"{cand_pn} {cand_desc}"

    if is_non_component(cand_pn, cand_desc, cand_cat):
        log.debug("  Filtered non-component: %s (cat=%s)", cand_pn, cand_cat)
        return -1

    if original_pn.upper() in cand_pn.upper():
        score += 50

    if has_real_mouser_part_number(candidate):
        score += 15
    else:
        score -= 20

    if candidate.get("PriceBreaks"):
        score += 10

    input_quals = detect_input_qualifiers(original_pn)
    for qual_name, weight in input_quals.items():
        pattern = QUALIFIER_RULES[qual_name]["candidate_pattern"]
        if pattern.search(cand_text):
            score += weight
        else:
            score -= weight * 1.5

    if "automotive" not in input_quals:
        auto_pat = QUALIFIER_RULES["automotive"]["candidate_pattern"]
        if auto_pat.search(cand_text):
            score -= 15

    score -= len(cand_pn) * 0.1

    avail = candidate.get("Availability", "")
    if avail and "In Stock" in avail:
        score += 10

    return score


def has_real_mouser_part_number(candidate: MouserPart) -> bool:
    """Return whether Mouser exposes a buyable part number for a candidate."""
    mouser_pn = str(candidate.get("MouserPartNumber") or "").strip()
    return bool(mouser_pn and mouser_pn.upper() != "N/A")


def is_orderable_candidate(candidate: MouserPart) -> bool:
    """Return whether a candidate appears to be an orderable purchasable part."""
    return bool(
        has_real_mouser_part_number(candidate)
        or candidate.get("PriceBreaks")
        or candidate.get("Availability")
    )


def _normalized_mpn(part_number: str) -> str:
    """Normalize part numbers for structure-aware suffix comparisons."""
    return re.sub(r"[^A-Z0-9]", "", part_number.upper())


def _shared_prefix_length(a: str, b: str) -> int:
    """Return the length of the common prefix shared by two normalized strings."""
    i = 0
    for left, right in zip(a, b):
        if left != right:
            break
        i += 1
    return i


def is_packaging_variant(
    left: MouserPart,
    right: MouserPart,
    manufacturer: str,
) -> bool:
    """Return whether two candidates differ only by packaging suffixes.

    This prevents unnecessary manual-review prompts for common tube-vs-reel or
    other packaging-only variants where the electrical part is effectively the
    same.
    """
    left_mpn = _normalized_mpn(left.get("ManufacturerPartNumber") or "")
    right_mpn = _normalized_mpn(right.get("ManufacturerPartNumber") or "")
    if not left_mpn or not right_mpn:
        return False

    if left_mpn.endswith("Q1") and right_mpn.endswith("Q1"):
        left_mpn = left_mpn[:-2]
        right_mpn = right_mpn[:-2]

    shared = _shared_prefix_length(left_mpn, right_mpn)
    left_suffix = left_mpn[shared:]
    right_suffix = right_mpn[shared:]
    if not left_suffix or not right_suffix:
        return False

    if len(left_suffix) > 2 or len(right_suffix) > 2:
        return False

    if (
        left_suffix not in _PACKAGING_SUFFIX_TOKENS
        or right_suffix not in _PACKAGING_SUFFIX_TOKENS
    ):
        return False

    left_package, _ = extract_package_info(left, manufacturer)
    right_package, _ = extract_package_info(right, manufacturer)
    if left_package and right_package and left_package != right_package:
        return False

    return True


def requires_manual_review(
    scored: list[ScoredCandidate],
    method: MatchMethod,
    manufacturer: str,
) -> bool:
    """Return whether the top fuzzy match is still materially ambiguous.

    Ambiguity is currently defined as a fuzzy lookup where the score gap to the
    runner-up is small and the runner-up is not merely a packaging-only
    variant.
    """
    if method != MatchMethod.FUZZY or not scored:
        return False
    if len(scored) == 1:
        return False

    top = scored[0]
    runner_up = scored[1]
    if is_packaging_variant(top.part, runner_up.part, manufacturer):
        return False

    score_gap = top.score - runner_up.score
    return score_gap < 10.0


# ---------------------------------------------------------------------------
# Multi-pass lookup
# ---------------------------------------------------------------------------

def _build_lookup_passes(part_number: str, base_pn: str) -> list[LookupPass]:
    """Build the ordered lookup passes used for one part-number search.

    Qualifier-style BOM part numbers such as ``-Q1`` or ``/NOPB`` skip the
    initial exact pass. A full-string ``BeginsWith`` lookup is typically
    enough to catch the same orderables while avoiding one redundant network
    call on cold runs.
    """
    if base_pn != part_number:
        return [
            LookupPass(part_number, "BeginsWith", MatchMethod.BEGINS_WITH),
            LookupPass(base_pn, "BeginsWith", MatchMethod.FUZZY),
        ]

    return [
        LookupPass(part_number, "Exact", MatchMethod.EXACT),
        LookupPass(part_number, "BeginsWith", MatchMethod.BEGINS_WITH),
    ]


def _resolve_mouser_api_keys(api_key: str = "") -> tuple[str, ...]:
    """Return the configured Mouser API keys in priority order."""
    explicit = api_key.strip()
    if explicit:
        return (explicit,)

    configured = get_secret_values("mouser_api_keys")
    if configured:
        return tuple(dict.fromkeys(configured))

    single = get_secret("mouser_api_key")
    return (single,) if single else ()


def _run_pass(
    client: MouserClient,
    lookup_pass: LookupPass,
    original_pn: str,
    manufacturer: str,
) -> list[ScoredCandidate]:
    """Run one lookup pass and return candidates sorted by descending score."""
    log.debug("  %s '%s'", lookup_pass.search_option, lookup_pass.search_term)
    parts = client.search(lookup_pass.search_term, lookup_pass.search_option)
    log.debug("  → %d raw results", len(parts))

    if not parts:
        return []

    scored = [
        ScoredCandidate(part, score)
        for part in parts
        for score in [score_candidate(part, original_pn, manufacturer)]
        if score >= 0
    ]
    scored.sort(key=lambda item: item.score, reverse=True)

    log.debug("  → %d after filter", len(scored))
    if scored:
        log.debug(
            "  → Winner: %s (score %.1f)",
            scored[0].part.get("ManufacturerPartNumber"),
            scored[0].score,
        )
    return scored


def smart_lookup(
    part_number: str, manufacturer: str, client: MouserClient
) -> LookupResult:
    """Run the multi-pass lookup pipeline for one BOM part number.

    Parameters
    ----------
    part_number:
        Original BOM part number.
    manufacturer:
        BOM manufacturer hint.
    client:
        Active Mouser client used to execute the searches.

    Returns
    -------
    LookupResult
        Best available result from exact, begins-with, and fuzzy fallback
        passes, including ambiguity metadata and the ranked candidate shortlist.
    """
    base_pn = strip_qualifiers(part_number)
    lookup_passes = _build_lookup_passes(part_number, base_pn)
    fallback: LookupResult | None = None

    if base_pn == part_number:
        log.debug("  Fuzzy pass skipped (no qualifiers to strip)")

    for i, lookup_pass in enumerate(lookup_passes):
        if i > 0 and not _lookup_is_cached(client, lookup_pass):
            time.sleep(0.3)

        log.debug("Pass %d: %s", i + 1, lookup_pass.method.value)
        scored = _run_pass(client, lookup_pass, part_number, manufacturer)

        if not scored:
            continue

        result = LookupResult(
            part=scored[0].part,
            method=lookup_pass.method,
            candidate_count=len(scored),
            review_required=requires_manual_review(
                scored, lookup_pass.method, manufacturer
            ),
            candidates=tuple(scored),
        )

        if is_orderable_candidate(scored[0].part):
            return result

        if fallback is None:
            fallback = result
        log.debug(
            "  Best %s result is not orderable yet (%s), continuing search",
            lookup_pass.method.value,
            scored[0].part.get("ManufacturerPartNumber"),
        )

    if fallback is not None:
        return fallback
    log.debug("All passes exhausted — no match")
    return LookupResult(None, MatchMethod.NOT_FOUND, 0)


# ---------------------------------------------------------------------------
# Price break selection
# ---------------------------------------------------------------------------


def best_price_break(price_breaks: list[dict], quantity: int) -> dict | None:
    """Select the best price break for the requested quantity.

    The function chooses the highest break not exceeding the requested
    quantity. If every break exceeds the requested quantity, it falls back to
    the smallest break rather than returning no price at all.
    """
    applicable = [
        pb for pb in price_breaks if int(pb.get("Quantity", 0)) <= quantity
    ]

    if not applicable:
        if price_breaks:
            return min(price_breaks, key=lambda pb: int(pb.get("Quantity", 0)))
        return None

    return max(applicable, key=lambda pb: int(pb.get("Quantity", 0)))


def _preferred_remainder_packaging_mode(details: MouserPackagingDetails) -> str | None:
    """Return the most useful non-reel packaging label for mixed plans."""
    packaging_mode = details.packaging_mode or ""
    packaging_text = packaging_mode.lower()
    if "cut tape" in packaging_text:
        return "Cut Tape"
    if "mousereel" in packaging_text:
        return "MouseReel"
    return details.packaging_mode


def _family_price_breaks(
    price_breaks: list[dict[str, Any]] | tuple[dict[str, Any], ...],
) -> tuple[FamilyPriceBreak, ...]:
    """Normalize Mouser price breaks into distributor-agnostic optimizer input."""
    normalized: list[FamilyPriceBreak] = []
    for price_break in price_breaks:
        quantity = int(price_break.get("Quantity", 0) or 0)
        if quantity <= 0:
            continue
        unit_price = parse_price(str(price_break.get("Price", "")))
        if unit_price is None:
            continue
        normalized.append(
            FamilyPriceBreak(
                quantity=quantity,
                unit_price=unit_price,
                currency=str(price_break.get("Currency", "EUR") or "EUR"),
            )
        )
    return tuple(normalized)


def _mouser_purchase_families(
    price_breaks: list[dict],
    details: MouserPackagingDetails,
) -> tuple[PurchaseFamily, ...]:
    """Return optimizer purchase families derived from Mouser packaging data."""
    families: list[PurchaseFamily] = []
    standard_packaging_mode = _preferred_remainder_packaging_mode(details)
    reel_only_search_pricing = bool(
        details.packaging_mode
        and "reel" in details.packaging_mode.lower()
        and "cut tape" not in details.packaging_mode.lower()
        and "mousereel" not in details.packaging_mode.lower()
    )

    standard_breaks = _family_price_breaks(price_breaks)
    if standard_breaks:
        families.append(
            PurchaseFamily(
                family_id="mouser_standard",
                packaging_mode="Full Reel" if reel_only_search_pricing else standard_packaging_mode,
                minimum_order_quantity=details.minimum_order_quantity,
                order_multiple=details.full_reel_quantity if reel_only_search_pricing else details.order_multiple,
                full_reel_quantity=details.full_reel_quantity if reel_only_search_pricing else None,
                base_pricing_strategy="full reel" if reel_only_search_pricing else "requested quantity",
                strategy_mode="full_reel" if reel_only_search_pricing else "price_break",
                allow_mixing_as_bulk=False,
                allow_mixing_as_remainder=not reel_only_search_pricing,
                price_breaks=standard_breaks,
            )
        )

    full_reel_breaks = _family_price_breaks(details.full_reel_price_breaks)
    if full_reel_breaks:
        families.append(
            PurchaseFamily(
                family_id="mouser_full_reel",
                packaging_mode="Full Reel",
                minimum_order_quantity=details.full_reel_quantity or details.minimum_order_quantity,
                order_multiple=details.full_reel_quantity or details.order_multiple,
                full_reel_quantity=details.full_reel_quantity,
                base_pricing_strategy="full reel",
                strategy_mode="full_reel",
                allow_mixing_as_bulk=bool(details.full_reel_quantity),
                allow_mixing_as_remainder=False,
                mix_quantity=details.full_reel_quantity,
                price_breaks=full_reel_breaks,
            )
        )

    return tuple(families)


def best_purchase_plan(
    price_breaks: list[dict],
    quantity: int,
    *,
    packaging_details: MouserPackagingDetails | None = None,
) -> PurchasePlan | None:
    """Return the cheapest buy plan across Mouser cut-tape and reel options."""
    details = packaging_details or MouserPackagingDetails()
    families = _mouser_purchase_families(price_breaks, details)
    if not families:
        return None
    return optimize_purchase_families(quantity, families)


def _append_lookup_error(priced: PricedPart, message: str) -> None:
    """Append a lookup or pricing note without discarding earlier context."""
    if priced.lookup_error:
        priced.lookup_error = f"{priced.lookup_error}; {message}"
    else:
        priced.lookup_error = message


def _apply_package_info(
    priced: PricedPart, mouser_part: MouserPart, manufacturer: str
) -> None:
    """Populate inferred package metadata when the BOM omitted it."""
    if priced.package and priced.pins is not None:
        return

    package, pins = extract_package_info(mouser_part, manufacturer)
    if package and not priced.package:
        priced.package = package
    if pins is not None and priced.pins is None:
        priced.pins = pins


def _apply_price_break(
    priced: PricedPart,
    price_breaks: list[dict],
    quantity: int,
    *,
    packaging_details: MouserPackagingDetails | None = None,
) -> None:
    """Apply the best matching price break to a priced part record."""
    details = packaging_details or MouserPackagingDetails()
    invalid_price = next(
        (
            price_break.get("Price")
            for price_break in price_breaks
            if parse_price(str(price_break.get("Price", ""))) is None
        ),
        None,
    )
    if invalid_price is None and details.full_reel_price_breaks:
        invalid_price = next(
            (
                price_break.get("Price")
                for price_break in details.full_reel_price_breaks
                if parse_price(str(price_break.get("Price", ""))) is None
            ),
            None,
        )
    plan = best_purchase_plan(price_breaks, quantity, packaging_details=details)
    if not plan:
        if invalid_price is not None:
            _append_lookup_error(priced, f"Failed to parse price: {invalid_price}")
        else:
            _append_lookup_error(priced, "No price breaks available")
        return

    priced.unit_price = plan.unit_price
    priced.extended_price = plan.extended_price
    priced.currency = plan.currency
    priced.price_break_quantity = plan.price_break_quantity
    priced.required_quantity = plan.required_quantity
    priced.purchased_quantity = plan.purchased_quantity
    priced.surplus_quantity = plan.surplus_quantity
    selected_packaging_modes = [
        leg.packaging_mode for leg in plan.purchase_legs if leg.packaging_mode
    ]
    if selected_packaging_modes:
        ordered_modes: list[str] = []
        seen_modes: set[str] = set()
        for mode in selected_packaging_modes:
            if mode not in seen_modes:
                ordered_modes.append(mode)
                seen_modes.add(mode)
        priced.packaging_mode = " + ".join(ordered_modes)
    else:
        priced.packaging_mode = details.packaging_mode
    priced.packaging_source = details.packaging_source
    priced.minimum_order_quantity = details.minimum_order_quantity
    priced.order_multiple = details.order_multiple
    priced.full_reel_quantity = details.full_reel_quantity
    priced.pricing_strategy = plan.pricing_strategy
    priced.order_plan = plan.order_plan
    priced.purchase_legs = [leg.model_copy(deep=True) for leg in plan.purchase_legs]


def _mouser_offer_from_priced(priced: PricedPart) -> DistributorOffer:
    """Return a normalized Mouser offer for one priced record."""
    return DistributorOffer(
        distributor="Mouser",
        distributor_part_number=priced.mouser_part_number,
        manufacturer_part_number=priced.manufacturer_part_number,
        unit_price=priced.unit_price,
        extended_price=priced.extended_price,
        currency=priced.currency,
        availability=priced.availability,
        price_break_quantity=priced.price_break_quantity,
        required_quantity=priced.required_quantity,
        purchased_quantity=priced.purchased_quantity,
        surplus_quantity=priced.surplus_quantity,
        package_type=priced.package_type,
        packaging_mode=priced.packaging_mode,
        packaging_source=priced.packaging_source,
        minimum_order_quantity=priced.minimum_order_quantity,
        order_multiple=priced.order_multiple,
        full_reel_quantity=priced.full_reel_quantity,
        pricing_strategy=priced.pricing_strategy,
        order_plan=priced.order_plan,
        match_method=priced.match_method,
        match_candidates=priced.match_candidates,
        resolution_source=priced.resolution_source,
        review_required=priced.review_required,
        lookup_error=priced.lookup_error,
        purchase_legs=[leg.model_copy(deep=True) for leg in priced.purchase_legs],
    )


def _packaging_details_for_candidate(
    client: Any | None,
    candidate: MouserPart,
    *,
    bom_part_number: str | None = None,
) -> MouserPackagingDetails:
    """Return packaging constraints using client enrichment when available."""
    resolver = getattr(client, "packaging_details", None)
    if callable(resolver):
        return resolver(candidate, bom_part_number=bom_part_number)
    return _packaging_details_from_candidate(candidate)


def _can_prompt_interactively() -> bool:
    """Return whether stdin/stdout support an interactive terminal flow."""
    return sys.stdin.isatty() and sys.stdout.isatty()


def _candidate_unit_price(
    candidate: ScoredCandidate,
    quantity: int,
    client: Any | None = None,
    bom_part_number: str | None = None,
) -> tuple[str, str]:
    """Return printable unit-price text and currency for one candidate."""
    plan = best_purchase_plan(
        candidate.part.get("PriceBreaks", []),
        quantity,
        packaging_details=_packaging_details_for_candidate(
            client,
            candidate.part,
            bom_part_number=bom_part_number,
        ),
    )
    if not plan:
        return "—", ""

    return f"{plan.unit_price:.4f}", plan.currency


def _candidate_package(candidate: ScoredCandidate, manufacturer: str) -> tuple[str, str]:
    """Return printable package metadata for a candidate."""
    package, pins = extract_package_info(candidate.part, manufacturer)
    package_text = package or "—"
    pins_text = str(pins) if pins is not None else "—"
    return package_text, pins_text


def _saved_resolution_for(
    agg: AggregatedPart,
    lookup: LookupResult,
    resolution_store: Any | None,
) -> LookupResult:
    """Apply a previously saved manual resolution when it matches a candidate.

    Saved resolutions are considered before AI or interactive review because
    they represent prior human-confirmed decisions.
    """
    if resolution_store is None:
        return lookup

    record = resolution_store.get(agg.manufacturer, agg.part_number)
    if record is None:
        return lookup

    for candidate in lookup.candidates:
        if record.matches(candidate.part):
            log.debug(
                "  Applied saved resolution for %s -> %s",
                agg.part_number,
                record.mouser_part_number,
            )
            return replace(
                lookup,
                part=candidate.part,
                review_required=False,
                resolution_source="saved",
            )

    return lookup


def _same_part_candidate(left: MouserPart, right: MouserPart) -> bool:
    """Return whether two Mouser payloads identify the same concrete orderable."""
    return (
        str(left.get("MouserPartNumber") or "").strip(),
        str(left.get("ManufacturerPartNumber") or "").strip(),
    ) == (
        str(right.get("MouserPartNumber") or "").strip(),
        str(right.get("ManufacturerPartNumber") or "").strip(),
    )


def _auto_select_packaging_variant(
    agg: AggregatedPart,
    lookup: LookupResult,
    client: Any | None = None,
) -> LookupResult:
    """Switch to the cheapest packaging-only Mouser variant when safe.

    The resolver already knows how to treat tube-vs-reel variants as the same
    logical electrical part for ambiguity purposes. This helper extends that
    behavior into pricing by comparing the actual purchase spend across the
    packaging-equivalent candidates and picking the cheapest one unless a human,
    saved, or AI-driven resolution already chose a specific orderable.
    """
    if (
        lookup.part is None
        or lookup.resolution_source is not None
        or not lookup.candidates
    ):
        return lookup

    equivalent_candidates = [
        candidate
        for candidate in lookup.candidates
        if _same_part_candidate(candidate.part, lookup.part)
        or is_packaging_variant(candidate.part, lookup.part, agg.manufacturer)
    ]
    if len(equivalent_candidates) < 2:
        return lookup

    priced_equivalents: list[tuple[ScoredCandidate, PurchasePlan]] = []
    for candidate in equivalent_candidates:
        if not is_orderable_candidate(candidate.part):
            continue
        plan = best_purchase_plan(
            candidate.part.get("PriceBreaks", []),
            agg.total_quantity,
            packaging_details=_packaging_details_for_candidate(
                client,
                candidate.part,
                bom_part_number=agg.part_number,
            ),
        )
        if plan is None:
            continue
        priced_equivalents.append((candidate, plan))

    if not priced_equivalents:
        return lookup

    selected_candidate, _ = min(
        priced_equivalents,
        key=lambda item: (
            item[1].extended_price,
            item[1].surplus_quantity,
            -item[0].score,
            str(item[0].part.get("ManufacturerPartNumber") or ""),
        ),
    )
    if _same_part_candidate(selected_candidate.part, lookup.part):
        return lookup

    log.debug(
        "  Switched packaging variant for %s to %s based on lower total spend",
        agg.part_number,
        selected_candidate.part.get("ManufacturerPartNumber"),
    )
    return replace(lookup, part=selected_candidate.part)


def _saved_resolution_fast_path(
    agg: AggregatedPart,
    client: MouserClient,
    resolution_store: Any | None,
) -> LookupResult | None:
    """Resolve a part directly from a saved mapping before normal lookup.

    The fast path uses the persisted Mouser or manufacturer part number from a
    previous confirmed resolution. This can collapse a repeat lookup from the
    normal multi-pass search down to one exact query, or zero network requests
    when that exact query is already cached.
    """
    if resolution_store is None:
        return None

    record = resolution_store.get(agg.manufacturer, agg.part_number)
    if record is None:
        return None

    search_terms: list[str] = []
    if record.mouser_part_number:
        search_terms.append(record.mouser_part_number)
    if (
        record.manufacturer_part_number
        and record.manufacturer_part_number not in search_terms
    ):
        search_terms.append(record.manufacturer_part_number)

    for search_term in search_terms:
        log.debug("  Saved-resolution fast path Exact '%s'", search_term)
        parts = client.search(search_term, "Exact")
        matching_parts = [
            part
            for part in parts
            if record.matches(part)
            and manufacturers_match(
                agg.manufacturer,
                str(part.get("Manufacturer") or ""),
            )
        ]
        if not matching_parts:
            continue

        selected = next(
            (part for part in matching_parts if is_orderable_candidate(part)),
            matching_parts[0],
        )
        return LookupResult(
            part=selected,
            method=MatchMethod.EXACT,
            candidate_count=1,
            review_required=False,
            candidates=(ScoredCandidate(selected, float("inf")),),
            resolution_source="saved",
        )

    return None


def _ai_resolution_for(
    agg: AggregatedPart,
    lookup: LookupResult,
    ai_resolver: Any | None,
) -> tuple[LookupResult, str | None]:
    """Apply the optional AI reranker before falling back to user review.

    Returns
    -------
    tuple[LookupResult, str | None]
        Potentially updated lookup result plus an optional diagnostic note to
        append if the AI abstained or failed.
    """
    if ai_resolver is None or not lookup.review_required or not lookup.candidates:
        return lookup, None

    try:
        decision = ai_resolver.rerank(agg, lookup)
    except Exception as e:
        log.exception("Unexpected AI resolver error for %s", agg.part_number)
        return (
            lookup,
            "AI resolver unavailable; deterministic fallback enabled for remaining ambiguous parts.",
        )

    if decision is None:
        return lookup, None

    if decision.is_select:
        selected = lookup.candidates[decision.selected_index - 1]
        log.debug(
            "  AI selected %s for %s with confidence %.2f",
            selected.part.get("ManufacturerPartNumber"),
            agg.part_number,
            decision.confidence,
        )
        return (
            replace(
                lookup,
                part=selected.part,
                review_required=False,
                resolution_source="ai",
            ),
            None,
        )

    if getattr(decision, "is_degraded", False):
        if getattr(decision, "technical_details", None):
            log.debug(
                "  AI resolver degraded for %s: %s",
                agg.part_number,
                decision.technical_details,
            )
        return lookup, decision.rationale if getattr(decision, "emit_user_notice", False) else None

    log.debug("  AI abstained for %s: %s", agg.part_number, decision.rationale)
    return lookup, None


def _interactive_resolution_prompt(
    agg: AggregatedPart,
    lookup: LookupResult,
    resolution_store: Any | None,
    page_size: int = 8,
    client: Any | None = None,
) -> LookupResult:
    """Prompt the user to choose a candidate for an ambiguous part.

    The terminal UI is intentionally compact but information-rich: it shows the
    current BOM hints, suggested candidate, paged candidate list, package/pin
    inference, quantity-aware unit price, and availability.
    """
    if lookup.part is not None and not lookup.review_required:
        return lookup
    if not lookup.candidates or not _can_prompt_interactively():
        return lookup

    total = len(lookup.candidates)
    page = 0

    while True:
        start = page * page_size
        end = min(start + page_size, total)
        print()
        print("=" * 78)
        print(f"Interactive resolver: {agg.part_number} ({agg.manufacturer})")
        if agg.description:
            print(f"  {agg.description}")
        if agg.package or agg.pins is not None:
            print(
                f"  BOM hints: package={agg.package or '—'} pins={agg.pins if agg.pins is not None else '—'}"
            )
        suggested = lookup.part.get("ManufacturerPartNumber") if lookup.part else "—"
        print(f"  Suggested: {suggested} [{lookup.method.value}]")
        print()
        print("  #  Manufacturer PN          Package      Pins    Unit      Availability")
        print("  -- ------------------------ ------------ ---- ---------- ----------------")

        for idx in range(start, end):
            candidate = lookup.candidates[idx]
            package_text, pins_text = _candidate_package(candidate, agg.manufacturer)
            unit_text, currency = _candidate_unit_price(
                candidate,
                agg.total_quantity,
                client,
                agg.part_number,
            )
            availability = str(candidate.part.get("Availability") or "—")
            availability = availability[:16]
            print(
                f"  {idx + 1:>2} "
                f"{str(candidate.part.get('ManufacturerPartNumber') or '—')[:24]:24} "
                f"{package_text[:12]:12} "
                f"{pins_text:>4} "
                f"{(unit_text + (' ' + currency if currency else ''))[:10]:10} "
                f"{availability:16}"
            )

        if total > page_size:
            print(f"\n  Showing candidates {start + 1}-{end} of {total}")
        print("  Commands: [number] choose, a accept suggested, n/p page, s skip, q quit")

        try:
            choice = input("  Selection> ").strip().lower()
        except EOFError:
            return lookup
        except KeyboardInterrupt as e:
            raise SystemExit(130) from e

        if not choice:
            continue
        if choice == "a" and lookup.part is not None:
            selected = lookup.candidates[0]
        elif choice == "s":
            return lookup
        elif choice == "n" and end < total:
            page += 1
            continue
        elif choice == "p" and page > 0:
            page -= 1
            continue
        elif choice == "q":
            raise SystemExit(130)
        elif choice.isdigit():
            index = int(choice) - 1
            if 0 <= index < total:
                selected = lookup.candidates[index]
            else:
                print("  Invalid candidate number.")
                continue
        else:
            print("  Unknown command.")
            continue

        if resolution_store is not None:
            resolution_store.set(
                agg.manufacturer,
                agg.part_number,
                str(selected.part.get("MouserPartNumber") or ""),
                str(selected.part.get("ManufacturerPartNumber") or ""),
            )
        return replace(
            lookup,
            part=selected.part,
            review_required=False,
            resolution_source="interactive",
        )


# ---------------------------------------------------------------------------
# High-level pricing
# ---------------------------------------------------------------------------


def price_part(
    agg: AggregatedPart,
    client: MouserClient,
    interactive: bool = False,
    resolution_store: Any | None = None,
    ai_resolver: Any | None = None,
) -> PricedPart:
    """Resolve one aggregated part into a priced distributor record.

    This is the high-level single-part pipeline used by the CLI:

    1. deterministic lookup
    2. saved manual resolution reuse
    3. optional AI reranking
    4. optional interactive user selection
    5. package and price-break enrichment
    """
    priced = PricedPart.from_aggregated(agg)

    try:
        ai_note: str | None = None
        lookup = _saved_resolution_fast_path(agg, client, resolution_store)
        if lookup is None:
            lookup = smart_lookup(agg.part_number, agg.manufacturer, client)
            lookup = _saved_resolution_for(agg, lookup, resolution_store)
        lookup, ai_note = _ai_resolution_for(agg, lookup, ai_resolver)
        if interactive:
            lookup = _interactive_resolution_prompt(
                agg,
                lookup,
                resolution_store,
                client=client,
            )
        lookup = _auto_select_packaging_variant(agg, lookup, client)
        priced.match_method = lookup.method
        priced.match_candidates = lookup.candidate_count
        priced.resolution_source = lookup.resolution_source
        priced.review_required = lookup.review_required

        if lookup.part is None:
            priced.lookup_error = (
                "No results found on Mouser (tried exact, begins_with, fuzzy)"
            )
            return priced

        mouser_part = lookup.part
        priced.mouser_part_number = mouser_part.get("MouserPartNumber")
        priced.manufacturer_part_number = mouser_part.get("ManufacturerPartNumber")
        priced.availability = mouser_part.get("Availability")

        _apply_package_info(priced, mouser_part, agg.manufacturer)
        packaging_details = _packaging_details_for_candidate(
            client,
            mouser_part,
            bom_part_number=agg.part_number,
        )

        if ai_note:
            _append_lookup_error(priced, ai_note)

        if lookup.review_required and lookup.resolution_source is None:
            matched_mpn = mouser_part.get("ManufacturerPartNumber", "")
            _append_lookup_error(
                priced,
                f"Fuzzy match: resolved to {matched_mpn} "
                f"({lookup.candidate_count} candidates) — verify manually",
            )

        _apply_price_break(
            priced,
            mouser_part.get("PriceBreaks", []),
            agg.total_quantity,
            packaging_details=packaging_details,
        )

    except httpx.HTTPStatusError as e:
        priced.lookup_error = f"HTTP {e.response.status_code}: {e.response.text[:200]}"
    except Exception as e:
        log.exception("Unexpected error pricing %s", agg.part_number)
        priced.lookup_error = str(e)

    mouser_offer = _mouser_offer_from_priced(priced)
    priced.offers = [mouser_offer]
    priced.apply_selected_offer(mouser_offer)
    return priced


def price_all_parts(
    parts: list[AggregatedPart],
    client: MouserClient,
    delay: float = 1.0,
    interactive: bool = False,
    resolution_store: Any | None = None,
    ai_resolver: Any | None = None,
) -> list[PricedPart]:
    """Resolve pricing for every aggregated part in the BOM.

    The function is responsible for user-facing progress output and for
    applying the configured per-part delay only when a real network request was
    required for that part.
    """
    results = []
    total = len(parts)

    for i, agg in enumerate(parts, 1):
        print(f"  [{i}/{total}] Looking up {agg.part_number}...")
        before_requests = getattr(client, "network_requests", None)
        priced = price_part(
            agg,
            client,
            interactive=interactive,
            resolution_store=resolution_store,
            ai_resolver=ai_resolver,
        )

        method = priced.match_method
        mpn = priced.mouser_part_number or "—"
        if priced.resolution_source == "saved":
            print(f"           ✓ Saved resolution → {mpn}")
        elif priced.resolution_source == "ai":
            print(f"           ✓ AI-reranked match → {mpn}")
        elif priced.resolution_source == "interactive":
            print(f"           ✓ Interactive selection → {mpn}")
        elif method == MatchMethod.EXACT:
            print(f"           ✓ Exact match → {mpn}")
        elif method == MatchMethod.BEGINS_WITH:
            print(f"           ~ BeginsWith match → {mpn} ({priced.match_candidates} candidates)")
        elif method == MatchMethod.FUZZY:
            if priced.review_required:
                print(
                    f"           ⚠ Fuzzy match → {mpn} "
                    f"({priced.match_candidates} candidates) — review!"
                )
            else:
                print(
                    f"           ~ Fuzzy-resolved match → {mpn} "
                    f"({priced.match_candidates} candidates)"
                )
        elif priced.lookup_error:
            detail = priced.lookup_error.splitlines()[0][:90]
            print(f"           ✗ Lookup failed → {detail}")
        else:
            print(f"           ✗ No match found")

        results.append(priced)

        used_network = before_requests is None or (
            getattr(client, "network_requests", before_requests) > before_requests
        )
        if i < total and delay > 0 and used_network:
            time.sleep(delay)

    return results


def _lookup_is_cached(client: MouserClient, lookup_pass: LookupPass) -> bool:
    """Return whether the upcoming lookup pass can be served from cache."""
    checker = getattr(client, "has_cached_search", None)
    return bool(callable(checker) and checker(lookup_pass.search_term, lookup_pass.search_option))


def _mouser_error_details(response: httpx.Response) -> tuple[str, str]:
    """Return the normalized Mouser error code and message when available."""
    code = ""
    message = ""
    try:
        payload = response.json()
    except ValueError:
        payload = None

    if isinstance(payload, dict):
        errors = payload.get("Errors")
        if isinstance(errors, list) and errors:
            first = errors[0]
            if isinstance(first, dict):
                code = str(first.get("Code") or "").strip()
                message = str(first.get("Message") or "").strip()

    if not message:
        message = response.text.strip()

    return code.lower(), message.lower()


def _is_mouser_daily_limit_error(response: httpx.Response) -> bool:
    """Return whether the response indicates the documented daily quota hit."""
    if response.status_code != 403:
        return False
    code, message = _mouser_error_details(response)
    return code == "toomanyrequests" and "per day" in message


def _is_retryable_rate_limit(response: httpx.Response) -> bool:
    """Return whether the response looks like a transient retryable throttle."""
    if response.status_code == 429:
        return True
    if response.status_code != 403:
        return False
    code, message = _mouser_error_details(response)
    return code == "toomanyrequests" and "per day" not in message
