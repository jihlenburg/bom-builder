"""Contract tests for the canonical machine-first command line interface."""

from __future__ import annotations

import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import bom_builder_cli as cli
from models import DistributorOffer, MatchMethod, PricedPart


def _priced_part(*, review_required: bool = False) -> PricedPart:
    """Return one normalized part suitable for CLI contract tests."""
    offer = DistributorOffer(
        distributor="Digi-Key",
        distributor_part_number="P5555-ND",
        manufacturer_part_number="ECA-1VHG102",
        unit_price=0.5,
        extended_price=5.0,
        currency="EUR",
        required_quantity=10,
        purchased_quantity=10,
        surplus_quantity=0,
        match_method=MatchMethod.EXACT,
        review_required=review_required,
    )
    part = PricedPart(
        part_number="ECA-1VHG102",
        manufacturer="Panasonic Industry",
        quantity_per_unit=1,
        total_quantity=10,
        required_quantity=10,
        offers=[offer],
    )
    part.apply_selected_offer(offer)
    return part


class TestCanonicalParser:
    """Argument-surface tests for task-focused subcommands."""

    def test_help_lists_focused_subcommands(self, capsys):
        with pytest.raises(SystemExit) as excinfo:
            cli.build_parser().parse_args(["--help"])

        output = capsys.readouterr().out
        assert excinfo.value.code == 0
        assert "price" in output
        assert "lookup" in output
        assert "validate" in output
        assert "providers" in output
        assert "schema" in output

    def test_lookup_does_not_accept_api_keys_on_command_line(self):
        with pytest.raises(SystemExit):
            cli.build_parser().parse_args(
                [
                    "lookup",
                    "PART",
                    "--manufacturer",
                    "Vendor",
                    "--mouser-api-key",
                    "secret",
                ]
            )

    def test_capabilities_advertise_standalone_packaging(self, capsys):
        exit_code = cli.run_cli(["capabilities"])

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == 0
        assert payload["features"]["standalone_build"] is True
        assert payload["features"]["frozen_executable"] is False
        assert payload["features"]["ti_requires_system_curl"] is True
        assert payload["features"]["nxp_requires_system_browser"] is True
        assert "provider_configuration" not in payload
        assert "schemas" not in payload


