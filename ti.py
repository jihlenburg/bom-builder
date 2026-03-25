"""Texas Instruments Store Inventory and Pricing API client.

This module integrates TI direct store pricing into BOM Builder using the TI
Store API suite's inventory and pricing endpoint:

https://transact.ti.com/v2/store/products/{part_number}

The TI store API is the correct direct-purchase source for BOM Builder because
it exposes real-time inventory, explicit currency-tagged price breaks, minimum
order quantities, standard pack quantities, package carrier metadata, and
order limits. That makes it a much better fit for cross-distributor BOM cost
comparison than the broader TI product-information APIs.
"""

from __future__ import annotations

from dataclasses import dataclass
import os
import subprocess
import time
from typing import Any
from urllib.parse import quote, urlencode

import httpx

from models import AggregatedPart, DistributorOffer, MatchMethod
from optimizer import FamilyPriceBreak, PurchaseFamily, optimize_purchase_families
from secret_store import get_secret

TI_OAUTH_TOKEN_URL = "https://transact.ti.com/v1/oauth/accesstoken"
TI_STORE_PRODUCTS_API_URL = "https://transact.ti.com/v2/store/products"
TI_DISTRIBUTOR_NAME = "TI"
TI_MANUFACTURERS = {"texas instruments", "ti"}


@dataclass(frozen=True)
class TIPriceBreak:
    """One TI direct price tier."""

    price_break_quantity: int
    price: float


@dataclass(frozen=True)
class TIPricingSchedule:
    """One TI pricing schedule for a specific currency."""

    currency: str
    price_breaks: tuple[TIPriceBreak, ...]


@dataclass(frozen=True)
class TIProduct:
    """Normalized TI store product/pricing payload."""

    query: str
    ti_part_number: str | None
    generic_part_number: str | None
    buy_now_url: str | None
    quantity_available: int | None
    order_limit: int | None
    description: str | None
    minimum_order_quantity: int | None
    standard_pack_quantity: int | None
    package_type: str | None
    package_carrier: str | None
    custom_reel: bool | None
    life_cycle: str | None
    pricing: tuple[TIPricingSchedule, ...]
    raw_response: dict[str, Any]


class TIOAuthError(RuntimeError):
    """Raised when TI OAuth token acquisition fails."""


def _normalized_manufacturer_name(name: str) -> str:
    """Normalize manufacturer names for TI direct-pricing eligibility checks."""
    return " ".join(name.lower().strip().split())


def ti_supports_manufacturer(manufacturer: str) -> bool:
    """Return whether BOM Builder should query TI direct pricing for a part."""
    return _normalized_manufacturer_name(manufacturer) in TI_MANUFACTURERS


def _configured_ti_value(
    primary_secret_name: str,
    legacy_secret_name: str,
    legacy_env_var: str,
) -> str:
    """Return a TI config value, preferring store-specific names over legacy ones."""
    return (
        get_secret(primary_secret_name)
        or get_secret(legacy_secret_name)
        or os.getenv(legacy_env_var, "").strip()
    )


def resolve_ti_price_currency(price_currency: str = "") -> str:
    """Return the request currency used for TI store pricing lookups."""
    resolved = (
        price_currency.strip()
        or os.getenv("TI_STORE_PRICE_CURRENCY", "").strip()
        or os.getenv("TI_PRODUCT_PRICE_CURRENCY", "").strip()
        or "USD"
    ).upper()
    return resolved or "USD"


def ti_is_configured() -> bool:
    """Return whether TI store API credentials are available to the runtime."""
    return bool(
        _configured_ti_value(
            "ti_store_api_key",
            "ti_product_api_key",
            "TI_PRODUCT_API_KEY",
        )
        and _configured_ti_value(
            "ti_store_api_secret",
            "ti_product_api_secret",
            "TI_PRODUCT_API_SECRET",
        )
    )


