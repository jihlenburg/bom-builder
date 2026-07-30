"""Canonical machine-first command-line interface for BOM Builder.

The CLI treats stdout as a protocol channel: successful commands emit one JSON
document and operational diagnostics go to stderr. CSV and Excel are optional
artifacts, while the versioned JSON envelope remains available to calling
agents for status, warnings, errors, and normalized part data.
"""

from __future__ import annotations

import argparse
from contextlib import redirect_stdout
from dataclasses import asdict
from datetime import UTC, datetime
import io
import json
from pathlib import Path
import sys
import time
import traceback
from typing import Any, Sequence
from uuid import uuid4

from pydantic import ValidationError

import main as pricing_engine
from bom import aggregate_parts
from cli_models import (
    CLI_SCHEMA_VERSION,
    CapabilitySchemas,
    CapabilitiesEnvelope,
    CliIssue,
    CommandErrorEnvelope,
    MaintenanceEnvelope,
    MaintenanceTarget,
    PricingResultEnvelope,
    ProviderHealthEnvelope,
    ProviderRunSummary,
    RunMetadata,
    ValidationResultEnvelope,
)
from config import DEFAULT_ATTRITION, PROJECT_VERSION, setup_logging
from lookup_cache import default_cache_db_path
from models import BomSummary, Design, PricedPart
from provider_health import (
    DISTRIBUTOR_NAMES,
    HEALTH_PROVIDER_NAMES,
    check_providers,
    configured_distributors,
    provider_configuration,
)
from report import write_csv, write_excel
from resolution_store import ResolutionStore, default_resolution_store_path

EXIT_OK = 0
EXIT_INPUT = 2
EXIT_INCOMPLETE = 3
EXIT_PROVIDER = 4
EXIT_INTERNAL = 5


class CliCommandError(RuntimeError):
    """Expected command failure with a stable code and process exit status."""

    def __init__(self, code: str, message: str, exit_code: int) -> None:
        super().__init__(message)
        self.code = code
        self.exit_code = exit_code


