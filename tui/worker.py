"""Threading bridge between the synchronous pricing pipeline and the Textual event loop.

The BOM pricing pipeline is inherently synchronous — it makes sequential HTTP
calls with rate-limiting sleeps.  Textual's event loop is async.  This module
bridges the two by running the pricing work inside a Textual worker thread
(``run_worker(thread=True)``) and posting ``Message`` events back to the UI
via ``app.post_message()``, which is thread-safe.

Thread-safety contract
----------------------
The worker thread and the UI thread coordinate through three primitives:

1. ``app.post_message()`` — thread-safe by Textual's guarantee.  Used for
   all worker → UI communication (progress, completion, errors, resolver
   requests).
2. ``app.shutdown_event`` — a :class:`threading.Event` owned by the app.
   Set by the UI thread before ``app.exit()``.  The worker checks it at
   every iteration boundary so it stops starting new parts promptly.
3. ``ResolverRendezvous`` — wraps a :class:`concurrent.futures.Future`.
   The worker blocks on ``rendezvous.wait()``, the modal calls
   ``rendezvous.resolve()``/``skip()``, and the app calls
   ``rendezvous.cancel()`` on shutdown.  All three paths wake the worker
   instantly via ``Future``'s internal ``Condition.notify_all()``.

The resolver callback
---------------------
When the Mouser pipeline encounters an ambiguous part, it calls our
callback instead of the text-based ``_interactive_resolution_prompt``.
The callback:

1. Creates a ``ResolverRendezvous`` carrying the lookup and resolution store.
2. Posts a ``ResolverRequest`` message (pure data + rendezvous reference).
3. Blocks on ``rendezvous.wait()``.
4. Returns the resolved lookup, or the original on ``CancelledError``.
"""

from __future__ import annotations

import argparse
import logging
import time
from concurrent.futures import CancelledError
from contextlib import ExitStack
from typing import TYPE_CHECKING

from textual.worker import get_current_worker

from ai_resolver import OpenAIResolver
from digikey import DigiKeyClient, digikey_is_configured
from fx import FXRateProvider, resolve_target_currency
from main import (
    SinglePartResult,
    _price_single_part,
)
from models import AggregatedPart, BomSummary, PricedPart
from mouser import LookupResult, MouserClient, _packaging_details_for_candidate
from mouser_packaging import MouserPackagingDetails
from mouser_scoring import ScoredCandidate, collapse_packaging_variants
from nxp import NXPClient, nxp_is_available
from resolution_store import ResolutionStore
from ti import TIClient, ti_is_configured, ti_supports_manufacturer

from tui.events import (
    PartPricingCompleted,
    PartPricingStarted,
    PricingRunCompleted,
    PricingRunFailed,
    ResolverRendezvous,
    ResolverRequest,
)

if TYPE_CHECKING:
    from tui.app import BomBuilderApp


def _should_stop(app: BomBuilderApp) -> bool:
    """Check whether the worker should stop immediately.

    Combines our own ``shutdown_event`` with Textual's built-in worker
    cancellation flag.  Textual sets ``worker.is_cancelled`` during
    ``app.exit()`` teardown — if we don't respect it, the worker thread
    keeps running and prevents Textual from restoring the terminal
    (disabling mouse tracking, exiting the alternate screen, etc.).
    """
    if app.shutdown_event.is_set():
        return True
    try:
        return get_current_worker().is_cancelled
    except LookupError:
        # get_current_worker() raises LookupError when called outside
        # a worker context (e.g. in tests).
        return False


