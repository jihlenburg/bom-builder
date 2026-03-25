"""Tests for simple environment-backed secret loading."""

import importlib
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import secret_store


@pytest.fixture(autouse=True)
def clear_env(monkeypatch):
    monkeypatch.delenv("MOUSER_API_KEY", raising=False)
    monkeypatch.delenv("MOUSER_API_KEYS", raising=False)
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)
    monkeypatch.delenv("DIGIKEY_CLIENT_ID", raising=False)
    monkeypatch.delenv("DIGIKEY_CLIENT_SECRET", raising=False)
    monkeypatch.delenv("DIGIKEY_ACCOUNT_ID", raising=False)
    monkeypatch.delenv("TI_STORE_API_KEY", raising=False)
    monkeypatch.delenv("TI_STORE_API_SECRET", raising=False)
    monkeypatch.delenv("TI_PRODUCT_API_KEY", raising=False)
    monkeypatch.delenv("TI_PRODUCT_API_SECRET", raising=False)


class TestGetSecret:
    def test_existing_environment_overrides_dotenv(self, monkeypatch, tmp_path):
        env_file = tmp_path / ".env"
        env_file.write_text("MOUSER_API_KEY=dotenv-value\n", encoding="utf-8")
        monkeypatch.setenv("MOUSER_API_KEY", "env-value")
        monkeypatch.chdir(tmp_path)
        reloaded = importlib.reload(secret_store)

        assert reloaded.get_secret("mouser_api_key") == "env-value"

    def test_reads_from_dotenv_when_env_missing(self, monkeypatch, tmp_path):
        env_file = tmp_path / ".env"
        env_file.write_text("OPENAI_API_KEY=dotenv-value\n", encoding="utf-8")
        monkeypatch.chdir(tmp_path)
        reloaded = importlib.reload(secret_store)

        assert reloaded.get_secret("openai_api_key") == "dotenv-value"

    def test_returns_default_when_missing(self):
        assert secret_store.get_secret("openai_api_key", default="fallback") == "fallback"

    def test_parses_multiple_secret_values(self, monkeypatch):
        monkeypatch.setenv(
            "MOUSER_API_KEYS",
            " key-one , key-two ; key-three\nkey-four ",
        )

        assert secret_store.get_secret_values("mouser_api_keys") == (
            "key-one",
            "key-two",
            "key-three",
            "key-four",
        )


class TestSpecs:
    def test_registry_lists_known_api_keys(self):
        names = {spec.name for spec in secret_store.list_secret_specs()}
        assert "mouser_api_key" in names
        assert "mouser_api_keys" in names
        assert "openai_api_key" in names
        assert "digikey_client_id" in names
        assert "digikey_client_secret" in names
        assert "digikey_account_id" in names
        assert "ti_store_api_key" in names
        assert "ti_store_api_secret" in names
        assert "ti_product_api_key" in names
        assert "ti_product_api_secret" in names

    def test_registry_contains_expected_env_var(self):
        spec = secret_store.get_secret_spec("mouser_api_key")
        assert spec.env_var == "MOUSER_API_KEY"

    def test_registry_contains_expected_digikey_env_vars(self):
        client_id = secret_store.get_secret_spec("digikey_client_id")
        client_secret = secret_store.get_secret_spec("digikey_client_secret")
        account_id = secret_store.get_secret_spec("digikey_account_id")

        assert client_id.env_var == "DIGIKEY_CLIENT_ID"
        assert client_secret.env_var == "DIGIKEY_CLIENT_SECRET"
        assert account_id.env_var == "DIGIKEY_ACCOUNT_ID"

    def test_registry_contains_expected_ti_store_env_vars(self):
        api_key = secret_store.get_secret_spec("ti_store_api_key")
        api_secret = secret_store.get_secret_spec("ti_store_api_secret")

        assert api_key.env_var == "TI_STORE_API_KEY"
        assert api_secret.env_var == "TI_STORE_API_SECRET"

    def test_registry_contains_expected_legacy_ti_env_vars(self):
        api_key = secret_store.get_secret_spec("ti_product_api_key")
        api_secret = secret_store.get_secret_spec("ti_product_api_secret")

        assert api_key.env_var == "TI_PRODUCT_API_KEY"
        assert api_secret.env_var == "TI_PRODUCT_API_SECRET"
