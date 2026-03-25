"""Command-line orchestration for the BOM Builder application.

This module wires together the whole runtime pipeline:

1. parse CLI arguments
2. load and aggregate design files
3. resolve pricing and ambiguity handling
4. write the selected report format
5. print the human-readable summary

It deliberately keeps business logic in the dedicated runtime modules so the
CLI layer stays focused on argument handling, flow control, and user-facing
messages.
"""

import argparse
from contextlib import nullcontext
from datetime import datetime
import os
import shlex
import sys
import time
from collections import Counter
from pathlib import Path
from typing import Callable

from ai_resolver import DEFAULT_AI_MODEL, OpenAIResolver
from bom import aggregate_parts, load_design
from config import (
    DEFAULT_ATTRITION,
    install_console_trace,
    resolve_trace_path,
    setup_logging,
)
from digikey import DigiKeyClient, digikey_is_configured, price_part_via_digikey
from fx import FXRateProvider, convert_offers_currency, resolve_target_currency
from models import (
    AggregatedPart,
    BomSummary,
    Design,
    DistributorOffer,
    MatchMethod,
    Part,
    PricedPart,
)
from mouser import MouserClient, price_part as price_mouser_part
from report import write_csv, write_excel, write_json
from resolution_store import ResolutionStore
from ti import TIClient, price_part_via_ti, ti_is_configured, ti_supports_manufacturer

FORMAT_EXTENSIONS = {"csv": ".csv", "excel": ".xlsx", "json": ".json"}
WRITERS: dict[str, Callable[[list[PricedPart], Path, BomSummary], None]] = {
    "csv": write_csv,
    "excel": write_excel,
    "json": write_json,
}


def _positive_int(value: str) -> int:
    """Argparse converter enforcing integers greater than or equal to one."""
    n = int(value)
    if n < 1:
        raise argparse.ArgumentTypeError(f"must be >= 1, got {n}")
    return n


def _non_negative_float(value: str) -> float:
    """Argparse converter enforcing floats greater than or equal to zero."""
    f = float(value)
    if f < 0:
        raise argparse.ArgumentTypeError(f"must be >= 0, got {f}")
    return f