def build_parser() -> argparse.ArgumentParser:
    """Build and return the canonical ``bom-builder`` argument parser."""
    parser = argparse.ArgumentParser(
        prog="bom-builder",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        description=(
            "Price electronic BOMs through independently selectable providers. "
            "Stdout is always machine-readable JSON."
        ),
        epilog=(
            "Examples:\n"
            "  bom-builder providers list --pretty\n"
            "  bom-builder lookup TMP421AQDCNRQ1 --manufacturer TI --quantity 100\n"
            "  bom-builder price designs/board.json --units 1000\n"
            "  bom-builder capabilities --pretty"
        ),
    )
    parser.add_argument(
        "--version",
        action="version",
        version=f"%(prog)s {PROJECT_VERSION}",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    price_parser = subparsers.add_parser(
        "price",
        help="Price one or more design JSON documents",
        epilog=(
            "Example: bom-builder price designs/board.json --units 1000 "
            "--providers mouser,digikey"
        ),
    )
    price_parser.add_argument(
        "design",
        nargs="+",
        help="Design JSON path(s), or '-' to read one document from stdin",
    )
    price_parser.add_argument(
        "--units",
        type=_positive_int,
        required=True,
        help="Number of finished units to build",
    )
    price_parser.add_argument(
        "--attrition",
        type=_non_negative_float,
        default=DEFAULT_ATTRITION,
        help="Additional fractional demand, for example 0.02",
    )
    _add_pricing_options(price_parser)

    lookup_parser = subparsers.add_parser(
        "lookup",
        help="Price one manufacturer part number",
        epilog=(
            "Example: bom-builder lookup ECA-1VHG102 "
            "--manufacturer 'Panasonic Industry' --quantity 100 "
            "--providers digikey"
        ),
    )
    lookup_parser.add_argument("part_number", help="Manufacturer part number")
    lookup_parser.add_argument(
        "--manufacturer",
        required=True,
        help="Part manufacturer",
    )
    lookup_parser.add_argument(
        "--quantity",
        type=_positive_int,
        default=1,
        help="Required total quantity (default: 1)",
    )
    lookup_parser.add_argument("--description", default="", help="Description hint")
    lookup_parser.add_argument("--package", default="", help="Package hint")
    lookup_parser.add_argument("--pins", type=_positive_int, default=None)
    _add_pricing_options(lookup_parser)

    validate_parser = subparsers.add_parser(
        "validate",
        help="Validate design JSON without contacting providers",
    )
    validate_parser.add_argument(
        "design",
        nargs="+",
        help="Design JSON path(s), or '-' to read from stdin",
    )
    validate_parser.add_argument("--pretty", action="store_true")

    providers_parser = subparsers.add_parser(
        "providers",
        help="Inspect provider capabilities and health",
    )
    providers_subparsers = providers_parser.add_subparsers(
        dest="providers_command",
        required=True,
    )
    providers_list_parser = providers_subparsers.add_parser(
        "list",
        help="List provider configuration without network requests",
    )
    providers_list_parser.add_argument("--pretty", action="store_true")
    providers_check_parser = providers_subparsers.add_parser(
        "check",
        help="Check provider configuration or make bounded live requests",
    )
    providers_check_parser.add_argument(
        "--providers",
        default="all",
        help="Comma-separated provider names (default: all)",
    )
    providers_check_parser.add_argument(
        "--live",
        action="store_true",
        help="Make one representative request per configured provider",
    )
    providers_check_parser.add_argument("--pretty", action="store_true")

    schema_parser = subparsers.add_parser(
        "schema",
        help="Print JSON Schema for automation contracts",
    )
    schema_parser.add_argument("target", choices=["input", "output", "providers"])
    schema_parser.add_argument("--pretty", action="store_true")

    capabilities_parser = subparsers.add_parser(
        "capabilities",
        help="Print a machine-readable feature and command manifest",
    )
    capabilities_parser.add_argument(
        "--full",
        action="store_true",
        help="Include safe provider configuration and all public JSON Schemas",
    )
    capabilities_parser.add_argument("--pretty", action="store_true")

    cache_parser = subparsers.add_parser(
        "cache",
        help="Inspect or purge the provider response cache",
    )
    cache_subparsers = cache_parser.add_subparsers(
        dest="cache_command",
        required=True,
    )
    cache_status_parser = cache_subparsers.add_parser("status")
    cache_status_parser.add_argument("--pretty", action="store_true")
    cache_purge_parser = cache_subparsers.add_parser("purge")
    cache_purge_parser.add_argument(
        "--yes",
        action="store_true",
        help="Delete the exact targets shown by the preview",
    )
    cache_purge_parser.add_argument("--pretty", action="store_true")

    resolutions_parser = subparsers.add_parser(
        "resolutions",
        help="Inspect or remove saved manual part resolutions",
    )
    resolutions_subparsers = resolutions_parser.add_subparsers(
        dest="resolutions_command",
        required=True,
    )
    resolutions_list_parser = resolutions_subparsers.add_parser("list")
    resolutions_list_parser.add_argument("--pretty", action="store_true")
    resolutions_remove_parser = resolutions_subparsers.add_parser("remove")
    resolutions_remove_parser.add_argument("part_number")
    resolutions_remove_parser.add_argument("--manufacturer", required=True)
    resolutions_remove_parser.add_argument("--yes", action="store_true")
    resolutions_remove_parser.add_argument("--pretty", action="store_true")
    resolutions_purge_parser = resolutions_subparsers.add_parser("purge")
    resolutions_purge_parser.add_argument("--yes", action="store_true")
    resolutions_purge_parser.add_argument("--pretty", action="store_true")

    return parser


def _add_pricing_options(parser: argparse.ArgumentParser) -> None:
    """Add options shared by ``price`` and ``lookup`` commands."""
    parser.add_argument(
        "--providers",
        default="auto",
        help=(
            "Comma-separated distributors: mouser,digikey,ti,nxp. "
            "'auto' selects every locally configured distributor."
        ),
    )
    parser.add_argument(
        "--exclude-provider",
        action="append",
        default=[],
        help="Distributor to exclude; may be repeated or comma-separated",
    )
    parser.add_argument(
        "--format",
        choices=["json", "csv", "excel"],
        default="json",
        help="Primary artifact format (default: json)",
    )
    parser.add_argument(
        "--output",
        default="-",
        help="'-' for JSON on stdout, or an artifact path",
    )
    parser.add_argument(
        "--fail-on",
        choices=["never", "error", "review"],
        default="error",
        help="Completion condition that produces exit code 3",
    )
    parser.add_argument(
        "--allow-partial",
        action="store_true",
        help="Alias for --fail-on never",
    )
    parser.add_argument(
        "--fresh",
        action="store_true",
        help="Bypass persistent provider response caches",
    )
    parser.add_argument(
        "--cache-ttl-hours",
        type=_non_negative_float,
        default=24.0,
    )
    parser.add_argument(
        "--mouser-delay",
        type=_non_negative_float,
        default=1.0,
    )
    parser.add_argument(
        "--ai-resolve",
        action="store_true",
        help="Use the configured OpenAI reranker for ambiguous Mouser matches",
    )
    parser.add_argument(
        "--ai-model",
        default=pricing_engine.DEFAULT_AI_MODEL,
    )
    parser.add_argument(
        "--ai-confidence-threshold",
        type=_probability_float,
        default=0.85,
    )
    parser.add_argument(
        "--include-documents",
        action="store_true",
        help=(
            "Attach provider product URLs and discover datasheet links; "
            "Digi-Key may make one additional cache-backed detail request per part"
        ),
    )
    parser.add_argument("--verbose", action="store_true")
    parser.add_argument(
        "--no-progress",
        action="store_true",
        help="Suppress human progress diagnostics on stderr",
    )
    parser.add_argument("--pretty", action="store_true")


def _positive_int(value: str) -> int:
    """Parse a strictly positive integer for argparse."""
    parsed = int(value)
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be >= 1")
    return parsed


def _non_negative_float(value: str) -> float:
    """Parse a non-negative floating-point value for argparse."""
    parsed = float(value)
    if parsed < 0:
        raise argparse.ArgumentTypeError("must be >= 0")
    return parsed


def _probability_float(value: str) -> float:
    """Parse a floating-point probability in the inclusive range zero to one."""
    parsed = float(value)
    if not 0 <= parsed <= 1:
        raise argparse.ArgumentTypeError("must be between 0 and 1")
    return parsed


def run_cli(argv: Sequence[str] | None = None) -> int:
    """Parse and execute one canonical CLI command.

    Parameters
    ----------
    argv:
        Optional argument sequence excluding the executable name.

    Returns
    -------
    int
        Stable process exit code documented by the CLI contract.
    """
    parser = build_parser()
    args = parser.parse_args(list(argv) if argv is not None else None)
    try:
        if args.command in {"price", "lookup"}:
            return _run_pricing_command(args)
        if args.command == "validate":
            return _run_validate_command(args)
        if args.command == "providers":
            return _run_providers_command(args)
        if args.command == "schema":
            return _run_schema_command(args)
        if args.command == "capabilities":
            return _run_capabilities_command(args)
        if args.command == "cache":
            return _run_cache_command(args)
        if args.command == "resolutions":
            return _run_resolutions_command(args)
        raise CliCommandError(
            "UNKNOWN_COMMAND",
            f"Unsupported command: {args.command}",
            EXIT_INPUT,
        )
    except CliCommandError as exc:
        _emit_command_error(
            command=args.command,
            code=exc.code,
            message=str(exc),
            exit_code=exc.exit_code,
            pretty=getattr(args, "pretty", False),
        )
        return exc.exit_code
    except (FileNotFoundError, json.JSONDecodeError, ValidationError) as exc:
        _emit_command_error(
            command=args.command,
            code="INVALID_INPUT",
            message=_safe_input_error(exc),
            exit_code=EXIT_INPUT,
            pretty=getattr(args, "pretty", False),
        )
        return EXIT_INPUT
    except Exception as exc:
        if getattr(args, "verbose", False):
            traceback.print_exc(file=sys.stderr)
        _emit_command_error(
            command=args.command,
            code="INTERNAL_ERROR",
            message=type(exc).__name__,
            exit_code=EXIT_INTERNAL,
            pretty=getattr(args, "pretty", False),
        )
        return EXIT_INTERNAL


def _run_pricing_command(args: argparse.Namespace) -> int:
    """Execute ``price`` or ``lookup`` and emit one pricing envelope."""
    selected_providers = _resolve_distributors(
        args.providers,
        args.exclude_provider,
    )
    if args.ai_resolve and "mouser" not in selected_providers:
        raise CliCommandError(
            "AI_REQUIRES_MOUSER",
            "--ai-resolve requires Mouser to be selected",
            EXIT_INPUT,
        )
    if args.format in {"csv", "excel"} and args.output == "-":
        raise CliCommandError(
            "OUTPUT_PATH_REQUIRED",
            f"--format {args.format} requires --output PATH",
            EXIT_INPUT,
        )

    run_id = str(uuid4())
    started_at = datetime.now(UTC)
    started_clock = time.perf_counter()
    engine_args = _engine_namespace(args, selected_providers)
    diagnostic_target: Any = io.StringIO() if args.no_progress else sys.stderr
    setup_logging(args.verbose, stream=sys.stderr)

    with redirect_stdout(diagnostic_target):
        designs = (
            _load_design_inputs(args.design)
            if args.command == "price"
            else [_lookup_design(args)]
        )
        aggregated = aggregate_parts(
            designs,
            engine_args.units,
            engine_args.attrition,
        )
        priced = pricing_engine.price_parts(
            aggregated,
            engine_args,
            run_started_at=started_clock,
        )

    completed_at = datetime.now(UTC)
    fail_on = "never" if args.allow_partial else args.fail_on
    envelope = _build_pricing_envelope(
        command=args.command,
        run_id=run_id,
        started_at=started_at,
        completed_at=completed_at,
        duration_seconds=time.perf_counter() - started_clock,
        selected_providers=selected_providers,
        priced=priced,
        units=engine_args.units,
        fail_on=fail_on,
    )
    _write_optional_artifact(args, envelope)
    _emit_json(envelope.model_dump(mode="json", exclude_none=True), pretty=args.pretty)
    return envelope.exit_code


def _engine_namespace(
    args: argparse.Namespace,
    selected_providers: list[str],
) -> argparse.Namespace:
    """Translate canonical CLI arguments into the pricing engine namespace."""
    units = args.units if args.command == "price" else args.quantity
    attrition = args.attrition if args.command == "price" else 0.0
    return argparse.Namespace(
        units=units,
        attrition=attrition,
        dry_run=False,
        no_cache=args.fresh,
        cache_ttl_hours=args.cache_ttl_hours,
        mouser_api_key="",
        mouser_delay=args.mouser_delay,
        ai_resolve=args.ai_resolve,
        ai_model=args.ai_model,
        ai_confidence_threshold=args.ai_confidence_threshold,
        interactive=False,
        providers=selected_providers,
        include_documents=args.include_documents,
    )


def _lookup_design(args: argparse.Namespace) -> Design:
    """Build the synthetic one-line design used by ``lookup``."""
    return Design(
        design="Direct lookup",
        parts=[
            {
                "part_number": args.part_number,
                "manufacturer": args.manufacturer,
                "quantity": 1,
                "description": args.description or None,
                "package": args.package or None,
                "pins": args.pins,
            }
        ],
    )


def _load_design_inputs(sources: Sequence[str]) -> list[Design]:
    """Load design documents from paths and at most one stdin marker."""
    designs: list[Design] = []
    stdin_seen = False
    for source in sources:
        if source != "-":
            with Path(source).open(encoding="utf-8") as stream:
                designs.append(Design.model_validate(json.load(stream)))
            continue
        if stdin_seen:
            raise CliCommandError(
                "STDIN_REUSED",
                "The '-' design input may only be used once",
                EXIT_INPUT,
            )
        stdin_seen = True
        payload = json.load(sys.stdin)
        designs.extend(_designs_from_stdin_payload(payload))
    return designs


def _designs_from_stdin_payload(payload: Any) -> list[Design]:
    """Validate one supported stdin payload shape into design models."""
    if isinstance(payload, list):
        return [Design.model_validate(item) for item in payload]
    if isinstance(payload, dict) and isinstance(payload.get("designs"), list):
        return [Design.model_validate(item) for item in payload["designs"]]
    return [Design.model_validate(payload)]


def _resolve_distributors(
    raw: str,
    exclusions: Sequence[str],
) -> list[str]:
    """Resolve automatic or explicit distributor selection."""
    if raw.strip().lower() == "auto":
        selected = configured_distributors()
    else:
        selected = _parse_names(raw, DISTRIBUTOR_NAMES)
        configuration = provider_configuration()
        unavailable = [
            name for name in selected if not configuration[name].configured
        ]
        if unavailable:
            raise CliCommandError(
                "PROVIDER_NOT_CONFIGURED",
                "Selected provider(s) are not configured: "
                + ", ".join(unavailable),
                EXIT_PROVIDER,
            )

    excluded: set[str] = set()
    for value in exclusions:
        excluded.update(_parse_names(value, DISTRIBUTOR_NAMES))
    selected = [name for name in selected if name not in excluded]
    if not selected:
        raise CliCommandError(
            "NO_PROVIDERS",
            "Provider selection is empty",
            EXIT_INPUT,
        )
    return selected


def _parse_names(raw: str, allowed: Sequence[str]) -> list[str]:
    """Parse, validate, and deduplicate a comma-separated name list."""
    if raw.strip().lower() == "all":
        return list(allowed)
    names: list[str] = []
    for token in raw.split(","):
        name = token.strip().lower()
        if not name:
            continue
        if name not in allowed:
            raise CliCommandError(
                "UNKNOWN_PROVIDER",
                f"Unknown provider '{name}'. Allowed: {', '.join(allowed)}",
                EXIT_INPUT,
            )
        if name not in names:
            names.append(name)
    if not names:
        raise CliCommandError(
            "NO_PROVIDERS",
            "No provider names were supplied",
            EXIT_INPUT,
        )
    return names


def _build_pricing_envelope(
    *,
    command: str,
    run_id: str,
    started_at: datetime,
    completed_at: datetime,
    duration_seconds: float,
    selected_providers: list[str],
    priced: list[PricedPart],
    units: int,
    fail_on: str,
) -> PricingResultEnvelope:
    """Build the stable result envelope and apply strict completion policy."""
    summary = BomSummary.from_parts(priced, units)
    warnings, errors = _pricing_issues(priced)
    unpriced = [part for part in priced if not part.is_priced]
    reviews = [part for part in priced if part.review_required]

    if len(unpriced) == len(priced) and priced:
        status = "failed"
    elif unpriced:
        status = "partial"
    elif reviews or errors or warnings:
        status = "complete_with_review"
    else:
        status = "complete"

    incomplete = bool(errors or unpriced)
    review_required = bool(reviews)
    should_fail = (
        (fail_on == "error" and incomplete)
        or (fail_on == "review" and (incomplete or review_required))
    )
    exit_code = EXIT_INCOMPLETE if should_fail else EXIT_OK
    return PricingResultEnvelope(
        status=status,
        exit_code=exit_code,
        run=RunMetadata(
            run_id=run_id,
            command=command,
            started_at=started_at,
            completed_at=completed_at,
            duration_seconds=round(duration_seconds, 6),
            selected_providers=selected_providers,
        ),
        summary=summary,
        providers=_provider_run_summaries(priced, selected_providers),
        parts=priced,
        warnings=warnings,
        errors=errors,
    )


def _pricing_issues(
    parts: list[PricedPart],
) -> tuple[list[CliIssue], list[CliIssue]]:
    """Return deterministic warning and error lists for priced parts."""
    warnings: list[CliIssue] = []
    errors: list[CliIssue] = []
    for part in parts:
        if part.review_required:
            warnings.append(
                CliIssue(
                    code="REVIEW_REQUIRED",
                    message="Selected match requires engineering review",
                    part_number=part.part_number,
                    provider=_normalized_provider(part.distributor),
                )
            )
        if not part.is_priced:
            errors.append(
                CliIssue(
                    code="PART_UNPRICED",
                    message=part.lookup_error or "No selected provider returned usable pricing",
                    part_number=part.part_number,
                    provider=_normalized_provider(part.distributor),
                )
            )
        for offer in part.offers:
            if not offer.lookup_error:
                continue
            warnings.append(
                CliIssue(
                    code="PROVIDER_LOOKUP_FAILED",
                    message=offer.lookup_error,
                    part_number=part.part_number,
                    provider=_normalized_provider(offer.distributor),
                )
            )
    return (
        sorted(warnings, key=_cli_issue_sort_key),
        sorted(errors, key=_cli_issue_sort_key),
    )


def _cli_issue_sort_key(issue: CliIssue) -> tuple[str, str, str, str]:
    """Return the deterministic ordering key for one machine-facing issue."""
    return (
        issue.part_number or "",
        issue.provider or "",
        issue.code,
        issue.message,
    )


def _provider_run_summaries(
    parts: list[PricedPart],
    selected_providers: list[str],
) -> list[ProviderRunSummary]:
    """Summarize normalized offers returned by each selected provider."""
    summaries: list[ProviderRunSummary] = []
    for provider in selected_providers:
        applicable = [
            part
            for part in parts
            if _provider_applies(provider, part.manufacturer)
        ]
        offers = [
            offer
            for part in parts
            for offer in part.offers
            if _normalized_provider(offer.distributor) == provider
        ]
        priced_offers = [offer for offer in offers if offer.is_priced]
        errors = [offer for offer in offers if offer.lookup_error]
        if not applicable:
            status = "not_applicable"
        elif errors:
            status = "degraded"
        elif priced_offers:
            status = "ok"
        else:
            status = "no_results"
        summaries.append(
            ProviderRunSummary(
                name=provider,
                status=status,
                applicable_parts=len(applicable),
                offers_returned=len(offers),
                priced_offers=len(priced_offers),
                error_count=len(errors),
            )
        )
    return summaries


def _provider_applies(provider: str, manufacturer: str) -> bool:
    """Return whether a distributor applies to one manufacturer line."""
    if provider == "ti":
        return pricing_engine.ti_supports_manufacturer(manufacturer)
    if provider == "nxp":
        return pricing_engine.nxp_supports_manufacturer(manufacturer)
    return True


def _normalized_provider(display_name: str | None) -> str | None:
    """Normalize provider display labels into stable CLI identifiers."""
    if not display_name:
        return None
    normalized = display_name.strip().lower().replace("-", "").replace(" ", "")
    return {
        "mouser": "mouser",
        "digikey": "digikey",
        "ti": "ti",
        "nxp": "nxp",
    }.get(normalized, normalized)


def _write_optional_artifact(
    args: argparse.Namespace,
    envelope: PricingResultEnvelope,
) -> None:
    """Write an optional JSON, CSV, or Excel artifact without polluting stdout."""
    if args.format == "json":
        if args.output == "-":
            return
        path = Path(args.output)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            _json_text(
                envelope.model_dump(mode="json", exclude_none=True),
                pretty=args.pretty,
            ),
            encoding="utf-8",
        )
        return

    path = Path(args.output)
    with redirect_stdout(sys.stderr):
        if args.format == "csv":
            write_csv(envelope.parts, path, envelope.summary)
        else:
            write_excel(envelope.parts, path, envelope.summary)


