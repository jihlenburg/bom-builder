"""Tests for the experimental NXP direct-store adapter."""

from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).parent.parent))

import pytest

from models import AggregatedPart
from nxp import (
    NXPPartDetail,
    NXPSearchResult,
    NXPSchemaChangedError,
    _part_detail_from_text,
    _select_best_result,
    nxp_supports_manufacturer,
    price_part_via_nxp,
)


def _store_result_payload(part_id: str, **meta_overrides):
    meta = {
        "part_id": part_id,
        "Description": "Test part",
        "Order": ["Buy Direct", "Buy Through Distributor"],
        "Availability": "In Stock",
        "packing_desc": "TRAY-Tray, Bakeable, Multiple in Drypack",
        "packing_name": "TRAY",
        "stock_quantity": 4310,
        "stepPrice": ["1::130::6.60", "26::120::6.10", "100::110::5.59"],
        "unitPrice": 6.60,
        "suggestRsllPrice": 5.08,
    }
    meta.update(meta_overrides)
    return {
        "summary": f"part_id::<b>{part_id}</b>|~~~|description_s::Test part",
        "metaData": meta,
        "url": f"https://www.nxp.com/webapp/salesItem.jsp?partId={part_id}",
    }


class FakeNXPClient:
    def __init__(
        self,
        result: NXPSearchResult | None,
        detail: NXPPartDetail | None,
        *,
        store_lookup_enabled: bool = True,
        detail_enrichment_enabled: bool = True,
    ):
        self._result = result
        self._detail = detail
        self.store_lookup_enabled = store_lookup_enabled
        self.detail_enrichment_enabled = detail_enrichment_enabled

    def search_result(self, _query: str):
        return self._result

    def part_detail(self, _query: str, _matched_part_id: str):
        return self._detail


def test_supports_nxp_and_freescale_manufacturers():
    assert nxp_supports_manufacturer("NXP")
    assert nxp_supports_manufacturer("Freescale")
    assert not nxp_supports_manufacturer("Texas Instruments")


def test_select_best_result_prefers_buy_direct_prefix_variant():
    payload = {
        "results": [
            _store_result_payload(
                "KW47B42ZB7AFTBR",
                Order=["Buy Through Distributor"],
                stepPrice=[],
                unitPrice=None,
                stock_quantity=0,
            ),
            _store_result_payload("KW47B42ZB7AFTBT"),
        ]
    }

    result = _select_best_result("KW47B42ZB7AFTB", payload)

    assert result is not None
    assert result.part_id == "KW47B42ZB7AFTBT"
    assert result.buy_direct is True
    assert result.step_prices[-1] == (100, 5.59)


def test_select_best_result_raises_when_payload_contract_changes():
    with pytest.raises(NXPSchemaChangedError):
        _select_best_result(
            "KW47B42ZB7AFTB",
            {"results": [{"metaData": {"Order": ["Buy Direct"]}}]},
        )


def test_part_detail_parser_extracts_moq_and_mpq():
    body_text = """
    KW47B42ZB7AFTBT
    ACTIVE
    Packing: TRAY-Tray, Bakeable, Multiple in Drypack
    Min. Package Quantity: 260
    Min. Order Quantity: 1300
    Lead Time: 26 weeks
    """

    detail = _part_detail_from_text("KW47B42ZB7AFTB", "KW47B42ZB7AFTBT", body_text)

    assert detail is not None
    assert detail.minimum_package_quantity == 260
    assert detail.minimum_order_quantity == 1300


def test_part_detail_parser_prefers_exact_orderable_over_family_prefix():
    body_text = """
    KW47B42ZB7AFTB
    ACTIVE
    Buy Options
    KW47B42ZB7AFTBT
    ACTIVE
    Details
    Packing: TRAY-Tray, Bakeable, Multiple in Drypack
    Min. Package Quantity: 260
    Min. Order Quantity: 1300
    """

    detail = _part_detail_from_text("KW47B42ZB7AFTB", "KW47B42ZB7AFTBT", body_text)

    assert detail is not None
    assert detail.matched_part_id == "KW47B42ZB7AFTBT"
    assert detail.minimum_package_quantity == 260
    assert detail.minimum_order_quantity == 1300