def _probability_float(value: str) -> float:
    """Argparse converter enforcing a probability in the inclusive range ``[0, 1]``."""
    f = float(value)
    if not 0 <= f <= 1:
        raise argparse.ArgumentTypeError(f"must be between 0 and 1, got {f}")
    return f


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    """Parse the CLI arguments for a BOM build run.

    Returns
    -------
    argparse.Namespace
        Parsed command-line arguments ready to pass into :func:`run`.
    """
    parser = argparse.ArgumentParser(
        description="Build and price an eBOM from design JSON files across supported distributors."
    )
    input_group = parser.add_mutually_exclusive_group(required=True)
    input_group.add_argument(
        "--design", "-d",
        nargs="+", type=Path,
        help="Path(s) to design JSON file(s)",
    )
    input_group.add_argument(
        "--part-number",
        type=str,
        help="Directly look up one manufacturer part number without a design JSON file",
    )
    parser.add_argument(
        "--manufacturer",
        type=str,
        default="",
        help="Manufacturer name for --part-number mode",
    )
    parser.add_argument(
        "--quantity-per-unit",
        type=_positive_int,
        default=1,
        help="Quantity per finished unit for --part-number mode (default: 1)",
    )
    parser.add_argument(
        "--description",
        type=str,
        default="",
        help="Optional description hint for --part-number mode",
    )
    parser.add_argument(
        "--package",
        type=str,
        default="",
        help="Optional package hint for --part-number mode",
    )
    parser.add_argument(
        "--pins",
        type=_positive_int,
        default=None,
        help="Optional pin-count hint for --part-number mode",
    )
    parser.add_argument(
        "--units", "-u",
        type=_positive_int, required=True,
        help="Number of units to build (must be >= 1)",
    )
    parser.add_argument(
        "--attrition", "-a",
        type=_non_negative_float, default=DEFAULT_ATTRITION,
        help="Attrition factor, e.g. 0.02 for 2%% (default: 0)",
    )
    parser.add_argument(
        "--format", "-f",
        choices=["csv", "excel", "json"], default=None,
        help="Output format (auto-detected from --output extension if not set)",
    )
    parser.add_argument(
        "--output", "-o",
        type=Path, default=None,
        help="Output file path (default: bom_output.<format>)",
    )
    parser.add_argument(
        "--api-key",
        type=str, default="",
        help="Mouser API key (overrides MOUSER_API_KEY / .env)",
    )
    parser.add_argument(
        "--delay",
        type=_non_negative_float, default=1.0,
        help="Delay between API requests in seconds (default: 1.0)",
    )
    parser.add_argument(
        "--trace-file",
        type=Path,
        default=None,
        help="Optional file path that captures this run's stdout/stderr transcript",
    )
    parser.add_argument(
        "--cache-ttl-hours",
        type=_non_negative_float, default=24.0,
        help="Retention for cached Mouser search results in hours (default: 24)",
    )
    parser.add_argument(
        "--no-cache",
        action="store_true",
        help="Disable the persistent Mouser search cache",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Aggregate BOM without calling the Mouser API",
    )
    parser.add_argument(
        "--verbose", "-v",
        action="store_true",
        help="Write full diagnostic trace output to stdout",
    )
    parser.add_argument(
        "--interactive",
        action="store_true",
        help="Prompt for manual candidate selection on unresolved or ambiguous parts",
    )
    parser.add_argument(
        "--ai-resolve",
        action="store_true",
        help="Use OpenAI to rerank still-ambiguous candidates before prompting",
    )
    parser.add_argument(
        "--ai-model",
        type=str,
        default=DEFAULT_AI_MODEL,
        help=f"OpenAI model for --ai-resolve (default: {DEFAULT_AI_MODEL})",
    )
    parser.add_argument(
        "--ai-confidence-threshold",
        type=_probability_float,
        default=0.85,
        help="Minimum AI confidence required to auto-accept a reranked candidate (default: 0.85)",
    )
    args = parser.parse_args(argv)

    single_part_fields = {
        "--manufacturer": args.manufacturer,
        "--quantity-per-unit": args.quantity_per_unit if args.quantity_per_unit != 1 else "",
        "--description": args.description,
        "--package": args.package,
        "--pins": args.pins if args.pins is not None else "",
    }
    if args.part_number:
        if not args.manufacturer.strip():
            parser.error("--manufacturer is required with --part-number")
    else:
        unexpected = [name for name, value in single_part_fields.items() if value]
        if unexpected:
            parser.error(f"{', '.join(unexpected)} require --part-number")

    return args


def resolve_output_format(args: argparse.Namespace) -> tuple[str, Path]:
    """Determine the effective output format and destination path.

    Parameters
    ----------
    args:
        Parsed CLI arguments.

    Returns
    -------
    tuple[str, Path]
        The selected format key and the final output path.
    """
    fmt = args.format
    if fmt is None and args.output is not None:
        ext_map = {v: k for k, v in FORMAT_EXTENSIONS.items()}
        fmt = ext_map.get(args.output.suffix)
        if fmt is None:
            print(
                f"Warning: unknown extension '{args.output.suffix}', defaulting to CSV",
                file=sys.stderr,
            )
            fmt = "csv"
    elif fmt is None:
        fmt = "csv"

    output = args.output or Path(f"bom_output{FORMAT_EXTENSIONS[fmt]}")
    return fmt, output


def _describe_tty(stream: object) -> str:
    """Return a compact description of a stream's TTY state for tracing."""
    checker = getattr(stream, "isatty", None)
    try:
        is_tty = bool(checker()) if callable(checker) else False
    except Exception:
        return "tty-state-unavailable"

    if not is_tty:
        return "not-a-tty"

    fileno = getattr(stream, "fileno", None)
    if callable(fileno):
        try:
            return os.ttyname(fileno())
        except Exception:
            return "tty"
    return "tty"