def _run_validate_command(args: argparse.Namespace) -> int:
    """Validate design documents and emit a compact result envelope."""
    designs = _load_design_inputs(args.design)
    envelope = ValidationResultEnvelope(
        design_count=len(designs),
        part_count=sum(len(design.parts) for design in designs),
        designs=[design.design for design in designs],
    )
    _emit_json(envelope.model_dump(mode="json"), pretty=args.pretty)
    return EXIT_OK


def _run_providers_command(args: argparse.Namespace) -> int:
    """Execute provider configuration discovery or bounded live checks."""
    if args.providers_command == "list":
        envelope = check_providers(list(HEALTH_PROVIDER_NAMES), live=False)
    else:
        names = _parse_names(args.providers, HEALTH_PROVIDER_NAMES)
        envelope = check_providers(names, live=args.live)
    _emit_json(envelope.model_dump(mode="json", exclude_none=True), pretty=args.pretty)
    return envelope.exit_code


def _run_schema_command(args: argparse.Namespace) -> int:
    """Emit JSON Schema for one public automation contract."""
    if args.target == "input":
        schema = Design.model_json_schema()
    elif args.target == "output":
        schema = PricingResultEnvelope.model_json_schema()
    else:
        schema = ProviderHealthEnvelope.model_json_schema()
    _emit_json(schema, pretty=args.pretty)
    return EXIT_OK


