"""Tests for Mouser lookup logic (no API calls required)."""

import builtins
import sys
from pathlib import Path

import httpx
import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from mouser import (
    MouserClient,
    _build_lookup_passes,
    _packaging_details_from_candidate,
    _packaging_details_from_product_page_html,
    MouserPackagingDetails,
    best_purchase_plan,
    best_price_break,
    detect_input_qualifiers,
    is_non_component,
    is_packaging_variant,
    load_manufacturer_aliases,
    manufacturers_match,
    parse_price,
    price_part,
    score_candidate,
    smart_lookup,
    strip_qualifiers,
)
from manufacturer_packaging import ManufacturerPackagingDetails
from models import AggregatedPart, MatchMethod
from resolution_store import ResolutionStore


class TestParsePrice:
    """Test locale-aware price string parsing."""

    def test_eu_format_simple(self):
        assert parse_price("0,045 €") == 0.045

    def test_eu_format_thousands(self):
        assert parse_price("1.234,56 €") == 1234.56

    def test_us_format_simple(self):
        assert parse_price("$0.045") == 0.045

    def test_us_format_thousands(self):
        assert parse_price("$1,234.56") == 1234.56

    def test_plain_number(self):
        assert parse_price("0.045") == 0.045

    def test_garbage(self):
        assert parse_price("no price") is None

    def test_empty(self):
        assert parse_price("") is None

    def test_eu_no_thousands(self):
        assert parse_price("12,34 €") == 12.34


class TestStripQualifiers:
    def test_strip_q1(self):
        assert strip_qualifiers("TMP423-Q1") == "TMP423"

    def test_strip_nopb(self):
        assert strip_qualifiers("LM3670MFX-3.3/NOPB") == "LM3670MFX-3.3"

    def test_strip_tr(self):
        assert strip_qualifiers("RC0402FR-TR") == "RC0402FR"

    def test_strip_pbf(self):
        assert strip_qualifiers("LTC3890#PBF") == "LTC3890"

    def test_no_qualifier(self):
        assert strip_qualifiers("RC0402FR-0710KL") == "RC0402FR-0710KL"

    def test_trailing_dash_cleanup(self):
        assert strip_qualifiers("PART-EP") == "PART"


class TestDetectInputQualifiers:
    def test_automotive_q1(self):
        quals = detect_input_qualifiers("TMP423-Q1")
        assert "automotive" in quals
        assert quals["automotive"] == 40

    def test_lead_free_nopb(self):
        quals = detect_input_qualifiers("LM3670MFX/NOPB")
        assert "lead_free" in quals

    def test_no_qualifiers(self):
        quals = detect_input_qualifiers("RC0402FR-0710KL")
        assert quals == {}

    def test_tape_reel(self):
        quals = detect_input_qualifiers("PART-TR")
        assert "tape_reel" in quals


class TestManufacturersMatch:
    """Test manufacturer name matching with alias table."""

    @pytest.fixture
    def aliases(self):
        return load_manufacturer_aliases()

    def test_exact_match(self, aliases):
        assert manufacturers_match("Texas Instruments", "Texas Instruments", aliases)

    def test_substring_match(self, aliases):
        assert manufacturers_match("Vishay", "Vishay General Semiconductor", aliases)

    def test_alias_ti(self, aliases):
        assert manufacturers_match("TI", "Texas Instruments", aliases)

    def test_alias_onsemi(self, aliases):
        assert manufacturers_match("onsemi", "ON Semiconductor", aliases)

    def test_alias_infineon(self, aliases):
        assert manufacturers_match("Infineon", "Infineon Technologies", aliases)

    def test_alias_diodes(self, aliases):
        assert manufacturers_match("Diodes Inc.", "Diodes Incorporated", aliases)

    def test_no_match(self, aliases):
        assert not manufacturers_match("TI", "NXP Semiconductors", aliases)

    def test_short_name_not_substring_false_positive(self, aliases):
        """Short names like 'TI' must not match inside unrelated words like 'Quantic'."""
        assert not manufacturers_match("TI", "Quantic X-Microwave", aliases)

    def test_case_insensitive(self, aliases):
        assert manufacturers_match("ti", "TEXAS INSTRUMENTS", aliases)


