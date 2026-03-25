"""Tests for the optional OpenAI reranker."""

import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import ai_resolver
from ai_resolver import OpenAIResolver
from models import AggregatedPart, MatchMethod
from mouser import LookupResult, ScoredCandidate


class _FakeResponse:
    def __init__(self, payload, status_code=200):
        self._payload = payload
        self.status_code = status_code
        self.request = ai_resolver.httpx.Request("POST", ai_resolver.OPENAI_RESPONSES_URL)

    def raise_for_status(self):
        if self.status_code >= 400:
            raise ai_resolver.httpx.HTTPStatusError(
                "error",
                request=self.request,
                response=ai_resolver.httpx.Response(
                    self.status_code,
                    request=self.request,
                ),
            )
        return None

    def json(self):
        return self._payload


class _FakeClient:
    def __init__(self, payload):
        self.payload = payload
        self.requests = []

    def post(self, url, headers=None, json=None):
        self.requests.append((url, headers, json))
        return _FakeResponse(self.payload)

    def close(self):
        return None


class TestOpenAIResolver:
    def test_confident_selection_is_returned(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "output_text": json.dumps(
                    {
                        "decision": "select",
                        "selected_index": 2,
                        "confidence": 0.91,
                        "rationale": "Candidate 2 matches the BOM package hint.",
                        "missing_context": [],
                    }
                )
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
            description="Automotive sensor",
        )
        lookup = LookupResult(
            part={
                "ManufacturerPartNumber": "PARTA-Q1",
                "MouserPartNumber": "595-PARTA-Q1",
            },
            method=MatchMethod.FUZZY,
            candidate_count=2,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Option A",
                    },
                    score=10,
                ),
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTB-Q1",
                        "MouserPartNumber": "595-PARTB-Q1",
                        "Description": "Option B",
                    },
                    score=9,
                ),
            ),
        )

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.is_select is True
        assert decision.selected_index == 2
        assert fake_client.requests
        _, _, payload = fake_client.requests[0]
        assert payload["model"] == "gpt-5.4-mini"
        assert payload["reasoning"] == {"effort": "none"}
        assert payload["text"]["verbosity"] == "low"

    def test_low_confidence_selection_becomes_abstain(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "output_text": json.dumps(
                    {
                        "decision": "select",
                        "selected_index": 1,
                        "confidence": 0.42,
                        "rationale": "Weak preference for candidate 1.",
                        "missing_context": ["package"],
                    }
                )
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key", confidence_threshold=0.8)
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={
                "ManufacturerPartNumber": "PARTA-Q1",
                "MouserPartNumber": "595-PARTA-Q1",
            },
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.decision == "abstain"
        assert "below threshold" in decision.rationale

    def test_auth_failure_disables_future_requests(self, monkeypatch):
        fake_client = _FakeClient({},)
        fake_client.payload = {}

        def failing_post(url, headers=None, json=None):
            fake_client.requests.append((url, headers, json))
            return _FakeResponse({}, status_code=401)

        fake_client.post = failing_post
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={"ManufacturerPartNumber": "PARTA-Q1", "MouserPartNumber": "595-PARTA-Q1"},
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        first = resolver.rerank(agg, lookup)
        assert first is not None
        assert first.decision == "abstain"
        assert first.is_degraded is True
        assert first.emit_user_notice is True
        assert "authentication failed" in first.rationale.lower()

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.decision == "abstain"
        assert "fallback remains enabled" in decision.rationale.lower()
        assert decision.emit_user_notice is False
        assert len(fake_client.requests) == 1

    def test_reads_nested_message_output_text(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "status": "completed",
                "output": [
                    {"id": "rs_1", "type": "reasoning", "summary": []},
                    {
                        "id": "msg_1",
                        "type": "message",
                        "role": "assistant",
                        "status": "completed",
                        "content": [
                            {
                                "type": "output_text",
                                "text": json.dumps(
                                    {
                                        "decision": "abstain",
                                        "selected_index": 0,
                                        "confidence": 0.5,
                                        "rationale": "Need package context.",
                                        "missing_context": ["package"],
                                    }
                                ),
                            }
                        ],
                    },
                ],
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={"ManufacturerPartNumber": "PARTA-Q1", "MouserPartNumber": "595-PARTA-Q1"},
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.decision == "abstain"
        assert decision.missing_context == ("package",)

    def test_incomplete_response_becomes_degraded_abstain(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "status": "incomplete",
                "incomplete_details": {"reason": "max_output_tokens"},
                "output": [{"id": "rs_1", "type": "reasoning", "summary": []}],
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={"ManufacturerPartNumber": "PARTA-Q1", "MouserPartNumber": "595-PARTA-Q1"},
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.decision == "abstain"
        assert decision.is_degraded is True
        assert "response invalid" in decision.rationale.lower()
        assert "incomplete: max_output_tokens" in (decision.technical_details or "")

    def test_missing_decision_field_becomes_degraded_abstain(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "output_text": json.dumps(
                    {
                        "selected_index": 1,
                        "confidence": 0.9,
                        "rationale": "Looks good.",
                        "missing_context": [],
                    }
                )
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={"ManufacturerPartNumber": "PARTA-Q1", "MouserPartNumber": "595-PARTA-Q1"},
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        decision = resolver.rerank(agg, lookup)

        assert decision is not None
        assert decision.decision == "abstain"
        assert decision.is_degraded is True
        assert "response invalid" in decision.rationale.lower()
        assert "missing required fields: decision" in (decision.technical_details or "")

    def test_repeated_invalid_response_notice_is_suppressed(self, monkeypatch):
        fake_client = _FakeClient(
            {
                "output_text": json.dumps(
                    {
                        "selected_index": 1,
                        "confidence": 0.9,
                        "rationale": "Looks good.",
                        "missing_context": [],
                    }
                )
            }
        )
        monkeypatch.setattr(ai_resolver.httpx, "Client", lambda timeout: fake_client)

        resolver = OpenAIResolver(api_key="test-key")
        agg = AggregatedPart(
            part_number="PART-Q1",
            manufacturer="Texas Instruments",
            quantity_per_unit=1,
            total_quantity=100,
        )
        lookup = LookupResult(
            part={"ManufacturerPartNumber": "PARTA-Q1", "MouserPartNumber": "595-PARTA-Q1"},
            method=MatchMethod.FUZZY,
            candidate_count=1,
            review_required=True,
            candidates=(
                ScoredCandidate(
                    part={
                        "ManufacturerPartNumber": "PARTA-Q1",
                        "MouserPartNumber": "595-PARTA-Q1",
                        "Description": "Only option",
                    },
                    score=10,
                ),
            ),
        )

        first = resolver.rerank(agg, lookup)
        second = resolver.rerank(agg, lookup)

        assert first is not None
        assert second is not None
        assert first.emit_user_notice is True
        assert second.emit_user_notice is False
