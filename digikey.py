"""Digi-Key Product Information V4 client and pricing helpers.

This module provides the first runtime-facing Digi-Key integration layer for
BOM Builder. Unlike the one-time 3-legged account lookup helper in
``digikey_auth.py``, the code here is designed for normal server-to-server BOM
pricing flows using Digi-Key's 2-legged OAuth model.

The client focuses on the subset of Digi-Key's V4 API that matters most for
the BOM cost engine:

1. locale-aware product detail lookups for a known Digi-Key product number
2. locale-aware quantity pricing via ``pricingbyquantity``
3. bounded compatibility fallbacks across Digi-Key's drifting account/customer
   header conventions
4. in-memory token caching so repeated calls do not request a fresh OAuth token
   every time

The design is intentionally narrow. Rather than mirroring the full Digi-Key
schema, the module normalizes just the fields needed to compare distributor
offers later on: manufacturer identity, requested quantity, total price,
effective unit price, and which header strategy actually worked in production.
"""

from __future__ import annotations

from dataclasses import dataclass
import logging
import os
import re
import time
from typing import Any
from urllib.parse import quote

import httpx

from config import (
    DIGIKEY_API_BASE_URL,
    DIGIKEY_DEFAULT_LOCALE_CURRENCY,
    DIGIKEY_DEFAULT_LOCALE_LANGUAGE,
    DIGIKEY_DEFAULT_LOCALE_SHIP_TO_COUNTRY,
    DIGIKEY_DEFAULT_LOCALE_SITE,
    DIGIKEY_PRODUCTS_V4_BASE_PATH,
    DIGIKEY_TOKEN_REFRESH_SAFETY_SECONDS,
)
from digikey_auth import DigiKeyTokens, resolve_digikey_client_credentials
from models import AggregatedPart, DistributorOffer, MatchMethod
from mouser import manufacturers_match
from optimizer import FamilyPriceBreak, PurchaseFamily, optimize_purchase_families
from secret_store import get_secret

log = logging.getLogger(__name__)

DIGIKEY_OAUTH_TOKEN_URL = f"{DIGIKEY_API_BASE_URL}/v1/oauth2/token"
DIGIKEY_DISTRIBUTOR_NAME = "Digi-Key"


@dataclass(frozen=True)
class DigiKeyLocale:
    """Locale settings used by Digi-Key pricing and availability endpoints.

    Attributes
    ----------
    site:
        Digi-Key storefront/site code such as ``"DE"`` or ``"US"``.
    language:
        Preferred language code such as ``"en"``.
    currency:
        Currency code such as ``"EUR"`` or ``"USD"``.
    ship_to_country:
        Lowercase ISO country code used in Digi-Key's ship-to header.
    """

    site: str
    language: str
    currency: str
    ship_to_country: str


@dataclass(frozen=True)
class DigiKeyPricingProduct:
    """One concrete Digi-Key product entry inside a quantity-pricing option."""

    digikey_product_number: str
    quantity_priced: int
    minimum_order_quantity: int
    unit_price: float
    extended_price: float
    package_type: str | None = None


@dataclass(frozen=True)
class DigiKeyPricingOption:
    """One pricing strategy returned by ``pricingbyquantity``.

    Digi-Key may return multiple ways to satisfy a requested quantity, such as
    an exact quantity, a minimum-order-quantity rounding, or a "better value"
    option using larger package breaks.
    """

    pricing_option: str
    total_quantity_priced: int
    total_price: float
    quantity_available: int | None
    products: tuple[DigiKeyPricingProduct, ...]

    @property
    def effective_unit_price(self) -> float:
        """Return the total option price divided by the total quantity priced."""
        if self.total_quantity_priced <= 0:
            return 0.0
        return self.total_price / self.total_quantity_priced


@dataclass(frozen=True)
class DigiKeyPricingResult:
    """Normalized response from Digi-Key's ``pricingbyquantity`` endpoint."""

    requested_product: str
    requested_quantity: int
    manufacturer_name: str | None
    manufacturer_part_number: str | None
    currency: str
    customer_id_used: int | None
    header_mode_used: str
    rate_limit_remaining: int | None
    my_pricing_options: tuple[DigiKeyPricingOption, ...]
    standard_pricing_options: tuple[DigiKeyPricingOption, ...]
    raw_response: dict[str, Any]


