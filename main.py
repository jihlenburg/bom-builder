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
import sys
from collections import Counter
from pathlib import Path
from typing import Callable

from ai_resolver import DEFAULT_AI_MODEL, OpenAIResolver
from bom import aggregate_parts, load_design
from config import DEFAULT_ATTRITION, setup_logging
from models import AggregatedPart, BomSummary, Design, MatchMethod, Part, PricedPart
from mouser import MouserClient, price_all_parts
from report import write_csv, write_excel, write_json
from resolution_store import ResolutionStore

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
        description="Build and price an eBOM from design JSON files using Mouser."
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
    constructs the Mouser client, optional AI resolver, and the persistent
    manual-resolution store used by interactive runs.
    """
    if args.dry_run:
        print("\n  [dry-run] Skipping Mouser API lookups")
        return [PricedPart.from_aggregated(agg) for agg in aggregated]

    print("\nLooking up prices on Mouser...")
    with MouserClient(
        api_key=args.api_key,
        cache_enabled=not args.no_cache,
        cache_ttl_seconds=int(args.cache_ttl_hours * 3600),
    ) as client:
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
            return price_all_parts(
                aggregated,
                client,
                delay=args.delay,
                interactive=args.interactive,
                resolution_store=resolution_store,
                ai_resolver=ai_resolver,
            )


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
    priced_parts = [p for p in parts if p.extended_price is not None]
    by_cost = sorted(priced_parts, key=lambda p: p.extended_price, reverse=True)
    by_qty = sorted(parts, key=lambda p: p.total_quantity, reverse=True)

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
    print(f"  Total BOM cost:        {summary.total_cost:>10,.2f} {cur}")
    print(f"  Cost per unit:         {summary.cost_per_unit:>10,.2f} {cur}")
    if summary.priced_count:
        avg = summary.total_cost / summary.priced_count
        print(f"  Avg cost per line:     {avg:>10,.2f} {cur}")
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
            print(f"    {p.part_number:30s} → {p.mouser_part_number or '—'}")
        print()

    if by_cost:
        print("  Top 10 by extended price:")
        print(f"    {'Part Number':30s} {'Qty':>8s} {'Unit':>10s} {'Extended':>12s}")
        print(f"    {'-' * 30} {'-' * 8} {'-' * 10} {'-' * 12}")
        for p in by_cost[:10]:
            print(
                f"    {p.part_number:30s} {p.total_quantity:>8,} "
                f"{p.unit_price:>10.4f} {p.extended_price:>12,.2f}"
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
    setup_logging(args.verbose)

    if args.interactive and (not sys.stdin.isatty() or not sys.stdout.isatty()):
        print("Error: --interactive requires a TTY on stdin/stdout", file=sys.stderr)
        return 2

    designs = build_input_designs(args)

    print(f"\nAggregating for {args.units} units (attrition: {args.attrition:.1%})...")
    aggregated = aggregate_parts(designs, args.units, args.attrition)
    print(f"  {len(aggregated)} unique parts")

    priced = price_parts(aggregated, args)
    summary = BomSummary.from_parts(priced, args.units)

    fmt, output = resolve_output_format(args)
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