class TestIsNonComponent:
    def test_evm_in_mpn(self):
        assert is_non_component("TPS4800Q1EVM", "", "")

    def test_eval_in_mpn(self):
        assert is_non_component("EVAL-PART", "", "")

    def test_eval_in_description(self):
        assert is_non_component("", "evaluation module for TPS4800", "")

    def test_dev_tools_category(self):
        assert is_non_component("", "", "Switch IC Development Tools")

    def test_normal_component(self):
        assert not is_non_component("TPS48000QDGXRQ1", "Gate Drivers", "Gate Drivers")

    def test_kit_in_mpn(self):
        assert is_non_component("PART-KIT-01", "", "")

    def test_none_fields_dont_crash(self):
        assert not is_non_component(None, None, None)

    def test_mixed_none_fields(self):
        assert is_non_component("TPS4800Q1EVM", None, None)


class TestScoreCandidate:
    def _candidate(self, mpn="PARTX", mfr="Texas Instruments", desc="", cat=""):
        return {
            "ManufacturerPartNumber": mpn,
            "Manufacturer": mfr,
            "Description": desc,
            "Category": cat,
            "Availability": "100 In Stock",
        }

    def test_wrong_manufacturer_returns_negative(self):
        c = self._candidate(mfr="NXP")
        assert score_candidate(c, "PART", "Texas Instruments") == -1

    def test_exact_pn_in_candidate_scores_higher(self):
        c1 = self._candidate(mpn="PARTXYZ")
        c2 = self._candidate(mpn="SOMETHING")
        s1 = score_candidate(c1, "PARTX", "Texas Instruments")
        s2 = score_candidate(c2, "PARTX", "Texas Instruments")
        assert s1 > s2

    def test_evm_filtered(self):
        c = self._candidate(mpn="PARTEVM", cat="Development Tools")
        assert score_candidate(c, "PART", "Texas Instruments") == -1

    def test_automotive_qualifier_boost(self):
        c_auto = self._candidate(mpn="PARTQDCRQ1", desc="AEC-Q100 Automotive")
        c_std = self._candidate(mpn="PARTDCR", desc="Standard part")
        s_auto = score_candidate(c_auto, "PART-Q1", "Texas Instruments")
        s_std = score_candidate(c_std, "PART-Q1", "Texas Instruments")
        assert s_auto > s_std

    def test_automotive_penalized_when_not_requested(self):
        c_auto = self._candidate(mpn="PARTQDCRQ1", desc="AEC-Q100 Automotive")
        c_std = self._candidate(mpn="PARTDCR", desc="Standard part")
        s_auto = score_candidate(c_auto, "PART", "Texas Instruments")
        s_std = score_candidate(c_std, "PART", "Texas Instruments")
        assert s_std > s_auto

    def test_ti_q1_hard_gate_disqualifies_non_q1(self):
        """TI non-Q1 candidates are disqualified when BOM specifies Q1."""
        c_std = self._candidate(mpn="LM2775DSGT", desc="Boost Regulator")
        assert score_candidate(c_std, "LM2775-Q1", "Texas Instruments") == -1
        assert score_candidate(c_std, "LM2775-Q1", "TI") == -1

    def test_ti_q1_hard_gate_keeps_q1_candidate(self):
        """TI Q1 candidates pass the hard gate when BOM specifies Q1."""
        c_q1 = self._candidate(mpn="LM2775QDSGRQ1", desc="AEC-Q100 Automotive")
        assert score_candidate(c_q1, "LM2775-Q1", "Texas Instruments") > 0

    def test_ti_q1_hard_gate_not_applied_to_other_manufacturers(self):
        """The Q1 hard gate is TI-specific — other manufacturers are unaffected."""
        c_std = self._candidate(mpn="PARTXYZ", mfr="STMicroelectronics", desc="Standard part")
        assert score_candidate(c_std, "PART-Q1", "STMicroelectronics") != -1


class TestBestPriceBreak:
    def test_picks_highest_applicable(self):
        breaks = [
            {"Quantity": "1", "Price": "1.00"},
            {"Quantity": "100", "Price": "0.80"},
            {"Quantity": "1000", "Price": "0.50"},
        ]
        best = best_price_break(breaks, 500)
        assert int(best["Quantity"]) == 100

    def test_exact_quantity_match(self):
        breaks = [
            {"Quantity": "1", "Price": "1.00"},
            {"Quantity": "1000", "Price": "0.50"},
        ]
        best = best_price_break(breaks, 1000)
        assert int(best["Quantity"]) == 1000

    def test_all_breaks_exceed_quantity(self):
        breaks = [
            {"Quantity": "100", "Price": "0.80"},
            {"Quantity": "1000", "Price": "0.50"},
        ]
        best = best_price_break(breaks, 10)
        assert int(best["Quantity"]) == 100  # Smallest break

    def test_empty_breaks(self):
        assert best_price_break([], 1000) is None


