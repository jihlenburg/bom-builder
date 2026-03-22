"""OpenAI-powered candidate reranking for ambiguous distributor matches.

This module implements the optional AI stage that sits between deterministic
Mouser lookup and interactive human review. It never invents new part numbers;
instead, it receives the shortlist already produced by the deterministic
resolver and asks an OpenAI model to either:

- select one of the provided candidates, or
- abstain when the BOM line is still underspecified

The API contract is intentionally narrow and uses structured JSON output so the
rest of the pricing pipeline can treat the AI stage as just another
deterministic decision provider with confidence and rationale metadata.
"""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from typing import Any

import httpx

from models import AggregatedPart
from mouser import LookupResult, ScoredCandidate, best_price_break, parse_price
from package import extract_package_info
from secret_store import get_secret

log = logging.getLogger(__name__)

OPENAI_RESPONSES_URL = "https://api.openai.com/v1/responses"
DEFAULT_AI_MODEL = "gpt-5.4-mini"
DEFAULT_MAX_CANDIDATES = 12
DEFAULT_TIMEOUT = 30.0
DEFAULT_MAX_OUTPUT_TOKENS = 400
DEFAULT_REASONING_EFFORT = "none"
DEFAULT_VERBOSITY = "low"
SCHEMA_NAME = "bom_candidate_rerank"


@dataclass(frozen=True)
class AIRerankDecision:
    """Normalized decision returned by the AI reranker.

    Attributes
    ----------
    decision:
        Either ``"select"`` or ``"abstain"``.
    selected_index:
        One-based candidate index chosen by the model, or ``0`` when the
        model abstains.
    confidence:
        Model-reported confidence in the decision, normalized to ``[0, 1]``.
    rationale:
        Human-readable explanation of why the model selected or abstained.
    missing_context:
        Tuple of context items the model believes would be needed to make a
        safe automatic selection.
    """

    decision: str
    selected_index: int
    confidence: float
    rationale: str
    missing_context: tuple[str, ...]

    @property
    def is_select(self) -> bool:
        """Return ``True`` when the decision is a concrete candidate choice."""
        return self.decision == "select" and self.selected_index > 0


class OpenAIResolver:
    """Thin OpenAI Responses API client specialized for BOM reranking.

    The class owns the HTTP client, request payload construction, and the
    decision-validation policy used before the result is allowed to affect the
    pricing pipeline.
    """

    def __init__(
        self,
        api_key: str = "",
        model: str = DEFAULT_AI_MODEL,
        confidence_threshold: float = 0.85,
        max_candidates: int = DEFAULT_MAX_CANDIDATES,
        timeout: float = DEFAULT_TIMEOUT,
    ):
        """Initialize the reranker client.

        Parameters
        ----------
        api_key:
            Explicit OpenAI API key override. When omitted, the resolver uses
            :func:`secret_store.get_secret` to read ``OPENAI_API_KEY``.
        model:
            OpenAI model name used for reranking.
        confidence_threshold:
            Minimum confidence required before an AI ``select`` decision is
            accepted automatically.
        max_candidates:
            Maximum number of deterministic candidates exposed to the model.
            This keeps prompts bounded even when distributor search returns
            very large ambiguous families.
        timeout:
            Per-request HTTP timeout in seconds.

        Raises
        ------
        ValueError
            If no OpenAI API key can be resolved.
        """
        self.api_key = api_key or get_secret("openai_api_key")
        if not self.api_key:
            raise ValueError(
                "OpenAI API key not set. Set OPENAI_API_KEY in the environment or .env."
            )
        self.model = model
        self.confidence_threshold = confidence_threshold
        self.max_candidates = max_candidates
        self._client = httpx.Client(timeout=timeout)
        self._disabled_reason = ""

    def close(self) -> None:
        """Close the underlying HTTP client."""
        self._client.close()

    def __enter__(self) -> "OpenAIResolver":
        """Enter context-manager usage and return ``self``."""
        return self

    def __exit__(self, *exc: Any) -> None:
        """Close client resources when leaving a ``with`` block."""
        self.close()

    def rerank(
        self,
        agg: AggregatedPart,
        lookup: LookupResult,
    ) -> AIRerankDecision | None:
        """Ask OpenAI to pick one candidate or abstain.

        Parameters
        ----------
        agg:
            Aggregated BOM line currently being resolved.
        lookup:
            Deterministic lookup result whose candidate shortlist will be
            exposed to the model.

        Returns
        -------
        AIRerankDecision | None
            Parsed and validated model decision, or ``None`` when there are no
            candidates to rerank.

        Notes
        -----
        Authentication failures permanently disable the AI stage for the
        current process so the resolver does not repeat the same failing
        request for every remaining ambiguous part.
        """
        if self._disabled_reason:
            return _abstain_decision(f"AI resolver disabled: {self._disabled_reason}")
        if not lookup.candidates:
            return None

        shortlist = tuple(lookup.candidates[: self.max_candidates])
        response = self._client.post(
            OPENAI_RESPONSES_URL,
            headers=_request_headers(self.api_key),
            json=self._build_payload(agg, lookup, shortlist),
        )
        try:
            response.raise_for_status()
        except httpx.HTTPStatusError as e:
            if e.response.status_code in {401, 403}:
                self._disabled_reason = f"HTTP {e.response.status_code}"
            raise
        decision = _parse_decision_response(response.json())
        return _validate_decision(decision, len(shortlist), self.confidence_threshold)

    def _build_payload(
        self,
        agg: AggregatedPart,
        lookup: LookupResult,
        shortlist: tuple[ScoredCandidate, ...],
    ) -> dict[str, Any]:
        """Build the structured Responses API payload for one rerank request."""
        prompt = _build_prompt(agg, lookup, shortlist)
        return {
            "model": self.model,
            "input": prompt,
            "max_output_tokens": DEFAULT_MAX_OUTPUT_TOKENS,
            "reasoning": {"effort": DEFAULT_REASONING_EFFORT},
            "text": {
                "verbosity": DEFAULT_VERBOSITY,
                "format": {
                    "type": "json_schema",
                    "name": SCHEMA_NAME,
                    "strict": True,
                    "schema": _decision_schema(len(shortlist)),
                }
            },
        }


