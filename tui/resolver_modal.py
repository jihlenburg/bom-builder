"""Modal screen for interactive candidate resolution in the TUI.

When the Mouser lookup pipeline produces an ambiguous or review-required
match, the TUI shows this full-screen modal instead of the CLI's text-based
prompt.  The user can browse candidates in a table, accept the suggested
match, pick a specific candidate by row, skip the part, or quit the run.

The modal communicates back to the worker thread exclusively through the
:class:`~tui.events.ResolverRendezvous` carried on the
:class:`~tui.events.ResolverRequest` message:

* ``rendezvous.resolve(lookup)`` — user chose a candidate.
* ``rendezvous.skip()`` — user skipped the part.

Both paths wake the blocked worker thread instantly via the underlying
``Future``.  On app shutdown the app calls ``rendezvous.cancel()`` which
raises ``CancelledError`` in the worker — the modal itself does not need
to handle shutdown.
"""

from __future__ import annotations

from dataclasses import replace

from textual.app import ComposeResult
from textual.binding import Binding
from textual.containers import Vertical, Horizontal
from textual.screen import ModalScreen
from textual.widgets import Button, DataTable, Label

from mouser import _candidate_package, best_price_break
from mouser_scoring import parse_price
from tui.events import ResolverRendezvous, ResolverRequest


