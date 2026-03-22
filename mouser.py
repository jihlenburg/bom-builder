"""Mouser integration, search heuristics, and pricing workflow.

This module is the heart of BOM Builder. It owns the distributor-facing lookup
pipeline and the heuristics used to turn messy BOM part numbers into buyable
Mouser orderables. The resolver flow is intentionally layered:

1. deterministic Mouser searches using exact and prefix-based passes
2. candidate scoring using manufacturer, qualifiers, availability, and
   packaging-aware heuristics
3. ambiguity handling through saved resolutions, optional AI reranking, and
   optional interactive human choice
4. price-break selection and enrichment of the final :class:`PricedPart`

The logic is kept in one module on purpose because the scoring, ambiguity, and
pricing decisions are tightly coupled and easier to reason about together.
"""

import logging
import re
import sys
import time
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

import httpx
import yaml

from config import (
    DATA_DIR,
    MOUSER_API_URL,
    MOUSER_DEFAULT_MAX_ATTEMPTS,
    MOUSER_DEFAULT_RATE_LIMIT_BACKOFF,
    MOUSER_SEARCH_API_LIMITS,
)
from lookup_cache import LookupCache
from models import AggregatedPart, MatchMethod, PricedPart
from package import extract_package_info
from secret_store import get_secret, get_secret_values

log = logging.getLogger(__name__)

type MouserPart = dict[str, Any]
_PACKAGING_SUFFIX_TOKENS = {
    "R",
    "T",
    "TR",
    "M",
    "RE",
    "TE",
}


@dataclass(frozen=True)
class ScoredCandidate:
    """One Mouser result paired with its computed relevance score.

    The resolver keeps the original raw Mouser part payload intact and stores
    the heuristic score alongside it, allowing later stages to inspect both the
    score and the underlying distributor metadata.
    """

    part: MouserPart
    score: float


@dataclass(frozen=True)
class LookupPass:
    """One explicit search attempt in the multi-pass resolver pipeline.

    Attributes
    ----------
    search_term:
        Part number or normalized base part number sent to Mouser.
    search_option:
        Mouser search mode, typically ``"Exact"`` or ``"BeginsWith"``.
    method:
        Match classification associated with this pass.
    """

    search_term: str
    search_option: str
    method: MatchMethod


@dataclass(frozen=True)
class LookupResult:
    """Best result currently known from the lookup pipeline.

    This object captures both the chosen candidate and the uncertainty state
    around that choice, which allows later stages to distinguish between
    confident deterministic matches, review-required fuzzy matches, and
    externally resolved matches coming from saved/AI/interactive flows.
    """

    part: MouserPart | None
    method: MatchMethod
    candidate_count: int = 0
    review_required: bool = False
    candidates: tuple[ScoredCandidate, ...] = ()
    resolution_source: str | None = None


def _normalize_manufacturer_name(name: str) -> str:
    """Normalize manufacturer names for alias and substring comparisons."""
    return " ".join(name.lower().strip().split())

# ---------------------------------------------------------------------------
# Manufacturer alias loading
# ---------------------------------------------------------------------------


def load_manufacturer_aliases(yaml_path: Path | None = None) -> dict[str, set[str]]:
    """Load manufacturer aliases from YAML and build a bidirectional lookup map.

    Parameters
    ----------
    yaml_path:
        Optional explicit path to the alias configuration. When omitted, the
        function uses ``manufacturers.yaml`` in the repository data directory.

    Returns
    -------
    dict[str, set[str]]
        Mapping from normalized manufacturer names to the other normalized
        names considered equivalent.
    """
    yaml_path = yaml_path or DATA_DIR / "manufacturers.yaml"
    if not yaml_path.exists():
        log.warning("Manufacturer aliases file not found: %s", yaml_path)
        return {}

    try:
        with yaml_path.open(encoding="utf-8") as f:
            raw = yaml.safe_load(f)
    except yaml.YAMLError as e:
        log.warning("Failed to parse %s: %s", yaml_path, e)
        return {}

    if not isinstance(raw, dict):
        log.warning("Manufacturer aliases in %s must be a mapping", yaml_path)
        return {}

    aliases: dict[str, set[str]] = {}
    for canonical, alias_list in raw.items():
        if not isinstance(canonical, str):
            continue

        related_names = {_normalize_manufacturer_name(canonical)}
        if isinstance(alias_list, (list, tuple, set)):
            related_names.update(
                _normalize_manufacturer_name(str(alias))
                for alias in alias_list
                if str(alias).strip()
            )
        elif isinstance(alias_list, str) and alias_list.strip():
            related_names.add(_normalize_manufacturer_name(alias_list))

        related_names.discard("")
        for name in related_names:
            aliases.setdefault(name, set()).update(related_names - {name})

    return aliases


# Module-level alias table (loaded once at import)
MANUFACTURER_ALIASES = load_manufacturer_aliases()

# ---------------------------------------------------------------------------
# Qualifier / suffix rules
# ---------------------------------------------------------------------------

STRIP_SUFFIXES = [
    r"[-/]NOPB$",
    r"-Q1$",
    r"-EP$",
    r"-ND$",
    r"#PBF$",
    r"-TR$",
]

