"""Tests for CLI helpers."""

import sys
from argparse import Namespace
from pathlib import Path
import types

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import main
from main import _match_result_label, build_input_designs, resolve_output_format
from models import AggregatedPart, BomSummary, DistributorOffer, MatchMethod, PricedPart


class TestResolveOutputFormat:
    def test_infers_format_from_output_extension(self):
        fmt, output = resolve_output_format(
            Namespace(format=None, output=Path("reports/bom.xlsx"))
        )

        assert fmt == "excel"
        assert output == Path("reports/bom.xlsx")

    def test_defaults_to_csv_when_output_missing(self):
        fmt, output = resolve_output_format(Namespace(format=None, output=None))

        assert fmt == "csv"
        assert output == Path("bom_output.csv")

    def test_unknown_extension_warns_and_defaults_to_csv(self, capsys):
        fmt, output = resolve_output_format(
            Namespace(format=None, output=Path("reports/bom.custom"))
        )

        captured = capsys.readouterr()
        assert fmt == "csv"
        assert output == Path("reports/bom.custom")
        assert "defaulting to CSV" in captured.err


class TestMatchResultLabel:
    def test_distinguishes_fuzzy_review_from_resolved(self):
        resolved = PricedPart(
            part_number="PART1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=1,
            match_method=MatchMethod.FUZZY,
            review_required=False,
        )
        review = PricedPart(
            part_number="PART2",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=1,
            match_method=MatchMethod.FUZZY,
            review_required=True,
        )

        assert _match_result_label(resolved) == "Fuzzy-resolved match"
        assert _match_result_label(review) == "Fuzzy match (review!)"

    def test_reports_lookup_failures_separately(self):
        failed = PricedPart(
            part_number="PART3",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=1,
            lookup_error="HTTP 403: rate limited",
        )

        assert _match_result_label(failed) == "Lookup failed"


class TestParseArgs:
    def test_accepts_single_part_mode(self):
        args = main.parse_args(
            [
                "--part-number", "ADS7138-Q1",
                "--manufacturer", "TI",
                "--quantity-per-unit", "2",
                "--description", "ADC",
                "--package", "TSSOP",
                "--pins", "16",
                "--units", "5",
            ]
        )

        assert args.design is None
        assert args.part_number == "ADS7138-Q1"
        assert args.manufacturer == "TI"
        assert args.quantity_per_unit == 2
        assert args.description == "ADC"
        assert args.package == "TSSOP"
        assert args.pins == 16

    def test_requires_manufacturer_for_single_part_mode(self):
        with pytest.raises(SystemExit):
            main.parse_args(["--part-number", "ADS7138-Q1", "--units", "1"])

    def test_rejects_single_part_flags_without_part_number(self):
        with pytest.raises(SystemExit):
            main.parse_args(
                [
                    "--design", "LUPA_48VGen_BOM.json",
                    "--manufacturer", "TI",
                    "--units", "1",
                ]
            )


class TestBuildInputDesigns:
    def test_builds_synthetic_design_for_single_part_mode(self, capsys):
        args = Namespace(
            design=None,
            part_number="ADS7138-Q1",
            manufacturer="TI",
            quantity_per_unit=2,
            description="ADC",
            package="TSSOP",
            pins=16,
        )

        designs = build_input_designs(args)
        captured = capsys.readouterr()

        assert len(designs) == 1
        assert designs[0].design == "Direct lookup"
        assert designs[0].parts[0].part_number == "ADS7138-Q1"
        assert designs[0].parts[0].manufacturer == "TI"
        assert designs[0].parts[0].quantity == 2
        assert "Preparing direct lookup" in captured.out


