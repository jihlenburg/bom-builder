"""Rich-powered console output for BOM Builder.

This module centralises all terminal-facing display logic behind a shared
:class:`rich.console.Console` instance with a project-specific colour theme.
When stdout is not a TTY (piped, redirected, or running under pytest's
``capsys`` fixture), Rich automatically falls back to unstyled plain text so
callers produce parseable output regardless of execution context.

Design notes
------------
* ``highlight=False`` disables Rich's automatic number/URL highlighting so
  that only *explicit* Rich markup or :class:`~rich.text.Text` styles drive
  the terminal colours.  This avoids surprises with bare part numbers and
  prices being auto-coloured.
* Callers should prefer :class:`~rich.text.Text` objects over markup strings
  when the text may contain literal square brackets (e.g. timing suffixes
  like ``[0.420s | mouser=0.111s]``), because Rich's markup parser can
  misinterpret bracket-heavy text.
"""

from __future__ import annotations

from rich.console import Console
from rich.panel import Panel
from rich.rule import Rule
from rich.table import Table
from rich.text import Text
from rich.theme import Theme

# ---------------------------------------------------------------------------
# Theme — defines the named styles that callers reference in markup strings
# or Text.append(style=...) calls.
# ---------------------------------------------------------------------------

_THEME = Theme(
    {
        # Status labels shown in live per-part output.
        "ok": "bold green",
        "review": "bold yellow",
        "error": "bold red",
        # Part-number emphasis.
        "part": "bold cyan",
        # Price and cost figures.
        "price": "green",
        # Subdued elements: timing suffixes, notes, separators.
        "dim": "dim",
        "note": "dim italic",
        # Section headings in the summary.
        "heading": "bold",
    }
)

# ---------------------------------------------------------------------------
# Shared Console instance
#
# Rich evaluates ``sys.stdout`` lazily (via a property fallback when
# ``file=None``), so this module-level object works correctly with pytest's
# capsys fixture and the TeeTextIO trace wrapper in config.py — both of
# which replace sys.stdout *after* import time.
# ---------------------------------------------------------------------------

console = Console(theme=_THEME, highlight=False)

# ---------------------------------------------------------------------------
# Re-exports so callers only need ``from console import ...``
# ---------------------------------------------------------------------------

__all__ = [
    "console",
    "Panel",
    "Rule",
    "Table",
    "Text",
]
