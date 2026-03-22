"""Tests for CLI helpers."""

import sys
from argparse import Namespace
from pathlib import Path
import types

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import main
from main import _match_result_label, build_input_designs, resolve_output_format
from models import MatchMethod, PricedPart


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
