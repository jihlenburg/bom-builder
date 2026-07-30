"""Textual-based terminal user interface for BOM Builder.

This package provides a full-screen interactive TUI that replaces the
line-by-line CLI output when ``--interactive`` is passed on a TTY.  The
TUI shows a live-updating parts table, real-time cost metrics, and a
modal dialog for interactive candidate resolution.

The package is structured as follows:

- :mod:`tui.events` — Custom Textual ``Message`` subclasses for
  worker-to-UI communication.
- :mod:`tui.worker` — Threading bridge that runs the synchronous pricing
  pipeline inside a Textual worker thread and posts progress events.
- :mod:`tui.widgets` — Reusable Textual widgets: parts table, cost panel,
  status bar.
- :mod:`tui.resolver_modal` — ``ModalScreen`` for interactive candidate
  selection.
- :mod:`tui.app` — The top-level ``BomBuilderApp`` that composes the
  screen layout and orchestrates the run lifecycle.

Usage from main.py::

    from tui import BomBuilderApp
    app = BomBuilderApp(aggregated=..., args=..., ...)
    app.run()
"""

from tui.app import BomBuilderApp

__all__ = ["BomBuilderApp"]
