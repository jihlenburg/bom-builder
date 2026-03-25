"""Application-wide configuration values and logging bootstrap helpers.

This module deliberately stays small. It exists to centralize stable constants
such as distributor endpoints, documented public quota values, and the logging
setup that controls whether verbose diagnostics flow to stdout or stderr.

Keeping those values in one place avoids scattering "magic" URLs and defaults
throughout the resolver implementations. That matters more now that BOM Builder
is growing beyond Mouser and beginning to integrate Digi-Key as a second
distributor with locale-specific pricing behavior.
"""

from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime
import logging
import os
import sys
from pathlib import Path
from typing import Iterator, TextIO


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
DIGIKEY_API_BASE_URL = "https://api.digikey.com"
DIGIKEY_PRODUCTS_V4_BASE_PATH = "/products/v4"
DIGIKEY_DEFAULT_LOCALE_SITE = "DE"
DIGIKEY_DEFAULT_LOCALE_LANGUAGE = "en"
DIGIKEY_DEFAULT_LOCALE_CURRENCY = "EUR"
DIGIKEY_DEFAULT_LOCALE_SHIP_TO_COUNTRY = "de"
DIGIKEY_TOKEN_REFRESH_SAFETY_SECONDS = 30
PROJECT_NAME = "BOM Builder"
PROJECT_VERSION = "1.0.1.0"
DEFAULT_ATTRITION = 0.0
DATA_DIR = Path(__file__).parent
TRACE_FILE_ENV_VAR = "BOM_BUILDER_TRACE_FILE"
TRACE_DIR_ENV_VAR = "BOM_BUILDER_TRACE_DIR"


class TeeTextIO:
    """Mirror writes to both a primary console stream and a trace file."""

    def __init__(self, primary: TextIO, mirror: TextIO) -> None:
        self._primary = primary
        self._mirror = mirror

    @property
    def encoding(self) -> str:
        """Expose the primary stream encoding for APIs like :func:`input`."""
        return getattr(self._primary, "encoding", "utf-8")

    def write(self, data: str) -> int:
        """Write text to both underlying streams."""
        written = self._primary.write(data)
        self._mirror.write(data)
        return written

    def flush(self) -> None:
        """Flush both underlying streams."""
        self._primary.flush()
        self._mirror.flush()

    def isatty(self) -> bool:
        """Delegate TTY checks to the primary console stream."""
        checker = getattr(self._primary, "isatty", None)
        return bool(checker()) if callable(checker) else False

    def fileno(self) -> int:
        """Delegate file-descriptor access to the primary stream when present."""
        return self._primary.fileno()

    def writable(self) -> bool:
        """Report that the tee wrapper accepts text writes."""
        return True

    def __getattr__(self, name: str) -> object:
        """Delegate unknown stream attributes to the primary console stream."""
        return getattr(self._primary, name)


def resolve_trace_path(trace_file: Path | None = None) -> Path | None:
    """Return the effective transcript path for this run, if enabled.

    Resolution order is:

    1. explicit ``trace_file`` argument
    2. ``BOM_BUILDER_TRACE_FILE``
    3. ``BOM_BUILDER_TRACE_DIR`` with an auto-generated timestamped filename
    """
    if trace_file is not None:
        return trace_file.expanduser()

    explicit = os.getenv(TRACE_FILE_ENV_VAR, "").strip()
    if explicit:
        return Path(explicit).expanduser()

    trace_dir = os.getenv(TRACE_DIR_ENV_VAR, "").strip()
    if not trace_dir:
        return None

    timestamp = datetime.now().astimezone().strftime("%Y%m%d-%H%M%S")
    filename = f"bom-builder-{timestamp}-pid{os.getpid()}.log"
    return Path(trace_dir).expanduser() / filename


@contextmanager
def install_console_trace(trace_path: Path | None) -> Iterator[TextIO | None]:
    """Mirror stdout/stderr into a file for the lifetime of the context."""
    if trace_path is None:
        yield None
        return

    final_path = trace_path.expanduser()
    final_path.parent.mkdir(parents=True, exist_ok=True)
    original_stdout = sys.stdout
    original_stderr = sys.stderr

    with final_path.open("w", encoding="utf-8", buffering=1) as trace_stream:
        sys.stdout = TeeTextIO(original_stdout, trace_stream)
        sys.stderr = TeeTextIO(original_stderr, trace_stream)
        try:
            yield trace_stream
        finally:
            try:
                sys.stdout.flush()
                sys.stderr.flush()
            finally:
                sys.stdout = original_stdout
                sys.stderr = original_stderr
                trace_stream.flush()


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
