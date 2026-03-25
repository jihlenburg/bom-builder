"""Tests for FX conversion helpers."""

import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from fx import FXRateProvider, convert_offer_currency, resolve_target_currency
from models import DistributorOffer


@pytest.fixture(autouse=True)
def clear_env(monkeypatch):
    monkeypatch.delenv("BOM_BUILDER_TARGET_CURRENCY", raising=False)
    monkeypatch.delenv("BOM_BUILDER_FX_OVERRIDES", raising=False)
    monkeypatch.delenv("DIGIKEY_LOCALE_CURRENCY", raising=False)


class FakeFXTransport:
    """Small fake HTTP transport for ECB FX feed tests."""

    def __init__(self, xml: str):
        self.xml = xml
        self.calls = 0

    def get(self, url):
        self.calls += 1
        request = httpx.Request("GET", url)
        return httpx.Response(200, text=self.xml, request=request)

    def close(self):
        pass


class TestResolveTargetCurrency:
    def test_prefers_explicit_then_env_then_digikey_locale(self, monkeypatch):
        assert resolve_target_currency() == "EUR"

        monkeypatch.setenv("DIGIKEY_LOCALE_CURRENCY", "usd")
        assert resolve_target_currency() == "USD"

        monkeypatch.setenv("BOM_BUILDER_TARGET_CURRENCY", "chf")
        assert resolve_target_currency() == "CHF"
        assert resolve_target_currency("sek") == "SEK"


class TestFXRateProvider:
    def test_uses_override_directly(self):
        provider = FXRateProvider(overrides={("USD", "EUR"): 0.9})

        quote = provider.quote("USD", "EUR")

        assert quote.rate == pytest.approx(0.9)
        assert quote.source == "env_override"

    def test_uses_ecb_cross_rate(self):
        xml = """
        <gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01"
                         xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
          <Cube>
            <Cube time="2026-03-24">
              <Cube currency="USD" rate="1.1000"/>
              <Cube currency="GBP" rate="0.8500"/>
            </Cube>
          </Cube>
        </gesmes:Envelope>
        """
        provider = FXRateProvider(http_client=FakeFXTransport(xml))

        usd_to_eur = provider.quote("USD", "EUR")
        usd_to_gbp = provider.quote("USD", "GBP")

        assert usd_to_eur.rate == pytest.approx(1 / 1.1)
        assert usd_to_gbp.rate == pytest.approx(0.85 / 1.1)
        assert usd_to_eur.as_of_date == "2026-03-24"


class TestConvertOfferCurrency:
    def test_converts_priced_offer(self):
        offer = DistributorOffer(
            distributor="TI",
            distributor_part_number="TMP421AQDCNRQ1",
            unit_price=1.0,
            extended_price=100.0,
            currency="USD",
        )
        provider = FXRateProvider(overrides={("USD", "EUR"): 0.91})

        converted = convert_offer_currency(offer, "EUR", provider)

        assert converted.currency == "EUR"
        assert converted.unit_price == pytest.approx(0.91)
        assert converted.extended_price == pytest.approx(91.0)

    def test_leaves_same_currency_offer_unchanged(self):
        offer = DistributorOffer(
            distributor="Mouser",
            distributor_part_number="595-PART",
            unit_price=1.0,
            extended_price=10.0,
            currency="EUR",
        )
        provider = FXRateProvider(overrides={("USD", "EUR"): 0.91})

        converted = convert_offer_currency(offer, "EUR", provider)

        assert converted == offer
