"""Mouser API client, multi-pass lookup pipeline, and pricing workflow.

This module owns the Mouser HTTP client, the multi-pass search pipeline, and
the end-to-end pricing workflow that turns aggregated BOM lines into priced
parts.  The resolver flow is layered:

1. deterministic Mouser searches using exact and prefix-based passes
2. ambiguity handling through saved resolutions, optional AI reranking, and
   optional interactive human choice
3. price-break selection and enrichment of the final :class:`PricedPart`

Candidate scoring and qualification rules live in :mod:`mouser_scoring`.
Packaging extraction from Mouser product pages lives in :mod:`mouser_packaging`.
"""

import logging
import os
import re
import sys
import time
from dataclasses import dataclass, replace
from typing import Any, Callable

import httpx

from config import (
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

from console import console, Rule, Table, Text

from mouser_scoring import (  # noqa: E402 — re-exported for backward compatibility
    MANUFACTURER_ALIASES,
    QUALIFIER_RULES,
    STRIP_SUFFIXES,
    MouserPart,
    ScoredCandidate,
    collapse_packaging_variants,
    detect_input_qualifiers,
    has_real_mouser_part_number,
    is_non_component,
    is_orderable_candidate,
    is_packaging_variant,
    load_manufacturer_aliases,
    manufacturers_match,
    parse_price,
    requires_manual_review,
    score_candidate,
    strip_qualifiers,
)
from mouser_packaging import (  # noqa: E402 — re-exported for backward compatibility
    MouserPackagingDetails,
    _VisibleTextParser,
    _candidate_product_detail_url,
    _deserialize_manufacturer_packaging_details,
    _deserialize_mouser_packaging_details,
    _extract_product_page_price_sections,
    _manufacturer_details_are_sufficient,
    _merge_packaging_details,
    _mouser_packaging_details_from_manufacturer_details,
    _packaging_details_from_candidate,
    _packaging_details_from_product_page_html,
    _search_details_are_sufficient,
    _serialize_manufacturer_packaging_details,
    _serialize_mouser_packaging_details,
    _should_fetch_product_page_packaging,
)

log = logging.getLogger(__name__)


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
                    "Mouser API key not set. Use --mouser-api-key or set MOUSER_API_KEYS "
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
        self.paced_network_requests = 0
        self.product_page_requests = 0
        self.manufacturer_page_requests = 0

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
                self.paced_network_requests += 1
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
                    cached, page_details = self._cached_product_page_details(product_url)
                    if not cached:
                        page_details = None
                        try:
                            self.network_requests += 1
                            self.paced_network_requests += 1
                            self.product_page_requests += 1
                            response = self._client.get(product_url, follow_redirects=True)
                            response.raise_for_status()
                            page_details = _packaging_details_from_product_page_html(response.text)
                            self._store_product_page_details(product_url, page_details)
                        except Exception as e:
                            log.debug("Failed to load Mouser product page %s: %s", product_url, e)
                            page_details = None
                            self._store_product_page_details(product_url, None)
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
            cached, manufacturer_details = self._cached_manufacturer_page_details(manufacturer_url)
            if not cached:
                manufacturer_details = None
                try:
                    self.network_requests += 1
                    self.manufacturer_page_requests += 1
                    response = self._client.get(manufacturer_url, follow_redirects=True)
                    response.raise_for_status()
                    manufacturer_details = manufacturer_packaging_details_from_html(
                        str(candidate.get("Manufacturer") or ""),
                        manufacturer_part_number=str(candidate.get("ManufacturerPartNumber") or "") or None,
                        bom_part_number=bom_part_number,
                        html=response.text,
                    )
                    self._store_manufacturer_page_details(
                        manufacturer_url,
                        manufacturer_details,
                    )
                except Exception as e:
                    log.debug("Failed to load manufacturer page %s: %s", manufacturer_url, e)
                    manufacturer_details = None
                    self._store_manufacturer_page_details(manufacturer_url, None)
            self._manufacturer_page_cache[manufacturer_url] = manufacturer_details

        return _merge_packaging_details(
            details,
            _mouser_packaging_details_from_manufacturer_details(
                self._manufacturer_page_cache[manufacturer_url]
            ),
        )

    def _cached_product_page_details(
        self,
        product_url: str,
    ) -> tuple[bool, MouserPackagingDetails | None]:
        """Return cached Mouser product-page packaging details when available."""
        if self._cache is None:
            return False, None
        return _deserialize_mouser_packaging_details(
            self._cache.get_provider_response(
                "mouser_product_page_packaging",
                product_url,
            )
        )

    def _store_product_page_details(
        self,
        product_url: str,
        details: MouserPackagingDetails | None,
    ) -> None:
        """Persist one Mouser product-page packaging-details result."""
        if self._cache is None:
            return
        self._cache.set_provider_response(
            "mouser_product_page_packaging",
            product_url,
            _serialize_mouser_packaging_details(details),
        )

    def _cached_manufacturer_page_details(
        self,
        manufacturer_url: str,
    ) -> tuple[bool, ManufacturerPackagingDetails | None]:
        """Return cached manufacturer-page packaging details when available."""
        if self._cache is None:
            return False, None
        return _deserialize_manufacturer_packaging_details(
            self._cache.get_provider_response(
                "manufacturer_page_packaging",
                manufacturer_url,
            )
        )

    def _store_manufacturer_page_details(
        self,
        manufacturer_url: str,
        details: ManufacturerPackagingDetails | None,
    ) -> None:
        """Persist one manufacturer-page packaging-details result."""
        if self._cache is None:
            return
        self._cache.set_provider_response(
            "manufacturer_page_packaging",
            manufacturer_url,
            _serialize_manufacturer_packaging_details(details),
        )


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

    # Collapse packaging variants (tube/reel/tape differences) so the user
    # only picks between electrically distinct parts.  The pricing pipeline's
    # _auto_select_packaging_variant() picks the cheapest reel size afterward.
    collapsed = collapse_packaging_variants(lookup.candidates, agg.manufacturer)
    if len(collapsed) <= 1:
        return lookup

    total = len(collapsed)
    page = 0

    while True:
        start = page * page_size
        end = min(start + page_size, total)
        console.print()
        console.print(Rule(style="dim"))

        # --- Header ---
        header = Text()
        header.append("Interactive resolver: ", style="heading")
        header.append(agg.part_number, style="part")
        header.append(f" ({agg.manufacturer})")
        console.print(header)
        if agg.description:
            console.print(f"  [dim]{agg.description}[/dim]")
        if agg.package or agg.pins is not None:
            console.print(
                f"  BOM hints: package={agg.package or '—'} pins={agg.pins if agg.pins is not None else '—'}"
            )
        suggested = lookup.part.get("ManufacturerPartNumber") if lookup.part else "—"
        suggested_line = Text("  Suggested: ")
        suggested_line.append(str(suggested), style="ok")
        suggested_line.append(f" [{lookup.method.value}]", style="dim")
        console.print(suggested_line)
        console.print()

        # --- Candidate table ---
        table = Table(border_style="dim", padding=(0, 1))
        table.add_column("#", justify="right", style="dim")
        table.add_column("Manufacturer PN", style="part")
        table.add_column("Package")
        table.add_column("Pins", justify="right")
        table.add_column("Unit", justify="right", style="price")
        table.add_column("Availability")

        for idx in range(start, end):
            candidate = collapsed[idx]
            package_text, pins_text = _candidate_package(candidate, agg.manufacturer)
            unit_text, currency = _candidate_unit_price(
                candidate,
                agg.total_quantity,
                client,
                agg.part_number,
            )
            availability = str(candidate.part.get("Availability") or "—")[:16]
            price_cell = f"{unit_text} {currency}".strip() if unit_text else "—"
            table.add_row(
                str(idx + 1),
                str(candidate.part.get("ManufacturerPartNumber") or "—")[:24],
                package_text[:12],
                pins_text,
                price_cell[:10],
                availability,
            )

        console.print(table)

        if total > page_size:
            console.print(f"\n  Showing candidates {start + 1}-{end} of {total}")
        console.print(
            "  Commands: \\[number] choose, a accept suggested, n/p page, s skip, q quit",
            style="dim",
        )

        try:
            choice = input("  Selection> ").strip().lower()
        except EOFError:
            return lookup
        except KeyboardInterrupt as e:
            raise SystemExit(130) from e

        if not choice:
            continue
        if choice == "a" and lookup.part is not None:
            selected = collapsed[0]
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
                selected = collapsed[index]
            else:
                console.print("  [error]Invalid candidate number.[/error]")
                continue
        else:
            console.print("  [error]Unknown command.[/error]")
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
    resolver_callback: Callable[
        ["AggregatedPart", "LookupResult", Any, "MouserClient"], "LookupResult"
    ] | None = None,
) -> PricedPart:
    """Resolve one aggregated part into a priced distributor record.

    This is the high-level single-part pipeline used by the CLI:

    1. deterministic lookup
    2. saved manual resolution reuse
    3. optional AI reranking
    4. optional interactive user selection (or TUI modal via *resolver_callback*)
    5. package and price-break enrichment

    Parameters
    ----------
    agg:
        The aggregated BOM line to price.
    client:
        Active Mouser API client for searches and product-page fetches.
    interactive:
        Whether to invoke interactive candidate selection when the lookup
        is ambiguous. Ignored when *resolver_callback* is provided.
    resolution_store:
        Persistent store for saved manual resolutions.
    ai_resolver:
        Optional AI reranker applied before interactive prompting.
    resolver_callback:
        When provided, this callable replaces the built-in text-based
        ``_interactive_resolution_prompt``. The TUI uses this to show a
        modal dialog instead. Signature:
        ``(agg, lookup, resolution_store, client) -> LookupResult``.
    """
    priced = PricedPart.from_aggregated(agg)

    try:
        ai_note: str | None = None
        lookup = _saved_resolution_fast_path(agg, client, resolution_store)
        if lookup is None:
            lookup = smart_lookup(agg.part_number, agg.manufacturer, client)
            lookup = _saved_resolution_for(agg, lookup, resolution_store)
        lookup, ai_note = _ai_resolution_for(agg, lookup, ai_resolver)
        if resolver_callback is not None:
            lookup = resolver_callback(agg, lookup, resolution_store, client)
        elif interactive:
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
