"""Tests for the TI Store Inventory and Pricing API client."""

import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from models import AggregatedPart, MatchMethod
from ti import (
    TIClient,
    price_part_via_ti,
    resolve_ti_price_currency,
    ti_is_configured,
    ti_supports_manufacturer,
)


@pytest.fixture(autouse=True)
def clear_env(monkeypatch):
    monkeypatch.delenv("TI_STORE_API_KEY", raising=False)
    monkeypatch.delenv("TI_STORE_API_SECRET", raising=False)
    monkeypatch.delenv("TI_STORE_PRICE_CURRENCY", raising=False)
    monkeypatch.delenv("TI_PRODUCT_API_KEY", raising=False)
    monkeypatch.delenv("TI_PRODUCT_API_SECRET", raising=False)
    monkeypatch.delenv("TI_PRODUCT_PRICE_CURRENCY", raising=False)


class FakeTITransport:
    """Small fake HTTP transport for TI store client unit tests."""

    def __init__(self):
        self.post_calls: list[tuple[str, str]] = []
        self.get_calls: list[tuple[str, dict[str, str]]] = []
        self.product_payloads: dict[str, httpx.Response] = {}
        self.fail_oauth = False

    def post(self, url, headers, data):
        self.post_calls.append((url, data))
        request = httpx.Request("POST", url, headers=headers)
        if self.fail_oauth:
            return httpx.Response(
                403,
                json={"message": "forbidden"},
                request=request,
            )
        return httpx.Response(
            200,
            json={"access_token": "token-123", "expires_in": 600},
            request=request,
        )

    def get(self, url, headers):
        self.get_calls.append((url, dict(headers)))
        request = httpx.Request("GET", url, headers=headers)
        product_number = url.split("/products/", 1)[-1].split("?", 1)[0]
        response = self.product_payloads.get(product_number)
        if response is None:
            return httpx.Response(
                404,
                json={"message": "not found"},
                request=request,
            )
        return httpx.Response(
            response.status_code,
            json=response.json(),
            request=request,
        )

    def close(self):
        pass


def _product_response(payload: dict) -> httpx.Response:
    request = httpx.Request("GET", "https://transact.ti.com/v2/store/products/example")
    return httpx.Response(200, json=payload, request=request)


class TestTIHelpers:
    def test_ti_configuration_detects_store_env_credentials(self, monkeypatch):
        monkeypatch.setenv("TI_STORE_API_KEY", "client-id")
        monkeypatch.setenv("TI_STORE_API_SECRET", "client-secret")

        assert ti_is_configured()

    def test_ti_configuration_falls_back_to_legacy_env_credentials(self, monkeypatch):
        monkeypatch.setenv("TI_PRODUCT_API_KEY", "legacy-id")
        monkeypatch.setenv("TI_PRODUCT_API_SECRET", "legacy-secret")

        assert ti_is_configured()

    def test_currency_prefers_explicit_hint_then_store_override(self, monkeypatch):
        assert resolve_ti_price_currency() == "USD"

        monkeypatch.setenv("TI_STORE_PRICE_CURRENCY", "eur")
        assert resolve_ti_price_currency() == "EUR"

        monkeypatch.setenv("TI_PRODUCT_PRICE_CURRENCY", "gbp")
        assert resolve_ti_price_currency() == "EUR"
        assert resolve_ti_price_currency("sek") == "SEK"

    def test_supports_only_ti_manufacturers(self):
        assert ti_supports_manufacturer("TI")
        assert ti_supports_manufacturer("Texas Instruments")
        assert not ti_supports_manufacturer("Analog Devices")


class TestTIClient:
    def test_reuses_cached_token(self):
        client = TIClient(client_id="client-id", client_secret="client-secret")
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TMP421AQDCNRQ1"] = _product_response(
            {
                "tiPartNumber": "TMP421AQDCNRQ1",
                "genericPartNumber": "TMP421-Q1",
                "pricing": [
                    {
                        "currency": "USD",
                        "priceBreaks": [{"priceBreakQuantity": 1, "price": 1.25}],
                    }
                ],
            }
        )
        client._client = transport

        client.product("TMP421AQDCNRQ1")
        client.product("TMP421AQDCNRQ1")

        assert len(transport.post_calls) == 1

    def test_requests_price_currency(self):
        client = TIClient(
            client_id="client-id",
            client_secret="client-secret",
            price_currency="EUR",
        )
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TMP421AQDCNRQ1"] = _product_response(
            {
                "tiPartNumber": "TMP421AQDCNRQ1",
                "pricing": [
                    {
                        "currency": "EUR",
                        "priceBreaks": [{"priceBreakQuantity": 1, "price": 1.15}],
                    }
                ],
            }
        )
        client._client = transport

        client.product("TMP421AQDCNRQ1")

        assert transport.get_calls
        assert "currency=EUR" in transport.get_calls[0][0]