QUALIFIER_RULES = {
    "automotive": {
        "input_pattern": re.compile(r"-Q1$|[-_]Q1\b", re.IGNORECASE),
        "candidate_pattern": re.compile(
            r"Q1$|Q1\b|AEC[-\s]?Q\d{3}|automotive", re.IGNORECASE
        ),
        "weight": 40,
    },
    "lead_free": {
        "input_pattern": re.compile(r"[-/]NOPB$|#PBF$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"NOPB|PBF|lead.?free|RoHS", re.IGNORECASE),
        "weight": 10,
    },
    "exposed_pad": {
        "input_pattern": re.compile(r"-EP$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"-EP\b|exposed.?pad", re.IGNORECASE),
        "weight": 20,
    },
    "tape_reel": {
        "input_pattern": re.compile(r"-TR$", re.IGNORECASE),
        "candidate_pattern": re.compile(r"TR$|tape.?reel", re.IGNORECASE),
        "weight": 5,
    },
}

# ---------------------------------------------------------------------------
# Non-component filter (EVMs, dev kits, etc.)
# ---------------------------------------------------------------------------

_NON_COMPONENT_MPN = re.compile(
    r"EVM\b|EVAL\b|DEMO\b|DEV\b|-EK\b|-DK\b|-KIT\b|BOOST-",
    re.IGNORECASE,
)
_NON_COMPONENT_DESC = re.compile(
    r"evaluation\s+module|evaluation\s+board|development\s+tool|"
    r"development\s+kit|demo\s+board|starter\s+kit|reference\s+design",
    re.IGNORECASE,
)
_NON_COMPONENT_CAT = re.compile(
    r"development\s+tool|evaluation|demo\s+board|starter\s+kit",
    re.IGNORECASE,
)

# ---------------------------------------------------------------------------
# Price parsing
# ---------------------------------------------------------------------------

# Matches price strings like "1.234,56", "0,045", "1,234.56", "0.045"
_PRICE_RE = re.compile(r"[\d.,]+")


def parse_price(price_str: str) -> float | None:
    """Parse a Mouser price string, handling both EU and US locale formats.

    Parameters
    ----------
    price_str:
        Raw price string returned by Mouser.

    Returns
    -------
    float | None
        Parsed numeric price, or ``None`` when no valid numeric representation
        can be extracted.

    Examples
    --------
    ``"0,045 €"`` -> ``0.045``
    ``"1.234,56 €"`` -> ``1234.56``
    ``"$1,234.56"`` -> ``1234.56``
    """
    m = _PRICE_RE.search(price_str)
    if not m:
        return None

    num = m.group()

    # Determine format by looking at the last separator
    last_comma = num.rfind(",")
    last_dot = num.rfind(".")

    if last_comma > last_dot:
        # EU format: 1.234,56 → comma is decimal separator
        num = num.replace(".", "").replace(",", ".")
    elif last_dot > last_comma:
        # US format: 1,234.56 → dot is decimal separator
        num = num.replace(",", "")
    else:
        # Only one type or none — replace comma with dot as fallback
        num = num.replace(",", ".")

    try:
        return float(num)
    except ValueError:
        log.warning("Failed to parse price: %r", price_str)
        return None


# ---------------------------------------------------------------------------
# Mouser API client
# ---------------------------------------------------------------------------


class MouserClient:
    """HTTP client for the Mouser part-number search API.

    The client wraps HTTP transport concerns such as retries, backoff, and
    caching. Resolver policy is intentionally left to the surrounding helper
    functions so tests can stub either the full client or just the search
    method as needed.
    """

    def __init__(
        self,
        api_key: str = "",
        rate_limit_backoff: float = MOUSER_DEFAULT_RATE_LIMIT_BACKOFF,
        max_attempts: int = MOUSER_DEFAULT_MAX_ATTEMPTS,
        cache_enabled: bool = True,
        cache_ttl_seconds: int = 24 * 60 * 60,
    ):
        """Initialize the Mouser API client.

        Parameters
        ----------
        api_key:
            Explicit Mouser API key override. When omitted, the client first
            reads ``MOUSER_API_KEYS`` as a priority-ordered fallback list and
            then falls back to ``MOUSER_API_KEY``.
        rate_limit_backoff:
            Base backoff in seconds used for throttling and transient errors.
        max_attempts:
            Maximum number of HTTP attempts for one search request.
        cache_enabled:
            Whether the persistent lookup cache should be used.
        cache_ttl_seconds:
            Freshness window for cached search results.

        Raises
        ------
        ValueError
            If no Mouser API key can be resolved.
        """
        self.api_keys = _resolve_mouser_api_keys(api_key)
        if not self.api_keys:
            raise ValueError(
                (
                    "Mouser API key not set. Use --api-key or set MOUSER_API_KEYS "
                    "or MOUSER_API_KEY in the environment or .env."
                )
            )
        self._current_api_key_index = 0
        self.api_key = self.api_keys[self._current_api_key_index]
        self.backoff = rate_limit_backoff
        self.max_attempts = max_attempts
        self._client = httpx.Client(timeout=30.0)
        self._cache = LookupCache(ttl_seconds=cache_ttl_seconds) if cache_enabled else None
        self.network_requests = 0

    def close(self) -> None:
        """Close any open cache/database and HTTP client resources."""
        if self._cache is not None:
            self._cache.close()
        self._client.close()

    def __enter__(self) -> "MouserClient":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Release network and cache resources at the end of a ``with`` block."""
        self.close()

    def has_cached_search(self, part_number: str, search_option: str = "Exact") -> bool:
        """Return whether a fresh cached response exists for this lookup key."""
        return self._cache.has(part_number, search_option) if self._cache is not None else False

    def search(self, part_number: str, search_option: str = "Exact") -> list[dict]:
        """Execute one Mouser part-number search with retries and caching.

        Parameters
        ----------
        part_number:
            Search term sent to Mouser.
        search_option:
            Mouser part-number search mode.

        Returns
        -------
        list[dict]
            Raw Mouser ``Parts`` list from the search response.
        """
        if self._cache is not None:
            cached = self._cache.get(part_number, search_option)
            if cached is not None:
                log.debug("  cache hit for %s '%s'", search_option, part_number)
                return cached

        payload = {
            "SearchByPartRequest": {
                "mouserPartNumber": part_number,
                "partSearchOptions": search_option,
            }
        }

        for attempt in range(self.max_attempts):
            try:
                url = f"{MOUSER_API_URL}?apiKey={self.api_key}"
                self.network_requests += 1
                resp = self._client.post(url, json=payload)
                if _is_mouser_daily_limit_error(resp):
                    if self._switch_to_next_api_key("daily quota exhausted"):
                        continue
                    log.warning(
                        (
                            "Mouser daily quota exhausted for %s '%s'. "
                            "All configured keys are exhausted. "
                            "Configured public limit is %d calls/day (%s)."
                        ),
                        search_option,
                        part_number,
                        MOUSER_SEARCH_API_LIMITS.calls_per_day,
                        MOUSER_SEARCH_API_LIMITS.source_url,
                    )
                    resp.raise_for_status()

                if _is_retryable_rate_limit(resp):
                    if self._switch_to_next_api_key("rate limit hit"):
                        continue
                    if attempt >= self.max_attempts - 1:
                        resp.raise_for_status()
                    backoff = self.backoff * (2 ** attempt)
                    log.debug(
                        "Mouser throttled %s/%s '%s' with HTTP %s, backing off %.1fs",
                        search_option,
                        part_number,
                        part_number,
                        resp.status_code,
                        backoff,
                    )
                    time.sleep(backoff)
                    continue

                resp.raise_for_status()
                parts = resp.json().get("SearchResults", {}).get("Parts", [])
                if self._cache is not None:
                    self._cache.set(part_number, search_option, parts)
                return parts
            except (httpx.ConnectError, httpx.ReadTimeout, httpx.RemoteProtocolError) as e:
                if attempt >= self.max_attempts - 1:
                    raise
                backoff = self.backoff * (2 ** attempt)
                log.debug(
                    "Transient Mouser error for %s/%s '%s': %s; retrying in %.1fs",
                    search_option,
                    part_number,
                    part_number,
                    e,
                    backoff,
                )
                time.sleep(backoff)

        return []

    def _switch_to_next_api_key(self, reason: str) -> bool:
        """Advance to the next configured Mouser API key when available."""
        if self._current_api_key_index >= len(self.api_keys) - 1:
            return False
        self._current_api_key_index += 1
        self.api_key = self.api_keys[self._current_api_key_index]
        log.warning(
            "Switching to backup Mouser API key %d of %d after %s",
            self._current_api_key_index + 1,
            len(self.api_keys),
            reason,
        )
        return True


# ---------------------------------------------------------------------------
# Matching helpers
# ---------------------------------------------------------------------------


def _is_word_boundary_match(needle: str, haystack: str) -> bool:
    """Check if needle appears in haystack at a word boundary.

    For short strings (fewer than four characters), the function requires a
    whole-word match to avoid false positives such as ``"ti"`` matching
    ``"Quantic"``. Longer strings can safely use plain substring matching.
    """
    if len(needle) < 4:
        # Require whole-word match for short strings
        return bool(re.search(r"\b" + re.escape(needle) + r"\b", haystack))
    return needle in haystack


def manufacturers_match(
    input_mfr: str,
    candidate_mfr: str,
    aliases: dict[str, set[str]] | None = None,
) -> bool:
    """Return whether two manufacturer names likely refer to the same company.

    Matching uses normalization, short-name boundary checks, and the alias
    table loaded from ``manufacturers.yaml``.
    """
    a = _normalize_manufacturer_name(input_mfr)
    b = _normalize_manufacturer_name(candidate_mfr)

    if a == b:
        return True

    # Substring/word-boundary match between the two names
    if _is_word_boundary_match(a, b) or _is_word_boundary_match(b, a):
        return True

    alias_table = aliases if aliases is not None else MANUFACTURER_ALIASES

    for src, tgt in [(a, b), (b, a)]:
        src_aliases = alias_table.get(src, set())
        if tgt in src_aliases:
            return True
        if any(
            _is_word_boundary_match(alias, tgt)
            or _is_word_boundary_match(tgt, alias)
            for alias in src_aliases
        ):
            return True

    return False


def is_non_component(mpn: str, description: str, category: str) -> bool:
    """Check if a Mouser result is an EVM, dev kit, or other non-component.

    Any of the three fields may be ``None`` from the Mouser API, so the helper
    first guards against missing values before applying regex searches.
    """
    return bool(
        (mpn and _NON_COMPONENT_MPN.search(mpn))
        or (description and _NON_COMPONENT_DESC.search(description))
        or (category and _NON_COMPONENT_CAT.search(category))
    )


def strip_qualifiers(part_number: str) -> str:
    """Strip known marketing or ordering qualifiers to derive a base part number.

    This is used for the fuzzy fallback pass where the exact orderable suffix
    is not expected to be present in the input BOM.
    """
    result = part_number
    for pattern in STRIP_SUFFIXES:
        result = re.sub(pattern, "", result)
    return result.rstrip("-")


def detect_input_qualifiers(part_number: str) -> dict[str, int]:
    """Detect weighted qualifier hints embedded in the input part number."""
    return {
        name: rule["weight"]
        for name, rule in QUALIFIER_RULES.items()
        if rule["input_pattern"].search(part_number)
    }


def score_candidate(
    candidate: dict[str, Any],
    original_pn: str,
    manufacturer: str,
) -> float:
    """Score a Mouser result for relevance to the original BOM line.

    Parameters
    ----------
    candidate:
        Raw Mouser part dictionary.
    original_pn:
        Original BOM part number before normalization.
    manufacturer:
        BOM manufacturer hint used for candidate filtering.

    Returns
    -------
    float
        Relevance score, or ``-1`` when the candidate should be discarded
        outright.

    Notes
    -----
    The scoring policy prefers manufacturer agreement, explicit part-number
    containment, real orderable Mouser part numbers, price/availability
    presence, and qualifier compatibility. It penalizes spurious automotive
    matches when the BOM did not request an automotive variant.
    """
    score = 0.0

    cand_mfr = candidate.get("Manufacturer", "")
    if manufacturers_match(manufacturer, cand_mfr):
        score += 100
    else:
        return -1

    cand_pn = candidate.get("ManufacturerPartNumber") or ""
    cand_desc = candidate.get("Description") or ""
    cand_cat = candidate.get("Category") or ""
    cand_text = f"{cand_pn} {cand_desc}"

    if is_non_component(cand_pn, cand_desc, cand_cat):
        log.debug("  Filtered non-component: %s (cat=%s)", cand_pn, cand_cat)
        return -1

    if original_pn.upper() in cand_pn.upper():
        score += 50

    if has_real_mouser_part_number(candidate):
        score += 15
    else:
        score -= 20

    if candidate.get("PriceBreaks"):
        score += 10

    input_quals = detect_input_qualifiers(original_pn)
    for qual_name, weight in input_quals.items():
        pattern = QUALIFIER_RULES[qual_name]["candidate_pattern"]
        if pattern.search(cand_text):
            score += weight
        else:
            score -= weight * 1.5

    if "automotive" not in input_quals:
        auto_pat = QUALIFIER_RULES["automotive"]["candidate_pattern"]
        if auto_pat.search(cand_text):
            score -= 15

    score -= len(cand_pn) * 0.1

    avail = candidate.get("Availability", "")
    if avail and "In Stock" in avail:
        score += 10

    return score


def has_real_mouser_part_number(candidate: MouserPart) -> bool:
    """Return whether Mouser exposes a buyable part number for a candidate."""
    mouser_pn = str(candidate.get("MouserPartNumber") or "").strip()
    return bool(mouser_pn and mouser_pn.upper() != "N/A")


def is_orderable_candidate(candidate: MouserPart) -> bool:
    """Return whether a candidate appears to be an orderable purchasable part."""
    return bool(
        has_real_mouser_part_number(candidate)
        or candidate.get("PriceBreaks")
        or candidate.get("Availability")
    )


def _normalized_mpn(part_number: str) -> str:
    """Normalize part numbers for structure-aware suffix comparisons."""
    return re.sub(r"[^A-Z0-9]", "", part_number.upper())


def _shared_prefix_length(a: str, b: str) -> int:
    """Return the length of the common prefix shared by two normalized strings."""
    i = 0
    for left, right in zip(a, b):
        if left != right:
            break
        i += 1
    return i


def is_packaging_variant(
    left: MouserPart,
    right: MouserPart,
    manufacturer: str,
) -> bool:
    """Return whether two candidates differ only by packaging suffixes.

    This prevents unnecessary manual-review prompts for common tube-vs-reel or
    other packaging-only variants where the electrical part is effectively the
    same.
    """
    left_mpn = _normalized_mpn(left.get("ManufacturerPartNumber") or "")
    right_mpn = _normalized_mpn(right.get("ManufacturerPartNumber") or "")
    if not left_mpn or not right_mpn:
        return False

    if left_mpn.endswith("Q1") and right_mpn.endswith("Q1"):
        left_mpn = left_mpn[:-2]
        right_mpn = right_mpn[:-2]

    shared = _shared_prefix_length(left_mpn, right_mpn)
    left_suffix = left_mpn[shared:]
    right_suffix = right_mpn[shared:]
    if not left_suffix or not right_suffix:
        return False

    if len(left_suffix) > 2 or len(right_suffix) > 2:
        return False

    if (
        left_suffix not in _PACKAGING_SUFFIX_TOKENS
        or right_suffix not in _PACKAGING_SUFFIX_TOKENS
    ):
        return False

    left_package, _ = extract_package_info(left, manufacturer)
    right_package, _ = extract_package_info(right, manufacturer)
    if left_package and right_package and left_package != right_package:
        return False

    return True


def requires_manual_review(
    scored: list[ScoredCandidate],
    method: MatchMethod,
    manufacturer: str,
) -> bool:
    """Return whether the top fuzzy match is still materially ambiguous.

    Ambiguity is currently defined as a fuzzy lookup where the score gap to the
    runner-up is small and the runner-up is not merely a packaging-only
    variant.
    """
    if method != MatchMethod.FUZZY or not scored:
        return False
    if len(scored) == 1:
        return False

    top = scored[0]
    runner_up = scored[1]
    if is_packaging_variant(top.part, runner_up.part, manufacturer):
        return False

    score_gap = top.score - runner_up.score
    return score_gap < 10.0


# ---------------------------------------------------------------------------
# Multi-pass lookup
# ---------------------------------------------------------------------------

def _build_lookup_passes(part_number: str, base_pn: str) -> list[LookupPass]:
    """Build the ordered lookup passes used for one part-number search.

    Qualifier-style BOM part numbers such as ``-Q1`` or ``/NOPB`` skip the
    initial exact pass. A full-string ``BeginsWith`` lookup is typically
    enough to catch the same orderables while avoiding one redundant network
    call on cold runs.
    """
    if base_pn != part_number:
        return [
            LookupPass(part_number, "BeginsWith", MatchMethod.BEGINS_WITH),
            LookupPass(base_pn, "BeginsWith", MatchMethod.FUZZY),
        ]

    return [
        LookupPass(part_number, "Exact", MatchMethod.EXACT),
        LookupPass(part_number, "BeginsWith", MatchMethod.BEGINS_WITH),
    ]


def _resolve_mouser_api_keys(api_key: str = "") -> tuple[str, ...]:
    """Return the configured Mouser API keys in priority order."""
    explicit = api_key.strip()
    if explicit:
        return (explicit,)

    configured = get_secret_values("mouser_api_keys")
    if configured:
        return tuple(dict.fromkeys(configured))

    single = get_secret("mouser_api_key")
    return (single,) if single else ()


def _run_pass(
    client: MouserClient,
    lookup_pass: LookupPass,
    original_pn: str,
    manufacturer: str,
) -> list[ScoredCandidate]:
    """Run one lookup pass and return candidates sorted by descending score."""
    log.debug("  %s '%s'", lookup_pass.search_option, lookup_pass.search_term)
    parts = client.search(lookup_pass.search_term, lookup_pass.search_option)
    log.debug("  → %d raw results", len(parts))

    if not parts:
        return []

    scored = [
        ScoredCandidate(part, score)
        for part in parts
        for score in [score_candidate(part, original_pn, manufacturer)]
        if score >= 0
    ]
    scored.sort(key=lambda item: item.score, reverse=True)

    log.debug("  → %d after filter", len(scored))
    if scored:
        log.debug(
            "  → Winner: %s (score %.1f)",
            scored[0].part.get("ManufacturerPartNumber"),
            scored[0].score,
        )
    return scored


def smart_lookup(
    part_number: str, manufacturer: str, client: MouserClient
) -> LookupResult:
    """Run the multi-pass lookup pipeline for one BOM part number.

    Parameters
    ----------
    part_number:
        Original BOM part number.
    manufacturer:
        BOM manufacturer hint.
    client:
        Active Mouser client used to execute the searches.

    Returns
    -------
    LookupResult
        Best available result from exact, begins-with, and fuzzy fallback
        passes, including ambiguity metadata and the ranked candidate shortlist.
    """
    base_pn = strip_qualifiers(part_number)
    lookup_passes = _build_lookup_passes(part_number, base_pn)
    fallback: LookupResult | None = None

    if base_pn == part_number:
        log.debug("  Fuzzy pass skipped (no qualifiers to strip)")

    for i, lookup_pass in enumerate(lookup_passes):
        if i > 0 and not _lookup_is_cached(client, lookup_pass):
            time.sleep(0.3)

        log.debug("Pass %d: %s", i + 1, lookup_pass.method.value)
        scored = _run_pass(client, lookup_pass, part_number, manufacturer)

        if not scored:
            continue

        result = LookupResult(
            part=scored[0].part,
            method=lookup_pass.method,
            candidate_count=len(scored),
            review_required=requires_manual_review(
                scored, lookup_pass.method, manufacturer
            ),
            candidates=tuple(scored),
        )

        if is_orderable_candidate(scored[0].part):
            return result

        if fallback is None:
            fallback = result
        log.debug(
            "  Best %s result is not orderable yet (%s), continuing search",
            lookup_pass.method.value,
            scored[0].part.get("ManufacturerPartNumber"),
        )

    if fallback is not None:
        return fallback
    log.debug("All passes exhausted — no match")
    return LookupResult(None, MatchMethod.NOT_FOUND, 0)


# ---------------------------------------------------------------------------
# Price break selection
# ---------------------------------------------------------------------------


def best_price_break(price_breaks: list[dict], quantity: int) -> dict | None:
    """Select the best price break for the requested quantity.

    The function chooses the highest break not exceeding the requested
    quantity. If every break exceeds the requested quantity, it falls back to
    the smallest break rather than returning no price at all.
    """
    applicable = [
        pb for pb in price_breaks if int(pb.get("Quantity", 0)) <= quantity
    ]

    if not applicable:
        if price_breaks:
            return min(price_breaks, key=lambda pb: int(pb.get("Quantity", 0)))
        return None

    return max(applicable, key=lambda pb: int(pb.get("Quantity", 0)))


def _append_lookup_error(priced: PricedPart, message: str) -> None:
    """Append a lookup or pricing note without discarding earlier context."""
    if priced.lookup_error:
        priced.lookup_error = f"{priced.lookup_error}; {message}"
    else:
        priced.lookup_error = message


def _apply_package_info(
    priced: PricedPart, mouser_part: MouserPart, manufacturer: str
) -> None:
    """Populate inferred package metadata when the BOM omitted it."""
    if priced.package and priced.pins is not None:
        return

    package, pins = extract_package_info(mouser_part, manufacturer)
    if package and not priced.package:
        priced.package = package
    if pins is not None and priced.pins is None:
        priced.pins = pins


def _apply_price_break(
    priced: PricedPart, price_breaks: list[dict], quantity: int
) -> None:
    """Apply the best matching price break to a priced part record."""
    best = best_price_break(price_breaks, quantity)
    if not best:
        _append_lookup_error(priced, "No price breaks available")
        return

    unit_price = parse_price(str(best.get("Price", "")))
    if unit_price is None:
        _append_lookup_error(priced, f"Failed to parse price: {best.get('Price')}")
        return

    priced.unit_price = unit_price
    priced.extended_price = round(unit_price * quantity, 2)
    priced.currency = best.get("Currency", "EUR")
    priced.price_break_quantity = int(best.get("Quantity", 0))


def _can_prompt_interactively() -> bool:
    """Return whether stdin/stdout support an interactive terminal flow."""
    return sys.stdin.isatty() and sys.stdout.isatty()


def _candidate_unit_price(candidate: ScoredCandidate, quantity: int) -> tuple[str, str]:
    """Return printable unit-price text and currency for one candidate."""
    best = best_price_break(candidate.part.get("PriceBreaks", []), quantity)
    if not best:
        return "—", ""

    value = parse_price(str(best.get("Price", "")))
    if value is None:
        return "—", str(best.get("Currency", "") or "")

    return f"{value:.4f}", str(best.get("Currency", "") or "")


def _candidate_package(candidate: ScoredCandidate, manufacturer: str) -> tuple[str, str]:
    """Return printable package metadata for a candidate."""
    package, pins = extract_package_info(candidate.part, manufacturer)
    package_text = package or "—"
    pins_text = str(pins) if pins is not None else "—"
    return package_text, pins_text


def _saved_resolution_for(
    agg: AggregatedPart,
    lookup: LookupResult,
    resolution_store: Any | None,
) -> LookupResult:
    """Apply a previously saved manual resolution when it matches a candidate.

    Saved resolutions are considered before AI or interactive review because
    they represent prior human-confirmed decisions.
    """
    if resolution_store is None:
        return lookup

    record = resolution_store.get(agg.manufacturer, agg.part_number)
    if record is None:
        return lookup

    for candidate in lookup.candidates:
        if record.matches(candidate.part):
            log.debug(
                "  Applied saved resolution for %s -> %s",
                agg.part_number,
                record.mouser_part_number,
            )
            return replace(
                lookup,
                part=candidate.part,
                review_required=False,
                resolution_source="saved",
            )

    return lookup


def _saved_resolution_fast_path(
    agg: AggregatedPart,
    client: MouserClient,
    resolution_store: Any | None,
) -> LookupResult | None:
    """Resolve a part directly from a saved mapping before normal lookup.

    The fast path uses the persisted Mouser or manufacturer part number from a
    previous confirmed resolution. This can collapse a repeat lookup from the
    normal multi-pass search down to one exact query, or zero network requests
    when that exact query is already cached.
    """
    if resolution_store is None:
        return None

    record = resolution_store.get(agg.manufacturer, agg.part_number)
    if record is None:
        return None

    search_terms: list[str] = []
    if record.mouser_part_number:
        search_terms.append(record.mouser_part_number)
    if (
        record.manufacturer_part_number
        and record.manufacturer_part_number not in search_terms
    ):
        search_terms.append(record.manufacturer_part_number)

    for search_term in search_terms:
        log.debug("  Saved-resolution fast path Exact '%s'", search_term)
        parts = client.search(search_term, "Exact")
        matching_parts = [
            part
            for part in parts
            if record.matches(part)
            and manufacturers_match(
                agg.manufacturer,
                str(part.get("Manufacturer") or ""),
            )
        ]
        if not matching_parts:
            continue

        selected = next(
            (part for part in matching_parts if is_orderable_candidate(part)),
            matching_parts[0],
        )
        return LookupResult(
            part=selected,
            method=MatchMethod.EXACT,
            candidate_count=1,
            review_required=False,
            candidates=(ScoredCandidate(selected, float("inf")),),
            resolution_source="saved",
        )

    return None


def _ai_resolution_for(
    agg: AggregatedPart,
    lookup: LookupResult,
    ai_resolver: Any | None,
) -> tuple[LookupResult, str | None]:
    """Apply the optional AI reranker before falling back to user review.

    Returns
    -------
    tuple[LookupResult, str | None]
        Potentially updated lookup result plus an optional diagnostic note to
        append if the AI abstained or failed.
    """
    if ai_resolver is None or not lookup.review_required or not lookup.candidates:
        return lookup, None

    try:
        decision = ai_resolver.rerank(agg, lookup)
    except Exception as e:
        log.warning("AI resolver failed for %s: %s", agg.part_number, e)
        return lookup, f"AI resolver failed: {e}"

    if decision is None:
        return lookup, None

    if decision.is_select:
        selected = lookup.candidates[decision.selected_index - 1]
        log.debug(
            "  AI selected %s for %s with confidence %.2f",
            selected.part.get("ManufacturerPartNumber"),
            agg.part_number,
            decision.confidence,
        )
        return (
            replace(
                lookup,
                part=selected.part,
                review_required=False,
                resolution_source="ai",
            ),
            None,
        )

    note = f"AI resolver abstained: {decision.rationale}"
    if decision.missing_context:
        note = f"{note}. Missing context: {', '.join(decision.missing_context)}"
    log.debug("  %s", note)
    return lookup, note


def _interactive_resolution_prompt(
    agg: AggregatedPart,
    lookup: LookupResult,
    resolution_store: Any | None,
    page_size: int = 8,
) -> LookupResult:
    """Prompt the user to choose a candidate for an ambiguous part.

    The terminal UI is intentionally compact but information-rich: it shows the
    current BOM hints, suggested candidate, paged candidate list, package/pin
    inference, quantity-aware unit price, and availability.
    """
    if lookup.part is not None and not lookup.review_required:
        return lookup
    if not lookup.candidates or not _can_prompt_interactively():
        return lookup

    total = len(lookup.candidates)
    page = 0

    while True:
        start = page * page_size
        end = min(start + page_size, total)
        print()
        print("=" * 78)
        print(f"Interactive resolver: {agg.part_number} ({agg.manufacturer})")
        if agg.description:
            print(f"  {agg.description}")
        if agg.package or agg.pins is not None:
            print(
                f"  BOM hints: package={agg.package or '—'} pins={agg.pins if agg.pins is not None else '—'}"
            )
        suggested = lookup.part.get("ManufacturerPartNumber") if lookup.part else "—"
        print(f"  Suggested: {suggested} [{lookup.method.value}]")
        print()
        print("  #  Manufacturer PN          Package      Pins    Unit      Availability")
        print("  -- ------------------------ ------------ ---- ---------- ----------------")

        for idx in range(start, end):
            candidate = lookup.candidates[idx]
            package_text, pins_text = _candidate_package(candidate, agg.manufacturer)
            unit_text, currency = _candidate_unit_price(candidate, agg.total_quantity)
            availability = str(candidate.part.get("Availability") or "—")
            availability = availability[:16]
            print(
                f"  {idx + 1:>2} "
                f"{str(candidate.part.get('ManufacturerPartNumber') or '—')[:24]:24} "
                f"{package_text[:12]:12} "
                f"{pins_text:>4} "
                f"{(unit_text + (' ' + currency if currency else ''))[:10]:10} "
                f"{availability:16}"
            )

        if total > page_size:
            print(f"\n  Showing candidates {start + 1}-{end} of {total}")
        print("  Commands: [number] choose, a accept suggested, n/p page, s skip, q quit")

        try:
            choice = input("  Selection> ").strip().lower()
        except EOFError:
            return lookup
        except KeyboardInterrupt as e:
            raise SystemExit(130) from e

        if not choice:
            continue
        if choice == "a" and lookup.part is not None:
            selected = lookup.candidates[0]
        elif choice == "s":
            return lookup
        elif choice == "n" and end < total:
            page += 1
            continue
        elif choice == "p" and page > 0:
            page -= 1
            continue
        elif choice == "q":
            raise SystemExit(130)
        elif choice.isdigit():
            index = int(choice) - 1
            if 0 <= index < total:
                selected = lookup.candidates[index]
            else:
                print("  Invalid candidate number.")
                continue
        else:
            print("  Unknown command.")
            continue

        if resolution_store is not None:
            resolution_store.set(
                agg.manufacturer,
                agg.part_number,
                str(selected.part.get("MouserPartNumber") or ""),
                str(selected.part.get("ManufacturerPartNumber") or ""),
            )
        return replace(
            lookup,
            part=selected.part,
            review_required=False,
            resolution_source="interactive",
        )


# ---------------------------------------------------------------------------
# High-level pricing
# ---------------------------------------------------------------------------


def price_part(
    agg: AggregatedPart,
    client: MouserClient,
    interactive: bool = False,
    resolution_store: Any | None = None,
    ai_resolver: Any | None = None,
) -> PricedPart:
    """Resolve one aggregated part into a priced distributor record.

    This is the high-level single-part pipeline used by the CLI:

    1. deterministic lookup
    2. saved manual resolution reuse
    3. optional AI reranking
    4. optional interactive user selection
    5. package and price-break enrichment
    """
    priced = PricedPart.from_aggregated(agg)

    try:
        ai_note: str | None = None
        lookup = _saved_resolution_fast_path(agg, client, resolution_store)
        if lookup is None:
            lookup = smart_lookup(agg.part_number, agg.manufacturer, client)
            lookup = _saved_resolution_for(agg, lookup, resolution_store)
        lookup, ai_note = _ai_resolution_for(agg, lookup, ai_resolver)
        if interactive:
            lookup = _interactive_resolution_prompt(agg, lookup, resolution_store)
        priced.match_method = lookup.method
        priced.match_candidates = lookup.candidate_count
        priced.resolution_source = lookup.resolution_source
        priced.review_required = lookup.review_required

        if lookup.part is None:
            priced.lookup_error = (
                "No results found on Mouser (tried exact, begins_with, fuzzy)"
            )
            return priced

        mouser_part = lookup.part
        priced.mouser_part_number = mouser_part.get("MouserPartNumber")
        priced.availability = mouser_part.get("Availability")

        _apply_package_info(priced, mouser_part, agg.manufacturer)

        if lookup.review_required and lookup.resolution_source is None:
            matched_mpn = mouser_part.get("ManufacturerPartNumber", "")
            _append_lookup_error(
                priced,
                f"Fuzzy match: resolved to {matched_mpn} "
                f"({lookup.candidate_count} candidates) — verify manually",
            )
            if ai_note:
                _append_lookup_error(priced, ai_note)

        _apply_price_break(priced, mouser_part.get("PriceBreaks", []), agg.total_quantity)

    except httpx.HTTPStatusError as e:
        priced.lookup_error = f"HTTP {e.response.status_code}: {e.response.text[:200]}"
    except Exception as e:
        log.exception("Unexpected error pricing %s", agg.part_number)
        priced.lookup_error = str(e)

    return priced


def price_all_parts(
    parts: list[AggregatedPart],
    client: MouserClient,
    delay: float = 1.0,
    interactive: bool = False,
    resolution_store: Any | None = None,
    ai_resolver: Any | None = None,
) -> list[PricedPart]:
    """Resolve pricing for every aggregated part in the BOM.

    The function is responsible for user-facing progress output and for
    applying the configured per-part delay only when a real network request was
    required for that part.
    """
    results = []
    total = len(parts)

    for i, agg in enumerate(parts, 1):
        print(f"  [{i}/{total}] Looking up {agg.part_number}...")
        before_requests = getattr(client, "network_requests", None)
        priced = price_part(
            agg,
            client,
            interactive=interactive,
            resolution_store=resolution_store,
            ai_resolver=ai_resolver,
        )

        method = priced.match_method
        mpn = priced.mouser_part_number or "—"
        if priced.resolution_source == "saved":
            print(f"           ✓ Saved resolution → {mpn}")
        elif priced.resolution_source == "ai":
            print(f"           ✓ AI-reranked match → {mpn}")
        elif priced.resolution_source == "interactive":
            print(f"           ✓ Interactive selection → {mpn}")
        elif method == MatchMethod.EXACT:
            print(f"           ✓ Exact match → {mpn}")
        elif method == MatchMethod.BEGINS_WITH:
            print(f"           ~ BeginsWith match → {mpn} ({priced.match_candidates} candidates)")
        elif method == MatchMethod.FUZZY:
            if priced.review_required:
                print(
                    f"           ⚠ Fuzzy match → {mpn} "
                    f"({priced.match_candidates} candidates) — review!"
                )
            else:
                print(
                    f"           ~ Fuzzy-resolved match → {mpn} "
                    f"({priced.match_candidates} candidates)"
                )
        elif priced.lookup_error:
            detail = priced.lookup_error.splitlines()[0][:90]
            print(f"           ✗ Lookup failed → {detail}")
        else:
            print(f"           ✗ No match found")

        results.append(priced)

        used_network = before_requests is None or (
            getattr(client, "network_requests", before_requests) > before_requests
        )
        if i < total and delay > 0 and used_network:
            time.sleep(delay)

    return results


def _lookup_is_cached(client: MouserClient, lookup_pass: LookupPass) -> bool:
    """Return whether the upcoming lookup pass can be served from cache."""
    checker = getattr(client, "has_cached_search", None)
    return bool(callable(checker) and checker(lookup_pass.search_term, lookup_pass.search_option))


def _mouser_error_details(response: httpx.Response) -> tuple[str, str]:
    """Return the normalized Mouser error code and message when available."""
    code = ""
    message = ""
    try:
        payload = response.json()
    except ValueError:
        payload = None

    if isinstance(payload, dict):
        errors = payload.get("Errors")
        if isinstance(errors, list) and errors:
            first = errors[0]
            if isinstance(first, dict):
                code = str(first.get("Code") or "").strip()
                message = str(first.get("Message") or "").strip()

    if not message:
        message = response.text.strip()

    return code.lower(), message.lower()


def _is_mouser_daily_limit_error(response: httpx.Response) -> bool:
    """Return whether the response indicates the documented daily quota hit."""
    if response.status_code != 403:
        return False
    code, message = _mouser_error_details(response)
    return code == "toomanyrequests" and "per day" in message


def _is_retryable_rate_limit(response: httpx.Response) -> bool:
    """Return whether the response looks like a transient retryable throttle."""
    if response.status_code == 429:
        return True
    if response.status_code != 403:
        return False
    code, message = _mouser_error_details(response)
    return code == "toomanyrequests" and "per day" not in message
