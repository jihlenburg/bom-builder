"""Digi-Key OAuth helpers and account-discovery utilities.

This module is the first reusable building block for Digi-Key integration in
the BOM Builder pipeline. The immediate use case is operational setup rather
than pricing: Digi-Key's Product Information V4 endpoints increasingly expect
an ``X-DIGIKEY-Account-ID`` header, but the easiest official way to discover
that value is a one-time 3-legged OAuth flow against Digi-Key's
``AssociatedAccounts`` reference API.

The helpers here intentionally separate three concerns:

1. building the browser authorization URL for the user-facing login/consent
   step
2. exchanging the returned authorization code for access tokens
3. querying the ``AssociatedAccounts`` endpoint and normalizing the returned
   account metadata

Keeping that logic in a dedicated module makes it easy to reuse later when the
runtime Digi-Key distributor client needs token handling, account-aware pricing,
or future customer-resource lookups.
"""

from __future__ import annotations

from dataclasses import dataclass
import secrets
from typing import Any
from urllib.parse import parse_qs, urlencode, urlparse

import httpx

from secret_store import get_secret

DIGIKEY_OAUTH_AUTHORIZE_URL = "https://api.digikey.com/v1/oauth2/authorize"
DIGIKEY_OAUTH_TOKEN_URL = "https://api.digikey.com/v1/oauth2/token"
DIGIKEY_ASSOCIATED_ACCOUNTS_URL = (
    "https://api.digikey.com/CustomerResource/v1/associatedaccounts"
)


@dataclass(frozen=True)
class DigiKeyTokens:
    """Access-token bundle returned by Digi-Key's OAuth token endpoint.

    Attributes
    ----------
    access_token:
        Bearer token used in the ``Authorization`` header for subsequent API
        calls.
    token_type:
        Expected token type, typically ``"Bearer"``.
    expires_in:
        Access-token lifetime in seconds as returned by Digi-Key. The docs are
        inconsistent across pages, so callers should trust this runtime value.
    refresh_token:
        Optional refresh token returned by the authorization-code exchange.
    refresh_token_expires_in:
        Optional refresh-token lifetime in seconds.
    scope:
        Optional returned scope string.
    """

    access_token: str
    token_type: str
    expires_in: int
    refresh_token: str | None = None
    refresh_token_expires_in: int | None = None
    scope: str | None = None


@dataclass(frozen=True)
class DigiKeyAccount:
    """One Digi-Key account association returned by ``AssociatedAccounts``.

    Attributes
    ----------
    account_id:
        Digi-Key Account ID used by account-aware product and pricing APIs.
    company_name:
        Company name from the account address when present.
    country_code:
        Country code from the account address when present.
    postal_code:
        Postal code from the account address when present.
    city:
        City from the account address when present.
    """

    account_id: int
    company_name: str | None = None
    country_code: str | None = None
    postal_code: str | None = None
    city: str | None = None


def resolve_digikey_client_credentials(
    client_id: str = "",
    client_secret: str = "",
) -> tuple[str, str]:
    """Resolve Digi-Key OAuth credentials from CLI overrides or ``.env``.

    Parameters
    ----------
    client_id:
        Optional explicit Digi-Key client ID override.
    client_secret:
        Optional explicit Digi-Key client secret override.

    Returns
    -------
    tuple[str, str]
        Resolved ``(client_id, client_secret)`` pair.

    Raises
    ------
    ValueError
        If either credential is missing after environment resolution.
    """
    resolved_id = client_id.strip() or get_secret("digikey_client_id")
    resolved_secret = client_secret.strip() or get_secret("digikey_client_secret")
    if not resolved_id or not resolved_secret:
        raise ValueError(
            "Digi-Key client credentials not set. Configure DIGIKEY_CLIENT_ID and "
            "DIGIKEY_CLIENT_SECRET in .env or pass them explicitly."
        )
    return resolved_id, resolved_secret


def generate_oauth_state() -> str:
    """Return a random state token for the 3-legged OAuth flow."""
    return secrets.token_urlsafe(24)


def build_authorization_url(
    client_id: str,
    redirect_uri: str,
    state: str,
) -> str:
    """Build the Digi-Key authorization URL for browser-based login/consent.

    Parameters
    ----------
    client_id:
        Registered Digi-Key OAuth client ID.
    redirect_uri:
        Redirect URI that exactly matches the Digi-Key app registration.
    state:
        Opaque CSRF-protection token that will be echoed back in the callback.

    Returns
    -------
    str
        Fully encoded authorization URL ready to open in a browser.
    """
    return (
        f"{DIGIKEY_OAUTH_AUTHORIZE_URL}?"
        + urlencode(
            {
                "response_type": "code",
                "client_id": client_id,
                "redirect_uri": redirect_uri,
                "state": state,
            }
        )
    )