def _write_trace_header(
    trace_stream: object,
    *,
    fmt: str,
    output: Path,
    trace_path: Path,
) -> None:
    """Write one startup banner into the optional run transcript."""
    if not hasattr(trace_stream, "write"):
        return

    header_lines = [
        "=== BOM Builder Trace ===",
        f"Started: {datetime.now().astimezone().isoformat(timespec='seconds')}",
        f"PID: {os.getpid()}  PPID: {os.getppid()}",
        f"CWD: {Path.cwd()}",
        f"Command: {shlex.join(sys.argv)}",
        f"Trace File: {trace_path}",
        f"Output Format: {fmt}",
        f"Output Path: {output}",
        f"stdin: {_describe_tty(sys.stdin)}",
        f"stdout: {_describe_tty(sys.stdout)}",
        "Execution Mode: single-process, sequential part lookups",
        "",
    ]
    trace_stream.write("\n".join(header_lines))
    trace_stream.flush()


def load_designs(paths: list[Path]) -> list[Design]:
    """Load and validate all requested design files from disk.

    Missing files are treated as user-facing CLI errors and terminate the run
    via :class:`SystemExit`.
    """
    designs: list[Design] = []
    for path in paths:
        if not path.exists():
            print(f"Error: {path} not found", file=sys.stderr)
            raise SystemExit(1)
        print(f"Loading design: {path}")
        designs.append(load_design(path))
    return designs


def build_input_designs(args: argparse.Namespace) -> list[Design]:
    """Build the input design list from either files or direct CLI part data.

    Parameters
    ----------
    args:
        Parsed CLI arguments.

    Returns
    -------
    list[Design]
        One or more design objects ready for aggregation.

    Notes
    -----
    Single-part lookup mode is modeled as a synthetic one-line design so it
    automatically reuses the normal aggregation, attrition, pricing, reporting,
    and summary pipeline.
    """
    if args.design:
        return load_designs(args.design)

    print(f"Preparing direct lookup: {args.part_number} ({args.manufacturer})")
    return [
        Design(
            design="Direct lookup",
            parts=[
                Part(
                    part_number=args.part_number,
                    manufacturer=args.manufacturer,
                    quantity=args.quantity_per_unit,
                    description=args.description or None,
                    package=args.package or None,
                    pins=args.pins,
                )
            ],
        )
    ]


def price_parts(
    aggregated: list[AggregatedPart], args: argparse.Namespace
) -> list[PricedPart]:
    """Resolve pricing for aggregated BOM lines according to CLI options.

    In ``--dry-run`` mode the function returns placeholder
    :class:`PricedPart` instances without distributor calls. Otherwise it
    constructs the distributor clients, optional AI resolver, and the
    persistent manual-resolution store used by interactive runs.
    """
    if args.dry_run:
        print("\n  [dry-run] Skipping distributor API lookups")
        return [PricedPart.from_aggregated(agg) for agg in aggregated]

    print("\nLooking up distributor prices...")
    with MouserClient(
        api_key=args.api_key,
        cache_enabled=not args.no_cache,
        cache_ttl_seconds=int(args.cache_ttl_hours * 3600),
    ) as mouser_client:
        resolution_store = ResolutionStore()
        ai_context = (
            OpenAIResolver(
                model=args.ai_model,
                confidence_threshold=args.ai_confidence_threshold,
            )
            if args.ai_resolve
            else nullcontext(None)
        )
        with ai_context as ai_resolver:
            digikey_context = DigiKeyClient() if digikey_is_configured() else nullcontext(None)
            with digikey_context as digikey_client:
                ti_context = TIClient() if ti_is_configured() else nullcontext(None)
                with ti_context as ti_client:
                    with FXRateProvider() as fx_rate_provider:
                        return _price_parts_across_distributors(
                            aggregated,
                            mouser_client,
                            digikey_client=digikey_client,
                            ti_client=ti_client,
                            fx_rate_provider=fx_rate_provider,
                            comparison_currency=resolve_target_currency(),
                            delay=args.delay,
                            interactive=args.interactive,
                            resolution_store=resolution_store,
                            ai_resolver=ai_resolver,
                        )