class TestBestPurchasePlan:
    def test_prefers_buying_up_to_the_next_break_when_total_spend_drops(self):
        breaks = [
            {"Quantity": "1", "Price": "0.10", "Currency": "EUR"},
            {"Quantity": "1000", "Price": "0.09", "Currency": "EUR"},
        ]

        plan = best_purchase_plan(breaks, 950)

        assert plan is not None
        assert plan.required_quantity == 950
        assert plan.purchased_quantity == 1000
        assert plan.surplus_quantity == 50
        assert plan.extended_price == pytest.approx(90.0)
        assert plan.pricing_strategy == "next price break"

    def test_prefers_mouser_full_reel_price_table_when_it_is_cheaper(self):
        breaks = [
            {"Quantity": "1", "Price": "1.16", "Currency": "EUR"},
            {"Quantity": "1000", "Price": "0.592", "Currency": "EUR"},
        ]
        details = MouserPackagingDetails(
            packaging_mode="Reel | Cut Tape | MouseReel",
            packaging_source="product_page",
            minimum_order_quantity=1,
            order_multiple=1,
            standard_pack_quantity=3000,
            full_reel_quantity=3000,
            full_reel_price_breaks=(
                {"Quantity": "3000", "Price": "0.565", "Currency": "EUR"},
            ),
        )

        plan = best_purchase_plan(breaks, 2950, packaging_details=details)

        assert plan is not None
        assert plan.purchased_quantity == 3000
        assert plan.extended_price == pytest.approx(1695.0)
        assert plan.pricing_strategy == "full reel"

    def test_prefers_mixed_reel_and_cut_tape_when_it_is_cheaper(self):
        breaks = [
            {"Quantity": "1", "Price": "1.00", "Currency": "EUR"},
            {"Quantity": "500", "Price": "0.60", "Currency": "EUR"},
            {"Quantity": "1000", "Price": "0.55", "Currency": "EUR"},
        ]
        details = MouserPackagingDetails(
            packaging_mode="Reel | Cut Tape | MouseReel",
            packaging_source="product_page",
            minimum_order_quantity=1,
            order_multiple=1,
            standard_pack_quantity=1800,
            full_reel_quantity=1800,
            full_reel_price_breaks=(
                {"Quantity": "1800", "Price": "0.40", "Currency": "EUR"},
            ),
        )

        plan = best_purchase_plan(breaks, 6000, packaging_details=details)

        assert plan is not None
        assert plan.purchased_quantity == 6000
        assert plan.surplus_quantity == 0
        assert plan.extended_price == pytest.approx(2520.0)
        assert plan.pricing_strategy == "mixed packaging"
        assert plan.order_plan == "3 reels x 1800 + 600 cut tape"
        assert len(plan.purchase_legs) == 2


