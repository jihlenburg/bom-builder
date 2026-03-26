"""Mouser candidate matching, scoring, and qualification rules.

This module owns the heuristics that turn raw Mouser search results into
scored, filtered, and classified candidates.  The scoring policy is built
around several domain-specific observations about electronic component part
numbers:

* Manufacturer agreement is the strongest signal — a mismatched manufacturer
  is an immediate disqualification (score = -1).
* Part-number containment rewards candidates whose orderable MPN contains the
  original BOM text, which handles the common case where BOM text is a
  shortened form of the full orderable.
* Qualifier awareness (``-Q1``, ``/NOPB``, ``-EP``, ``-TR``) allows the
  scorer to reward or penalize candidates based on whether their qualifiers
  match what the BOM implies.
* Non-component filtering (EVMs, dev kits, evaluation boards) removes results
  that are search-relevant but not purchasable components.

The module also owns the manufacturer-alias table loaded from
``manufacturers.yaml``, which is shared across the codebase for fuzzy
manufacturer-name matching.
"""

import logging
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml

from config import DATA_DIR
from manufacturer_packaging import _normalize_manufacturer_name
from models import MatchMethod
from package import extract_package_info

log = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Type alias — raw Mouser result dictionaries
# ---------------------------------------------------------------------------

type MouserPart = dict[str, Any]

# ---------------------------------------------------------------------------
# Packaging suffix tokens — used by is_packaging_variant to detect tube-vs-reel
# or other packaging-only orderable differences.
# ---------------------------------------------------------------------------

_PACKAGING_SUFFIX_TOKENS = {
    "R",
    "T",
    "TR",
    "M",
    "RE",
    "TE",
}


# ---------------------------------------------------------------------------
# Data classes
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ScoredCandidate:
    """One Mouser result paired with its computed relevance score.

    The resolver keeps the original raw Mouser part payload intact and stores
    the heuristic score alongside it, allowing later stages to inspect both the
    score and the underlying distributor metadata.
    """

    part: MouserPart
    score: float


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

    # TI hard gate: when the BOM specifies a Q1 (automotive-qualified) part,
    # non-Q1 candidates are a different qualification tier entirely — never
    # acceptable as substitutes.  TI's Q1 suffix is always appended to the
    # orderable MPN (e.g. "LM2775-Q1" → "LM2775QDSGRQ1"), so checking
    # whether the candidate MPN ends with "Q1" is definitive.
    if "automotive" in input_quals:
        norm_mfr = _normalize_manufacturer_name(manufacturer)
        if norm_mfr in ("texas instruments", "ti"):
            if not cand_pn.upper().rstrip().endswith("Q1"):
                log.debug("  TI Q1 hard gate: %s lacks Q1 suffix", cand_pn)
                return -1

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


def collapse_packaging_variants(
    scored: list[ScoredCandidate] | tuple[ScoredCandidate, ...],
    manufacturer: str,
) -> list[ScoredCandidate]:
    """Deduplicate candidates that differ only by packaging (reel size, etc.).

    Groups all candidates into packaging-variant equivalence classes using
    :func:`is_packaging_variant`, then returns one representative per group —
    the highest-scored member.  The result preserves the original score
    ordering.

    This is used before showing the interactive resolver so users choose
    between electrically distinct parts (e.g. automotive Q1 vs. commercial)
    rather than between reel sizes that ``_auto_select_packaging_variant()``
    will optimise later anyway.

    Parameters
    ----------
    scored:
        Candidates sorted by descending score (as returned by the scoring
        pipeline).
    manufacturer:
        Manufacturer name for package-info comparison inside
        ``is_packaging_variant()``.

    Returns
    -------
    list[ScoredCandidate]
        One representative per packaging-variant group, in original score
        order.
    """
    if not scored:
        return []

    # Each candidate is assigned to a group.  The first candidate seen in
    # score order becomes the representative of its group.  Later candidates
    # that are packaging variants of an existing representative are dropped.
    representatives: list[ScoredCandidate] = []
    for candidate in scored:
        is_variant = False
        for rep in representatives:
            if is_packaging_variant(candidate.part, rep.part, manufacturer):
                is_variant = True
                break
        if not is_variant:
            representatives.append(candidate)
    return representatives


def requires_manual_review(
    scored: list[ScoredCandidate],
    method: MatchMethod,
    manufacturer: str,
) -> bool:
    """Return whether the top fuzzy match is still materially ambiguous.

    Ambiguity is defined as a fuzzy lookup where, after collapsing packaging
    variants (tube/reel/tape differences), there are still multiple distinct
    candidates with a small score gap.  Packaging variants are handled
    automatically by ``_auto_select_packaging_variant()`` in the pricing
    pipeline, so they never constitute meaningful ambiguity.
    """
    if method != MatchMethod.FUZZY or not scored:
        return False
    if len(scored) == 1:
        return False

    # Collapse packaging variants so reel-size differences don't trigger
    # the interactive resolver needlessly.
    collapsed = collapse_packaging_variants(scored, manufacturer)
    if len(collapsed) <= 1:
        return False

    top = collapsed[0]
    runner_up = collapsed[1]
    score_gap = top.score - runner_up.score
    return score_gap < 10.0