def _price_parts_across_distributors(
    parts: list[AggregatedPart],
    mouser_client: MouserClient,
    *,
    digikey_client: DigiKeyClient | None,
    ti_client: TIClient | None,
    fx_rate_provider: FXRateProvider,
    comparison_currency: str,
    delay: float,
    interactive: bool,
    resolution_store: ResolutionStore,
    ai_resolver: OpenAIResolver | None,
) -> list[PricedPart]:
    """Resolve prices for each BOM line across all configured distributors."""
    results: list[PricedPart] = []
    total = len(parts)

    for i, agg in enumerate(parts, 1):
        print(f"  [{i}/{total}] Looking up {agg.part_number}...")
        before_requests = _network_request_count(mouser_client, digikey_client, ti_client)
        priced = price_mouser_part(
            agg,
            mouser_client,
            interactive=interactive,
            resolution_store=resolution_store,
            ai_resolver=ai_resolver,
        )
        if digikey_client is not None:
            priced.offers.append(
                price_part_via_digikey(
                    agg,
                    digikey_client,
                    query_terms=_digikey_query_terms(agg, priced),
                )
            )
        if ti_client is not None and ti_supports_manufacturer(agg.manufacturer):
            priced.offers.append(
                price_part_via_ti(
                    agg,
                    ti_client,
                    query_terms=_ti_query_terms(agg, priced),
                )
            )
        priced.offers = convert_offers_currency(
            priced.offers,
            comparison_currency,
            fx_rate_provider,
        )
        selected_offer = _select_preferred_offer(priced.offers)
        if selected_offer is not None:
            priced.apply_selected_offer(selected_offer)

        _print_lookup_status(priced)
        results.append(priced)

        used_network = (
            _network_request_count(mouser_client, digikey_client, ti_client)
            > before_requests
        )
        if i < total and delay > 0 and used_network:
            time.sleep(delay)

    return results


def _network_request_count(
    mouser_client: MouserClient,
    digikey_client: DigiKeyClient | None,
    ti_client: TIClient | None,
) -> int:
    """Return the combined tracked network requests across active distributors."""
    digikey_requests = 0 if digikey_client is None else digikey_client.network_requests
    ti_requests = 0 if ti_client is None else ti_client.network_requests
    return mouser_client.network_requests + digikey_requests + ti_requests


def _digikey_query_terms(agg: AggregatedPart, priced: PricedPart) -> list[str]:
    """Return the ordered Digi-Key lookup terms for one BOM line."""
    terms: list[str] = []
    if priced.manufacturer_part_number:
        terms.append(priced.manufacturer_part_number)
    if agg.part_number not in terms:
        terms.append(agg.part_number)
    return terms


def _ti_query_terms(agg: AggregatedPart, priced: PricedPart) -> list[str]:
    """Return the ordered TI lookup terms for one BOM line."""
    terms: list[str] = []
    if agg.part_number:
        terms.append(agg.part_number)
    if (
        priced.manufacturer_part_number
        and priced.manufacturer_part_number not in terms
    ):
        terms.append(priced.manufacturer_part_number)
    return terms


