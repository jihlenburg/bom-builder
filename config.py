"""Application-wide configuration values and logging bootstrap helpers.

This module deliberately stays small. It exists to centralize stable constants
such as the Mouser endpoint and default attrition factor, plus the logging
setup that controls whether verbose diagnostics flow to stdout or stderr.

It also records the documented public Mouser Search API quota values in one
place so request-budget logic does not scatter magic numbers throughout the
resolver implementation.
"""

from dataclasses import dataclass
import logging
import sys
from pathlib import Path
from typing import TextIO


@dataclass(frozen=True)
class MouserSearchApiLimits:
    """Documented public quotas for the Mouser Search API.

    Attributes
    ----------
    calls_per_minute:
        Published request ceiling per minute.
    calls_per_day:
        Published request ceiling per day.
    source_url:
        Official Mouser page documenting the public limits.
    """

    calls_per_minute: int
    calls_per_day: int
    source_url: str


MOUSER_API_URL = "https://api.mouser.com/api/v2/search/partnumber"
MOUSER_DEFAULT_RATE_LIMIT_BACKOFF = 2.0
MOUSER_DEFAULT_MAX_ATTEMPTS = 4
MOUSER_SEARCH_API_LIMITS = MouserSearchApiLimits(
    calls_per_minute=30,
    calls_per_day=1000,
    source_url="https://www.mouser.com/api-search/",
)
DEFAULT_ATTRITION = 0.0
DATA_DIR = Path(__file__).parent


def setup_logging(verbose: bool = False, stream: TextIO | None = None) -> None:
    """Configure process-wide logging for the CLI.

    Parameters
    ----------
    verbose:
        When ``True``, the root logger is set to ``DEBUG`` and writes to
        stdout so users can capture a full resolver trace with shell tools.
        Otherwise logging stays at ``INFO`` and uses stderr.
    stream:
        Optional explicit stream override used mainly by tests.

    Notes
    -----
    The function forces logging reconfiguration each time it is called because
    the CLI may invoke it from tests or repeated short-lived runs where stale
    handlers would otherwise accumulate.
    """
    level = logging.DEBUG if verbose else logging.INFO
    target_stream = stream or (sys.stdout if verbose else sys.stderr)
    logging.basicConfig(
        level=level,
        format="%(levelname)-5s %(name)s: %(message)s",
        stream=target_stream,
        force=True,
    )
    # Suppress noisy third-party loggers so verbose mode stays focused on
    # BOM-builder diagnostics rather than low-level HTTP connection chatter.
    logging.getLogger("httpx").setLevel(logging.WARNING)
    logging.getLogger("httpcore").setLevel(logging.WARNING)
