"""Writers for BOM output formats and shared presentation helpers.

The report layer is responsible only for formatting already-computed pricing
data. It does not recalculate totals or resolve parts. Each writer consumes the
same :class:`models.BomSummary` object so CSV, Excel, JSON, and console output
all agree on totals and error counts.
"""

import csv
import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from models import BomSummary, PricedPart

@dataclass(frozen=True)
class ColumnSpec:
    """Definition of one output column in tabular report formats.

    Attributes
    ----------
    header:
        Human-readable column title.
    accessor:
        Callable that extracts or formats the corresponding value from a
        :class:`PricedPart`.
    """

    header: str
    accessor: Callable[[PricedPart], Any]


# Column definitions — order here determines output column order.
COLUMNS: tuple[ColumnSpec, ...] = (
    ColumnSpec("Part Number", lambda p: p.part_number),
    ColumnSpec("Manufacturer", lambda p: p.manufacturer),
    ColumnSpec("Description", lambda p: p.description or ""),
    ColumnSpec("Package", lambda p: p.package or ""),
    ColumnSpec("Pins", lambda p: p.pins if p.pins is not None else ""),
    ColumnSpec("Reference", lambda p: p.reference or ""),
    ColumnSpec("Qty/Unit", lambda p: p.quantity_per_unit),
    ColumnSpec("Total Qty", lambda p: p.total_quantity),
    ColumnSpec("Price Break Qty", lambda p: p.price_break_quantity or ""),
    ColumnSpec("Unit Price", lambda p: f"{p.unit_price:.4f}" if p.unit_price is not None else ""),
    ColumnSpec("Extended Price", lambda p: f"{p.extended_price:.2f}" if p.extended_price is not None else ""),
    ColumnSpec("Currency", lambda p: p.currency or ""),
    ColumnSpec("Mouser PN", lambda p: p.mouser_part_number or ""),
    ColumnSpec("Match Method", lambda p: p.match_method.value if p.match_method else ""),
    ColumnSpec("Candidates", lambda p: p.match_candidates if p.match_candidates is not None else ""),
    ColumnSpec("Resolution Source", lambda p: p.resolution_source or ""),
    ColumnSpec("Availability", lambda p: p.availability or ""),
    ColumnSpec("Errors", lambda p: p.lookup_error or ""),
)

HEADER_ROW = [column.header for column in COLUMNS]
EXTENDED_PRICE_INDEX = next(
    i for i, column in enumerate(COLUMNS) if column.header == "Extended Price"
)


def _prepare_output_path(output: Path) -> None:
    """Ensure the destination directory exists before writing a report."""
    output.parent.mkdir(parents=True, exist_ok=True)


def _part_to_row(p: PricedPart) -> list:
    """Convert a priced part into one tabular output row."""
    return [column.accessor(p) for column in COLUMNS]


def _summary_row(label: str, value: str) -> list:
    """Create one footer row aligned with the extended-price column."""
    row = [label] + [""] * (len(COLUMNS) - 1)
    row[EXTENDED_PRICE_INDEX] = value
    return row


def _summary_rows(summary: BomSummary) -> list[list[str]]:
    """Return the footer rows shared by CSV and Excel outputs."""
    rows = [
        [],
        _summary_row("TOTAL COST", f"{summary.total_cost:.2f}"),
        _summary_row("COST PER UNIT", f"{summary.cost_per_unit:.2f}"),
        [f"UNIQUE PARTS: {summary.total_parts}"],
    ]
    if summary.error_count:
        rows.append([f"LOOKUP ERRORS: {summary.error_count}"])
    return rows


def _print_write_status(output: Path, summary: BomSummary) -> None:
    """Print the common post-write status message shown by all writers."""
    print(f"BOM written to {output}")
    print(f"  {summary.total_parts} unique parts, total cost: {summary.total_cost:.2f}")
    if summary.cost_per_unit > 0:
        print(f"  Cost per unit: {summary.cost_per_unit:.2f}")
    if summary.error_count:
        print(f"  {summary.error_count} part(s) had lookup errors")


def write_csv(
    parts: list[PricedPart], output: Path, summary: BomSummary
) -> None:
    """Write the priced BOM to CSV.

    Parameters
    ----------
    parts:
        Final priced part records to write.
    output:
        Destination file path.
    summary:
        Precomputed BOM summary shared across all writers.
    """
    _prepare_output_path(output)
    with output.open("w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(HEADER_ROW)

        for p in parts:
            writer.writerow(_part_to_row(p))

        for row in _summary_rows(summary):
            writer.writerow(row)

    _print_write_status(output, summary)


def write_excel(
    parts: list[PricedPart], output: Path, summary: BomSummary
) -> None:
    """Write the priced BOM to an Excel workbook.

    When :mod:`openpyxl` is unavailable, the function falls back to CSV output
    rather than failing the entire run.
    """
    try:
        from openpyxl import Workbook
        from openpyxl.styles import Font
    except ImportError:
        print("openpyxl not installed. Install with: pip install openpyxl")
        print("Falling back to CSV output.")
        write_csv(parts, output.with_suffix(".csv"), summary)
        return

    _prepare_output_path(output)
    wb = Workbook()
    ws = wb.active
    ws.title = "eBOM"

    ws.append(HEADER_ROW)
    for cell in ws[1]:
        cell.font = Font(bold=True)

    for p in parts:
        ws.append(_part_to_row(p))

    for row in _summary_rows(summary):
        ws.append(row)

    for col in ws.columns:
        max_len = max(len(str(cell.value or "")) for cell in col)
        ws.column_dimensions[col[0].column_letter].width = min(max_len + 2, 40)

    wb.save(output)
    _print_write_status(output, summary)


def write_json(
    parts: list[PricedPart], output: Path, summary: BomSummary
) -> None:
    """Write the priced BOM and summary metadata to JSON."""
    bom = {
        **summary.model_dump(),
        "parts": [p.model_dump(exclude_none=True) for p in parts],
    }

    _prepare_output_path(output)
    with output.open("w", encoding="utf-8") as f:
        json.dump(bom, f, indent=2, default=str)

    _print_write_status(output, summary)