def resolve_digikey_locale(
    site: str = "",
    language: str = "",
    currency: str = "",
    ship_to_country: str = "",
) -> DigiKeyLocale:
    """Resolve Digi-Key locale settings from overrides or ``.env`` defaults.

    Parameters
    ----------
    site:
        Optional explicit Digi-Key locale site override.
    language:
        Optional explicit Digi-Key locale language override.
    currency:
        Optional explicit Digi-Key locale currency override.
    ship_to_country:
        Optional explicit Digi-Key ship-to country override.

    Returns
    -------
    DigiKeyLocale
        Normalized locale settings suitable for request headers.
    """
    resolved_site = (
        site.strip()
        or os.getenv("DIGIKEY_LOCALE_SITE", "").strip()
        or DIGIKEY_DEFAULT_LOCALE_SITE
    ).upper()
    resolved_language = (
        language.strip()
        or os.getenv("DIGIKEY_LOCALE_LANGUAGE", "").strip()
        or DIGIKEY_DEFAULT_LOCALE_LANGUAGE
    ).lower()
    resolved_currency = (
        currency.strip()
        or os.getenv("DIGIKEY_LOCALE_CURRENCY", "").strip()
        or DIGIKEY_DEFAULT_LOCALE_CURRENCY
    ).upper()
    resolved_ship_to_country = (
        ship_to_country.strip()
        or os.getenv("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", "").strip()
        or DIGIKEY_DEFAULT_LOCALE_SHIP_TO_COUNTRY
    ).lower()
    return DigiKeyLocale(
        site=resolved_site,
        language=resolved_language,
        currency=resolved_currency,
        ship_to_country=resolved_ship_to_country,
    )


def best_pricing_option(result: DigiKeyPricingResult) -> DigiKeyPricingOption | None:
    """Return the cheapest available Digi-Key pricing option.

    The helper considers both ``MyPricingOptions`` and ``StandardPricingOptions``
    and chooses the option with the lowest total price. This mirrors the later
    BOM-distributor-comparison step, where Digi-Key should compete based on the
    actual total cost needed to satisfy the requested quantity.
    """
    options = [
        option
        for option in list(result.my_pricing_options) + list(result.standard_pricing_options)
        if option.total_quantity_priced > 0 and option.total_price > 0
    ]
    if not options:
        return None
    satisfying = [
        option for option in options if option.total_quantity_priced >= result.requested_quantity
    ]
    comparable = satisfying or options
    return min(
        comparable,
        key=lambda option: (option.total_price, option.total_quantity_priced),
    )


def digikey_is_configured() -> bool:
    """Return whether Digi-Key client credentials are available to the runtime."""
    try:
        resolve_digikey_client_credentials()
    except ValueError:
        return False
    return True