class TestPackagingDetails:
    def test_extracts_packaging_constraints_from_search_payload(self):
        candidate = {
            "Packaging": "Reel, Cut Tape, MouseReel",
            "ReelingAvailability": "Full Reel (Order in multiples of 3000)",
            "MinimumOrderQuantity": "1",
            "OrderQuantityMultiples": "1",
            "StandardPackQuantity": "3000",
        }

        details = _packaging_details_from_candidate(candidate)

        assert details.packaging_mode == "Reel, Cut Tape, MouseReel | Full Reel (Order in multiples of 3000)"
        assert details.minimum_order_quantity == 1
        assert details.order_multiple == 1
        assert details.full_reel_quantity == 3000
        assert details.packaging_source == "search_api"

    def test_parses_full_reel_price_table_from_product_page_html(self):
        html = """
        <html><body>
        <div>Minimum: 1 Multiples: 1</div>
        <div>Packaging:</div>
        <div>Full Reel (Order in multiples of 3000)</div>
        <div>Cut Tape</div>
        <div>MouseReel</div>
        <div>Pricing (EUR)</div>
        <div>Qty. Unit Price Ext. Price</div>
        <div>Cut Tape / MouseReel</div>
        <div>1 1,16 € 1,16 €</div>
        <div>1000 0,592 € 592,00 €</div>
        <div>Full Reel (Order in multiples of 3000)</div>
        <div>3000 0,565 € 1.695,00 €</div>
        <div>6000 0,55 € 3.300,00 €</div>
        <div>Packaging Choice</div>
        </body></html>
        """

        details = _packaging_details_from_product_page_html(html)

        assert details.minimum_order_quantity == 1
        assert details.order_multiple == 1
        assert details.full_reel_quantity == 3000
        assert len(details.full_reel_price_breaks) == 2
        assert details.full_reel_price_breaks[0]["Quantity"] == "3000"
        assert details.full_reel_price_breaks[0]["Price"] == "0,565 €"

    def test_prefers_embedded_packaging_data_over_visible_text_scrape(self):
        html = """
        <html><body>
        <script>
        window.__PRODUCT_DETAIL__ = {
          "product": {
            "packaging": "Reel",
            "reelingAvailability": "Full Reel (Order in multiples of 3000)",
            "minimumOrderQuantity": "5",
            "orderQuantityMultiples": "5",
            "fullReelQuantity": "3000",
            "packagingOptions": [
              {
                "label": "Full Reel",
                "priceBreaks": [
                  {"Quantity": "3000", "Price": "0.565", "Currency": "EUR"}
                ]
              }
            ]
          }
        };
        </script>
        <div>Minimum: 1 Multiples: 1</div>
        <div>Packaging:</div>
        <div>Full Reel (Order in multiples of 2000)</div>
        <div>Pricing (EUR)</div>
        <div>Qty. Unit Price Ext. Price</div>
        <div>Full Reel (Order in multiples of 2000)</div>
        <div>2000 0,600 € 1.200,00 €</div>
        <div>Packaging Choice</div>
        </body></html>
        """

        details = _packaging_details_from_product_page_html(html)

        assert details.packaging_source == "product_page_embedded + product_page"
        assert details.minimum_order_quantity == 5
        assert details.order_multiple == 5
        assert details.full_reel_quantity == 3000
        assert details.full_reel_price_breaks == (
            {"Quantity": "3000", "Price": "0.565", "Currency": "EUR"},
        )


class TestPackagingVariants:
    def test_ti_tube_and_reel_variants_are_packaging_only(self):
        tube = {
            "ManufacturerPartNumber": "TMP421AQDCNTQ1",
            "Description": (
                "Board Mount Temperature Sensors AEC-Q100 Automotive "
                "1Ch Remote Temperature Sensor 8-SOT-23 -40 to 125"
            ),
            "ImagePath": (
                "https://www.mouser.com/images/texasinstruments/images/"
                "ITP_TI_SOT-23-8_DCN_t.jpg"
            ),
        }
        reel = {
            "ManufacturerPartNumber": "TMP421AQDCNRQ1",
            "Description": (
                "Board Mount Temperature Sensors AEC-Q100 Automotive "
                "1Ch Remote Temperatu A 595-TMP421AQDCNTQ1"
            ),
            "ImagePath": (
                "https://www.mouser.com/images/texasinstruments/images/"
                "ITP_TI_SOT-23-8_DCN_t.jpg"
            ),
        }

        assert is_packaging_variant(tube, reel, "TI")


class StubMouserClient:
    def __init__(self, responses):
        self.responses = responses

    def search(self, part_number, search_option="Exact"):
        return self.responses.get((part_number, search_option), [])