def _make_resolver_callback(
    app: BomBuilderApp,
    resolution_store: ResolutionStore,
    ti_client: TIClient | None = None,
):
    """Build a resolver callback that posts a TUI modal request.

    The returned callable satisfies the ``ResolverCallback`` signature
    expected by :func:`main._price_single_part` and
    :func:`mouser.price_part`.  When invoked from the worker thread it:

    1. Creates a :class:`ResolverRendezvous` wrapping a ``Future``.
    2. Posts a :class:`ResolverRequest` message to the Textual event loop.
    3. Blocks on ``rendezvous.wait()`` — no polling, no timeout tuning.
    4. Returns the resolved lookup, or the original on ``CancelledError``
       (which means the app is shutting down).

    If the lookup has no candidates or does not require review, the callback
    returns the lookup unchanged — no modal is shown.

    Parameters
    ----------
    app:
        The running Textual application, used for ``post_message()``.
    resolution_store:
        The persistent resolution store, passed into the rendezvous so
        the TUI modal can save interactive selections.
    ti_client:
        Optional TI client for fetching MPQ/packaging data from TI's
        store API.  Cached, so repeated lookups are free.
    """

    def _resolver(agg, lookup, _resolution_store, client):
        # Only show the modal when there are multiple candidates to choose
        # from and the match is uncertain or ambiguous.  A single candidate
        # means there's nothing to decide — accept it automatically.
        if len(lookup.candidates) <= 1:
            return lookup
        if not lookup.review_required:
            return lookup

        # Collapse packaging variants (tube/reel/tape differences) so the
        # resolver only shows electrically distinct parts.  The pricing
        # pipeline's _auto_select_packaging_variant() will pick the cheapest
        # reel size afterward — no need to burden the user with that choice.
        collapsed = collapse_packaging_variants(
            lookup.candidates, agg.manufacturer,
        )
        if len(collapsed) <= 1:
            return lookup

        # Bail immediately if the app is already shutting down.
        if _should_stop(app):
            return lookup

        suggested_mpn = None
        if lookup.part is not None:
            suggested_mpn = lookup.part.get("ManufacturerPartNumber")

        # Create the rendezvous — owns the Future, the original lookup,
        # and the resolution store reference.
        rendezvous = ResolverRendezvous(
            lookup=lookup,
            resolution_store=resolution_store,
        )

        # Enrich each candidate with packaging details (MPQ, reel size,
        # etc.).  For TI parts, query TI's store API which reliably
        # returns standardPackQuantity and minimumOrderQuantity.  For
        # other parts, use the Mouser client (search payload + optional
        # product page fallback).  All results are cached.
        log = logging.getLogger(__name__)
        packaging_map: dict[str, MouserPackagingDetails] = {}
        use_ti = ti_client is not None and ti_supports_manufacturer(agg.manufacturer)
        log.debug("MPQ enrichment: use_ti=%s, ti_client=%s, manufacturer=%s",
                   use_ti, ti_client is not None, agg.manufacturer)
        for sc in collapsed:
            mpn = str(sc.part.get("ManufacturerPartNumber") or "")
            if not mpn or mpn in packaging_map:
                continue

            # Try TI's store API first for TI parts — it always has
            # standardPackQuantity and minimumOrderQuantity.
            if use_ti:
                try:
                    ti_product = ti_client.product(mpn)
                    log.debug("TI MPQ for %s: spq=%s moq=%s carrier=%s",
                              mpn, ti_product.standard_pack_quantity,
                              ti_product.minimum_order_quantity,
                              ti_product.package_carrier)
                    packaging_map[mpn] = MouserPackagingDetails(
                        packaging_mode=ti_product.package_carrier,
                        packaging_source="ti_store_api",
                        minimum_order_quantity=ti_product.minimum_order_quantity,
                        standard_pack_quantity=ti_product.standard_pack_quantity,
                    )
                    continue
                except Exception as exc:
                    log.debug("TI MPQ lookup failed for %s: %s", mpn, exc)

            packaging_map[mpn] = _packaging_details_for_candidate(
                client, sc.part, bom_part_number=agg.part_number,
            )
            log.debug("Mouser MPQ for %s: spq=%s moq=%s frq=%s",
                      mpn, packaging_map[mpn].standard_pack_quantity,
                      packaging_map[mpn].minimum_order_quantity,
                      packaging_map[mpn].full_reel_quantity)

        # Store the rendezvous on the app so shutdown can cancel it.
        app.active_rendezvous = rendezvous

        # Show only the collapsed (deduplicated) candidates in the modal.
        app.post_message(ResolverRequest(
            part=agg,
            candidates=tuple(collapsed),
            suggested_mpn=suggested_mpn,
            method=lookup.method.value if lookup.method else "",
            rendezvous=rendezvous,
            packaging_map=packaging_map,
        ))

        try:
            return rendezvous.wait()
        except CancelledError:
            # App is shutting down — return the original lookup unchanged.
            return lookup
        finally:
            app.active_rendezvous = None

    return _resolver