def _request_headers(api_key: str) -> dict[str, str]:
    """Return standard HTTP headers for a JSON Responses API call."""
    return {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
    }


def _decision_schema(max_candidates: int) -> dict[str, Any]:
    """Return the JSON schema used for structured rerank decisions.

    Parameters
    ----------
    max_candidates:
        Highest legal one-based candidate index for this request.
    """
    return {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "decision": {
                "type": "string",
                "enum": ["select", "abstain"],
            },
            "selected_index": {
                "type": "integer",
                "minimum": 0,
                "maximum": max_candidates,
            },
            "confidence": {
                "type": "number",
                "minimum": 0,
                "maximum": 1,
            },
            "rationale": {"type": "string"},
            "missing_context": {
                "type": "array",
                "items": {"type": "string"},
            },
        },
        "required": [
            "decision",
            "selected_index",
            "confidence",
            "rationale",
            "missing_context",
        ],
    }


def _abstain_decision(
    rationale: str,
    *,
    confidence: float = 0.0,
    missing_context: tuple[str, ...] = (),
) -> AIRerankDecision:
    """Build a normalized abstention decision.

    This helper keeps abstention responses structurally identical regardless of
    whether they come from the model, validation policy, or a local failure
    path.
    """
    return AIRerankDecision(
        decision="abstain",
        selected_index=0,
        confidence=confidence,
        rationale=rationale,
        missing_context=missing_context,
    )


def _parse_decision_response(response_json: dict[str, Any]) -> AIRerankDecision:
    """Parse a structured rerank decision from a Responses API payload.

    Parameters
    ----------
    response_json:
        Decoded JSON object returned by the OpenAI Responses API.

    Returns
    -------
    AIRerankDecision
        Parsed structured decision payload.
    """
    output_text = _response_output_text(response_json)
    decision_json = json.loads(output_text)
    return AIRerankDecision(
        decision=str(decision_json["decision"]),
        selected_index=int(decision_json["selected_index"]),
        confidence=float(decision_json["confidence"]),
        rationale=str(decision_json["rationale"]),
        missing_context=tuple(str(item) for item in decision_json["missing_context"]),
    )


def _validate_decision(
    decision: AIRerankDecision,
    shortlist_size: int,
    confidence_threshold: float,
) -> AIRerankDecision:
    """Normalize low-confidence or invalid selections into abstentions.

    Parameters
    ----------
    decision:
        Raw structured decision returned by the model.
    shortlist_size:
        Number of candidates that were actually presented to the model.
    confidence_threshold:
        Minimum confidence required for automatic acceptance of a selection.
    """
    if decision.is_select and decision.confidence < confidence_threshold:
        return _abstain_decision(
            (
                f"confidence {decision.confidence:.2f} below threshold "
                f"{confidence_threshold:.2f}; {decision.rationale}"
            ),
            confidence=decision.confidence,
            missing_context=decision.missing_context,
        )

    if decision.is_select and decision.selected_index > shortlist_size:
        return _abstain_decision(
            "model selected a candidate outside the provided shortlist",
        )

    return decision