def _select_preferred_offer(
    offers: list[DistributorOffer],
) -> DistributorOffer | None:
    """Return the preferred distributor offer for one BOM line.

    The selector avoids trading confidence away for small price wins by
    preferring priced offers that do not require manual review. Price
    comparison then happens only within one comparable currency group.
    """
    if not offers:
        return None

    confident_priced = [offer for offer in offers if offer.is_priced and not offer.review_required]
    if confident_priced:
        return _select_by_price_in_currency_group(confident_priced)

    priced = [offer for offer in offers if offer.is_priced]
    if priced:
        return _select_by_price_in_currency_group(priced)

    non_review = [offer for offer in offers if not offer.review_required]
    return non_review[0] if non_review else offers[0]


def _select_by_price_in_currency_group(
    offers: list[DistributorOffer],
) -> DistributorOffer:
    """Return the cheapest offer within the chosen comparable currency group."""
    currencies = {offer.currency or "" for offer in offers}
    comparable = offers
    if len(currencies) > 1:
        primary_currency = next((offer.currency for offer in offers if offer.currency), "")
        comparable = [
            offer for offer in offers if (offer.currency or "") == (primary_currency or "")
        ] or offers

    return min(
        comparable,
        key=lambda offer: (
            float("inf") if offer.extended_price is None else offer.extended_price,
            offer.distributor.lower(),
        ),
    )


def _line_cost_per_unit(part: PricedPart, units: int) -> float | None:
    """Return the actual BOM-line cost contribution per finished unit."""
    if units <= 0 or part.extended_price is None:
        return None
    return part.extended_price / units


def _print_lookup_status(priced: PricedPart) -> None:
    """Print a compact buyer-facing status block for one priced part."""
    indent = " " * 11
    status = _lookup_status_label(priced)
    source = _lookup_source_label(priced)
    headline = _lookup_headline(priced)

    print(f"{indent}{status:<7} {source:<10} {headline}")

    note = _lookup_note(priced)
    if note:
        print(f"{indent}note: {note}")


def _lookup_status_label(priced: PricedPart) -> str:
    """Return the compact status keyword shown in live output."""
    if priced.review_required:
        return "REVIEW"
    if priced.is_priced or priced.distributor_part_number:
        return "OK"
    return "ERROR"


def _lookup_source_label(priced: PricedPart) -> str:
    """Return the compact source label shown in live output."""
    if priced.distributor == "TI":
        return "TI direct"
    if priced.distributor:
        return priced.distributor
    return "Lookup"


def _lookup_headline(priced: PricedPart) -> str:
    """Return the primary live-output line for one part."""
    distributor_pn = priced.distributor_part_number or "—"
    if priced.is_priced:
        details = [
            distributor_pn,
            _live_order_plan(priced),
            _format_unit_price(priced),
            _format_line_total(priced),
        ]
        return "   ".join(item for item in details if item)

    if priced.distributor_part_number:
        return f"{distributor_pn}   price unavailable"

    if priced.lookup_error:
        return _short_error_detail(priced.lookup_error)

    return "no match found"


def _compared_cheapest_note(priced: PricedPart) -> str | None:
    """Return a short note when the selected offer won a price comparison."""
    priced_offers = [offer for offer in priced.offers if offer.is_priced]
    if len(priced_offers) < 2 or not priced.distributor:
        return None

    selected_offer = next(
        (
            offer
            for offer in priced_offers
            if offer.distributor == priced.distributor
            and offer.distributor_part_number == priced.distributor_part_number
        ),
        None,
    )
    if selected_offer is None:
        return None

    comparable = [
        offer
        for offer in priced_offers
        if (offer.currency or "") == (selected_offer.currency or "")
    ] or priced_offers
    cheapest = min(
        comparable,
        key=lambda offer: float("inf") if offer.extended_price is None else offer.extended_price,
    )
    if (
        cheapest.distributor == selected_offer.distributor
        and cheapest.distributor_part_number == selected_offer.distributor_part_number
    ):
        return "cheapest source"
    return None