class TestRun:
    def test_interactive_requires_tty(self, monkeypatch):
        args = Namespace(
            verbose=False,
            interactive=True,
            ai_resolve=False,
            ai_model="gpt-5.4-mini",
            ai_confidence_threshold=0.85,
            design=[],
            units=1,
            attrition=0.0,
            dry_run=True,
            format="json",
            output=Path("out.json"),
            api_key="",
            delay=0.0,
            no_cache=False,
            cache_ttl_hours=24.0,
        )
        monkeypatch.setattr(main.sys, "stdin", types.SimpleNamespace(isatty=lambda: False))
        monkeypatch.setattr(main.sys, "stdout", types.SimpleNamespace(isatty=lambda: False))

        assert main.run(args) == 2

    def test_print_summary_uses_per_unit_cost_and_reports_overbuy(self, capsys):
        parts = [
            PricedPart(
                part_number="PART1",
                manufacturer="TI",
                quantity_per_unit=2,
                total_quantity=200,
                unit_price=0.05,
                extended_price=10.0,
                currency="EUR",
                required_quantity=200,
                purchased_quantity=200,
                surplus_quantity=0,
            ),
            PricedPart(
                part_number="PART2",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=100,
                unit_price=0.09,
                extended_price=11.0,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=120,
                surplus_quantity=20,
                pricing_strategy="next price break",
            ),
        ]
        summary = BomSummary.from_parts(parts, units=100)

        main.print_summary(parts, summary)

        output = capsys.readouterr().out
        assert "Total BOM cost" not in output
        assert "BOM cost per unit" in output
        assert "Avg priced line/unit" not in output
        assert "Top 10 by per-unit cost" in output
        assert "Overbuy selections" in output

    def test_run_writes_trace_transcript_when_enabled(self, tmp_path):
        trace_path = tmp_path / "trace.log"
        output_path = tmp_path / "out.json"
        args = Namespace(
            verbose=False,
            interactive=False,
            ai_resolve=False,
            ai_model="gpt-5.4-mini",
            ai_confidence_threshold=0.85,
            design=None,
            part_number="ADS7138-Q1",
            manufacturer="TI",
            quantity_per_unit=1,
            description="ADC",
            package="TSSOP",
            pins=16,
            units=1,
            attrition=0.0,
            dry_run=True,
            format="json",
            output=output_path,
            trace_file=trace_path,
            api_key="",
            delay=0.0,
            no_cache=False,
            cache_ttl_hours=24.0,
        )

        assert main.run(args) == 0

        trace_text = trace_path.read_text(encoding="utf-8")
        assert "=== BOM Builder Trace ===" in trace_text
        assert "Execution Mode: single-process, sequential part lookups" in trace_text
        assert "Preparing direct lookup: ADS7138-Q1 (TI)" in trace_text
        assert f"Output Path: {output_path}" in trace_text


class TestOfferSelection:
    def test_prefers_cheapest_confident_offer(self):
        offers = [
            DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART",
                extended_price=10.0,
                unit_price=1.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="P5555-ND",
                extended_price=8.0,
                unit_price=0.8,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            ),
        ]

        selected = main._select_preferred_offer(offers)

        assert selected is not None
        assert selected.distributor == "Digi-Key"


