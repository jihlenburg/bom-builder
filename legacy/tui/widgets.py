"""Reusable Textual widgets for the BOM Builder TUI.

This module defines the visual building blocks of the TUI screen:

- :class:`PartsTable` — A scrollable ``DataTable`` showing each BOM line
  with live status updates as pricing progresses.
- :class:`CostPanel` — A compact cost/progress summary panel displayed
  at the bottom of the screen.
- :class:`StatusBar` — A thin status line showing the current operation
  and elapsed time.
"""

from __future__ import annotations

import time

from textual.app import ComposeResult
from textual.containers import Horizontal
from textual.widgets import DataTable, Footer, Header, Label, Static

from models import PricedPart


# ---------------------------------------------------------------------------
# Parts table — the main data display
# ---------------------------------------------------------------------------

class PartsTable(DataTable):
    """Live-updating table of BOM lines being priced.

    Columns are added at mount time. Rows are inserted when pricing starts
    for each part (showing "looking up...") and updated in-place when pricing
    completes with the resolved distributor data.
    """

    COLUMNS = (
        ("status", "Status", 8),
        ("part_number", "Part Number", 22),
        ("manufacturer", "Manufacturer", 16),
        ("distributor", "Source", 10),
        ("dist_pn", "Distributor PN", 22),
        ("qty", "Qty", 8),
        ("unit_price", "Unit Price", 12),
        ("ext_price", "Ext. Price", 12),
        ("plan", "Order Plan", 18),
        ("match", "Match", 14),
    )

    def on_mount(self) -> None:
        """Add columns when the widget is mounted into the DOM."""
        self.cursor_type = "row"
        self.zebra_stripes = True
        for key, label, width in self.COLUMNS:
            self.add_column(label, key=key, width=width)

    def add_pending_row(self, index: int, part_number: str, manufacturer: str) -> None:
        """Insert a placeholder row for a part that is being looked up.

        Parameters
        ----------
        index:
            One-based part index used as the row key.
        part_number:
            The BOM part number being looked up.
        manufacturer:
            The manufacturer name for display.
        """
        self.add_row(
            "...",
            part_number,
            manufacturer,
            "",
            "looking up...",
            "",
            "",
            "",
            "",
            "",
            key=str(index),
        )
        # Scroll to keep the latest row visible.
        self.scroll_end(animate=False)

    def update_priced_row(self, index: int, priced: PricedPart) -> None:
        """Replace a pending row with the final pricing data.

        Parameters
        ----------
        index:
            One-based part index matching the row key.
        priced:
            The fully priced part record.
        """
        status = _status_label(priced)
        unit_price = f"{priced.unit_price:.4f}" if priced.unit_price is not None else ""
        ext_price = f"{priced.extended_price:,.2f}" if priced.extended_price is not None else ""
        qty = str(priced.purchased_quantity or priced.total_quantity)
        plan = priced.order_plan or ""
        match = priced.match_method.value if priced.match_method else ""
        dist = priced.distributor or ""
        dist_pn = priced.distributor_part_number or ""

        row_key = str(index)
        self.update_cell(row_key, "status", status)
        self.update_cell(row_key, "distributor", dist)
        self.update_cell(row_key, "dist_pn", dist_pn)
        self.update_cell(row_key, "qty", qty)
        self.update_cell(row_key, "unit_price", unit_price)
        self.update_cell(row_key, "ext_price", ext_price)
        self.update_cell(row_key, "plan", plan)
        self.update_cell(row_key, "match", match)


def _status_label(priced: PricedPart) -> str:
    """Return the compact status keyword for one priced part."""
    if priced.lookup_error and not priced.is_priced:
        return "ERROR"
    if priced.review_required:
        return "REVIEW"
    if priced.is_priced or priced.distributor_part_number:
        return "OK"
    return "ERROR"


# ---------------------------------------------------------------------------
# Cost panel — running totals and progress
# ---------------------------------------------------------------------------