def _lookup_note(priced: PricedPart) -> str | None:
    """Return the secondary live-output note line for one part."""
    notes: list[str] = []
    match_note = _match_resolution_note(priced)
    if match_note:
        notes.append(match_note)

    cheapest_note = _compared_cheapest_note(priced)
    if cheapest_note:
        notes.append(cheapest_note)

    purchase_note = _purchase_selection_note(priced)
    if purchase_note:
        notes.append(purchase_note)

    lookup_note = _live_lookup_error_note(priced)
    if lookup_note:
        notes.append(lookup_note)

    return "; ".join(note for note in notes if note) or None


def _match_resolution_note(priced: PricedPart) -> str | None:
    """Return a short note describing how the part was matched."""
    if priced.resolution_source == "saved":
        return "saved resolution"
    if priced.resolution_source == "ai":
        return "AI-reranked match"
    if priced.resolution_source == "interactive":
        return "interactive selection"

    if priced.match_method == MatchMethod.EXACT:
        return "exact match"
    if priced.match_method == MatchMethod.BEGINS_WITH:
        candidate_note = (
            f" ({priced.match_candidates} candidates)"
            if priced.match_candidates and priced.match_candidates > 1
            else ""
        )
        return f"prefix match{candidate_note}"
    if priced.match_method == MatchMethod.FUZZY:
        candidate_note = (
            f" ({priced.match_candidates} candidates)"
            if priced.match_candidates and priced.match_candidates > 1
            else ""
        )
        return (
            f"manual review recommended{candidate_note}"
            if priced.review_required
            else f"fuzzy-resolved match{candidate_note}"
        )
    return None


def _purchase_selection_note(priced: PricedPart) -> str | None:
    """Return a short note describing the chosen buy quantity."""
    if priced.has_surplus_purchase:
        return (
            f"{priced.required_quantity:,} needed, "
            f"{priced.purchased_quantity:,} ordered, "
            f"{priced.surplus_quantity:,} spare"
        )
    return None


def _live_lookup_error_note(priced: PricedPart) -> str | None:
    """Return a concise error/detail note suitable for live console output."""
    if not priced.lookup_error:
        return None

    note = " ".join(segment.strip() for segment in priced.lookup_error.splitlines() if segment.strip())
    if not note:
        return None

    if not priced.is_priced and not priced.distributor_part_number:
        return None

    fragments = [fragment.strip() for fragment in note.split(";") if fragment.strip()]
    filtered = [
        fragment
        for fragment in fragments
        if not fragment.startswith("Fuzzy match:")
    ]
    if not filtered:
        return None
    return "; ".join(filtered)


def _live_order_plan(priced: PricedPart) -> str:
    """Return the concise order-plan fragment used in live output."""
    if priced.order_plan:
        return priced.order_plan

    quantity = priced.purchased_quantity or priced.required_quantity or priced.total_quantity
    packaging = _compact_packaging_label(priced.packaging_mode)
    if packaging:
        return f"{quantity:,} {packaging}"
    return f"{quantity:,} ordered"


def _compact_packaging_label(value: str | None) -> str:
    """Normalize verbose packaging labels into short buyer-facing text."""
    if not value:
        return ""

    compact = value.strip().lower()
    if compact.startswith("cut tape"):
        return "cut tape"
    if compact.startswith("mouse reel"):
        return "MouseReel"
    if compact.startswith("reel"):
        return "reel"
    if compact.startswith("tray"):
        return "tray"
    if compact.startswith("bulk"):
        return "bulk"
    return compact


def _format_unit_price(priced: PricedPart) -> str:
    """Return the unit price fragment for live output."""
    if priced.unit_price is None:
        return ""
    currency = priced.currency or ""
    return f"{priced.unit_price:.4f} {currency} ea".strip()


def _format_line_total(priced: PricedPart) -> str:
    """Return the extended line total fragment for live output."""
    if priced.extended_price is None:
        return ""
    currency = priced.currency or ""
    return f"{priced.extended_price:,.2f} {currency}".strip()


