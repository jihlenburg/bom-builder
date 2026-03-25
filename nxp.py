"""Experimental NXP direct-store integration backed by Playwright.

NXP does not appear to expose a public store pricing API comparable to TI's
inventory-and-pricing endpoint. The public store pages do, however, fetch a
structured search payload in the browser that includes exact orderable part
numbers, availability, packing descriptors, and price breaks. This adapter
uses a real browser session to capture that payload and only falls back to
light page-text parsing for MOQ enrichment on matching family part pages.

The implementation is deliberately conservative:

- query NXP only for BOM lines whose manufacturer is NXP/Freescale
- trust only results that explicitly include ``Buy Direct``
- prefer structured store-search payloads over DOM scraping
- use family part pages only to enrich MOQ data when available
- mark offers as review-required when real price breaks exist but MOQ could not
  be confirmed, so weak data does not silently win final supplier selection
"""

from __future__ import annotations

from dataclasses import asdict, dataclass
from datetime import datetime
import json
import os
from pathlib import Path
import re
import tempfile
from typing import Any
from urllib.parse import quote

from lookup_cache import LookupCache, default_cache_db_path
from models import AggregatedPart, DistributorOffer, MatchMethod
from optimizer import FamilyPriceBreak, PurchaseFamily, optimize_purchase_families

try:
    from playwright.sync_api import Error as PlaywrightError
    from playwright.sync_api import sync_playwright
except Exception:  # pragma: no cover - optional dependency import guard
    PlaywrightError = RuntimeError
    sync_playwright = None

NXP_DISTRIBUTOR_NAME = "NXP"
NXP_STORE_SEARCH_URL = "https://www.nxp.com/store:STORE"
NXP_PART_DETAIL_URL = "https://www.nxp.com/part"
NXP_MANUFACTURERS = {"nxp", "nxp semiconductors", "freescale", "freescale semiconductor"}
NXP_BROWSER_CHANNELS = ("chrome", "msedge")
NXP_DEFAULT_TIMEOUT_MS = 30000
_PART_ID_RE = re.compile(r"part_id::(?:<b>)?([^<|]+)")
_STEP_PRICE_RE = re.compile(r"^\s*(\d+)\s*::.*::\s*([0-9][0-9,]*(?:\.[0-9]+)?)\s*$")
_MOQ_RE = re.compile(r"min\.\s*order quantity:\s*([0-9,]+)", re.IGNORECASE)
_MPQ_RE = re.compile(r"min\.\s*package quantity:\s*([0-9,]+)", re.IGNORECASE)
_SAFE_ARTIFACT_NAME_RE = re.compile(r"[^A-Za-z0-9._-]+")


class NXPDirectError(RuntimeError):
    """Base exception for NXP direct-store failures that should not crash the run."""


class NXPSchemaChangedError(NXPDirectError):
    """Raised when NXP's site payload no longer matches the expected contract."""


class NXPStoreDisabledError(NXPDirectError):
    """Raised when NXP direct pricing has been disabled for the remainder of the run."""


@dataclass(frozen=True)
class NXPSearchResult:
    """Normalized result row from NXP's structured store-search payload."""

    query: str
    part_id: str
    description: str | None
    buy_direct: bool
    order_actions: tuple[str, ...]
    unit_price: float | None
    suggested_resale_price: float | None
    currency: str | None
    stock_quantity: int | None
    availability: str | None
    packing_name: str | None
    packing_description: str | None
    step_prices: tuple[tuple[int, float], ...]
    package_quality_url: str | None
    raw_url: str | None


@dataclass(frozen=True)
class NXPPartDetail:
    """MOQ/package enrichment parsed from an NXP family part page."""

    query: str
    matched_part_id: str
    minimum_order_quantity: int | None
    minimum_package_quantity: int | None


def _normalized_manufacturer_name(name: str) -> str:
    """Normalize manufacturer names for NXP direct-pricing eligibility checks."""
    return " ".join(name.lower().strip().split())


def nxp_supports_manufacturer(manufacturer: str) -> bool:
    """Return whether BOM Builder should query NXP direct pricing for a part."""
    return _normalized_manufacturer_name(manufacturer) in NXP_MANUFACTURERS