class CostPanel(Static):
    """Compact cost summary panel shown below the parts table.

    Displays the running total cost, per-unit cost, progress fraction,
    and elapsed time. Updated incrementally as each part completes.
    """

    DEFAULT_CSS = """
    CostPanel {
        dock: bottom;
        height: 3;
        padding: 0 2;
        background: $surface;
        border-top: solid $primary;
    }
    """

    def __init__(self) -> None:
        super().__init__()
        self._total_cost: float = 0.0
        self._priced_count: int = 0
        self._error_count: int = 0
        self._total_parts: int = 0
        self._units: int = 0
        self._currency: str = ""
        self._start_time: float = time.perf_counter()

    def set_run_params(self, total_parts: int, units: int) -> None:
        """Configure the panel for a new pricing run.

        Parameters
        ----------
        total_parts:
            Total number of BOM lines to price.
        units:
            Number of units being built.
        """
        self._total_parts = total_parts
        self._units = units
        self._start_time = time.perf_counter()
        self._refresh_display()

    def record_part(self, priced: PricedPart) -> None:
        """Update running totals after one part completes.

        Parameters
        ----------
        priced:
            The just-completed priced part record.
        """
        if priced.is_priced:
            self._total_cost += priced.extended_price or 0.0
            self._priced_count += 1
            if not self._currency and priced.currency:
                self._currency = priced.currency
        if priced.lookup_error and not priced.is_priced:
            self._error_count += 1
        self._refresh_display()

    def show_final(self, total_cost: float, cost_per_unit: float, currency: str) -> None:
        """Display the final summary when the run completes.

        Parameters
        ----------
        total_cost:
            Final total BOM cost.
        cost_per_unit:
            Final per-unit cost.
        currency:
            Currency code for the totals.
        """
        self._total_cost = total_cost
        self._currency = currency
        elapsed = time.perf_counter() - self._start_time
        self.update(
            f" Total: {total_cost:,.2f} {currency}   "
            f"Per unit: {cost_per_unit:,.2f} {currency}   "
            f"Parts: {self._priced_count}/{self._total_parts}   "
            f"Errors: {self._error_count}   "
            f"Elapsed: {_format_elapsed(elapsed)}   "
            f"COMPLETE"
        )

    def _refresh_display(self) -> None:
        """Recompute and render the display text."""
        elapsed = time.perf_counter() - self._start_time
        done = self._priced_count + self._error_count
        per_unit = (
            self._total_cost / self._units
            if self._units > 0 and self._total_cost > 0
            else 0.0
        )
        cur = self._currency or "---"
        self.update(
            f" Total: {self._total_cost:,.2f} {cur}   "
            f"Per unit: {per_unit:,.2f} {cur}   "
            f"Progress: {done}/{self._total_parts}   "
            f"Errors: {self._error_count}   "
            f"Elapsed: {_format_elapsed(elapsed)}"
        )


# ---------------------------------------------------------------------------
# Status bar — current operation indicator
# ---------------------------------------------------------------------------

class StatusBar(Static):
    """Thin status line at the very bottom showing the current operation."""

    DEFAULT_CSS = """
    StatusBar {
        dock: bottom;
        height: 1;
        padding: 0 2;
        background: $primary;
        color: $text;
    }
    """

    def set_status(self, text: str) -> None:
        """Update the status line text.

        Parameters
        ----------
        text:
            The new status text to display.
        """
        self.update(f" {text}")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _format_elapsed(seconds: float) -> str:
    """Return a compact elapsed-time string like ``01:23`` or ``1:02:15``."""
    total_seconds = max(0, int(round(seconds)))
    minutes, secs = divmod(total_seconds, 60)
    hours, minutes = divmod(minutes, 60)
    if hours:
        return f"{hours:d}:{minutes:02d}:{secs:02d}"
    return f"{minutes:02d}:{secs:02d}"
