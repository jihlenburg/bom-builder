"""Top-level Textual application for BOM Builder's interactive TUI.

The :class:`BomBuilderApp` composes a full-screen terminal interface with:

- A header showing the project name and run parameters.
- A scrollable :class:`~tui.widgets.PartsTable` that fills with rows as
  each BOM line is priced.
- A :class:`~tui.widgets.CostPanel` showing running cost totals.
- A :class:`~tui.widgets.StatusBar` indicating the current operation.

The pricing pipeline runs in a background worker thread (see
:mod:`tui.worker`).  All UI mutations happen in response to strongly-typed
``Message`` events posted from that thread.

When the worker requests interactive resolution, the app pushes a
:class:`~tui.resolver_modal.ResolverModal` screen that blocks the worker
until the user chooses a candidate.

After all parts are priced, the app writes the report file and shows a
completion summary before allowing the user to quit.

Thread-safety model
-------------------
The worker thread and the Textual event loop coordinate through three
primitives:

1. ``app.post_message()`` — thread-safe by Textual's contract.
2. ``app.shutdown_event`` — a :class:`threading.Event` checked by the
   worker at iteration boundaries.
3. ``app.active_rendezvous`` — the currently outstanding
   :class:`~tui.events.ResolverRendezvous`, if any.  On shutdown the app
   calls ``rendezvous.cancel()`` which raises ``CancelledError`` in the
   blocked worker thread, waking it instantly.  The worker sets this
   attribute to ``None`` after each resolution completes.
"""

from __future__ import annotations

import argparse
import asyncio
import threading

from textual.app import App, ComposeResult
from textual.binding import Binding
from textual.widgets import Header, Footer

from models import AggregatedPart, BomSummary, PricedPart
from main import resolve_output_format, write_report

from tui.events import (
    PartPricingCompleted,
    PartPricingStarted,
    PricingRunCompleted,
    PricingRunFailed,
    ResolverRendezvous,
    ResolverRequest,
)
from tui.resolver_modal import ResolverModal
from tui.widgets import CostPanel, PartsTable, StatusBar