class TestValidateCommand:
    """Validation command tests for file and stdin inputs."""

    def test_validates_file_and_emits_json_only(self, tmp_path, capsys):
        design = tmp_path / "design.json"
        design.write_text(
            json.dumps(
                {
                    "design": "Demo",
                    "parts": [
                        {
                            "part_number": "R1",
                            "manufacturer": "Yageo",
                            "quantity": 1,
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )

        exit_code = cli.run_cli(["validate", str(design)])

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == 0
        assert payload["status"] == "valid"
        assert payload["design_count"] == 1
        assert payload["part_count"] == 1

    def test_accepts_design_from_stdin(self, monkeypatch, capsys):
        monkeypatch.setattr(
            cli.sys,
            "stdin",
            __import__("io").StringIO(
                '{"design":"stdin","parts":[{"part_number":"R1",'
                '"manufacturer":"Yageo","quantity":2}]}'
            ),
        )

        exit_code = cli.run_cli(["validate", "-"])

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == 0
        assert payload["designs"] == ["stdin"]
        assert payload["part_count"] == 1

    def test_invalid_input_uses_stable_error_envelope(self, tmp_path, capsys):
        design = tmp_path / "broken.json"
        design.write_text('{"design":', encoding="utf-8")

        exit_code = cli.run_cli(["validate", str(design)])

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == cli.EXIT_INPUT
        assert payload["status"] == "failed"
        assert payload["errors"][0]["code"] == "INVALID_INPUT"


class TestPricingCommand:
    """Pricing command tests with the network engine replaced by a stub."""

    def test_lookup_emits_versioned_json_and_selected_providers(
        self,
        monkeypatch,
        capsys,
    ):
        observed = {}
        monkeypatch.setattr(
            cli,
            "_resolve_distributors",
            lambda *_args: ["digikey"],
        )

        def fake_price_parts(_aggregated, args, **_kwargs):
            observed["providers"] = args.providers
            return [_priced_part()]

        monkeypatch.setattr(cli.pricing_engine, "price_parts", fake_price_parts)

        exit_code = cli.run_cli(
            [
                "lookup",
                "ECA-1VHG102",
                "--manufacturer",
                "Panasonic Industry",
                "--quantity",
                "10",
                "--no-progress",
            ]
        )

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == 0
        assert payload["schema_version"] == cli.CLI_SCHEMA_VERSION
        assert payload["status"] == "complete"
        assert payload["run"]["selected_providers"] == ["digikey"]
        assert payload["providers"][0]["status"] == "ok"
        assert observed["providers"] == ["digikey"]

    def test_fail_on_review_returns_stable_incomplete_exit(
        self,
        monkeypatch,
        capsys,
    ):
        monkeypatch.setattr(cli, "_resolve_distributors", lambda *_args: ["digikey"])
        monkeypatch.setattr(
            cli.pricing_engine,
            "price_parts",
            lambda *_args, **_kwargs: [_priced_part(review_required=True)],
        )

        exit_code = cli.run_cli(
            [
                "lookup",
                "ECA-1VHG102",
                "--manufacturer",
                "Panasonic Industry",
                "--quantity",
                "10",
                "--fail-on",
                "review",
                "--no-progress",
            ]
        )

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == cli.EXIT_INCOMPLETE
        assert payload["exit_code"] == cli.EXIT_INCOMPLETE
        assert payload["status"] == "complete_with_review"
        assert payload["warnings"][0]["code"] == "REVIEW_REQUIRED"

    def test_csv_artifact_keeps_stdout_as_json(
        self,
        monkeypatch,
        tmp_path,
        capsys,
    ):
        output = tmp_path / "bom.csv"
        monkeypatch.setattr(cli, "_resolve_distributors", lambda *_args: ["digikey"])
        monkeypatch.setattr(
            cli.pricing_engine,
            "price_parts",
            lambda *_args, **_kwargs: [_priced_part()],
        )

        exit_code = cli.run_cli(
            [
                "lookup",
                "ECA-1VHG102",
                "--manufacturer",
                "Panasonic Industry",
                "--quantity",
                "10",
                "--format",
                "csv",
                "--output",
                str(output),
                "--no-progress",
            ]
        )

        payload = json.loads(capsys.readouterr().out)
        assert exit_code == 0
        assert payload["status"] == "complete"
        assert output.exists()


class TestSchemaCommand:
    """Schema discovery tests for agent-driven integrations."""

    @pytest.mark.parametrize("target", ["input", "output", "providers"])
    def test_emits_valid_json_schema(self, target, capsys):
        assert cli.run_cli(["schema", target]) == 0

        payload = json.loads(capsys.readouterr().out)
        assert payload["type"] == "object"


class TestDiscoveryAndMaintenance:
    """Capability and recoverable maintenance command tests."""

    def test_capabilities_reports_current_features(self, capsys):
        assert cli.run_cli(["capabilities"]) == 0

        payload = json.loads(capsys.readouterr().out)
        assert payload["version"]
        assert "price" in payload["commands"]
        assert payload["features"]["json_stdout"] is True
        assert payload["features"]["alternative_parts"] is False

    def test_full_capabilities_bundle_configuration_and_schemas(self, capsys):
        assert cli.run_cli(["capabilities", "--full"]) == 0

        payload = json.loads(capsys.readouterr().out)
        assert payload["provider_configuration"]["live"] is False
        assert len(payload["provider_configuration"]["providers"]) == 6
        assert payload["schemas"]["input"]["type"] == "object"
        assert payload["schemas"]["output"]["type"] == "object"
        assert payload["schemas"]["providers"]["type"] == "object"

    def test_cache_purge_previews_before_confirmation(
        self,
        monkeypatch,
        tmp_path,
        capsys,
    ):
        cache_path = tmp_path / "cache.sqlite3"
        cache_path.write_text("cache", encoding="utf-8")
        monkeypatch.setenv("BOM_BUILDER_CACHE_DB", str(cache_path))

        assert cli.run_cli(["cache", "purge"]) == 0
        preview = json.loads(capsys.readouterr().out)

        assert preview["status"] == "preview"
        assert preview["confirmed"] is False
        assert cache_path.exists()

        assert cli.run_cli(["cache", "purge", "--yes"]) == 0
        result = json.loads(capsys.readouterr().out)

        assert result["status"] == "ok"
        assert result["affected_count"] == 1
        assert not cache_path.exists()

    def test_resolution_remove_previews_exact_record(
        self,
        monkeypatch,
        tmp_path,
        capsys,
    ):
        resolution_path = tmp_path / "resolutions.json"
        monkeypatch.setenv(
            "BOM_BUILDER_RESOLUTIONS_FILE",
            str(resolution_path),
        )
        from resolution_store import ResolutionStore

        ResolutionStore(resolution_path).set(
            "TI",
            "TMP421-Q1",
            "595-TMP421AQDCNRQ1",
            "TMP421AQDCNRQ1",
        )

        assert (
            cli.run_cli(
                [
                    "resolutions",
                    "remove",
                    "TMP421-Q1",
                    "--manufacturer",
                    "TI",
                ]
            )
            == 0
        )
        preview = json.loads(capsys.readouterr().out)

        assert preview["status"] == "preview"
        assert preview["records"][0]["part_number"] == "TMP421-Q1"
        assert resolution_path.exists()

        assert (
            cli.run_cli(
                [
                    "resolutions",
                    "remove",
                    "TMP421-Q1",
                    "--manufacturer",
                    "TI",
                    "--yes",
                ]
            )
            == 0
        )
        result = json.loads(capsys.readouterr().out)

        assert result["affected_count"] == 1