class ResolverModal(ModalScreen[None]):
    """Full-screen modal for interactive candidate selection.

    The modal displays the part under review, the current suggested match,
    and a scrollable table of scored candidates.  The user interacts via
    keyboard bindings or the action buttons at the bottom.

    Row selection is handled by the DataTable natively — pressing Enter
    or double-clicking a row fires ``DataTable.RowSelected`` which we
    handle in :meth:`on_data_table_row_selected`.  We do NOT bind
    ``enter`` at the screen level because screen-level bindings take
    priority over widget bindings in Textual and would prevent the
    DataTable from ever seeing the Enter key.

    Parameters
    ----------
    request:
        The :class:`ResolverRequest` message that triggered this modal.
        Contains the part data, candidates, and the rendezvous reply
        channel.
    """

    BINDINGS = [
        Binding("a", "accept", "Accept suggested"),
        Binding("s", "skip", "Skip part"),
        Binding("q", "quit_run", "Quit run"),
        Binding("escape", "skip", "Skip"),
        # No "enter" binding here — the DataTable handles Enter natively
        # and fires RowSelected, which we catch in on_data_table_row_selected().
    ]

    DEFAULT_CSS = """
    ResolverModal {
        align: center middle;
    }

    #resolver-container {
        width: 90%;
        height: 85%;
        border: thick $primary;
        background: $surface;
        padding: 1 2;
    }

    #resolver-header {
        height: auto;
        margin-bottom: 1;
    }

    #resolver-suggested {
        height: auto;
        margin-bottom: 1;
        color: $success;
    }

    #resolver-table {
        height: 1fr;
        margin-bottom: 1;
        overflow-y: scroll;
    }

    #resolver-actions {
        height: 3;
        align: center middle;
    }

    #resolver-actions Button {
        margin: 0 1;
    }
    """

    def __init__(self, request: ResolverRequest) -> None:
        super().__init__()
        self._request = request
        self._rendezvous: ResolverRendezvous = request.rendezvous

    def compose(self) -> ComposeResult:
        """Build the modal layout."""
        part = self._request.part
        suggested = self._request.suggested_mpn or "---"
        method = self._request.method

        with Vertical(id="resolver-container"):
            # --- Header with part info ---
            header_text = (
                f"Interactive resolver: {part.part_number} ({part.manufacturer})"
            )
            if part.description:
                header_text += f"\n  {part.description}"
            if part.package or part.pins is not None:
                pkg = part.package or "---"
                pins = str(part.pins) if part.pins is not None else "---"
                header_text += f"\n  Package: {pkg}   Pins: {pins}"
            yield Label(header_text, id="resolver-header")

            # --- Suggested match ---
            yield Label(
                f"  Suggested: {suggested} [{method}]",
                id="resolver-suggested",
            )

            # --- Candidate table ---
            yield DataTable(id="resolver-table")

            # --- Action buttons ---
            # Buttons are set non-focusable so the DataTable always keeps
            # keyboard focus.  Mouse clicks still fire Button.Pressed
            # (Textual's _on_click doesn't require focus), and the
            # keyboard shortcuts [a], [s], [q] are handled by the modal's
            # key bindings — so nothing is lost.
            with Horizontal(id="resolver-actions"):
                yield Button("Accept [a]", id="btn-accept", variant="success")
                yield Button("Skip [s]", id="btn-skip", variant="default")
                yield Button("Quit [q]", id="btn-quit", variant="error")

    def on_mount(self) -> None:
        """Populate the candidate table when the modal mounts."""
        # Prevent action buttons from stealing keyboard focus away from
        # the DataTable.  can_focus is a class attribute, not a constructor
        # param, so we set it per-instance after compose.  Mouse clicks
        # still fire Button.Pressed — Textual's _on_click doesn't require
        # focus — and the keyboard shortcuts [a/s/q] are handled by the
        # modal's key bindings.
        for btn in self.query(Button):
            btn.can_focus = False

        table = self.query_one("#resolver-table", DataTable)
        table.cursor_type = "row"
        table.zebra_stripes = True
        table.add_column("#", key="index", width=4)
        table.add_column("Manufacturer PN", key="mpn", width=26)
        table.add_column("Mouser PN", key="mouser_pn", width=20)
        table.add_column("Package", key="package", width=12)
        table.add_column("Pins", key="pins", width=5)
        table.add_column("MPQ", key="mpq", width=7)
        table.add_column("Unit Price", key="price", width=14)
        table.add_column("Available", key="avail", width=10)

        quantity = self._request.part.total_quantity
        for idx, candidate in enumerate(self._request.candidates):
            part_data = candidate.part
            mpn = str(part_data.get("ManufacturerPartNumber") or "---")[:24]
            mouser_pn = str(part_data.get("MouserPartNumber") or "---")[:18]

            # Availability — extract just the numeric stock count when
            # the raw value follows Mouser's "NNN In Stock" format.
            raw_avail = part_data.get("Availability")
            if raw_avail and str(raw_avail) not in ("None", ""):
                avail_str = str(raw_avail)
                avail = avail_str.split(" ")[0] if " " in avail_str else avail_str
            else:
                avail = "---"

            # Unit price — pick the best price break for the BOM quantity.
            price_breaks = part_data.get("PriceBreaks", [])
            pb = best_price_break(price_breaks, quantity)
            if pb:
                price_val = parse_price(str(pb.get("Price", "")))
                currency = str(pb.get("Currency", ""))
                if price_val is not None:
                    price_text = f"{price_val:.4f} {currency}".strip()
                else:
                    price_text = str(pb.get("Price", "---"))
            else:
                price_text = "---"

            # MPQ — minimum package quantity (pcs per reel/tube/tray).
            # First try the enriched packaging details from the worker
            # thread (which may include product-page data).  Fall back to
            # the smallest price-break quantity from the search API, which
            # is the effective MOQ and always present.
            mpq_val = None
            pkg_details = self._request.packaging_map.get(mpn)
            if pkg_details is not None:
                mpq_val = (
                    pkg_details.standard_pack_quantity
                    or pkg_details.full_reel_quantity
                    or pkg_details.minimum_order_quantity
                )
            if not mpq_val and price_breaks:
                mpq_val = min(
                    (int(pb.get("Quantity", 0)) for pb in price_breaks),
                    default=None,
                )
            mpq_text = str(mpq_val) if mpq_val else "---"

            # Use the same helper as the CLI resolver — it converts
            # None → "—" and int pins → str, which DataTable requires.
            pkg_text, pins_text = _candidate_package(
                candidate, self._request.part.manufacturer
            )

            table.add_row(
                str(idx + 1),
                mpn,
                mouser_pn,
                pkg_text[:12],
                pins_text,
                mpq_text,
                price_text[:13],
                avail,
                key=str(idx),
            )

        # Give the table keyboard focus so arrow keys navigate rows
        # immediately.  Use direct focus() — call_after_refresh() can
        # desynchronize the cursor rendering state from the focus state
        # inside a ModalScreen (Textual issue #4524).
        table.focus()

    # --- Actions ---

    def action_accept(self) -> None:
        """Accept the currently suggested candidate."""
        lookup = self._rendezvous.original_lookup
        if lookup.part is not None and self._request.candidates:
            self._resolve_with_candidate(0)
        else:
            self._skip()

    def action_skip(self) -> None:
        """Skip this part without changing the lookup result."""
        self._skip()

    async def action_quit_run(self) -> None:
        """Quit the entire pricing run.

        Delegates to the app's ``action_quit()`` which properly:

        1. Sets ``shutdown_event`` so the worker stops at the next
           iteration boundary.
        2. Cancels the active ``ResolverRendezvous`` so the blocked
           worker thread wakes instantly with ``CancelledError``.
        3. Waits briefly for the worker to exit.
        4. Calls ``app.exit()`` which tears down all screens (including
           this modal) and restores the terminal cleanly.

        We must NOT call ``_skip()`` first — that would resolve the
        Future before ``cancel()`` can fire, allowing the worker to
        continue processing before ``shutdown_event`` is checked.
        """
        await self.app.action_quit()

    def on_data_table_row_highlighted(self, event: DataTable.RowHighlighted) -> None:
        """Force a full re-render when the cursor moves between rows.

        DataTable's ``watch_cursor_coordinate`` calls ``refresh_row()`` for
        the old and new rows, but that only marks a visual region dirty via
        ``_refresh_region()``.  In a ``ModalScreen`` context the region
        overlap check can bail out, leaving the internal render caches
        (``_line_cache``, ``_row_render_cache``, ``_cell_render_cache``)
        holding stale entries keyed on the old ``cursor_coordinate``.

        Calling ``_clear_caches()`` (DataTable's own method) purges all
        eight internal caches, and ``refresh()`` forces a complete
        re-render.  This is slightly more work than a targeted invalidation
        but guarantees correct cursor highlighting on every arrow-key press.
        """
        table = event.data_table
        table._clear_caches()
        table.refresh()

    def on_data_table_row_selected(self, event: DataTable.RowSelected) -> None:
        """Handle row selection from Enter key or double-click on the table.

        Textual fires this event when ``cursor_type`` is ``"row"`` and
        the user presses Enter on the highlighted row or double-clicks
        a row.  The ``row_key`` carries the string key we assigned when
        adding the row (the 0-based candidate index as a string).
        """
        try:
            index = int(event.row_key.value)
        except (ValueError, TypeError):
            return
        if 0 <= index < len(self._request.candidates):
            self._resolve_with_candidate(index)

    async def on_button_pressed(self, event: Button.Pressed) -> None:
        """Handle action button presses."""
        if event.button.id == "btn-accept":
            self.action_accept()
        elif event.button.id == "btn-skip":
            self.action_skip()
        elif event.button.id == "btn-quit":
            await self.action_quit_run()

    # --- Resolution helpers ---

    def _resolve_with_candidate(self, index: int) -> None:
        """Resolve the rendezvous with the candidate at the given index.

        Persists the choice to the resolution store (if available) so it
        is reused on subsequent runs, then fulfils the rendezvous and
        dismisses the modal.
        """
        selected = self._request.candidates[index]

        # Persist the interactive selection for future runs.
        store = self._rendezvous.resolution_store
        if store is not None:
            store.set(
                self._request.part.manufacturer,
                self._request.part.part_number,
                str(selected.part.get("MouserPartNumber") or ""),
                str(selected.part.get("ManufacturerPartNumber") or ""),
            )

        resolved_lookup = replace(
            self._rendezvous.original_lookup,
            part=selected.part,
            review_required=False,
            resolution_source="interactive",
        )
        self._rendezvous.resolve(resolved_lookup)
        self.dismiss()

    def _skip(self) -> None:
        """Skip this part — fulfil the rendezvous with the original lookup."""
        self._rendezvous.skip()
        self.dismiss()
