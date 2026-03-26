"""Custom Textual message types and the resolver rendezvous for TUI threading.

The pricing pipeline runs in a background worker thread (see :mod:`tui.worker`).
Since Textual widgets must only be mutated from the main async event loop,
all progress updates flow through strongly-typed ``Message`` subclasses that
the worker posts via ``app.post_message()``, which is thread-safe by Textual's
contract.

Thread-safety model
-------------------
The worker–UI boundary uses two mechanisms:

1. **Textual messages** (one-way, worker → UI) for progress, completion, and
   error events.  These are pure data carriers with no synchronization state.
2. **ResolverRendezvous** (bidirectional, worker ↔ UI) for interactive
   resolution.  Wraps a :class:`concurrent.futures.Future` so the worker can
   block with ``future.result()`` and the UI can fulfil with
   ``set_result()`` or abort with ``cancel()``.  Both paths wake the worker
   instantly via the Future's internal ``Condition.notify_all()``.

Design note — Textual's ``Message`` base class uses ``__init_subclass__``
hooks and its own ``__init__`` for internal plumbing (bubble flags, handler
routing).  Applying ``@dataclass`` would override that ``__init__`` and
break message delivery.  All message classes here use explicit ``__init__``
methods that call ``super().__init__()`` first.
"""

from __future__ import annotations

from concurrent.futures import Future, CancelledError  # noqa: F401 — re-exported

from textual.message import Message

from models import AggregatedPart, BomSummary, PricedPart
from mouser import LookupResult
from resolution_store import ResolutionStore


# ---------------------------------------------------------------------------
# Resolver rendezvous — the thread-safe reply channel
# ---------------------------------------------------------------------------

class ResolverRendezvous:
    """Thread-safe reply channel for interactive candidate resolution.

    Created by the worker thread, passed into :class:`ResolverRequest` as a
    field, and fulfilled by the :class:`~tui.resolver_modal.ResolverModal`.
    The worker blocks on :meth:`wait` and the modal calls :meth:`resolve` or
    :meth:`skip`.  On app shutdown the app calls :meth:`cancel`, which wakes
    the worker instantly with a :class:`CancelledError`.

    The internal :class:`~concurrent.futures.Future` provides:

    * ``set_result()`` → ``result()`` returns the resolved lookup.
    * ``cancel()`` → ``result()`` raises ``CancelledError``.
    * Both paths call ``Condition.notify_all()`` under the hood, so the
      blocked worker wakes immediately — no polling, no timeout tuning.

    Attributes
    ----------
    original_lookup:
        The unmodified lookup result as it entered the resolver stage.
    resolution_store:
        Optional persistent store for saving interactive selections so
        they are reused on subsequent runs.
    """

    def __init__(
        self,
        lookup: LookupResult,
        resolution_store: ResolutionStore | None = None,
    ) -> None:
        self._future: Future[LookupResult] = Future()
        self.original_lookup = lookup
        self.resolution_store = resolution_store

    def resolve(self, lookup: LookupResult) -> None:
        """Fulfil the rendezvous with a resolved lookup.

        Wakes the blocked worker thread immediately.

        Parameters
        ----------
        lookup:
            The lookup result reflecting the user's candidate selection.
        """
        self._future.set_result(lookup)

    def skip(self) -> None:
        """Fulfil the rendezvous with the original unmodified lookup.

        Used when the user skips the part without making a selection.
        Wakes the blocked worker thread immediately.
        """
        self._future.set_result(self.original_lookup)

    def cancel(self) -> None:
        """Abort the rendezvous, waking the worker with ``CancelledError``.

        Called by the app on shutdown so the worker thread unblocks
        instantly instead of waiting for a modal that will never appear.
        """
        self._future.cancel()

    def wait(self) -> LookupResult:
        """Block until resolved, skipped, or cancelled.

        Returns
        -------
        LookupResult
            The resolved or original lookup.

        Raises
        ------
        CancelledError
            If :meth:`cancel` was called (app shutting down).
        """
        return self._future.result()


# ---------------------------------------------------------------------------
# Progress messages — posted once per part as pricing completes
# ---------------------------------------------------------------------------

class PartPricingStarted(Message):
    """Emitted when the worker begins pricing one BOM line.

    The UI uses this to show a "looking up..." row in the parts table
    and update the progress indicator.
    """

    def __init__(self, index: int, total: int, part: AggregatedPart) -> None:
        super().__init__()
        self.index = index
        self.total = total
        self.part = part


class PartPricingCompleted(Message):
    """Emitted when one BOM line has been fully priced.

    Carries the final :class:`PricedPart`, timing telemetry, and any
    runtime notices (e.g. NXP availability warnings) so the UI can
    update the row, refresh cost totals, and display notices.
    """

    def __init__(
        self,
        index: int,
        total: int,
        priced: PricedPart,
        duration: float = 0.0,
        source_timings: list[tuple[str, float]] | None = None,
        runtime_notices: list[str] | None = None,
    ) -> None:
        super().__init__()
        self.index = index
        self.total = total
        self.priced = priced
        self.duration = duration
        self.source_timings = source_timings or []
        self.runtime_notices = runtime_notices or []


# ---------------------------------------------------------------------------
# Run lifecycle messages
# ---------------------------------------------------------------------------

class PricingRunCompleted(Message):
    """Emitted once after all parts have been priced.

    The UI uses this to transition from the "pricing in progress" state
    to the summary/export view.
    """

    def __init__(self, parts: list[PricedPart], summary: BomSummary) -> None:
        super().__init__()
        self.parts = parts
        self.summary = summary


class PricingRunFailed(Message):
    """Emitted when the pricing run encounters a fatal error.

    Carries the exception so the UI can display a meaningful error panel
    instead of silently hanging.
    """

    def __init__(self, error: Exception) -> None:
        super().__init__()
        self.error = error


# ---------------------------------------------------------------------------
# Interactive resolution message
# ---------------------------------------------------------------------------

class ResolverRequest(Message):
    """Posted by the worker when a part needs interactive resolution.

    This is a pure data message — all synchronization state lives in
    the :class:`ResolverRendezvous`.  The worker creates the rendezvous,
    embeds it here, posts this message, and then blocks on
    ``rendezvous.wait()``.  The UI pushes a modal that calls
    ``rendezvous.resolve()`` or ``rendezvous.skip()``.

    Attributes
    ----------
    part:
        The aggregated BOM line that needs resolution.
    candidates:
        Scored candidates from the Mouser lookup, in ranked order.
    suggested_mpn:
        The currently suggested manufacturer part number, or None.
    method:
        The match method that produced the current suggestion.
    rendezvous:
        The thread-safe reply channel the modal uses to send back
        the user's decision.
    """

    def __init__(
        self,
        part: AggregatedPart,
        candidates: tuple,
        suggested_mpn: str | None,
        method: str,
        rendezvous: ResolverRendezvous,
        packaging_map: dict | None = None,
    ) -> None:
        super().__init__()
        self.part = part
        self.candidates = candidates
        self.suggested_mpn = suggested_mpn
        self.method = method
        self.rendezvous = rendezvous
        self.packaging_map: dict = packaging_map or {}
