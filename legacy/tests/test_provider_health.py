"""Tests for safe provider configuration and live-health normalization."""

from __future__ import annotations

from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).parent.parent))

import provider_health


def test_configuration_never_contains_credential_values(monkeypatch):
    """Provider discovery reports presence/counts without exposing secrets."""
    monkeypatch.setattr(
        provider_health,
        "get_secret_values",
        lambda name: ("super-secret-primary", "super-secret-backup")
        if name == "mouser_api_keys"
        else (),
    )
    monkeypatch.setattr(
        provider_health,
        "get_secret",
        lambda name: "another-secret" if name == "openai_api_key" else "",
    )

    configuration = provider_health.provider_configuration()
    serialized = str(
        {
            name: result.model_dump(mode="json")
            for name, result in configuration.items()
        }
    )

    assert "super-secret-primary" not in serialized
    assert "super-secret-backup" not in serialized
    assert "another-secret" not in serialized
    assert configuration["mouser"].details["credential_count"] == 2
    assert configuration["openai"].configured is True


def test_live_failure_is_sanitized(monkeypatch):
    """Raw exception text and request URLs do not reach health JSON."""

    def fail():
        raise RuntimeError("https://provider.invalid?apiKey=secret-value")

    monkeypatch.setattr(
        provider_health,
        "_live_check_registry",
        lambda: {"mouser": fail},
    )
    configured = provider_health.ProviderHealthResult(
        name="mouser",
        kind="distributor",
        configured=True,
        status="configured",
        live=False,
    )

    result = provider_health._run_live_check("mouser", configured)
    serialized = result.model_dump_json()

    assert result.status == "failed"
    assert result.error == "RuntimeError"
    assert "secret-value" not in serialized
    assert "provider.invalid" not in serialized