class BomBuilderApp(App):
    """Full-screen Textual application for interactive BOM pricing.

    Attributes
    ----------
    shutdown_event:
        Set when the app begins shutting down.  The worker thread checks
        this at every iteration boundary.
    active_rendezvous:
        The :class:`ResolverRendezvous` the worker is currently blocked
        on, or ``None``.  Written by the worker thread, read (and
        cancelled) by the UI thread on shutdown.  The ``None`` ↔
        rendezvous transitions are safe because the worker is the sole
        writer and the UI thread only reads/cancels during shutdown
        (after ``shutdown_event`` is set, so no new rendezvous will be
        created).

    Parameters
    ----------
    aggregated:
        Pre-computed aggregated BOM lines ready for pricing.
    args:
        Parsed CLI arguments controlling API keys, output format, etc.
    """

    TITLE = "BOM Builder"
    SUB_TITLE = "Interactive Pricing"

    CSS = """
    Screen {
        layout: vertical;
    }

    #parts-table {
        height: 1fr;
    }
    """

    BINDINGS = [
        Binding("q", "quit", "Quit", show=True),
        Binding("ctrl+c", "quit", "Quit", show=False),
    ]

    def __init__(
        self,
        aggregated: list[AggregatedPart],
        args: argparse.Namespace,
    ) -> None:
        super().__init__()
        self._aggregated = aggregated
        self._args = args
        self._results: list[PricedPart] = []
        self._summary: BomSummary | None = None
        self._run_complete = False
        self.shutdown_event = threading.Event()
        self.active_rendezvous: ResolverRendezvous | None = None

    def compose(self) -> ComposeResult:
        """Build the screen layout."""
        yield Header()
        yield PartsTable(id="parts-table")
        yield CostPanel()
        yield StatusBar()
        yield Footer()

    def on_mount(self) -> None:
        """Start the pricing worker when the app mounts."""
        total = len(self._aggregated)
        units = self._args.units
        self.sub_title = f"{total} parts, {units:,} units"

        cost_panel = self.query_one(CostPanel)
        cost_panel.set_run_params(total_parts=total, units=units)

        status_bar = self.query_one(StatusBar)
        status_bar.set_status(f"Starting pricing run for {total} parts...")

        # Launch the pricing pipeline in a worker thread.
        from tui.worker import run_pricing_pipeline
        self.run_worker(
            lambda: run_pricing_pipeline(self, self._aggregated, self._args),
            thread=True,
            exclusive=True,
        )

    async def action_quit(self) -> None:
        """Shut down the worker thread cleanly, then exit the app.

        The shutdown sequence is:

        1. Set ``shutdown_event`` so the worker stops starting new parts.
        2. Cancel the active rendezvous (if any) so the worker unblocks
           from ``rendezvous.wait()`` with ``CancelledError`` instead of
           hanging forever.
        3. Wait briefly for the worker thread to notice the cancellation
           and exit.  This is critical — if we call ``exit()`` while the
           worker is still running, Textual may restore the terminal
           (disabling mouse tracking, exiting the alternate screen)
           before the worker has finished, causing raw escape sequences
           to leak to the shell.
        4. Call ``app.exit()`` to begin Textual's teardown.
        """
        self.shutdown_event.set()
        rendezvous = self.active_rendezvous
        if rendezvous is not None:
            rendezvous.cancel()
        # Give the worker thread time to see shutdown_event / is_cancelled,
        # break out of the pricing loop, and close HTTP clients via
        # ExitStack.  Textual's event loop stays responsive during this
        # await — no visible freeze.
        await asyncio.sleep(0.3)
        self.exit(return_code=130)

    # --- Message handlers ---

    def on_part_pricing_started(self, event: PartPricingStarted) -> None:
        """Handle the start of pricing for one BOM line."""
        table = self.query_one(PartsTable)
        table.add_pending_row(
            event.index,
            event.part.part_number,
            event.part.manufacturer,
        )
        status_bar = self.query_one(StatusBar)
        status_bar.set_status(
            f"Looking up {event.index}/{event.total}: {event.part.part_number}..."
        )

    def on_part_pricing_completed(self, event: PartPricingCompleted) -> None:
        """Handle the completion of pricing for one BOM line."""
        table = self.query_one(PartsTable)
        table.update_priced_row(event.index, event.priced)

        cost_panel = self.query_one(CostPanel)
        cost_panel.record_part(event.priced)

        status_bar = self.query_one(StatusBar)
        timing = f" ({event.duration:.1f}s)" if event.duration > 0 else ""
        status_bar.set_status(
            f"Completed {event.index}/{event.total}: "
            f"{event.priced.part_number}{timing}"
        )

    def on_pricing_run_completed(self, event: PricingRunCompleted) -> None:
        """Handle the successful completion of the entire pricing run."""
        self._results = event.parts
        self._summary = event.summary
        self._run_complete = True

        # Write the report file.
        fmt, output = resolve_output_format(self._args)
        write_report(self._results, fmt, output, self._summary)

        # Update the UI to show completion state.
        cost_panel = self.query_one(CostPanel)
        cost_panel.show_final(
            event.summary.total_cost,
            event.summary.cost_per_unit,
            event.summary.currency,
        )
        status_bar = self.query_one(StatusBar)
        status_bar.set_status(
            f"Run complete. Report written to {output}. Press [q] to exit."
        )

    def on_pricing_run_failed(self, event: PricingRunFailed) -> None:
        """Handle a fatal error during the pricing run."""
        status_bar = self.query_one(StatusBar)
        status_bar.set_status(f"ERROR: {event.error}")

    def on_resolver_request(self, event: ResolverRequest) -> None:
        """Handle an interactive resolution request from the worker."""
        status_bar = self.query_one(StatusBar)
        status_bar.set_status(
            f"Awaiting resolution: {event.part.part_number} "
            f"({len(event.candidates)} candidates)"
        )
        self.push_screen(ResolverModal(event))
