"""Tests for the Digi-Key Product Information V4 client."""

import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from digikey import (
    DigiKeyClient,
    best_pricing_option,
    price_part_via_digikey,
    resolve_digikey_locale,
)
from models import AggregatedPart, MatchMethod


@pytest.fixture(autouse=True)
def clear_env(monkeypatch):
    monkeypatch.delenv("DIGIKEY_LOCALE_SITE", raising=False)
    monkeypatch.delenv("DIGIKEY_LOCALE_LANGUAGE", raising=False)
    monkeypatch.delenv("DIGIKEY_LOCALE_CURRENCY", raising=False)
    monkeypatch.delenv("DIGIKEY_LOCALE_SHIP_TO_COUNTRY", raising=False)


class TestResolveDigiKeyLocale:
    def test_uses_eur_de_defaults(self):
        locale = resolve_digikey_locale()

        assert locale.site == "DE"
        assert locale.language == "en"
        assert locale.currency == "EUR"
        assert locale.ship_to_country == "de"

    def test_normalizes_explicit_overrides(self):
        locale = resolve_digikey_locale(
            site="fr",
            language="EN",
            currency="eur",
            ship_to_country="FR",
        )

        assert locale.site == "FR"
        assert locale.language == "en"
        assert locale.currency == "EUR"
        assert locale.ship_to_country == "fr"


class FakeDigiKeyTransport:
    """Small fake HTTP transport for Digi-Key client unit tests."""

    def __init__(self):
        self.post_calls: list[tuple[str, dict[str, str]]] = []
        self.get_calls: list[tuple[str, dict[str, str]]] = []
        self.fail_account_id = False

    def post(self, url, data):
        self.post_calls.append((url, dict(data)))
        request = httpx.Request("POST", url)
        return httpx.Response(
            200,
            json={
                "access_token": "token-123",
                "token_type": "Bearer",
                "expires_in": 600,
            },
            request=request,
        )

    def get(self, url, headers):
        headers_dict = dict(headers)
        self.get_calls.append((url, headers_dict))
        request = httpx.Request("GET", url, headers=headers)
        if self.fail_account_id and "X-DIGIKEY-Account-Id" in headers_dict:
            return httpx.Response(
                401,
                json={"error": "bad account header"},
                request=request,
            )
        return httpx.Response(
            200,
            json={
                "RequestedProduct": "P5555-ND",
                "RequestedQuantity": 100,
                "ManufacturerPartNumber": "ECA-1VHG102",
                "Manufacturer": {"Name": "Panasonic Electronic Components"},
                "SettingsUsed": {
                    "SearchLocaleUsed": {"Currency": "EUR"},
                    "CustomerIdUsed": 11492916,
                },
                "MyPricingOptions": [],
                "StandardPricingOptions": [
                    {
                        "PricingOption": "Exact",
                        "TotalQuantityPriced": 100,
                        "TotalPrice": 69.8,
                        "QuantityAvailable": 2097,
                        "Products": [
                            {
                                "DigiKeyProductNumber": "P5555-ND",
                                "QuantityPriced": 100,
                                "MinimumOrderQuantity": 1,
                                "ExtendedPrice": 69.8,
                                "UnitPrice": 0.698,
                                "PackageType": {"Name": "Bulk"},
                            }
                        ],
                    }
                ],
            },
            headers={"X-RateLimit-Remaining": "997"},
            request=request,
        )

    def close(self):
        pass


class TestDigiKeyClient:
    def test_reuses_cached_token(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        transport = FakeDigiKeyTransport()
        client._client = transport

        first = client.pricing_by_quantity("P5555-ND", 100)
        second = client.pricing_by_quantity("P5555-ND", 200)

        assert first.requested_product == "P5555-ND"
        assert second.requested_product == "P5555-ND"
        assert len(transport.post_calls) == 1

    def test_falls_back_from_account_id_to_customer_zero_on_401(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        transport = FakeDigiKeyTransport()
        transport.fail_account_id = True
        client._client = transport

        result = client.pricing_by_quantity("P5555-ND", 100)

        assert result.header_mode_used == "customer_zero"
        assert len(transport.get_calls) == 2
        assert "X-DIGIKEY-Account-Id" in transport.get_calls[0][1]
        assert transport.get_calls[1][1]["X-DIGIKEY-Customer-Id"] == "0"

    def test_sends_locale_headers(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
            locale=resolve_digikey_locale(
                site="FR",
                language="fr",
                currency="EUR",
                ship_to_country="fr",
            ),
        )
        transport = FakeDigiKeyTransport()
        client._client = transport

        client.pricing_by_quantity("P5555-ND", 100)
        headers = transport.get_calls[0][1]

        assert headers["X-DIGIKEY-Locale-Site"] == "FR"
        assert headers["X-DIGIKEY-Locale-Language"] == "fr"
        assert headers["X-DIGIKEY-Locale-Currency"] == "EUR"
        assert headers["X-DIGIKEY-Locale-ShipToCountry"] == "fr"

    def test_pricing_response_is_reused_from_persistent_cache(self, monkeypatch, tmp_path):
        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(tmp_path / "cache.sqlite3"))

        first_client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
            cache_enabled=True,
        )
        first_transport = FakeDigiKeyTransport()
        first_client._client = first_transport

        first_result = first_client.pricing_by_quantity("P5555-ND", 100)
        first_client.close()

        second_client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
            cache_enabled=True,
        )
        second_transport = FakeDigiKeyTransport()
        second_client._client = second_transport

        second_result = second_client.pricing_by_quantity("P5555-ND", 100)
        second_client.close()

        assert first_result.requested_product == "P5555-ND"
        assert second_result.requested_product == "P5555-ND"
        assert len(first_transport.post_calls) == 1
        assert len(first_transport.get_calls) == 1
        assert len(second_transport.post_calls) == 0
        assert len(second_transport.get_calls) == 0