def nxp_is_available() -> bool:
    """Return whether the runtime can launch the browser-backed NXP client."""
    return sync_playwright is not None


def _normalized_part_number(part_number: str) -> str:
    """Normalize part numbers for loose equality checks."""
    return "".join(ch for ch in part_number.upper() if ch.isalnum())


def _part_numbers_equivalent(left: str, right: str) -> bool:
    """Return whether two product identifiers likely refer to the same part."""
    normalized_left = _normalized_part_number(left)
    normalized_right = _normalized_part_number(right)
    return bool(
        normalized_left
        and normalized_right
        and (
            normalized_left == normalized_right
            or normalized_left in normalized_right
            or normalized_right in normalized_left
        )
    )


def _candidate_score(query: str, part_id: str) -> int:
    """Return how strongly one store result matches the requested part number."""
    normalized_query = _normalized_part_number(query)
    normalized_part = _normalized_part_number(part_id)
    if not normalized_query or not normalized_part:
        return 0
    if normalized_query == normalized_part:
        return 100
    if normalized_part.startswith(normalized_query):
        return 90
    if normalized_query.startswith(normalized_part):
        return 80
    if normalized_query in normalized_part or normalized_part in normalized_query:
        return 70
    return 0


def _unique_query_terms(query_terms: list[str] | tuple[str, ...]) -> list[str]:
    """Return non-empty NXP lookup terms with duplicates removed in order."""
    unique: list[str] = []
    seen: set[str] = set()
    for term in query_terms:
        normalized = term.strip()
        if not normalized or normalized in seen:
            continue
        unique.append(normalized)
        seen.add(normalized)
    return unique


def _store_search_url(query: str) -> str:
    """Return the public NXP store search URL for one part query."""
    return (
        f"{NXP_STORE_SEARCH_URL}?collection=salesitem&keyword={quote(query, safe='')}"
        "&language=en&max=12&query=typeTax%3E%3Et000&siblings=false&start=0"
    )


def _part_detail_url(query: str) -> str:
    """Return the NXP family/detail page URL for one BOM part number."""
    return f"{NXP_PART_DETAIL_URL}/{quote(query, safe='')}"


def _part_id_from_result(result: dict[str, Any]) -> str | None:
    """Extract the exact NXP sales-item part number from one search result."""
    summary = str(result.get("summary") or "")
    match = _PART_ID_RE.search(summary)
    if match:
        return match.group(1).strip()

    meta = result.get("metaData") or {}
    part_id = str(meta.get("part_id") or "").strip()
    return part_id or None


def _optional_float(value: object) -> float | None:
    """Return a float from a loose payload field when present."""
    if value in (None, "", False):
        return None
    return float(value)


def _optional_int(value: object) -> int | None:
    """Return a positive integer from a loose payload field when present."""
    if value in (None, "", False):
        return None
    number = int(value)
    return number if number >= 0 else None


def _step_prices(entries: object) -> tuple[tuple[int, float], ...]:
    """Normalize NXP step-price entries into quantity/price pairs."""
    if not isinstance(entries, list):
        return ()
    parsed: list[tuple[int, float]] = []
    for entry in entries:
        match = _STEP_PRICE_RE.match(str(entry))
        if not match:
            continue
        parsed.append(
            (
                int(match.group(1)),
                float(match.group(2).replace(",", "")),
            )
        )
    parsed.sort(key=lambda item: item[0])
    return tuple(parsed)