def test_price_part_via_nxp_builds_priced_offer_from_store_result():
    agg = AggregatedPart(
        part_number="KW47B42ZB7AFTB",
        manufacturer="NXP",
        quantity_per_unit=1,
        total_quantity=1000,
    )
    result = NXPSearchResult(
        query="KW47B42ZB7AFTB",
        part_id="KW47B42ZB7AFTBT",
        description="KW47",
        buy_direct=True,
        order_actions=("Buy Direct", "Buy Through Distributor"),
        unit_price=6.60,
        suggested_resale_price=5.08,
        currency="USD",
        stock_quantity=4310,
        availability="In Stock",
        packing_name="TRAY",
        packing_description="TRAY-Tray, Bakeable, Multiple in Drypack",
        step_prices=((1, 6.60), (26, 6.10), (100, 5.59)),
        package_quality_url="https://www.nxp.com/products/KW47?fpsp=1&tab=Package_Quality_Tab",
        raw_url="https://www.nxp.com/webapp/salesItem.jsp?partId=KW47B42ZB7AFTBT",
    )
    detail = NXPPartDetail(
        query="KW47B42ZB7AFTB",
        matched_part_id="KW47B42ZB7AFTBT",
        minimum_order_quantity=1300,
        minimum_package_quantity=260,
    )

    offer = price_part_via_nxp(agg, FakeNXPClient(result, detail), query_terms=[agg.part_number])

    assert offer.distributor == "NXP"
    assert offer.distributor_part_number == "KW47B42ZB7AFTBT"
    assert offer.price_break_quantity == 100
    assert offer.unit_price == 5.59
    assert offer.purchased_quantity == 1300
    assert offer.surplus_quantity == 300
    assert offer.review_required is False


def test_price_part_via_nxp_marks_unconfirmed_moq_for_review():
    agg = AggregatedPart(
        part_number="KW47B42ZB7AFTB",
        manufacturer="NXP",
        quantity_per_unit=1,
        total_quantity=1000,
    )
    result = NXPSearchResult(
        query="KW47B42ZB7AFTB",
        part_id="KW47B42ZB7AFTBT",
        description="KW47",
        buy_direct=True,
        order_actions=("Buy Direct",),
        unit_price=6.60,
        suggested_resale_price=5.08,
        currency="USD",
        stock_quantity=4310,
        availability="In Stock",
        packing_name="TRAY",
        packing_description="TRAY-Tray, Bakeable, Multiple in Drypack",
        step_prices=((1, 6.60), (26, 6.10), (100, 5.59)),
        package_quality_url=None,
        raw_url=None,
    )

    offer = price_part_via_nxp(agg, FakeNXPClient(result, None), query_terms=[agg.part_number])

    assert offer.review_required is True
    assert offer.pricing_strategy == "NXP direct price break (MOQ not confirmed)"


def test_price_part_via_nxp_returns_unpriced_offer_when_store_is_disabled():
    agg = AggregatedPart(
        part_number="KW47B42ZB7AFTB",
        manufacturer="NXP",
        quantity_per_unit=1,
        total_quantity=1000,
    )

    offer = price_part_via_nxp(
        agg,
        FakeNXPClient(None, None, store_lookup_enabled=False),
        query_terms=[agg.part_number],
    )

    assert offer.extended_price is None
    assert offer.lookup_error == "NXP direct unavailable for this run"


def test_price_part_via_nxp_returns_unpriced_offer_when_buy_direct_is_unavailable():
    agg = AggregatedPart(
        part_number="NCJ3310AHN/0J",
        manufacturer="NXP",
        quantity_per_unit=1,
        total_quantity=1000,
    )
    result = NXPSearchResult(
        query="NCJ3310AHN/0J",
        part_id="NCJ3310AHN/0J",
        description="NCx3310",
        buy_direct=False,
        order_actions=("Buy Through Distributor",),
        unit_price=None,
        suggested_resale_price=None,
        currency=None,
        stock_quantity=None,
        availability=None,
        packing_name="REEL",
        packing_description='REEL-Reel 13" Q1/T1',
        step_prices=(),
        package_quality_url=None,
        raw_url=None,
    )

    offer = price_part_via_nxp(agg, FakeNXPClient(result, None), query_terms=[agg.part_number])

    assert offer.extended_price is None
    assert offer.lookup_error == "NXP lists this part, but direct buy is not available"