def _resolve_ti_credentials(
    client_id: str = "",
    client_secret: str = "",
) -> tuple[str, str]:
    """Resolve TI store API credentials from arguments or environment."""
    resolved_id = client_id.strip() or _configured_ti_value(
        "ti_store_api_key",
        "ti_product_api_key",
        "TI_PRODUCT_API_KEY",
    )
    resolved_secret = client_secret.strip() or _configured_ti_value(
        "ti_store_api_secret",
        "ti_product_api_secret",
        "TI_PRODUCT_API_SECRET",
    )
    if not resolved_id or not resolved_secret:
        raise ValueError(
            (
                "TI Store API credentials not set. Configure TI_STORE_API_KEY and "
                "TI_STORE_API_SECRET in .env or pass them explicitly."
            )
        )
    return resolved_id, resolved_secret


def _optional_int(value: object) -> int | None:
    """Return a positive integer from a loose API field when present."""
    if value in (None, "", False):
        return None
    number = int(value)
    return number if number > 0 else None


def _optional_float(value: object) -> float | None:
    """Return a float from a loose API field when present."""
    if value in (None, "", False):
        return None
    return float(value)


def _optional_bool(value: object) -> bool | None:
    """Return a bool from a loose API field when present."""
    if value in (None, ""):
        return None
    if isinstance(value, bool):
        return value
    text = str(value).strip().lower()
    if text in {"true", "1", "yes"}:
        return True
    if text in {"false", "0", "no"}:
        return False
    return None


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


def _unique_query_terms(query_terms: list[str] | tuple[str, ...]) -> list[str]:
    """Return non-empty TI lookup terms with duplicates removed in order."""
    unique: list[str] = []
    seen: set[str] = set()
    for term in query_terms:
        normalized = term.strip()
        if not normalized or normalized in seen:
            continue
        unique.append(normalized)
        seen.add(normalized)
    return unique


def _availability_text(product: TIProduct) -> str | None:
    """Return one compact availability summary for a TI store product."""
    parts = []
    if product.quantity_available is not None:
        parts.append(f"{product.quantity_available:,} in stock")
    if product.order_limit is not None:
        parts.append(f"limit {product.order_limit:,}")
    if product.life_cycle:
        parts.append(product.life_cycle)
    return "; ".join(parts) or None


def _select_pricing_schedule(
    product: TIProduct,
    requested_currency: str,
) -> TIPricingSchedule | None:
    """Return the best TI pricing schedule for the requested currency."""
    if not product.pricing:
        return None
    requested = requested_currency.upper().strip()
    if requested:
        for schedule in product.pricing:
            if schedule.currency.upper() == requested:
                return schedule
    return product.pricing[0]


def _full_reel_quantity(product: TIProduct) -> int | None:
    """Return the full reel quantity when TI explicitly exposes reel packaging."""
    carrier = (product.package_carrier or "").lower()
    if (
        product.standard_pack_quantity
        and any(token in carrier for token in ("reel", "t&r"))
    ):
        return product.standard_pack_quantity
    return None


