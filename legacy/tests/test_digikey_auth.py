"""Tests for Digi-Key OAuth helpers and account parsing."""

import importlib
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import digikey_auth
import secret_store


@pytest.fixture(autouse=True)
def clear_env(monkeypatch):
    monkeypatch.delenv("DIGIKEY_CLIENT_ID", raising=False)
    monkeypatch.delenv("DIGIKEY_CLIENT_SECRET", raising=False)


class TestCredentials:
    def test_resolves_from_environment(self, monkeypatch, tmp_path):
        env_file = tmp_path / ".env"
        env_file.write_text(
            "DIGIKEY_CLIENT_ID=dotenv-id\nDIGIKEY_CLIENT_SECRET=dotenv-secret\n",
            encoding="utf-8",
        )
        monkeypatch.chdir(tmp_path)
        importlib.reload(secret_store)
        reloaded = importlib.reload(digikey_auth)

        assert reloaded.resolve_digikey_client_credentials() == (
            "dotenv-id",
            "dotenv-secret",
        )

    def test_requires_both_credentials(self):
        with pytest.raises(ValueError):
            digikey_auth.resolve_digikey_client_credentials()


class TestAuthorizationUrl:
    def test_builds_expected_query_parameters(self):
        url = digikey_auth.build_authorization_url(
            "client-123",
            "https://localhost",
            "state-456",
        )

        assert "response_type=code" in url
        assert "client_id=client-123" in url
        assert "redirect_uri=https%3A%2F%2Flocalhost" in url
        assert "state=state-456" in url


class TestExtractAuthorizationCode:
    def test_accepts_raw_code(self):
        code, state = digikey_auth.extract_authorization_code("abc123")

        assert code == "abc123"
        assert state is None

    def test_extracts_code_and_state_from_callback_url(self):
        code, state = digikey_auth.extract_authorization_code(
            "https://localhost?code=abc123&state=state-456"
        )

        assert code == "abc123"
        assert state == "state-456"

    def test_raises_for_oauth_error(self):
        with pytest.raises(ValueError, match="authorization failed"):
            digikey_auth.extract_authorization_code(
                "https://localhost?error=access_denied&error_description=nope"
            )

    def test_raises_when_code_missing(self):
        with pytest.raises(ValueError, match="authorization code"):
            digikey_auth.extract_authorization_code("https://localhost?state=only")


class TestAssociatedAccountsParsing:
    def test_parses_accounts_and_email(self):
        accounts, email = digikey_auth.parse_associated_accounts_response(
            {
                "EmailAddressUsed": "user@example.com",
                "Accounts": [
                    {
                        "AccountId": 12345,
                        "Address": {
                            "CompanyName": "Example Corp",
                            "CountryCode": "US",
                            "PostalCode": "12345",
                            "City": "Austin",
                        },
                    }
                ],
            }
        )

        assert email == "user@example.com"
        assert len(accounts) == 1
        assert accounts[0].account_id == 12345
        assert accounts[0].company_name == "Example Corp"
        assert accounts[0].country_code == "US"
        assert accounts[0].postal_code == "12345"
        assert accounts[0].city == "Austin"

    def test_ignores_malformed_account_entries(self):
        accounts, email = digikey_auth.parse_associated_accounts_response(
            {
                "Accounts": [
                    None,
                    {"Address": {"CompanyName": "Missing ID"}},
                    {"AccountId": 5},
                ]
            }
        )

        assert email is None
        assert [account.account_id for account in accounts] == [5]