def _search_result_from_payload(query: str, result: dict[str, Any]) -> NXPSearchResult | None:
    """Normalize one raw store-search result into a typed record."""
    part_id = _part_id_from_result(result)
    if not part_id:
        return None

    meta = result.get("metaData") or {}
    order_actions = tuple(str(item).strip() for item in meta.get("Order") or [] if str(item).strip())
    description = str(meta.get("Description") or "").strip() or None
    stock_quantity = _optional_int(meta.get("stock_quantity"))
    availability = str(meta.get("Availability") or "").strip() or None
    packing_name = str(meta.get("packing_name") or "").strip() or None
    packing_description = str(meta.get("packing_desc") or "").strip() or None
    return NXPSearchResult(
        query=query,
        part_id=part_id,
        description=description,
        buy_direct="Buy Direct" in order_actions,
        order_actions=order_actions,
        unit_price=_optional_float(meta.get("unitPrice")),
        suggested_resale_price=_optional_float(meta.get("suggestRsllPrice")),
        currency="USD" if meta.get("unitPrice") is not None else None,
        stock_quantity=stock_quantity,
        availability=availability,
        packing_name=packing_name,
        packing_description=packing_description,
        step_prices=_step_prices(meta.get("stepPrice")),
        package_quality_url=str(meta.get("packageQualityUrl") or "").strip() or None,
        raw_url=str(result.get("url") or "").strip() or None,
    )


def _select_best_result(query: str, payload: dict[str, Any]) -> NXPSearchResult | None:
    """Return the best matching store-search result for one requested part."""
    results = payload.get("results")
    if not isinstance(results, list):
        raise NXPSchemaChangedError("NXP store payload no longer exposes a 'results' list")

    best: tuple[tuple[int, int, int, int], NXPSearchResult] | None = None
    parsed_any_result = False
    for raw_result in results:
        result = _search_result_from_payload(query, raw_result)
        if result is None:
            continue
        parsed_any_result = True
        score = _candidate_score(query, result.part_id)
        if score <= 0:
            continue
        rank = (
            score,
            1 if result.buy_direct else 0,
            1 if result.step_prices else 0,
            result.stock_quantity or 0,
        )
        if best is None or rank > best[0]:
            best = (rank, result)
    if results and not parsed_any_result:
        raise NXPSchemaChangedError("NXP store results no longer expose stable part identifiers")
    return best[1] if best is not None else None


def _availability_text(result: NXPSearchResult) -> str | None:
    """Return one compact availability summary for a selected NXP result."""
    parts = []
    if result.stock_quantity is not None:
        parts.append(f"{result.stock_quantity:,} in stock")
    if result.availability:
        parts.append(result.availability)
    return "; ".join(dict.fromkeys(parts)) or None


def _part_detail_from_text(query: str, matched_part_id: str, body_text: str) -> NXPPartDetail | None:
    """Parse MOQ data for one selected orderable from a family/detail page body."""
    if "HTTP Status 400" in body_text:
        return None

    lines = [" ".join(line.split()).strip() for line in body_text.splitlines() if line.strip()]
    if not lines:
        return None

    normalized_target = _normalized_part_number(matched_part_id)
    candidate_indices = [
        index
        for index, line in enumerate(lines)
        if _normalized_part_number(line) == normalized_target
    ]
    if not candidate_indices:
        candidate_indices = [
            index
            for index, line in enumerate(lines)
            if _part_numbers_equivalent(line, matched_part_id)
        ]

    for index in candidate_indices:
        line = lines[index]
        if not _part_numbers_equivalent(line, matched_part_id):
            continue
        window = "\n".join(lines[index: index + 18])
        moq_match = _MOQ_RE.search(window)
        mpq_match = _MPQ_RE.search(window)
        return NXPPartDetail(
            query=query,
            matched_part_id=matched_part_id,
            minimum_order_quantity=(
                int(moq_match.group(1).replace(",", "")) if moq_match else None
            ),
            minimum_package_quantity=(
                int(mpq_match.group(1).replace(",", "")) if mpq_match else None
            ),
        )
    return None


def _detail_has_confirmed_quantities(detail: NXPPartDetail | None) -> bool:
    """Return whether a parsed detail record contains actionable MOQ/MPQ data."""
    return bool(
        detail is not None
        and (
            detail.minimum_order_quantity is not None
            or detail.minimum_package_quantity is not None
        )
    )