class TestMultiDistributorPricing:
    def test_selects_digikey_offer_when_it_is_cheaper(self, monkeypatch):
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*args, **kwargs):
            priced = PricedPart.from_aggregated(agg)
            mouser_offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                manufacturer_part_number="PART1",
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [mouser_offer]
            priced.apply_selected_offer(mouser_offer)
            return priced

        def fake_price_part_via_digikey(*args, **kwargs):
            return DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="P5555-ND",
                manufacturer_part_number="PART1",
                unit_price=0.9,
                extended_price=90.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )

        class DummyMouserClient:
            network_requests = 1

        class DummyDigiKeyClient:
            network_requests = 1

        class DummyFXRateProvider:
            pass

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "price_part_via_digikey", fake_price_part_via_digikey)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)

        results = main._price_parts_across_distributors(
            [agg],
            DummyMouserClient(),
            digikey_client=DummyDigiKeyClient(),
            ti_client=None,
            fx_rate_provider=DummyFXRateProvider(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 1
        assert results[0].distributor == "Digi-Key"
        assert results[0].distributor_part_number == "P5555-ND"
        assert results[0].extended_price == 90.0
        assert len(results[0].offers) == 2

    def test_selects_ti_direct_offer_when_it_is_cheaper(self, monkeypatch):
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*args, **kwargs):
            priced = PricedPart.from_aggregated(agg)
            mouser_offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                manufacturer_part_number="PART1",
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [mouser_offer]
            priced.apply_selected_offer(mouser_offer)
            return priced

        def fake_price_part_via_ti(*args, **kwargs):
            return DistributorOffer(
                distributor="TI",
                distributor_part_number="PART1",
                manufacturer_part_number="PART1",
                unit_price=0.75,
                extended_price=75.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )

        class DummyMouserClient:
            network_requests = 1

        class DummyTIClient:
            network_requests = 1

        class DummyFXRateProvider:
            pass

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "price_part_via_ti", fake_price_part_via_ti)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)

        results = main._price_parts_across_distributors(
            [agg],
            DummyMouserClient(),
            digikey_client=None,
            ti_client=DummyTIClient(),
            fx_rate_provider=DummyFXRateProvider(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 1
        assert results[0].distributor == "TI"
        assert results[0].distributor_part_number == "PART1"
        assert results[0].extended_price == 75.0
        assert len(results[0].offers) == 2

    def test_selects_ti_offer_after_usd_to_eur_conversion(self, monkeypatch):
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*args, **kwargs):
            priced = PricedPart.from_aggregated(agg)
            mouser_offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                manufacturer_part_number="PART1",
                unit_price=0.95,
                extended_price=95.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [mouser_offer]
            priced.apply_selected_offer(mouser_offer)
            return priced

        def fake_price_part_via_ti(*args, **kwargs):
            return DistributorOffer(
                distributor="TI",
                distributor_part_number="PART1",
                manufacturer_part_number="PART1",
                unit_price=1.0,
                extended_price=100.0,
                currency="USD",
                match_method=MatchMethod.EXACT,
            )

        class DummyMouserClient:
            network_requests = 1

        class DummyTIClient:
            network_requests = 1

        def fake_convert_offers_currency(offers, comparison_currency, _fx):
            converted: list[DistributorOffer] = []
            for offer in offers:
                if offer.distributor == "TI":
                    converted.append(
                        offer.model_copy(
                            update={
                                "currency": comparison_currency,
                                "unit_price": 0.9,
                                "extended_price": 90.0,
                            }
                        )
                    )
                else:
                    converted.append(offer)
            return converted

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "price_part_via_ti", fake_price_part_via_ti)
        monkeypatch.setattr(main, "convert_offers_currency", fake_convert_offers_currency)

        results = main._price_parts_across_distributors(
            [agg],
            DummyMouserClient(),
            digikey_client=None,
            ti_client=DummyTIClient(),
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 1
        assert results[0].distributor == "TI"
        assert results[0].currency == "EUR"
        assert results[0].unit_price == pytest.approx(0.9)
        assert results[0].extended_price == pytest.approx(90.0)

    def test_prefers_confident_offer_over_cheaper_review_required_offer(self):
        offers = [
            DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-FUZZY",
                extended_price=7.0,
                unit_price=0.7,
                currency="EUR",
                match_method=MatchMethod.FUZZY,
                review_required=True,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="P5555-ND",
                extended_price=8.0,
                unit_price=0.8,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            ),
        ]

        selected = main._select_preferred_offer(offers)

        assert selected is not None
        assert selected.distributor == "Digi-Key"

    def test_cheapest_note_omits_suffix_when_selected_offer_is_only_safer(self):
        priced = PricedPart(
            part_number="PART1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
            distributor="Digi-Key",
            distributor_part_number="P5555-ND",
            currency="EUR",
        )
        priced.offers = [
            DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                extended_price=50.0,
                unit_price=0.5,
                currency="EUR",
                match_method=MatchMethod.FUZZY,
                review_required=True,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="P5555-ND",
                extended_price=60.0,
                unit_price=0.6,
                currency="EUR",
                match_method=MatchMethod.EXACT,
                review_required=False,
            ),
        ]

        assert main._compared_cheapest_note(priced) is None

    def test_print_lookup_status_uses_compact_buyer_facing_layout(self, capsys):
        priced = PricedPart(
            part_number="PART1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
            distributor="Digi-Key",
            distributor_part_number="296-PART1CT-ND",
            unit_price=0.8123,
            extended_price=81.23,
            currency="EUR",
            match_method=MatchMethod.EXACT,
            required_quantity=100,
            purchased_quantity=120,
            surplus_quantity=20,
            packaging_mode="Cut Tape (CT)",
        )
        priced.offers = [
            DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                extended_price=85.0,
                unit_price=0.85,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="296-PART1CT-ND",
                extended_price=81.23,
                unit_price=0.8123,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            ),
        ]

        main._print_lookup_status(priced)

        output = capsys.readouterr().out
        assert "OK      Digi-Key" in output
        assert "296-PART1CT-ND" in output
        assert "120 cut tape" in output
        assert "0.8123 EUR ea" in output
        assert "81.23 EUR" in output
        assert "exact match; cheapest source; 100 needed, 120 ordered, 20 spare" in output