def run_pricing_pipeline(
    app: BomBuilderApp,
    aggregated: list[AggregatedPart],
    args: argparse.Namespace,
) -> None:
    """Execute the full pricing pipeline, posting progress to the TUI.

    This function is designed to be called from a Textual worker thread
    via ``self.run_worker(lambda: run_pricing_pipeline(...), thread=True)``.
    It must not touch any Textual widgets directly — all UI updates flow
    through ``app.post_message()``.

    Parameters
    ----------
    app:
        The running Textual application, used for ``post_message()`` and
        ``shutdown_event``.
    aggregated:
        The aggregated BOM lines to price.
    args:
        Parsed CLI arguments controlling API keys, delays, caching, etc.
    """
    try:
        total = len(aggregated)
        results: list[PricedPart] = []

        cache_kw = dict(
            cache_enabled=not args.no_cache,
            cache_ttl_seconds=int(args.cache_ttl_hours * 3600),
        )

        with ExitStack() as stack:
            mouser_client = stack.enter_context(MouserClient(
                api_key=args.mouser_api_key, **cache_kw,
            ))
            ai_resolver = (
                stack.enter_context(OpenAIResolver(
                    model=args.ai_model,
                    confidence_threshold=args.ai_confidence_threshold,
                ))
                if args.ai_resolve
                else None
            )
            digikey_client = (
                stack.enter_context(DigiKeyClient(**cache_kw))
                if digikey_is_configured()
                else None
            )
            ti_client = (
                stack.enter_context(TIClient(**cache_kw))
                if ti_is_configured()
                else None
            )
            nxp_client = (
                stack.enter_context(NXPClient(**cache_kw))
                if nxp_is_available()
                else None
            )
            fx_rate_provider = stack.enter_context(FXRateProvider())
            comparison_currency = resolve_target_currency()

            resolution_store = ResolutionStore()
            resolver_cb = _make_resolver_callback(
                app, resolution_store, ti_client=ti_client,
            )

            for i, agg in enumerate(aggregated, 1):
                # Check both our shutdown_event and Textual's worker
                # cancellation before starting each part.
                if _should_stop(app):
                    break

                app.post_message(PartPricingStarted(
                    index=i,
                    total=total,
                    part=agg,
                ))

                result: SinglePartResult = _price_single_part(
                    agg,
                    mouser_client,
                    digikey_client=digikey_client,
                    ti_client=ti_client,
                    nxp_client=nxp_client,
                    fx_rate_provider=fx_rate_provider,
                    comparison_currency=comparison_currency,
                    interactive=True,
                    resolution_store=resolution_store,
                    ai_resolver=ai_resolver,
                    resolver_callback=resolver_cb,
                )

                # Re-check after pricing — user may have quit during
                # the resolver modal for this very part.
                if _should_stop(app):
                    break

                app.post_message(PartPricingCompleted(
                    index=i,
                    total=total,
                    priced=result.priced,
                    duration=result.duration,
                    source_timings=result.source_timings,
                    runtime_notices=result.runtime_notices,
                ))
                results.append(result.priced)

                # Rate-limit pacing between live Mouser requests.  Use
                # shutdown_event.wait() instead of time.sleep() so the
                # worker wakes immediately on quit.
                if i < total and args.mouser_delay > 0 and result.used_live_mouser:
                    app.shutdown_event.wait(timeout=args.mouser_delay)
                    if _should_stop(app):
                        break

        # Only post the completion message if the app is still running.
        if not _should_stop(app):
            summary = BomSummary.from_parts(results, args.units)
            app.post_message(PricingRunCompleted(parts=results, summary=summary))

    except Exception as exc:
        # Don't post failure if the app is already exiting.
        if not _should_stop(app):
            app.post_message(PricingRunFailed(error=exc))
