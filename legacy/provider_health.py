"""Provider capability discovery and safe live health checks.

Health checks deliberately return normalized metadata rather than raw
responses. This keeps credentials, request URLs containing query-string keys,
OAuth tokens, and provider-specific payload noise out of CLI output while
still giving automation enough information to diagnose configuration and
endpoint failures.
"""

from __future__ import annotations

import time
from typing import Any, Callable, Literal

from ai_resolver import DEFAULT_AI_MODEL, OpenAIResolver
from cli_models import ProviderHealthEnvelope, ProviderHealthResult
from digikey import DigiKeyClient, digikey_is_configured, resolve_digikey_locale
from fx import FXRateProvider
from models import AggregatedPart, MatchMethod
from mouser import LookupResult, MouserClient
from mouser_scoring import ScoredCandidate
from nxp import NXPClient, nxp_is_available
from secret_store import get_secret, get_secret_values
from ti import TIClient, ti_is_configured

HEALTH_PROVIDER_NAMES = ("mouser", "digikey", "ti", "nxp", "ecb", "openai")
DISTRIBUTOR_NAMES = ("mouser", "digikey", "ti", "nxp")


def provider_configuration() -> dict[str, ProviderHealthResult]:
    """Return safe, non-secret configuration status for every integration."""
    locale = resolve_digikey_locale()
    mouser_key_count = len(get_secret_values("mouser_api_keys"))
    if not mouser_key_count and get_secret("mouser_api_key"):
        mouser_key_count = 1

    values = {
        "mouser": ProviderHealthResult(
            name="mouser",
            kind="distributor",
            configured=mouser_key_count > 0,
            status="configured" if mouser_key_count > 0 else "not_configured",
            live=False,
            details={"credential_count": mouser_key_count},
        ),
        "digikey": ProviderHealthResult(
            name="digikey",
            kind="distributor",
            configured=digikey_is_configured(),
            status="configured" if digikey_is_configured() else "not_configured",
            live=False,
            details={
                "account_id_configured": bool(get_secret("digikey_account_id")),
                "locale": {
                    "site": locale.site,
                    "language": locale.language,
                    "currency": locale.currency,
                    "ship_to_country": locale.ship_to_country,
                },
            },
        ),
        "ti": ProviderHealthResult(
            name="ti",
            kind="distributor",
            configured=ti_is_configured(),
            status="configured" if ti_is_configured() else "not_configured",
            live=False,
            details={},
        ),
        "nxp": ProviderHealthResult(
            name="nxp",
            kind="distributor",
            configured=nxp_is_available(),
            status="configured" if nxp_is_available() else "not_configured",
            live=False,
            details={"authentication_required": False},
        ),
        "ecb": ProviderHealthResult(
            name="ecb",
            kind="service",
            configured=True,
            status="configured",
            live=False,
            details={"authentication_required": False},
        ),
        "openai": ProviderHealthResult(
            name="openai",
            kind="service",
            configured=bool(get_secret("openai_api_key")),
            status="configured" if get_secret("openai_api_key") else "not_configured",
            live=False,
            details={"model": DEFAULT_AI_MODEL},
        ),
    }
    return values


def configured_distributors() -> list[str]:
    """Return distributor names currently usable by automatic provider selection."""
    configuration = provider_configuration()
    return [
        name
        for name in DISTRIBUTOR_NAMES
        if configuration[name].configured
    ]


def check_providers(
    names: list[str] | tuple[str, ...],
    *,
    live: bool,
) -> ProviderHealthEnvelope:
    """Check selected provider configuration and optionally make live requests.

    Parameters
    ----------
    names:
        Provider names from :data:`HEALTH_PROVIDER_NAMES`.
    live:
        When true, execute one bounded representative request per configured
        provider. When false, inspect configuration only.

    Returns
    -------
    ProviderHealthEnvelope
        Stable health document with one normalized provider result per name.
    """
    configuration = provider_configuration()
    results: list[ProviderHealthResult] = []
    for name in names:
        configured_result = configuration[name]
        if not live or not configured_result.configured:
            results.append(configured_result)
            continue
        results.append(_run_live_check(name, configured_result))

    failed = [result for result in results if result.status == "failed"]
    unavailable = [result for result in results if result.status == "not_configured"]
    if failed and len(failed) == len(results):
        status: Literal["ok", "degraded", "failed"] = "failed"
    elif failed or unavailable:
        status = "degraded"
    else:
        status = "ok"
    return ProviderHealthEnvelope(
        status=status,
        exit_code=4 if failed else 0,
        live=live,
        providers=results,
    )