def _run_capabilities_command(args: argparse.Namespace) -> int:
    """Emit the installed command and feature manifest."""
    provider_config = None
    schemas = None
    if args.full:
        provider_config = check_providers(list(HEALTH_PROVIDER_NAMES), live=False)
        schemas = CapabilitySchemas(
            input=Design.model_json_schema(),
            output=PricingResultEnvelope.model_json_schema(),
            providers=ProviderHealthEnvelope.model_json_schema(),
        )

    envelope = CapabilitiesEnvelope(
        schema_version=CLI_SCHEMA_VERSION,
        commands=[
            "price",
            "lookup",
            "validate",
            "providers list",
            "providers check",
            "cache status",
            "cache purge",
            "resolutions list",
            "resolutions remove",
            "resolutions purge",
            "schema input",
            "schema output",
            "schema providers",
            "capabilities",
        ],
        distributors=list(DISTRIBUTOR_NAMES),
        services=["ecb", "openai"],
        artifact_formats=["json", "csv", "excel"],
        fail_on_policies=["never", "error", "review"],
        features={
            "json_stdout": True,
            "stdin_designs": True,
            "provider_selection": True,
            "live_provider_health": True,
            "saved_resolutions": True,
            "interactive_resolution": False,
            "alternative_parts": False,
            "datasheet_downloads": False,
            "standalone_build": True,
            "frozen_executable": bool(getattr(sys, "frozen", False)),
            "ti_requires_system_curl": True,
            "nxp_requires_system_browser": True,
        },
        provider_configuration=provider_config,
        schemas=schemas,
    )
    _emit_json(
        envelope.model_dump(mode="json", exclude_none=True),
        pretty=args.pretty,
    )
    return EXIT_OK