def _short_error_detail(detail: str, limit: int = 110) -> str:
    """Return a short single-line error detail for live console output."""
    text = " ".join(line.strip() for line in detail.splitlines() if line.strip())
    if len(text) <= limit:
        return text
    return f"{text[:limit - 1].rstrip()}…"


def write_report(parts: list[PricedPart], fmt: str, output: Path, summary: BomSummary) -> None:
    """Write the priced BOM using the selected report writer."""
    WRITERS[fmt](parts, output, summary)


def print_summary(parts: list[PricedPart], summary: BomSummary) -> None:
    """Print the console summary shown at the end of a run.

    The summary intentionally complements the machine-readable output file by
    surfacing resolver quality, missing-price cases, cost hotspots, quantity
    hotspots, manufacturer distribution, and package coverage in a single view.
    """
    sep = "=" * 60

    methods = Counter(_match_result_label(p) for p in parts)
    lookup_failures = [p for p in parts if p.match_method is None and p.lookup_error]
    not_found = [p for p in parts if p.match_method == MatchMethod.NOT_FOUND]
    no_price = [
        p for p in parts
        if p.match_method not in {None, MatchMethod.NOT_FOUND} and p.extended_price is None
    ]
    manufacturers = Counter(p.manufacturer for p in parts)
    distributors = Counter(p.distributor for p in parts if p.distributor)
    priced_parts = [p for p in parts if p.extended_price is not None]
    by_cost = sorted(
        priced_parts,
        key=lambda p: _line_cost_per_unit(p, summary.units) or 0.0,
        reverse=True,
    )
    by_qty = sorted(parts, key=lambda p: p.total_quantity, reverse=True)
    overbuy_parts = [p for p in by_cost if p.has_surplus_purchase]

    print(sep)
    print("  eBOM SUMMARY")
    print(sep)
    print()

    print(f"  Units to build:        {summary.units:>10,}")
    print(f"  Unique part numbers:   {summary.total_parts:>10,}")
    print(f"  Components per unit:   {summary.total_components_per_unit:>10,}")
    print(f"  Total components:      {summary.total_components_per_unit * summary.units:>10,}")
    print()

    cur = summary.currency
    print(f"  BOM cost per unit:     {summary.cost_per_unit:>10,.2f} {cur}")
    print()

    print("  Match results:")
    for label, count in methods.most_common():
        print(f"    {label:30s} {count:>4}")
    print()

    if not_found:
        print(f"  Parts not found ({len(not_found)}):")
        for p in not_found:
            print(f"    {p.part_number:30s} {p.manufacturer}")
        print()

    if lookup_failures:
        print(f"  Lookup failures ({len(lookup_failures)}):")
        for p in lookup_failures:
            detail = (p.lookup_error or "").splitlines()[0][:90]
            print(f"    {p.part_number:30s} → {detail}")
        print()

    if no_price:
        print(f"  Matched but no price ({len(no_price)}):")
        for p in no_price:
            print(f"    {p.part_number:30s} → {p.distributor_part_number or '—'}")
        print()

    if by_cost:
        print("  Top 10 by per-unit cost:")
        print(f"    {'Part Number':30s} {'Qty/Unit':>8s} {'Part Price':>12s} {'Per Unit':>12s}")
        print(f"    {'-' * 30} {'-' * 8} {'-' * 12} {'-' * 12}")
        for p in by_cost[:10]:
            per_unit_cost = _line_cost_per_unit(p, summary.units) or 0.0
            print(
                f"    {p.part_number:30s} {p.quantity_per_unit:>8,} "
                f"{(p.unit_price or 0.0):>12.4f} {per_unit_cost:>12.4f}"
            )
        print()

    if overbuy_parts:
        print("  Overbuy selections:")
        print(f"    {'Part Number':30s} {'Need':>8s} {'Buy':>8s} {'Spare':>8s} {'Strategy':18s}")
        print(f"    {'-' * 30} {'-' * 8} {'-' * 8} {'-' * 8} {'-' * 18}")
        for p in overbuy_parts[:10]:
            strategy = ", ".join(
                item
                for item in [
                    p.pricing_strategy,
                    p.packaging_mode,
                    p.package_type,
                    f"reel {p.full_reel_quantity}" if p.full_reel_quantity else "",
                ]
                if item
            ) or "—"
            print(
                f"    {p.part_number:30s} "
                f"{(p.required_quantity or 0):>8,} "
                f"{(p.purchased_quantity or 0):>8,} "
                f"{(p.surplus_quantity or 0):>8,} "
                f"{strategy[:18]:18}"
            )
        print()

    print("  Top 10 by total quantity:")
    print(f"    {'Part Number':30s} {'Qty/Unit':>8s} {'Total Qty':>10s}")
    print(f"    {'-' * 30} {'-' * 8} {'-' * 10}")
    for p in by_qty[:10]:
        print(f"    {p.part_number:30s} {p.quantity_per_unit:>8,} {p.total_quantity:>10,}")
    print()

    print(f"  Manufacturers ({len(manufacturers)}):")
    for mfr, count in manufacturers.most_common():
        print(f"    {mfr:30s} {count:>4} part(s)")
    print()

    if distributors:
        print(f"  Selected distributors ({len(distributors)}):")
        for distributor, count in distributors.most_common():
            print(f"    {distributor:30s} {count:>4} part(s)")
        print()

    with_pkg = [p for p in parts if p.package]
    if with_pkg:
        packages = Counter(p.package for p in with_pkg)
        without_pkg = summary.total_parts - len(with_pkg)
        print(f"  Packages ({len(with_pkg)} identified, {without_pkg} unknown):")
        for pkg, count in packages.most_common():
            print(f"    {pkg:30s} {count:>4} part(s)")
        print()

    print(sep)