class TestBestPricingOption:
    def test_selects_cheapest_total_price(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        transport = FakeDigiKeyTransport()
        client._client = transport

        result = client.pricing_by_quantity("P5555-ND", 100)
        option = best_pricing_option(result)

        assert option is not None
        assert option.pricing_option == "Exact"
        assert option.total_price == pytest.approx(69.8)
        assert option.effective_unit_price == pytest.approx(0.698)


class TestPricePartViaDigiKey:
    def test_normalizes_successful_offer(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        transport = FakeDigiKeyTransport()
        client._client = transport
        agg = AggregatedPart(
            part_number="ECA-1VHG102",
            manufacturer="Panasonic Electronic Components",
            quantity_per_unit=1,
            total_quantity=100,
        )

        offer = price_part_via_digikey(agg, client)

        assert offer.distributor == "Digi-Key"
        assert offer.distributor_part_number == "P5555-ND"
        assert offer.manufacturer_part_number == "ECA-1VHG102"
        assert offer.extended_price == pytest.approx(69.8)
        assert offer.unit_price == pytest.approx(0.698)
        assert offer.required_quantity == 100
        assert offer.purchased_quantity == 100
        assert offer.surplus_quantity == 0
        assert offer.package_type == "Bulk"
        assert offer.packaging_mode == "Bulk"
        assert offer.packaging_source == "digikey_api"
        assert offer.minimum_order_quantity == 1
        assert offer.order_multiple == 1
        assert offer.full_reel_quantity is None
        assert offer.pricing_strategy == "Exact"
        assert offer.order_plan == "100 bulk"
        assert len(offer.purchase_legs) == 1

    def test_prefers_cheaper_overbuy_option(self):
        class OverbuyTransport(FakeDigiKeyTransport):
            def get(self, url, headers):
                headers_dict = dict(headers)
                self.get_calls.append((url, headers_dict))
                request = httpx.Request("GET", url, headers=headers)
                return httpx.Response(
                    200,
                    json={
                        "RequestedProduct": "P5555-ND",
                        "RequestedQuantity": 950,
                        "ManufacturerPartNumber": "ECA-1VHG102",
                        "Manufacturer": {"Name": "Panasonic Electronic Components"},
                        "SettingsUsed": {
                            "SearchLocaleUsed": {"Currency": "EUR"},
                            "CustomerIdUsed": 11492916,
                        },
                        "MyPricingOptions": [],
                        "StandardPricingOptions": [
                            {
                                "PricingOption": "Exact",
                                "TotalQuantityPriced": 950,
                                "TotalPrice": 95.0,
                                "QuantityAvailable": 5000,
                                "Products": [
                                    {
                                        "DigiKeyProductNumber": "P5555CT-ND",
                                        "QuantityPriced": 950,
                                        "MinimumOrderQuantity": 1,
                                        "ExtendedPrice": 95.0,
                                        "UnitPrice": 0.10,
                                        "PackageType": {"Name": "Cut Tape"},
                                    }
                                ],
                            },
                            {
                                "PricingOption": "Better Value",
                                "TotalQuantityPriced": 1000,
                                "TotalPrice": 90.0,
                                "QuantityAvailable": 5000,
                                "Products": [
                                    {
                                        "DigiKeyProductNumber": "P5555TR-ND",
                                        "QuantityPriced": 1000,
                                        "MinimumOrderQuantity": 1000,
                                        "ExtendedPrice": 90.0,
                                        "UnitPrice": 0.09,
                                        "PackageType": {"Name": "Tape & Reel"},
                                    }
                                ],
                            },
                        ],
                    },
                    headers={"X-RateLimit-Remaining": "997"},
                    request=request,
                )

        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        client._client = OverbuyTransport()
        agg = AggregatedPart(
            part_number="ECA-1VHG102",
            manufacturer="Panasonic Electronic Components",
            quantity_per_unit=1,
            total_quantity=950,
        )

        offer = price_part_via_digikey(agg, client)

        assert offer.distributor_part_number == "P5555TR-ND"
        assert offer.extended_price == pytest.approx(90.0)
        assert offer.unit_price == pytest.approx(0.09)
        assert offer.purchased_quantity == 1000
        assert offer.surplus_quantity == 50
        assert offer.package_type == "Tape & Reel"
        assert offer.packaging_mode == "Tape & Reel"
        assert offer.packaging_source == "digikey_api"
        assert offer.minimum_order_quantity == 1000
        assert offer.order_multiple == 1000
        assert offer.full_reel_quantity == 1000
        assert offer.pricing_strategy == "Better Value"
        assert offer.order_plan == "1 reel x 1000"
        assert len(offer.purchase_legs) == 1
        assert offer.currency == "EUR"
        assert offer.match_method == MatchMethod.EXACT

    def test_rejects_manufacturer_mismatch(self):
        client = DigiKeyClient(
            client_id="client-id",
            client_secret="client-secret",
            account_id="12345678",
        )
        transport = FakeDigiKeyTransport()
        client._client = transport
        agg = AggregatedPart(
            part_number="ECA-1VHG102",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        offer = price_part_via_digikey(agg, client)

        assert offer.extended_price is None
        assert offer.lookup_error is not None
        assert "Manufacturer mismatch" in offer.lookup_error