class TestPricePart:
    def test_uses_cheaper_price_break_overbuy_when_it_reduces_total_spend(self):
        responses = {
            ("PART1", "Exact"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PART1",
                    "MouserPartNumber": "595-PART1",
                    "Description": "Exact match",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [
                        {"Quantity": "1", "Price": "0.10", "Currency": "EUR"},
                        {"Quantity": "1000", "Price": "0.09", "Currency": "EUR"},
                    ],
                }
            ],
        }
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=950,
        )

        priced = price_part(agg, StubMouserClient(responses))

        assert priced.extended_price == pytest.approx(90.0)
        assert priced.unit_price == pytest.approx(0.09)
        assert priced.required_quantity == 950
        assert priced.purchased_quantity == 1000
        assert priced.surplus_quantity == 50
        assert priced.pricing_strategy == "next price break"

    def test_records_mixed_reel_and_cut_tape_plan_in_priced_part(self):
        responses = {
            ("PART2", "Exact"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PART2",
                    "MouserPartNumber": "595-PART2",
                    "Description": "Exact match",
                    "Availability": "100000 In Stock",
                    "Packaging": "Reel, Cut Tape, MouseReel",
                    "ReelingAvailability": "Full Reel (Order in multiples of 1800)",
                    "MinimumOrderQuantity": "1",
                    "OrderQuantityMultiples": "1",
                    "StandardPackQuantity": "1800",
                    "PriceBreaks": [
                        {"Quantity": "1", "Price": "1.00", "Currency": "EUR"},
                        {"Quantity": "500", "Price": "0.60", "Currency": "EUR"},
                        {"Quantity": "1000", "Price": "0.55", "Currency": "EUR"},
                    ],
                }
            ],
        }
        client = StubMouserClient(responses)
        client.packaging_details = lambda candidate, **kwargs: MouserPackagingDetails(
            packaging_mode="Reel | Cut Tape | MouseReel",
            packaging_source="product_page",
            minimum_order_quantity=1,
            order_multiple=1,
            standard_pack_quantity=1800,
            full_reel_quantity=1800,
            full_reel_price_breaks=(
                {"Quantity": "1800", "Price": "0.40", "Currency": "EUR"},
            ),
        )
        agg = AggregatedPart(
            part_number="PART2",
            manufacturer="Texas Instruments",
            quantity_per_unit=6,
            total_quantity=6000,
        )

        priced = price_part(agg, client)

        assert priced.extended_price == pytest.approx(2520.0)
        assert priced.purchased_quantity == 6000
        assert priced.surplus_quantity == 0
        assert priced.pricing_strategy == "mixed packaging"
        assert priced.order_plan == "3 reels x 1800 + 600 cut tape"
        assert len(priced.purchase_legs) == 2
        assert priced.packaging_mode == "Full Reel + Cut Tape"

    def test_switches_to_cheapest_packaging_variant(self):
        responses = {
            ("PART", "Exact"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTT",
                    "MouserPartNumber": "595-PARTT",
                    "Description": "Tube packaging",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.10", "Currency": "EUR"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTR",
                    "MouserPartNumber": "595-PARTR",
                    "Description": "Tape reel packaging",
                    "Availability": "1000 In Stock",
                    "PriceBreaks": [{"Quantity": "1000", "Price": "0.09", "Currency": "EUR"}],
                },
            ],
        }
        agg = AggregatedPart(
            part_number="PART",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=950,
        )

        priced = price_part(agg, StubMouserClient(responses))

        assert priced.mouser_part_number == "595-PARTR"
        assert priced.manufacturer_part_number == "PARTR"
        assert priced.extended_price == pytest.approx(90.0)
        assert priced.purchased_quantity == 1000
        assert priced.surplus_quantity == 50

    def test_saved_resolution_fast_path_uses_direct_exact_lookup(self, tmp_path):
        responses = {
            ("595-PARTB-Q1", "Exact"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                }
            ],
        }

        class RecordingClient(StubMouserClient):
            def __init__(self, responses):
                super().__init__(responses)
                self.calls = []

            def search(self, part_number, search_option="Exact"):
                self.calls.append((part_number, search_option))
                return super().search(part_number, search_option)

        store = ResolutionStore(tmp_path / "resolutions.json")
        store.set("Texas Instruments", "PART-Q1", "595-PARTB-Q1", "PARTB-Q1")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        client = RecordingClient(responses)

        priced = price_part(agg, client, resolution_store=store)

        assert priced.mouser_part_number == "595-PARTB-Q1"
        assert priced.resolution_source == "saved"
        assert client.calls == [("595-PARTB-Q1", "Exact")]

    def test_preserves_fuzzy_warning_when_price_parsing_fails(self):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "n/a"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                },
            ],
        }
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        priced = price_part(agg, StubMouserClient(responses))

        assert priced.match_method == MatchMethod.FUZZY
        assert priced.match_candidates == 2
        assert priced.lookup_error is not None
        assert "Fuzzy match" in priced.lookup_error
        assert "Failed to parse price" in priced.lookup_error

    def test_saved_resolution_is_reused(self, tmp_path):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.60"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                },
            ],
        }
        store = ResolutionStore(tmp_path / "resolutions.json")
        store.set("Texas Instruments", "PART-Q1", "595-PARTB-Q1", "PARTB-Q1")

        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        priced = price_part(agg, StubMouserClient(responses), resolution_store=store)

        assert priced.mouser_part_number == "595-PARTB-Q1"
        assert priced.resolution_source == "saved"
        assert priced.review_required is False
        assert priced.lookup_error is None

    def test_interactive_selection_is_saved(self, monkeypatch, tmp_path):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.60"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                },
            ],
        }
        store = ResolutionStore(tmp_path / "resolutions.json")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        monkeypatch.setattr("mouser._can_prompt_interactively", lambda: True)
        monkeypatch.setattr(builtins, "input", lambda _: "2")

        priced = price_part(
            agg,
            StubMouserClient(responses),
            interactive=True,
            resolution_store=store,
        )

        saved = store.get("Texas Instruments", "PART-Q1")
        assert priced.mouser_part_number == "595-PARTB-Q1"
        assert priced.resolution_source == "interactive"
        assert priced.review_required is False
        assert priced.lookup_error is None
        assert saved is not None
        assert saved.mouser_part_number == "595-PARTB-Q1"

    def test_interactive_mode_skips_confident_matches(self, monkeypatch, tmp_path):
        responses = {
            ("PART1", "Exact"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PART1",
                    "MouserPartNumber": "595-PART1",
                    "Description": "Exact match",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.10"}],
                }
            ],
        }
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=10,
        )

        calls: list[str] = []
        monkeypatch.setattr("mouser._can_prompt_interactively", lambda: True)
        monkeypatch.setattr(
            builtins,
            "input",
            lambda prompt: calls.append(prompt) or "1",
        )

        priced = price_part(
            agg,
            StubMouserClient(responses),
            interactive=True,
            resolution_store=ResolutionStore(tmp_path / "resolutions.json"),
        )

        assert priced.mouser_part_number == "595-PART1"
        assert priced.resolution_source is None
        assert calls == []

    def test_ai_selection_runs_before_interactive(self, monkeypatch, tmp_path):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.60"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                },
            ],
        }
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        class FakeAIResolver:
            def rerank(self, agg, lookup):
                return type(
                    "Decision",
                    (),
                    {
                        "is_select": True,
                        "selected_index": 2,
                        "confidence": 0.95,
                        "rationale": "Candidate 2 is correct",
                        "missing_context": (),
                    },
                )()

        calls: list[str] = []
        monkeypatch.setattr("mouser._can_prompt_interactively", lambda: True)
        monkeypatch.setattr(
            builtins,
            "input",
            lambda prompt: calls.append(prompt) or "1",
        )

        priced = price_part(
            agg,
            StubMouserClient(responses),
            interactive=True,
            resolution_store=ResolutionStore(tmp_path / "resolutions.json"),
            ai_resolver=FakeAIResolver(),
        )

        assert priced.mouser_part_number == "595-PARTB-Q1"
        assert priced.resolution_source == "ai"
        assert priced.review_required is False
        assert calls == []

    def test_interactive_runs_when_ai_abstains(self, monkeypatch, tmp_path):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.60"}],
                },
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTB-Q1",
                    "MouserPartNumber": "595-PARTB-Q1",
                    "Description": "AEC-Q100 Automotive sensor alt",
                    "Availability": "50 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                },
            ],
        }
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        class FakeAIResolver:
            def rerank(self, agg, lookup):
                return type(
                    "Decision",
                    (),
                    {
                        "is_select": False,
                        "selected_index": 0,
                        "confidence": 0.2,
                        "rationale": "Need package information",
                        "missing_context": ("package",),
                    },
                )()

        monkeypatch.setattr("mouser._can_prompt_interactively", lambda: True)
        monkeypatch.setattr(builtins, "input", lambda _: "2")

        priced = price_part(
            agg,
            StubMouserClient(responses),
            interactive=True,
            resolution_store=ResolutionStore(tmp_path / "resolutions.json"),
            ai_resolver=FakeAIResolver(),
        )

        assert priced.mouser_part_number == "595-PARTB-Q1"
        assert priced.resolution_source == "interactive"
        assert priced.review_required is False

    def test_smart_lookup_skips_inter_pass_sleep_when_cached(self, monkeypatch):
        responses = {
            ("PART-Q1", "Exact"): [],
            ("PART-Q1", "BeginsWith"): [],
            ("PART", "BeginsWith"): [
                {
                    "Manufacturer": "Texas Instruments",
                    "ManufacturerPartNumber": "PARTA-Q1",
                    "MouserPartNumber": "595-PARTA-Q1",
                    "Description": "AEC-Q100 Automotive sensor",
                    "Availability": "100 In Stock",
                    "PriceBreaks": [{"Quantity": "1", "Price": "0.50"}],
                }
            ],
        }

        class CachedClient(StubMouserClient):
            def has_cached_search(self, part_number, search_option="Exact"):
                return True

        sleeps: list[float] = []
        monkeypatch.setattr("mouser.time.sleep", lambda seconds: sleeps.append(seconds))

        lookup = smart_lookup("PART-Q1", "Texas Instruments", CachedClient(responses))

        assert lookup.method == MatchMethod.FUZZY
        assert sleeps == []

    def test_qualified_parts_skip_initial_exact_pass(self):
        passes = _build_lookup_passes("PART-Q1", "PART")

        assert [(item.search_term, item.search_option) for item in passes] == [
            ("PART-Q1", "BeginsWith"),
            ("PART", "BeginsWith"),
        ]