def _match_result_label(part: PricedPart) -> str:
    """Return the user-facing match label used in summary statistics."""
    if part.match_method is None:
        if part.lookup_error:
            return "Lookup failed"
        return "unknown"
    if part.match_method == MatchMethod.FUZZY:
        if part.review_required:
            return "Fuzzy match (review!)"
        return "Fuzzy-resolved match"
    return part.match_method.display_name


def run(args: argparse.Namespace) -> int:
    """Execute one full BOM build run from parsed arguments.

    Parameters
    ----------
    args:
        Parsed CLI namespace, typically produced by :func:`parse_args`.

    Returns
    -------
    int
        Conventional process exit code. ``0`` means success and ``2`` is
        returned for argument/environment usage errors such as requesting
        interactive mode without a TTY.
    """
    trace_path = resolve_trace_path(getattr(args, "trace_file", None))
    with install_console_trace(trace_path) as trace_stream:
        fmt, output = resolve_output_format(args)
        if trace_stream is not None and trace_path is not None:
            _write_trace_header(
                trace_stream,
                fmt=fmt,
                output=output,
                trace_path=trace_path,
            )

        setup_logging(args.verbose)

        if trace_path is not None:
            print(f"Trace transcript: {trace_path}")

        if args.interactive and (not sys.stdin.isatty() or not sys.stdout.isatty()):
            print("Error: --interactive requires a TTY on stdin/stdout", file=sys.stderr)
            return 2

        designs = build_input_designs(args)

        print(f"\nAggregating for {args.units} units (attrition: {args.attrition:.1%})...")
        aggregated = aggregate_parts(designs, args.units, args.attrition)
        print(f"  {len(aggregated)} unique parts")

        priced = price_parts(aggregated, args)
        summary = BomSummary.from_parts(priced, args.units)

        print()
        write_report(priced, fmt, output, summary)

        print()
        print_summary(priced, summary)
        return 0


def main() -> None:
    """CLI entry point used by ``python main.py``."""
    raise SystemExit(run(parse_args()))


if __name__ == "__main__":
    main()