def _response_output_text(response_json: dict[str, Any]) -> str:
    """Extract the model's structured JSON text from a Responses payload.

    The Responses API can place output text either in the top-level
    ``output_text`` convenience field or inside ``output[*].content[*]`` items.
    This helper hides those transport details from the rest of the resolver.

    Raises
    ------
    ValueError
        If the response completed without a usable message payload or if the
        API reported an incomplete run.
    """
    output_text = str(response_json.get("output_text") or "").strip()
    if output_text:
        return output_text

    for item in response_json.get("output", []):
        for content in item.get("content", []):
            text = str(content.get("text") or "").strip()
            if text:
                return text

            json_value = content.get("json")
            if isinstance(json_value, (dict, list)):
                return json.dumps(json_value)

    if response_json.get("status") == "incomplete":
        details = response_json.get("incomplete_details") or {}
        reason = str(details.get("reason") or "unknown")
        raise ValueError(f"OpenAI response incomplete: {reason}")

    raise ValueError("OpenAI response did not include output_text")


def _build_prompt(
    agg: AggregatedPart,
    lookup: LookupResult,
    shortlist: tuple[ScoredCandidate, ...],
) -> str:
    """Build the prompt shown to the OpenAI model.

    The prompt deliberately constrains the model to reranking only and makes
    abstention the correct behavior whenever the BOM line lacks enough context
    to disambiguate real electrical variants.
    """
    candidates = [_candidate_payload(index, agg, candidate) for index, candidate in enumerate(shortlist, start=1)]
    bom_hints = {
        "part_number": agg.part_number,
        "manufacturer": agg.manufacturer,
        "description": agg.description or "",
        "package": agg.package or "",
        "pins": agg.pins,
        "quantity_per_unit": agg.quantity_per_unit,
        "total_quantity": agg.total_quantity,
        "current_match_method": lookup.method.value,
        "candidate_count": lookup.candidate_count,
    }
    instructions = (
        "You are reranking distributor candidates for an electrical BOM.\n"
        "Choose only from the provided candidates.\n"
        "If the BOM text does not distinguish real electrical variants such as output voltage, gain, "
        "or package choice, abstain instead of guessing.\n"
        "Treat reel vs tube and obvious packaging suffixes as packaging-only when the package and device are otherwise the same.\n"
        "Prefer candidates that match the manufacturer, family, automotive qualifier, BOM package/pin hints, and orderable MPN structure.\n"
        "Return decision='select' with selected_index set to the chosen 1-based candidate number only when the evidence is strong.\n"
        "Return decision='abstain' and selected_index=0 when the BOM is underspecified."
    )
    return (
        f"{instructions}\n\n"
        f"BOM line:\n{json.dumps(bom_hints, indent=2, ensure_ascii=True)}\n\n"
        f"Candidates:\n{json.dumps(candidates, indent=2, ensure_ascii=True)}"
    )


def _candidate_payload(
    index: int,
    agg: AggregatedPart,
    candidate: ScoredCandidate,
) -> dict[str, Any]:
    """Serialize one candidate into the prompt-friendly payload format.

    The payload mixes the deterministic score, Mouser metadata, package hints,
    and quantity-aware pricing so the model can reason about likely equivalence
    without needing direct access to the raw distributor schema.
    """
    package, pins = extract_package_info(candidate.part, agg.manufacturer)
    best = best_price_break(candidate.part.get("PriceBreaks", []), agg.total_quantity)
    raw_price = str(best.get("Price") or "") if best else ""
    unit_price = parse_price(raw_price) if raw_price else None
    return {
        "index": index,
        "score": round(candidate.score, 2),
        "manufacturer_part_number": str(candidate.part.get("ManufacturerPartNumber") or ""),
        "mouser_part_number": str(candidate.part.get("MouserPartNumber") or ""),
        "package": package or "",
        "pins": pins,
        "availability": str(candidate.part.get("Availability") or ""),
        "unit_price": unit_price,
        "currency": str(best.get("Currency") or "") if best else "",
        "description": str(candidate.part.get("Description") or ""),
    }