def _run_cache_command(args: argparse.Namespace) -> int:
    """Inspect or explicitly delete provider cache files."""
    targets = _cache_targets()
    if args.cache_command == "status":
        envelope = MaintenanceEnvelope(
            status="ok",
            exit_code=EXIT_OK,
            resource="cache",
            action="status",
            confirmed=False,
            targets=[_maintenance_target(path) for path in targets],
        )
        _emit_json(envelope.model_dump(mode="json"), pretty=args.pretty)
        return EXIT_OK

    return _run_target_purge(
        resource="cache",
        action="purge",
        targets=targets,
        confirmed=args.yes,
        pretty=args.pretty,
    )


def _run_resolutions_command(args: argparse.Namespace) -> int:
    """Inspect, remove, or purge saved manual resolutions."""
    path = default_resolution_store_path()
    targets = [path, path.with_suffix(".tmp")]
    if args.resolutions_command == "list":
        store = ResolutionStore(path=path)
        records = [asdict(record) for record in store.list_records()]
        envelope = MaintenanceEnvelope(
            status="ok",
            exit_code=EXIT_OK,
            resource="resolutions",
            action="list",
            confirmed=False,
            targets=[_maintenance_target(target) for target in targets],
            records=records,
            affected_count=len(records),
        )
        _emit_json(envelope.model_dump(mode="json"), pretty=args.pretty)
        return EXIT_OK

    if args.resolutions_command == "remove":
        store = ResolutionStore(path=path)
        record = store.get(args.manufacturer, args.part_number)
        if not args.yes:
            envelope = MaintenanceEnvelope(
                status="preview",
                exit_code=EXIT_OK,
                resource="resolutions",
                action="remove",
                confirmed=False,
                targets=[_maintenance_target(target) for target in targets],
                records=[asdict(record)] if record is not None else [],
                affected_count=1 if record is not None else 0,
            )
            _emit_json(envelope.model_dump(mode="json"), pretty=args.pretty)
            return EXIT_OK
        removed = store.remove(args.manufacturer, args.part_number)
        envelope = MaintenanceEnvelope(
            status="ok",
            exit_code=EXIT_OK,
            resource="resolutions",
            action="remove",
            confirmed=True,
            targets=[_maintenance_target(target) for target in targets],
            affected_count=1 if removed else 0,
        )
        _emit_json(envelope.model_dump(mode="json"), pretty=args.pretty)
        return EXIT_OK

    return _run_target_purge(
        resource="resolutions",
        action="purge",
        targets=targets,
        confirmed=args.yes,
        pretty=args.pretty,
    )