def extract_authorization_code(callback_input: str) -> tuple[str, str | None]:
    """Extract an authorization code from a callback URL or raw pasted code.

    Parameters
    ----------
    callback_input:
        Either the full redirected URL from the browser address bar or just the
        raw ``code`` value copied out of that URL.

    Returns
    -------
    tuple[str, str | None]
        Parsed ``(authorization_code, returned_state)`` pair.

    Raises
    ------
    ValueError
        If the callback contains an OAuth error or no authorization code can be
        extracted.
    """
    raw = callback_input.strip()
    if not raw:
        raise ValueError("No callback URL or authorization code was provided.")

    if "://" not in raw and "code=" not in raw and "error=" not in raw:
        return raw, None

    parsed = urlparse(raw)
    params = parse_qs(parsed.query)
    error = _first_query_value(params, "error")
    if error:
        description = _first_query_value(params, "error_description") or "unknown OAuth error"
        raise ValueError(f"Digi-Key authorization failed: {error} ({description})")

    code = _first_query_value(params, "code")
    if not code:
        raise ValueError(
            "Could not find a Digi-Key authorization code in the pasted callback URL."
        )
    return code, _first_query_value(params, "state")


def exchange_authorization_code(
    client_id: str,
    client_secret: str,
    code: str,
    redirect_uri: str,
    *,
    timeout_seconds: float = 30.0,
) -> DigiKeyTokens:
    """Exchange a Digi-Key authorization code for access and refresh tokens.

    Parameters
    ----------
    client_id:
        Registered Digi-Key OAuth client ID.
    client_secret:
        Registered Digi-Key OAuth client secret.
    code:
        Short-lived authorization code returned from the browser redirect.
    redirect_uri:
        Redirect URI originally used in the authorization request.
    timeout_seconds:
        HTTP timeout for the token exchange request.

    Returns
    -------
    DigiKeyTokens
        Normalized token bundle returned by Digi-Key.
    """
    payload = {
        "grant_type": "authorization_code",
        "code": code,
        "client_id": client_id,
        "client_secret": client_secret,
        "redirect_uri": redirect_uri,
    }
    with httpx.Client(timeout=timeout_seconds) as client:
        response = client.post(DIGIKEY_OAUTH_TOKEN_URL, data=payload)
        response.raise_for_status()
        data = response.json()
    return DigiKeyTokens(
        access_token=str(data["access_token"]),
        token_type=str(data.get("token_type") or "Bearer"),
        expires_in=int(data.get("expires_in") or 0),
        refresh_token=_optional_str(data, "refresh_token"),
        refresh_token_expires_in=_optional_int(data, "refresh_token_expires_in"),
        scope=_optional_str(data, "scope"),
    )


def fetch_associated_accounts(
    access_token: str,
    client_id: str,
    *,
    timeout_seconds: float = 30.0,
) -> tuple[list[DigiKeyAccount], str | None]:
    """Fetch Digi-Key Account IDs associated with the authenticated user.

    Parameters
    ----------
    access_token:
        Bearer token from :func:`exchange_authorization_code`.
    client_id:
        Registered Digi-Key OAuth client ID sent in ``X-DIGIKEY-Client-Id``.
    timeout_seconds:
        HTTP timeout for the API request.

    Returns
    -------
    tuple[list[DigiKeyAccount], str | None]
        Normalized associated-account records plus the email address Digi-Key
        reports as the identity used for the lookup.
    """
    headers = {
        "Authorization": f"Bearer {access_token}",
        "X-DIGIKEY-Client-Id": client_id,
    }
    with httpx.Client(timeout=timeout_seconds) as client:
        response = client.get(DIGIKEY_ASSOCIATED_ACCOUNTS_URL, headers=headers)
        response.raise_for_status()
        data = response.json()
    return parse_associated_accounts_response(data)


def parse_associated_accounts_response(
    payload: dict[str, Any],
) -> tuple[list[DigiKeyAccount], str | None]:
    """Normalize the ``AssociatedAccounts`` API response payload.

    Parameters
    ----------
    payload:
        Raw decoded JSON object returned by Digi-Key.

    Returns
    -------
    tuple[list[DigiKeyAccount], str | None]
        Parsed account list and optional email address reported by Digi-Key.
    """
    raw_accounts = payload.get("Accounts")
    accounts: list[DigiKeyAccount] = []
    if isinstance(raw_accounts, list):
        for raw in raw_accounts:
            if not isinstance(raw, dict):
                continue
            account_id = raw.get("AccountId")
            if account_id is None:
                continue
            address = raw.get("Address")
            address_data = address if isinstance(address, dict) else {}
            accounts.append(
                DigiKeyAccount(
                    account_id=int(account_id),
                    company_name=_optional_str(address_data, "CompanyName"),
                    country_code=_optional_str(address_data, "CountryCode"),
                    postal_code=_optional_str(address_data, "PostalCode"),
                    city=_optional_str(address_data, "City"),
                )
            )

    return accounts, _optional_str(payload, "EmailAddressUsed")


def _first_query_value(params: dict[str, list[str]], key: str) -> str | None:
    """Return the first value for one parsed query-string key when present."""
    values = params.get(key)
    if not values:
        return None
    value = values[0].strip()
    return value or None


def _optional_str(payload: dict[str, Any], key: str) -> str | None:
    """Return a stripped string field from a decoded JSON object when present."""
    value = payload.get(key)
    if value is None:
        return None
    text = str(value).strip()
    return text or None


def _optional_int(payload: dict[str, Any], key: str) -> int | None:
    """Return an integer field from a decoded JSON object when present."""
    value = payload.get(key)
    if value in (None, ""):
        return None
    return int(value)