class TestMouserClient:
    def test_packaging_details_do_not_fetch_product_page_by_default(self):
        client = MouserClient(api_key="dummy", cache_enabled=False)

        class RecordingTransport:
            def get(self, url, follow_redirects=True):
                raise AssertionError("product page fallback should stay disabled by default")

            def close(self):
                pass

        client._client = RecordingTransport()

        details = client.packaging_details(
            {"ProductDetailUrl": "https://example.com/product"}
        )

        assert details.packaging_source is None

    def test_packaging_details_can_use_explicit_product_page_fallback(self):
        client = MouserClient(
            api_key="dummy",
            cache_enabled=False,
            allow_product_page_fallback=True,
        )

        class RecordingTransport:
            def __init__(self):
                self.calls = 0

            def get(self, url, follow_redirects=True):
                self.calls += 1
                request = httpx.Request("GET", url)
                return httpx.Response(
                    200,
                    text=(
                        "<html><body><div>Minimum: 1 Multiples: 1</div>"
                        "<div>Full Reel (Order in multiples of 3000)</div>"
                        "<div>Pricing (EUR)</div>"
                        "<div>Qty. Unit Price Ext. Price</div>"
                        "<div>Full Reel (Order in multiples of 3000)</div>"
                        "<div>3000 0,565 € 1.695,00 €</div>"
                        "<div>Packaging Choice</div></body></html>"
                    ),
                    request=request,
                )

            def close(self):
                pass

        transport = RecordingTransport()
        client._client = transport

        details = client.packaging_details(
            {"ProductDetailUrl": "https://example.com/product"}
        )

        assert transport.calls == 1
        assert details.packaging_source == "product_page"
        assert details.full_reel_quantity == 3000

    def test_product_page_fallback_is_reused_from_persistent_cache(
        self, monkeypatch, tmp_path
    ):
        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(tmp_path / "cache.sqlite3"))

        class RecordingTransport:
            def __init__(self):
                self.calls = 0

            def get(self, url, follow_redirects=True):
                self.calls += 1
                request = httpx.Request("GET", url)
                return httpx.Response(
                    200,
                    text=(
                        "<html><body><div>Minimum: 1 Multiples: 1</div>"
                        "<div>Full Reel (Order in multiples of 3000)</div>"
                        "<div>Pricing (EUR)</div>"
                        "<div>Qty. Unit Price Ext. Price</div>"
                        "<div>Full Reel (Order in multiples of 3000)</div>"
                        "<div>3000 0,565 € 1.695,00 €</div>"
                        "<div>Packaging Choice</div></body></html>"
                    ),
                    request=request,
                )

            def close(self):
                pass

        first_transport = RecordingTransport()
        client = MouserClient(
            api_key="dummy",
            cache_enabled=True,
            allow_product_page_fallback=True,
        )
        client._client = first_transport
        first_details = client.packaging_details(
            {"ProductDetailUrl": "https://example.com/product"}
        )
        client.close()

        second_transport = RecordingTransport()
        cached_client = MouserClient(
            api_key="dummy",
            cache_enabled=True,
            allow_product_page_fallback=True,
        )
        cached_client._client = second_transport
        cached_details = cached_client.packaging_details(
            {"ProductDetailUrl": "https://example.com/product"}
        )
        cached_client.close()

        assert first_transport.calls == 1
        assert second_transport.calls == 0
        assert first_details.full_reel_quantity == 3000
        assert cached_details.full_reel_quantity == 3000

    def test_manufacturer_page_fallback_is_reused_from_persistent_cache(
        self, monkeypatch, tmp_path
    ):
        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(tmp_path / "cache.sqlite3"))
        manufacturer_details = ManufacturerPackagingDetails(
            packaging_mode="Full Reel",
            packaging_source="manufacturer_page",
            full_reel_quantity=3000,
        )
        manufacturer_url = "https://www.ti.com/product/PART/part-details/PARTOPN"

        monkeypatch.setattr("mouser.manufacturer_page_url", lambda *args, **kwargs: manufacturer_url)
        monkeypatch.setattr(
            "mouser.manufacturer_packaging_details_from_html",
            lambda *args, **kwargs: manufacturer_details,
        )

        class RecordingTransport:
            def __init__(self):
                self.calls = 0

            def get(self, url, follow_redirects=True):
                self.calls += 1
                request = httpx.Request("GET", url)
                return httpx.Response(200, text="<html></html>", request=request)

            def close(self):
                pass

        candidate = {
            "Manufacturer": "Texas Instruments",
            "ManufacturerPartNumber": "PARTOPN",
        }

        first_transport = RecordingTransport()
        client = MouserClient(
            api_key="dummy",
            cache_enabled=True,
            allow_manufacturer_page_fallback=True,
        )
        client._client = first_transport
        first_details = client.packaging_details(
            candidate,
            bom_part_number="PART",
        )
        client.close()

        second_transport = RecordingTransport()
        cached_client = MouserClient(
            api_key="dummy",
            cache_enabled=True,
            allow_manufacturer_page_fallback=True,
        )
        cached_client._client = second_transport
        cached_details = cached_client.packaging_details(
            candidate,
            bom_part_number="PART",
        )
        cached_client.close()

        assert first_transport.calls == 1
        assert second_transport.calls == 0
        assert first_details.full_reel_quantity == 3000
        assert cached_details.full_reel_quantity == 3000

    def test_rotates_to_backup_key_after_daily_limit(self, monkeypatch):
        client = MouserClient(api_key="primary-key", cache_enabled=False)
        client.api_keys = ("primary-key", "backup-key")
        client.api_key = "primary-key"
        client._current_api_key_index = 0

        class FakeTransport:
            def __init__(self):
                self.calls = []

            def post(self, url, json):
                self.calls.append(url)
                request = httpx.Request("POST", url)
                if "primary-key" in url:
                    return httpx.Response(
                        403,
                        json={
                            "Errors": [
                                {
                                    "Code": "TooManyRequests",
                                    "Message": "Maximum calls per day exceeded",
                                }
                            ]
                        },
                        request=request,
                    )
                return httpx.Response(
                    200,
                    json={
                        "SearchResults": {
                            "Parts": [
                                {
                                    "Manufacturer": "Texas Instruments",
                                    "ManufacturerPartNumber": "PART-Q1",
                                    "MouserPartNumber": "595-PART-Q1",
                                }
                            ]
                        }
                    },
                    request=request,
                )

            def close(self):
                pass

        transport = FakeTransport()
        client._client = transport
        monkeypatch.setattr("mouser.time.sleep", lambda seconds: None)

        parts = client.search("PART-Q1", "Exact")

        assert parts[0]["MouserPartNumber"] == "595-PART-Q1"
        assert len(transport.calls) == 2
        assert "primary-key" in transport.calls[0]
        assert "backup-key" in transport.calls[1]

    def test_daily_limit_error_is_not_retried(self, monkeypatch):
        client = MouserClient(api_key="dummy", cache_enabled=False)

        class FakeTransport:
            def __init__(self):
                self.calls = 0

            def post(self, url, json):
                self.calls += 1
                request = httpx.Request("POST", url)
                return httpx.Response(
                    403,
                    json={
                        "Errors": [
                            {
                                "Code": "TooManyRequests",
                                "Message": "Maximum calls per day exceeded",
                            }
                        ]
                    },
                    request=request,
                )

            def close(self):
                pass

        transport = FakeTransport()
        client._client = transport
        monkeypatch.setattr("mouser.time.sleep", lambda seconds: None)

        with pytest.raises(httpx.HTTPStatusError):
            client.search("PART-Q1", "Exact")

        assert transport.calls == 1