def _cache_targets() -> list[Path]:
    """Return exact cache database and SQLite sidecar targets."""
    database = default_cache_db_path()
    return [
        database,
        Path(f"{database}-shm"),
        Path(f"{database}-wal"),
        Path(f"{database}-journal"),
    ]


def _maintenance_target(path: Path) -> MaintenanceTarget:
    """Return current metadata for one exact maintenance target."""
    exists = path.exists()
    size_bytes = path.stat().st_size if exists and path.is_file() else None
    return MaintenanceTarget(
        path=str(path),
        exists=exists,
        size_bytes=size_bytes,
    )


def _run_target_purge(
    *,
    resource: str,
    action: str,
    targets: list[Path],
    confirmed: bool,
    pretty: bool,
) -> int:
    """Preview or delete an exact, pre-resolved maintenance target list."""
    if not confirmed:
        envelope = MaintenanceEnvelope(
            status="preview",
            exit_code=EXIT_OK,
            resource=resource,
            action=action,
            confirmed=False,
            targets=[_maintenance_target(path) for path in targets],
        )
        _emit_json(envelope.model_dump(mode="json"), pretty=pretty)
        return EXIT_OK

    affected_count = 0
    errors: list[CliIssue] = []
    for path in targets:
        if not path.exists():
            continue
        try:
            path.unlink()
            affected_count += 1
        except OSError as exc:
            errors.append(
                CliIssue(
                    code="DELETE_FAILED",
                    message=f"{type(exc).__name__}: could not delete target",
                    details={"path": str(path)},
                )
            )
    exit_code = EXIT_INTERNAL if errors else EXIT_OK
    envelope = MaintenanceEnvelope(
        status="failed" if errors else "ok",
        exit_code=exit_code,
        resource=resource,
        action=action,
        confirmed=True,
        targets=[_maintenance_target(path) for path in targets],
        affected_count=affected_count,
        errors=errors,
    )
    _emit_json(envelope.model_dump(mode="json"), pretty=pretty)
    return exit_code