class DigiKeyClient:
    """Locale-aware Digi-Key Product Information V4 client.

    The client handles:

    - 2-legged OAuth token retrieval and short-lived in-memory caching
    - locale-specific request headers for market/currency selection
    - compatibility fallback between ``Account-Id``, ``Customer-Id: 0``, and
      no account/customer header when Digi-Key's behavior differs between docs
      and production
    """

    def __init__(
        self,
        client_id: str = "",
        client_secret: str = "",
        account_id: str = "",
        locale: DigiKeyLocale | None = None,
        timeout_seconds: float = 30.0,
    ):
        """Initialize the Digi-Key client from explicit or environment config."""
        self.client_id, self.client_secret = resolve_digikey_client_credentials(
            client_id,
            client_secret,
        )
        self.account_id = account_id.strip() or get_secret("digikey_account_id")
        self.locale = locale or resolve_digikey_locale()
        self.timeout_seconds = timeout_seconds
        self._client = httpx.Client(timeout=timeout_seconds)
        self._tokens: DigiKeyTokens | None = None
        self._token_expires_at = 0.0
        self.network_requests = 0

    def close(self) -> None:
        """Close underlying HTTP resources."""
        self._client.close()

    def __enter__(self) -> "DigiKeyClient":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Release HTTP resources at the end of a ``with`` block."""
        self.close()

    def product_details(self, product_number: str) -> tuple[dict[str, Any], str]:
        """Return raw Digi-Key product-details JSON plus the header mode used."""
        path = f"/search/{quote(product_number, safe='')}/productdetails"
        response, header_mode = self._request_with_header_fallback(path)
        return response.json(), header_mode

    def pricing_by_quantity(
        self,
        product_number: str,
        requested_quantity: int,
    ) -> DigiKeyPricingResult:
        """Return normalized Digi-Key quantity pricing for one product number."""
        path = (
            f"/search/{quote(product_number, safe='')}/pricingbyquantity/"
            f"{requested_quantity}"
        )
        response, header_mode = self._request_with_header_fallback(path)
        payload = response.json()
        return _parse_pricing_result(
            payload,
            header_mode_used=header_mode,
            rate_limit_remaining=_header_int(response.headers, "X-RateLimit-Remaining"),
        )

    def _request_with_header_fallback(
        self,
        path: str,
    ) -> tuple[httpx.Response, str]:
        """Issue one GET request using the best available account header mode.

        Digi-Key's documentation has drifted between ``X-DIGIKEY-Customer-Id``
        and ``X-DIGIKEY-Account-Id``. The runtime therefore prefers the current
        account-ID contract, but can fall back to ``Customer-Id: 0`` and then
        finally to no account/customer header when production accepts that mode.
        """
        token = self._access_token()
        url = f"{DIGIKEY_API_BASE_URL}{DIGIKEY_PRODUCTS_V4_BASE_PATH}{path}"
        last_response: httpx.Response | None = None

        for header_mode, extra_headers in self._header_mode_candidates():
            headers = self._base_headers(token)
            headers.update(extra_headers)
            self.network_requests += 1
            response = self._client.get(url, headers=headers)
            if response.status_code < 400:
                log.debug("Digi-Key request succeeded with header mode %s", header_mode)
                return response, header_mode

            last_response = response
            if response.status_code not in {401, 403}:
                response.raise_for_status()

            log.debug(
                "Digi-Key request failed with header mode %s: HTTP %s",
                header_mode,
                response.status_code,
            )

        assert last_response is not None
        last_response.raise_for_status()
        raise AssertionError("unreachable")

    def _access_token(self) -> str:
        """Return a cached Digi-Key bearer token, refreshing when needed."""
        now = time.time()
        if self._tokens is not None and now < self._token_expires_at:
            return self._tokens.access_token

        payload = {
            "client_id": self.client_id,
            "client_secret": self.client_secret,
            "grant_type": "client_credentials",
        }
        self.network_requests += 1
        response = self._client.post(DIGIKEY_OAUTH_TOKEN_URL, data=payload)
        response.raise_for_status()
        data = response.json()
        self._tokens = DigiKeyTokens(
            access_token=str(data["access_token"]),
            token_type=str(data.get("token_type") or "Bearer"),
            expires_in=int(data.get("expires_in") or 0),
            refresh_token=None,
            refresh_token_expires_in=None,
            scope=str(data.get("scope") or "") or None,
        )
        self._token_expires_at = now + max(
            0,
            self._tokens.expires_in - DIGIKEY_TOKEN_REFRESH_SAFETY_SECONDS,
        )
        return self._tokens.access_token

    def _base_headers(self, access_token: str) -> dict[str, str]:
        """Build the Digi-Key headers shared by product and pricing requests."""
        return {
            "Authorization": f"Bearer {access_token}",
            "X-DIGIKEY-Client-Id": self.client_id,
            "X-DIGIKEY-Locale-Site": self.locale.site,
            "X-DIGIKEY-Locale-Language": self.locale.language,
            "X-DIGIKEY-Locale-Currency": self.locale.currency,
            "X-DIGIKEY-Locale-ShipToCountry": self.locale.ship_to_country,
        }

    def _header_mode_candidates(self) -> tuple[tuple[str, dict[str, str]], ...]:
        """Return candidate Digi-Key account/customer header strategies."""
        candidates: list[tuple[str, dict[str, str]]] = []
        if self.account_id:
            candidates.append(
                ("account_id", {"X-DIGIKEY-Account-Id": self.account_id})
            )
        candidates.append(("customer_zero", {"X-DIGIKEY-Customer-Id": "0"}))
        candidates.append(("none", {}))
        return tuple(candidates)


def _parse_pricing_result(
    payload: dict[str, Any],
    *,
    header_mode_used: str,
    rate_limit_remaining: int | None,
) -> DigiKeyPricingResult:
    """Normalize one Digi-Key ``pricingbyquantity`` response payload."""
    settings_used = payload.get("SettingsUsed")
    settings = settings_used if isinstance(settings_used, dict) else {}
    search_locale_used = settings.get("SearchLocaleUsed")
    locale_used = search_locale_used if isinstance(search_locale_used, dict) else {}
    manufacturer = payload.get("Manufacturer")
    manufacturer_data = manufacturer if isinstance(manufacturer, dict) else {}
    currency = str(locale_used.get("Currency") or "").strip()
    return DigiKeyPricingResult(
        requested_product=str(payload.get("RequestedProduct") or "").strip(),
        requested_quantity=int(payload.get("RequestedQuantity") or 0),
        manufacturer_name=_optional_str(manufacturer_data, "Name"),
        manufacturer_part_number=_optional_str(payload, "ManufacturerPartNumber"),
        currency=currency or DIGIKEY_DEFAULT_LOCALE_CURRENCY,
        customer_id_used=_optional_int(settings, "CustomerIdUsed"),
        header_mode_used=header_mode_used,
        rate_limit_remaining=rate_limit_remaining,
        my_pricing_options=_parse_pricing_options(payload.get("MyPricingOptions")),
        standard_pricing_options=_parse_pricing_options(
            payload.get("StandardPricingOptions")
        ),
        raw_response=payload,
    )


def _parse_pricing_options(raw_options: Any) -> tuple[DigiKeyPricingOption, ...]:
    """Parse one Digi-Key pricing-options array into typed records."""
    if not isinstance(raw_options, list):
        return ()

    parsed: list[DigiKeyPricingOption] = []
    for raw_option in raw_options:
        if not isinstance(raw_option, dict):
            continue
        products_raw = raw_option.get("Products")
        products: list[DigiKeyPricingProduct] = []
        if isinstance(products_raw, list):
            for raw_product in products_raw:
                if not isinstance(raw_product, dict):
                    continue
                package_type = raw_product.get("PackageType")
                package = package_type if isinstance(package_type, dict) else {}
                products.append(
                    DigiKeyPricingProduct(
                        digikey_product_number=str(
                            raw_product.get("DigiKeyProductNumber") or ""
                        ).strip(),
                        quantity_priced=int(raw_product.get("QuantityPriced") or 0),
                        minimum_order_quantity=int(
                            raw_product.get("MinimumOrderQuantity") or 0
                        ),
                        unit_price=float(raw_product.get("UnitPrice") or 0),
                        extended_price=float(raw_product.get("ExtendedPrice") or 0),
                        package_type=_optional_str(package, "Name"),
                    )
                )
        parsed.append(
            DigiKeyPricingOption(
                pricing_option=str(raw_option.get("PricingOption") or "").strip(),
                total_quantity_priced=int(raw_option.get("TotalQuantityPriced") or 0),
                total_price=float(raw_option.get("TotalPrice") or 0),
                quantity_available=_optional_int(raw_option, "QuantityAvailable"),
                products=tuple(products),
            )
        )
    return tuple(parsed)


def _optional_str(payload: dict[str, Any], key: str) -> str | None:
    """Return a stripped string field from a decoded JSON object when present."""
    value = payload.get(key)
    if value is None:
        return None
    text = str(value).strip()
    return text or None


def _optional_int(payload: dict[str, Any], key: str) -> int | None:
    """Return an integer field from a decoded JSON object when present."""
    value = payload.get(key)
    if value in (None, ""):
        return None
    return int(value)


def _header_int(headers: httpx.Headers, key: str) -> int | None:
    """Return an integer response-header value when present."""
    value = headers.get(key)
    if not value:
        return None
    return int(value)


def price_part_via_digikey(
    agg: AggregatedPart,
    client: DigiKeyClient,
    query_terms: list[str] | tuple[str, ...] | None = None,
) -> DistributorOffer:
    """Resolve one BOM line into a normalized Digi-Key offer.

    Parameters
    ----------
    agg:
        Aggregated BOM line being priced.
    client:
        Active Digi-Key client configured for the target locale.
    query_terms:
        Preferred lookup terms to try in order. This typically contains the
        best resolved manufacturer part number first and falls back to the
        original BOM part number.

    Returns
    -------
    DistributorOffer
        Normalized Digi-Key offer. When no valid result is available, the
        returned offer contains ``lookup_error`` and no price.
    """
    attempted = _unique_query_terms(query_terms or [agg.part_number])
    last_error = "No results found on Digi-Key"
    last_result: DigiKeyPricingResult | None = None

    for query in attempted:
        try:
            result = client.pricing_by_quantity(query, agg.total_quantity)
        except httpx.HTTPStatusError as e:
            last_error = f"HTTP {e.response.status_code}: {e.response.text[:200]}"
            continue
        except Exception as e:
            last_error = str(e)
            continue

        last_result = result
        if not manufacturers_match(agg.manufacturer, result.manufacturer_name or ""):
            last_error = (
                "Manufacturer mismatch on Digi-Key: "
                f"expected {agg.manufacturer}, got {result.manufacturer_name or 'unknown'}"
            )
            continue

        returned_mpn = result.manufacturer_part_number or ""
        if returned_mpn and not _part_numbers_equivalent(query, returned_mpn):
            last_error = (
                "Digi-Key resolved a different manufacturer part number: "
                f"{returned_mpn}"
            )
            continue

        families = _digikey_purchase_families(result)
        plan = optimize_purchase_families(result.requested_quantity, families)
        if plan is None:
            last_error = "No Digi-Key pricing options available"
            continue

        selected_leg = plan.purchase_legs[0] if plan.purchase_legs else None
        if selected_leg is None:
            last_error = "No Digi-Key pricing options available"
            continue
        selected_product = _best_matching_product(result, selected_leg)
        purchased_quantity = plan.purchased_quantity
        distributor_pn = (
            selected_product.digikey_product_number
            if selected_product is not None and selected_product.digikey_product_number
            else result.requested_product or query
        )
        return DistributorOffer(
            distributor=DIGIKEY_DISTRIBUTOR_NAME,
            distributor_part_number=distributor_pn,
            manufacturer_part_number=returned_mpn or query,
            unit_price=plan.unit_price,
            extended_price=plan.extended_price,
            currency=plan.currency,
            availability=_availability_text(_availability_for_plan(result, plan)),
            price_break_quantity=plan.price_break_quantity or agg.total_quantity,
            required_quantity=agg.total_quantity,
            purchased_quantity=purchased_quantity,
            surplus_quantity=max(purchased_quantity - agg.total_quantity, 0),
            package_type=selected_leg.package_type,
            packaging_mode=selected_leg.packaging_mode,
            packaging_source="digikey_api",
            minimum_order_quantity=selected_product.minimum_order_quantity if selected_product is not None else None,
            order_multiple=selected_product.minimum_order_quantity if selected_product is not None else None,
            full_reel_quantity=(
                selected_product.minimum_order_quantity
                if selected_product is not None
                and selected_product.minimum_order_quantity > 1
                and "reel" in ((selected_product.package_type or "").lower())
                else None
            ),
            pricing_strategy=plan.pricing_strategy or None,
            order_plan=plan.order_plan,
            purchase_legs=[leg.model_copy(deep=True) for leg in plan.purchase_legs],
            match_method=MatchMethod.EXACT,
        )

    return DistributorOffer(
        distributor=DIGIKEY_DISTRIBUTOR_NAME,
        distributor_part_number=_last_digikey_product_number(last_result),
        manufacturer_part_number=(
            last_result.manufacturer_part_number if last_result is not None else None
        ),
        currency=last_result.currency if last_result is not None else None,
        required_quantity=agg.total_quantity,
        packaging_source="digikey_api" if last_result is not None else None,
        lookup_error=last_error,
    )


def _unique_query_terms(query_terms: list[str] | tuple[str, ...]) -> list[str]:
    """Return non-empty lookup terms with duplicates removed in order."""
    unique: list[str] = []
    seen: set[str] = set()
    for term in query_terms:
        normalized = term.strip()
        if not normalized or normalized in seen:
            continue
        unique.append(normalized)
        seen.add(normalized)
    return unique


def _part_numbers_equivalent(left: str, right: str) -> bool:
    """Return whether two part numbers likely refer to the same orderable MPN."""
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


def _normalized_part_number(part_number: str) -> str:
    """Normalize part numbers for loose equality checks across distributors."""
    return re.sub(r"[^A-Z0-9]", "", part_number.upper())


def _best_priced_product(
    option: DigiKeyPricingOption,
) -> DigiKeyPricingProduct | None:
    """Return the concrete priced Digi-Key product with the lowest total cost."""
    if not option.products:
        return None
    return min(option.products, key=lambda product: product.extended_price)


def _availability_text(quantity_available: int | None) -> str | None:
    """Return a human-readable availability string for normalized offers."""
    if quantity_available is None:
        return None
    return f"{quantity_available} available"


def _package_type_summary(option: DigiKeyPricingOption) -> str | None:
    """Return a readable package-type summary for one Digi-Key option."""
    package_types: list[str] = []
    for product in option.products:
        package_type = (product.package_type or "").strip()
        if package_type and package_type not in package_types:
            package_types.append(package_type)
    if not package_types:
        return None
    return ", ".join(package_types)


def _last_digikey_product_number(
    result: DigiKeyPricingResult | None,
) -> str | None:
    """Return the first Digi-Key product number visible in a pricing result."""
    if result is None:
        return None
    option = best_pricing_option(result)
    product = _best_priced_product(option) if option is not None else None
    if product is not None and product.digikey_product_number:
        return product.digikey_product_number
    return result.requested_product or None


def _digikey_purchase_families(
    result: DigiKeyPricingResult,
) -> tuple[PurchaseFamily, ...]:
    """Return normalized purchase families derived from Digi-Key pricing options."""
    families: list[PurchaseFamily] = []
    options = list(result.my_pricing_options) + list(result.standard_pricing_options)
    for option in options:
        product = _best_priced_product(option)
        price_break = FamilyPriceBreak(
            quantity=option.total_quantity_priced,
            unit_price=option.effective_unit_price,
            currency=result.currency,
        )
        minimum_order_quantity = product.minimum_order_quantity if product is not None else None
        package_summary = _package_type_summary(option)
        full_reel_quantity = (
            product.minimum_order_quantity
            if product is not None
            and product.minimum_order_quantity > 1
            and "reel" in ((product.package_type or "").lower())
            else None
        )
        families.append(
            PurchaseFamily(
                family_id=option.pricing_option or f"option_{option.total_quantity_priced}",
                package_type=package_summary,
                packaging_mode=package_summary,
                minimum_order_quantity=minimum_order_quantity,
                order_multiple=minimum_order_quantity,
                full_reel_quantity=full_reel_quantity,
                base_pricing_strategy=option.pricing_option or "Digi-Key option",
                strategy_mode="static",
                allow_mixing_as_bulk=False,
                allow_mixing_as_remainder=False,
                price_breaks=(price_break,),
            )
        )
    return tuple(families)


def _best_matching_product(
    result: DigiKeyPricingResult,
    selected_leg,
) -> DigiKeyPricingProduct | None:
    """Return the Digi-Key product that best matches the selected optimizer leg."""
    options = list(result.my_pricing_options) + list(result.standard_pricing_options)
    for option in options:
        if option.total_quantity_priced != selected_leg.purchased_quantity:
            continue
        if round(option.effective_unit_price, 6) != round(selected_leg.unit_price, 6):
            continue
        return _best_priced_product(option)
    best_option = best_pricing_option(result)
    return _best_priced_product(best_option) if best_option is not None else None


def _availability_for_plan(
    result: DigiKeyPricingResult,
    plan,
) -> int | None:
    """Return the best Digi-Key availability figure associated with a chosen plan."""
    options = list(result.my_pricing_options) + list(result.standard_pricing_options)
    for option in options:
        if option.total_quantity_priced != plan.purchased_quantity:
            continue
        if round(option.effective_unit_price, 6) != round(plan.unit_price, 6):
            continue
        return option.quantity_available
    best_option = best_pricing_option(result)
    return best_option.quantity_available if best_option is not None else None