class NXPClient:
    """Browser-backed client for the public NXP direct store."""

    def __init__(
        self,
        timeout_seconds: float = 30.0,
        cache_enabled: bool = False,
        cache_ttl_seconds: int = 24 * 60 * 60,
    ):
        """Initialize the browser-backed NXP store client."""
        self.timeout_ms = int(timeout_seconds * 1000)
        self._cache = LookupCache(ttl_seconds=cache_ttl_seconds) if cache_enabled else None
        self._playwright_manager = None
        self._browser = None
        self._store_disabled_reason: str | None = None
        self._detail_disabled_reason: str | None = None
        self._runtime_notices: list[str] = []
        self._seen_runtime_notices: set[str] = set()
        self.network_requests = 0

    def close(self) -> None:
        """Close browser and cache resources."""
        if self._browser is not None:
            self._browser.close()
            self._browser = None
        if self._playwright_manager is not None:
            self._playwright_manager.stop()
            self._playwright_manager = None
        if self._cache is not None:
            self._cache.close()

    def __enter__(self) -> "NXPClient":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Release browser and cache resources."""
        self.close()

    @property
    def store_lookup_enabled(self) -> bool:
        """Return whether NXP direct pricing remains enabled for this run."""
        return self._store_disabled_reason is None

    @property
    def detail_enrichment_enabled(self) -> bool:
        """Return whether NXP part-detail enrichment remains enabled for this run."""
        return self._detail_disabled_reason is None

    def consume_runtime_notices(self) -> list[str]:
        """Return and clear any one-shot runtime notices generated by the adapter."""
        notices = self._runtime_notices[:]
        self._runtime_notices.clear()
        return notices

    def _queue_notice(self, message: str) -> None:
        """Queue one user-facing notice once per run."""
        if message in self._seen_runtime_notices:
            return
        self._seen_runtime_notices.add(message)
        self._runtime_notices.append(message)

    def _failure_artifact_dir(self) -> Path:
        """Return the directory used for best-effort NXP failure snapshots."""
        override = os.getenv("BOM_BUILDER_NXP_FAILURE_DIR", "").strip()
        if override:
            return Path(override).expanduser()
        try:
            return default_cache_db_path().parent / "nxp-failures"
        except Exception:
            return Path(tempfile.gettempdir()) / "bom-builder-nxp-failures"

    def _write_failure_artifact(
        self,
        *,
        failure_kind: str,
        reason: str,
        query: str | None = None,
        url: str | None = None,
        error: str | None = None,
        response_text: str | None = None,
        payload: Any | None = None,
        body_text: str | None = None,
    ) -> None:
        """Persist one best-effort failure snapshot for later parser repair."""
        artifact = {
            "captured_at": datetime.now().astimezone().isoformat(timespec="seconds"),
            "failure_kind": failure_kind,
            "reason": reason,
            "query": query,
            "url": url,
            "error": error,
            "response_text": (response_text or "")[:12000] or None,
            "payload": payload,
            "body_text": (body_text or "")[:12000] or None,
        }
        artifact_dir = self._failure_artifact_dir()
        safe_query = _SAFE_ARTIFACT_NAME_RE.sub("-", (query or "unknown").strip()).strip("-") or "unknown"
        filename = (
            f"{datetime.now().astimezone().strftime('%Y%m%d-%H%M%S-%f')}"
            f"-{failure_kind}-{safe_query}.json"
        )
        try:
            artifact_dir.mkdir(parents=True, exist_ok=True)
            (artifact_dir / filename).write_text(
                json.dumps(artifact, indent=2, sort_keys=True),
                encoding="utf-8",
            )
        except Exception:
            return

    def _disable_store_lookup(
        self,
        reason: str,
        *,
        query: str | None = None,
        url: str | None = None,
        error: str | None = None,
        response_text: str | None = None,
        payload: Any | None = None,
    ) -> None:
        """Disable NXP direct pricing for the rest of the run."""
        if self._store_disabled_reason is not None:
            return
        self._store_disabled_reason = reason
        self._write_failure_artifact(
            failure_kind="store-disabled",
            reason=reason,
            query=query,
            url=url,
            error=error,
            response_text=response_text,
            payload=payload,
        )
        self._queue_notice(
            "NXP direct disabled for this run; continuing without NXP direct pricing"
        )

    def _disable_detail_enrichment(
        self,
        reason: str,
        *,
        query: str | None = None,
        matched_part_id: str | None = None,
        url: str | None = None,
        error: str | None = None,
        body_text: str | None = None,
    ) -> None:
        """Disable only the fragile part-detail enrichment path for the run."""
        if self._detail_disabled_reason is not None:
            return
        self._detail_disabled_reason = reason
        self._write_failure_artifact(
            failure_kind="detail-disabled",
            reason=reason,
            query=query,
            url=url,
            error=error,
            payload={"matched_part_id": matched_part_id},
            body_text=body_text,
        )
        self._queue_notice(
            "NXP detail enrichment unavailable; continuing with review-required NXP pricing only"
        )

    def _ensure_browser(self):
        """Launch a supported Chromium-based browser when needed."""
        if self._browser is not None:
            return self._browser
        if sync_playwright is None:
            raise RuntimeError("Playwright is not installed, so NXP direct lookups are unavailable")

        self._playwright_manager = sync_playwright().start()
        launch_errors: list[str] = []
        for channel in NXP_BROWSER_CHANNELS:
            try:
                self._browser = self._playwright_manager.chromium.launch(
                    channel=channel,
                    headless=True,
                )
                return self._browser
            except Exception as exc:  # pragma: no cover - depends on local browser install
                launch_errors.append(f"{channel}: {exc}")

        self._playwright_manager.stop()
        self._playwright_manager = None
        raise RuntimeError(
            "Could not launch a browser for NXP direct lookups. "
            + "; ".join(launch_errors)
        )

    def _cache_key(self, query: str) -> str:
        """Return the stable persistent-cache key for one NXP search lookup."""
        return json.dumps({"query": query.strip().upper()}, sort_keys=True, separators=(",", ":"))

    def _detail_cache_key(self, query: str, matched_part_id: str) -> str:
        """Return the persistent-cache key for one NXP part-detail enrichment lookup."""
        return json.dumps(
            {"query": query.strip().upper(), "matched_part_id": matched_part_id.strip().upper()},
            sort_keys=True,
            separators=(",", ":"),
        )

    def _search_payload(self, query: str) -> dict[str, Any]:
        """Load one structured NXP store-search payload through Playwright."""
        if not self.store_lookup_enabled:
            raise NXPStoreDisabledError(self._store_disabled_reason or "NXP direct lookup is disabled")

        cache_key = self._cache_key(query)
        if self._cache is not None:
            cached = self._cache.get_provider_response("nxp_store_search_payload", cache_key)
            if isinstance(cached, dict):
                try:
                    _select_best_result(query, cached)
                except NXPSchemaChangedError:
                    self._cache.delete_provider_response("nxp_store_search_payload", cache_key)
                else:
                    return cached

        browser = self._ensure_browser()
        context = browser.new_context(locale="en-US")
        page = context.new_page()
        self.network_requests += 1
        response_text = None
        url = _store_search_url(query)
        try:
            with page.expect_response(
                lambda response: "webapp-rest/api/search/getAsset/allResults/" in response.url,
                timeout=self.timeout_ms,
            ) as response_info:
                page.goto(url, wait_until="domcontentloaded", timeout=self.timeout_ms)
            response = response_info.value
            response_text = response.text()
            payload = json.loads(response_text)
            _select_best_result(query, payload)
        except NXPSchemaChangedError as exc:
            self._disable_store_lookup(
                "NXP store response format changed",
                query=query,
                url=url,
                error=str(exc),
                response_text=response_text,
                payload=payload if "payload" in locals() else None,
            )
            raise NXPStoreDisabledError(self._store_disabled_reason or str(exc)) from exc
        except (json.JSONDecodeError, PlaywrightError, RuntimeError) as exc:
            self._disable_store_lookup(
                "NXP store data could not be loaded",
                query=query,
                url=url,
                error=str(exc),
                response_text=response_text,
            )
            raise NXPStoreDisabledError(self._store_disabled_reason or str(exc)) from exc
        except Exception as exc:
            self._disable_store_lookup(
                "NXP store data could not be loaded",
                query=query,
                url=url,
                error=str(exc),
                response_text=response_text,
            )
            raise NXPStoreDisabledError(self._store_disabled_reason or str(exc)) from exc
        finally:
            page.close()
            context.close()

        if self._cache is not None:
            self._cache.set_provider_response("nxp_store_search_payload", cache_key, payload)
        return payload

    def search_result(self, query: str) -> NXPSearchResult | None:
        """Return the best matching structured store-search result for one query."""
        return _select_best_result(query, self._search_payload(query))

    def part_detail(self, query: str, matched_part_id: str) -> NXPPartDetail | None:
        """Return MOQ enrichment from one family/detail page when available."""
        if not self.detail_enrichment_enabled:
            return None

        cache_key = self._detail_cache_key(query, matched_part_id)
        if self._cache is not None:
            cached = self._cache.get_provider_response("nxp_part_detail", cache_key)
            if isinstance(cached, dict):
                if cached == {}:
                    return None
                cached_detail = NXPPartDetail(**cached)
                if _detail_has_confirmed_quantities(cached_detail):
                    return cached_detail

        browser = self._ensure_browser()
        context = browser.new_context(locale="en-US")
        page = context.new_page()
        self.network_requests += 1
        url = _part_detail_url(query)
        detail = None
        body_text = ""
        try:
            page.goto(url, wait_until="domcontentloaded", timeout=self.timeout_ms)
            body_text = page.locator("body").inner_text()
            if (
                matched_part_id not in body_text
                and "HTTP Status 400" not in body_text
                and "Page not available" not in body_text
            ):
                page.wait_for_timeout(1500)
                body_text = page.locator("body").inner_text()
            if (
                matched_part_id in body_text
                and "Min. Order Quantity" not in body_text
                and "Min. Package Quantity" not in body_text
            ):
                try:
                    page.wait_for_timeout(500)
                    body_text = page.locator("body").inner_text()
                except PlaywrightError:
                    pass
            detail = _part_detail_from_text(query, matched_part_id, body_text)
            if detail is None and matched_part_id not in body_text and "HTTP Status 400" not in body_text:
                self._disable_detail_enrichment(
                    "NXP part-detail page format changed",
                    query=query,
                    matched_part_id=matched_part_id,
                    url=url,
                    body_text=body_text,
                )
        except (PlaywrightError, RuntimeError) as exc:
            self._disable_detail_enrichment(
                "NXP part-detail data could not be loaded",
                query=query,
                matched_part_id=matched_part_id,
                url=url,
                error=str(exc),
                body_text=body_text,
            )
        except Exception as exc:
            self._disable_detail_enrichment(
                "NXP part-detail data could not be loaded",
                query=query,
                matched_part_id=matched_part_id,
                url=url,
                error=str(exc),
                body_text=body_text,
            )
        finally:
            page.close()
            context.close()

        if self._detail_disabled_reason is not None and detail is None:
            return None

        if self._cache is not None:
            if _detail_has_confirmed_quantities(detail):
                self._cache.set_provider_response(
                    "nxp_part_detail",
                    cache_key,
                    asdict(detail),
                )
            elif "HTTP Status 400" in body_text:
                self._cache.set_provider_response(
                    "nxp_part_detail",
                    cache_key,
                    {},
                )
        return detail


def _price_breaks_from_search_result(result: NXPSearchResult) -> tuple[FamilyPriceBreak, ...]:
    """Return optimizer-friendly price breaks derived from one NXP search result."""
    if not result.step_prices:
        if result.unit_price is None:
            return ()
        return (
            FamilyPriceBreak(
                quantity=1,
                unit_price=result.unit_price,
                currency=result.currency or "USD",
            ),
        )

    return tuple(
        FamilyPriceBreak(quantity=quantity, unit_price=price, currency=result.currency or "USD")
        for quantity, price in result.step_prices
    )


def price_part_via_nxp(
    agg: AggregatedPart,
    client: NXPClient,
    query_terms: list[str] | tuple[str, ...] | None = None,
) -> DistributorOffer:
    """Resolve one BOM line into a normalized experimental NXP direct offer."""
    attempted = _unique_query_terms(query_terms or [agg.part_number])
    if not attempted:
        return DistributorOffer(
            distributor=NXP_DISTRIBUTOR_NAME,
            required_quantity=agg.total_quantity,
            pricing_strategy="NXP direct lookup",
            lookup_error="No NXP query terms available",
        )

    if not client.store_lookup_enabled:
        return DistributorOffer(
            distributor=NXP_DISTRIBUTOR_NAME,
            required_quantity=agg.total_quantity,
            pricing_strategy="NXP direct lookup",
            lookup_error="NXP direct unavailable for this run",
        )

    last_error = "No NXP direct store data found"
    last_result: NXPSearchResult | None = None

    for query in attempted:
        try:
            result = client.search_result(query)
        except NXPStoreDisabledError:
            last_error = "NXP direct unavailable for this run"
            continue
        except Exception as exc:
            last_error = str(exc)
            continue

        if result is None:
            last_error = "NXP store did not return a matching result"
            continue
        last_result = result
        if not result.buy_direct:
            last_error = "NXP lists this part, but direct buy is not available"
            continue

        price_breaks = _price_breaks_from_search_result(result)
        if not price_breaks:
            last_error = "NXP direct pricing was not available"
            continue

        detail = client.part_detail(query, result.part_id)
        minimum_order_quantity = detail.minimum_order_quantity if detail is not None else None
        order_multiple = detail.minimum_package_quantity if detail is not None else None
        review_required = minimum_order_quantity is None
        strategy = "NXP direct price break"
        if not client.detail_enrichment_enabled:
            strategy = "NXP direct price break (detail unavailable)"
        elif review_required:
            strategy = "NXP direct price break (MOQ not confirmed)"

        families = (
            PurchaseFamily(
                family_id=result.part_id,
                package_type=None,
                packaging_mode=result.packing_description,
                minimum_order_quantity=minimum_order_quantity,
                order_multiple=order_multiple,
                full_reel_quantity=None,
                base_pricing_strategy=strategy,
                strategy_mode="static",
                allow_mixing_as_bulk=False,
                allow_mixing_as_remainder=False,
                price_breaks=price_breaks,
            ),
        )
        selected_plan = optimize_purchase_families(agg.total_quantity, families)
        if selected_plan is None:
            last_error = "NXP pricing did not yield a legal purchase plan"
            continue

        return DistributorOffer(
            distributor=NXP_DISTRIBUTOR_NAME,
            distributor_part_number=result.part_id,
            manufacturer_part_number=result.part_id,
            unit_price=selected_plan.unit_price,
            extended_price=selected_plan.extended_price,
            currency=selected_plan.currency,
            availability=_availability_text(result),
            price_break_quantity=selected_plan.price_break_quantity,
            required_quantity=agg.total_quantity,
            purchased_quantity=selected_plan.purchased_quantity,
            surplus_quantity=selected_plan.surplus_quantity,
            package_type=None,
            packaging_mode=result.packing_description,
            packaging_source="nxp_store_browser_search",
            minimum_order_quantity=minimum_order_quantity,
            order_multiple=order_multiple,
            pricing_strategy=selected_plan.pricing_strategy or strategy,
            order_plan=selected_plan.order_plan,
            purchase_legs=[leg.model_copy(deep=True) for leg in selected_plan.purchase_legs],
            match_method=MatchMethod.EXACT,
            review_required=review_required,
        )

    return DistributorOffer(
        distributor=NXP_DISTRIBUTOR_NAME,
        distributor_part_number=last_result.part_id if last_result is not None else None,
        manufacturer_part_number=last_result.part_id if last_result is not None else None,
        currency=last_result.currency if last_result is not None else None,
        availability=_availability_text(last_result) if last_result is not None else None,
        required_quantity=agg.total_quantity,
        packaging_mode=last_result.packing_description if last_result is not None else None,
        packaging_source="nxp_store_browser_search" if last_result is not None else None,
        pricing_strategy="NXP store search",
        lookup_error=last_error,
    )