def _safe_input_error(exc: Exception) -> str:
    """Return useful input diagnostics without leaking unrelated runtime state."""
    if isinstance(exc, FileNotFoundError):
        return f"Design file not found: {exc.filename}"
    if isinstance(exc, json.JSONDecodeError):
        return f"Invalid JSON at line {exc.lineno}, column {exc.colno}"
    if isinstance(exc, ValidationError):
        first = exc.errors()[0]
        location = ".".join(str(token) for token in first.get("loc", ()))
        return f"{location}: {first.get('msg', 'validation failed')}".strip(": ")
    return type(exc).__name__


def _emit_command_error(
    *,
    command: str,
    code: str,
    message: str,
    exit_code: int,
    pretty: bool,
) -> None:
    """Emit one stable command-level JSON error envelope."""
    envelope = CommandErrorEnvelope(
        exit_code=exit_code,
        command=command,
        errors=[CliIssue(code=code, message=message)],
    )
    _emit_json(envelope.model_dump(mode="json"), pretty=pretty)


def _json_text(payload: Any, *, pretty: bool) -> str:
    """Serialize one JSON value with deterministic key ordering."""
    return json.dumps(
        payload,
        indent=2 if pretty else None,
        sort_keys=True,
        separators=None if pretty else (",", ":"),
    )


def _emit_json(payload: Any, *, pretty: bool) -> None:
    """Write exactly one JSON document to stdout."""
    sys.stdout.write(_json_text(payload, pretty=pretty))
    sys.stdout.write("\n")
    sys.stdout.flush()


def main(argv: Sequence[str] | None = None) -> None:
    """Console-script entry point."""
    raise SystemExit(run_cli(argv))


if __name__ == "__main__":
    main()
