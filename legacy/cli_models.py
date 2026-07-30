"""Versioned machine-interface models for the BOM Builder CLI.

The pricing engine uses domain models from :mod:`models`. This module wraps
those models in stable command-level envelopes so automation can distinguish
successful pricing, partial results, review requirements, and provider
degradation without parsing terminal prose.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any, Literal

from pydantic import BaseModel, Field

from config import PROJECT_VERSION
from models import BomSummary, PricedPart

CLI_SCHEMA_VERSION = "1.0"


class CliIssue(BaseModel):
    """One stable warning or error emitted by a machine-facing command."""

    code: str = Field(description="Stable machine-readable issue code")
    message: str = Field(description="Human-readable issue summary")
    part_number: str | None = None
    provider: str | None = None
    details: dict[str, Any] = Field(default_factory=dict)


class ProviderRunSummary(BaseModel):
    """Observed outcome for one selected provider during a pricing run."""

    name: str
    status: Literal["ok", "degraded", "no_results", "not_applicable"]
    applicable_parts: int = Field(ge=0)
    offers_returned: int = Field(ge=0)
    priced_offers: int = Field(ge=0)
    error_count: int = Field(ge=0)


class RunMetadata(BaseModel):
    """Execution metadata attached to a pricing result envelope."""

    run_id: str
    command: Literal["price", "lookup"]
    version: str = PROJECT_VERSION
    started_at: datetime
    completed_at: datetime
    duration_seconds: float = Field(ge=0)
    selected_providers: list[str]


class PricingResultEnvelope(BaseModel):
    """Stable top-level JSON result returned by ``price`` and ``lookup``."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["complete", "complete_with_review", "partial", "failed"]
    exit_code: int
    run: RunMetadata
    summary: BomSummary
    providers: list[ProviderRunSummary]
    parts: list[PricedPart]
    warnings: list[CliIssue] = Field(default_factory=list)
    errors: list[CliIssue] = Field(default_factory=list)


class CommandErrorEnvelope(BaseModel):
    """Stable JSON error emitted when a command cannot produce normal output."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["failed"] = "failed"
    exit_code: int
    command: str
    errors: list[CliIssue]


class ValidationResultEnvelope(BaseModel):
    """Machine-readable result returned by the ``validate`` command."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["valid"] = "valid"
    exit_code: int = 0
    design_count: int = Field(ge=0)
    part_count: int = Field(ge=0)
    designs: list[str]


class ProviderHealthResult(BaseModel):
    """Configuration and optional live status for one external provider."""

    name: str
    kind: Literal["distributor", "service"]
    configured: bool
    status: Literal["configured", "not_configured", "ok", "failed"]
    live: bool
    latency_ms: float | None = Field(default=None, ge=0)
    request_count: int | None = Field(default=None, ge=0)
    details: dict[str, Any] = Field(default_factory=dict)
    error_code: str | None = None
    error: str | None = None


class ProviderHealthEnvelope(BaseModel):
    """Top-level JSON document returned by provider discovery/check commands."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["ok", "degraded", "failed"]
    exit_code: int
    live: bool
    providers: list[ProviderHealthResult]


class CapabilitySchemas(BaseModel):
    """Public JSON Schemas bundled into full capability discovery output."""

    input: dict[str, Any]
    output: dict[str, Any]
    providers: dict[str, Any]


class CapabilitiesEnvelope(BaseModel):
    """Machine-readable discovery document for the installed CLI."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["ok"] = "ok"
    exit_code: int = 0
    version: str = PROJECT_VERSION
    commands: list[str]
    distributors: list[str]
    services: list[str]
    artifact_formats: list[str]
    fail_on_policies: list[str]
    features: dict[str, bool]
    provider_configuration: ProviderHealthEnvelope | None = None
    schemas: CapabilitySchemas | None = None


class MaintenanceTarget(BaseModel):
    """One exact filesystem target shown by a maintenance command."""

    path: str
    exists: bool
    size_bytes: int | None = Field(default=None, ge=0)


class MaintenanceEnvelope(BaseModel):
    """Result returned by cache and saved-resolution maintenance commands."""

    schema_version: str = CLI_SCHEMA_VERSION
    status: Literal["ok", "preview", "failed"]
    exit_code: int
    resource: Literal["cache", "resolutions"]
    action: str
    confirmed: bool
    targets: list[MaintenanceTarget] = Field(default_factory=list)
    records: list[dict[str, Any]] = Field(default_factory=list)
    affected_count: int = Field(default=0, ge=0)
    errors: list[CliIssue] = Field(default_factory=list)
