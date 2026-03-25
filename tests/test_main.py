"""Tests for CLI helpers."""

import sys
from argparse import Namespace
from pathlib import Path
import types

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import main
from config import PROJECT_VERSION
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
    def test_help_lists_mouser_specific_flags(self, capsys):
        with pytest.raises(SystemExit) as excinfo:
            main.parse_args(["-h"])

        output = capsys.readouterr().out
        assert excinfo.value.code == 0
        assert "-h, --help" in output
        assert "--mouser-api-key" in output
        assert "--mouser-delay" in output
        assert "--flush" in output
        assert "--flush-resolutions" in output
        assert "--api-key" not in output
        assert "--delay" not in output

    def test_version_option_prints_release(self, capsys):
        with pytest.raises(SystemExit) as excinfo:
            main.parse_args(["--version"])

        output = capsys.readouterr().out.strip()
        assert excinfo.value.code == 0
        assert output == f"main.py {PROJECT_VERSION}"

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

    def test_accepts_mouser_specific_cli_flags(self):
        args = main.parse_args(
            [
                "--design", "LUPA_48VGen_BOM.json",
                "--units", "5",
                "--mouser-api-key", "test-key",
                "--mouser-delay", "2.5",
            ]
        )

        assert args.mouser_api_key == "test-key"
        assert args.mouser_delay == pytest.approx(2.5)

    def test_accepts_flush_as_a_standalone_action(self):
        args = main.parse_args(["--flush"])

        assert args.flush is True
        assert args.flush_resolutions is False
        assert args.design is None
        assert args.part_number is None
        assert args.units is None

    def test_accepts_flush_resolutions_as_a_standalone_action(self):
        args = main.parse_args(["--flush-resolutions"])

        assert args.flush is False
        assert args.flush_resolutions is True
        assert args.design is None
        assert args.part_number is None
        assert args.units is None

    def test_rejects_missing_lookup_target_without_flush(self):
        with pytest.raises(SystemExit):
            main.parse_args([])

    def test_rejects_units_for_standalone_flush(self):
        with pytest.raises(SystemExit):
            main.parse_args(["--flush", "--units", "5"])

    def test_digikey_query_terms_use_confirmed_manufacturer_part_number_only(self):
        agg = AggregatedPart(
            part_number="TCAN1472V-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
        )
        priced = PricedPart(
            part_number="TCAN1472V-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
            manufacturer_part_number="TCAN1472VDRQ1",
            match_method=MatchMethod.EXACT,
            review_required=False,
        )

        terms = main._digikey_query_terms(agg, priced)

        assert terms == ["TCAN1472VDRQ1"]

    def test_ti_query_terms_use_bom_part_number_first_with_confirmed_mpn_fallback(self):
        agg = AggregatedPart(
            part_number="TPS61041-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
        )
        priced = PricedPart(
            part_number="TPS61041-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
            manufacturer_part_number="TPS61041QDBVRQ1",
            match_method=MatchMethod.EXACT,
            review_required=False,
        )

        terms = main._ti_query_terms(agg, priced)

        assert terms == ["TPS61041-Q1", "TPS61041QDBVRQ1"]

    def test_ti_query_terms_use_bom_part_number_without_confirmed_mpn(self):
        agg = AggregatedPart(
            part_number="TPS61041-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
        )
        priced = PricedPart(
            part_number="TPS61041-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
            match_method=MatchMethod.NOT_FOUND,
            review_required=False,
        )

        terms = main._ti_query_terms(agg, priced)

        assert terms == ["TPS61041-Q1"]

    def test_ti_query_terms_include_review_candidate_as_manufacturer_fallback(self):
        agg = AggregatedPart(
            part_number="TLIN4029A-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
        )
        priced = PricedPart(
            part_number="TLIN4029A-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=1000,
            manufacturer_part_number="TLIN4029ADRQ1",
            match_method=MatchMethod.FUZZY,
            review_required=True,
        )

        terms = main._ti_query_terms(agg, priced)

        assert terms == ["TLIN4029A-Q1", "TLIN4029ADRQ1"]

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


