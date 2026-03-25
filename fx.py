"""Foreign-exchange helpers for cross-distributor price comparison.

The BOM engine prefers to compare normalized offers in one target currency
instead of dropping back to "same-currency only" comparisons. By default this
module uses the ECB's daily euro foreign exchange reference rates and supports
manual overrides for deterministic testing or offline runs.
"""

from __future__ import annotations

from dataclasses import dataclass
import os
from typing import Iterable
from xml.etree import ElementTree

import httpx

from models import DistributorOffer

ECB_EURO_FXREF_DAILY_XML_URL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"


@dataclass(frozen=True)
class ExchangeRateQuote:
    """One resolved FX quote between two currencies."""

    from_currency: str
    to_currency: str
    rate: float
    as_of_date: str | None
    source: str


def resolve_target_currency(target_currency: str = "") -> str:
    """Return the run-wide comparison/reporting currency."""
    resolved = (
        target_currency.strip()
        or os.getenv("BOM_BUILDER_TARGET_CURRENCY", "").strip()
        or os.getenv("DIGIKEY_LOCALE_CURRENCY", "").strip()
        or "EUR"
    ).upper()
    return resolved or "EUR"


def _normalized_currency(currency: str) -> str:
    """Return an uppercase stripped currency code."""
    return currency.strip().upper()


def _parse_rate_overrides(raw: str) -> dict[tuple[str, str], float]:
    """Parse FX overrides like ``USD:EUR=0.92,GBP:EUR=1.17``."""
    overrides: dict[tuple[str, str], float] = {}
    for chunk in raw.split(","):
        token = chunk.strip()
        if not token or "=" not in token or ":" not in token:
            continue
        pair, value_text = token.split("=", 1)
        from_currency, to_currency = pair.split(":", 1)
        rate = float(value_text.strip())
        if rate <= 0:
            continue
        overrides[(
            _normalized_currency(from_currency),
            _normalized_currency(to_currency),
        )] = rate
    return overrides


class FXRateProvider:
    """FX rate lookup with optional manual overrides and ECB fallback."""

    def __init__(
        self,
        *,
        feed_url: str = ECB_EURO_FXREF_DAILY_XML_URL,
        http_client: httpx.Client | None = None,
        overrides: dict[tuple[str, str], float] | None = None,
    ):
        """Initialize the provider."""
        self.feed_url = feed_url
        self._client = http_client or httpx.Client(timeout=15.0)
        self._owns_client = http_client is None
        self._overrides = overrides or _parse_rate_overrides(
            os.getenv("BOM_BUILDER_FX_OVERRIDES", "")
        )
        self._rates_per_eur: dict[str, float] | None = None
        self._as_of_date: str | None = None
        self.network_requests = 0

    def close(self) -> None:
        """Release owned HTTP resources."""
        if self._owns_client:
            self._client.close()

    def __enter__(self) -> "FXRateProvider":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: object) -> None:
        """Release owned HTTP resources at the end of a ``with`` block."""
        self.close()

    def _load_ecb_rates(self) -> None:
        """Fetch and cache the ECB daily reference-rate feed."""
        if self._rates_per_eur is not None:
            return

        self.network_requests += 1
        response = self._client.get(self.feed_url)
        response.raise_for_status()
        root = ElementTree.fromstring(response.text)

        rates = {"EUR": 1.0}
        as_of_date = None
        for element in root.iter():
            currency = element.attrib.get("currency")
            rate = element.attrib.get("rate")
            if "time" in element.attrib:
                as_of_date = element.attrib.get("time")
            if currency and rate:
                rates[_normalized_currency(currency)] = float(rate)

        if len(rates) == 1:
            raise ValueError("ECB FX feed did not contain any currency rates")

        self._rates_per_eur = rates
        self._as_of_date = as_of_date

    def quote(self, from_currency: str, to_currency: str) -> ExchangeRateQuote:
        """Return a quote from one currency into another."""
        src = _normalized_currency(from_currency)
        dst = _normalized_currency(to_currency)
        if not src or not dst:
            raise ValueError("Both source and target currencies are required")
        if src == dst:
            return ExchangeRateQuote(
                from_currency=src,
                to_currency=dst,
                rate=1.0,
                as_of_date=None,
                source="identity",
            )

        direct = self._overrides.get((src, dst))
        if direct is not None:
            return ExchangeRateQuote(
                from_currency=src,
                to_currency=dst,
                rate=direct,
                as_of_date=None,
                source="env_override",
            )
        inverse = self._overrides.get((dst, src))
        if inverse is not None and inverse > 0:
            return ExchangeRateQuote(
                from_currency=src,
                to_currency=dst,
                rate=1.0 / inverse,
                as_of_date=None,
                source="env_override_inverse",
            )

        self._load_ecb_rates()
        assert self._rates_per_eur is not None

        if src not in self._rates_per_eur or dst not in self._rates_per_eur:
            raise ValueError(f"No ECB FX rate available for {src}->{dst}")

        if src == "EUR":
            rate = self._rates_per_eur[dst]
        elif dst == "EUR":
            rate = 1.0 / self._rates_per_eur[src]
        else:
            rate = self._rates_per_eur[dst] / self._rates_per_eur[src]

        return ExchangeRateQuote(
            from_currency=src,
            to_currency=dst,
            rate=rate,
            as_of_date=self._as_of_date,
            source="ecb_reference_rate",
        )


def convert_offer_currency(
    offer: DistributorOffer,
    target_currency: str,
    rate_provider: FXRateProvider,
) -> DistributorOffer:
    """Return a copy of one offer converted into the target currency."""
    if not offer.is_priced or not offer.currency:
        return offer

    src = _normalized_currency(offer.currency)
    dst = _normalized_currency(target_currency)
    if not src or not dst or src == dst:
        return offer

    quote = rate_provider.quote(src, dst)
    updates = {
        "currency": dst,
        "unit_price": (
            round(offer.unit_price * quote.rate, 6)
            if offer.unit_price is not None
            else None
        ),
        "extended_price": (
            round(offer.extended_price * quote.rate, 2)
            if offer.extended_price is not None
            else None
        ),
    }
    return offer.model_copy(update=updates)


def convert_offers_currency(
    offers: Iterable[DistributorOffer],
    target_currency: str,
    rate_provider: FXRateProvider,
) -> list[DistributorOffer]:
    """Return a list of offers normalized into the target currency when possible."""
    converted: list[DistributorOffer] = []
    for offer in offers:
        try:
            converted.append(convert_offer_currency(offer, target_currency, rate_provider))
        except Exception:
            converted.append(offer)
    return converted