class TIClient:
    """Authenticated client for TI's store inventory and pricing API.

    In live runs, TI's Akamai edge currently accepts the documented curl flow
    while rejecting equivalent requests from ``httpx`` with HTTP 403. The
    client therefore uses curl-backed requests by default and keeps the Python
    transport path available for unit tests.
    """

    def __init__(
        self,
        client_id: str = "",
        client_secret: str = "",
        price_currency: str = "",
        timeout_seconds: float = 30.0,
    ):
        """Initialize the TI client from explicit or environment config."""
        self.client_id, self.client_secret = _resolve_ti_credentials(
            client_id,
            client_secret,
        )
        self.price_currency = resolve_ti_price_currency(price_currency)
        self.timeout_seconds = timeout_seconds
        self._client = httpx.Client(timeout=timeout_seconds)
        self.use_curl = True
        self._access_token = ""
        self._token_expires_at = 0.0
        self.network_requests = 0

    def close(self) -> None:
        """Close underlying HTTP resources."""
        self._client.close()

    def __enter__(self) -> "TIClient":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Release HTTP resources at the end of a ``with`` block."""
        self.close()

    def _ensure_access_token(self) -> str:
        """Return a fresh TI OAuth access token."""
        now = time.time()
        if self._access_token and now < self._token_expires_at:
            return self._access_token

        payload = self._request_json(
            "POST",
            TI_OAUTH_TOKEN_URL,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
            form_data={
                "grant_type": "client_credentials",
                "client_id": self.client_id,
                "client_secret": self.client_secret,
            },
        )
        self._access_token = str(payload.get("access_token") or "")
        expires_in = int(payload.get("expires_in") or 0)
        if not self._access_token or expires_in <= 0:
            raise ValueError("TI OAuth response did not include a usable access token")
        self._token_expires_at = now + max(expires_in - 60, 0)
        return self._access_token

    def _request_headers(self, token: str) -> dict[str, str]:
        """Return standard headers for TI store product requests."""
        return {
            "Authorization": f"Bearer {token}",
            "Accept": "application/json",
        }

    def _curl_response(
        self,
        method: str,
        url: str,
        *,
        headers: dict[str, str] | None = None,
        form_data: dict[str, str] | None = None,
    ) -> httpx.Response:
        """Execute one HTTP request through curl and return an httpx-style response."""
        marker = "__BOM_BUILDER_HTTP_STATUS__:"
        config_lines = [
            f'url = "{url}"',
            f'request = "{method.upper()}"',
        ]
        for name, value in (headers or {}).items():
            header_value = f"{name}: {value}".replace("\\", "\\\\").replace('"', '\\"')
            config_lines.append(f'header = "{header_value}"')
        if form_data:
            encoded = urlencode(form_data)
            encoded = encoded.replace("\\", "\\\\").replace('"', '\\"')
            config_lines.append(f'data = "{encoded}"')

        self.network_requests += 1
        completed = subprocess.run(
            [
                "curl",
                "--silent",
                "--show-error",
                "--config",
                "-",
                "--write-out",
                f"\\n{marker}%{{http_code}}",
            ],
            input="\n".join(config_lines) + "\n",
            text=True,
            capture_output=True,
            check=False,
            timeout=self.timeout_seconds,
        )
        if completed.returncode != 0:
            raise RuntimeError(
                f"curl exited with status {completed.returncode}: {completed.stderr.strip()}"
            )

        body, separator, status_text = completed.stdout.rpartition(marker)
        if not separator:
            raise RuntimeError("curl response did not include an HTTP status marker")
        status_code = int(status_text.strip())
        request = httpx.Request(method.upper(), url, headers=headers)
        response = httpx.Response(status_code, text=body, request=request)
        return response

    def _request_json(
        self,
        method: str,
        url: str,
        *,
        headers: dict[str, str] | None = None,
        form_data: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        """Execute one HTTP request and return the decoded JSON payload."""
        if self.use_curl:
            response = self._curl_response(
                method,
                url,
                headers=headers,
                form_data=form_data,
            )
        else:
            self.network_requests += 1
            if method.upper() == "POST":
                response = self._client.post(url, headers=headers, data=form_data)
            elif method.upper() == "GET":
                response = self._client.get(url, headers=headers)
            else:
                raise ValueError(f"Unsupported TI request method: {method}")

        try:
            response.raise_for_status()
        except httpx.HTTPStatusError as e:
            if url == TI_OAUTH_TOKEN_URL:
                raise TIOAuthError(_oauth_lookup_error(e)) from e
            raise
        return response.json()

    def _product_from_payload(self, query: str, payload: dict[str, Any]) -> TIProduct:
        """Normalize one TI store product payload into the local dataclass."""
        pricing: list[TIPricingSchedule] = []
        for schedule_payload in payload.get("pricing") or []:
            currency = str(schedule_payload.get("currency") or "").strip().upper()
            price_breaks = []
            for price_break_payload in schedule_payload.get("priceBreaks") or []:
                quantity = _optional_int(price_break_payload.get("priceBreakQuantity"))
                price = _optional_float(price_break_payload.get("price"))
                if quantity is None or price is None:
                    continue
                price_breaks.append(
                    TIPriceBreak(
                        price_break_quantity=quantity,
                        price=price,
                    )
                )
            if currency and price_breaks:
                pricing.append(
                    TIPricingSchedule(
                        currency=currency,
                        price_breaks=tuple(price_breaks),
                    )
                )

        return TIProduct(
            query=query,
            ti_part_number=str(payload.get("tiPartNumber") or "").strip() or None,
            generic_part_number=str(payload.get("genericPartNumber") or "").strip() or None,
            buy_now_url=str(payload.get("buyNowURL") or "").strip() or None,
            quantity_available=_optional_int(payload.get("quantity")),
            order_limit=_optional_int(payload.get("limit")),
            description=str(payload.get("description") or "").strip() or None,
            minimum_order_quantity=_optional_int(payload.get("minimumOrderQuantity")),
            standard_pack_quantity=_optional_int(payload.get("standardPackQuantity")),
            package_type=str(payload.get("packageType") or "").strip() or None,
            package_carrier=str(payload.get("packageCarrier") or "").strip() or None,
            custom_reel=_optional_bool(payload.get("customReel")),
            life_cycle=str(payload.get("lifeCycle") or "").strip() or None,
            pricing=tuple(pricing),
            raw_response=payload,
        )

    def product(self, product_number: str) -> TIProduct:
        """Return normalized TI store pricing information for one part number."""
        token = self._ensure_access_token()
        payload = self._request_json(
            "GET",
            (
                f"{TI_STORE_PRODUCTS_API_URL}/{quote(product_number, safe='')}"
                f"?currency={quote(self.price_currency, safe='')}"
            ),
            headers=self._request_headers(token),
        )
        return self._product_from_payload(product_number, payload)


def _http_status_lookup_error(e: httpx.HTTPStatusError) -> str:
    """Return a concise TI store product-endpoint error summary."""
    status_code = e.response.status_code
    if status_code == 403:
        return (
            "HTTP 403 from TI store inventory/pricing lookup. The TI app appears "
            "authenticated, but it is not authorized for the requested store endpoint."
        )
    if status_code == 404:
        return "TI store inventory/pricing API did not find that part number"
    body = e.response.text[:200].strip()
    return f"HTTP {status_code}: {body}"


def _oauth_lookup_error(e: httpx.HTTPStatusError) -> str:
    """Return a concise user-facing TI OAuth error summary."""
    status_code = e.response.status_code
    if status_code == 401:
        return "TI OAuth token request returned HTTP 401. Check the TI API key and secret."
    if status_code == 403:
        return (
            "TI OAuth token request returned HTTP 403. Verify the TI API key/secret "
            "pair and confirm this app is approved for the TI Store API suite."
        )
    body = e.response.text[:200].strip()
    return f"TI OAuth token request failed with HTTP {status_code}: {body}"


def price_part_via_ti(
    agg: AggregatedPart,
    client: TIClient,
    query_terms: list[str] | tuple[str, ...] | None = None,
) -> DistributorOffer:
    """Resolve one BOM line into a normalized TI direct-purchase offer."""
    attempted = _unique_query_terms(query_terms or [agg.part_number])
    last_error = "No TI store product data found"
    last_product: TIProduct | None = None

    for query in attempted:
        try:
            product = client.product(query)
        except TIOAuthError as e:
            last_error = str(e)
            break
        except httpx.HTTPStatusError as e:
            last_error = _http_status_lookup_error(e)
            continue
        except Exception as e:
            last_error = str(e)
            continue

        last_product = product
        candidates = [
            value
            for value in [product.ti_part_number, product.generic_part_number]
            if value
        ]
        if candidates and not any(
            _part_numbers_equivalent(query, candidate)
            for candidate in candidates
        ):
            last_error = (
                "TI resolved a different product identifier: "
                f"{product.ti_part_number or product.generic_part_number or 'unknown'}"
            )
            continue

        pricing_schedule = _select_pricing_schedule(product, client.price_currency)
        if pricing_schedule is None:
            last_error = "TI store inventory/pricing API did not return pricing"
            continue

        if (
            product.order_limit is not None
            and agg.total_quantity > product.order_limit
        ):
            last_error = (
                "TI store order limit "
                f"{product.order_limit} is below required quantity {agg.total_quantity}"
            )
            continue

        families = _ti_purchase_families(product, pricing_schedule)
        selected_plan = optimize_purchase_families(agg.total_quantity, families)
        if selected_plan is None:
            if product.order_limit is not None and agg.total_quantity > product.order_limit:
                last_error = (
                    "TI store order limit "
                    f"{product.order_limit} is below required quantity {agg.total_quantity}"
                )
            else:
                last_error = "TI store pricing did not yield a legal purchase plan"
            continue

        full_reel_quantity = _full_reel_quantity(product)

        return DistributorOffer(
            distributor=TI_DISTRIBUTOR_NAME,
            distributor_part_number=product.ti_part_number or query,
            manufacturer_part_number=product.ti_part_number or product.generic_part_number or query,
            unit_price=selected_plan.unit_price,
            extended_price=selected_plan.extended_price,
            currency=selected_plan.currency,
            availability=_availability_text(product),
            price_break_quantity=selected_plan.price_break_quantity,
            required_quantity=agg.total_quantity,
            purchased_quantity=selected_plan.purchased_quantity,
            surplus_quantity=selected_plan.surplus_quantity,
            package_type=product.package_type,
            packaging_mode=product.package_carrier,
            packaging_source="ti_store_inventory_pricing_api",
            minimum_order_quantity=product.minimum_order_quantity,
            order_multiple=None,
            full_reel_quantity=full_reel_quantity,
            pricing_strategy=selected_plan.pricing_strategy or "TI store price break",
            order_plan=selected_plan.order_plan,
            purchase_legs=[leg.model_copy(deep=True) for leg in selected_plan.purchase_legs],
            match_method=MatchMethod.EXACT,
        )

    return DistributorOffer(
        distributor=TI_DISTRIBUTOR_NAME,
        distributor_part_number=last_product.ti_part_number if last_product is not None else None,
        manufacturer_part_number=(
            last_product.ti_part_number if last_product is not None else None
        ),
        currency=(
            _select_pricing_schedule(last_product, client.price_currency).currency
            if last_product is not None
            and _select_pricing_schedule(last_product, client.price_currency) is not None
            else client.price_currency
        ),
        availability=_availability_text(last_product) if last_product is not None else None,
        required_quantity=agg.total_quantity,
        package_type=last_product.package_type if last_product is not None else None,
        packaging_mode=last_product.package_carrier if last_product is not None else None,
        packaging_source=(
            "ti_store_inventory_pricing_api" if last_product is not None else None
        ),
        minimum_order_quantity=(
            last_product.minimum_order_quantity if last_product is not None else None
        ),
        order_multiple=None,
        full_reel_quantity=_full_reel_quantity(last_product) if last_product is not None else None,
        lookup_error=last_error,
    )


def _ti_purchase_families(
    product: TIProduct,
    schedule: TIPricingSchedule,
) -> tuple[PurchaseFamily, ...]:
    """Return normalized purchase families derived from one TI store orderable."""
    if product.order_limit is not None and (
        product.minimum_order_quantity or 1
    ) > product.order_limit:
        return ()

    family = PurchaseFamily(
        family_id=product.ti_part_number or product.generic_part_number or "ti_store",
        package_type=product.package_type,
        packaging_mode=product.package_carrier,
        minimum_order_quantity=product.minimum_order_quantity,
        order_multiple=None,
        full_reel_quantity=None,
        base_pricing_strategy="TI store price break",
        strategy_mode="static",
        allow_mixing_as_bulk=False,
        allow_mixing_as_remainder=False,
        price_breaks=tuple(
            FamilyPriceBreak(
                quantity=price_break.price_break_quantity,
                unit_price=price_break.price,
                currency=schedule.currency,
            )
            for price_break in schedule.price_breaks
            if product.order_limit is None or price_break.price_break_quantity <= product.order_limit
        ),
    )
    return (family,) if family.price_breaks else ()