class TestPricePartViaTI:
    def test_normalizes_successful_store_offer_with_overbuy(self):
        client = TIClient(
            client_id="client-id",
            client_secret="client-secret",
            price_currency="EUR",
        )
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TPS61160DRVR"] = _product_response(
            {
                "tiPartNumber": "TPS61160DRVR",
                "genericPartNumber": "TPS61160",
                "buyNowURL": "https://www.ti.com/product/TPS61160",
                "quantity": 5000,
                "limit": 10000,
                "description": "White LED driver",
                "minimumOrderQuantity": 1,
                "standardPackQuantity": 3000,
                "packageType": "Large reel",
                "packageCarrier": "LARGE T&R",
                "customReel": True,
                "lifeCycle": "ACTIVE",
                "pricing": [
                    {
                        "currency": "EUR",
                        "priceBreaks": [
                            {"priceBreakQuantity": 1, "price": 0.10},
                            {"priceBreakQuantity": 1000, "price": 0.09},
                        ],
                    }
                ],
            }
        )
        client._client = transport
        agg = AggregatedPart(
            part_number="TPS61160DRVR",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=950,
        )

        offer = price_part_via_ti(agg, client)

        assert offer.distributor == "TI"
        assert offer.distributor_part_number == "TPS61160DRVR"
        assert offer.manufacturer_part_number == "TPS61160DRVR"
        assert offer.currency == "EUR"
        assert offer.unit_price == pytest.approx(0.09)
        assert offer.extended_price == pytest.approx(90.0)
        assert offer.price_break_quantity == 1000
        assert offer.required_quantity == 950
        assert offer.purchased_quantity == 1000
        assert offer.surplus_quantity == 50
        assert offer.packaging_mode == "LARGE T&R"
        assert offer.package_type == "Large reel"
        assert offer.packaging_source == "ti_store_inventory_pricing_api"
        assert offer.minimum_order_quantity == 1
        assert offer.order_multiple is None
        assert offer.full_reel_quantity == 3000
        assert offer.pricing_strategy == "TI store price break"
        assert len(offer.purchase_legs) == 1
        assert (offer.order_plan or "").startswith("1000 ")
        assert offer.match_method == MatchMethod.EXACT
        assert "5,000 in stock" in (offer.availability or "")

    def test_respects_minimum_order_quantity(self):
        client = TIClient(client_id="client-id", client_secret="client-secret")
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TMP421AQDCNRQ1"] = _product_response(
            {
                "tiPartNumber": "TMP421AQDCNRQ1",
                "genericPartNumber": "TMP421-Q1",
                "minimumOrderQuantity": 25,
                "pricing": [
                    {
                        "currency": "USD",
                        "priceBreaks": [
                            {"priceBreakQuantity": 1, "price": 1.10},
                            {"priceBreakQuantity": 25, "price": 1.00},
                        ],
                    }
                ],
            }
        )
        client._client = transport
        agg = AggregatedPart(
            part_number="TMP421AQDCNRQ1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=10,
        )

        offer = price_part_via_ti(agg, client)

        assert offer.extended_price == pytest.approx(25.0)
        assert offer.purchased_quantity == 25
        assert offer.surplus_quantity == 15
        assert offer.price_break_quantity == 25
        assert len(offer.purchase_legs) == 1
        assert offer.order_plan == "1 batch x 25"

    def test_rejects_order_limit_below_required_quantity(self):
        client = TIClient(client_id="client-id", client_secret="client-secret")
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TMP421AQDCNRQ1"] = _product_response(
            {
                "tiPartNumber": "TMP421AQDCNRQ1",
                "genericPartNumber": "TMP421-Q1",
                "limit": 50,
                "pricing": [
                    {
                        "currency": "USD",
                        "priceBreaks": [{"priceBreakQuantity": 1, "price": 1.10}],
                    }
                ],
            }
        )
        client._client = transport
        agg = AggregatedPart(
            part_number="TMP421AQDCNRQ1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
        )

        offer = price_part_via_ti(agg, client)

        assert offer.extended_price is None
        assert offer.lookup_error is not None
        assert "order limit 50" in offer.lookup_error

    def test_reports_oauth_failures_clearly(self):
        client = TIClient(client_id="client-id", client_secret="client-secret")
        client.use_curl = False
        transport = FakeTITransport()
        transport.fail_oauth = True
        client._client = transport
        agg = AggregatedPart(
            part_number="TMP421AQDCNRQ1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
        )

        offer = price_part_via_ti(agg, client)

        assert offer.extended_price is None
        assert offer.lookup_error is not None
        assert "Store API suite" in offer.lookup_error

    def test_falls_back_to_second_query_term(self):
        client = TIClient(client_id="client-id", client_secret="client-secret")
        client.use_curl = False
        transport = FakeTITransport()
        transport.product_payloads["TPS61160DRVR"] = _product_response(
            {
                "tiPartNumber": "TPS61160DRVR",
                "genericPartNumber": "TPS61160",
                "pricing": [
                    {
                        "currency": "USD",
                        "priceBreaks": [{"priceBreakQuantity": 1, "price": 0.11}],
                    }
                ],
            }
        )
        client._client = transport
        agg = AggregatedPart(
            part_number="TPS61160",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=10,
        )

        offer = price_part_via_ti(agg, client, query_terms=["TPS61160X", "TPS61160DRVR"])

        assert offer.extended_price == pytest.approx(1.1)
        assert offer.distributor_part_number == "TPS61160DRVR"