class TestFlushAction:
    def test_run_flushes_cache_files_and_orphaned_resolution_temp(
        self, monkeypatch, tmp_path, capsys
    ):
        cache_db = tmp_path / "cache.sqlite3"
        cache_shm = tmp_path / "cache.sqlite3-shm"
        cache_wal = tmp_path / "cache.sqlite3-wal"
        cache_journal = tmp_path / "cache.sqlite3-journal"
        resolutions = tmp_path / "resolutions.json"
        resolutions_tmp = tmp_path / "resolutions.tmp"

        for path in [cache_db, cache_shm, cache_wal, cache_journal, resolutions, resolutions_tmp]:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("x", encoding="utf-8")

        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(cache_db))
        monkeypatch.setenv("BOM_BUILDER_RESOLUTIONS_FILE", str(resolutions))

        exit_code = main.run(main.parse_args(["--flush"]))

        output = capsys.readouterr().out
        assert exit_code == 0
        assert "Flushing runtime caches and temp files..." in output
        assert not cache_db.exists()
        assert not cache_shm.exists()
        assert not cache_wal.exists()
        assert not cache_journal.exists()
        assert resolutions.exists()
        assert not resolutions_tmp.exists()

    def test_run_flush_resolutions_removes_saved_resolution_store(
        self, monkeypatch, tmp_path, capsys
    ):
        cache_db = tmp_path / "cache.sqlite3"
        resolutions = tmp_path / "resolutions.json"
        resolutions_tmp = tmp_path / "resolutions.tmp"
        cache_db.write_text("x", encoding="utf-8")
        resolutions.write_text("{}", encoding="utf-8")
        resolutions_tmp.write_text("{}", encoding="utf-8")

        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(cache_db))
        monkeypatch.setenv("BOM_BUILDER_RESOLUTIONS_FILE", str(resolutions))

        exit_code = main.run(main.parse_args(["--flush-resolutions"]))

        output = capsys.readouterr().out
        assert exit_code == 0
        assert "including saved manual resolutions" in output
        assert not cache_db.exists()
        assert not resolutions.exists()
        assert not resolutions_tmp.exists()

    def test_run_flush_then_lookup_continues_normally(self, monkeypatch, tmp_path, capsys):
        cache_db = tmp_path / "cache.sqlite3"
        cache_db.parent.mkdir(parents=True, exist_ok=True)
        cache_db.write_text("x", encoding="utf-8")
        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(cache_db))
        monkeypatch.setenv("BOM_BUILDER_RESOLUTIONS_FILE", str(tmp_path / "resolutions.json"))

        fake_design = types.SimpleNamespace(design="Demo")
        aggregated = [
            AggregatedPart(
                part_number="PART1",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=10,
            )
        ]
        priced = [
            PricedPart(
                part_number="PART1",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=10,
            )
        ]
        summary = BomSummary.from_parts(priced, units=10)
        observed: dict[str, object] = {}

        monkeypatch.setattr(main, "build_input_designs", lambda _args: [fake_design])
        monkeypatch.setattr(main, "aggregate_parts", lambda _designs, units, attrition: aggregated)
        monkeypatch.setattr(main, "price_parts", lambda _aggregated, _args, **_kwargs: priced)
        monkeypatch.setattr(main, "write_report", lambda _parts, fmt, output, _summary: observed.update({"fmt": fmt, "output": output}))
        monkeypatch.setattr(main, "print_summary", lambda _parts, _summary: observed.update({"summary": summary}))

        exit_code = main.run(
            main.parse_args(
                ["--flush", "--design", "demo.json", "--units", "10", "--format", "json"]
            )
        )

        output = capsys.readouterr().out
        assert exit_code == 0
        assert "Flushing runtime caches and temp files..." in output
        assert "Aggregating for 10 units" in output
        assert observed["fmt"] == "json"
        assert observed["output"] == Path("bom_output.json")
        assert not cache_db.exists()


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
            mouser_api_key="",
            mouser_delay=0.0,
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
            mouser_api_key="",
            mouser_delay=0.0,
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

    def test_prefers_lower_surplus_supplier_when_cheapest_overbuys_too_much(self, monkeypatch):
        monkeypatch.setenv("BOM_BUILDER_SURPLUS_PENALTY_FACTOR", "0.25")
        offers = [
            DistributorOffer(
                distributor="NXP",
                distributor_part_number="NXP-PART",
                extended_price=100.0,
                unit_price=1.0,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=200,
                surplus_quantity=100,
                match_method=MatchMethod.EXACT,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="DGK-PART",
                extended_price=120.0,
                unit_price=1.2,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=100,
                surplus_quantity=0,
                match_method=MatchMethod.EXACT,
            ),
        ]

        selected = main._select_preferred_offer(offers)

        assert selected is not None
        assert selected.distributor == "Digi-Key"

    def test_keeps_cheapest_supplier_when_surplus_penalty_is_outweighed(self, monkeypatch):
        monkeypatch.setenv("BOM_BUILDER_SURPLUS_PENALTY_FACTOR", "0.25")
        offers = [
            DistributorOffer(
                distributor="NXP",
                distributor_part_number="NXP-PART",
                extended_price=100.0,
                unit_price=1.0,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=140,
                surplus_quantity=40,
                match_method=MatchMethod.EXACT,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="DGK-PART",
                extended_price=120.0,
                unit_price=1.2,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=100,
                surplus_quantity=0,
                match_method=MatchMethod.EXACT,
            ),
        ]

        selected = main._select_preferred_offer(offers)

        assert selected is not None
        assert selected.distributor == "NXP"


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

    def test_lookup_note_explains_surplus_adjusted_choice(self, monkeypatch):
        monkeypatch.setenv("BOM_BUILDER_SURPLUS_PENALTY_FACTOR", "0.25")
        priced = PricedPart(
            part_number="PART1",
            manufacturer="NXP",
            quantity_per_unit=1,
            total_quantity=100,
            distributor="Digi-Key",
            distributor_part_number="DGK-PART",
            currency="EUR",
        )
        priced.offers = [
            DistributorOffer(
                distributor="NXP",
                distributor_part_number="NXP-PART",
                extended_price=100.0,
                unit_price=1.0,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=200,
                surplus_quantity=100,
                match_method=MatchMethod.EXACT,
                review_required=False,
            ),
            DistributorOffer(
                distributor="Digi-Key",
                distributor_part_number="DGK-PART",
                extended_price=120.0,
                unit_price=1.2,
                currency="EUR",
                required_quantity=100,
                purchased_quantity=100,
                surplus_quantity=0,
                match_method=MatchMethod.EXACT,
                review_required=False,
            ),
        ]

        note = main._lookup_note(priced)

        assert note is not None
        assert "surplus-adjusted over NXP" in note
        assert "-100 spare" in note

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

        main._print_lookup_status(
            priced,
            part_duration=0.42,
            source_timings=[("mouser", 0.111), ("digikey", 0.309)],
        )

        output = capsys.readouterr().out
        assert "OK      Digi-Key" in output
        assert "296-PART1CT-ND" in output
        assert "120 cut tape" in output
        assert "0.8123 EUR ea" in output
        assert "81.23 EUR" in output
        assert "[0.420s | mouser=0.111s | digikey=0.309s]" in output
        assert "exact match; cheapest source; 100 needed, 120 ordered, 20 spare" in output

    def test_live_lookup_output_includes_elapsed_and_part_duration(
        self, monkeypatch, capsys
    ):
        agg = AggregatedPart(
            part_number="PART1",
            manufacturer="TI",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*_args, **_kwargs):
            priced = PricedPart.from_aggregated(agg)
            offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number="595-PART1",
                manufacturer_part_number="PART1",
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [offer]
            priced.apply_selected_offer(offer)
            return priced

        class DummyMouserClient:
            network_requests = 0
            paced_network_requests = 0

        perf_values = iter([100.0, 100.0, 100.5, 100.5])
        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)
        monkeypatch.setattr(main.time, "perf_counter", lambda: next(perf_values))

        results = main._price_parts_across_distributors(
            [agg],
            DummyMouserClient(),
            digikey_client=None,
            ti_client=None,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
            run_started_at=90.0,
        )

        output = capsys.readouterr().out
        assert len(results) == 1
        assert "[1/1 +00:10.000] Looking up PART1..." in output
        assert "[0.500s | mouser=0.500s]" in output

    def test_delay_is_not_applied_when_only_ti_uses_live_network(self, monkeypatch):
        parts = [
            AggregatedPart(
                part_number="PART1",
                manufacturer="Texas Instruments",
                quantity_per_unit=1,
                total_quantity=100,
            ),
            AggregatedPart(
                part_number="PART2",
                manufacturer="Texas Instruments",
                quantity_per_unit=1,
                total_quantity=100,
            ),
        ]

        def fake_price_mouser_part(agg, *_args, **_kwargs):
            priced = PricedPart.from_aggregated(agg)
            offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number=f"595-{agg.part_number}",
                manufacturer_part_number=agg.part_number,
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [offer]
            priced.apply_selected_offer(offer)
            return priced

        class DummyMouserClient:
            def __init__(self):
                self.network_requests = 0

        class DummyTIClient:
            def __init__(self):
                self.network_requests = 0

        ti_client = DummyTIClient()

        def fake_price_part_via_ti(_agg, client, **_kwargs):
            client.network_requests += 1
            return DistributorOffer(
                distributor="TI",
                distributor_part_number="TI-PART",
                manufacturer_part_number="TI-PART",
                unit_price=0.9,
                extended_price=90.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )

        sleep_calls: list[float] = []

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "price_part_via_ti", fake_price_part_via_ti)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)
        monkeypatch.setattr(main.time, "sleep", lambda seconds: sleep_calls.append(seconds))

        results = main._price_parts_across_distributors(
            parts,
            DummyMouserClient(),
            digikey_client=None,
            ti_client=ti_client,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=1.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 2
        assert sleep_calls == []

    def test_uses_bom_part_number_for_ti_lookup_when_manufacturer_is_ti(self, monkeypatch):
        agg = AggregatedPart(
            part_number="TPS61041-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*_args, **_kwargs):
            return PricedPart.from_aggregated(agg)

        class DummyMouserClient:
            network_requests = 0
            paced_network_requests = 0

        class DummyTIClient:
            network_requests = 0

        captured_terms: list[str] = []

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)

        def fake_price_part_via_ti(_agg, _client, **kwargs):
            captured_terms.extend(kwargs.get("query_terms") or [])
            return DistributorOffer(distributor="TI", lookup_error="not priced")

        monkeypatch.setattr(main, "price_part_via_ti", fake_price_part_via_ti)

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
            run_started_at=0.0,
        )

        assert len(results) == 1
        assert captured_terms == ["TPS61041-Q1"]

    def test_uses_bom_part_number_for_nxp_lookup_when_manufacturer_is_nxp(self, monkeypatch):
        agg = AggregatedPart(
            part_number="KW47B42ZB7AFTB",
            manufacturer="NXP",
            quantity_per_unit=1,
            total_quantity=100,
        )

        def fake_price_mouser_part(*_args, **_kwargs):
            return PricedPart.from_aggregated(agg)

        class DummyMouserClient:
            network_requests = 0
            paced_network_requests = 0

        class DummyNXPClient:
            network_requests = 0

            def consume_runtime_notices(self):
                return []

        captured_terms: list[str] = []

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)

        def fake_price_part_via_nxp(_agg, _client, **kwargs):
            captured_terms.extend(kwargs.get("query_terms") or [])
            return DistributorOffer(distributor="NXP", lookup_error="not priced")

        monkeypatch.setattr(main, "price_part_via_nxp", fake_price_part_via_nxp)

        results = main._price_parts_across_distributors(
            [agg],
            DummyMouserClient(),
            digikey_client=None,
            ti_client=None,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
            run_started_at=0.0,
            nxp_client=DummyNXPClient(),
        )

        assert len(results) == 1
        assert captured_terms == ["KW47B42ZB7AFTB"]

    def test_prints_nxp_runtime_notice_once(self, monkeypatch, capsys):
        parts = [
            AggregatedPart(
                part_number="KW47B42ZB7AFTB",
                manufacturer="NXP",
                quantity_per_unit=1,
                total_quantity=100,
            ),
            AggregatedPart(
                part_number="NCJ3310AHN/0J",
                manufacturer="NXP",
                quantity_per_unit=1,
                total_quantity=100,
            ),
        ]

        def fake_price_mouser_part(agg, *_args, **_kwargs):
            return PricedPart.from_aggregated(agg)

        class DummyMouserClient:
            network_requests = 0
            paced_network_requests = 0

        class DummyNXPClient:
            network_requests = 0

            def __init__(self):
                self._consumed = False

            def consume_runtime_notices(self):
                if self._consumed:
                    return []
                self._consumed = True
                return ["NXP direct disabled for this run; continuing without NXP direct pricing"]

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)
        monkeypatch.setattr(
            main,
            "price_part_via_nxp",
            lambda *_args, **_kwargs: DistributorOffer(
                distributor="NXP",
                lookup_error="NXP direct unavailable for this run",
            ),
        )

        results = main._price_parts_across_distributors(
            parts,
            DummyMouserClient(),
            digikey_client=None,
            ti_client=None,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=0.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
            run_started_at=0.0,
            nxp_client=DummyNXPClient(),
        )

        assert len(results) == 2
        output = capsys.readouterr().out
        assert (
            output.count(
                "info: NXP direct disabled for this run; continuing without NXP direct pricing"
            )
            == 1
        )

    def test_delay_is_applied_when_mouser_uses_live_network(self, monkeypatch):
        parts = [
            AggregatedPart(
                part_number="PART1",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=100,
            ),
            AggregatedPart(
                part_number="PART2",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=100,
            ),
        ]

        class DummyMouserClient:
            def __init__(self):
                self.network_requests = 0

        mouser_client = DummyMouserClient()

        def fake_price_mouser_part(agg, client, *_args, **_kwargs):
            client.network_requests += 1
            priced = PricedPart.from_aggregated(agg)
            offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number=f"595-{agg.part_number}",
                manufacturer_part_number=agg.part_number,
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [offer]
            priced.apply_selected_offer(offer)
            return priced

        sleep_calls: list[float] = []

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)
        monkeypatch.setattr(main.time, "sleep", lambda seconds: sleep_calls.append(seconds))

        results = main._price_parts_across_distributors(
            parts,
            mouser_client,
            digikey_client=None,
            ti_client=None,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=1.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 2
        assert sleep_calls == [1.0]

    def test_delay_is_not_applied_for_non_paced_mouser_auxiliary_fetches(self, monkeypatch):
        parts = [
            AggregatedPart(
                part_number="PART1",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=100,
            ),
            AggregatedPart(
                part_number="PART2",
                manufacturer="TI",
                quantity_per_unit=1,
                total_quantity=100,
            ),
        ]

        class DummyMouserClient:
            def __init__(self):
                self.network_requests = 0
                self.paced_network_requests = 0

        mouser_client = DummyMouserClient()

        def fake_price_mouser_part(agg, client, *_args, **_kwargs):
            client.network_requests += 1
            priced = PricedPart.from_aggregated(agg)
            offer = DistributorOffer(
                distributor="Mouser",
                distributor_part_number=f"595-{agg.part_number}",
                manufacturer_part_number=agg.part_number,
                unit_price=1.0,
                extended_price=100.0,
                currency="EUR",
                match_method=MatchMethod.EXACT,
            )
            priced.offers = [offer]
            priced.apply_selected_offer(offer)
            return priced

        sleep_calls: list[float] = []

        monkeypatch.setattr(main, "price_mouser_part", fake_price_mouser_part)
        monkeypatch.setattr(main, "convert_offers_currency", lambda offers, *_: offers)
        monkeypatch.setattr(main.time, "sleep", lambda seconds: sleep_calls.append(seconds))

        results = main._price_parts_across_distributors(
            parts,
            mouser_client,
            digikey_client=None,
            ti_client=None,
            fx_rate_provider=object(),
            comparison_currency="EUR",
            delay=1.0,
            interactive=False,
            resolution_store=object(),
            ai_resolver=None,
        )

        assert len(results) == 2
        assert sleep_calls == []