def _run_live_check(
    name: str,
    configured_result: ProviderHealthResult,
) -> ProviderHealthResult:
    """Execute one registry-backed live check and normalize failures."""
    registry = _live_check_registry()
    check = registry[name]
    started = time.perf_counter()
    try:
        request_count, details = check()
    except Exception as exc:
        status_code = getattr(getattr(exc, "response", None), "status_code", None)
        error_code = (
            f"PROVIDER_HTTP_{status_code}"
            if status_code is not None
            else f"PROVIDER_{type(exc).__name__.upper()}"
        )
        suffix = f" (HTTP {status_code})" if status_code is not None else ""
        return configured_result.model_copy(
            update={
                "status": "failed",
                "live": True,
                "latency_ms": round((time.perf_counter() - started) * 1000, 2),
                "error_code": error_code,
                "error": f"{type(exc).__name__}{suffix}",
            }
        )

    merged_details = dict(configured_result.details)
    merged_details.update(details)
    return configured_result.model_copy(
        update={
            "status": "ok",
            "live": True,
            "latency_ms": round((time.perf_counter() - started) * 1000, 2),
            "request_count": request_count,
            "details": merged_details,
        }
    )


def _live_check_registry() -> dict[str, Callable[[], tuple[int, dict[str, Any]]]]:
    """Return the live-check callable registry."""
    return {
        "mouser": _check_mouser,
        "digikey": _check_digikey,
        "ti": _check_ti,
        "nxp": _check_nxp,
        "ecb": _check_ecb,
        "openai": _check_openai,
    }


def _check_mouser() -> tuple[int, dict[str, Any]]:
    """Verify Mouser authentication and exact part-number search."""
    with MouserClient(cache_enabled=False, max_attempts=1) as client:
        parts = client.search("RC0402FR-0710KL", "Exact")
        if not parts:
            raise RuntimeError("Mouser returned no representative search result")
        return client.network_requests, {
            "result_count": len(parts),
            "matched_part_number": parts[0].get("ManufacturerPartNumber"),
        }


def _check_digikey() -> tuple[int, dict[str, Any]]:
    """Verify Digi-Key OAuth and account-aware quantity pricing."""
    with DigiKeyClient(cache_enabled=False) as client:
        result = client.pricing_by_quantity("P5555-ND", 100)
        return client.network_requests, {
            "currency": result.currency,
            "header_mode": result.header_mode_used,
            "rate_limit_remaining": result.rate_limit_remaining,
            "pricing_options": (
                len(result.my_pricing_options)
                + len(result.standard_pricing_options)
            ),
        }


def _check_ti() -> tuple[int, dict[str, Any]]:
    """Verify TI OAuth and store product pricing."""
    with TIClient(cache_enabled=False) as client:
        product = client.product("TMP421AQDCNRQ1")
        return client.network_requests, {
            "matched_part_number": product.ti_part_number,
            "quantity_available": product.quantity_available,
            "pricing_schedules": len(product.pricing),
            "currency": client.price_currency,
        }


def _check_nxp() -> tuple[int, dict[str, Any]]:
    """Verify NXP browser-backed public store search."""
    with NXPClient(cache_enabled=False) as client:
        result = client.search_result("KW47B42ZB7AFTB")
        if result is None:
            raise RuntimeError("NXP returned no representative search result")
        return client.network_requests, {
            "matched_part_number": result.part_id,
            "currency": result.currency,
            "price_breaks": len(result.step_prices),
            "store_lookup_enabled": client.store_lookup_enabled,
        }


def _check_ecb() -> tuple[int, dict[str, Any]]:
    """Verify the ECB reference-rate feed with one USD-to-EUR quote."""
    with FXRateProvider() as provider:
        quote = provider.quote("USD", "EUR")
        return provider.network_requests, {
            "pair": f"{quote.from_currency}/{quote.to_currency}",
            "rate": quote.rate,
            "as_of_date": quote.as_of_date,
            "source": quote.source,
        }


def _check_openai() -> tuple[int, dict[str, Any]]:
    """Verify the configured OpenAI reranker using its production request path."""
    aggregate = AggregatedPart(
        part_number="TMP421AQDCNRQ1",
        manufacturer="Texas Instruments",
        quantity_per_unit=1,
        total_quantity=100,
        description="Automotive remote and local temperature sensor",
    )
    candidate = ScoredCandidate(
        part={
            "ManufacturerPartNumber": "TMP421AQDCNRQ1",
            "MouserPartNumber": "595-TMP421AQDCNRQ1",
            "Manufacturer": "Texas Instruments",
            "Description": "Automotive remote and local temperature sensor",
        },
        score=100,
    )
    lookup = LookupResult(
        part=candidate.part,
        method=MatchMethod.FUZZY,
        candidate_count=1,
        review_required=True,
        candidates=(candidate,),
    )
    with OpenAIResolver() as resolver:
        decision = resolver.rerank(aggregate, lookup)
        if decision is None or decision.is_degraded:
            reason = decision.degradation_reason if decision is not None else "no_decision"
            raise RuntimeError(f"OpenAI resolver degraded: {reason}")
        return 1, {
            "model": resolver.model,
            "decision": decision.decision,
            "confidence": decision.confidence,
        }
